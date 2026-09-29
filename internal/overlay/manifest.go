// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/procgroup"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// execCommand and lookPath are package-level seams over os/exec so tests can
// stub pkgdev discovery and invocation without a real binary. Both default to
// the real functions.
var (
	execCommand = exec.CommandContext
	lookPath    = exec.LookPath
)

// Errors for manifest operations.
var (
	ErrPkgdevNotFound       = errors.New("pkgdev not found; install dev-util/pkgdev")
	ErrManifestNoTargets    = errors.New("no packages found to update")
	ErrManifestInvalidScope = errors.New("invalid manifest scope")
)

// DefaultManifestJobs is the default number of pkgdev workers run in parallel
// when ManifestOptions.Jobs is not set (or set to a non-positive value).
const DefaultManifestJobs = 10

// DefaultDistfilesCache is the system path queried, when readable, to skip
// re-downloading distfiles already present in the portage cache. Used as the
// default for ManifestOptions.DistfilesCache.
//
// The value lives in internal/common/distfiles alongside the resolution logic
// that consumes it; it is re-exported here under the name the CLI has always
// used for the --distfiles-cache flag default.
const DefaultDistfilesCache = distfiles.DefaultCache

// ManifestScope identifies one or more packages to regenerate Manifests for.
//
// Resolution rules:
//   - Empty Category and Package: every package in the overlay.
//   - Non-empty Category, empty Package: every package in that category.
//   - Both set: that single package.
type ManifestScope struct {
	Category string
	Package  string
}

// ManifestOptions controls Manifest regeneration behavior.
type ManifestOptions struct {
	// Keep, if true, leaves the existing Manifest in place and lets pkgdev
	// reconcile it. By default, the existing Manifest is moved to a backup
	// before regeneration and restored only on failure (clean regen).
	Keep bool
	// DryRun, if true, lists the packages that would be processed without
	// running pkgdev or touching files.
	DryRun bool
	// Distdir, when non-empty, is used as pkgdev's --distdir. The path is
	// expanded (~ and relative paths) and created if missing, and is
	// preserved across runs as a persistent download cache. When empty,
	// a temporary directory is created under os.TempDir() and removed
	// when the run completes.
	Distdir string
	// Jobs is the maximum number of pkgdev invocations to run in parallel.
	// Values <= 0 fall back to DefaultManifestJobs. Internally clamped to
	// the number of targets so we never spin idle workers.
	Jobs int
	// DistfilesCache, when non-empty, points to a read-only distfiles cache
	// (typically /var/cache/distfiles) that is consulted before each pkgdev
	// invocation. For every DIST entry listed in the package's existing
	// Manifest, if a file with the same name exists in this cache, a symlink
	// is created in the working distdir so pkgdev can reuse it instead of
	// downloading. Empty string disables the optimization entirely. The
	// cache is never written to.
	DistfilesCache string
	// Reporter receives lifecycle events as workers process targets.
	// Nil means silent (no progress output) — it is normalized to tui.Noop().
	// The CLI typically wires a live TUI or plain reporter here.
	Reporter tui.Reporter
	// Summary composes the sentence the run's batch closes on, from the result
	// the caller is about to receive. Nil closes the batch with no wording at
	// all, which is what `overlay autoupdate` already passes today.
	//
	// # It is a function because the words are the CALLER's and the numbers
	// are the run's
	//
	// A plain string field could not carry a summary: nothing is known to
	// summarize until the last worker has finished, which is inside this
	// package. A function lets the run supply the facts at the moment it has
	// them and the caller supply the wording it chose, without either one
	// holding the other's half (S046-R5.1, S046-R5.2; design.md D5).
	//
	// # Why a library that must not choose wording still relays some
	//
	// The alternative — this package stops calling BatchDone and the caller
	// makes the call itself — splits a bracket: BatchStart is emitted inside
	// the run, so a batch opened here and closed out there stays open on every
	// path a caller forgets, and the Reporter's consumers would have to grow a
	// case for a run that never ends. Unchanged Behavior 3 of story 046 is
	// exactly that those consumers do not move. R5.2 governs who chose the
	// WORDS, not who ended the run.
	//
	// The four paths that return before the worker loop — an empty selection, a
	// preview, and the two pre-flight refusals — open no batch, so they never
	// reach this at all. A --dry-run in particular emits no lifecycle event of
	// any kind, which is why a summary composer never has to answer for one.
	Summary func(ManifestResult) string
	// Ctx, when non-nil, is propagated to the pkgdev sub-processes via
	// exec.CommandContext so callers can cancel an in-flight run (e.g.
	// on SIGINT). Nil is treated as context.Background().
	Ctx context.Context
}

