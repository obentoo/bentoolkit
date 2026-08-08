package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// captureCompareReviewWarnings redirects the one warning this file is allowed to
// emit and returns an accessor for what was said.
//
// It exists because logger binds its io.Writer at first use and exposes no
// setter (logger.go:44-52), so the alternative is reading the process's stderr —
// which the rest of the suite writes to concurrently. The same shape
// internal/overlay uses for warnLogf, for the same reason.
func captureCompareReviewWarnings(t *testing.T) func() []string {
	t.Helper()
	var lines []string
	previous := reviewWarnf
	reviewWarnf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { reviewWarnf = previous })
	return func() []string { return lines }
}

// stubClaudeAsker replaces the CLI construction seam for the duration of a test
// and returns a counter of how many times construction was ATTEMPTED.
//
// The count is the whole point for R5.6: "contact no model" is a claim about a
// process that never starts, and the only way to assert a thing did not happen
// is to have a seam that would record it if it had.
func stubClaudeAsker(t *testing.T, build func() (claudeAsker, error)) func() int {
	t.Helper()
	attempts := 0
	previous := newClaudeAsker
	newClaudeAsker = func(context.Context) (claudeAsker, error) {
		attempts++
		return build()
	}
	t.Cleanup(func() { newClaudeAsker = previous })
	return func() int { return attempts }
}

// askCall is one invocation of the CLI as the adapter issued it: what went in
// -p (argv), what went on stdin, and what schema constrained the reply.
type askCall struct {
	instruction string
	content     []byte
	schema      string
}

// fakeAsker answers with a canned reply and records every call, so the two
// halves of the adapter — what it sends and what it makes of what comes back —
// can be asserted separately.
type fakeAsker struct {
	reply  string
	err    error
	answer func(askCall) (string, error)

	calls []askCall
}

func (f *fakeAsker) AskJSON(instruction string, content []byte, schema string) (string, error) {
	call := askCall{instruction: instruction, content: append([]byte(nil), content...), schema: schema}
	f.calls = append(f.calls, call)
	if f.answer != nil {
		return f.answer(call)
	}
	return f.reply, f.err
}

var _ claudeAsker = (*fakeAsker)(nil)

// reviewerOver builds the production adapter over a scripted asker, through the
// production constructor, so a test never assembles a shape production cannot
// produce.
func reviewerOver(t *testing.T, asker claudeAsker) overlay.DivergenceReviewer {
	t.Helper()
	stubClaudeAsker(t, func() (claudeAsker, error) { return asker, nil })
	reviewer, err := newDivergenceReviewer(context.Background())
	if err != nil {
		t.Fatalf("newDivergenceReviewer returned %v, want nil", err)
	}
	if reviewer == nil {
		t.Fatal("newDivergenceReviewer returned no reviewer over a constructible asker")
	}
	return reviewer
}

// The two ebuilds one finding compares: ours carries a patch ::gentoo does not
// ship, which is the live shape of a divergence that is ours
// (kde-plasma/spectacle-6.7.4).
const (
	cmdReviewOurs   = "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n"
	cmdReviewTheirs = "EAPI=8\ninherit ecm\n"
)

// cmdReviewRequest is the request the adapter is handed unless a subtest varies
// part of it.
func cmdReviewRequest() overlay.ReviewRequest {
	return overlay.ReviewRequest{
		Category: "kde-plasma",
		Package:  "spectacle",
		Version:  "6.7.4",
		Ours:     []byte(cmdReviewOurs),
		Theirs:   []byte(cmdReviewTheirs),
	}
}

// reviewDirProvider is a Provider whose compared repository is a directory on
// disk — the shape `provider: local` and `--clone` both produce, and the only
// shape resolvePackagePaths can re-read two ebuilds from.
type reviewDirProvider struct {
	root     string
	versions map[string][]string
}

func (p *reviewDirProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	v, ok := p.versions[category+"/"+pkg]
	if !ok {
		return nil, provider.ErrNotFound
	}
	return v, nil
}

func (p *reviewDirProvider) GetName() string   { return "fake-local" }
func (p *reviewDirProvider) SupportsAPI() bool { return false }
func (p *reviewDirProvider) Close() error      { return nil }
func (p *reviewDirProvider) LocalPackagePath(category, pkg string) (string, error) {
	return filepath.Join(p.root, category, pkg), nil
}

var (
	_ provider.Provider           = (*reviewDirProvider)(nil)
	_ provider.PackageDirProvider = (*reviewDirProvider)(nil)
)

