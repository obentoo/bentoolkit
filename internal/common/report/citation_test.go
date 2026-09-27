package report

// Authored for story 046, sub-task 10.4 — S046-R8.3.
//
// The rule: no comment and no string under internal/common/report, render/
// included, may cite a requirement number that story 046 does not have without
// saying which story does. A number 046 never issued is unresolvable: the same
// token exists in a dozen other stories in this repository, so a reader meeting
// it has no way to find the sentence it points at. .epic/ is not committed
// (`git ls-files .epic/` returns nothing), which is what makes this a code
// problem rather than a bookkeeping one — the comment is the whole record.
//
// A bare number that DOES resolve inside 046 is not refused here. It is not
// resolved either, and the section titled "RESOLVING IS NOT ATTRIBUTING" below
// is the correction — read it before trusting a green run of this guard.
//
// The disambiguation already exists and is used several hundred times across
// the repository: an S0NN- prefix naming the owning story. This guard requires
// it exactly where a bare citation cannot be resolved, and nowhere else.
//
// # RESOLVING IS NOT ATTRIBUTING — amended by sub-task 11.2 (S046-R8.3)
//
// This file was written with a second claim beside the rule, and the second
// claim was wrong. It read: "A bare number that resolves inside 046 is fine —
// it is unambiguous where it stands." That holds in a package story 046 wrote
// from nothing. This is not one. Story 044 wrote most of it and issues
// S044-R1.1 through S044-R10.5, so every number 046 has, 044 has too, with a
// different sentence behind it. A bare citation here that citationFault accepts
// has therefore been matched on its NUMBER, not on its MEANING.
//
// It is not hypothetical, and the collision is not confined to code 046
// inherited. section.go is a file story 046 CREATED, and line 84 of it says a
// repeated reason is "printed once (R7.2), while the report itself still
// carries the whole string (R7.4)". Those two are S044-R7.2 and S044-R7.4:
// 046's own R7.2 is the presentation-field guard and its R7.4 is "adding a kind
// edits no renderer". Neither is what the comment is about, and this guard
// called both of them resolved.
//
// citationFault is UNCHANGED by the amendment. A bare-but-resolvable citation
// is still not a hard failure, because 356 of them are in this package and
// failing all 356 would fail the package rather than fix it. What changed is
// that the class stopped being silent: citation_debt_test.go counts it, prints
// the per-file breakdown on every run, and fails when it grows.
// unattributedCitationDebt at the foot of this file is the ceiling, and the
// evidence that the ceiling bites is recorded beside it. Deciding which story
// each of the 356 meant is story 047's work — done mechanically it would write
// confident falsehoods, and a wrong prefix is worse than a bare number because
// it looks resolved.
//
// # THE SECOND NUMBERING — DESIGN DECISIONS (sub-task 16.5 — S046-R8.3)
//
// The amendment above is about requirement numbers, and requirement numbers
// are half of what a story issues. design.md numbers its decisions too —
// S046-D1 through S046-D10 — and a comment reading "the mode is resolved once
// per run (D4)" is a citation in precisely the sense this file means: a token
// standing in for a sentence written somewhere that is not committed.
//
// It collides exactly as the R-class does, and the collision is already in the
// tree rather than hypothetical. render/fullscreen_sections_test.go opens by
// naming story 044's seventh decision in prose and writing the token bare;
// render/inline.go and render/text_test.go each cite a bare fifth decision
// that means S044-D5, and 046's own fifth is titled "tui.Reporter is
// unchanged, and D5 of story 044 still holds" — a design document stating in
// its own heading that the number is shared and the sentences are not.
//
// Measured on 2026-09-03 over the swept tree: 101 bare decision citations in
// 29 files, spanning all ten numbers story 046 issues. Until this sub-task
// citationPattern matched the R form and nothing else, so every one of the 101
// was invisible to the guard AND to the ceiling — a debt no artifact published
// because no artifact could see it.
//
// The widening carries them as COUNTED DEBT rather than as a hard failure:
// the decision sub-task 11.2 took for the R-class, for the reason it gave.
// Refusing 101 arrivals at once fails the package instead of fixing it, and
// deciding which story each token meant is reading a comment, not sweeping a
// tree — done mechanically it writes confident falsehoods, and a wrong prefix
// is worse than a bare token because it looks resolved.
// unattributedDecisionDebt at the foot of this file is that ceiling, and it is
// a SEPARATE number from the requirement one. Separate is the whole point: one
// summed total would let a fall in either class pay for a rise in the other,
// which is a debt that grew, published as a debt that did not.
//
// A decision citation naming a number 046 does NOT issue is a hard failure,
// exactly as its R-class equivalent is — the choice this sub-task made where
// its ToDo left one open, because it is the strictly stronger of the two and
// costs nothing today: all ten numbers in the tree are inside 046's set, so
// the branch has no live instance to refuse. That is also what makes it a
// guard green on the day it is written, and the evidence that it bites is at
// the foot of this file with the rest.
//
// # RED ON ARRIVAL — 79 of them
//
// This guard is not green on the day it is written. It finds 79 unresolvable
// citations across 21 files under this package, all of them numbers issued by
// story 044 and carried over with the code: 12 distinct requirements, R-9.3
// eighteen times, R-2.5 thirteen, R-6.4 seven, and so on down. Sub-task 10.4's
// work is to prefix exactly those, and this file is the list.
//
//	$ go test ./internal/common/report/ -run TestCitationsResolve
//	--- FAIL: TestCitationsResolve (0.01s)
//	    citation_test.go:457: autoupdate_check.go:48 cites R-9.3 — story 046 has
//	        no R-9.3, and the citation is bare, so it names nothing a reader can
//	        resolve.
//	        [... 78 more, one per occurrence ...]
//	    citation_test.go:491: 79 of the 471 citations read across 37 files
//	        cannot be resolved
//
// # EVIDENCE THAT IT STILL FAILS ONCE IT IS GREEN (S046-R8.3)
//
// The paragraph above stops being checkable the moment 10.4 finishes: from then
// on this guard passes, and a green test proves nothing about what it would
// catch. S046-R8.3 asks for the evidence a green guard cannot supply, and the
// story artifacts holding it (.draft/red-evidence.yaml) are not committed — so
// a pointer to that file is a pointer to nothing for anyone who cloned this. It
// is written down here instead, the way boundary_fields_test.go writes down its
// own.
//
// Measured on 2026-08-29. The finished state was simulated first, so that each
// mutation could be seen on its own rather than as an eightieth line in a list
// of seventy-nine: every offending citation in the package was prefixed with
// S044- in place, which took the guard to green — the sweep is not failing
// vacuously, it goes green exactly when the rule holds. Three mutations were
// then applied against that green state, one at a time, each run and reverted
// immediately, and the whole package was restored with `git checkout -- ` and
// the restore verified by an empty `git status --porcelain`.
//
// A note on the transcripts: a requirement number is written below with a
// hyphen after the R — R-9.3 for what the run printed without that hyphen —
// because this file is swept by its own rule and quoting a bare citation
// verbatim would make it violate the thing it checks. The hyphen is the only
// edit; every other character is as it was printed.
//
// Mutation 1 — a bare citation, in a comment, in mode.go:
//
//	mode.go:118 cites R-9.3 — story 046 has no R-9.3, and the citation is bare,
//	    so it names nothing a reader can resolve.
//	        Remedy: prefix the citation with the story that owns the requirement,
//	        in the S0NN- form used throughout this repository. The story files
//	        under .epic/ are not committed, so this comment is the only record of
//	        what the code was answering.
//	1 of the 470 citations read across 37 files cannot be resolved
//
// Mutation 2 — this story's own prefix on a number this story does not have,
// in model.go. It is not a repetition of the first: the prefix is the remedy's
// own invention, so the rule that motivated it cannot name what would collide
// with it, and prefixing every offender with the story being worked on is the
// cheapest way to satisfy mutation 1 while asserting something false.
//
//	model.go:89 cites S046-R-9.3 — the citation claims story 046 owns R-9.3, and
//	    story 046 has no R-9.3.
//	        [the Remedy paragraph above, word for word]
//	1 of the 470 citations read across 37 files cannot be resolved
//
// Mutation 3 — a citation inside a string rather than a comment, in
// mode_test.go's failure message. Five of the 79 found on arrival live in that
// position, and a sweep that read only comments would have left them.
//
//	mode_test.go:123 cites R-6.4 — story 046 has no R-6.4, and the citation is
//	    bare, so it names nothing a reader can resolve.
//	        [the Remedy paragraph above, word for word]
//	1 of the 470 citations read across 37 files cannot be resolved
//
// Each names the file, the line, the number and what to do about it, which is
// what makes the failure answerable by someone who did not write this file.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// story046Requirements is story 046's requirement set, written out rather than
// read from .epic/stories/046-report-across-the-cli/story.md.
//
// The file is not committed, so a guard that parsed it would pass vacuously —
// or fail spuriously — for everyone who cloned this repository. Copying the set
// into the guard is what gives the rule an existence independent of the story
// directory, which is the same reason the citations themselves have to be
// resolvable from the code alone.
//
// A copied set drifts, and this one did. R-3.6 was added to story.md by its
// third refinement, months after sub-task 10.4 transcribed the table, and the
// omission was found by sub-task 11.2 rather than by anything here: no bare
// citation of it exists in this package, so the guard had nothing to be wrong
// about yet. It would have been wrong the moment one appeared — a real
// requirement reported as a number story 046 never issued. Anyone adding a
// requirement to story 046 adds it here too, and the count below is 35.
// (Written R-3.6, with the hyphen, for the reason the header gives: this file
// is swept by its own rule.)
var story046Requirements = map[string]bool{
	"R1.1": true, "R1.2": true, "R1.3": true, "R1.4": true, "R1.5": true, "R1.6": true,
	"R2.1": true, "R2.2": true, "R2.3": true, "R2.4": true,
	"R3.1": true, "R3.2": true, "R3.3": true, "R3.4": true, "R3.5": true, "R3.6": true, "R3.7": true,
	"R4.1": true, "R4.2": true, "R4.3": true, "R4.4": true,
	"R5.1": true, "R5.2": true, "R5.3": true,
	"R6.1": true, "R6.2": true, "R6.3": true,
	"R7.1": true, "R7.2": true, "R7.3": true, "R7.4": true,
	"R8.1": true, "R8.2": true, "R8.3": true, "R8.4": true,
}

// thisStoryPrefix is the prefix that claims a citation belongs to story 046.
const thisStoryPrefix = "S046-"

