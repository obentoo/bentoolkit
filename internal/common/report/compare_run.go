package report

import (
	"fmt"
	"slices"
	"strings"
)

// This file declares the payload for `overlay compare`, renders it, and carries
// GroupKeep, the rule that fills its keep groups — the one-file-per-payload rule
// manifest_run.go states. It adds no key at the root of an exported document
// and changes no existing one, so SchemaVersion stays 2 and this fifth kind
// costs a consumer no migration.
//
// TestPackageDeclaresNoPresentationField walks every struct field in this
// package, so the types below are inspected: renaming KeepGroup.ByCategory to
// CategoryIndent fails it with a message naming this file, the type and the
// field.

// CompareRun is everything one `overlay compare` run established: the
// repository the overlay was compared against, how much was scanned and how
// much the two trees have in common, what the run recommends for each package
// it has an opinion about, and the tally that summarizes the lot.
// It is the Payload behind the `overlay.compare` kind.
//
// Every field is a primitive because this package may not import
// internal/overlay (boundary_test.go's forbiddenImports lists it): conversion
// happens once, at the adapter in cmd/bentoo, and internal/overlay's enum
// vocabulary never becomes a wire contract that changes without notice.
//
// KeepGroups is a FIELD, never something Sections computes: a group computed at
// render time would exist on the terminal and nowhere in the exported file.
//
// Whether the run finished is the envelope's (Run.Complete, Run.NotEvaluated),
// never restated here. A compare run falls short two ways — interrupted, or
// finished while failing to establish facts: a review killed, an upstream
// lookup that errored, a pair the content check refused — and the adapter adds
// both up; nothing here re-derives either from the counts below.
type CompareRun struct {
	// Repository is the repository the overlay's packages were compared
	// against, in the operator's own words — "gentoo" for the ::gentoo tree.
	//
	// It is carried rather than assumed because a report is read after the run
	// that produced it is gone, and "outdated" means nothing without the tree
	// it is outdated against. Nothing here abbreviates it, decorates it with
	// the :: form or capitalises it: a repository name travels as the producer
	// spelled it, and every renderer prints the same string.
	Repository string `json:"repository"`
	// Scanned is how many packages the run read out of the overlay, InBoth how
	// many of those the compared repository also carries, and OnlyLocal how
	// many it carries no version of at all.
	//
	// They are FIELDS because a program holding the exported file cannot call a
	// method, and none is derived from another: the producer counts them on
	// different axes, so a subtraction here would invent an answer the run never
	// gave. A filter flag narrows none of them — a filtered row was evaluated,
	// the operator merely chose not to look at it.
	//
	// None carries omitempty: a dropped zero reads as "the producer never said",
	// and "the remote carries every package we do" is the healthy case that
	// would lose its zero.
	Scanned   int `json:"scanned"`
	InBoth    int `json:"in_both"`
	OnlyLocal int `json:"only_local"`
	// Redundant is every package the run recommends removing: the overlay's
	// copy adds nothing the compared repository does not already carry.
	//
	// It is the list an operator acts on destructively, which is why the entry
	// beside it carries a reading state and a diff cell rather than a bare
	// verdict — a recommendation to delete is only as good as the evidence that
	// somebody looked.
	//
	// A nil slice reaches the JSON export as null and an empty one as [], and
	// the two say different things: the first is a producer that established
	// nothing, the second a run that found nothing redundant. The renderer
	// rewrites neither into the other, so the adapter normalises every slice
	// here to non-nil and a consumer never has to tell "no rows" from "the
	// producer forgot".
	Redundant []ComparePkg `json:"redundant"`
	// NeedsRebase is every package ::gentoo moved ahead of while the overlay
	// carries changes of its own, so ours owes a bump AND a re-application of
	// the delta.
	//
	// It is its own list because its advice is its own: folding it into
	// Redundant would recommend deleting changes ::gentoo has no copy of — the
	// most expensive mistake this report exists to prevent — and folding it
	// into Keep would report the copy as earning its place while it falls
	// behind. `overlay compare` already printed this table (verdictSections in
	// internal/overlay/compare.go), so leaving the field out would have been a
	// regression.
	//
	// Nil versus empty says what it says on Redundant above.
	NeedsRebase []ComparePkg `json:"needs_rebase"`
	// Keep is every package the run recommends keeping, in the order the run
	// established them.
	//
	// It is the longest list a healthy overlay produces and the one KeepGroups
	// below compresses. The two are not alternatives: this holds every member,
	// one entry each, whatever the groups say, so a consumer that ignores the
	// groups still reads a complete list and a consumer that reads the groups
	// is not left re-deriving the membership it was handed.
	//
	// The ORDER is information rather than presentation, and nothing here or
	// downstream sorts it. What order the run established is the run's to
	// decide; a renderer that re-sorted would make two modes disagree about
	// what one run said.
	Keep []ComparePkg `json:"keep"`
	// KeepGroups is the sets of kept packages that share one version pair, as
	// the run established them.
	//
	// It is a field because it is the only place the exported document says a
	// large block of entries moves together; re-deriving it from the flat list
	// means re-implementing the rule — which pair counts, whether a package
	// carrying a finding may be absorbed, how ties break — and two
	// implementations of one rule is how a terminal and a file disagree.
	//
	// A group is never a group of one: grouping compresses repetition rather
	// than giving a single package a second name. Groups are ordered by member
	// count descending, ties broken by the first member's atom, so two runs over
	// one overlay produce byte-identical output.
	//
	// GroupKeep fills it, and it is the ADAPTER that calls GroupKeep, never
	// Sections: a group computed while rows are drawn would exist on a terminal
	// and nowhere in the exported file.
	KeepGroups []KeepGroup `json:"keep_groups"`
	// Unknown is every package the run reached no recommendation about — the
	// comparison failed, the remote answered nothing usable, or the package's
	// state was never recorded.
	//
	// It is deliberately its own list rather than an absence. A package that
	// fell out of the comparison and a package the comparison had no opinion
	// about would otherwise both be "not in any list", and only one of those is
	// something an operator should look at. Its entries carry the reason the
	// run could not answer, which is what turns a bucket into a finding.
	Unknown []ComparePkg `json:"unknown"`
	// Verdicts is the four-column tally: how many packages the run recommends
	// keeping, removing, rebasing, and how many it could not judge.
	//
	// It is the payload's own count and not the envelope's, which is the
	// asymmetry Run's doc comment states from the other side: "the run reached
	// the end of its plan" is answerable for any batch, while a tally is
	// counted in each command's own vocabulary and a universal one would either
	// lose a column or invent columns another command has no answer for.
	//
	// It is NOT derivable from the four slices above, and that is the point of
	// declaring it. Every scanned package carries a verdict, while the lists
	// hold only the packages that also exist in ::gentoo — 99 of the measured
	// run's 279 have no row in any of them — so a consumer summing the lists
	// answers a number smaller than the run's and has nothing to tell it so.
	// The summary section states that coverage gap in words for the same
	// reason.
	Verdicts VerdictTally `json:"verdicts"`
	// Unread is how many comparisons nobody read: a review that was never
	// requested, one that was attempted and failed, or a pair the content check
	// refused to compare.
	//
	// It is a run-level count because the per-package fact is already on each
	// entry (ComparePkg.Reading) and a total is what an operator reads before
	// deciding whether to trust the lists at all. "We checked and it matches"
	// and "nobody checked" are the two answers this whole payload exists to
	// keep apart; a report that stated the second only per row
	// would make an operator scanning 300 lines discover it by accident.
	//
	// It carries no omitempty for the reason the three counts above do not: a
	// run in which everything WAS read is the good case, and the good case is
	// the one that would lose its zero.
	Unread int `json:"unread"`
	// Notes is what the run has to say about ITSELF: the sentences no row
	// carries and no count states — the classification share, the baseline
	// review's coverage, a review that reached no model at all, the advice to
	// prune what is redundant.
	//
	// They are DATA rather than something the command prints beside the report
	// because an export re-derives its blocks from this payload: attached to the
	// terminal's sections only, every one of them reached the screen and none
	// reached an export, so the file was silently shorter than the screen.
	// Carried here, both paths read Sections and Sections reads this.
	//
	// They are SENTENCES, as ComparePkg.Reason is: a run-level summary names no
	// package to travel on, and no set of counts reconstitutes it. What this
	// type forbids is a sentence about the DEVICE, and none here mentions one.
	//
	// Null is a producer that never considered the question, [] a run that had
	// nothing to add; the adapter fills it either way, and no omitempty drops
	// it — a run with nothing to say still has to say so.
	Notes []string `json:"notes"`
	// ReadingFailures splits the failed readings among Unread by why each
	// review failed: one {cause, count} per cause, over the same population
	// Unread counts. The cause is a word the adapter spells, as it
	// spells Reading. No omitempty: a run with no failed review publishes an
	// empty list, which is the good case.
	ReadingFailures []CauseCount `json:"reading_failures"`
}