func writeReviewEbuild(t *testing.T, root, category, pkg, version, body string) {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, pkg+"-"+version+".ebuild"), []byte(body), 0o600); err != nil {
		t.Fatalf("write ebuild: %v", err)
	}
}

// annotateFixture compares one package that carries an undeclared divergence and
// returns the finished report with the provider and options it came from — the
// same three values runCompare hands AnnotateReviews.
//
// HOME is redirected because AnnotateReviews resolves its note cache from
// os.UserHomeDir(): without this the suite would read and write the developer's
// real ~/.cache/bentoo/compare, where one run's stored note would answer the
// next run's assertion about what a reviewer returned.
func annotateFixture(t *testing.T) (*overlay.CompareReport, provider.Provider, overlay.CompareOptions) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	writeReviewEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4", cmdReviewOurs)
	writeReviewEbuild(t, upstreamRoot, "kde-plasma", "spectacle", "6.7.4", cmdReviewTheirs)

	prov := &reviewDirProvider{root: upstreamRoot, versions: map[string][]string{
		"kde-plasma/spectacle": {"6.7.4"},
	}}
	opts := overlay.CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence:    map[string]overlay.Divergence{"kde-plasma/spectacle": {}},
	}

	report, err := overlay.CompareWithProvider([]overlay.PackageInfo{
		{Category: "kde-plasma", Package: "spectacle", LatestVersion: "6.7.4"},
	}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != 1 || report.Results[0].Verified != overlay.VerifiedDiffers || report.Results[0].Patched {
		t.Fatalf("the fixture must hold exactly one undeclared divergence, got %+v", report.Results)
	}
	return report, prov, opts
}