// summarize is the sentence the run's batch closes on: the caller's, or the
// empty string when the caller supplied none.
//
// The nil check lives here rather than at the call site so the run reads as one
// statement — close the batch on the caller's sentence — and so "no composer"
// has exactly one meaning for every future caller. The empty string is a close
// with no wording, not a missing event: the batch still ends, which is what the
// Reporter's consumers are built to expect.
func (o *ManifestOptions) summarize(result ManifestResult) string {
	if o.Summary == nil {
		return ""
	}
	return o.Summary(result)
}

// ManifestResult collects per-package results of a regeneration run, and is the
// value a caller asks how the run went (S046-R1.5).
//
// # The counts are derived, not stored
//
// Ok and Failed are methods over Updates rather than fields set beside it. A
// pair of stored counters is a second copy of what the rows already say, and two
// copies of one fact are two things to keep in agreement: a target appended
// without the counter being touched makes the summary disagree with the list
// printed under it, and nothing catches it because both numbers still look
// plausible. Derived, they cannot drift.
//
// # Why the counts exist at all
//
// The run used to state them in exactly one place — inside a sentence handed to
// the Reporter, "%d ok, %d failed" — which is a report squeezed through a
// progress channel (design.md D5). Nothing downstream can count that sentence,
// export it, or render it a second time in another mode. They are values the
// caller holds now, and the sentence is composed FROM them rather than instead
// of them.
//
// # NotEvaluated and Interrupted ARE stored, and the asymmetry is the point
//
// Ok and Failed are questions about the ROWS, so the rows can answer them and a
// stored copy could only disagree. "How much of the plan did this run never get
// to" is a question about the RUN, and no row can answer it: a target the run
// never reached contributes nothing to look at, which is exactly what makes it
// missing. So the two facts that only the run knows are set by the run, once,
// where it knows them (S046-R1.4).
type ManifestResult struct {
	// Updates is one entry per target the run FINISHED EVALUATING, in the order
	// the targets were given.
	//
	// On a run that reached the end of its list that is every target it was
	// handed. On an interrupted one it is short, and the two fields below are
	// what account for the difference — see Interrupted for why the missing
	// targets are absent rather than present and marked.
	Updates []ManifestUpdate
	// NotEvaluated is how many of the targets the run was handed it never
	// established an outcome for: the ones it never started, and the ones whose
	// pkgdev was still running when the run was cancelled.
	//
	// It is a fact about the RUN, which is why it is a field beside Updates
	// rather than a method over it: nothing in the slice can say how long the
	// slice was supposed to be. Its zero value is the honest answer for every
	// run that finished, and for a ManifestResult a caller assembled by hand.
	//
	// The report envelope carries the same number under the same name
	// (report.Run.NotEvaluated) so the mapping is a copy rather than a
	// translation — one less place for the two to drift apart.
	NotEvaluated int
	// Interrupted reports that the run stopped before reaching the end of its
	// target list.
	//
	// # It is separate from NotEvaluated, and both are needed
	//
	// A run cancelled in the instant after its LAST target completed lost
	// nothing: NotEvaluated is zero and the run was still cut short. Deriving
	// "was it interrupted" from "is the gap zero" would report that run as a
	// complete one, and the operator who pressed ctrl+c would be told their run
	// finished normally.
	//
	// # False is the safe zero value, which is why the field is not named Complete
	//
	// A ManifestResult built by hand — in a test, or by a caller wrapping a
	// slice — gets false, and false here means "not interrupted". A Complete
	// bool would default to "this run was cut short" and quietly draw the
	// interrupted block over every such report.
	Interrupted bool
}

