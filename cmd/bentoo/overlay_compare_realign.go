package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// This file is the whole of what `--realign` adds to a shipped command, and it
// is a separate file so that the promise underneath it stays checkable: WITHOUT
// `--realign` nothing here runs, so `overlay compare` prints exactly what it
// printed yesterday and exits exactly as it did (R7.2).
//
// That promise needs a mechanism rather than a good intention, and the mechanism
// is not in this file — it is in internal/overlay, where every field the review
// writes renders nothing at its zero value. FormatReport takes a *CompareReport
// and never learns which flags were passed (overlay_compare.go:408-413 says so
// outright), so a gate in the renderer was never available. What this file owns
// is the other half: the passes that FILL those fields are called from exactly
// one place, behind exactly one condition, and `IncludeNotInRemote` is switched
// on there and nowhere else — unconditionally it would give the 84 Bentoo-only
// packages rows they do not have today and change `counted - len(report.Results)`
// under everyone (D1).
//
// NOTHING HERE WRITES A FILE. The candidate declarations and the baseline text a
// model proposes are printed for a maintainer to paste: the overlay repository
// auto-commits and pushes within minutes, so a declaration this program wrote
// would be a PUBLISHED one before anyone had read it (R3.6, R4.2).

// realignBaselineRepo is the ONE repository a baseline review may read from.
//
// It is a constant rather than a parameter because the alternative is incoherent
// rather than merely unsupported: reading the baseline from ::gentoo while
// comparing against GURU would print two repositories' answers on one line and
// name neither. realignPreflight refuses that invocation instead, which is why
// internal/overlay can hold the same constant with no way to reach it from
// outside (baseline.go's baselineRepo).
const realignBaselineRepo = "gentoo"

// realignPreflight reports why THIS INVOCATION cannot carry a baseline review,
// or nil when it can (R7.4).
//
// # It is a returned value, not a log line, and that is the point
//
// logger binds its io.Writer at first use and exposes no setter
// (logger.go:44-52), so a refusal written there cannot be read by anything but a
// human watching a terminal. R7.4 requires the reason to be NAMED, and a returned
// error is the only shape in which the reason can be asserted, wrapped, or
// printed by a caller that knows where its output goes.
//
// # Both refusals are usage errors, and neither is a SKIPPED run
//
// A SKIPPED run says "we looked and could not see"; these say "the request itself
// was impossible". Nothing has been examined at the point this is called, and the
// operator asked for something this invocation cannot do:
//
//   - ANOTHER REPOSITORY. `compare guru --realign` would run the comparison
//     against GURU while every baseline was read from ::gentoo (R6.2 keeps
//     other repositories informative precisely because they have not been through
//     the same review).
//   - AN API-ONLY PROVIDER. Reading ebuild TEXT needs the tree on this machine,
//     which is the capability provider.PackageDirProvider names and the API
//     providers deliberately do not satisfy. `compare` itself still works
//     perfectly well over the API; only the review cannot.
//
// It reads nothing and touches no disk: it is two questions about the arguments
// it was handed, which is what lets the caller refuse before spending the run.
//
// _Requirements: R7, R7.4_
func realignPreflight(repoName string, prov provider.Provider) error {
	if !strings.EqualFold(strings.TrimSpace(repoName), realignBaselineRepo) {
		return fmt.Errorf(
			"--realign reads every baseline from ::%s, so it cannot run against ::%s: the comparison would report ::%s's versions beside a baseline read from a repository it was never compared with. Drop the repository argument to review against ::%s, or drop --realign to compare against ::%s exactly as before",
			realignBaselineRepo, repoName, repoName, realignBaselineRepo, repoName)
	}

	// The type assertion IS the question, and it answers for a nil provider too:
	// a nil interface satisfies nothing, so the name below is resolved defensively
	// rather than by dereferencing it.
	if _, ok := prov.(provider.PackageDirProvider); !ok {
		return fmt.Errorf(
			"--realign reads ::%s's ebuild text, which needs its tree on this machine, and %s answers over an API instead: configure it in ~/.config/bentoo/config.yaml as `provider: local` with the path of a synced tree, or pass --clone. `bentoo overlay compare` itself is unaffected and still compares over the API",
			realignBaselineRepo, realignProviderName(prov))
	}
	return nil
}