// story046Decisions is story 046's design-decision set, transcribed from
// design.md: every level-two heading of the form "## DN." is one decision and
// nothing else in that file takes that shape. There are ten.
//
// Unlike story046Requirements this set has ONE copy rather than two, and the
// difference is deliberate. The requirement set is written twice — the table
// the guard reads and a transcription checked against it — because sub-task
// 13.1 needed a check that runs for everyone who cloned this repository, and
// two committed copies can be compared with .epic/ absent. What that pairing
// cannot catch is the case its own mutation C recorded: two copies agreeing
// with each other and with nothing else are consistent, and consistency is all
// a comparison of copies can see. With one copy there is nothing to compare,
// so the only check worth writing is the one against design.md itself, and
// TestCitationDecisionTableAgreesWithTheDesignFileWhereItIsPresent is it. It
// skips where .epic/ is absent — which is exactly where nobody is in a
// position to add a decision either.
//
// The ids are assembled from their numbers rather than written out, for the
// reason bareDecision gives: this file is swept by its own rule.
var story046Decisions = decisionSet("1", "2", "3", "4", "5", "6", "7", "8", "9", "10")

// decisionSet spells a set of decision ids out of their numbers.
func decisionSet(numbers ...string) map[string]bool {
	set := make(map[string]bool, len(numbers))
	for _, number := range numbers {
		set[bareDecision(number)] = true
	}
	return set
}

// citationClass names which of the two things a story numbers a citation
// points at. It is not a label on a message: the two classes carry SEPARATE
// ceilings, so the class is what decides which number a new arrival counts
// against, and mixing them would let one debt's repayment fund the other's
// growth.
type citationClass string

const (
	requirementCitation citationClass = "requirement"
	decisionCitation    citationClass = "decision"
)

// classOf reads a citation's class off its id, prefixed or bare.
func classOf(citation string) citationClass {
	if _, id := splitCitation(citation); strings.HasPrefix(id, "D") {
		return decisionCitation
	}
	return requirementCitation
}

// story046Issues answers whether story 046 issued the thing an id names,
// sending each class to its own set.
//
// citationFault asks this rather than reading a table directly, which is what
// keeps the rule below at three outcomes while the number of numberings grows:
// a third would add a case here and change nothing about what a fault IS.
func story046Issues(id string) bool {
	if strings.HasPrefix(id, "D") {
		return story046Decisions[id]
	}
	return story046Requirements[id]
}