// TestNewDivergenceReviewer is 6.5's construction half: the reviewer exists when
// the CLI does, absence is not a failure, and `--no-review` reaches no seam at
// all.
//
// _Requirements: R5.5, R5.6_
func TestNewDivergenceReviewer(t *testing.T) {
	t.Run("an absent claude CLI yields no reviewer and no error", func(t *testing.T) {
		warnings := captureCompareReviewWarnings(t)
		stubClaudeAsker(t, func() (claudeAsker, error) { return nil, autoupdate.ErrClaudeCodeUnavailable })

		reviewer, err := newDivergenceReviewer(context.Background())
		if err != nil {
			t.Fatalf("newDivergenceReviewer returned %v, want nil: an absent CLI is a machine without the reviewer, not a failed run (R5.5)", err)
		}
		if reviewer != nil {
			t.Fatalf("newDivergenceReviewer returned %#v, want nil", reviewer)
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("an absent CLI warned %q; there is nothing wrong to report", lines)
		}
	})

	t.Run("the nil it returns is a nil INTERFACE, not a boxed nil pointer", func(t *testing.T) {
		stubClaudeAsker(t, func() (claudeAsker, error) { return nil, autoupdate.ErrClaudeCodeUnavailable })

		reviewer, _ := newDivergenceReviewer(context.Background())
		// `reviewer != nil` above is already this assertion; reflect states it
		// again in the terms that make the failure legible. A boxed
		// (*claudeDivergenceReviewer)(nil) is a NON-nil interface with a nil
		// dynamic value, so AnnotateReviews's `reviewer == nil` guard would let it
		// through and the first call would dereference nothing.
		if v := reflect.ValueOf(reviewer); v.IsValid() {
			t.Fatalf("newDivergenceReviewer boxed a %s into the interface; AnnotateReviews's nil guard cannot see through that and the first review would panic", v.Type())
		}

		// The same claim stated where it costs a panic to be wrong: the whole
		// annotate pass, run with what the constructor returned.
		report, prov, opts := annotateFixture(t)
		overlay.AnnotateReviews(report, reviewer, prov, opts)
		if got := report.Results[0].Review; got != (overlay.ReviewNote{}) {
			t.Errorf("a run with no reviewer annotated %+v, want the zero note", got)
		}
	})

	t.Run("any other construction failure warns and still yields no reviewer", func(t *testing.T) {
		warnings := captureCompareReviewWarnings(t)
		broken := errors.New("the secrets file is unreadable")
		attempts := stubClaudeAsker(t, func() (claudeAsker, error) { return nil, broken })

		if reviewer, err := newDivergenceReviewer(context.Background()); reviewer != nil || !errors.Is(err, broken) {
			t.Fatalf("newDivergenceReviewer returned (%#v, %v), want (nil, the construction error)", reviewer, err)
		}

		reviewer := compareDivergenceReviewer(context.Background(), false)
		if reviewer != nil {
			t.Fatalf("compareDivergenceReviewer returned %#v after a failed construction, want nil", reviewer)
		}
		lines := warnings()
		if len(lines) != 1 {
			t.Fatalf("a failed construction produced %d warnings (%q), want exactly one", len(lines), lines)
		}
		if !strings.Contains(lines[0], broken.Error()) {
			t.Errorf("the warning %q does not say why the reviewer could not be built", lines[0])
		}
		if attempts() != 2 {
			t.Errorf("the CLI seam was reached %d times, want 2 (once per call above)", attempts())
		}
	})

	t.Run("--no-review constructs nothing at all", func(t *testing.T) {
		warnings := captureCompareReviewWarnings(t)
		attempts := stubClaudeAsker(t, func() (claudeAsker, error) {
			t.Error("--no-review reached the CLI construction seam; R5.6 is that no model is contacted, which starts with nothing being built")
			return nil, errors.New("unreachable")
		})

		reviewer := compareDivergenceReviewer(context.Background(), true)
		if reviewer != nil {
			t.Fatalf("--no-review returned %#v, want nil", reviewer)
		}
		if attempts() != 0 {
			t.Errorf("--no-review attempted construction %d times, want 0", attempts())
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("--no-review warned %q; asking for no review is not a failure", lines)
		}
	})

	t.Run("a constructible CLI yields a reviewer", func(t *testing.T) {
		warnings := captureCompareReviewWarnings(t)
		attempts := stubClaudeAsker(t, func() (claudeAsker, error) { return &fakeAsker{reply: `{"origin":"upstream","summary":"s"}`}, nil })

		reviewer := compareDivergenceReviewer(context.Background(), false)
		if reviewer == nil {
			t.Fatal("compareDivergenceReviewer returned nil over a constructible CLI")
		}
		if attempts() != 1 {
			t.Errorf("construction was attempted %d times, want 1", attempts())
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("a successful construction warned %q", lines)
		}
	})

	t.Run("the CLI is asked for no API key, so none can leak", func(t *testing.T) {
		// The subscription shape (design.md, Security): api_key_env empty means
		// NewClaudeCodeClient resolves no credential and resolveBare stays false,
		// so childEnv SCRUBS any inherited ANTHROPIC_API_KEY from the spawned
		// process. Asserted on the config this file builds rather than on the
		// spawned environment, because that is where the decision is made.
		cfg := reviewLLMConfig()
		if cfg.APIKeyEnv != "" {
			t.Errorf("the review config names api_key_env %q; the review runs on the CLI's own subscription and must put no key on that path", cfg.APIKeyEnv)
		}
		if cfg.Bare != "" && cfg.Bare != "false" {
			t.Errorf("the review config asks for bare mode %q, which injects a credential into the child environment", cfg.Bare)
		}
	})
}

// TestDivergenceReviewSchema pins the schema to the type it describes: the
// vocabulary the CLI is constrained to is the vocabulary ReviewNote decodes, in
// both directions.
//
// _Requirements: R5.2, R5.3, R5.4_
func TestDivergenceReviewSchema(t *testing.T) {
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(divergenceReviewSchema), &schema); err != nil {
		t.Fatalf("the schema handed to --json-schema is not valid JSON: %v", err)
	}

	// Every field ReviewNote holds is a property the model is asked for, named by
	// the struct's own json tag. Read from the TYPE so a field added next door
	// cannot quietly go unasked-for.
	noteType := reflect.TypeOf(overlay.ReviewNote{})
	for i := 0; i < noteType.NumField(); i++ {
		tag := noteType.Field(i).Tag.Get("json")
		if tag == "" {
			t.Fatalf("ReviewNote.%s carries no json tag; the schema and the decoder share those names", noteType.Field(i).Name)
		}
		if _, ok := schema.Properties[tag]; !ok {
			t.Errorf("the schema does not ask for %q, which ReviewNote holds", tag)
		}
	}
	if len(schema.Properties) != noteType.NumField() {
		t.Errorf("the schema declares %d properties for a ReviewNote of %d fields", len(schema.Properties), noteType.NumField())
	}

	// The origin enum is exactly the four words ReviewOrigin round-trips. Built
	// from the type rather than copied, because the cache stores one of these
	// with no expiry: a word the schema allowed and UnmarshalText rejects would
	// be a reply thrown away on every run, forever.
	want := []string{
		overlay.OriginUnknown.String(),
		overlay.OriginOverlay.String(),
		overlay.OriginUpstream.String(),
		overlay.OriginBoth.String(),
	}
	got := append([]string(nil), schema.Properties["origin"].Enum...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the schema constrains origin to %v, want %v", got, want)
	}
}

