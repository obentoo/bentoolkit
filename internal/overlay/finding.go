package overlay

import "strings"

// Finding is one thing a pass under internal/overlay established for the
// operator, held as a VALUE instead of as a line this package composed and
// coloured.
//
// A fact held only as a printed line — colour picked, width applied, magnitude
// interpolated — cannot be exported to JSON, re-laid-out for Markdown or a log,
// counted or filtered by the command that received it, or tested without
// capturing stdout. Returning the facts is what lets `overlay compare` move
// into the report envelope.
//
// The shape is deliberately flat, with optional fields: every producer in this
// package answers the same two questions — WHAT is this about, and WHAT was
// found — so a renderer walks one list rather than type-switching over a union,
// as CompareResult already carries its four annotation passes.
//
// Every optional field's zero value means NOTHING WAS ESTABLISHED, never a
// claim; a field whose zero would assert something (a CompareStatus, whose zero
// is "up-to-date") is deliberately NOT here — see Status's absence, below.
type Finding struct {
	// Kind names WHICH finding this is, so a renderer can group, count or
	// suppress a class of them without pattern-matching the prose. It is the
	// one field every producer sets.
	Kind FindingKind

	// Atom is what the finding is about: "<category>/<package>", exactly as an
	// operator types it.
	//
	// It is a FIELD and never a fragment of Detail: an identifier formatted into
	// a sentence cannot be counted, filtered on, used as a lookup key or
	// re-emitted as a JSON field — it can only be printed.
	//
	// It is empty on a RUN-scoped finding, and nowhere else. Exactly one kind is
	// about the run: FindingBaselineSkipped, which says no ::gentoo tree was
	// reached. Naming a package would assert something the run never established
	// about it, and repeating the sentence on all 321 packages would bury the
	// per-package findings. That is NOT a licence for a blank atom elsewhere:
	// renderCompareFindings' section caveat is not a Finding for the same reason
	// (it is about a subset the CALLER chose), and a package-scoped finding with
	// no atom is a bug the tests assert as one.
	Atom string

	// Detail is what was found, in one sentence the producer wrote and NOBODY
	// decorated: no colour, no escape sequence, no leading glyph, no padding, no
	// trailing newline. The "⚠ " that opens a warning today is the renderer's,
	// and so is the atom in front of the colon.
	//
	// It overlaps the fields below on purpose, as report.ManifestTarget.Error
	// does beside its Package: the fields are the FACTS, and this is the
	// producer's own sentence over them, kept verbatim so today's rendered line
	// survives unchanged and a later rewrite has a before to diff against.
	//
	// A divergence's EFFECT and its provenance are NOT flattened in here; they
	// are Effect below. "ours: files/nodejs-26.7.0-gcc17.patch" tells an operator
	// deciding whether to keep or delete a copy nothing; what must be headlined
	// is what the divergence DOES and who says so, and composed into this string
	// it could never be re-headlined.
	Detail string

	// Version is the overlay's version the finding was established at, and
	// Upstream is ::gentoo's, both empty when the finding is not about a version
	// pair.
	//
	// Two fields rather than one joined string because they are compared, not
	// concatenated: a consumer asking "did the two sides agree?" would otherwise
	// have to split a sentence apart to find out.
	Version  string
	Upstream string

	// Entry is the registry key that declared the divergence — the identifier an
	// operator greps packages.toml for — and empty when nothing declared one.
	//
	// It is printed VERBATIM wherever it is printed: a shortened key looks usable
	// and is not, which is worse than printing none. See patchedReasonCap, which
	// makes the opposite call about the reason for the opposite reason.
	Entry string

	// Added and Removed are the size of the difference between our ebuild and
	// ::gentoo's, ours against upstream's, and are zero on every finding that has
	// no difference to measure.
	//
	// They DESCRIBE and never decide: a large diff authorises nothing and
	// a small one forbids nothing. compare_diff_counts_fence_test.go holds that
	// mechanically over CompareResult's own DiffAdded/DiffRemoved, and these two
	// are named differently precisely so the fence keeps watching the source of
	// truth rather than a copy of it — a copy nobody may compute on either, but
	// one whose misuse the fence was never written to catch.
	Added, Removed int

	// Classified is how the three-way reduction divided ONE package's
	// differences — how many were attributed to the version move, how many to
	// us, how many to neither — together with how far the attribution reached.
	// Its zero value means NOTHING WAS CLASSIFIED, which is every package of
	// every run that asked for no baseline review, and is the same predicate
	// (`== (Classified{})`) that keeps those runs' rendering unchanged.
	//
	// It is the STRUCT and not a sentence, for the reason Atom is a field: "17
	// differences against the baseline, 4 of them attributed to nobody" cannot be
	// summed across a run, key a consumer's threshold, or reach a JSON export
	// without its digits being parsed back out. Detail is the sentence over it.
	//
	// It DESCRIBES and never decides, like Added and Removed: a large
	// unclassified count authorises no realignment and forbids none. Reduced and
	// Span are carried with the three counts because they are read TOGETHER — a
	// third point refused for being too wide and one never offered both
	// attribute nothing, and only the span tells them apart.
	Classified Classified

	// Authorship is what the overlay's own CONTENT proved about where a
	// difference came from. Its zero, AuthorshipUnproved, means THE REPORT CANNOT
	// TELL and is never "the change is upstream's" — which is exactly the
	// "nothing was established" zero every optional field here needs.
	Authorship Authorship

	// ProvedBy names the file that proves it, package-relative ("files/<name>"),
	// so the claim is confirmed with one `ls`. It is empty whenever Authorship is
	// unproved: there is then no file to name.
	//
	// It is SECONDARY EVIDENCE and not the headline. A filename proves authorship
	// and says nothing about what the divergence does, which is what an operator
	// deciding to keep or delete needs — see Effect.
	ProvedBy string

	// Effect is what the divergence DOES and whose sentence that is, held as two
	// values so a renderer can say which it is reading.
	//
	// This is the field the standing knowledge about `overlay compare` asks for
	// by name. Its zero — no text, EffectUnstated — is an honest "not known", and
	// a renderer meeting it says so rather than filling the silence.
	Effect Effect

	// Origin is which side a MODEL read the divergence as coming from. Its zero,
	// OriginUnknown, means nothing was said, which is every run no review
	// reached.
	//
	// It sits beside Effect rather than inside it because they are not the same
	// claim: Effect.Text is what the difference does, Origin is where it came
	// from, and a model can be useful about one while saying nothing about the
	// other. It is COMMENTARY in both cases — nothing that decides a verdict or
	// an exit code may read it.
	Origin ReviewOrigin

	// Proposal is the `patched` declaration a model offered for a divergence it
	// read as ours, empty otherwise.
	//
	// It is carried AT FULL LENGTH. The rendered line caps it at
	// patchedReasonCap so one model's essay cannot decide the width of the
	// report, and that cap is the RENDERER's: a value truncated on the way into
	// the report is truncated in the JSON export too, where there is no width to
	// respect and nothing to un-truncate it from.
	Proposal string

	// Deliberately absent: a Status or Verdict field.
	//
	// Both are answered for every compared package by CompareResult, which the
	// caller is already holding, so a copy here would be a second answer free to
	// drift from the first. Both also have zero values that ASSERT — StatusUpToDate
	// and VerdictKeep are real answers, not "unset" — so a finding from a producer
	// with no version comparison to report would silently claim its subject was
	// up-to-date and worth keeping. The relationship a comparison found is stated
	// in words by Detail on the FindingCompared entry, and in numbers by Version
	// and Upstream.
}