// citationPattern reads a citation of either numbered thing a story issues — a
// requirement (R7.2) or a design decision (D4) — with its story prefix when it
// has one.
//
// The leading character class is the boundary, and it is load-bearing in both
// directions. Without it the pattern would find a citation inside an identifier
// that merely ends in R or D followed by digits, and — worse — it would read the
// tail of a prefixed citation as a bare one, turning every correctly
// disambiguated reference in the tree into a violation.
//
// Every number part is greedy for the converse reason: a citation is the whole
// number it is written as. Reading a prefix of it would collapse two distinct
// requirements into one and report a violation against a requirement nobody
// cited. On the decision form that is not a hypothetical symmetry — this
// package cites the first decision and the tenth, and a pattern reading one
// digit would read all five citations of the tenth as citations of the first.
//
// The two alternatives share no leading character, so their order inside the
// group is documentation rather than mechanism: neither can shadow the other.
var citationPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z_])((?:S[0-9]{3}-)?(?:R[0-9]+\.[0-9]+|D[0-9]+))`)

// citationRef is one citation and where it sits inside the text it was read
// from, counted in lines from the start of that text.
//
// The offset is not decoration. A doc comment in this package runs to fifty
// lines, and a failure that named only the comment's first line would send its
// reader to a paragraph and leave them to search it.
type citationRef struct {
	text string
	line int
}

// citationsIn reads every citation in a block of text.
func citationsIn(text string) []citationRef {
	var refs []citationRef
	for _, match := range citationPattern.FindAllStringSubmatchIndex(text, -1) {
		start, end := match[2], match[3]
		refs = append(refs, citationRef{
			text: text[start:end],
			line: strings.Count(text[:start], "\n"),
		})
	}
	return refs
}

// splitCitation separates a citation's story prefix from its requirement id.
// An empty prefix means the citation was written bare.
func splitCitation(citation string) (prefix, id string) {
	if hyphen := strings.IndexByte(citation, '-'); hyphen >= 0 {
		return citation[:hyphen+1], citation[hyphen+1:]
	}
	return "", citation
}

// citationFault answers why a citation cannot be resolved, or "" when it can.
//
// Three outcomes, and the third is the one a reader is most likely to miss:
//
//   - Bare, and 046 issues the number: not a fault here — and not resolved
//     either. Story 044 issues the same numbers, in BOTH numberings, so the
//     match is on the digits rather than on the sentence. It stays outside the
//     hard-failure class because 356 requirement citations and 101 decision
//     citations are in this package and refusing 457 at once would fail the
//     package rather than fix it; sub-task 11.2 gave the requirement class a
//     counted ceiling and sub-task 16.5 gave the decision class its own.
//     citationIsUnattributed at the foot of this file is that class, and the
//     header sections "RESOLVING IS NOT ATTRIBUTING" and "THE SECOND
//     NUMBERING" are why it exists.
//   - Bare, and 046 does not issue it: the fault this guard exists for.
//   - Prefixed S046-, and 046 does not issue the number: also a fault. The
//     prefix is the fix's own invention, so nothing in the rule above forbids
//     spelling it wrong, and the cheapest way to silence a bare-citation guard
//     is to prefix every offender with the story being worked on rather than
//     the story that owns the requirement. That produces a citation which is
//     confidently, checkably false — worse than the bare one, because it looks
//     resolved.
//
// A prefix naming any OTHER story passes unchecked. This guard knows one
// story's requirement set and cannot audit S044- or S033- without reading
// artifacts that are not committed; the prefix is the disambiguation, and
// checking that it points at a real sentence is a reader's job.
func citationFault(citation string) string {
	prefix, id := splitCitation(citation)

	switch {
	case prefix == "" && !story046Issues(id):
		return fmt.Sprintf("story 046 has no %s, and the citation is bare, so it names nothing a reader can resolve", id)
	case prefix == thisStoryPrefix && !story046Issues(id):
		return fmt.Sprintf("the citation claims story 046 owns %s, and story 046 has no %s", id, id)
	}
	return ""
}

// citationViolation is the sentence a maintainer meets. It names the file, the
// line, the citation and what to do, because a guard whose message stops at
// "unresolvable citation" is a guard that gets deleted rather than answered.
//
// The noun follows the citation's class. Telling the reader of a bare decision
// token to "prefix the requirement" sends them looking through story.md for a
// number that is only ever in design.md, which is a remedy that costs its
// reader the search the message was written to save. The requirement wording
// is unchanged to the byte, so the transcripts recorded in this file's header
// remain what the guard prints.
func citationViolation(path string, line int, citation, fault string) string {
	owned := "requirement"
	if classOf(citation) == decisionCitation {
		owned = "design decision"
	}

	return fmt.Sprintf(
		"%s:%d cites %s — %s.\n"+
			"    Remedy: prefix the citation with the story that owns the %s, in the S0NN- form used\n"+
			"    throughout this repository. The story files under .epic/ are not committed, so this comment is\n"+
			"    the only record of what the code was answering.",
		path, line, citation, fault, owned)
}

// bareID spells a citation out of parts, so that a fixture in this file is not
// itself a citation this file's own sweep would find.
//
// The guard reads string literals as well as comments, and it does not exempt
// itself — an exemption would be a hole exactly where someone tempted to hide a
// citation would put one. Writing the hostile fixtures whole would therefore
// make this file fail against itself. Assembling them at run time keeps the
// fixture and the rule in the same file without either one lying about the
// other.
func bareID(number string) string { return "R" + number }

// bareDecision does the same for a design-decision id, for the same reason.
func bareDecision(number string) string { return "D" + number }

// TestCitationRule pins the classifier, hostile cases first.
//
// The order is the point. Written after the tree had been cleaned, a case
// asking "does a good citation pass?" would pass against a classifier that
// approves everything, and a sweep built on that classifier would report a
// clean package forever. The cases that must FIRE come first, and each names
// the way the rule could be satisfied wrongly.
func TestCitationRule(t *testing.T) {
	cases := []struct {
		name     string
		citation string
		fault    bool
		why      string
	}{
		// Hostile: the rule must fire. The number below belongs to story 044
		// (S044-R9.3, written here with the prefix this guard asks for) and it
		// is the most-cited of the borrowed numbers in this package.
		{
			name:     "bare citation of a requirement 046 does not have",
			citation: bareID("9.3"),
			fault:    true,
			why:      "a number 046 never issued, written as though it were 046's",
		},
		// Hostile: the same, for a number that exists in many stories at once.
		{
			name:     "bare citation of a number many stories share",
			citation: bareID("2.5"),
			fault:    true,
			why:      "sixteen story files in this repository define one; bare, it names none of them",
		},
		// Hostile, the converse: the rule must NOT fire. Over-firing here would
		// condemn every correct citation in the package and cost a rewrite of
		// twenty-five files that buys nothing.
		{
			name:     "bare citation of a requirement 046 does have",
			citation: "R7.2",
			fault:    false,
			why:      "not refused: 046 has R7.2, so this class is counted as debt rather than failed",
		},
		// Hostile, the third element: the prefix is a spelling the fix invents,
		// so the rule that motivated it cannot name what would collide with it.
		// An S046- prefix on a number 046 does not have is the collision — it
		// silences a bare-citation sweep while asserting something false.
		{
			name:     "this story's prefix on a requirement this story does not have",
			citation: thisStoryPrefix + bareID("9.3"),
			fault:    true,
			why:      "prefixing every offender with the story being worked on is the cheapest wrong fix",
		},
		// Hostile, the second numbering: a bare decision token naming a number
		// 046 does not issue. 046's design stops at its tenth decision and
		// story 044's does not, so this is the D-class's exact analogue of a
		// bare citation of S044-R9.3 and must fail for the same reason.
		{
			name:     "bare citation of a decision 046 does not have",
			citation: bareDecision("11"),
			fault:    true,
			why:      "046 issues ten decisions; bare, this one names nothing a reader can find",
		},
		// Hostile, its converse form: this story's prefix on the same number.
		{
			name:     "this story's prefix on a decision this story does not have",
			citation: thisStoryPrefix + bareDecision("11"),
			fault:    true,
			why:      "a confident, checkable falsehood — it looks resolved and names nothing",
		},
		// Hostile, the over-firing direction for the second numbering. A fault
		// here would refuse 101 citations already standing in this package and
		// fail the whole suite the moment the pattern was widened.
		{
			name:     "bare citation of a decision 046 does have",
			citation: bareDecision("4"),
			fault:    false,
			why:      "not refused: 046 issues it, so this class is counted as debt rather than failed",
		},
		// Correct disambiguation in the second numbering.
		{
			name:     "the owning story's prefix on a decision",
			citation: "S044-" + bareDecision("7"),
			fault:    false,
			why:      "render/fullscreen_sections_test.go means story 044's, and this form says so",
		},
		// Correct disambiguation, and the reason the guard exists.
		{
			name:     "the owning story's prefix",
			citation: "S044-" + bareID("9.3"),
			fault:    false,
			why:      "the reader can find the sentence: story 044 issued it",
		},
		{
			name:     "another story's prefix",
			citation: "S033-" + bareID("9.5"),
			fault:    false,
			why:      "this guard knows one story's set and does not audit another's",
		},
		{
			name:     "this story's prefix on one of its own requirements",
			citation: thisStoryPrefix + "R5.1",
			fault:    false,
			why:      "the form already used twenty-three times in this repository",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fault := citationFault(testCase.citation)

			if testCase.fault && fault == "" {
				t.Errorf("%s was accepted — %s", testCase.citation, testCase.why)
			}
			if !testCase.fault && fault != "" {
				t.Errorf("%s was rejected as %q — %s", testCase.citation, fault, testCase.why)
			}
		})
	}
}

// TestCitationScannerReadsWholeCitations pins the reader the classifier is fed
// from, hostile cases first.
//
// Every case here is a way for two different citations to arrive at the same
// answer. A scanner that collapses them would either invent violations against
// requirements nobody cited, or read a correctly prefixed reference as a bare
// one and condemn the very form the remedy asks for.
func TestCitationScannerReadsWholeCitations(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		// Hostile: a longer number must not collapse onto the shorter citation
		// it starts with. A two-digit sub-number is its own requirement, and a
		// scanner that read only the first digit would report a violation
		// against a number nobody wrote. The fixture is assembled rather than
		// written out for the reason bareID gives.
		//
		// The pair is the whole hostile case here: these two numbers differ by
		// one character and must stay apart, in both directions — the long one
		// must not be read as the short one, and the short one, cited on its
		// own elsewhere in this table, must still be found.
		{
			name: "a longer sub-number is not the shorter one it starts with",
			text: "see " + bareID("2.55") + " for the detail",
			want: []string{bareID("2.55")},
		},
		// Hostile, the converse: a prefixed citation must not split into a bare
		// one. This is the failure that would turn every correct citation in the
		// tree into a violation the moment the remedy was applied.
		{
			name: "a prefixed citation is one citation, not a bare tail",
			text: "as " + thisStoryPrefix + "R5.1 requires",
			want: []string{thisStoryPrefix + "R5.1"},
		},
		// Hostile: an identifier that merely ends in R plus digits is not a
		// citation. A scanner without a left boundary finds one inside it.
		{
			name: "a citation inside a word is not a citation",
			text: "the CHAR2.5 column and the VAR7.2 binding",
			want: nil,
		},
		{
			name: "a citation at the very start of the text",
			text: bareID("9.3") + " is answered here",
			want: []string{bareID("9.3")},
		},
		{
			name: "adjacent citations are read separately",
			text: "R1.1," + bareID("9.3") + " and " + "S044-" + bareID("5.4"),
			want: []string{"R1.1", bareID("9.3"), "S044-" + bareID("5.4")},
		},
		// Hostile, the second numbering: a two-digit decision number must not
		// collapse onto the one-digit citation it starts with. This package
		// cites both the first decision and the tenth, so a scanner reading one
		// digit would report all five citations of the tenth against the first.
		{
			name: "a two-digit decision number is not the one-digit one it starts with",
			text: "precedence is decided once, see " + bareDecision("10"),
			want: []string{bareDecision("10")},
		},
		// Hostile: a decision token inside a word is not a citation, and the D
		// form is likelier than the R form to appear inside an identifier —
		// there is no dot to make it look unusual.
		{
			name: "a decision citation inside a word is not a citation",
			text: "the FIELD3 column and the ID7 binding",
			want: nil,
		},
		// Hostile: the two numberings must be read separately rather than one
		// swallowing the other, because each is counted against its own ceiling.
		{
			name: "a requirement and a decision side by side are read separately",
			text: "as S046-R5.1 and " + bareDecision("4") + " require",
			want: []string{"S046-R5.1", bareDecision("4")},
		},
		{
			name: "prose without a citation",
			text: "the report is rendered in four modes",
			want: nil,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var got []string
			for _, ref := range citationsIn(testCase.text) {
				got = append(got, ref.text)
			}

			if strings.Join(got, " ") != strings.Join(testCase.want, " ") {
				t.Errorf("read %v from %q, want %v", got, testCase.text, testCase.want)
			}
		})
	}
}

// TestCitationScannerCountsLinesWithinAText pins the offset, because a doc
// comment is one node and the citation inside it can be forty lines down.
func TestCitationScannerCountsLinesWithinAText(t *testing.T) {
	text := "// first line\n// second line\n// third line cites " + bareID("9.3")

	refs := citationsIn(text)
	if len(refs) != 1 {
		t.Fatalf("read %d citations from the block, want 1", len(refs))
	}
	if refs[0].line != 2 {
		t.Errorf("the citation was reported %d lines into the block, want 2 — a failure that names the block's first line sends its reader to a paragraph", refs[0].line)
	}
}

// TestCitationViolationNamesFileAndCitation pins the message. A guard that
// fails without naming the file and the offending number costs its reader a
// search through twenty-five files, which is how a mechanical rule turns back
// into a convention nobody follows.
func TestCitationViolationNamesFileAndCitation(t *testing.T) {
	citation := bareID("9.3")

	fault := citationFault(citation)
	if fault == "" {
		t.Fatalf("%s is not classified as a fault — its message cannot be checked", citation)
	}

	message := citationViolation("mode.go", 42, citation, fault)

	for _, want := range []string{"mode.go", "42", citation, "Remedy", "S0NN-"} {
		if !strings.Contains(message, want) {
			t.Errorf("the failure message does not name %q:\n%s", want, message)
		}
	}
}

// # THE SUBJECT WAS NARROWER THAN THE STORY (sub-task 16.6 — S046-R8.3)
//
// Everything above reasons about "this package", and until this sub-task the
// sweep's subject was exactly that: internal/common/report and render/. Story
// 046 did not confine itself to those two directories. It CREATED eight
// non-test Go files outside them — five under cmd/bentoo and three under
// internal/overlay — and every citation in all eight was outside the guard, so
// nothing fired on any of them and the ceilings did not count one.
//
// internal/overlay/finding.go is what that cost. It is a file this story wrote
// from nothing, it carries 19 bare requirement citations, and EIGHT of them
// name numbers story 046 does not issue at all: R-2.5 twice, R-3.8 once, R-5.4
// once and R-5.8 four times. Those eight are the hard-failure class — the one
// this file's header opens by defining — standing in the tree, unreported, for
// as long as the file has existed. The remaining eleven resolved by number into
// story 046 sentences none of them meant. (Written with hyphens, for the reason
// the header gives: this file is swept by its own rule.)
//
// The widening is by FILE LIST rather than by directory, and the list is what
// git answers, not what a reader remembers: every path in
// storyCreatedFilesOutsideThePackage is in
// `git diff --diff-filter=A --name-only c8e347e HEAD`, filtered to .go, less
// _test.go, less this package. c8e347e is the commit this branch left main at.
//
// WHAT IS DELIBERATELY NOT IN IT: the fifty non-test files this story MODIFIED
// rather than created. The line is drawn at authorship because a citation in a
// file this story wrote is a citation this story is answerable for, while a
// bare number in a file it merely touched was written by whoever wrote the file
// — attributing those is the same reading job the ceilings already hand to
// story 047, at fifty files instead of eight. Sub-task 16.6 states the boundary
// rather than leaving it implied; see the note beside the list.
var storyCreatedFilesOutsideThePackage = []string{
	"../../../cmd/bentoo/overlay_manifest_report.go",
	"../../../cmd/bentoo/overlay_validate_report.go",
	"../../../cmd/bentoo/report_export.go",
	"../../../cmd/bentoo/root.go",
	"../../../cmd/bentoo/snapshot_report.go",
	"../../../internal/overlay/finding.go",
	// internal/overlay/manifest_cancel_other.go and manifest_cancel_unix.go,
	// the other two files story 046 created there, left this list when story
	// 054 deleted them (its sub-task 1.2): their one function moved unchanged
	// into internal/common/procgroup as KillGroupNow, outside this sweep. The
	// list still answers the git command above, since a deleted file is not in
	// HEAD. The Unix file's bare citations left the ceiling with it; see the
	// story 054 re-pin on unattributedCitationDebt.
}

// eachCitationInPackage reads every citation this package writes and hands each
// one to visit: the file it stands in, the line it stands on, and the citation
// itself. It returns the number of files it parsed, and — separately — how many
// of those came from storyCreatedFilesOutsideThePackage.
//
// The second count is separate because it is the only one that can go to zero
// on its own. A widened sweep whose extra half silently matches nothing reads
// exactly like a narrow sweep that was never widened: the totals barely move,
// every test passes, and the eight files are back outside the guard. Publishing
// the number is what lets each caller refuse that state.
//
// This is the SUBJECT, written once and shared by both sweeps below. Every .go
// file under internal/common/report, render/ and the _test.go files included —
// a citation in a test's failure message is read by the same person with the
// same question, and five of the 79 violations this guard found on arrival
// lived inside strings, so literals are read alongside comments.
//
// skip is the ONLY difference between the guard and the ceiling, and making it
// an argument is what keeps the two from drifting apart. Before this, each
// sweep declared the subject for itself, so narrowing one of them would have
// gone unnoticed by the other — and a ceiling reading fewer files than the
// guard reports a debt that fell without anything being fixed. A nil skip
// reads everything.
//
// The returned count is what each caller's vacuity guard is built on: a sweep
// that read nothing finds no violation and no debt, and both of those look
// exactly like success.
func eachCitationInPackage(t *testing.T, skip func(path string) bool, visit func(path string, line int, citation string)) (filesScanned, storyFilesScanned int) {
	t.Helper()

	fileSet := token.NewFileSet()

	read := func(path string) error {
		file, parseErr := parser.ParseFile(fileSet, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		filesScanned++

		inspect := func(pos token.Pos, text string) {
			base := fileSet.Position(pos).Line
			for _, ref := range citationsIn(text) {
				visit(path, base+ref.line, ref.text)
			}
		}

		for _, group := range file.Comments {
			for _, comment := range group.List {
				inspect(comment.Pos(), comment.Text)
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				inspect(literal.Pos(), literal.Value)
			}
			return true
		})

		return nil
	}

	walkErr := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if skip != nil && skip(path) {
			return nil
		}
		return read(path)
	})
	if walkErr != nil {
		t.Fatalf("sweeping the package: %v", walkErr)
	}

	// The second half of the subject, read by NAME rather than by walking a
	// directory: these files sit beside code no story 046 requirement is about,
	// and a directory rule would either drag that in or need an exception list
	// longer than this one.
	for _, path := range storyCreatedFilesOutsideThePackage {
		if skip != nil && skip(path) {
			continue
		}
		if err := read(path); err != nil {
			t.Fatalf("sweeping the files this story created outside this package: %v", err)
		}
		storyFilesScanned++
	}

	return filesScanned, storyFilesScanned
}

// TestCitationsResolve is the guard itself: every citation written in a comment
// or a string anywhere under internal/common/report, render/ included, must
// name a sentence a reader can find.
//
// It exempts nothing, this file included — an exemption would be a hole exactly
// where someone tempted to hide a citation would put one.
func TestCitationsResolve(t *testing.T) {
	citationsRead := 0
	violations := 0

	filesScanned, storyFilesScanned := eachCitationInPackage(t, nil, func(path string, line int, citation string) {
		citationsRead++
		if fault := citationFault(citation); fault != "" {
			violations++
			t.Errorf("%s", citationViolation(path, line, citation, fault))
		}
	})

	// A sweep that read nothing passes, and would keep passing after the
	// citations were moved out from under it.
	if filesScanned == 0 {
		t.Fatal("scanned no Go file — the sweep passed without looking at anything")
	}
	if citationsRead == 0 {
		t.Fatal("read no citation at all — this package cites requirements in almost every file, so the reader is broken, not the package")
	}
	// The widened half, guarded on its own. Folded into the count above it
	// would be eight files inside forty-odd, and losing all eight would still
	// leave a number that looks like a sweep.
	if storyFilesScanned == 0 {
		t.Fatalf("read none of the %d files this story created outside this package — the widened sweep is back to the narrow one, and finding.go's eight unresolvable citations are outside the guard again",
			len(storyCreatedFilesOutsideThePackage))
	}

	t.Logf("swept %d files, %d of them created by this story outside this package", filesScanned, storyFilesScanned)

	if violations > 0 {
		t.Logf("%d of the %d citations read across %d files cannot be resolved", violations, citationsRead, filesScanned)
	}
}

// # THE SECOND CLASS — WHAT RESOLVES WITHOUT BEING ATTRIBUTED (sub-task 11.2)
//
// Everything below serves the ceiling in citation_debt_test.go, and it lives in
// THIS file for a reason that is not tidiness. The debt sweep excludes
// citation_test.go by name: story046Requirements above DEFINES story 046's set,
// and counting the definition as a citation OF the set would pin a ceiling
// against the definition. Putting the counter inside the one excluded file
// keeps both sweeps reading an identical subject with a single exclusion rule
// to maintain, and keeps the counter's own explanation — which cites
// requirements in almost every sentence — out of the number it reports. A
// helper in a third file would sit inside the subject it measures.

// requirementTableFile is the one file the debt sweep does not read: this one,
// where the requirement table lives. citation_debt_test.go's independent walk
// names the same basename, and TestCitationDebtSweepAgreesWithTheGuardsSubject
// fails if the two subjects ever part company.
const requirementTableFile = "citation_test.go"

// isRequirementTable is that exclusion as eachCitationInPackage takes it.
func isRequirementTable(path string) bool { return filepath.Base(path) == requirementTableFile }

// citationIsUnattributed answers whether a citation was matched on its NUMBER
// rather than on its MEANING: written bare, and let through by citationFault
// only because story 046 issues that number too — as does story 044, which
// wrote most of this package.
//
// It answers for both numberings, and deliberately does not distinguish them:
// what makes a citation unattributed is identical in each, so one predicate
// serves both and classOf decides afterwards which ceiling the hit counts
// against. Two predicates differing only in a letter is the duplication that
// lets one class quietly stop being checked.
//
// It is written THROUGH citationFault rather than beside it so that the two
// classes cannot drift into overlapping. Whatever citationFault rejects is a
// hard failure and is never counted as debt; what this counts is exactly the
// residue citationFault lets through bare. A prefixed citation is attributed by
// definition, whichever story the prefix names — that is what a prefix is FOR,
// and re-deciding it here would put the counter and the guard at odds.
func citationIsUnattributed(citation string) bool {
	prefix, _ := splitCitation(citation)
	return prefix == "" && citationFault(citation) == ""
}

// unattributedCitationDebt is the ceiling for the REQUIREMENT class, and it is
// a MEASUREMENT rather than a target: 356 bare citations over 36 of the 37
// files the sweep read, taken on 2026-08-29 against the tree exactly as it is committed. Nothing was picked
// to make the number fit — it is what the sweep printed on the day it landed,
// and classify_test.go is the single swept file with none.
//
// It may not rise. That trigger is the thing story 044 lacked when it recorded
// three typed column widths as debt "to be repaid when one of them is next
// touched": none were touched, and the count had reached 18 by the time anyone
// looked (cmd/bentoo/width_debt_test.go carries that history, and this ceiling
// is built in its shape). A number that can only fall is the hand-off
// Constraint 8 asks for — story 047 can CHECK it instead of believing a
// description of it.
//
// It will not fall by itself. Repaying it means reading 356 comments and
// deciding which story's sentence each one meant; done mechanically it would
// write confident falsehoods, and a wrong prefix is worse than a bare number
// because it looks resolved. Sub-task 11.2 therefore prefixes nothing and hands
// the reading to story 047.
//
// # RE-PINNED BY SUB-TASK 16.6: 356 -> 410, over 42 files
//
// The number rose and NOTHING got worse. It rose because the SUBJECT grew:
// sub-task 16.6 added the eight non-test files story 046 created outside this
// package (storyCreatedFilesOutsideThePackage), and every bare citation in all
// eight arrived at once, having been counted by nothing since the day they were
// written. Measured 2026-09-03 on the widened, corrected tree.
//
// The arithmetic is stated so the next reader can check it rather than believe
// it. The widening brought 81 requirement citations in — snapshot_report.go 22,
// overlay_manifest_report.go 19, overlay_validate_report.go 15, finding.go 11,
// report_export.go 7, root.go 4, manifest_cancel_unix.go 3 — taking the run to
// 437. This sub-task then REPAID 27 of them: all 11 of finding.go's, which left
// the list entirely, and 16 inside this package where a citation was found to
// name the wrong story (the ShowAll rows, the same-content rule, the two
// meanings of a sixth-group third requirement, the interrupted-run rule).
// 356 + 81 - 27 = 410.
//
// So the pin moved for the one reason a ceiling may move: the measurement it
// publishes is now taken over a larger subject, and the sub-task that widened
// it repaid every citation it was in a position to attribute. It still may not
// rise. A rise from here is a new bare citation, not a new file.
//
// # RE-PINNED BY STORY 054, SUB-TASK 1.2: 410 -> 407, over 41 files
//
// The number fell and nothing in this package was repaid. It fell because the
// SUBJECT shrank: story 054 deleted internal/overlay/manifest_cancel_unix.go,
// moving its one function into internal/common/procgroup, and the 3 bare
// requirement citations the file carried left the sweep with it. They were not
// dropped: in their new home each is written with its story prefix. Measured
// 2026-09-26 on the tree without the file: 410 - 3 = 407. The pin follows the
// measurement down because a ceiling left 3 above it would let 3 new bare
// citations in without a failure.
const unattributedCitationDebt = 407

// unattributedDecisionDebt is the same ceiling for the other numbering: 101
// bare design-decision citations over 29 of the 40 files the debt sweep reads,
// measured on 2026-09-03 against the tree exactly as it stands. Nothing was
// chosen to make the number fit — it is what the sweep printed on the day
// citationPattern first matched the D form.
//
// It is a second constant rather than a larger first one. Summing the two
// would publish 457 and hide both movements inside it: the classes are repaid
// by different work — story 047 reads comments against story.md for one and
// against design.md for the other — so a total that cannot say which half
// moved is a number nobody can act on. Both may fall; neither may rise.
//
// The ten numbers it counts are all inside story 046's own set, which is why
// none of the 101 is a hard failure. That is also the measurement that makes
// the class worth counting at all: a bare token whose number BOTH stories
// issue is the one a reader cannot resolve and a sweep cannot correct.
//
// # RE-PINNED BY SUB-TASK 16.6: 101 -> 109, over 33 files
//
// Same widening, same arithmetic, and it is reported separately for exactly the
// reason the paragraph above gives. The eight newly-swept files carried 9 bare
// decision tokens — overlay_validate_report.go 3, snapshot_report.go 3,
// overlay_manifest_report.go 1, report_export.go 1, finding.go 1 — taking the
// run to 110; finding.go's was attributed (it meant this story's own seventh
// decision, the one that stops internal/ printing) and left the list.
// 101 + 9 - 1 = 109. The in-package total did not move at all: 101 before this
// sub-task and 101 after, which is what says the whole rise is arrival rather
// than growth.
const unattributedDecisionDebt = 109

// # EVIDENCE THAT THE CEILING BITES, AND THAT IT LEFT THE GUARD ALONE (S046-R8.3)
//
// The pin above is green on the day it lands, and a green ceiling proves
// nothing about what it would catch — the same hole this file's header
// identifies for TestCitationsResolve and closes there. The artifacts holding
// the Red (.draft/red-evidence.yaml) are not committed, so a pointer to them is
// a pointer to nothing for anyone who cloned this. It is written here instead.
//
// Measured on 2026-08-29. Two mutations, applied to mode.go one at a time from
// a copy taken first, each run and reverted immediately, the restore verified
// by `git status --porcelain` reporting no change to mode.go and by a rerun
// going green. Both transcripts are re-wrapped to this comment's width and the
// long breakdowns are elided where marked; no other character is edited, and
// the single exception to that is named at the foot.
//
// Mutation 1 — ONE bare citation of a number story 046 DOES issue, added as a
// comment at the end of mode.go. This is the class the ceiling counts:
//
//	=== RUN   TestCitationDebtDoesNotGrow
//	    citation_debt_test.go:97: unattributed citations remaining in this package: 357, over 36 files
//	        [... 7 rows unchanged ...]
//	        mode.go                                                    12
//	        [... 28 rows unchanged ...]
//	    citation_debt_test.go:101: unattributed citations rose to 357, above the pinned 356.
//	        A bare citation in this package resolves by NUMBER, and story 044's numbering
//	        covers story 046's entirely — so a new one names a sentence the reader cannot
//	        find. Prefix it with the story that owns it, in the S0NN- form.
//	        Per file:
//	        [the same 36 rows, mode.go again at 12]
//	--- FAIL: TestCitationDebtDoesNotGrow (0.01s)
//
// mode.go stood at 11 before the mutation and at 12 after, and that is what
// makes the breakdown the answerable half: the total says the debt grew, the
// list says which file to open. The elided rows are identical in both listings
// and identical to the ones a green run prints.
//
// Mutation 2 — ONE bare citation of a number story 046 does NOT have, in the
// same position. It is not a repetition of the first. It is the demonstration
// that the two classes stayed disjoint once this file grew a second one: the
// ceiling must NOT move, and the hard guard must refuse the citation exactly as
// it did before sub-task 11.2 touched anything. Both tests were run together,
// in one command, against one mutated tree:
//
//	=== RUN   TestCitationDebtDoesNotGrow
//	    citation_debt_test.go:97: unattributed citations remaining in this package: 356, over 36 files
//	        [the 36 rows, every one as a green run prints it]
//	--- PASS: TestCitationDebtDoesNotGrow (0.01s)
//	=== RUN   TestCitationsResolve
//	    citation_test.go:546: mode.go:272 cites R-9.3 — story 046 has no R-9.3, and the
//	        citation is bare, so it names nothing a reader can resolve.
//	        Remedy: prefix the citation with the story that owns the requirement, in the
//	        S0NN- form used throughout this repository. The story files under .epic/ are
//	        not committed, so this comment is the only record of what the code was
//	        answering.
//	    citation_test.go:560: 1 of the 499 citations read across 38 files cannot be resolved
//	--- FAIL: TestCitationsResolve (0.01s)
//
// Read together, the two runs say what neither says alone: an unresolvable
// citation still fails the package and is NOT counted as debt, and an
// unattributed one is counted and does NOT fail the package. Neither mutation
// moved the other verdict. Mutation 2's message is word for word the one this
// file's header recorded for its own first mutation, before the ceiling
// existed — a different line of mode.go, the same sentence — which is the
// evidence that sub-task 11.2 left the hard guard's behaviour where it was.
//
// The one exception: R-9.3 above is written with a hyphen the run did not
// print. TestCitationsResolve sweeps this file too, so quoting that number bare
// would make the transcript violate the rule it is transcribing — the device is
// this file's own, from the header. Mutation 1's transcript needed no device at
// all, and that is a property of the ceiling worth noticing: its failure names
// files and counts, never a requirement number, so it can be quoted anywhere.

// # EVIDENCE FOR THE SECOND CEILING AND THE REPAIRED CROSS-CHECK (sub-task 16.5 — S046-R8.3)
//
// Everything sub-task 16.5 added is green on the day it lands, which is the
// state this file has twice recorded as proving nothing on its own. Five
// mutations, measured on 2026-09-03 with Go 1.26. Each was applied ALONE, run
// once, and reverted immediately; every revert was verified by md5sum against
// the file taken before the mutation rather than by eye — mode.go at
// 79603c3d49768eba70af7ed7692d6e3f before M1 and after M4, and citation_test.go
// and citation_debt_test.go each byte-identical after M3 and M5.
//
// A number story 046 does NOT issue is written below with a hyphen — D-11 for
// what the runs printed as one token — because this file is swept by its own
// rule and quoting it whole would make the evidence violate the thing it
// records. That device is this file's own, from its header. The hyphen is the
// only edit; the lines are otherwise as printed, re-wrapped to this comment's
// width, with the 36- and 29-row breakdowns elided where marked.
//
// One further elision, and it is not a stylistic one. Where a run below names a
// line of THIS file, the line number is written NNN. Pasting a transcript into
// the file it reports on moves every line beneath the paste, so a number copied
// here would be wrong the moment it was written and wrong again after the next
// edit — the failing sites are named by their test instead, which is what a
// reader needs anyway. Line numbers in OTHER files (mode.go, and
// citation_debt_test.go, which this sub-task edited before these runs) are left
// as printed.
//
// M1 — one bare requirement citation appended as a comment to mode.go. This is
// 11.2's own mutation 1, re-run against two ceilings instead of one:
//
//	--- FAIL: TestCitationDebtDoesNotGrow (0.01s)
//	    citation_debt_test.go:170: unattributed citations remaining in this
//	        package: 357, over 36 files
//	        [the 36 rows, mode.go at 12]
//	    citation_debt_test.go:174: unattributed citations rose to 357, above the
//	        pinned 356.
//	        [the four-line remedy, then the same 36 rows]
//	    citation_debt_test.go:170: unattributed design-decision citations
//	        remaining in this package: 101, over 29 files
//	        [the 29 rows, unchanged]
//
// The last line is what one summed ceiling could not have printed. The other
// class did not move, and the run SAYS the other class did not move rather than
// leaving its reader to subtract 356 from a total.
//
// M2 — one bare decision citation, the same position in the same file. The
// classes are pinned apart, so this run is the mirror image of M1 and not a
// repetition of it: the requirement line holds and the decision line rises.
//
//	--- FAIL: TestCitationDebtDoesNotGrow (0.01s)
//	    citation_debt_test.go:170: unattributed citations remaining in this
//	        package: 356, over 36 files
//	        [the 36 rows, mode.go at 11 — unchanged]
//	    citation_debt_test.go:170: unattributed design-decision citations
//	        remaining in this package: 102, over 29 files
//	        [the 29 rows, mode.go at 8]
//	    citation_debt_test.go:174: unattributed design-decision citations rose to
//	        102, above the pinned 101.
//	        A bare decision token resolves by NUMBER exactly as a requirement
//	        citation does: story 044's design issues the same ten numbers with
//	        different sentences behind them, and this package cites both —
//	        render/fullscreen_sections_test.go already means S044-D7 while
//	        writing the token bare. Prefix it with the story that owns it, in the
//	        S0NN- form.
//	        Per file: [the same 29 rows]
//
// M3 — the cross-check, and the only one of the five that had to be run twice,
// because what it establishes is a CONTRAST between two versions of one test
// rather than a failure of one. Both runs used the same mutation, applied to
// the independent walk in citation_debt_test.go: count each file at most once
// per class. It is the exact case that file's own doc comment described and
// could not detect — every per-file value collapses to 1 and the key set is
// untouched.
//
// First against the code as COMMITTED at 0fd2f2e, unpacked to a pristine tree
// with `git archive HEAD | tar -x`, where the comparison was
// `len(got) != len(want)` over two maps keyed by path:
//
//	=== RUN   TestCitationDebtSweepAgreesWithTheGuardsSubject
//	--- PASS: TestCitationDebtSweepAgreesWithTheGuardsSubject (0.02s)
//	ok  	github.com/obentoo/bentoolkit/internal/common/report	0.030s
//
// That PASS is the defect. The independent sweep was reporting 36 unattributed
// citations where the ceiling reported 356 — a published debt short by three
// hundred and twenty — and the check written to stop exactly that agreed with
// it, because 36 files and 36 files are the same number of files. The run in
// the same command printed "unattributed citations remaining in this package:
// 356, over 36 files" and passed, so the two halves of one suite disagreed by
// an order of magnitude without a word.
//
// Then the same mutation against the repaired comparison, in the working tree:
//
//	--- FAIL: TestCitationDebtSweepAgreesWithTheGuardsSubject (0.02s)
//	    citation_debt_test.go:310: the ceiling counts 356 unattributed
//	        requirement citations and this file's independent sweep 36, across
//	        file lists that agree — the debt one of them publishes is not the
//	        debt that is there, and comparing the number of files would not have
//	        noticed
//	    citation_debt_test.go:310: the ceiling counts 101 unattributed decision
//	        citations and this file's independent sweep 29, across file lists
//	        that agree — [the same sentence]
//
// Two lines, one per class, and the second is why the comparison is made per
// class rather than over the pair.
//
// M4 — a bare decision token naming a number 046 does not issue, appended to
// mode.go. This is the branch that has no live instance — all ten numbers in
// the tree are inside 046's set — so without a mutation it is an assertion
// nobody has seen fire:
//
//	--- FAIL: TestCitationsResolve (0.01s)
//	    citation_test.go:NNN: mode.go:378 cites D-11 — story 046 has no D-11,
//	        and the citation is bare, so it names nothing a reader can resolve.
//	        Remedy: prefix the citation with the story that owns the design
//	        decision, in the S0NN- form used throughout this repository. The
//	        story files under .epic/ are not committed, so this comment is the
//	        only record of what the code was answering.
//	    citation_test.go:NNN: 1 of the 660 citations read across 41 files cannot
//	        be resolved
//
//	    (both lines from TestCitationsResolve — the first its per-violation
//	    t.Errorf, the second its closing t.Logf)
//
// It carries three things beyond the failure. The remedy says "design decision"
// where the requirement class says "requirement", so a reader is sent to
// design.md rather than through story.md for a number that was never in it. The
// citation total is 660 rather than the 470 recorded before this sub-task,
// which is the widened pattern reading a numbering that was previously
// invisible. And TestCitationDebtDoesNotGrow ran in the SAME command and
// reported no failure — the D-debt was still 101 — so the two classes stayed
// disjoint under the D form exactly as 11.2's mutation 2 established them for
// the R form: an unresolvable citation fails the package and is not counted,
// an unattributed one is counted and does not fail the package.
//
// M5 — an eleventh number added to story046Decisions, which story 046's design
// does not state. The set has one copy and one check; this is that check:
//
//	--- FAIL: TestCitationDecisionTableAgreesWithTheDesignFileWhereItIsPresent (0.00s)
//	    citation_test.go:NNN: the set in this file carries D-11 and story 046's
//	        design does not state it — a decision was removed or renumbered, and
//	        the guard would go on accepting bare citations of a decision that no
//	        longer exists
//
// What none of the five shows, because no test here can: that any one of the
// 457 counted citations means what a reader would guess. A ceiling stops a
// debt growing. It does not repay a unit of it.

// # EVIDENCE FOR THE WIDENED SUBJECT (sub-task 16.6 — S046-R8.3)
//
// This sub-task needed no mutation for its main claim, and that is worth
// stating before the one it did need. Widening the subject produced a NATURAL
// Red: eight unresolvable citations, every one of them in
// internal/overlay/finding.go, a file story 046 created from nothing. They had
// been standing in the tree since the day it was written. Measured 2026-09-03,
// Go 1.26, after the widening and before a single citation was prefixed. The
// four-line Remedy paragraph is identical on all eight and elided after the
// first; the numbers are hyphenated for the reason the header gives.
//
//	=== RUN   TestCitationsResolve
//	    citation_test.go:NNN: ../../../internal/overlay/finding.go:144 cites
//	        R-5.8 — story 046 has no R-5.8, and the citation is bare, so it
//	        names nothing a reader can resolve.
//	        Remedy: prefix the citation with the story that owns the
//	        requirement, in the S0NN- form used throughout this repository. The
//	        story files under .epic/ are not committed, so this comment is the
//	        only record of what the code was answering.
//	    [finding.go:147 R-2.5, :181 R-5.8, :185 R-5.4, :236 R-3.8, :294 R-2.5,
//	     :306 R-5.8, :411 R-5.8 — the same sentence, seven more times]
//	    citation_test.go:NNN: swept 49 files, 8 of them created by this story
//	        outside this package
//	    citation_test.go:NNN: 8 of the 774 citations read across 49 files cannot
//	        be resolved
//	--- FAIL: TestCitationsResolve (0.01s)
//
// All eight are attributed now — to stories 025, 032 and 034, checked against
// those stories' own acceptance criteria rather than guessed — and finding.go
// has left both debt lists entirely: 32 citations, every one prefixed.
//
// THE MUTATION, and it is aimed at the one thing the natural Red cannot cover.
// A widened sweep that silently stops matching reads exactly like a narrow one:
// the totals fall, every test passes, and the eight files are outside the guard
// again. storyCreatedFilesOutsideThePackage was emptied — one edit, the exact
// shape a bad merge or a path change would produce — and all three sweeps
// refused it rather than passing over nothing:
//
//	    citation_test.go:NNN: read none of the 0 files this story created
//	        outside this package — the widened sweep is back to the narrow one,
//	        and finding.go's eight unresolvable citations are outside the guard
//	        again
//	--- FAIL: TestCitationsResolve (0.01s)
//	    citation_debt_test.go:127: read none of the 0 files this story created
//	        outside this package — the ceilings below were measured over a
//	        subject that includes them, so a sweep without them publishes a debt
//	        that fell without a fix
//	--- FAIL: TestCitationDebtDoesNotGrow (0.01s)
//	    citation_debt_test.go:310: [the same sentence, from the independent walk]
//	--- FAIL: TestCitationDebtSweepAgreesWithTheGuardsSubject (0.01s)
//
// Three failures from one edit is the point rather than noise: the guard, the
// ceiling and the cross-check each read this list, so each has to refuse an
// empty one on its own. Without these the ceilings would simply have fallen to
// 340 and 101 and reported a repayment nobody made. The file was restored from
// a copy taken first and the restore verified by md5sum —
// 98eddfb27c3d1743a3b41729b53e115c before the mutation and after it — not by
// eye.
//
// WHAT THIS STILL DOES NOT COVER, stated where the next reader will meet it:
// the sweep now reads the eight files story 046 CREATED outside this package
// and none of the fifty it MODIFIED. A bare citation in a file this story
// merely touched is still counted by nothing. That boundary is argued beside
// storyCreatedFilesOutsideThePackage; it is a scope line, not a claim that
// those files are clean.

// unattributedCitations counts, per file, the citations this package resolves
// on their number alone.
//
// It reads eachCitationInPackage's subject — the guard's own — less exactly one
// file. A narrower subject would let the debt hide in the difference, which is
// why the exclusion is an argument here rather than a second walk, and why
// TestCitationDebtSweepAgreesWithTheGuardsSubject checks this count against an
// independent sweep written in citation_debt_test.go.
//
// The exclusion is the whole file rather than the table alone. Every citation
// in citation_test.go is either the definition of 046's set or a fixture for
// the rule, and neither is a claim this package's code makes about a
// requirement.
func unattributedCitations(t *testing.T) citationDebt {
	t.Helper()

	debt := citationDebt{}
	filesScanned, storyFilesScanned := eachCitationInPackage(t, isRequirementTable, func(path string, _ int, citation string) {
		if citationIsUnattributed(citation) {
			debt.add(classOf(citation), path)
		}
	})

	// A sweep that read nothing reports a debt of zero, and zero is exactly
	// what a finished repayment looks like — so the vacuity guard counts FILES
	// and not hits. Guarding on hits would turn story 047's success into a
	// failure. That the reader itself still works is pinned by
	// TestCitationScannerReadsWholeCitations and by TestCitationsResolve's own
	// "read no citation at all" check, which share citationsIn with this sweep.
	if filesScanned == 0 {
		t.Fatal("scanned no Go file — the debt sweep reported its count without looking at anything")
	}
	// Same guard for the widened half, and it counts FILES for the same reason:
	// zero hits in those eight is a finished repayment, zero FILES is a sweep
	// that stopped reading them. Sub-task 16.6 widened this subject, and a
	// widening that quietly reverts takes the published ceilings back down with
	// it — a debt that fell because the sweep looked away.
	if storyFilesScanned == 0 {
		t.Fatalf("read none of the %d files this story created outside this package — the ceilings below were measured over a subject that includes them, so a sweep without them publishes a debt that fell without a fix",
			len(storyCreatedFilesOutsideThePackage))
	}

	return debt
}

// citationDebt is the unattributed count per class, per file.
//
// The two classes are kept apart rather than summed, and that is the point of
// the type rather than a detail of it. One total would let a fall in either
// class pay for a rise in the other — thirty requirement citations repaid and
// thirty decision citations added reads as a debt that did not move — so
// TestCitationDebtDoesNotGrow pins each against its own ceiling.
//
// There is still exactly ONE sweep and ONE pattern behind both numbers.
// classOf sorts each hit as it arrives; nothing walks the tree twice. A second
// walk differing only in a letter is how one of two classes silently stops
// being read.
type citationDebt map[citationClass]map[string]int

// add records one unattributed citation against its class and its file.
func (debt citationDebt) add(class citationClass, path string) {
	if debt[class] == nil {
		debt[class] = map[string]int{}
	}
	debt[class][path]++
}

// total sums one class's per-file counts.
//
// It exists because len() of the same map is a FILE count and the two are not
// interchangeable, which is the defect sub-task 16.5 closed in
// TestCitationDebtSweepAgreesWithTheGuardsSubject: that check compared len()
// and called it the debt, so every count in a file could fall to one while it
// went on passing.
func (debt citationDebt) total(class citationClass) int {
	sum := 0
	for _, count := range debt[class] {
		sum += count
	}
	return sum
}

// # THE TABLE AND THE STORY IT COPIES (sub-task 13.1 — S046-R8.3)
//
// story046Requirements is a copy, and the header above already says what a copy
// does: it drifts. It has now drifted twice.
//
// The first time, story 046's third refinement added R-3.6 and the table kept
// its 33 entries. Sub-task 11.2 found it by READING, not by running anything —
// no citation of R-3.6 existed in this package yet, so the guard had nothing to
// be wrong about. The paragraph beside the table records that history and
// states the remedy as a sentence addressed to a maintainer: "anyone adding a
// requirement to story 046 adds it here too, and the count below is N". The
// fourth refinement then added R-3.7 (story.md, in the R3 group) and did not.
//
// Nothing failed, and that is the entire defect. A sentence is not a check.
// While no citation of the new number existed the omission cost nothing; the
// moment one appeared, the guard reported a requirement story 046 genuinely
// issues as a number 046 never issued — and its own printed remedy failed with
// it, because citationFault refuses this story's prefix on a number missing
// from the table. A citation of R-3.7 under this package was, at that point,
// unwritable in EVERY form the guard accepts: bare and prefixed both fail, so
// the failure message asks for something it will then reject.
//
// Four checks replace the sentence. They are separate because a fix for one is
// not a fix for the others, and the last two are green the day they are written
// — deliberately, and the evidence that each can fail is recorded below.
//
//   - TestCitationTableMatchesTheRequirementsStory046Issues — the table holds
//     an entry for every requirement 046 issues, and none it does not. Both
//     directions, because each is a different lie: a missing entry reports a
//     real requirement as fictional, and a spurious one accepts a citation that
//     resolves to nothing.
//   - TestCitationTableResolvesTheRefinementRequirement — R-3.7 specifically,
//     in the bare and the prefixed form, which is what a reader will actually
//     type and what the failure message tells them to type.
//   - TestCitationTableCountMatchesTheSentenceBesideIt — the sentence and the
//     table cannot part company. A maintainer reading "the count below is N" is
//     entitled to believe it, and half-fixing the table would leave it false.
//   - TestCitationTableAgreesWithTheStoryFileWhereItIsPresent — the
//     transcription below against story.md itself, where .epic/ is on disk.
//
// The chain is deliberate: story.md → the transcription → the guard's table.
// The middle link exists because .epic/ is not committed. A check that read
// story.md directly into the guard's table would be the only link there is, and
// it would skip for everyone who cloned this repository — the header's reason
// for copying the set in the first place. The transcription is committed, so
// the link that matters most (transcription → table) runs everywhere; the link
// that cannot (story.md → transcription) runs wherever the story exists, which
// is wherever someone is in a position to add a requirement to it.

// story046IssuedRequirements is story 046's acceptance-criteria set, read off
// story.md and written here: 35 requirements in eight groups, R1.1 through
// R8.4. Every line of that file opening with "- RN.M:" is one of them and
// nothing else in the file matches that shape.
//
// The ids are assembled from their numbers through bareID rather than written
// whole, for the reason bareID gives: this file is swept by its own rule, and a
// literal citation of a number the table does not yet list would make the file
// fail against itself — which is precisely the state this sub-task exists to
// end, and it would be indistinguishable from it.
func story046IssuedRequirements() []string {
	numbers := []string{
		// R1. Every long-running command ends in a report.
		"1.1", "1.2", "1.3", "1.4", "1.5", "1.6",
		// R2. The same three ways to read it, in every command.
		"2.1", "2.2", "2.3", "2.4",
		// R3. One declaration of the flags. The last of these is the one the
		// fourth refinement added: an out-of-set mode from the environment or
		// the configuration falls back to plain and says so.
		"3.1", "3.2", "3.3", "3.4", "3.5", "3.6", "3.7",
		// R4. A machine reader can tell two exports apart.
		"4.1", "4.2", "4.3", "4.4",
		// R5. Findings cross the library boundary as facts.
		"5.1", "5.2", "5.3",
		// R6. A column is measured, never declared.
		"6.1", "6.2", "6.3",
		// R7. The model describes runs; it does not describe screens.
		"7.1", "7.2", "7.3", "7.4",
		// R8. Each change is proved by a test that can fail for its own reason.
		"8.1", "8.2", "8.3", "8.4",
	}

	ids := make([]string, 0, len(numbers))
	for _, number := range numbers {
		ids = append(ids, bareID(number))
	}
	return ids
}

// TestCitationTableMatchesTheRequirementsStory046Issues pins the guard's table
// to the set it claims to be, in both directions.
//
// Neither direction is the other's restatement. A requirement 046 issues that
// the table omits makes the guard SPLIT one thing into two: the number in the
// code and the number in the story stop being the same requirement, and a
// truthful citation is reported as a fiction. An id in the table that 046 does
// not issue makes the guard COLLAPSE: a citation of a requirement that does not
// exist is waved through as resolved, which is worse than the first because it
// is silent.
//
// The second direction is not hypothetical bookkeeping — it is the cheapest
// wrong fix for this very sub-task. TestCitationTableCountMatchesTheSentence-
// BesideIt asks the table to hold as many entries as its comment claims, and
// that is satisfied by adding ANY thirty-fifth id. Padding the table to the
// right size while R-3.7 stays missing would satisfy the count, satisfy nothing
// else, and leave the guard accepting a number nobody issued. This test is what
// makes the identity of the entries matter rather than their number.
func TestCitationTableMatchesTheRequirementsStory046Issues(t *testing.T) {
	issued := story046IssuedRequirements()

	// A transcription that read as empty would agree with any table at all.
	if len(issued) == 0 {
		t.Fatal("the transcription of story 046's requirements is empty — every check below would pass against a table holding anything")
	}

	issuedSet := make(map[string]bool, len(issued))
	for _, id := range issued {
		issuedSet[id] = true
	}

	// The split: 046 issues it, the table does not list it.
	for _, id := range issued {
		if !story046Requirements[id] {
			t.Errorf("story 046 issues %s and the guard's table does not list it — a citation of it "+
				"in this package is reported as a number 046 never issued, and the remedy the guard "+
				"prints (%s%s) is refused for the same reason, so the citation cannot be written in "+
				"any form this guard accepts",
				id, thisStoryPrefix, id)
		}
	}

	// The collapse: the table lists it, 046 does not issue it.
	for id := range story046Requirements {
		if !issuedSet[id] {
			t.Errorf("the guard's table lists %s and story 046 does not issue it — a bare citation of "+
				"%s would be accepted as resolvable and resolve to nothing, which is the failure this "+
				"guard exists to prevent, committed by the guard itself",
				id, id)
		}
	}
}

// TestCitationTableResolvesTheRefinementRequirement is the objective stated on
// the two forms a reader writes, hostile cases first.
//
// The order is the point, as it is in TestCitationRule above. Written after the
// table has been corrected, a case asking "does a citation of R-3.7 pass?"
// passes against a classifier that approves everything — including against a
// table padded until the count fits. The cases that must FIRE come first: each
// one is a number story 046 does NOT issue, and each must stay refused however
// the table is repaired.
func TestCitationTableResolvesTheRefinementRequirement(t *testing.T) {
	// The requirement the fourth refinement added, and the one it did not: the
	// next number in the same group, which no refinement has issued.
	refinement := bareID("3.7")
	absent := bareID("3.8")

	cases := []struct {
		name     string
		citation string
		fault    bool
		why      string
	}{
		// Hostile: the number immediately after the last one 046 issues in that
		// group. It is what padding the table to the right size most plausibly
		// adds, and it must stay a fault.
		{
			name:     "a bare citation of the number just past the end of that group",
			citation: absent,
			fault:    true,
			why:      "story 046 stops at the number below it; accepting this one buys the count and loses the rule",
		},
		// Hostile, the converse form: this story's prefix on the same number.
		// The prefix is the remedy's own invention, so nothing in the rule
		// forbids spelling it over a number that does not exist.
		{
			name:     "this story's prefix on the number just past the end of that group",
			citation: thisStoryPrefix + absent,
			fault:    true,
			why:      "a confident, checkable falsehood — it looks resolved and names nothing",
		},
		// Hostile: a number 046 never issued at any refinement, cited bare.
		// This is the class the guard was built for and must not be weakened.
		{
			name:     "a bare citation of a number 046 never issued",
			citation: bareID("9.3"),
			fault:    true,
			why:      "story 044's most-cited number, carried in with the code; bare, it names nothing",
		},
		// The requirement itself, bare — the form 356 citations in this package
		// already use, and the form someone answering R-3.7 will reach for.
		{
			name:     "a bare citation of the requirement the fourth refinement added",
			citation: refinement,
			fault:    false,
			why:      "story 046 issues it, so the guard must not report it as a number 046 never issued",
		},
		// The requirement itself, prefixed — the form the guard's OWN failure
		// message demands. Refusing this one is what made the citation
		// unwritable: the remedy and the rule contradicted each other.
		{
			name:     "this story's prefix on the requirement the fourth refinement added",
			citation: thisStoryPrefix + refinement,
			fault:    false,
			why:      "this is the remedy the guard prints; a guard that refuses its own remedy leaves no way to comply",
		},
		// The third element. Two spellings are handled above; a third reaches
		// the same number by another route. Another story's prefix on it is not
		// this guard's business and must not become so as the table is
		// repaired — the guard knows one story's set and cannot audit 044's.
		{
			name:     "another story's prefix on the same number",
			citation: "S044-" + refinement,
			fault:    false,
			why:      "attributed to a story this guard does not audit; deciding it here would invent violations",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fault := citationFault(testCase.citation)

			if testCase.fault && fault == "" {
				t.Errorf("%s was accepted — %s", testCase.citation, testCase.why)
			}
			if !testCase.fault && fault != "" {
				t.Errorf("%s was rejected as %q — %s", testCase.citation, fault, testCase.why)
			}
		})
	}
}

// requirementTableName is the table this file's own checks read, by the name it
// is declared under.
const requirementTableName = "story046Requirements"

// tableCountSentence reads the size the table's comment claims for it.
//
// The phrase is fixed on purpose. A looser pattern — "any number in the
// paragraph" — would match the requirement numbers the same paragraph quotes
// and pin the table against one of those, which is a check that fails for a
// reason nobody can act on. When the sentence is reworded the test says so and
// names the form it reads, rather than passing quietly.
var tableCountSentence = regexp.MustCompile(`the count below is ([0-9]+)`)

// requirementTableDoc returns the doc comment attached to the requirement
// table, read from this file's own source.
//
// Reading the source rather than restating the sentence in the test is what
// makes the check non-trivial: the assertion is about the words a maintainer
// will actually read on the way past the table, not about a copy of them.
func requirementTableDoc(t *testing.T) string {
	t.Helper()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, requirementTableFile, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s to read the table's comment: %v", requirementTableFile, err)
	}

	for _, decl := range file.Decls {
		genDecl, isGen := decl.(*ast.GenDecl)
		if !isGen || genDecl.Tok != token.VAR {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}
			for _, name := range valueSpec.Names {
				if name.Name != requirementTableName {
					continue
				}
				if genDecl.Doc == nil {
					t.Fatalf("%s has no doc comment — the paragraph stating how the table is maintained is the only instruction a maintainer gets", requirementTableName)
				}
				return genDecl.Doc.Text()
			}
		}
	}

	t.Fatalf("%s is not declared in %s — this check reads the table's comment from source and has just read nothing", requirementTableName, requirementTableFile)
	return ""
}

// TestCitationTableCountMatchesTheSentenceBesideIt keeps the table and the
// sentence beside it from parting company.
//
// This one is GREEN on the day it is written, and that is deliberate rather
// than an oversight: at the moment of writing, the comment says one number and
// the table holds that many entries. What it forbids is the half-fix. Adding
// the missing requirement to the table without touching the sentence leaves a
// paragraph that instructs the next maintainer with a number that is now wrong
// — which is exactly how the omission this sub-task repairs went unnoticed for
// two refinements. The evidence that it fails when that happens is recorded at
// the foot of this file, because a green check proves nothing on its own.
func TestCitationTableCountMatchesTheSentenceBesideIt(t *testing.T) {
	doc := requirementTableDoc(t)

	match := tableCountSentence.FindStringSubmatch(doc)
	if match == nil {
		t.Fatalf("the table's comment no longer states its size in the form this check reads "+
			"(\"the count below is N\"), so the sentence and the table are no longer held together.\n"+
			"    Remedy: restore that phrase with the current number, or teach this test the new "+
			"wording — do not leave the paragraph making a claim nothing checks.\n"+
			"    The comment read:\n%s", doc)
	}

	if stated, actual := match[1], fmt.Sprint(len(story046Requirements)); stated != actual {
		t.Errorf("the table's comment says the count below is %s and the table holds %s entries.\n"+
			"    The paragraph is the only instruction a maintainer adding a requirement gets, and a "+
			"stale number in it is how the table fell behind story 046 twice.\n"+
			"    Remedy: change both together — the entry and the sentence.",
			stated, actual)
	}
}

// story046File is story 046's requirements as the story itself states them.
// The path is relative to this package, which is where a test runs.
const story046File = "../../../.epic/stories/046-report-across-the-cli/story.md"

// storyCriterionLine reads one acceptance criterion's id off a line of the
// story file. Every criterion in that file opens a list item with its id and a
// colon, and nothing else in the file takes that shape — a requirement quoted
// mid-sentence is not at the start of a line, and a heading has no colon after
// the number.
var storyCriterionLine = regexp.MustCompile(`(?m)^- (R[0-9]+\.[0-9]+):`)

// TestCitationTableAgreesWithTheStoryFileWhereItIsPresent checks the
// transcription against the story it transcribes.
//
// It SKIPS where .epic/ is absent, and the skip is the whole reason the
// transcription exists rather than this test replacing it. `git ls-files .epic/`
// returns nothing: for anyone who cloned this repository the story file is not
// there, and a guard that read it directly would pass vacuously for them. So
// the link that must hold everywhere is transcription → table, checked by
// TestCitationTableMatchesTheRequirementsStory046Issues, which reads no file at
// all. This test covers the one link a committed file cannot: it runs wherever
// the story is on disk, which is wherever someone is in a position to add a
// requirement to it — and it fires when they do, instead of waiting for a
// citation of the new number to appear before anything notices.
func TestCitationTableAgreesWithTheStoryFileWhereItIsPresent(t *testing.T) {
	source, err := os.ReadFile(story046File)
	if err != nil {
		t.Skipf("story 046's file is not on disk (%v).\n"+
			"    .epic/ is not committed, so this cross-check runs only where the story is present. "+
			"The transcription it would check is pinned against the guard's table by "+
			"TestCitationTableMatchesTheRequirementsStory046Issues, which reads no file.", err)
	}

	var stated []string
	statedSet := map[string]bool{}
	for _, match := range storyCriterionLine.FindAllStringSubmatch(string(source), -1) {
		stated = append(stated, match[1])
		statedSet[match[1]] = true
	}

	// The story was found and read as having almost no requirements: the reader
	// is broken, not the story. Without this, a changed heading style would
	// turn this check into a comparison of two empty sets.
	if len(stated) < 20 {
		t.Fatalf("read %d acceptance criteria from %s — story 046 has eight requirement groups and "+
			"more than thirty criteria, so this reader has stopped matching the file's shape rather "+
			"than found a shrunken story", len(stated), story046File)
	}

	transcribed := map[string]bool{}
	for _, id := range story046IssuedRequirements() {
		transcribed[id] = true
	}

	for _, id := range stated {
		if !transcribed[id] {
			t.Errorf("story 046 states %s and the transcription in this file does not carry it — the "+
				"story has been refined since the transcription was written, and the guard's table is "+
				"about to fall behind it exactly as it did twice before", id)
		}
	}
	for id := range transcribed {
		if !statedSet[id] {
			t.Errorf("the transcription in this file carries %s and story 046 does not state it — a "+
				"requirement was removed or renumbered, and the guard would go on accepting citations "+
				"of a criterion that no longer exists", id)
		}
	}
}

// story046DesignFile is story 046's design decisions as the design itself
// states them. The path is relative to this package, which is where a test runs.
const story046DesignFile = "../../../.epic/stories/046-report-across-the-cli/design.md"

// designDecisionHeading reads one decision's id off a heading of the design
// file. Every decision there opens a level-two heading with its id and a
// period, and nothing else in the file takes that shape — a decision quoted
// mid-sentence is not at the start of a line, and no other heading is a bare
// letter and number.
var designDecisionHeading = regexp.MustCompile(`(?m)^## (D[0-9]+)\.`)