// TestReviewDivergenceRequest is what the adapter SENDS: both ebuilds on stdin,
// nothing of them in argv, and nothing anywhere that derives from the size of
// the difference (R1.3).
//
// _Requirements: R5.1, R5.2_
func TestReviewDivergenceRequest(t *testing.T) {
	asker := &fakeAsker{reply: `{"origin":"overlay","summary":"adds a patch"}`}
	reviewer := reviewerOver(t, asker)
	req := cmdReviewRequest()

	if _, err := reviewer.ReviewDivergence(context.Background(), req); err != nil {
		t.Fatalf("ReviewDivergence returned %v, want nil", err)
	}
	if len(asker.calls) != 1 {
		t.Fatalf("one review issued %d CLI calls, want 1", len(asker.calls))
	}
	call := asker.calls[0]

	// R5.1: the model is shown the two files, whole. Anything less and its
	// reading is of something other than the divergence the report found.
	if !strings.Contains(string(call.content), cmdReviewOurs) {
		t.Errorf("the piped payload does not carry our ebuild verbatim:\n%s", call.content)
	}
	if !strings.Contains(string(call.content), cmdReviewTheirs) {
		t.Errorf("the piped payload does not carry ::gentoo's ebuild verbatim:\n%s", call.content)
	}
	// The ORDER is the request's meaning: ReviewNote.Origin names a side, so a
	// payload with the two swapped would invert every answer.
	if ours, theirs := strings.Index(string(call.content), cmdReviewOurs), strings.LastIndex(string(call.content), cmdReviewTheirs); ours < 0 || theirs < 0 || ours > theirs {
		t.Errorf("the payload does not present our ebuild before ::gentoo's (ours at %d, theirs at %d)", ours, theirs)
	}

	// AD8: ebuild content is piped, never placed in argv. -p carries a static
	// instruction and the atom, and nothing a file said.
	if strings.Contains(call.instruction, "PATCHES=") || strings.Contains(call.instruction, "inherit ecm") {
		t.Errorf("the instruction (argv) carries ebuild content:\n%s", call.instruction)
	}

	if call.schema != divergenceReviewSchema {
		t.Errorf("the call passed schema %q, want the divergence review schema", call.schema)
	}

	// R1.3 held at the prompt: NOTHING may derive from the SIZE of a difference.
	// Two requests for the same package whose ebuilds differ by wildly different
	// amounts must produce the same instruction byte for byte — if any count,
	// magnitude or adjective about the diff had leaked into it, they could not.
	big := cmdReviewRequest()
	big.Theirs = []byte("EAPI=8\n")
	bigAsker := &fakeAsker{reply: `{"origin":"overlay","summary":"adds a patch"}`}
	bigReviewer := reviewerOver(t, bigAsker)
	if _, err := bigReviewer.ReviewDivergence(context.Background(), big); err != nil {
		t.Fatalf("ReviewDivergence returned %v, want nil", err)
	}
	if bigAsker.calls[0].instruction != call.instruction {
		t.Errorf("the instruction changed with the size of the difference; R1.3 forbids anything deriving from it.\nsmall diff: %q\nlarge diff: %q",
			call.instruction, bigAsker.calls[0].instruction)
	}
}