// Ok is how many targets the run regenerated successfully.
func (r ManifestResult) Ok() int {
	ok := 0
	for i := range r.Updates {
		if r.Updates[i].Success {
			ok++
		}
	}
	return ok
}

// Failed is how many targets did not succeed.
//
// It is the COMPLEMENT of Ok rather than a second loop with the condition
// inverted, so the two can never both skip a target or both claim one: a column
// defined as "the rest" cannot drift from the column it is the rest of. Every
// target therefore lands in exactly one, and Ok()+Failed() is always
// len(Updates) — which is what makes the pair safe to print as a summary line
// and safe to sum inside a report that also lists the rows.
//
// A target the run never evaluated is in NEITHER column, because it is not in
// Updates at all. Counting it as failed would be this package inventing a
// failure out of the operator's own ctrl+c: nothing was learned about that
// package, and "we did not get to it" is not "it did not work". The count of
// those targets is NotEvaluated, a statement about the RUN rather than about any
// target, and it travels to the report envelope (report.Run.Complete /
// NotEvaluated) which already says it once for every kind of batch instead of
// once per domain.
func (r ManifestResult) Failed() int {
	return len(r.Updates) - r.Ok()
}

// fail records one target's failure in the two forms its readers need, from one
// cause, at one call site — so the sentence and the value can never end up
// describing different failures.
//
// Error gets the bare cause because the formatters print the atom in front of
// it; Err gets the same cause wrapped with %w AND with the atom, because an
// error that travels on its own has to name the target it belongs to (S046-R5.1,
// and the repository's Go convention: an error without the identifier that
// triggered it is unactionable). Callers that want to add context wrap the cause
// before passing it, so the two fields stay in step whatever it says.
func (u *ManifestUpdate) fail(cause error) {
	u.Success = false
	u.Error = cause.Error()
	u.Err = fmt.Errorf("%s/%s: %w", u.Category, u.Package, cause)
}

// ParseManifestScope parses a single CLI argument into a ManifestScope.
//
// Accepted forms:
//   - ""                      -> whole overlay
//   - "<category>"            -> all packages in category
//   - "<category>/<package>"  -> single package
func ParseManifestScope(arg string) (ManifestScope, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ManifestScope{}, nil
	}
	parts := strings.Split(arg, "/")
	switch len(parts) {
	case 1:
		cat := strings.TrimSpace(parts[0])
		if cat == "" {
			return ManifestScope{}, fmt.Errorf("%w: empty category", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat}, nil
	case 2:
		cat := strings.TrimSpace(parts[0])
		pkg := strings.TrimSpace(parts[1])
		if cat == "" || pkg == "" {
			return ManifestScope{}, fmt.Errorf("%w: expected <category>/<package>", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat, Package: pkg}, nil
	default:
		return ManifestScope{}, fmt.Errorf("%w: too many '/' separators in %q", ErrManifestInvalidScope, arg)
	}
}

// ResolveManifestTargets expands a scope into the concrete list of packages
// (category/package pairs) present in the overlay.
func ResolveManifestTargets(overlayPath string, scope ManifestScope) ([]ManifestUpdate, error) {
	if overlayPath == "" {
		return nil, ErrOverlayPathNotSet
	}

	if scope.Category != "" && scope.Package != "" {
		pkgDir := filepath.Join(overlayPath, scope.Category, scope.Package)
		if !isPackageDir(pkgDir) {
			return nil, fmt.Errorf("package %s/%s not found in overlay", scope.Category, scope.Package)
		}
		return []ManifestUpdate{{Category: scope.Category, Package: scope.Package}}, nil
	}

	scan, err := ScanOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("scanning overlay: %w", err)
	}

	var targets []ManifestUpdate
	for _, p := range scan.Packages {
		if scope.Category != "" && p.Category != scope.Category {
			continue
		}
		targets = append(targets, ManifestUpdate{Category: p.Category, Package: p.Package})
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Category != targets[j].Category {
			return targets[i].Category < targets[j].Category
		}
		return targets[i].Package < targets[j].Package
	})

	if len(targets) == 0 {
		if scope.Category != "" {
			return nil, fmt.Errorf("%w: category %q has no packages", ErrManifestNoTargets, scope.Category)
		}
		return nil, ErrManifestNoTargets
	}

	return targets, nil
}