// ComparePkg is what the run established about one package: which package, the
// two versions, how they relate, whether anyone read the difference, what the
// difference was, and why — in the producer's own words, at full length.
//
// # Six strings, and every one of them is already a word rather than a value
//
// Status, Reading and Diff are enums at the producer and strings here, and the
// conversion is the adapter's. That is not a loss of type safety at
// this seam: the exported document has no Go types in it, so the choice is only
// between converting once at the adapter and converting in each renderer plus
// the exporter. The closed vocabularies those three draw from are stated on the
// fields below so that the string a reader meets can still be checked against a
// list.
type ComparePkg struct {
	// Package is the atom, "<category>/<package>", as an operator types it.
	//
	// The two halves are joined by the adapter rather than carried apart, for
	// the reason ManifestTarget.Package gives: every reader of this field
	// prints the atom, and carrying the halves separately would leave each
	// renderer to re-join them with a separator of its own — two renderers
	// spelling one package name differently is the disagreement a model exists
	// to make impossible.
	Package string `json:"package"`
	// Local is the version the overlay carries and Remote the version the
	// compared repository carries, each exactly as the producer read it.
	//
	// Either may be empty, and empty is a fact rather than a gap: a package the
	// remote carries no version of has no remote version to state, and a
	// placeholder invented here would be this type answering a question the run
	// did not. Which of the two is missing is also what OnlyLocal counts one
	// level up.
	//
	// The two are the group key: KeepGroup collects the kept packages whose
	// (Local, Remote) pair is identical, so these strings are compared for
	// equality rather than parsed. Nothing here or downstream normalises them —
	// a version the producer read as "1.2.3-r1" is not the same string as
	// "1.2.3", and pretending otherwise would merge two groups that differ.
	Local  string `json:"local_version"`
	Remote string `json:"remote_version"`
	// Status is how the two versions relate, in the producer's own vocabulary:
	// up to date, outdated, newer, not in remote, or an error reading either.
	//
	// It answers "how do the versions compare", which is a different question
	// from "what should be done about it" — that one is answered by which of
	// CompareRun's lists this entry is in, and by Verdicts one level up. The
	// producer counts the two axes separately and every compared package is
	// counted exactly once on each; deriving one from the other would silently
	// change what the other means.
	Status string `json:"status"`
	// Reading is whether anybody read the difference between the two versions,
	// as one of four words: not requested, not comparable, failed, read.
	//
	// Four values, because a review note's zero value covers reviews disabled,
	// no reviewer available, a reviewer that errored, and a finding that was
	// never a candidate — so "is the note empty?" cannot answer "did anybody
	// read this?", the question an operator asks before acting on a
	// recommendation to delete.
	//
	// It is separate from Diff: one says whether the files differ, the other
	// whether anyone explained the difference. The empty string is not one of
	// the four; the adapter maps every case to a word, and an unmapped
	// combination is a defect, not a blank cell.
	Reading string `json:"reading"`
	// Diff is what the content check found, drawn from a CLOSED vocabulary:
	// "+N/-M" for a measured difference, "identical", "not compared", or
	// "unreadable".
	//
	// The vocabulary is closed so that "no difference was found" can never be
	// read as "no comparison was made". Those two are the same blank cell in
	// the output this payload replaces, and they are opposite answers: one says
	// the overlay's copy is redundant, the other says nobody has established
	// anything about it.
	//
	// It is a formatted string rather than two ints on purpose. The magnitude
	// is meaningful only when a comparison ran and found a difference; a pair
	// of zeroes would be indistinguishable from a check that never ran, which
	// is the conflation the closed vocabulary exists to prevent. The numbers
	// the producer measured are what the adapter formats into the first form.
	Diff string `json:"diff"`
	// Reason is this entry's finding, in one line: why the run recommends what
	// it recommends, or why it could not decide.
	//
	// It is empty when the entry has nothing of its own to add — a package
	// whose recommendation follows from its Status and needs no sentence — and
	// that emptiness is load-bearing rather than incidental. A kept package
	// with an empty Reason carries no finding, and a package carrying no
	// finding is one that grouping may absorb; a package with a finding is
	// never absorbed into a group, because grouping compresses repetition and a
	// finding is not repetition. GroupKeep reads this field for exactly that
	// test.
	//
	// It is ONE LINE by construction, folded by the adapter, because a raw
	// newline in a row's detail corrupts both the plain writer and the Markdown
	// pipe table. Anything that will not survive being shortened belongs in a
	// section's lead or notes, which wrap, rather than in a row detail, which
	// is cut. What travels here is the whole folded string at no width budget:
	// the export carries every explanation in full, whatever the terminal it
	// was rendered at was able to show.
	Reason string `json:"reason"`
	// FurtherFindings is everything ELSE the run established about this
	// package: the second finding and every one after it, in the producer's own
	// words and at full length.
	//
	// Reason holds the FIRST and this holds the rest, because a row detail is
	// one line that a narrow device cuts; what does not fit a cell is said
	// beside it, in prose that wraps, under the block holding this package's
	// row. The package is NOT named in the text — the block names it — so no
	// second spelling of Package can disagree with the row beside it.
	//
	// Every entry is ONE LINE, folded by the adapter, and nothing is shortened.
	// Only an empty list says "the run established nothing further", and no
	// omitempty drops it: a package with a single finding is the common case.
	FurtherFindings []string `json:"further_findings"`
	// Cause is why this row failed, in the adapter's closed vocabulary: the
	// upstream lookup's cause on a row whose Status is "error", the review's
	// cause on a row whose Reading is "failed", "" on a row with no failure.
	// No omitempty, for the reason Error gives.
	Cause string `json:"cause"`
	// Error is the full text of that failure, folded to one line and scrubbed
	// of the run's credentials by the producer, and "" on a row with no
	// failure. It follows ManifestResult.Error (model.go): kept whole here,
	// never shortened; only a table cell may cut it.
	Error string `json:"error"`
}

// CauseCount is one entry of CompareRun.ReadingFailures: how many failed
// readings share one cause.
type CauseCount struct {
	Cause string `json:"cause"`
	Count int    `json:"count"`
}

