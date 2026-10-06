package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// Authored for story 048, sub-task 4.2 — S048-R2.2, S048-R2.3, S048-R5.1.
//
// # What this guard is for
//
// After sub-task 3.3, TWO constants can bound a review: config.DefaultReviewTimeout,
// which the getter returns when the operator configured nothing, and
// llm.DefaultClaudeCodeTimeout, which the client falls back to when it is
// handed no timeout option at all. Sub-task 4.2 writes a measurement into the
// FIRST one, and that measurement is worth nothing if the second is what
// actually governs. This guard is the statement that the review's budget comes
// from the constant whose documentation carries the measurement.
//
// # The trap it must not reproduce
//
// claude_code_test.go:128 and :192 assert the client's default against
// DefaultClaudeCodeTimeout — the constant that DEFINES it. That assertion holds
// for any value the constant is ever given, so it can never fail for its own
// reason, and a review silently running on the wrong constant is exactly what it
// would not catch. This file therefore asserts nothing about a constant against
// itself. It observes the budget the production wiring HANDS OVER, having read a
// real config file, and it fails when that value is one no config layer
// produced.
//
// # Why the observation is at the seam and not on the wall clock
//
// Sub-task 3.3's guard measures wall-clock time, because a CONFIGURED budget can
// be made small enough to measure. This one is about the budget in force when
// nothing is configured, and that budget is minutes long by construction — a
// wall-clock version would have to wait it out on every run. So the two split
// the chain at its one seam: 3.3 proves that the value handed to newClaudeAsker
// becomes the deadline an invocation dies at, and this proves which value is
// handed over when the operator has set nothing. Neither half is worth much
// alone, and the pair is Success Metric 5 plus S048-R2.2 together.
//
// GOTCHA — a zero is not "no opinion", it is the other constant. Sub-task 3.3
// passes the budget through autoupdate.WithClaudeCodeTimeout, which ASSIGNS ONLY
// WHEN d > 0 (claude_code.go:136). Wiring that forwarded the raw
// `autoupdate.review.timeout` int instead of the getter's resolved duration
// would therefore hand over 0 for every unset, half-written or zeroed key, the
// option would be silently ignored, and every such review would run on
// DefaultClaudeCodeTimeout while the measurement sat in the other constant's
// doc. Case 4 below is that fixture.
//
// GOTCHA for whoever changes config.DefaultReviewTimeout — it is an INT OF
// SECONDS, the shape DefaultValidateTimeout (config.go:765) already uses and the
// shape S048-R3.3 requires, so the expectation below multiplies it by
// time.Second. A constant redeclared as a time.Duration would make this file
// compare 120ns against 120s, and it should be corrected here rather than
// worked around.

// reviewBudgetCase is one config file and the budget the seam must be handed
// after loading it.
type reviewBudgetCase struct {
	name string
	// autoupdateBlock is spliced into the config file verbatim, or omitted when
	// empty.
	autoupdateBlock string
	want            time.Duration
	why             string
}

// captureReviewBudget replaces, on d, the construction seam both review paths
// share and returns every budget it was handed during the run.
//
// The asker it returns FAILS every request, which is what keeps this guard about
// the budget and nothing else: the review is attempted, it does not return, the
// run warns and prints its report, and no model, CLI or PATH is involved.
//
// NOTE for sub-task 3.3: the assignment below is the new seam signature — the
// budget arrives as a time.Duration, and since story 059 no context beside it.
// If the signature lands in another shape, this line moves with the other five
// consumers, and what it asserts is unchanged.
func captureReviewBudget(d *deps) func() []time.Duration {
	var handed []time.Duration
	d.newClaudeAsker = func(_ *slog.Logger, budget time.Duration) (claudeAsker, error) {
		handed = append(handed, budget)
		return &fakeAsker{err: errors.New("this asker exists to be counted, never to answer")}, nil
	}
	return func() []time.Duration { return handed }
}