// RegenerateManifests regenerates Manifest files for the given packages using
// pkgdev. Workers are dispatched in parallel up to opts.Jobs (default
// DefaultManifestJobs); each pkgdev process runs against its own package
// directory and shares the resolved distdir as a download cache.
//
// By default, the existing Manifest is moved aside before pkgdev runs so a
// fresh file is produced (clean regeneration). The backup is restored on
// failure. opts.Keep skips this step.
//
// pkgdev output is captured per job and surfaced through opts.Reporter as
// TaskStart/TaskLine/TaskDone events, bracketed by BatchStart/BatchDone. If
// Reporter is nil it is normalized to tui.Noop(), so the call is silent — and
// silent costs the caller nothing, because the reporter is a view of the run
// and not the run's result: every fact about how it went is in the returned
// ManifestResult, each failure as an unwrappable Err and as the Output its
// command printed, and the ok/failed counts a method call away (S046-R5.1).
//
// The returned Updates preserve the order of the input targets, even when
// workers complete out of order.
//
// # Cancelling opts.Ctx stops the run and does not fail what it never reached
//
// Workers stop PULLING from the queue once the context is done, and a target
// that was still running when the cancellation arrived contributes no row
// either. Both are counted in ManifestResult.NotEvaluated, and Interrupted says
// the run was cut short even when that count is zero (S046-R1.4).
//
// The alternative — letting the loop drain the queue against a dead context, so
// every remaining target comes back with "context canceled" — is what this
// function used to do, and it is a report full of failures the operator caused
// by asking the run to stop. Nothing was learned about those packages; saying so
// is the whole of R1.4.
//
// # It returns a ManifestResult now, not the bare slice
//
// The slice was enough while the only facts were per target. A run also
// establishes two things about ITSELF — how far it got, and whether it was cut
// short — and neither can be read off a list of what it did reach. Handing back
// the value that holds all three keeps them travelling together; a caller
// wrapping the slice by hand could only wrap what it was given, and would have
// to guess the rest from lengths it no longer knows.
func RegenerateManifests(overlayPath string, targets []ManifestUpdate, opts *ManifestOptions) ManifestResult {
	if opts == nil {
		opts = &ManifestOptions{}
	}

	updates := make([]ManifestUpdate, len(targets))
	copy(updates, targets)

	// Every early return below hands back reached(updates): a run that answered
	// for every target it was given, with no gap. That is true of all three —
	// an empty selection, a preview, and the two pre-flight refusals — because
	// each of them finished the whole of what it set out to do. Only the worker
	// loop can leave a target unevaluated, so only the worker loop builds a
	// result that says so.
	if len(updates) == 0 {
		return reached(updates)
	}

	if opts.DryRun {
		return reached(updates)
	}

	// pkgdev discovery short-circuits BEFORE any reporter call: a missing
	// binary marks every target failed without opening a batch, so a nil/Noop
	// or recording reporter sees no events at all.
	if _, lookErr := lookPath("pkgdev"); lookErr != nil {
		// The two fields are set here instead of through fail() because they
		// must not say the same thing. What the operator READS is the sentinel
		// alone — it is the actionable form of this exact condition, and it
		// already names the package to install; exec's own wording ("executable
		// file not found in $PATH") would only lengthen it. What the caller
		// HOLDS keeps exec's error in the chain, so the lookup that produced the
		// verdict is still reachable and is not simply dropped: errors.Is matches
		// ErrPkgdevNotFound, and exec.ErrNotFound through lookErr.
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = ErrPkgdevNotFound.Error()
			updates[i].Err = fmt.Errorf("%s/%s: %w (%w)",
				updates[i].Category, updates[i].Package, ErrPkgdevNotFound, lookErr)
		}
		return reached(updates)
	}

	// ResolveOrTemp, not Resolve: this command documents in its own --help that
	// an unset --distdir means a throwaway temporary directory, which is what
	// lets it run without sudo. distfiles.Resolve implements the autoupdate
	// path's precedence instead (host DISTDIR by default) and would silently
	// change that promise.
	//
	// The resolved directory carries its own provenance: Cleanup removes it
	// only when this process created it, so a caller-supplied --distdir (which
	// may be the host's real DISTDIR) survives the run.
	dir, err := distfiles.ResolveOrTemp(opts.Distdir)
	if err != nil {
		// Wrapped with what was being attempted: on its own the cause is a bare
		// "mkdir /x: permission denied", which tells an operator reading a list
		// of failed packages nothing about WHICH step of the run produced it.
		cause := fmt.Errorf("resolving the distfiles directory: %w", err)
		for i := range updates {
			updates[i].fail(cause)
		}
		return reached(updates)
	}
	defer dir.Cleanup()
	distdir := dir.Path

	// Resolve the distfiles cache once: a missing or unreadable directory
	// silently disables the optimization for the whole run, so workers don't
	// repeatedly stat a path that doesn't exist.
	cacheDir := distfiles.ResolveCache(opts.DistfilesCache, distdir)

	jobs := opts.Jobs
	if jobs <= 0 {
		jobs = DefaultManifestJobs
	}
	if jobs > len(updates) {
		jobs = len(updates)
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: opts.Ctx is an additive field; nil means "no cancellation requested"
	}

	// Normalize the reporter once so workers can emit unconditionally. A nil
	// reporter becomes a no-op (R3.3), matching the previous silent behavior.
	rep := opts.Reporter
	if rep == nil {
		rep = tui.Noop()
	}
	rep.BatchStart(len(updates))

	queue := make(chan int, len(updates))
	for i := range updates {
		queue <- i
	}
	close(queue)

	// evaluated[i] records that the run established an outcome for updates[i].
	// It is written only by the worker that owns index i and read only after
	// wg.Wait, which is exactly the discipline updates[i] already follows: one
	// writer per element, and a happens-before edge between the writes and the
	// single reader. No lock, and nothing for -race to find.
	evaluated := make([]bool, len(updates))

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				// Checked before the target is STARTED, not inside it. Starting
				// a target against a dead context would spawn a pkgdev that is
				// killed before it does anything and then record it as a
				// failure — a package reported as broken because the operator
				// asked the run to stop. Leaving the queue undrained is what
				// makes "never reached" a real state instead of a fabricated
				// verdict (S046-R1.4).
				if ctx.Err() != nil {
					return
				}

				runOneManifest(ctx, overlayPath, distdir, cacheDir, &updates[i], opts, rep)

				// A success is always an outcome: the Manifest was regenerated,
				// and a cancellation arriving afterwards cannot un-regenerate
				// it. A FAILURE observed once the context is already dead is a
				// different matter — pkgdev killed mid-fetch and pkgdev failing
				// on its own merits both come back as a non-zero exit, and this
				// package cannot tell them apart. Attributing it to the
				// interrupt is the conservative reading: an outcome we are
				// unsure of is reported as one we never reached, never as a
				// failure the package earned.
				//
				// The cost is a narrow race in the other direction — a genuine
				// failure that lands in the same instant as the cancellation is
				// counted as unevaluated rather than as failed. That trade is
				// deliberate: under-claiming what a stopped run learned is
				// recoverable by re-running it, while a fabricated failure sends
				// an operator to debug a package that was never broken.
				evaluated[i] = updates[i].Success || ctx.Err() == nil
			}
		}()
	}
	wg.Wait()

	// The run's own account of how far it got, taken once, here — the only
	// place that knows both how many targets were handed in and which of them
	// came back with something to say (S046-R1.4).
	result := ManifestResult{Updates: updates, Interrupted: ctx.Err() != nil}
	if result.Interrupted {
		result.Updates, result.NotEvaluated = evaluatedOnly(updates, evaluated)
	}

	// The batch closes on a sentence this package did not write. The facts are
	// the run's and go out whole; the wording is whatever the caller chose, and
	// a caller that chose none gets an empty close rather than one invented here
	// (design.md D5, S046-R5.2).
	//
	// # What moved is the WORDING, and it is not what sub-task 5.1 moved
	//
	// 5.1 moved where the numbers come from: Ok and Failed are methods over the
	// rows now, not a pair of counters kept for one sentence. The format string
	// stayed, which left this package still deciding how a run LOOKS — and a
	// sentence composed inside a library is a finding that left as text, so
	// nothing downstream can count it, export it, or draw it a second time in
	// another mode. That is the defect D5 names, and moving the numbers did not
	// touch it.
	//
	// # The argument for keeping it here survives the move, on the other side
	//
	// What stood here said the sentence is one more reader of the numbers rather
	// than the only place they exist, so the live region and the report cannot
	// disagree about how the run went. That is true and it is preserved: the
	// composer is handed the very value the caller builds its report from, so
	// both readings still come off one set of rows. What it did not answer is
	// who chooses the words — which is the question R5.2 asks, and the reason
	// the composer is now the caller's.
	//
	// On an interrupted run the value carries what the run established rather
	// than what it was handed, so a summary composed from it counts the same
	// targets the report lists.
	rep.BatchDone(opts.summarize(result))

	return result
}