// KeepGroup is a set of packages that share one version pair.
//
// It exists because a report that lists two hundred kept packages one per line
// tells an operator less than one that says most of them are the same fact
// repeated. The compression is a fact the run established, carried as data, so
// the exported document states it too.
//
// # A group never hides anything that needs action
//
// Only packages carrying no finding are collected here, and a package with a
// finding stays its own row whether or not its version pair matches a group's.
// Every member also remains in CompareRun.Keep, so the group is a
// view over the list rather than a replacement for it, and dropping the groups
// costs a reader the summary and no data.
type KeepGroup struct {
	// Label is the group's heading in the reader's own words. It is prose, not
	// a key: two groups may share one, and nothing matches on it.
	Label string `json:"label"`
	// Local and Remote are the version pair every member of this group shares —
	// the group's identity, and the only thing that decides membership.
	//
	// The JSON keys say "version" where the Go names do not, because the two
	// names have different readers: `.local_version` is how a consumer asks the
	// question, while the Go name sits beside a Label and a Members list where
	// no other reading is available. snapshot_run.go's Subvolumes carries the
	// same deliberate split in the other direction.
	Local  string `json:"local_version"`
	Remote string `json:"remote_version"`
	// Members are the atoms in this group, in the order the run established
	// them. The first one breaks ties when two groups have the same member
	// count, so this order is part of what makes the report byte-identical
	// across two runs over one overlay.
	//
	// A group always has at least two: a version pair with one member is an
	// ordinary row and never a group of one.
	Members []string `json:"members"`
	// ByCategory is how the group's members break down across categories —
	// "42 in dev-lang, 8 in app-editors" as values rather than as a sentence.
	//
	// It is a field rather than something a renderer counts off Members for the
	// reason the whole type is data: the export is read by a program, and a
	// breakdown that exists only in a renderer is a breakdown the exported
	// document does not carry. It is also what makes a group line useful
	// without expanding it — a count alone says how much was compressed, and
	// the breakdown says what was compressed.
	ByCategory []CategoryCount `json:"by_category"`
}

// CategoryCount is one category and how many of a group's members are in it.
//
// Category rather than a bare name because that is the half of the atom being
// counted, and Count rather than Total because it counts within one group: the
// run's own totals live on CompareRun, and a name that read like a run-level
// total here would invite the two to be compared.
type CategoryCount struct {
	// Category is the atom's first half — "dev-lang", "app-editors" — as the
	// producer read it, never abbreviated.
	Category string `json:"category"`
	// Count is how many of the group's members are in that category.
	//
	// It carries no omitempty, and the zero is reachable in exactly one way: a
	// producer that established the entry without establishing its count. That
	// is worth being able to see, which is what dropping the key would prevent.
	Count int `json:"count"`
}

// VerdictTally is the four-column count of what the run recommends: keep,
// remove, rebase, and no opinion. It mirrors the four counters the producer
// maintains, one per verdict, and adds nothing to them.
//
// It is a payload's own count because each command counts in its own
// vocabulary — four validation outcomes, two manifest columns, two snapshot
// columns, four verdicts — and one universal tally on the envelope would lose
// or invent columns.
//
// It is counted by the run, never summed from CompareRun's lists: a filter
// flag narrows what an operator looks at and narrows no counter, and a second
// answer to one question is the first place the two could disagree.
//
// No field carries omitempty. A zero dropped from the document reads as "the
// producer never said", which would let an unexamined overlay look like a
// clean one.
type VerdictTally struct {
	// Keep is how many packages the run recommends keeping.
	Keep int `json:"keep"`
	// Redundant is how many the run recommends removing, because the overlay's
	// copy adds nothing the compared repository does not already carry.
	Redundant int `json:"redundant"`
	// NeedsRebase is how many carry changes of ours on top of a version the
	// compared repository has since moved past: the overlay's work is worth
	// keeping and has to be re-applied.
	NeedsRebase int `json:"needs_rebase"`
	// Unknown is how many the run reached no recommendation about. It is never
	// folded into any of the three above: a package nothing is known about must
	// not be counted as one recommended for removal, which is the rule the
	// producer's own verdict table is written to guarantee.
	Unknown int `json:"unknown"`
}

// GroupKeep is the rule that fills CompareRun.KeepGroups: the kept packages
// that share one version pair and carry no finding, collected into groups and
// ordered so that two runs over one overlay produce the same list.
//
// It is exported because it runs when the payload is BUILT, not when it is
// drawn: the adapter in cmd/bentoo calls it, and Sections only reads the field,
// so the exported document carries the groups too. It returns the list rather
// than filling the field, so the assignment is visible where the payload is
// built — run.KeepGroups = report.GroupKeep(run.Keep) — and a correctly built
// payload does not depend on somebody remembering a method call.
//
// A package carrying a finding (a non-empty Reason) is never absorbed, and a
// version pair with a single member stays an ordinary row; both remain in
// CompareRun.Keep and compareKeepTable lists them, as it lists every member
// under --all. Groups come back by member count descending, ties broken by the
// first member's atom; membership is accumulated in a map, but the order comes
// from a slice of first appearances, because map iteration is randomised per
// run. The result is non-nil even when empty: the rule RAN and collected
// nothing.
func GroupKeep(pkgs []ComparePkg) []KeepGroup {
	order := make([]compareVersionPair, 0, len(pkgs))
	members := make(map[compareVersionPair][]string, len(pkgs))

	for _, p := range pkgs {
		// The finding test, and the only one: a package with something of its
		// own to say stays its own row.
		if p.Reason != "" {
			continue
		}

		pair := compareVersionPair{local: p.Local, remote: p.Remote}
		if _, seen := members[pair]; !seen {
			order = append(order, pair)
		}
		members[pair] = append(members[pair], p.Package)
	}

	groups := make([]KeepGroup, 0, len(order))
	for _, pair := range order {
		atoms := members[pair]
		if len(atoms) < 2 {
			continue
		}

		groups = append(groups, KeepGroup{
			Label:      compareGroupLabel(atoms),
			Local:      pair.local,
			Remote:     pair.remote,
			Members:    atoms,
			ByCategory: compareCategoryCounts(atoms),
		})
	}

	slices.SortFunc(groups, compareGroupOrder)

	return groups
}

// compareVersionPair is the (local, remote) pair that decides membership, in
// the shape a map can key on.
//
// It is a struct rather than the two strings joined, because a join needs a
// separator no version may contain and no such character is promised: two pairs
// that differ would collide on it and the report would merge two groups into
// one. Nothing here normalises either half — ComparePkg's own doc says a
// version travels as the producer read it, and "1.2.3-r1" is not "1.2.3".
type compareVersionPair struct {
	local  string
	remote string
}

// compareGroupOrder is the total order over groups: member count descending,
// ties broken by the first member's atom.
//
// Total is the operative word. Two groups can tie on count, and without the
// second key their order would be whatever the sort happened to do with them —
// which is exactly enough non-determinism to make one run's report differ from
// the next over identical data, and to make a golden file impossible. The tie
// key cannot itself tie: a package belongs to at most one group, so no two
// groups share a first member.
func compareGroupOrder(a, b KeepGroup) int {
	if n := len(b.Members) - len(a.Members); n != 0 {
		return n
	}
	return strings.Compare(compareFirstMember(a), compareFirstMember(b))
}

// compareFirstMember is the atom the tie-break reads, with an empty group
// answering the empty string.
//
// GroupKeep emits no group with fewer than two members, so the guard is for the
// comparator's own totality rather than for a case this file produces: a
// comparison that panicked on a hand-built group would fail somewhere other
// than where the mistake is.
func compareFirstMember(g KeepGroup) string {
	if len(g.Members) == 0 {
		return ""
	}
	return g.Members[0]
}