// realignProviderName names the provider in a refusal, and never dereferences a
// nil one. "no provider at all" is unreachable from runCompare, which refuses
// long before this on a provider it could not build; it exists so the refusal
// stays a refusal rather than becoming a panic if a second caller appears.
func realignProviderName(prov provider.Provider) string {
	if prov == nil {
		return "no provider at all"
	}
	return prov.GetName()
}

// realignBaselineTreeCandidate names the directory the review will look for the
// ::gentoo repository in, or "" when this invocation names none.
//
// The two sources are the two ways a local tree reaches this command, and they
// are asked in the order of how explicit they are. An operator who configured
// `provider: local` NAMED the tree, and that is the path the report must speak
// about — R1.5 asks the review to say what it looked for, and a path the operator
// recognises is the only kind that can be acted on. A `--clone` run named no
// path, so the clone's own cache directory is the honest answer, taken from the
// provider that owns it rather than spelled a second time here.
//
// It resolves a path and stats nothing: whether a repository is actually there is
// LocateBaselineTree's question, asked once, in one place, against Portage's own
// marker.
func realignBaselineTreeCandidate(repoInfo *provider.RepositoryInfo, prov provider.Provider) string {
	if repoInfo != nil {
		if path := strings.TrimSpace(repoInfo.Path); path != "" {
			return path
		}
	}
	if clone, ok := prov.(*provider.GitCloneProvider); ok {
		return clone.LocalPath
	}
	return ""
}

// realignBaselineIsLocatable answers the run-level question R1.5 asks — is there
// a ::gentoo repository to read at all — and records the answer on the report
// when there is not.
//
// It is the ONE condition the command exits non-zero for (D9). Everything else
// the review can fail at is a per-package value: a package ::gentoo does not
// carry is Baseline.Found false, a baseline that will not read is
// Baseline.Unexamined, and an unreachable model leaves an empty verdict. None of
// those reaches an exit code, because an overlay of a derived distribution has
// divergences by definition and an exit code that counted them would be non-zero
// forever and ignored within a week.
//
// The path LocateBaselineTree resolves is deliberately DROPPED. AnnotateBaseline
// reaches the tree through the provider (baselineTreeOf) rather than through a
// path of its own, and a second way to name it would be a second thing to
// disagree with the first. What this call adds is the question the provider
// cannot answer: LocalPackagePath is happy with any directory, while
// /var/db/repos/gentoo on a machine that has never synced it would report all 321
// packages as absent from ::gentoo — 321 packages presented as Bentoo's own work,
// by a run that exited 0 and said nothing.
//
// _Requirements: R1, R1.5, R7, R7.5_
func realignBaselineIsLocatable(report *overlay.CompareReport, candidate string) bool {
	_, err := overlay.LocateBaselineTree(candidate)
	switch {
	case err == nil:
		return true
	case errors.Is(err, overlay.ErrNoBaselineTree):
		overlay.MarkBaselineSkipped(report, candidate)
		return false
	default:
		// Unreachable today: LocateBaselineTree wraps the sentinel into every
		// refusal it has. Reported rather than dropped, and treated as the same
		// outcome, because it is the same outcome — no tree was located, whatever
		// the reason, and the report must not claim otherwise.
		logger.Warn("locating the ::%s tree at %s: %v", realignBaselineRepo, candidate, err)
		overlay.MarkBaselineSkipped(report, candidate)
		return false
	}
}