// runCompareWithAutoupdateBlock writes a world whose config carries the given
// `autoupdate:` block (or none at all) and runs the shipped command over it,
// built from d.
func runCompareWithAutoupdateBlock(t *testing.T, autoupdateBlock string, d *deps) {
	t.Helper()

	home := t.TempDir()
	overlayPath := filepath.Join(home, "overlay")
	gentooPath := filepath.Join(home, "gentoo")
	for _, sub := range []string{"profiles", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlayPath, sub), 0o750); err != nil {
			t.Fatalf("mkdir overlay/%s: %v", sub, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(gentooPath, "profiles"), 0o750); err != nil {
		t.Fatalf("mkdir gentoo/profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gentooPath, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}

	// One undeclared divergence, so the run has something to review and the seam
	// is reached the way a real run reaches it.
	realignWriteEbuild(t, overlayPath, "media-libs", "gst-plugins-qt6", "1.29.2", realignOursEbuild)
	realignWriteEbuild(t, gentooPath, "media-libs", "gst-plugins-qt6", "1.29.2", realignBaselineEbuild)

	configDir := filepath.Join(home, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0o750); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	cfg := "overlay:\n  path: " + overlayPath + "\n  remote: origin\n" +
		"git:\n  user: Test\n  email: test@test.com\n" +
		autoupdateBlock +
		"repositories:\n" +
		"  gentoo:\n    provider: local\n    path: " + gentooPath + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// No `claude` anywhere: the seam is stubbed, so nothing is spawned and this
	// guard cannot depend on what is installed on the machine running it.
	t.Setenv("PATH", t.TempDir())

	realignFlags(t, false, false)
	realignRunWith(t, nil, d)
}

// TestAbsentReviewKeyRunsOnTheConfigDefaultBudget pins WHICH constant governs a
// review nobody configured, at the seam that decides it.
//
// The cases are ordered hostile-first. An implementation that hardcoded
// config.DefaultReviewTimeout at the seam would satisfy the benign case
// perfectly and ignore the operator entirely, so the case where the key IS set
// is written first; the two shapes that mean "unset" without being absent come
// next, because a wiring that answered only the fully-absent block would leave a
// half-written one resolving to a different budget than an absent one — the
// collapse S048-R3.2 forbids, read from the other side.
//
// _Requirements: S048-R2.2, S048-R2.3, S048-R5.1_
func TestAbsentReviewKeyRunsOnTheConfigDefaultBudget(t *testing.T) {
	// The int-of-seconds convention, converted here once. See the GOTCHA above.
	wantDefault := time.Duration(config.DefaultReviewTimeout) * time.Second

	cases := []reviewBudgetCase{
		{
			name:            "the key is set, so it and not the default is handed over",
			autoupdateBlock: "autoupdate:\n  review:\n    timeout: 45\n",
			want:            45 * time.Second,
			why: "a seam that always hands over config.DefaultReviewTimeout satisfies every case below and ignores " +
				"the operator; this case is what tells the two apart",
		},
		{
			name:            "a half-written block resolves to the default",
			autoupdateBlock: "autoupdate:\n  review: {}\n",
			want:            wantDefault,
			why: "S048-R3.2 puts an absent block, a half-written block and a nil pointer on the same value; a " +
				"half-written block that resolved elsewhere would give the same operator two budgets",
		},
		{
			name:            "a zero key resolves to the default, not to no option at all",
			autoupdateBlock: "autoupdate:\n  review:\n    timeout: 0\n",
			want:            wantDefault,
			why: "autoupdate.WithClaudeCodeTimeout assigns only when d > 0, so a zero handed to the seam is silently " +
				"the client's own constant and not the one carrying the measurement",
		},
		{
			name:            "no autoupdate block at all",
			autoupdateBlock: "",
			want:            wantDefault,
			why:             "the shipped case: nothing configured, and the review still runs on the measured budget",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			td := defaultDeps()
			handed := captureReviewBudget(td)
			runCompareWithAutoupdateBlock(t, tc.autoupdateBlock, td)

			budgets := handed()
			// The vacuity guard: an assertion over an empty slice passes, and a
			// run that never built a reviewer would report this guard clean while
			// measuring nothing.
			if len(budgets) == 0 {
				t.Fatalf("the run never reached newClaudeAsker, so no budget was observed. This guard is about the value " +
					"the shared construction seam is handed, and a run that never reaches it proves nothing about it.")
			}

			for i, got := range budgets {
				if got == 0 {
					t.Errorf("invocation %d was constructed with a zero budget.\n"+
						"autoupdate.WithClaudeCodeTimeout ignores a non-positive duration (claude_code.go:136), so a zero "+
						"leaves llm.DefaultClaudeCodeTimeout (%s) governing the review while the measurement "+
						"S048-R2.2 asks for sits in config.DefaultReviewTimeout, read by nothing. %s",
						i, llm.DefaultClaudeCodeTimeout, tc.why)
					continue
				}
				if got != tc.want {
					t.Errorf("invocation %d was constructed with a %s budget, want %s.\n%s\n"+
						"The budget in force must come from the configuration layer — the constant whose documentation "+
						"names the measurement and its date — and never from the client's own fallback (%s), which this "+
						"story leaves to the four other claude invocation sites (S048-R2.2, S048-R2.3, S048-R4.2).",
						i, got, tc.want, tc.why, llm.DefaultClaudeCodeTimeout)
				}
			}
		})
	}
}