// compareCategoryCounts is how a group's members break down across categories,
// largest first.
//
// # Count descending, ties broken by the category name
//
// Largest first is what makes the breakdown useful when it is cut: a shortened
// line still says what the group mostly is. The name breaks ties because it is
// the only other fact in the entry and it cannot itself tie — a category
// appears once in the list — so the order is total, which byte-identical
// output needs from this list as much as from the groups above it.
//
// The counts are accumulated in a map and emitted from a slice of first
// appearances, never by ranging over the map, for the reason GroupKeep does the
// same: map order is randomised per run.
func compareCategoryCounts(atoms []string) []CategoryCount {
	counts := make(map[string]int, len(atoms))
	order := make([]string, 0, len(atoms))

	for _, atom := range atoms {
		category, _ := compareAtomHalves(atom)
		if _, seen := counts[category]; !seen {
			order = append(order, category)
		}
		counts[category]++
	}

	out := make([]CategoryCount, 0, len(order))
	for _, category := range order {
		out = append(out, CategoryCount{Category: category, Count: counts[category]})
	}

	slices.SortFunc(out, func(a, b CategoryCount) int {
		if n := b.Count - a.Count; n != 0 {
			return n
		}
		return strings.Compare(a.Category, b.Category)
	})

	return out
}

// compareAtomHalves splits "<category>/<package>" into the two halves an atom
// is written from.
//
// An atom with no slash is malformed, and what comes back for it is an empty
// category and the whole string as the package name. That is the honest answer:
// the run established no category for it, which is what an empty cell says
// everywhere else in this report, and inventing one by reusing the atom would
// put a fabricated category into an exported breakdown.
func compareAtomHalves(atom string) (category, name string) {
	if i := strings.IndexByte(atom, '/'); i >= 0 {
		return atom[:i], atom[i+1:]
	}
	return "", atom
}

// compareLabelFloor is the shortest shared stem that may stand as a label, in
// bytes.
//
// Two, because a two-character name is a real one — qt, go, sh — while a single
// character is a fragment of somebody else's word. A stem under the floor is
// not a short label, it is a wrong one, so the fallback names a member instead.
const compareLabelFloor = 2

// compareGroupLabel is the group's heading, derived from what its members
// actually have in common: the longest prefix their package names share, cut
// back to where a token ends.
//
// It does not produce domain prose such as "gstreamer stack" or "rust
// toolchain": a name-to-label map would put Gentoo knowledge inside the package
// whose guards keep domain out, and would go stale. Over those groups it gives
// "gst", "rust", "vulkan" and "mesa" — affordable because a label is prose, not
// a key, so two groups may share one.
//
// Only the half after the slash is read: a group may span categories, and the
// spread is stated by ByCategory. A stem shorter than compareLabelFloor names
// nothing ("g (82 packages)"), so under the floor — including members sharing
// no token at all — the label is the FIRST MEMBER'S ATOM: deterministic,
// because Members is in the run's own order, and naming something really in
// the group, with the member count beside it.
func compareGroupLabel(atoms []string) string {
	names := make([]string, 0, len(atoms))
	for _, atom := range atoms {
		_, name := compareAtomHalves(atom)
		names = append(names, name)
	}

	if stem := compareSharedStem(names); len(stem) >= compareLabelFloor {
		return foldToOneLine(stem)
	}
	if len(atoms) == 0 {
		return ""
	}
	return foldToOneLine(atoms[0])
}

// compareSeparatorAt reports whether name has an explicit token separator at i.
//
// It is compareTokenBoundary minus its two implicit openings — the end of the
// string, and the first digit of a run — because a stem accepted on those would
// name a group after one member's tail rather than after what they share.
func compareSeparatorAt(name string, i int) bool {
	if i >= len(name) {
		return false
	}
	return name[i] == '-' || name[i] == '_' || name[i] == '.'
}

// compareSharedStem is the longest prefix every name shares, cut back so that
// it ends where a token ends.
//
// The cut is what keeps a label from reading as debris. gst-plugins-good and
// gst-python share "gst-p", which is a prefix of a word rather than a word; cut
// back to the last boundary it is "gst", which is the thing they are both named
// after. The prefix is kept whole only when it already ends a token in every
// name — rust and rust-bin share "rust", and there is nothing there to cut.
//
// Both return paths land on a rune boundary, and that is not luck: the prefix
// is taken byte by byte, but the whole-prefix path is taken only when every
// name ends there or continues with a separator or a digit — all ASCII — and
// the cut path returns the prefix up to an ASCII separator or digit. A Gentoo
// package name is ASCII by the package manager's own grammar anyway.
func compareSharedStem(names []string) string {
	if len(names) == 0 {
		return ""
	}

	prefix := names[0]
	for _, name := range names[1:] {
		limit := min(len(prefix), len(name))
		cut := limit
		for i := 0; i < limit; i++ {
			if prefix[i] != name[i] {
				cut = i
				break
			}
		}
		prefix = prefix[:cut]
	}

	whole := true
	for _, name := range names {
		if !compareTokenBoundary(name, len(prefix)) {
			whole = false
			break
		}
	}
	if whole {
		return prefix
	}

	// A stem is not debris if it is a whole token SOMEWHERE in the group, even
	// where one member runs on past it: gst-python, gst-plugins-good and
	// gstreamer share "gst", which ends a token in the first two, so the group
	// is named "gst" rather than falling back to one member.
	//
	// Only an explicit SEPARATOR counts here, not every boundary: python3 and
	// python311 share "python3", which ends the first name but continues the
	// second's NUMBER, so the group is named after python, not one member's
	// version. The rule above still holds — "gst-p" ends a token in no name and
	// still falls to the cut below, which returns "gst".
	for _, name := range names {
		if compareSeparatorAt(name, len(prefix)) {
			return prefix
		}
	}

	for i := len(prefix) - 1; i > 0; i-- {
		if compareTokenBoundary(prefix, i) {
			return prefix[:i]
		}
	}

	return ""
}

// compareTokenBoundary answers whether offset i in s starts a new token: the
// end of the string, one of the three separators a package name is built with,
// or the first digit of a run of digits.
//
// # The digit rule reads a RUN, not a character
//
// gtk3 and gtk4 share "gtk" and the label wanted there is "gtk", so a digit
// opens a token. python3 and python311 share "python3", and calling the second
// name's next digit a boundary would hand back a label that is a real package's
// name applied to a group of two — so a digit CONTINUING a run opens nothing,
// and the stem is cut back to "python".
func compareTokenBoundary(s string, i int) bool {
	if i >= len(s) {
		return true
	}

	digit := func(b byte) bool { return b >= '0' && b <= '9' }

	switch {
	case s[i] == '-' || s[i] == '_' || s[i] == '.':
		return true
	case digit(s[i]):
		return i == 0 || !digit(s[i-1])
	default:
		return false
	}
}