// FindingKind is which finding this is: the closed vocabulary a renderer groups,
// counts and suppresses by.
//
// The four values below are `overlay compare`'s. The later producers add their
// own to the same type rather than to types of their own, so that one renderer
// can walk a mixed list — which is what a report assembling several passes into
// one document does.
type FindingKind int

const (
	// FindingCompared is the row-level statement: how the overlay's version of
	// one package relates to ::gentoo's, and what is recommended about it. It is
	// established for EVERY compared package, which is what makes the finding
	// list a complete account of a run rather than a list of its exceptions.
	//
	// It is the zero value because it is the finding that claims the least: a
	// producer that forgot to set a Kind reports "here is a package the run
	// looked at", not "here is a warning".
	FindingCompared FindingKind = iota
	// FindingStaleDeclaration is a registry entry describing a divergence that no
	// longer exists — the two ebuilds are byte-identical — so it is suppressing a
	// removal recommendation for nothing.
	FindingStaleDeclaration
	// FindingUndeclaredDivergence is the loud one: our ebuild is not the one
	// ::gentoo ships and no entry says why, on a package the report is about to
	// list as a removal candidate. Authorship and ProvedBy say whether the
	// content settled who wrote the difference; unproved is NOT a finding that it
	// is ::gentoo's.
	FindingUndeclaredDivergence
	// FindingDeclaredDivergence is the declaration itself, stated wherever it has
	// not already been contradicted. It is what makes a patched package
	// visible at all on an API-only run, where no content check can run and a
	// patched package would otherwise print exactly like an unpatched one.
	FindingDeclaredDivergence

	// The BASELINE REVIEW's kinds, added to this same type by
	// annotate_baseline.go rather than to a type of its own — which is what the
	// paragraph above promised the later producers would do, and what lets one
	// renderer walk a list holding both a comparison's findings and a baseline
	// review's.
	//
	// They are eight rather than one because a renderer groups, counts and
	// suppresses BY KIND, and these are eight different things to say: "here is
	// the file we measured against" is evidence, "we could not measure" is an
	// absence, "the inherit lines differ" is a divergence, and a model's opinion
	// about realigning is none of the three. Collapsed into one kind they would
	// have to be told apart by pattern-matching the prose, which is the defect
	// the whole vocabulary exists to remove.

	// FindingBaseline names the ::gentoo ebuild one package was measured
	// against: which version, how far it is from ours, and the path, so
	// "what was this compared with" is answered by the report rather than
	// re-derived by whoever reads it. Upstream carries the baseline's version
	// beside Version, which is ours.
	FindingBaseline
	// FindingBaselineUnexamined is the per-package "we could not look": ::gentoo
	// carries the package but the ebuild would not read, or a baseline was named
	// and could not be opened. It is stated rather than left silent
	// because a package nobody could measure is otherwise indistinguishable from
	// one that matched ::gentoo exactly.
	FindingBaselineUnexamined
	// FindingBaselineSkipped is the RUN-level version of the same absence: no
	// ::gentoo tree was reached at all, so nothing was compared against ::gentoo.
	// It is the one kind with no Atom — see the field's own doc for why it may
	// not borrow one — and the one the caller derives an exit code from.
	FindingBaselineSkipped
	// FindingAxisDivergence is one structural axis on which our ebuild differs
	// from the baseline: inherit, options, iuse or deps. The DETAIL
	// is what makes it actionable — "the inherit lines differ" never told anyone
	// which eclass ::gentoo delegates the option list to — so the difference is
	// stated and not merely counted.
	FindingAxisDivergence
	// FindingEbuildDeclaration is a `# BENTOO-DIVERGENCE:` tag our own ebuild
	// carries: the maintainer's account, in the ebuild, of why a
	// difference is there. It is NOT FindingDeclaredDivergence, which is the
	// registry's `patched` entry — two different files, written and edited by
	// different acts, and one kind for both would leave a consumer unable to say
	// which of them it had read.
	FindingEbuildDeclaration
	// FindingExpiredDeclaration is such a declaration whose `drop-when:`
	// condition has been evaluated against the tree and is MET: the
	// divergence it was protecting is back in front of the review. It is the
	// loud one and is its own kind so a renderer can keep it loud — read as an
	// ordinary declaration it would stay quiet forever.
	FindingExpiredDeclaration
	// FindingClassification is what the three-way reduction made of one
	// package's differences. Classified carries the counts and
	// the reach as numbers; Detail is the sentence over them.
	FindingClassification
	// FindingOtherRepo is a repository other than ::gentoo that was consulted
	// about this package. It is INFORMATIVE ONLY — a
	// repository outside ::gentoo has not been through the same review, and
	// nothing proposes a realignment from one.
	FindingOtherRepo
	// FindingRealignVerdict is a MODEL's reading of whether a divergence still
	// earns its place. Its Effect carries the model's own sentence
	// with EffectReviewed on it, so a renderer can say whose words these are; it
	// is commentary and nothing that decides a verdict or an exit code may read
	// it.
	FindingRealignVerdict
)

