package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// This file is THE import edge between the two halves of the review feature, and
// the only one this story adds. internal/overlay declares the DivergenceReviewer
// interface it consumes and names no LLM type; internal/autoupdate owns the
// `claude` CLI and knows nothing about a comparison. 025 R2.4 keeps that edge
// absent in both directions — internal/overlay's own suite fences it
// mechanically (TestOverlayImportsNoAutoupdate) — so the adapter that joins them
// lives HERE, in cmd/, which already imported both before this story started.
//
// NO API KEY IS ON THIS PATH, by construction rather than by care. reviewLLMConfig
// below asks for the subscription shape: no api_key_env, and bare mode explicitly
// off, which makes autoupdate's childEnv strip every inherited ANTHROPIC_* key
// from the spawned process so the CLI authenticates on its own logged-in session.
// Nothing here reads, holds or forwards a credential, so none can leak into argv,
// a log or an error.
//
// WHAT COMES BACK IS TEXT FOR A TERMINAL. It reaches no shell, no command and no
// file: the report prints it as an argument (compare.go's reviewCommentary), and
// R5.4's proposed declaration is a PROPOSAL — the overlay repository auto-commits
// and pushes within minutes, so a declaration this program wrote would be
// published before anyone could read it.

// reviewWarnf emits the one warning this file is allowed to print: a reviewer
// that was asked for and could not be built.
//
// It is a package var for the same narrow reason internal/overlay's warnLogf is
// one — logger binds its io.Writer at first use and exposes no setter
// (logger.go's `func Default`), so without a seam the only way to assert on this
// line would be to read the process's stderr. Production never assigns it.
var reviewWarnf = logger.Warn

// claudeAsker is the slice of *autoupdate.ClaudeCodeClient this adapter uses: one
// schema-constrained round trip. Declaring it here rather than holding the
// concrete client keeps the CLI out of every test that is about the translation,
// and it is the same "capability declared by its consumer" shape
// overlay.DivergenceReviewer and provider.PackageDirProvider already use.
type claudeAsker interface {
	AskJSON(instruction string, content []byte, schema string) (string, error)
}

// newClaudeAsker builds the real client. It is a var so tests can script the CLI
// without one being installed, and so a test can prove that `--no-review`
// reaches it ZERO times — R5.6 is a claim about a process that never starts, and
// the only way to assert something did not happen is to have the thing that
// would have recorded it.
//
// THE BUDGET ENTERS HERE AND NOWHERE ELSE. Both review paths are built through
// this one seam, so the operator's configured value bounds the divergence review
// and the realignment review as ONE number that cannot disagree with itself —
// which is why the parameter is here rather than on each of the two passes
// (S048-R4.1). Until it was passed the option was never applied at all: every
// review this program has ever run was bounded by
// autoupdate.DefaultClaudeCodeTimeout, whatever the operator had configured
// (S048-R3.1).
//
// IT ARRIVES AS A DURATION ALREADY RESOLVED BY THE CONFIG GETTER
// (`func GetTimeout` on the review block), never as the raw seconds from the
// file. WithClaudeCodeTimeout ignores a NON-POSITIVE duration, so a zero
// forwarded from an unwritten key would be dropped in silence and leave the
// client's own default governing a run that reads as configured — a defect
// shaped exactly like the one this change fixes. The getter is the one place
// "unset, zero or negative" becomes the documented default, and this seam takes
// the value from nowhere else.
//
// It is set ON THE CLIENT rather than per call, beside the context and for the
// same reason: autoupdate combines the two on entry to every invocation, so one
// value here bounds each round trip the reviewers make and there is no second
// deadline to keep in step with this one.
//
// A construction FAILURE returns an untyped nil rather than the (*ClaudeCodeClient)(nil)
// the constructor hands back beside its error. Boxed into this interface that
// pointer would be non-nil, and every `!= nil` check downstream would wave it
// through to a dereference.
var newClaudeAsker = func(ctx context.Context, budget time.Duration) (claudeAsker, error) {
	client, err := autoupdate.NewClaudeCodeClient(reviewLLMConfig(),
		autoupdate.WithClaudeCodeContext(ctx),
		autoupdate.WithClaudeCodeTimeout(budget))
	if err != nil {
		return nil, err
	}
	return client, nil
}