// The three words of ComparePkg.Reading that this file branches on.
//
// Reading's vocabulary has four words — not requested, not comparable, failed,
// read — and the field's own doc is where they are defined. All four are named
// here because the redundant block counts all four: three to say why a package
// carries no reading, and the fourth to decide whether a removal
// recommendation is supported at all.
//
// They are constants rather than literals for one reason: the adapter that
// produces these strings lives in cmd/bentoo and the sentences that count
// them live here, so the vocabulary is spelled in two packages and
// a typo in either would show up as a count of zero rather than as a failure.
// Naming them at least makes the half this package owns greppable from the
// field that defines them.
//
// Counting a Reading value is not the same thing as re-deriving it. Every cell
// this file prints comes from the payload verbatim — Diff above all, whose
// closed vocabulary is the adapter's to produce and this file's to render
// as-is — and the counts below feed a NOTE, never a cell.
const (
	readingNotRequested  = "not requested"
	readingNotComparable = "not comparable"
	readingFailed        = "failed"
	readingRead          = "read"
)

// compareReadingFailedMark is the marker a row carries when the review of its
// difference was attempted and failed.
//
// It is a constant because two places print it and they must not drift: the
// row detail that carries it, and the note in the first section that says what
// it means. A marker whose explanation names a different string is worse than
// no marker at all — the reader searches the report for a legend that is not
// there.
const compareReadingFailedMark = "[reading failed]"

// compareReviewCauseOrder is the review-cause vocabulary in its stated order.
// The scope note orders ReadingFailures by count, highest first,
// and breaks ties by this order, so the note reads the same whatever order the
// producer listed the entries in. A cause missing from it sorts last.
var compareReviewCauseOrder = []string{
	"timed out", "could not start", "exited non-zero", "empty or unusable reply",
	"cancelled", "ebuild unreadable", "other",
}

// compareFailureCounts renders ReadingFailures as "<n> <cause>" joined by ", ",
// highest count first and ties in compareReviewCauseOrder.
func compareFailureCounts(failures []CauseCount) string {
	rank := func(cause string) int {
		for i, c := range compareReviewCauseOrder {
			if c == cause {
				return i
			}
		}
		return len(compareReviewCauseOrder)
	}
	sorted := make([]CauseCount, 0, len(failures))
	for _, f := range failures {
		if f.Count > 0 {
			sorted = append(sorted, f)
		}
	}
	slices.SortStableFunc(sorted, func(a, b CauseCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return rank(a.Cause) - rank(b.Cause)
	})
	parts := make([]string, 0, len(sorted))
	for _, f := range sorted {
		parts = append(parts, fmt.Sprintf("%d %s", f.Count, f.Cause))
	}
	return strings.Join(parts, ", ")
}

// Sections is the compare run as structure: what it says, in order, with every
// value at full length and not one decision about how it will look. It is also
// what makes CompareRun satisfy the Payload interface.
//
// Six blocks, because the run answers six questions: how much was compared at
// all, what may be removed, what needs work re-applied, what earns its place,
// what nothing is known about, and how it tallies. Redundant leads the verdicts
// because it is the only destructive one; the summary comes last because a
// tally read before its evidence is a number nobody can check.
//
// Every block is returned on every path: "nothing is redundant" is an answer,
// and a missing heading is indistinguishable from a report cut short. Guards
// read the COUNTS the run established, never the length of a list. Counts go
// in the Lead; omissions, causes and anything that must be read whole go in
// the Notes, which wrap — a Row.Detail is cut at the device width, so it holds
// only a line that still means something shortened.
//
// Only the keep section reads ShowAll, and it changes what is LISTED, never a
// count. SectionOptions.SkipPlan is not read: none of the six blocks is a plan.
func (r CompareRun) Sections(opts SectionOptions) []Section {
	return []Section{
		compareScopeSection(r),
		compareRedundantSection(r),
		compareNeedsRebaseSection(r),
		compareKeepSection(r, opts.ShowAll),
		compareUnknownSection(r),
		compareSummarySection(r),
	}
}