// String returns the finding's stable word, used in a report and in an export.
//
// The words are hyphen-free and lowercase because they are a machine
// vocabulary first — a `kind` in an exported document, which a consumer greps —
// and a label second.
func (k FindingKind) String() string {
	switch k {
	case FindingCompared:
		return "compared"
	case FindingStaleDeclaration:
		return "stale-declaration"
	case FindingUndeclaredDivergence:
		return "undeclared-divergence"
	case FindingDeclaredDivergence:
		return "declared-divergence"
	case FindingBaseline:
		return "baseline"
	case FindingBaselineUnexamined:
		return "baseline-unexamined"
	case FindingBaselineSkipped:
		return "baseline-skipped"
	case FindingAxisDivergence:
		return "axis-divergence"
	case FindingEbuildDeclaration:
		return "ebuild-declaration"
	case FindingExpiredDeclaration:
		return "expired-declaration"
	case FindingClassification:
		return "classification"
	case FindingOtherRepo:
		return "other-repository"
	case FindingRealignVerdict:
		return "realignment-verdict"
	default:
		// A value with no word — reachable only if a kind is added without a case
		// here — reads as "unknown" rather than as a bare integer, matching
		// CompareStatus.String() and Verdict.String().
		return "unknown"
	}
}

// Effect is what a divergence DOES, together with who says so.
//
// # Why the two are separate values
//
// An operator reading a divergence finding is deciding whether to keep the
// overlay's copy or let `prune` delete it. "ours:
// files/nodejs-26.7.0-gcc17.patch" does not support that decision; "slots the
// install under /usr/lib/node-${SLOT} and wires it to app-eselect/eselect-nodejs,
// so several majors coexist" does. The first is a filename, the second is the
// effect — and they are not interchangeable.
//
// The effect alone is still not enough, because the same sentence can arrive
// from sources an operator must weigh differently: a maintainer's own
// declaration is a commitment, and a language model's reading is a guess that
// has been wrong before. Composed into one string they become indistinguishable,
// and the operator acts on the guess as though it were the commitment. Held
// apart, a renderer can label the guess — which is what the two `↳` leads in
// compare.go have always done, and this is those leads expressed as data.
type Effect struct {
	// Text is the sentence, at full length and with its internal whitespace
	// collapsed to single spaces.
	//
	// The collapse is SANITISATION, not layout. Some of this text is a language
	// model's, and the report's structure is its lines — "⚠ " opens a finding
	// this tool stands behind — so a newline inside a summary would forge a
	// second finding line about a package that need not exist. That is true of
	// every line-oriented consumer, including one reading the JSON export, so it
	// is done once here rather than hoped for at each renderer. What is NOT done
	// here is capping: a width is a terminal's business and the export has none.
	Text string
	// Source is where the sentence came from, and so how far it may be trusted.
	Source EffectSource
}