// reached is the result of a run that answered for every target it was given:
// no gap, and not cut short.
//
// It exists so the four paths that return before the worker loop say that in
// one word instead of four literal zero-valued fields, and so the day a fifth
// path is added there is one obvious thing to return rather than a struct
// literal to get right from scratch.
func reached(updates []ManifestUpdate) ManifestResult {
	return ManifestResult{Updates: updates}
}

// evaluatedOnly keeps the targets the run established an outcome for and counts
// the ones it did not.
//
// # The unevaluated targets are DROPPED, not marked
//
// Keeping them with a flag would put them in front of every reader that already
// exists — the ok/failed counts, the rendered table, the JSON export — and each
// of those readers would then need to learn to skip them. One that did not would
// print a package the run never touched beside a verdict it never reached. The
// count travels on the result instead, where the single reader that needs it
// (the report envelope) is the only one that has to know about it at all.
//
// The order of what remains is the order of the input, because filtering cannot
// reorder: RegenerateManifests promises the caller's target order and an
// interrupted run keeps that promise over the shorter list.
func evaluatedOnly(updates []ManifestUpdate, evaluated []bool) (kept []ManifestUpdate, dropped int) {
	kept = make([]ManifestUpdate, 0, len(updates))
	for i := range updates {
		if evaluated[i] {
			kept = append(kept, updates[i])
		}
	}
	return kept, len(updates) - len(kept)
}