// compareAgrees picks the form of a verb that agrees with n.
//
// The report's "package(s)" idiom sidesteps the NOUN and leaves the verb where
// it was, so a run with one of something reads "1 were never compared at all".
// A count is written by the same code whatever it counts and English is not,
// which is the whole of why this exists. It is deliberately not a pluraliser:
// it takes both forms as arguments, because the ones this report needs are
// is/are, has/have, was/were, exists/exist and carries/carry, and a rule that
// derived those from a stem would be wrong on four of the five.
func compareAgrees(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// compareScopeSection is the first block: how much was scanned, how much of it
// could be compared at all, and how much of the comparison nobody read.
//
// It has no table: the three counts are the whole content, and a one-row table
// would repeat the sentence above it.
//
// The unread sentence is a NOTE, prose that wraps, because an operator must
// read all of it before trusting any list below. It appears only when Unread is
// above zero, and it names all three of Unread's causes: the payload carries a
// single total, so naming one would answer a question the run did not. Which
// cause applies to a listed package is answered by the redundant block from
// each row's Reading.
func compareScopeSection(r CompareRun) Section {
	s := Section{Title: "Overlay Comparison"}

	s.Lead = []string{fmt.Sprintf(
		"%d package(s) scanned in the Bentoo overlay. %d also %s in %s and %s compared below; %d %s only here and %s nothing to compare against.",
		r.Scanned,
		r.InBoth, compareAgrees(r.InBoth, "exists", "exist"), compareRepository(r), compareAgrees(r.InBoth, "is", "are"),
		r.OnlyLocal, compareAgrees(r.OnlyLocal, "exists", "exist"), compareAgrees(r.OnlyLocal, "has", "have"))}

	if r.Unread > 0 {
		s.Notes = []string{fmt.Sprintf(
			"%d comparison(s) %s never read: a review nobody requested, a review that was attempted and failed, or a version pair the content check refused. A row whose review failed is marked %s — the difference is real, the explanation is missing, and no row below was quietly downgraded to hide it.",
			r.Unread, compareAgrees(r.Unread, "was", "were"), compareReadingFailedMark)}
		if counts := compareFailureCounts(r.ReadingFailures); counts != "" {
			s.Notes[0] += " The failed reviews, by cause: " + counts + "."
		}
	}

	return s
}

// compareRedundantSection is the block an operator acts on destructively: the
// packages whose overlay copy adds nothing the compared repository does not
// already carry.
//
// It is the one section that carries the DIFF column, together with the
// needs-rebase block below, because a recommendation to delete is only as good
// as the evidence that somebody looked and the diff cell is that
// evidence. The cell is printed exactly as the payload carries it: the closed
// vocabulary it is drawn from is what keeps "no difference was found" from
// being read as "no comparison was made", and re-wording it here would reopen
// the conflation.
func compareRedundantSection(r CompareRun) Section {
	s := Section{Title: "Redundant — " + compareRepository(r) + " ships the version, and nothing cleared the content check"}

	// The guard is on the count the run established, not on the length of the
	// list, for the reason manifest_run.go gives at length: the two are
	// separate fields and nothing in the type makes them agree, so a sentence
	// claiming "nothing" from the wrong one is how a report starts lying about
	// the numbers it exists to state.
	if r.Verdicts.Redundant == 0 && len(r.Redundant) == 0 {
		s.Lead = []string{"No package is counted as redundant, so nothing here is recommended for removal."}
		return s
	}

	if len(r.Redundant) == 0 {
		s.Lead = []string{fmt.Sprintf("%d package(s) are counted as redundant.", r.Verdicts.Redundant)}
		s.Notes = []string{"No per-package row reached this report, so the count above is stated without the list it was taken over."}
		return s
	}

	// The advice is DERIVED, in compareRedundantLead, from what was read. An
	// unconditional recommendation to remove would contradict this section's own
	// title two lines above it, and nobody should delete work of ours because
	// a report showed an unread package as if it had been checked.
	s.Lead = []string{compareRedundantLead(r.Redundant)}
	s.Rows = comparePkgTable(r, r.Redundant, true)

	// The caveat, and nothing else. The reading breakdown is in the Lead, where
	// an operator meets it BEFORE the list it qualifies rather than after; saying
	// it in both places would be two copies of one fact, free to drift.
	//
	// It is unconditional once there are rows. It is the sentence that keeps this
	// report from being acted on wrongly — the compared repository revises
	// ebuilds in place, so a small diff is more often our copy having fallen
	// behind than work of ours — and a caveat that appeared only sometimes is a
	// caveat a reader learns to ignore.
	s.Notes = []string{fmt.Sprintf(
		"A difference is not proof of authorship: %s also revises ebuilds in place, without a revbump, so a small diff is often our copy having fallen behind rather than work of ours. Diff before removing or declaring.",
		compareRepository(r))}
	s.Notes = append(s.Notes, comparePkgNotes(r.Redundant)...)

	return s
}

// compareRedundantLead is the count, why the listed packages carry no reading,
// and the removal advice that FOLLOWS FROM those two — in that order, above the
// table they describe.
//
// The advice is derived from what was read, never unconditional: nothing read
// gives no removal advice (the measured overlay's case), some read limits the
// recommendation to those by count, and everything read covers the whole list.
// A Reading this file does not know, the empty string included, counts as NOT
// read — the fail-safe direction, which can cost a recommendation but never
// earn one over evidence nobody has.
//
// The producer's NotVerified collapses four causes into readingNotComparable:
// no local copy, no upstream copy at that version, an unreadable file, and two
// versions that differ. Only the version pair is provable from the row —
// resolvePackagePaths refuses a differing pair before any other exit — so it
// alone is named, and every other refusal gets a clause naming no cause:
// stating the wrong cause is worse than stating none. Every clause is
// conditional on its count, since a false "never compared" invites an operator
// to distrust a list with nothing wrong with it.
func compareRedundantLead(pkgs []ComparePkg) string {
	var read, versionsDiffer, refusedUnstated, failed, notRequested int
	for _, p := range pkgs {
		switch p.Reading {
		case readingRead:
			read++
		case readingNotComparable:
			// The producer collapsed the cause, but ONE of the four is provable
			// from the row itself. `func resolvePackagePaths` in
			// internal/overlay/compare.go refuses a differing version pair at its
			// SECOND statement, before any exit that could stand for another
			// cause, so a row whose two versions differ was refused for exactly
			// that reason and no other. Everything else stays unattributed.
			if p.Local != "" && p.Remote != "" && p.Local != p.Remote {
				versionsDiffer++
			} else {
				refusedUnstated++
			}
		case readingFailed:
			failed++
		case readingNotRequested:
			notRequested++
		}
	}

	var clauses []string
	if versionsDiffer > 0 {
		clauses = append(clauses, fmt.Sprintf(
			"%d %s never compared at all, because the check refuses to diff two different versions",
			versionsDiffer, compareAgrees(versionsDiffer, "was", "were")))
	}
	if refusedUnstated > 0 {
		// The remaining causes — no overlay path, a provider that cannot resolve
		// a package directory, an ebuild that would not open — are indistinguishable
		// here, so this names none of them. Borrowing the clause above would state
		// a cause the run never established, which is the objection `func
		// compareDiffCell` in cmd/bentoo/overlay_compare_report.go already makes
		// about printing "unreadable" for a state it cannot observe.
		clauses = append(clauses, fmt.Sprintf(
			"%d %s never compared at all, and this run did not record why",
			refusedUnstated, compareAgrees(refusedUnstated, "was", "were")))
	}
	if failed > 0 {
		// "the other" is only true when the two clauses account for every
		// package listed, which is the measured overlay's shape and the wording
		// the target rendering was written from. Any other arrangement gets the
		// count on its own, because a reader cannot subtract what they were not
		// given.
		if versionsDiffer > 0 && refusedUnstated == 0 && versionsDiffer+failed == len(pkgs) {
			clauses = append(clauses, fmt.Sprintf("the review died on the other %d", failed))
		} else {
			clauses = append(clauses, fmt.Sprintf("%d had a review that was attempted and failed", failed))
		}
	}
	if notRequested > 0 {
		clauses = append(clauses, fmt.Sprintf("%d %s never submitted for review",
			notRequested, compareAgrees(notRequested, "was", "were")))
	}

	sentences := []string{compareReadingCount(len(pkgs), read)}
	if len(clauses) > 0 {
		sentences = append(sentences, strings.Join(clauses, "; ")+".")
	}
	return strings.Join(append(sentences, compareRemovalAdvice(len(pkgs), read, len(clauses))), " ")
}

// compareReadingCount is the first sentence: how many packages are listed, and
// how many of them anybody read.
func compareReadingCount(listed, read int) string {
	switch read {
	case 0:
		return fmt.Sprintf("%d package(s), and not one of them carries a reading.", listed)
	case listed:
		return fmt.Sprintf("%d package(s), and every one of them carries a reading.", listed)
	default:
		return fmt.Sprintf("%d package(s), of which %d %s a reading.", listed, read, compareAgrees(read, "carries", "carry"))
	}
}

// compareRemovalAdvice is the last sentence, and the only place in this report
// that recommends deleting anything.
//
// It reads the same two numbers the sentences before it were built from, so the
// advice cannot say more than the evidence above it just said.
func compareRemovalAdvice(listed, read, causes int) string {
	switch {
	case read == 0 && causes == 2:
		// The measured overlay's wording, kept to the byte: two causes, so
		// "either" is what names them both.
		return "No removal advice follows from either."
	case read == 0:
		return "No removal advice follows from a package nobody read."
	case read == listed:
		return "The recommendation to remove covers the whole list."
	default:
		return fmt.Sprintf("The recommendation to remove covers the %d somebody read, and none of the rest.", read)
	}
}

// compareNeedsRebaseSection is the block that asks for work rather than for a
// decision: the compared repository moved ahead of a version the overlay
// carries changes on top of, so ours owes a bump AND a re-application.
//
// # It carries the DIFF column, and the keep block does not
//
// The distinction is what the column MEASURES. Here the difference IS the
// work: it is the delta somebody has to re-apply, so its size is the size of
// the job. In the keep block the recommendation follows from the versions
// alone, and a diff cell there would be a number with no decision attached to
// it — a column that means nothing on most rows teaches a reader to stop
// reading it, which is exactly when the one row that matters goes past.
func compareNeedsRebaseSection(r CompareRun) Section {
	s := Section{Title: "Needs Rebase — " + compareRepository(r) + " moved ahead of changes of ours, which must be re-applied"}

	if r.Verdicts.NeedsRebase == 0 && len(r.NeedsRebase) == 0 {
		s.Lead = []string{"No package needs a rebase: nothing here carries changes of ours on top of a version " +
			compareRepository(r) + " has since moved past."}
		return s
	}

	if len(r.NeedsRebase) == 0 {
		s.Lead = []string{fmt.Sprintf("%d package(s) are counted as needing a rebase.", r.Verdicts.NeedsRebase)}
		s.Notes = []string{"No per-package row reached this report, so the count above is stated without the list it was taken over."}
		return s
	}

	s.Lead = []string{fmt.Sprintf(
		"%d package(s) listed: each carries changes of ours on top of a version %s has since moved past, so both a bump and a re-application are owed.",
		len(r.NeedsRebase), compareRepository(r))}
	s.Rows = comparePkgTable(r, r.NeedsRebase, true)
	s.Notes = comparePkgNotes(r.NeedsRebase)

	return s
}

// compareKeepSection is the longest block a healthy overlay produces, and the
// only one whose listing ShowAll changes.
//
// # The Lead counts the LIST, and the summary counts the RUN
//
// They are different numbers and both are correct. Verdicts.Keep counts every
// scanned package the run recommends keeping, including the ones the compared
// repository carries no version of; this list holds only the packages that
// exist in both trees, because a package with nothing to compare against has
// no row to print. The gap between the two is stated once, in words, in the
// summary block — which is what CompareRun.Verdicts' own doc says it is for.
func compareKeepSection(r CompareRun, listEvery bool) Section {
	s := Section{Title: "Keep — the overlay copy is ahead"}

	if r.Verdicts.Keep == 0 && len(r.Keep) == 0 {
		s.Lead = []string{"No package is counted as worth keeping on its own merits."}
		return s
	}

	if len(r.Keep) == 0 {
		s.Lead = []string{fmt.Sprintf("%d package(s) are counted as worth keeping.", r.Verdicts.Keep)}
		s.Notes = []string{"No per-package row reached this report, so the count above is stated without the list it was taken over."}
		return s
	}

	s.Lead = []string{fmt.Sprintf("%d package(s) listed, and the run recommends keeping every one of them.", len(r.Keep))}
	s.Rows = compareKeepTable(r, listEvery)
	s.Notes = append(compareKeepNotes(r, listEvery), comparePkgNotes(r.Keep)...)

	return s
}

// compareKeepTable is the keep block's rows: one per group when the groups are
// collapsed, one per package when they are not.
//
// # ShowAll is the only thing that changes what is listed
//
// Collapsed, the table prints one row per group followed by every package no
// group claimed, in the order the run established. Expanded, it prints the
// run's own list, unchanged and in full — every group member is also in Keep,
// which CompareRun.Keep's doc guarantees, so nothing is lost by dropping the
// group rows and the operator gets exactly the flat list they asked for.
//
// # An empty KeepGroups is not a special case
//
// GroupKeep fills the slice, and it returns an empty one for a run whose kept
// packages genuinely share no version pair — every pair with a single member,
// or every repeat carrying a finding of its own. The first arm below is the
// right answer for that run and for a payload nobody ran the rule over: list
// every kept package, because there is nothing to collapse.
func compareKeepTable(r CompareRun, listEvery bool) Table {
	t := Table{Headers: comparePkgHeaders(r, false)}

	if listEvery || len(r.KeepGroups) == 0 {
		for _, p := range r.Keep {
			t.Rows = append(t.Rows, comparePkgRow(p, false))
		}
		return t
	}

	status := make(map[string]string, len(r.Keep))
	for _, p := range r.Keep {
		status[p.Package] = p.Status
	}

	grouped := make(map[string]bool)
	for _, g := range r.KeepGroups {
		for _, member := range g.Members {
			grouped[member] = true
		}
		t.Rows = append(t.Rows, compareKeepGroupRow(g, status))
	}

	for _, p := range r.Keep {
		if grouped[p.Package] {
			continue
		}
		t.Rows = append(t.Rows, comparePkgRow(p, false))
	}

	return t
}

// compareKeepGroupRow is one group as one row: the label and how many packages
// it stands for, the version pair every member shares, and the breakdown of
// what was compressed.
//
// # The STATE cell is read from the first member, and that is not a derivation
//
// A group's identity IS its version pair, and Status is how the two versions
// relate — so every member of a group has the same status by construction, and
// reading it off the first one reports the group's own value rather than
// computing a new one. A member the Keep list does not carry leaves the cell
// empty, which says "the run established none" in the way every empty cell in
// this report does.
func compareKeepGroupRow(g KeepGroup, status map[string]string) Row {
	shared := ""
	if len(g.Members) > 0 {
		shared = status[g.Members[0]]
	}

	return Row{
		Cells: []string{
			fmt.Sprintf("%s (%d packages)", g.Label, len(g.Members)),
			g.Local,
			g.Remote,
			shared,
		},
		Detail: compareCategoryBreakdown(g.ByCategory),
	}
}

// compareCategoryBreakdown is a group's ByCategory as one line: what was
// compressed, beside the count of how much.
//
// It is a Row.Detail rather than prose because it is one line per row and it
// stays meaningful when it is cut — the first categories are the largest, so a
// shortened breakdown still says what the group mostly is. It is
// folded for the reason every detail in this package is: a raw newline
// corrupts both the plain writer and the Markdown pipe table, and the category
// names arrive from the producer.
func compareCategoryBreakdown(counts []CategoryCount) string {
	if len(counts) == 0 {
		return ""
	}

	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s ×%d", c.Category, c.Count))
	}

	return foldToOneLine(strings.Join(parts, ", "))
}