// EffectSource ranks the three ways the report can come to know what a
// divergence does, most trusted first.
//
// The order is a statement about EVIDENCE and not about presentation: a
// maintainer wrote the declaration and stands behind it, a model produced a
// reading and stands behind nothing, and an absent answer is an absent answer.
// A renderer is free to show them however it likes, and is not free to present
// the second as the first.
type EffectSource int

const (
	// EffectUnstated means nobody said what the divergence does. It is the ZERO
	// value, so a finding whose effect nobody established says "not known"
	// rather than accidentally claiming the empty string is the answer.
	//
	// It is not a dead end for the operator: the identifiers needed to find out
	// — the atom and the version — are on the finding, and the renderer is what
	// names the command that would show the difference. That sentence names a
	// CLI, and a library that named one would be deciding what its caller is.
	EffectUnstated EffectSource = iota
	// EffectDeclared is the maintainer's own reason — the registry's `patched`
	// text, or the ebuild's own `# BENTOO-DIVERGENCE:` tag, which is the same
	// commitment written in the other file. It is the most trusted source there
	// is, because somebody committed it on purpose.
	EffectDeclared
	// EffectReviewed is a MODEL's reading of the two ebuilds — a reading, never a
	// proof, and never an input to anything this report decides. A
	// renderer that drops the distinction invites the operator to act on a guess.
	EffectReviewed
)

// declared builds an Effect from text a maintainer committed to the registry.
//
// Whitespace-only text yields the ZERO Effect rather than an EffectDeclared with
// nothing in it: a source label over an empty sentence claims the maintainer
// said something, and they did not. Validation rejects such a reason, but
// LoadPackagesConfig never calls ValidatePackageConfig, so the compare
// path sees entries validation never judged — this is a production case, not
// defensive padding.
func declared(text string) Effect {
	return effect(text, EffectDeclared)
}

// reviewed builds an Effect from a model's reading, on the same terms.
func reviewed(text string) Effect {
	return effect(text, EffectReviewed)
}

// effect is the one constructor both go through, so the empty-text rule is
// stated once and cannot come to differ between the trusted source and the
// untrusted one.
func effect(text string, source EffectSource) Effect {
	collapsed := oneLine(text)
	if strings.TrimSpace(collapsed) == "" {
		return Effect{}
	}
	return Effect{Text: collapsed, Source: source}
}