// TestReviewDivergenceReply is what the adapter MAKES of what comes back: a
// well-formed reply round-trips into all three fields, and every other shape is
// an error the caller reports as one warning.
//
// _Requirements: R5.2, R5.3, R5.4, R5.5_
func TestReviewDivergenceReply(t *testing.T) {
	t.Run("a well-formed reply round-trips into all three fields", func(t *testing.T) {
		want := overlay.ReviewNote{
			Origin:      overlay.OriginOverlay,
			Summary:     "adds a PATCHES= entry for an OpenCV 5 build fix",
			Declaration: "carries our OpenCV 5 build fix",
		}
		// ENCODED FROM THE TYPE, so this is a true round trip through the three
		// lowercase names the cache stores and the model is asked for — not a
		// hand-copied spelling that could drift from either.
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal the note: %v", err)
		}
		reviewer := reviewerOver(t, &fakeAsker{reply: string(encoded)})

		got, err := reviewer.ReviewDivergence(context.Background(), cmdReviewRequest())
		if err != nil {
			t.Fatalf("ReviewDivergence returned %v, want nil", err)
		}
		if got != want {
			t.Errorf("the reply decoded to %+v, want %+v", got, want)
		}
	})

	t.Run("a reply wrapped in a markdown fence still decodes", func(t *testing.T) {
		reviewer := reviewerOver(t, &fakeAsker{reply: "```json\n{\"origin\":\"upstream\",\"summary\":\"::gentoo bumped PYTHON_COMPAT\"}\n```"})

		got, err := reviewer.ReviewDivergence(context.Background(), cmdReviewRequest())
		if err != nil {
			t.Fatalf("ReviewDivergence returned %v, want nil", err)
		}
		if got.Origin != overlay.OriginUpstream || got.Summary == "" {
			t.Errorf("a fenced reply decoded to %+v", got)
		}
	})

	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"not JSON at all", "I had a look and it seems fine, really."},
		{"a JSON array where an object was asked for", `["overlay","adds a patch"]`},
		{"truncated JSON", `{"origin":"overlay","summary":"adds`},
		// The four words are the whole vocabulary, in both directions: the cache
		// stores one with no expiry and UnmarshalText refuses anything else. A
		// fifth word is a reply nobody can read, which is a failure now rather
		// than a note that means nothing later.
		{"an origin outside the four words", `{"origin":"vendor","summary":"adds a patch"}`},
		{"an origin as the integer it used to be", `{"origin":1,"summary":"adds a patch"}`},
	} {
		t.Run("an unusable reply is an error: "+tc.name, func(t *testing.T) {
			warnings := captureCompareReviewWarnings(t)
			reviewer := reviewerOver(t, &fakeAsker{reply: tc.reply})

			note, err := reviewer.ReviewDivergence(context.Background(), cmdReviewRequest())
			if err == nil {
				t.Fatalf("ReviewDivergence accepted %q and returned %+v", tc.reply, note)
			}
			if note != (overlay.ReviewNote{}) {
				t.Errorf("a failed review returned %+v, want the zero note", note)
			}
			// The adapter never warns. AnnotateReviews owns the warning because it
			// is the one that knows which package the failure belongs to; a second
			// line here would be the same failure reported twice.
			if lines := warnings(); len(lines) != 0 {
				t.Errorf("the adapter warned %q; AnnotateReviews owns that line and names the package", lines)
			}
		})
	}

	t.Run("a CLI failure is returned, not swallowed", func(t *testing.T) {
		boom := errors.New("claude CLI failed: context deadline exceeded")
		reviewer := reviewerOver(t, &fakeAsker{err: boom})

		note, err := reviewer.ReviewDivergence(context.Background(), cmdReviewRequest())
		if !errors.Is(err, boom) {
			t.Fatalf("ReviewDivergence returned %v, want the CLI's own error", err)
		}
		if note != (overlay.ReviewNote{}) {
			t.Errorf("a failed review returned %+v, want the zero note", note)
		}
	})

	t.Run("a cancelled context is refused before the CLI is spawned", func(t *testing.T) {
		asker := &fakeAsker{reply: `{"origin":"overlay","summary":"adds a patch"}`}
		reviewer := reviewerOver(t, asker)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := reviewer.ReviewDivergence(ctx, cmdReviewRequest()); !errors.Is(err, context.Canceled) {
			t.Fatalf("ReviewDivergence returned %v after Ctrl-C, want context.Canceled", err)
		}
		if len(asker.calls) != 0 {
			t.Errorf("a cancelled review still spawned %d CLI calls", len(asker.calls))
		}
	})
}