// compareKeepNotes is the omission the keep table made and how to undo it.
//
// # Both numbers come from the payload, never from the rows that were built
//
// The member count is summed over KeepGroups.Members and the total over Keep,
// which are fields of the run; the count of rows in the table is a consequence
// of ShowAll and moves with it. Reading the latter would make the sentence
// under the table disagree with the sentence above it exactly when an operator
// passes the flag to check.
//
// The wording keeps its shape in both directions and only the tail changes,
// which is what manifest_run.go and snapshot_run.go already do for the same
// flag. A run with no groups gets no note: nothing was collapsed, so there is
// nothing to say --all would expand.
func compareKeepNotes(r CompareRun, listEvery bool) []string {
	if len(r.KeepGroups) == 0 {
		return nil
	}

	members := 0
	for _, g := range r.KeepGroups {
		members += len(g.Members)
	}

	if listEvery {
		return []string{fmt.Sprintf(
			"%d of the %d share a version pair and would collapse into %d group(s); --all listed every member above instead.",
			members, len(r.Keep), len(r.KeepGroups))}
	}

	return []string{fmt.Sprintf(
		"%d of the %d share a version pair and are listed above as %d group(s); pass --all to list their members.",
		members, len(r.Keep), len(r.KeepGroups))}
}

// compareUnknownSection is the block that exists so an absence cannot pass for
// an answer: the packages the run reached no recommendation about.
//
// Its rows carry no DIFF column for the same reason the keep block's do not —
// there is no decision for the number to support — and they do carry their
// details, because the reason the run could not answer is the only thing an
// entry here has to offer.
func compareUnknownSection(r CompareRun) Section {
	s := Section{Title: "Unknown — no recommendation"}

	if r.Verdicts.Unknown == 0 && len(r.Unknown) == 0 {
		s.Lead = []string{"Every package the run judged reached a recommendation, so nothing is listed here."}
		return s
	}

	if len(r.Unknown) == 0 {
		s.Lead = []string{fmt.Sprintf("%d package(s) are counted as unknown.", r.Verdicts.Unknown)}
		s.Notes = []string{"No per-package row reached this report, so the count above is stated without the list it was taken over."}
		return s
	}

	s.Lead = []string{fmt.Sprintf(
		"%d package(s) listed. Nothing on record describes them, so neither advice is supported.", len(r.Unknown))}
	s.Rows = comparePkgTable(r, r.Unknown, false)
	s.Notes = comparePkgNotes(r.Unknown)

	return s
}