// annotateOtherRepositories fills in, for every package ::gentoo carries no
// version of, which other repositories carry it (R6.1).
//
// AnnotateBaseline is deliberately not given the repository list and does not go
// looking for one, so this is the caller's half of task 6.2: internal/overlay
// answers "does this repository carry it", and the command decides which
// repositories the report may speak about at all.
//
// IT IS GATED ON A MISSING BASELINE, and that gate is the requirement rather than
// an optimisation. A row saying another repository also carries a package we
// already measured against ::gentoo answers a question nobody asked, and it would
// sit directly beneath the baseline line contradicting its own "never a baseline"
// caveat. R6.1 is scoped to WHERE no ::gentoo baseline exists — 84 of the
// overlay's 321 packages.
//
// _Requirements: R6, R6.1_
func annotateOtherRepositories(report *overlay.CompareReport, repos []overlay.LocalRepo) {
	if report == nil || len(repos) == 0 {
		return
	}
	for i := range report.Results {
		// Indexed rather than ranged over a copy: this writes back onto the report
		// the caller is holding, exactly as the annotation passes it follows do.
		r := &report.Results[i]
		if r.Baseline.Found {
			continue
		}
		r.Others = overlay.OtherRepositories(r.Category+"/"+r.Package, repos)
	}
}

// realignLocalRepos is the list of repositories the report may speak about: the
// ones the OPERATOR configured, each with an honest answer about whether its
// contents are on this machine.
//
// THE REGISTRY IS NOT CONSULTED, and that is the whole design of it. The ~428
// repositories of the Gentoo ecosystem are resolvable by name, and passing all of
// them would print one "NOT checked" row per repository per package — some 35,000
// lines over the 84 packages with no baseline — while resolving their contents
// would be thousands of network lookups in a stage this story promises makes
// none (R6.1). What the operator wrote in config.yaml is the set they meant the
// tool to know about.
//
// The order is SORTED rather than map order, so two runs over one configuration
// print the same report. Map iteration would reorder the rows of every package on
// every run and make the output undiffable.
//
// _Requirements: R6, R6.1_
func realignLocalRepos(cfg *config.Config) []overlay.LocalRepo {
	if cfg == nil || len(cfg.Repositories) == 0 {
		return nil
	}

	names := make([]string, 0, len(cfg.Repositories))
	for name := range cfg.Repositories {
		names = append(names, name)
	}
	sort.Strings(names)

	repos := make([]overlay.LocalRepo, 0, len(names))
	for _, name := range names {
		repos = append(repos, realignLocalRepo(name, cfg.Repositories[name]))
	}
	return repos
}

// realignLocalRepo answers, for ONE configured repository, whether its contents
// are on this machine and where.
//
// Two sources, and a third answer. A `provider: local` entry names its tree
// directly. An entry with a remote URL may still have been cloned by an earlier
// `compare <name> --clone`, and that cache tree is as readable as any other. Any
// repository neither of those finds is available=false — REGISTERED, CONTENTS NOT
// HERE — which OtherRepositories reports as NOT CHECKED rather than as one that
// does not carry the package. Those are different answers and only one of them is
// true.
//
// A nil entry — what a map of pointers hands back for a key that is not there,
// and what a `repositories:` block with a bare name under it parses to — is the
// same third answer. It is a repository the operator named and said nothing else
// about, which is precisely "registered, contents not here".
func realignLocalRepo(name string, repo *config.RepoConfig) overlay.LocalRepo {
	if repo == nil {
		return overlay.LocalRepo{Name: name}
	}
	if path := strings.TrimSpace(repo.Path); realignIsTree(path) {
		return overlay.LocalRepo{Name: name, Path: path, Available: true}
	}
	if path := realignClonedTree(name, repo); path != "" {
		return overlay.LocalRepo{Name: name, Path: path, Available: true}
	}
	return overlay.LocalRepo{Name: name}
}