// TestCitationDecisionTableAgreesWithTheDesignFileWhereItIsPresent checks the
// decision set against the design it is copied from, in both directions.
//
// It is the ONLY check the decision set can have, and that is a consequence of
// the set having one copy rather than two — see story046Decisions for why one
// is the right number. So this test carries alone what four tests carry for the
// requirement table, and the two directions are the two different lies a stale
// copy tells: a missing entry reports a decision 046 genuinely made as a number
// it never issued, and a spurious one waves through a citation that resolves to
// nothing.
//
// It SKIPS where .epic/ is absent, and the skip is honest rather than
// convenient: `git ls-files .epic/` returns nothing, so for anyone who cloned
// this repository the design file is not there — and neither is any way to add
// a decision to it. The check runs wherever the drift it catches can be caused.
func TestCitationDecisionTableAgreesWithTheDesignFileWhereItIsPresent(t *testing.T) {
	source, err := os.ReadFile(story046DesignFile)
	if err != nil {
		t.Skipf("story 046's design file is not on disk (%v).\n"+
			"    .epic/ is not committed, so this cross-check runs only where the design is present. "+
			"The set it checks has one copy, so there is no second committed copy for a check that "+
			"runs everywhere to compare it against — and where the design is absent, so is any way "+
			"to add a decision to it.", err)
	}

	stated := map[string]bool{}
	for _, match := range designDecisionHeading.FindAllStringSubmatch(string(source), -1) {
		stated[match[1]] = true
	}

	// The design was found and read as having almost no decisions: the reader
	// is broken, not the design. Without this, a changed heading style would
	// turn both directions below into comparisons against an empty set — and
	// the second direction would then report all ten as removed.
	if len(stated) < 5 {
		t.Fatalf("read %d design decisions from %s — story 046's design states ten, so this reader has "+
			"stopped matching the file's shape rather than found a shrunken design", len(stated), story046DesignFile)
	}

	for id := range stated {
		if !story046Decisions[id] {
			t.Errorf("story 046's design states %s and the set in this file does not carry it — a bare "+
				"citation of it under this package would be reported as a number 046 never issued, and "+
				"the remedy the guard prints (%s%s) would be refused for the same reason, so the citation "+
				"could not be written in any form this guard accepts", id, thisStoryPrefix, id)
		}
	}
	for id := range story046Decisions {
		if !stated[id] {
			t.Errorf("the set in this file carries %s and story 046's design does not state it — a "+
				"decision was removed or renumbered, and the guard would go on accepting bare citations "+
				"of a decision that no longer exists", id)
		}
	}
}