// runOneManifest performs the backup/regenerate/rollback dance for a single
// target and writes the outcome back into *u: Success always, and on failure
// also Err — the cause wrapped with %w and with this target's atom — and
// Output, the bytes its pkgdev printed. Those two are the target's share of
// what the run learned, and writing them here is what lets a report built after
// the loop say why a package failed without re-running it (S046-R5.1, R5.2).
//
// It is invoked from a worker goroutine; concurrent calls write to distinct
// slice indices, so no lock is required for the result and the added fields
// change nothing about that — they land in the same element as Success, never
// in shared state. Lifecycle events are emitted through rep, which is always
// non-nil (normalized by the caller) and goroutine-safe.
func runOneManifest(ctx context.Context, overlayPath, distdir, cacheDir string, u *ManifestUpdate, opts *ManifestOptions, rep tui.Reporter) {
	id := u.Category + "/" + u.Package
	rep.TaskStart(id, id)

	pkgPath := filepath.Join(overlayPath, u.Category, u.Package)
	manifestPath := filepath.Join(pkgPath, "Manifest")

	// Snapshot DIST filenames from the existing Manifest before any backup
	// move, so prepopulation works under both --keep and the default flow.
	// On error or missing Manifest, the slice is empty and prepopulation
	// degrades to a no-op.
	var distNames []string
	if cacheDir != "" {
		distNames = distfiles.ParseManifestDistFilenames(manifestPath)
	}

	var backupPath string
	if !opts.Keep {
		if _, statErr := os.Stat(manifestPath); statErr == nil {
			backupPath = manifestPath + ".bak"
			if mvErr := os.Rename(manifestPath, backupPath); mvErr != nil {
				u.fail(fmt.Errorf("failed to back up Manifest: %w", mvErr))
				// No captured output: pkgdev was never spawned, so there is
				// nothing the child printed to hand back. An empty Output here
				// means "no command ran", not "the command said nothing".
				rep.TaskDone(id, false, u.Error, "")
				return
			}
		}
	}

	// Stream pkgdev output through a StreamCapture: it tails live lines to the
	// reporter while keeping a verbatim copy for the error path (R7.1). Each
	// worker owns its own StreamCapture but they all forward to the same rep,
	// which is goroutine-safe (R7.4).
	sc := tui.NewStreamCapture(rep, id, tui.StreamStdout)
	if cacheDir != "" && len(distNames) > 0 {
		reused := distfiles.PrepopulateFromCache(distdir, cacheDir, distNames)
		u.Reused = reused
		if reused > 0 {
			fmt.Fprintf(sc, "[bentoo] reused %d distfile(s) from %s\n", reused, cacheDir)
		}
	}
	cmd := execCommand(ctx, "pkgdev", "manifest", "--distdir", distdir)
	cmd.Dir = pkgPath
	cmd.Stdout = sc
	cmd.Stderr = sc
	// Without this, cancelling the run does NOT end this call: exec kills the
	// direct child only, and cmd.Run keeps draining the capture pipe for as long
	// as any process pkgdev spawned still holds it. Measured at 30s against a
	// child that sleeps 30s, from a cancel delivered at 300ms — which is a run
	// that cannot report, because the report is assembled before it is rendered
	// (R1.3) and there is nothing to assemble until this returns. See
	// procgroup.KillGroupNow for what it does and what it costs.
	procgroup.KillGroupNow(cmd)

	runErr := cmd.Run()
	// StreamCapture.Close only flushes a trailing partial line to the reporter
	// and is documented to always return nil, so there is no failure here that
	// could belong in the target's outcome. Closing before Captured() is read is
	// what guarantees the last unterminated line is in the buffer.
	_ = sc.Close()
	if runErr != nil {
		// The captured output IS the diagnostic — pkgdev's own account of what
		// it could not fetch or verify — so it is attached to the target before
		// the reporter is told anything. The report the caller assembles later
		// then holds the same bytes the live region showed, instead of the live
		// region being the only place they ever existed (S046-R5.2).
		u.Output = sc.Captured()

		cause := runErr
		if backupPath != "" {
			if rbErr := os.Rename(backupPath, manifestPath); rbErr != nil {
				// Two %w verbs, not a formatted append: the rollback failure is
				// a SECOND thing that went wrong, not a replacement for the
				// first, and flattening either into text would leave a caller
				// unable to match it. Both stay reachable through errors.Is,
				// and the rendered sentence is byte-for-byte the one this path
				// has always produced.
				cause = fmt.Errorf("%w; rollback failed: %w", runErr, rbErr)
			}
		}
		u.fail(cause)
		rep.TaskDone(id, false, u.Error, u.Output)
		return
	}

	if backupPath != "" {
		if rmErr := os.Remove(backupPath); rmErr != nil {
			// The regeneration SUCCEEDED; only the housekeeping did not. Marking
			// the target failed would report a good Manifest as a bad one, and
			// dropping the error would leave a stale .bak beside it with nothing
			// said anywhere. So it goes to the reporter — the sink this run's
			// caller wired for exactly these events — rather than into u.Err,
			// which is reserved for the failures Success already announces and
			// which a successful target must leave nil.
			rep.Log("warn", fmt.Sprintf("%s: the Manifest backup %s could not be removed: %v", id, backupPath, rmErr))
		}
	}
	u.Success = true
	rep.TaskDone(id, true, "", sc.Captured())
}