// realignClonedTree names the tree an earlier `compare <name> --clone` left on
// disk, or "" when there is none.
//
// It asks the PROVIDER where it puts a clone rather than spelling that cache
// layout a second time here: NewGitCloneProvider resolves a path, validates the
// URL and branch, and does nothing else — it clones nothing, opens nothing and
// reaches no host, so building one to read a path costs a filepath.Join and
// cannot drift from where the real clone lands.
func realignClonedTree(name string, repo *config.RepoConfig) string {
	if repo == nil || strings.TrimSpace(repo.URL) == "" {
		return ""
	}
	clone, err := provider.NewGitCloneProvider(&provider.RepositoryInfo{
		Name:   name,
		URL:    repo.URL,
		Branch: repo.Branch,
	})
	if err != nil {
		// A URL or branch this program refuses to clone from is one no cache tree
		// was ever built for. Nothing to report and nothing to warn about: the
		// comparison itself would have failed on the same value long before here.
		return ""
	}
	if !realignIsTree(clone.LocalPath) {
		return ""
	}
	return clone.LocalPath
}

// realignIsTree reports whether path is a directory that exists. It is the
// caller's half of LocalRepo.Available, which the type's own comment insists is
// the CALLER'S ANSWER and never a guess made inside the report.
func realignIsTree(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// compareRealignReviewer answers "what realignment reviewer should this run use?"
// with the one value AnnotateRealignVerdicts needs: a RealignReviewer, or nil.
//
// The two ways of having none — `--no-review` (R5.6) and a machine with no
// `claude` on PATH (R5.5) — both end here as a nil INTERFACE, which is why that
// pass needs no second parameter and this command needs no second condition that
// could disagree with it. It mirrors compareDivergenceReviewer exactly, including
// the reason `--no-review` returns BEFORE the seam: "contact no model" means
// nothing is constructed, no PATH is consulted and no process is spawned.
//
// _Requirements: R4, R4.1_
func compareRealignReviewer(ctx context.Context, noReview bool) overlay.RealignReviewer {
	if noReview {
		return nil
	}

	reviewer, err := newRealignReviewer(ctx)
	if err != nil {
		// The error is an ARGUMENT and never a format string: it may carry the
		// CLI's own text.
		reviewWarnf("the realignment review could not be started (%v); every divergence is reported without a verdict and the rest of the report is unchanged", err)
		return nil
	}
	return reviewer
}

// newRealignReviewer builds the adapter over the `claude` CLI.
//
// AN ABSENT CLI IS (nil, nil), on exactly the terms newDivergenceReviewer states:
// absence is not a failure, because the deterministic half of the review — the
// baseline, the structural axes, the declarations, the reduction — is complete
// without a model. Every OTHER construction failure is (nil, err), because "you
// do not have this" and "you have it and it would not start" want different
// things done about them.
//
// It reuses newClaudeAsker, the same seam the divergence review is built through,
// so `--no-review` reaching it zero times stays ONE assertable property rather
// than two that could disagree.
func newRealignReviewer(ctx context.Context) (overlay.RealignReviewer, error) {
	asker, err := newClaudeAsker(ctx)
	if err != nil {
		if errors.Is(err, autoupdate.ErrClaudeCodeUnavailable) {
			return nil, nil
		}
		return nil, err
	}
	if asker == nil {
		// Unreachable from the seam above, and cheap insurance against a later one:
		// an adapter wrapping a nil asker is a non-nil RealignReviewer that panics
		// on first use.
		return nil, nil
	}
	return &claudeRealignReviewer{asker: asker}, nil
}

// claudeRealignReviewer puts one undeclared divergence to the model and turns the
// reply back into a note.
type claudeRealignReviewer struct {
	asker claudeAsker
}

var _ overlay.RealignReviewer = (*claudeRealignReviewer)(nil)

// ReviewRealignment asks whether one divergence is still justified, and for the
// ::gentoo text that would replace it when it is not (R4.1, R4.2).
//
// IT NEVER WARNS and it never writes. Every failure is RETURNED, and
// AnnotateRealignVerdicts turns the first of each kind into one warning and
// counts the rest — which is the half this side cannot know. It grants the model
// no capability that can modify a file (R4.5): AskJSON pins `--allowedTools ""`
// on every invocation, so the CLI answers from what is on stdin and has no tool
// to reach a file with at all.
//
// The context is checked on ENTRY and then not again, exactly as
// claudeDivergenceReviewer explains: the deadline that bounds the call belongs to
// the client, and what the entry check buys is the interrupted run — after
// Ctrl-C the remaining packages fail here instead of each spawning a process that
// is about to be killed.
func (r *claudeRealignReviewer) ReviewRealignment(ctx context.Context, req overlay.RealignRequest) (overlay.RealignNote, error) {
	if err := ctx.Err(); err != nil {
		return overlay.RealignNote{}, fmt.Errorf("the realignment review was not started: %w", err)
	}

	reply, err := r.asker.AskJSON(realignReviewInstruction(req), realignReviewPayload(req), realignReviewSchema)
	if err != nil {
		return overlay.RealignNote{}, fmt.Errorf("the claude CLI could not read the two ebuilds: %w", err)
	}

	// Decoded straight into the consumer's own type: RealignNote carries the three
	// lowercase json tags, and the verdict cache persists the same spelling, so one
	// name serves the model's reply, the cache's file and this decode.
	var note overlay.RealignNote
	if err := json.Unmarshal([]byte(unfenceJSON(reply)), &note); err != nil {
		// The reply is NOT included in the error. It is model-written text of
		// unbounded length and the caller prints this on a terminal line.
		return overlay.RealignNote{}, fmt.Errorf("the model's reply is not the JSON the realignment review asked for: %w", err)
	}
	return note, nil
}

// realignReviewSchema is the shape the CLI is constrained to via --json-schema.
// It describes exactly what RealignNote holds and nothing else.
//
// `why` is REQUIRED beside `justified` because a bool has no "nobody said" value:
// without a reason an empty reply is indistinguishable from a considered "not
// justified", and that particular default is the expensive one — it is the
// verdict that invites a realignment. A reason is also what makes the verdict
// checkable at all, which is why realignNoteSpeaks treats a note without one as
// no note.
//
// `baseline_text` is not required: it is empty by construction for a divergence
// the model considers justified, and requiring it would invite one to be invented.
const realignReviewSchema = `{
  "type": "object",
  "properties": {
    "justified": {"type": "boolean"},
    "why": {"type": "string"},
    "baseline_text": {"type": "string"}
  },
  "required": ["justified", "why"]
}`

// realignReviewInstruction is the static instruction, the value of -p.
//
// IT CARRIES NO EBUILD CONTENT. The two files are piped on stdin because argv is
// world-readable through /proc/<pid>/cmdline and an ebuild is arbitrary shell —
// the same rule autoupdate applies to page content (AD8).
//
// IT CARRIES NOTHING DERIVED FROM THE SIZE OF THE DIFFERENCE, on the fence
// compare_diff_counts_fence_test.go holds: a prompt built from the line counts
// would be a computation on the size of a diff wearing a different hat.
//
// IT ALSO CARRIES NO DISTANCE. How far the baseline is from our version is a
// deterministic answer this program already has and already prints beside the
// verdict (baselineDistanceProse), so the operator reads it from the report
// rather than from a model that was told it.
//
// The question is deliberately narrow: is the divergence still justified, and
// what would replace it. Whether to keep, remove or rebase the package is the
// report's decision and stays the report's (R4.3) — a model asked for that too
// would produce an opinion the operator has to argue with rather than an input
// they can check.
func realignReviewInstruction(req overlay.RealignRequest) string {
	var sb strings.Builder

	sb.WriteString("Read the two Gentoo ebuilds piped on stdin and judge whether the difference between them is still justified.\n\n")
	sb.WriteString("Each file follows a header line: first the overlay's copy (\"ours\"), then the ::gentoo ebuild it is measured against (\"the baseline\"). ")
	sb.WriteString("They may be at DIFFERENT versions, so part of the difference may be nothing but the version move between them.\n\n")

	// Context for reading the files — a category tells the model which eclasses and
	// conventions to expect. Deliberately not something to repeat back: the verdict
	// cache is keyed on the two files' content and excludes the atom, so a verdict
	// that named a package would be wrong the day two packages carried identical
	// ebuilds.
	fmt.Fprintf(&sb, "For context while reading them, ours is %s/%s at version %s. Do not repeat that back.\n\n",
		req.Category, req.Package, req.Version)

	sb.WriteString("Answer as the JSON object the schema describes and nothing else.\n\n")

	sb.WriteString("justified - true when what ours carries on top of the baseline still earns its place, ")
	sb.WriteString("false when the baseline has since absorbed it, replaced it, or made it unnecessary.\n\n")

	sb.WriteString("why - ONE line of plain English giving the reason. ")
	sb.WriteString("Name the eclasses, variables, dependencies, USE flags, patches or phase functions involved. ")
	sb.WriteString("A verdict with no reason cannot be checked or argued with, so it is discarded.\n\n")

	sb.WriteString("baseline_text - only when justified is false: the baseline's own lines that would replace the divergence, verbatim. ")
	sb.WriteString("Leave it empty when the divergence is justified, and leave it empty rather than inventing text the baseline does not contain.\n\n")

	sb.WriteString("Decide nothing else. Do not recommend keeping, removing or rebasing the package, ")
	sb.WriteString("do not say how much changed, and do not write to any file - everything you need is on stdin.")

	return sb.String()
}

// realignReviewPayload is what goes on the CLI's stdin: both ebuilds, whole and
// verbatim, ours first.
//
// THE ORDER IS THE QUESTION'S MEANING. The judgement is about what OURS carries
// on top of the baseline, so a payload with the two swapped would invert it —
// the same reason the verdict cache's fingerprint does not commute.
//
// The headers are plain ASCII rules rather than a markup a model might imitate in
// its reply, and each file is written whole through appendEbuild: a truncated
// ebuild would be a reading of something other than the divergence the report
// found.
func realignReviewPayload(req overlay.RealignRequest) []byte {
	var buf []byte
	buf = append(buf, "--- OUR EBUILD (the bentoo overlay's copy) ---\n"...)
	buf = appendEbuild(buf, req.Ours)
	buf = append(buf, "--- THE BASELINE EBUILD (::gentoo's copy this one is measured against) ---\n"...)
	buf = appendEbuild(buf, req.Baseline)
	return buf
}

// realignAddendum is what a review run prints AFTER the report: the state of the
// verdicts when no model produced any, and the candidate declarations a
// maintainer is invited to paste.
//
// It is printed beside the report rather than inside it because FormatReport
// takes a *CompareReport and never learns which flags were passed — the same
// constraint that put the whole `--realign` gate in this file. Both halves render
// "" for a run with nothing to say, so a review that judged everything and
// proposed nothing appends nothing at all.
//
// _Requirements: R3, R3.5, R4, R4.4_
func realignAddendum(report *overlay.CompareReport, judged, noReview bool) string {
	if report == nil {
		return ""
	}

	var sb strings.Builder
	if !judged {
		sb.WriteString(realignNoVerdictNotice(noReview))
	}
	sb.WriteString(realignCandidateSection(report.Results))
	return sb.String()
}

// realignNoVerdictNotice says that NO verdict was produced, and why (R4.4).
//
// It exists because the report cannot say it. formatRealignSummary states how
// many divergences the model was ASKED about and did not judge, which is the
// right sentence when a model answered some of them — but with no reviewer at all
// nothing is asked, both counters stay zero, and the report renders silence.
// Silence here reads as "every divergence was judged and none objected", which is
// exactly the reading R4.4 exists to prevent: a divergence with no verdict and a
// divergence judged justified look identical otherwise.
//
// It prints on a run that produced no verdicts even when nothing needed judging.
// The predicate that decides who the model is asked about lives in
// internal/overlay and is unexported on purpose, and a second spelling of it here
// would be a second answer to one question — free to drift, and wrong in the
// direction that hides things. The line is one sentence, it is never false, and
// the worst it costs is a reader learning that a review they asked for reached no
// model.
//
// _Requirements: R4, R4.4_
func realignNoVerdictNotice(noReview bool) string {
	reason := "no model was reachable"
	if noReview {
		reason = "--no-review contacted no model"
	}
	return output.Sprintf(output.Warning,
		"\nRealignment verdicts: none was produced — %s, so every divergence above carries no verdict, and an unjudged divergence is not a justified one. Everything above was established by reading files and stands without a model.\n",
		reason)
}

// realignCandidateSection renders the candidate declarations, or "" when there
// are none to propose (R3.5).
//
// It is text and it is only text (R3.6). The overlay repository auto-commits and
// pushes within minutes, so a declaration this program wrote into an ebuild would
// be a published one with nobody having read it; what a candidate saves is the
// transcription, and that is enough.
//
// CandidateDeclarationsWithVerdict is preferred over the plain
// CandidateDeclarations because it degrades to exactly that when no verdict
// exists — every `--no-review` run, and every run on a machine with no model —
// so one call site serves both and there is no condition here to get wrong.
//
// _Requirements: R3, R3.5, R3.6, R4.1_
func realignCandidateSection(results []overlay.CompareResult) string {
	var body strings.Builder
	for _, r := range results {
		candidates := overlay.CandidateDeclarationsWithVerdict(r)
		if len(candidates) == 0 {
			continue
		}
		body.WriteString(output.Sprintf(output.Info, "\n  %s/%s:\n", r.Category, r.Package))
		for _, block := range candidates {
			body.WriteString(realignIndentedBlock(block))
		}
	}

	if body.Len() == 0 {
		return ""
	}
	return output.Sprintf(output.Info,
		"\nCandidate declarations — one per undeclared axis, for a maintainer to accept or edit. Nothing here was written to any file.\n") +
		body.String()
}

// realignIndentedBlock indents one candidate block under the package it belongs
// to, so several packages' candidates stay legible in one list.
//
// The indent does not cost the block its promise of pasting back as one
// well-formed declaration: the parser trims a comment line before reading it
// (divergenceComment), so leading whitespace is not part of the tag. It is passed
// as an ARGUMENT and never as a format string, like every other piece of text
// this command prints — a candidate is built from an ebuild's own text and a
// model's words.
func realignIndentedBlock(block string) string {
	var sb strings.Builder
	for _, line := range strings.Split(block, "\n") {
		sb.WriteString("    " + line + "\n")
	}
	return sb.String()
}

// reportSkippedBaseline names the run-level SKIPPED outcome on the ONE path the
// renderer cannot reach (R1.5).
//
// FormatReport opens with that line precisely so it can correct the sentence
// below it — "All packages are up-to-date!" is exactly how "we could not look"
// must never render — but a report with no rows returns before FormatReport is
// ever called, and reportEmptyCompare prints that same claim from here. So this
// says it where it would otherwise be lost. It is reachable only from a
// `--realign` run, since nothing else can set the field.
func reportSkippedBaseline(report *overlay.CompareReport) {
	if report == nil || report.BaselineSkipped == "" {
		return
	}
	// An ARGUMENT and never a format string: the text names an operator-configured
	// path.
	logger.Warn("%s", report.BaselineSkipped)
}

// exitOnSkippedBaseline is R7.5 and D9 in one place: the command's exit code
// reports the REVIEW's own outcome and nothing about the overlay's shape.
//
// BaselineSkipped is the only state it reads, and MarkBaselineSkipped is the only
// thing that writes it, so no count of divergences can reach an exit code through
// here even by accident. An overlay of a derived distribution has divergences by
// definition; an exit code that counted them would be non-zero forever and
// ignored within a week.
//
// Everything else stays 0 by construction: an unreachable model
// (AnnotateRealignVerdicts returns nothing to exit on), a baseline ebuild that
// will not read (a per-package Unexamined state), and a review that found every
// divergence justified are all successful runs that have something to say.
//
// _Requirements: R7, R7.5_
func exitOnSkippedBaseline(report *overlay.CompareReport) {
	if report == nil || report.BaselineSkipped == "" {
		return
	}
	osExit(1)
}