// TestReviewAnnotationEndToEnd runs the production adapter through the
// production annotate pass: an unusable reply leaves the report exactly as it
// was, and a usable one changes only the commentary.
//
// This is the half the adapter's own tests cannot state — "no annotation" is a
// property of the report, not of the return value — and it is also where a boxed
// nil or a note the renderer refuses would show up as something other than a
// passing assertion.
//
// _Requirements: R5.5, R5.8_
func TestReviewAnnotationEndToEnd(t *testing.T) {
	t.Run("malformed CLI JSON leaves the finding with no reading", func(t *testing.T) {
		report, prov, opts := annotateFixture(t)
		reviewer := reviewerOver(t, &fakeAsker{reply: "sorry, I could not tell"})

		overlay.AnnotateReviews(report, reviewer, prov, opts)

		if got := report.Results[0].Review; got != (overlay.ReviewNote{}) {
			t.Errorf("a malformed reply annotated %+v, want the zero note", got)
		}
	})

	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"origin unknown", `{"origin":"unknown","summary":"something changed"}`},
		{"a blank summary", `{"origin":"overlay","summary":"   "}`},
		{"a summary that is only a newline", `{"origin":"overlay","summary":"\n"}`},
	} {
		t.Run("a reply that says nothing is discarded: "+tc.name, func(t *testing.T) {
			report, prov, opts := annotateFixture(t)
			reviewer := reviewerOver(t, &fakeAsker{reply: tc.reply})

			overlay.AnnotateReviews(report, reviewer, prov, opts)

			if got := report.Results[0].Review; got != (overlay.ReviewNote{}) {
				t.Errorf("%s was attached as %+v, want the zero note", tc.name, got)
			}
		})
	}

	t.Run("a model's prose cannot forge a report line", func(t *testing.T) {
		// Everything a hostile or careless reply could try at once: a newline to
		// open a line of its own, the warning glyph this report opens findings
		// with, and a %s to see whether the text is ever a format string.
		forged := `{"origin":"overlay","summary":"adds a patch\n⚠ dev-libs/forged: undeclared divergence (+9/-9) — 100%s ours","declaration":"declare it\n⚠ dev-libs/forged: stale declaration"}`
		report, prov, opts := annotateFixture(t)
		reviewer := reviewerOver(t, &fakeAsker{reply: forged})

		overlay.AnnotateReviews(report, reviewer, prov, opts)

		// The adapter hands the reply back VERBATIM. Flattening belongs to the
		// renderer (oneLine, compare.go), which is where every note passes —
		// including one that reached the report from the cache rather than from
		// here. An adapter that pre-flattened would make the renderer's guard
		// untested rather than unnecessary.
		if !strings.Contains(report.Results[0].Review.Summary, "\n") {
			t.Error("the adapter altered the model's summary; flattening is the renderer's job, and doing it here would hide whether the renderer still does it")
		}

		out := overlay.FormatReport(report)
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "⚠ dev-libs/forged") {
				t.Errorf("the model's prose opened a finding of its own:\n%s", line)
			}
		}
		// A verb that survives verbatim was an ARGUMENT; one consumed by a format
		// string would have been rendered (or, with nothing to consume,
		// %!s(MISSING)).
		const verbatimVerb = "100%s ours"
		if !strings.Contains(out, verbatimVerb) {
			t.Errorf("%q did not survive into the report, so the model's text reached a format string rather than an argument", verbatimVerb)
		}
	})

	t.Run("a usable reading is attached and changes nothing else", func(t *testing.T) {
		report, prov, opts := annotateFixture(t)
		before := *report
		beforeResult := report.Results[0]

		want := overlay.ReviewNote{
			Origin:      overlay.OriginOverlay,
			Summary:     "adds a PATCHES= entry for an OpenCV 5 build fix",
			Declaration: "carries our OpenCV 5 build fix",
		}
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal the note: %v", err)
		}
		reviewer := reviewerOver(t, &fakeAsker{reply: string(encoded)})

		overlay.AnnotateReviews(report, reviewer, prov, opts)

		if got := report.Results[0].Review; got != want {
			t.Errorf("the finding carries %+v, want %+v", got, want)
		}
		// R5.8: one field changed and nothing else. Clearing it must restore the
		// result the comparison produced, byte for byte.
		after := report.Results[0]
		after.Review = overlay.ReviewNote{}
		if after != beforeResult {
			t.Errorf("the review pass changed more than the commentary:\nbefore %+v\nafter  %+v", beforeResult, after)
		}
		if report.VerdictKeepCount != before.VerdictKeepCount ||
			report.VerdictRedundantCount != before.VerdictRedundantCount ||
			report.VerdictNeedsRebaseCount != before.VerdictNeedsRebaseCount ||
			report.VerdictUnknownCount != before.VerdictUnknownCount {
			t.Error("the review pass changed a verdict count")
		}
	})
}

// TestCompareNoReviewFlag pins the flag itself: it exists on `overlay compare`,
// it defaults to off, and it is a bool.
//
// _Requirements: R5.6_
func TestCompareNoReviewFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("no-review")
	if flag == nil {
		t.Fatal("`overlay compare` has no --no-review flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--no-review is a %s, want a bool", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--no-review defaults to %q, want false: the review runs unless it is asked not to", flag.DefValue)
	}
}