// compareSummarySection is the tally, last, after the evidence it counts.
//
// The verdict counts cover every package scanned, while the tables hold only
// what reached this report, so the third line states how many scanned packages
// have no row above at all — otherwise a reader adding up rows lands on a
// smaller number with nothing to say why.
//
// That line is the one number measured on the lists, and it does not break
// the never-derive-a-counter-from-len rule: it asks about the SCREEN, and what
// is on screen IS the lists. It used to read OnlyLocal, which agrees only on an
// unfiltered run: under --only-redundant a 265-package overlay showed three
// rows while OnlyLocal still read 101, telling an operator deciding a deletion
// that they saw 161 packages they did not. A member collapsed into a group
// counts as listed — its group's row stands for it — so the number does not
// move with --all. A negative is floored at zero: lists outrunning the scan is
// a malformed payload, and "-3 of 265" is arithmetic nobody can act on.
//
// The needs-rebase column prints even at zero: dropping a zero would make
// "no package needs a rebase" and "nobody counted" the same line.
func compareSummarySection(r CompareRun) Section {
	listed := len(r.Redundant) + len(r.NeedsRebase) + len(r.Keep) + len(r.Unknown)

	unlisted := r.Scanned - listed
	if unlisted < 0 {
		unlisted = 0
	}

	return Section{
		Title: "Summary",
		Lead: []string{
			fmt.Sprintf("%d scanned · %d in both · %d only here", r.Scanned, r.InBoth, r.OnlyLocal),
			fmt.Sprintf("keep %d · redundant %d · needs rebase %d · unknown %d",
				r.Verdicts.Keep, r.Verdicts.Redundant, r.Verdicts.NeedsRebase, r.Verdicts.Unknown),
			fmt.Sprintf("Verdicts count every package scanned; %d of %d have no row above.", unlisted, r.Scanned),
		},
		// The run's own sentences go LAST, under the tally, and the last block
		// is the only fixed position this payload has: `func (r Run) Sections`
		// in run.go PREPENDS an interruption block to an incomplete run, so
		// filing "no ::gentoo tree was reached" under the first block would
		// explain one gap with another gap's heading.
		//
		// The slice is copied rather than handed over. Sections builds fresh
		// blocks on every call, and a renderer that appended to what it was
		// given would otherwise write back into the payload it is rendering.
		Notes: append([]string(nil), r.Notes...),
	}
}

// compareFindingsLead introduces a block's package findings once, so a reader
// meets a sentence rather than a list of loose strings under a table. It is
// only ever emitted where at least one such finding follows it.
const compareFindingsLead = "Beside the reason on each row, the run established more about these packages:"

// comparePkgNotes is every further finding the packages of ONE block carry,
// each said under a lead that explains what it is doing there.
//
// # Placement is by CONSTRUCTION, and that is the gain
//
// These sentences used to be built by the command and then filed into a section
// by searching every rendered row for the package's atom — `type Section` in
// section.go carries no identifier and a Title is prose, so the rows were the
// only honest handle on "which block shows this package". Here the question
// never arises: a block is built from ONE list, so the findings under it are
// the findings of the packages in it, and no search can put one under the wrong
// table.
//
// The package is named in every sentence and not merely used to place it: a
// note is read as prose, several may sit under one table, and a reader must not
// have to count rows to learn which package a sentence is about.
//
// It returns nil when no package in the list has anything further, which is
// most runs. A lead introducing an empty list would be a heading over nothing.
func comparePkgNotes(pkgs []ComparePkg) []string {
	var notes []string
	for _, p := range pkgs {
		for _, finding := range p.FurtherFindings {
			if notes == nil {
				notes = []string{compareFindingsLead}
			}
			notes = append(notes, p.Package+": "+finding)
		}
	}
	return notes
}

// comparePkgTable is a list of packages as a table, with the DIFF column only
// where a difference is evidence for the section's recommendation.
func comparePkgTable(r CompareRun, pkgs []ComparePkg, withDiff bool) Table {
	t := Table{Headers: comparePkgHeaders(r, withDiff)}
	for _, p := range pkgs {
		t.Rows = append(t.Rows, comparePkgRow(p, withDiff))
	}
	return t
}

// comparePkgHeaders names the columns, and names the two version columns after
// the two trees being compared.
//
// # One side is a literal and the other is read from the payload
//
// The local side is the overlay this toolkit maintains, and there is only ever
// one of it, so it is named here rather than carried as a field nothing could
// ever set differently. The remote side is whatever repository the run was
// pointed at, which is a fact the run established and which CompareRun carries
// — so a run compared against something other than ::gentoo prints that
// repository's name over its own column instead of a wrong one.
//
// Repository is upper-cased here and nowhere else. The field's own doc is
// explicit that the string travels as the producer spelled it and that no
// renderer decorates it; this is a COLUMN HEADING rather than the value, every
// heading in this package is upper-case, and doing it once here is what keeps
// every renderer printing the same one.
func comparePkgHeaders(r CompareRun, withDiff bool) []string {
	headers := []string{"PACKAGE", "BENTOO", compareRemoteColumn(r), "STATE"}
	if withDiff {
		headers = append(headers, "DIFF")
	}
	return headers
}

// comparePkgRow is one package as one record: the atom, the two versions, how
// they relate, and — where the section carries the column — what the content
// check found.
//
// Every cell is the payload's own string, printed as it was carried. Diff in
// particular is drawn from a closed vocabulary the adapter produces, and
// re-wording it here would be a second implementation of the one thing that
// vocabulary exists to guarantee.
func comparePkgRow(p ComparePkg, withDiff bool) Row {
	cells := []string{p.Package, p.Local, p.Remote, p.Status}
	if withDiff {
		cells = append(cells, p.Diff)
	}
	return Row{Cells: cells, Detail: compareDetail(p)}
}

// compareDetail is the row's own finding, in one line, with the failed-review
// marker after it.
//
// # The marker goes LAST, and that is a decision about what survives a cut
//
// A detail is shortened to the width the device allows and marked where it was
// cut, so whatever sits at the end is the first thing to go. The
// finding is what the operator needs; the marker repeats a fact the first
// section already states for the whole run and that ComparePkg.Reading carries
// in full in the exported document. Putting the marker first would spend the
// visible width on the redundant half.
//
// The reason is FOLDED, never shortened: a raw newline corrupts both the plain
// writer and the Markdown pipe table. The adapter folds it too, and folding an
// already folded string costs nothing, while trusting the producer is how one
// unfolded string reaches a table.
func compareDetail(p ComparePkg) string {
	reason := foldToOneLine(p.Reason)

	if p.Reading != readingFailed {
		return reason
	}
	// The marker names the review's cause when one was recorded,
	// and stays the bare marker the scope note explains when none was.
	mark := compareReadingFailedMark
	if p.Cause != "" {
		mark = "[reading failed: " + p.Cause + "]"
	}
	if reason == "" {
		return mark
	}
	return reason + " " + mark
}

// compareRepository is the compared repository as the report names it in
// prose: the operator's own word for it, in the :: form a Gentoo repository is
// written in.
//
// The :: is added HERE rather than carried on the field, which is what
// CompareRun.Repository's doc asks for: the value travels as the producer
// spelled it, and one place composes the sentence so that every renderer
// prints the same string.
//
// An empty Repository is a producer that never said which tree was compared,
// and the fallback says so in words instead of printing a bare "::" — a reader
// meeting that would be looking at a bug rather than at an answer, which is the
// same reason snapshot_run.go refuses to print a sentence trailing into an
// empty list.
func compareRepository(r CompareRun) string {
	if r.Repository == "" {
		return "the compared repository"
	}
	return "::" + r.Repository
}

// compareRemoteColumn is the same name as a column heading, upper-cased, with
// a neutral word when the run named no repository.
func compareRemoteColumn(r CompareRun) string {
	if r.Repository == "" {
		return "REMOTE"
	}
	return strings.ToUpper(r.Repository)
}