// # EVIDENCE THAT THESE CHECKS FAIL WHEN THE RULE THEY GUARD IS BROKEN (S046-R8.3)
//
// Two of the four checks above are RED on the day they are written and two are
// GREEN, and the two states need different evidence.
//
// The red pair — the set match and the two forms of R-3.7 — fail against the
// tree exactly as committed, which is the defect this sub-task exists to close.
// A check that fails always is no more informative than one that passes always,
// so what they still owe is proof that they go green when, and only when, the
// table is corrected. Mutation A supplies it.
//
// The green pair — the count sentence and the story-file cross-check — would
// otherwise be assertions nobody has ever seen fail. Mutations B through E
// break each of their rules in turn. The artifacts holding these runs
// (.draft/red-evidence.yaml) are not committed, so a pointer to them is a
// pointer to nothing for anyone who cloned this; the transcripts are written
// here instead, as this file's header does for the guard itself.
//
// Measured on 2026-08-30, Go 1.27. Each mutation was applied on its own to a
// copy of this file placed in the package, run once, and the file restored
// immediately; the tree was returned with `git checkout -- ` and the restore
// verified by an empty `git status --porcelain`.
//
// Every requirement number below is written with a hyphen after the R — R-3.7
// for what the runs printed without one — because this file is swept by its own
// rule and quoting these citations verbatim would make the evidence violate the
// thing it records. The hyphen is the only edit; the lines are otherwise as
// printed, re-wrapped to this comment's width, and the PASS lines of unrelated
// subtests are elided where marked.
//
// Mutation A — the fix applied by halves: R-3.7 added to the table, the
// sentence beside it left saying 34.
//
//	--- PASS: TestCitationTableMatchesTheRequirementsStory046Issues (0.00s)
//	--- PASS: TestCitationTableResolvesTheRefinementRequirement (0.00s)
//	    [... its six subtests, every one PASS ...]
//	    citation_test.go:1050: the table's comment says the count below is 34
//	        and the table holds 35 entries.
//	        The paragraph is the only instruction a maintainer adding a
//	        requirement gets, and a stale number in it is how the table fell
//	        behind story 046 twice.
//	        Remedy: change both together — the entry and the sentence.
//	--- FAIL: TestCitationTableCountMatchesTheSentenceBesideIt (0.00s)
//	--- PASS: TestCitationTableAgreesWithTheStoryFileWhereItIsPresent (0.00s)
//
// It carries two facts at once. The count check fires on the half-fix — which
// is the state this sub-task's own remedy leaves behind if the sentence is
// forgotten, and forgetting the sentence is how the table fell behind story 046
// the first time. And the two checks that are red on arrival PASS here: they
// are not failing vacuously, they go green precisely when the table gains the
// entry the story issues.
//
// Mutation B — the converse half: the sentence updated to 35, the table left at
// 34. The check must be symmetric, or a maintainer could satisfy it by editing
// the easier of the two.
//
//	citation_test.go:1050: the table's comment says the count below is 35 and
//	    the table holds 34 entries.
//	    [the same three lines, word for word]
//	--- FAIL: TestCitationTableCountMatchesTheSentenceBesideIt (0.00s)
//
// Mutation C — R-3.7 removed from the transcription in this file, so that the
// transcription and the table are wrong TOGETHER. This is the most instructive
// of the five, and it is the reason the story-file check exists at all:
//
//	--- PASS: TestCitationTableMatchesTheRequirementsStory046Issues (0.00s)
//	    citation_test.go:966: R-3.7 was rejected as "story 046 has no R-3.7, and
//	        the citation is bare, so it names nothing a reader can resolve" —
//	        story 046 issues it, so the guard must not report it as a number 046
//	        never issued
//	    citation_test.go:966: S046-R-3.7 was rejected as "the citation claims
//	        story 046 owns R-3.7, and story 046 has no R-3.7" — this is the
//	        remedy the guard prints; a guard that refuses its own remedy leaves
//	        no way to comply
//	--- FAIL: TestCitationTableResolvesTheRefinementRequirement (0.00s)
//	--- PASS: TestCitationTableCountMatchesTheSentenceBesideIt (0.00s)
//	    citation_test.go:1114: story 046 states R-3.7 and the transcription in
//	        this file does not carry it — the story has been refined since the
//	        transcription was written, and the guard's table is about to fall
//	        behind it exactly as it did twice before
//	--- FAIL: TestCitationTableAgreesWithTheStoryFileWhereItIsPresent (0.00s)
//
// The set match PASSES: two copies that agree with each other and with nothing
// else are consistent, and consistency is all that check can see. Only the run
// that reads story.md notices, and the named-requirement check keeps failing
// for its own reason. That is the shape of the original defect — a table
// agreeing with a maintainer's memory of the story rather than with the story —
// and it is why the transcription is checked against the file wherever the file
// is on disk, rather than trusted.
//
// Mutation D — the converse: "3.8" added to the transcription, a number no
// refinement has issued.
//
//	citation_test.go:1121: the transcription in this file carries R-3.8 and
//	    story 046 does not state it — a requirement was removed or renumbered,
//	    and the guard would go on accepting citations of a criterion that no
//	    longer exists
//	--- FAIL: TestCitationTableAgreesWithTheStoryFileWhereItIsPresent (0.00s)
//
// Mutation E — the sentence reworded rather than corrected, to "and there are
// 34 of them below". Nothing about the table changed; only the phrase the check
// reads did. A silent pass here would be the worst outcome of the five, because
// the paragraph would go on instructing maintainers with a number nothing
// verifies:
//
//	citation_test.go:1042: the table's comment no longer states its size in the
//	    form this check reads ("the count below is N"), so the sentence and the
//	    table are no longer held together.
//	    Remedy: restore that phrase with the current number, or teach this test
//	    the new wording — do not leave the paragraph making a claim nothing
//	    checks.
//	    The comment read:
//	    [the paragraph, printed in full so the reader can see what to restore]
//	--- FAIL: TestCitationTableCountMatchesTheSentenceBesideIt (0.00s)
//
// Every message names the file, what disagreed with what, and what to change —
// which is what makes these four answerable by someone who did not write them.