// reviewLLMConfig is the configuration the review's CLI client is built from:
// the SUBSCRIPTION shape, and deliberately not the operator's autoupdate LLM
// config.
//
// An empty api_key_env is the valid subscription shape in NewClaudeCodeClient —
// it is the one provider constructor for which an unnamed key var is a
// configuration rather than ErrLLMNotConfigured — and it means no credential is
// resolved at all. `bare: false` on top of that is what makes the absence
// active rather than incidental: resolveBare returns false without consulting
// anything, so childEnv REMOVES ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN and the
// configured key var from the spawned process's environment. A key exported in
// the operator's shell therefore cannot reach this review, and no key can leak
// from a path that never carries one.
//
// The model is left unset so it follows autoupdate's own default (sonnet), which
// is the one place that decision is made for every `claude` invocation this
// program issues.
func reviewLLMConfig() autoupdate.LLMConfig {
	return autoupdate.LLMConfig{Bare: "false"}
}

// compareDivergenceReviewer answers "what reviewer should this compare run use?"
// with the one value AnnotateReviews needs: a DivergenceReviewer, or nil.
//
// The two ways of having none — `--no-review` (R5.6) and a machine with no
// `claude` on PATH (R5.5) — both end here as a nil INTERFACE, which is why
// AnnotateReviews needs no second parameter and cmd/ needs no second condition
// that could disagree with it.
//
// `--no-review` returns BEFORE the seam, not after: R5.6 is "contact no model",
// so nothing is constructed, no PATH is consulted, and no process is spawned.
// A construction failure warns exactly once — the operator asked for a review
// and is not getting one, which is worth a line — and then the run proceeds
// without commentary, because the report they asked for is already complete
// without it.
func compareDivergenceReviewer(ctx context.Context, noReview bool, budget time.Duration) overlay.DivergenceReviewer {
	if noReview {
		return nil
	}

	reviewer, err := newDivergenceReviewer(ctx, budget)
	if err != nil {
		// The error is an ARGUMENT and never a format string: it may carry the
		// CLI's own text.
		reviewWarnf("the divergence review could not be started (%v); the report is unchanged apart from carrying no commentary", err)
		return nil
	}
	return reviewer
}