// RegenerateManifestsForScope is a convenience wrapper that resolves a scope
// and runs RegenerateManifests.
func RegenerateManifestsForScope(cfg *config.Config, scope ManifestScope, opts *ManifestOptions) (*ManifestResult, error) {
	if cfg == nil {
		return nil, ErrOverlayPathNotSet
	}
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}
	targets, err := ResolveManifestTargets(overlayPath, scope)
	if err != nil {
		return nil, err
	}
	// Taken whole rather than re-wrapped around its Updates: the gap an
	// interrupted run left is on the value the run returned, and rebuilding the
	// struct from one field would drop it silently — the caller would receive a
	// short list with nothing to say why.
	result := RegenerateManifests(overlayPath, targets, opts)
	return &result, nil
}

// FormatManifestResult renders a ManifestResult for display.
//
// It has had NO production caller since sub-task 5.2 moved `overlay manifest`
// onto the report envelope: the counts are values the run hands back
// (ManifestResult.Ok/Failed) and the rendering moved to internal/common/report
// (S046-R5.1). Nothing outside its own three tests calls it.
//
// It stays because four comments elsewhere cite what it DOES as the canonical
// reading, and deleting the function would leave all four pointing at nothing:
//
//   - rename.go, on ManifestUpdate.Error — the field carries no atom because
//     this formatter and FormatRenameResult both write "<category>/<package>: "
//     immediately before it, so an atom in the field would print twice.
//   - cmd/bentoo/overlay_manifest.go — records logger.Info over this function
//     as the sentence story 046 replaced.
//   - cmd/bentoo/overlay_manifest_report.go, on buildManifestReport — cites the
//     dry-run branch below being answered BEFORE any count is taken as the
//     order that avoids reporting every previewed package as failed.
//   - the same comment, on a nil result — cites the reading a nil gets here,
//     "No packages processed" and not a crash, beside the one
//     report.Run.Sections gives a nil payload.
//
// So removing it is a change to those four comments first and to this function
// second; the orphaning is recorded rather than acted on.
func FormatManifestResult(result *ManifestResult, dryRun bool) string {
	var sb strings.Builder

	if result == nil || len(result.Updates) == 0 {
		return "No packages processed"
	}

	if dryRun {
		fmt.Fprintf(&sb, "Dry run: %d package(s) would have Manifest regenerated\n\n", len(result.Updates))
		for _, u := range result.Updates {
			fmt.Fprintf(&sb, "  %s/%s\n", u.Category, u.Package)
		}
		return sb.String()
	}

	// Counted by the result, not by a loop of its own. This function was the
	// third place in the package that summed the same slice, and three
	// independent counts of one thing are three chances for the header to
	// disagree with the failure list printed underneath it.
	failed := result.Failed()
	fmt.Fprintf(&sb, "Manifest regeneration: %d succeeded, %d failed (of %d)\n",
		result.Ok(), failed, len(result.Updates))

	if failed > 0 {
		sb.WriteString("\nFailures:\n")
		for _, u := range result.Updates {
			if !u.Success {
				fmt.Fprintf(&sb, "  %s/%s: %s\n", u.Category, u.Package, u.Error)
			}
		}
	}

	return sb.String()
}

// isPackageDir reports whether the path looks like a valid package directory
// (exists, is a directory, contains at least one .ebuild file).
func isPackageDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ebuild") {
			return true
		}
	}
	return false
}