// newDivergenceReviewer builds the adapter over the `claude` CLI.
//
// AN ABSENT CLI IS (nil, nil): ABSENCE IS NOT A FAILURE. The review is
// commentary on a report that is complete without it, so a machine that has
// never installed `claude` must print the same report as one that has, silently
// — warning about it on every run would train the operator to ignore the line
// that matters. Every OTHER construction failure is (nil, err), because the
// difference between "you do not have this" and "you have it and it would not
// start" is the difference between nothing to say and something to fix.
//
// The CONTEXT is threaded into the client rather than applied per call, because
// that is where autoupdate puts it: ClaudeCodeClient combines c.ctx with
// c.timeout on entry to claude_code.go's `func run`, so a run cancelled with
// Ctrl-C kills the `claude` process it is waiting on instead of holding the
// terminal for the rest of the budget.
//
// The BUDGET is threaded the same way and for the same reason, and this
// function does nothing with it but carry it to the seam (S048-R4.1).
func newDivergenceReviewer(ctx context.Context, budget time.Duration) (overlay.DivergenceReviewer, error) {
	asker, err := newClaudeAsker(ctx, budget)
	if err != nil {
		if errors.Is(err, autoupdate.ErrClaudeCodeUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	if asker == nil {
		// Unreachable from the seam above, and cheap insurance against a later
		// one: an adapter wrapping a nil asker is a non-nil DivergenceReviewer
		// that panics on first use, which is the failure mode this whole file is
		// careful about.
		return nil, nil
	}
	return &claudeDivergenceReviewer{asker: asker}, nil
}

// claudeDivergenceReviewer translates one divergence into a CLI request and the
// reply back into a note. It is the whole of what this feature asks of a model.
type claudeDivergenceReviewer struct {
	asker claudeAsker
}

var _ overlay.DivergenceReviewer = (*claudeDivergenceReviewer)(nil)

// ReviewDivergence puts the two ebuilds to the model and returns its reading.
//
// IT NEVER WARNS. Every failure is returned, and AnnotateReviews turns it into
// one warning naming the package — which is the half this side does not know.
// Warning here as well would report the same failure twice, once without saying
// what it was about.
//
// The context is checked on ENTRY and then not again: the deadline that bounds
// the call belongs to the client — the budget the seam was given, combined with
// the same ctx by claude_code.go's `func run` — so a second one here would be a
// second thing to tune for one round trip, and a second answer to a question the
// operator now configures in exactly one place (S048-R3.1).
//
// What the entry check buys is the interrupted run: AnnotateReviews walks its
// findings in order, and after Ctrl-C the remaining ones fail here instead of
// each spawning a process that is about to be killed.
func (r *claudeDivergenceReviewer) ReviewDivergence(ctx context.Context, req overlay.ReviewRequest) (overlay.ReviewNote, error) {
	if err := ctx.Err(); err != nil {
		return overlay.ReviewNote{}, fmt.Errorf("the review was not started: %w", err)
	}

	reply, err := r.asker.AskJSON(divergenceReviewInstruction(req), divergenceReviewPayload(req), divergenceReviewSchema)
	if err != nil {
		// IT NAMES THIS OPERATION AND CLAIMS NOTHING ELSE. This seam opens no
		// file: both ebuilds arrive as bytes in req, read upstream before
		// ReviewDivergence was called, and all this function does with them is
		// put them on the CLI's stdin. The sentence that used to be here — "the
		// claude CLI could not read the two ebuilds" — was therefore false for
		// every failure it could ever report, and it sent whoever read it to the
		// filesystem to debug a deadline in this program's own code (S048-R1.4).
		//
		// THE CAUSE TRAVELS UNALTERED. Which failure this was — a budget that
		// elapsed, a process that never started, a non-zero exit — is decided
		// once, by autoupdate's own classifier, and every caller inherits that
		// one answer. Re-wording or re-classifying it here would be a second
		// answer free to drift from the first (S048-R1.3).
		return overlay.ReviewNote{}, fmt.Errorf("the divergence review failed: %w", withReviewOutcome(err))
	}

	// Decoded straight into the consumer's own type. ReviewNote carries the three
	// lowercase json tags and ReviewOrigin implements UnmarshalText, so ONE
	// spelling of `origin|summary|declaration` serves the model's reply, the note
	// cache's file and this decode — and a fifth origin word is rejected here for
	// exactly the reason the cache rejects it, rather than degrading to a note
	// that says nothing and is stored forever.
	var note overlay.ReviewNote
	if err := json.Unmarshal([]byte(unfenceJSON(reply)), &note); err != nil {
		// The reply is NOT included in the error. It is model-written text of
		// unbounded length and the caller prints this on a terminal line.
		return overlay.ReviewNote{}, fmt.Errorf("the model's reply is not the JSON the review asked for: %w", joinOutcome(err, overlay.ErrReviewUnusableReply))
	}

	// Returned VERBATIM, including a summary carrying newlines. Flattening
	// belongs to the renderer (oneLine, compare.go), which every note passes
	// through — including one that reaches the report from the cache rather than
	// from here. Doing it here as well would leave that guard untested rather
	// than unnecessary.
	return note, nil
}

// reviewOutcome translates the autoupdate outcome riding on a `claude` failure
// into the sentinel internal/overlay reads, which cannot import autoupdate
// (TestOverlayImportsNoAutoupdate). A stopped run becomes context.Canceled, so
// the review cause reads `cancelled`. It returns nil when err carries no
// outcome.
func reviewOutcome(err error) error {
	switch {
	case errors.Is(err, autoupdate.ErrClaudeTimedOut):
		return overlay.ErrReviewTimedOut
	case errors.Is(err, autoupdate.ErrClaudeCouldNotStart):
		return overlay.ErrReviewCouldNotStart
	case errors.Is(err, autoupdate.ErrClaudeExitedNonZero):
		return overlay.ErrReviewExitedNonZero
	case errors.Is(err, autoupdate.ErrClaudeUnusableOutput):
		return overlay.ErrReviewUnusableReply
	case errors.Is(err, autoupdate.ErrClaudeStopped):
		return context.Canceled
	}
	return nil
}

// withReviewOutcome attaches reviewOutcome(err) to err when there is one, and
// returns err unchanged otherwise.
func withReviewOutcome(err error) error {
	if outcome := reviewOutcome(err); outcome != nil {
		return joinOutcome(err, outcome)
	}
	return err
}

// joinOutcome makes err also match outcome without changing its text: the
// wrapper sentences are pinned (TestDivergenceReviewWrapperDoesNotBlameTheEbuilds),
// so the sentinel is attached through Unwrap and never spelled.
func joinOutcome(err, outcome error) error {
	return &outcomeError{err: err, outcome: outcome}
}

// outcomeError is the error joinOutcome returns.
type outcomeError struct {
	err     error
	outcome error
}

func (e *outcomeError) Error() string { return e.err.Error() }

func (e *outcomeError) Unwrap() []error { return []error{e.err, e.outcome} }

// divergenceReviewSchema is the shape the CLI is constrained to via
// --json-schema. It describes exactly what ReviewNote holds and nothing else:
// the model answers the report's three questions and decides nothing.
//
// The origin enum is the four words ReviewOrigin round-trips. It must stay in
// step with UnmarshalText, and the cost of drifting is not a compile error but a
// silent one: a fifth word the schema permitted would be refused on decode, on
// every run, forever — the note cache has no expiry, so nothing would ever heal
// it. A test builds the expected enum from ReviewOrigin.String() rather than
// copying it here.
//
// Only origin and summary are required. They are the two halves the report needs
// before it will print anything (reviewNoteSpeaks); declaration is empty for
// three of the four origins by design, and requiring it would invite a model to
// invent one where there is nothing of ours to declare.
const divergenceReviewSchema = `{
  "type": "object",
  "properties": {
    "origin": {"type": "string", "enum": ["unknown", "overlay", "upstream", "both"]},
    "summary": {"type": "string"},
    "declaration": {"type": "string"}
  },
  "required": ["origin", "summary"]
}`

// divergenceReviewInstruction is the static instruction, the value of -p.
//
// IT CARRIES NO EBUILD CONTENT. The two files are piped on stdin
// (divergenceReviewPayload below) because argv is world-readable through
// /proc/<pid>/cmdline and an ebuild is arbitrary shell — the same rule
// autoupdate applies to page content (AD8).
//
// IT ALSO CARRIES NOTHING DERIVED FROM THE SIZE OF THE DIFFERENCE. R1.3 is that
// nothing may be computed from how big a diff is, and a prompt built from the
// line counts would be exactly that computation wearing a different hat.
// ReviewRequest does not even hold them; this keeps the promise on the one field
// that could smuggle it back in, and a test compares the instruction produced
// for a one-line difference against a large one byte for byte.
//
// IT ASKS FOR PROSE ABOUT THE DIFFERENCE, NOT ABOUT THE PACKAGE, and that is not
// style. The note cache is keyed on the two files' CONTENT and excludes the atom
// (R5.7), so two packages carrying byte-identical ebuild pairs would share one
// note — the atom below is context for reading the files, never something to
// repeat back. It cannot happen on the live overlay, where no two packages have
// identical ebuilds, but a summary that named a package would be wrong the day
// it did.
//
// THE DECLARATION IS ASKED FOR AS ONE LINE OF AT MOST 72 CHARACTERS because the
// renderer flattens whitespace and truncates at patchedReasonCap — the same 72
// the operator's own `patched` reason is capped at. A model that writes a TOML
// block gets it printed as one legible-but-unpasteable line; a model that writes
// a `patched_reason`-shaped sentence gets a line the operator can paste.
func divergenceReviewInstruction(req overlay.ReviewRequest) string {
	var sb strings.Builder

	sb.WriteString("Read the two Gentoo ebuilds piped on stdin and explain the DIFFERENCE between them.\n\n")
	sb.WriteString("Each file follows a header line: first the overlay's copy (\"ours\"), then ::gentoo's copy (\"theirs\"). ")
	sb.WriteString("They are the same package at the same version, and they are not byte-identical.\n\n")

	// Context for reading the files — a category tells the model which eclasses
	// and conventions to expect. Deliberately not something to repeat back; see
	// the cache note above.
	fmt.Fprintf(&sb, "For context while reading them, they are %s/%s at version %s. Do not repeat that back.\n\n",
		req.Category, req.Package, req.Version)

	sb.WriteString("Answer three questions, as the JSON object the schema describes and nothing else.\n\n")

	sb.WriteString("origin - which side introduced the difference:\n")
	sb.WriteString("  \"overlay\"  ours carries work on top of ::gentoo's ebuild\n")
	sb.WriteString("  \"upstream\" ::gentoo revised its ebuild and ours did not follow\n")
	sb.WriteString("  \"both\"     each side carries something the other does not\n")
	sb.WriteString("  \"unknown\"  the two files do not let you tell\n\n")

	sb.WriteString("summary - ONE line of plain English saying what the difference DOES. ")
	sb.WriteString("Write about the change, not about the package: name the variables, dependencies, USE flags, patches, eclasses or phase functions involved. ")
	sb.WriteString("Do not name the package, and do not say how much changed.\n\n")

	sb.WriteString("declaration - ONE line of at most 72 characters, and only when origin is \"overlay\"; leave it empty otherwise. ")
	sb.WriteString("It is the sentence a maintainer would put in the `patched` field of .autoupdate/packages.toml to declare this difference, ")
	sb.WriteString("e.g. \"keeps our OpenCV 5 build fix and the extra runtime dep\".\n\n")

	// The report decides what happens to the package; the model reads the files.
	// Keeping that boundary in the prompt is what keeps the commentary
	// commentary (R5.8) rather than an opinion the operator has to argue with.
	sb.WriteString("Decide nothing else. Do not recommend keeping, removing or rebasing the package, ")
	sb.WriteString("do not judge whether the difference is justified, and do not read or write any file - everything you need is on stdin.")

	return sb.String()
}

// divergenceReviewPayload is what goes on the CLI's stdin: both ebuilds, whole
// and verbatim, ours first.
//
// THE ORDER IS THE QUESTION'S MEANING. ReviewNote.Origin names a SIDE, so a
// payload with the two swapped would invert every answer — the same reason the
// note cache's fingerprint does not commute.
//
// The headers are plain ASCII rules rather than a markup a model might imitate
// in its reply, and each file is written whole: a truncated ebuild would be a
// reading of something other than the divergence the report found.
func divergenceReviewPayload(req overlay.ReviewRequest) []byte {
	var buf []byte
	buf = append(buf, "--- OUR EBUILD (the bentoo overlay's copy) ---\n"...)
	buf = appendEbuild(buf, req.Ours)
	buf = append(buf, "--- THEIR EBUILD (::gentoo's copy) ---\n"...)
	buf = appendEbuild(buf, req.Theirs)
	return buf
}

// appendEbuild writes one file into the payload, ending it with a newline so the
// next header starts a line of its own. An ebuild with no trailing newline is
// legal and does occur; without this its last line and the next header would run
// together into a line neither file contains.
func appendEbuild(buf, ebuild []byte) []byte {
	buf = append(buf, ebuild...)
	if len(ebuild) > 0 && ebuild[len(ebuild)-1] != '\n' {
		buf = append(buf, '\n')
	}
	return buf
}

// unfenceJSON removes a markdown code fence from a reply, if there is one.
//
// With --json-schema in force the CLI answers with a bare object, so this is
// belt-and-braces: it costs three string operations and it is the difference
// between a fenced reply being a note and being a warning. autoupdate's
// stripJSONFences does the same for the same reason and is unexported, so this
// is a second spelling of a small thing rather than an import edge for it.
//
// It removes nothing else. Scanning a reply for the first "{...}" would recover
// JSON out of prose, and prose around the answer is a model that did not answer
// the question — better refused than half-read.
func unfenceJSON(reply string) string {
	trimmed := strings.TrimSpace(reply)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	// Drop the opening fence line, which may carry a language tag (```json).
	if nl := strings.IndexByte(trimmed, '\n'); nl != -1 {
		trimmed = trimmed[nl+1:]
	} else {
		trimmed = strings.TrimPrefix(trimmed, "```")
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(trimmed), "```"))
}
