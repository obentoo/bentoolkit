package report

// Authored for story 047, sub-task 2.1 — S047-R1.1, S047-R1.4.
//
// # Why this file exists at all, when two guards already cover the new types
//
// Sub-task 2.1 declares types and no behaviour, so most of what could be
// asserted about it the compiler has already asserted: that a field exists,
// that it has the type design.md gives it, that the package builds. A test
// restating any of that would be a test that cannot fail.
//
// Two guards cover the rest. TestPackageDeclaresNoPresentationField walks every
// field name in the package against the forbidden vocabulary, and
// TestPackageImportsNoPresentation fails the suite for an import of
// internal/overlay or of a presentation library. Between them, naming and
// dependency direction are mechanical.
//
// ONE THING IS LEFT UNCOVERED BY ALL OF IT, and it is the one design.md calls
// a trap: the exported fields and their json tags become a public wire contract
// the moment they ship, and nothing forces anyone to look at them (S047-D8).
// render/json.go marshals the run as-is, so adding `omitempty` to a count, or
// renaming a tag, compiles, passes both guards, passes go vet, and silently
// changes what every consumer of the export reads. run.go documents the cost of
// exactly that on Complete and NotEvaluated: a dropped `false` or `0` reads as
// "the producer never said", which is the conflation the fields exist to
// remove. This file is the check that fails when it happens.
//
// # Recorded evidence that it fails when its rule is broken (S047-R8.1)
//
// Measured against HEAD ab5a471 during sub-task 2.1, and re-stated here in
// sub-task 6.3's quality-gate pass because the evidence had been recorded in
// the story's deviation register rather than beside the guard it describes —
// and a guard whose proof lives somewhere a reader will not look is a guard
// nobody can check.
//
// Mutation: `needs_rebase` removed from compareRunKeys, run, reverted, the
// revert verified byte-identical by md5sum. The failure names the type and the
// key, which is the whole of what a maintainer meeting it needs:
//
//	report.CompareRun published the key "needs_rebase", which this contract does not list
//
// The contract is taken at the ZERO value, and that is where it has to be
// taken: a populated fixture publishes every key whether or not a tag carries
// omitempty, so it would agree with the very change this exists to catch.
//
// # What is deliberately NOT here
//
// Nothing asserts what a section says or how a group is built. Sections is
// sub-task 2.2's and the grouping rule is sub-task 2.3's, and each arrives with
// the behavioural tests its own Validation command names.
//
// Sub-task 2.2 has since landed, so CompareRun does satisfy Payload and
// compare_run_test.go is where what its sections SAY is pinned. This file is
// unchanged by that: the document a payload marshals into and the blocks it
// renders are two different behaviours, and a key set is not something Sections
// can state.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// keysOf marshals a value and returns the top-level keys of the object it
// produced, sorted.
//
// It reads the document back rather than inspecting struct tags with reflect on
// purpose: a tag is what the code says, and the key set is what a consumer
// holding the file actually meets. `omitempty` is invisible to the first and
// decisive in the second, which is the whole difference this file exists to
// catch.
func keysOf(t *testing.T, value any) []string {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshalling %T: %v", value, err)
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("%T did not marshal into a JSON object: %v\n%s", value, err, data)
	}

	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// assertKeys compares a value's key set against the contract, naming both
// halves of any disagreement — a key that vanished and a key that appeared are
// different defects with different remedies, and a message reporting only
// "keys differ" costs its reader the comparison.
func assertKeys(t *testing.T, value any, want []string) {
	t.Helper()

	got := keysOf(t, value)

	present := make(map[string]bool, len(got))
	for _, key := range got {
		present[key] = true
	}
	expected := make(map[string]bool, len(want))
	for _, key := range want {
		expected[key] = true
	}

	for _, key := range want {
		if !present[key] {
			t.Errorf("%T dropped the key %q from the exported document.\n"+
				"    A key a consumer read yesterday and cannot read today is a broken wire contract, and the\n"+
				"    usual cause is an added omitempty: a zero or a false dropped from the document reads as\n"+
				"    \"the producer never said\", which is the opposite of what the field states (S047-D8).\n"+
				"    Keys present: %s", value, key, strings.Join(got, ", "))
		}
	}
	for _, key := range got {
		if !expected[key] {
			t.Errorf("%T published the key %q, which this contract does not list.\n"+
				"    An exported field is public the moment it ships. Add it here deliberately, with the\n"+
				"    JSON golden for the compare kind, or unexport it (S047-D8).", value, key)
		}
	}
}

// The key sets themselves, written out as the contract rather than derived from
// the structs. Deriving them would make the test agree with whatever the code
// says, which is agreement rather than a check.
var (
	compareRunKeys = []string{
		"in_both", "keep", "keep_groups", "needs_rebase", "notes", "only_local",
		"reading_failures", "redundant", "repository", "scanned", "unknown", "unread", "verdicts",
	}
	comparePkgKeys = []string{
		"cause", "diff", "error", "further_findings", "local_version", "package", "reading",
		"reason", "remote_version", "status",
	}
	keepGroupKeys     = []string{"by_category", "label", "local_version", "members", "remote_version"}
	categoryCountKeys = []string{"category", "count"}
	verdictTallyKeys  = []string{"keep", "needs_rebase", "redundant", "unknown"}
)

// TestComparePayloadPublishesEveryKeyAtItsZeroValue is the omitempty guard, and
// the zero value is where it has to be taken.
//
// A populated payload publishes every key whether or not a tag carries
// omitempty, so a test built only on a full fixture would pass over the defect
// it was written for. The zero value is the case that separates them: an
// omitempty on `scanned` costs nothing until a run scans nothing, and "we
// scanned zero packages" then becomes indistinguishable from "nobody counted"
// in the one document a consumer has.
//
// Every nested type is checked at its zero value too, for the same reason and
// one more: a count inside a group ("how many of these members are in
// dev-lang") reaches zero the moment a producer builds the entry without
// filling it, and that is precisely the state worth being able to see.
func TestComparePayloadPublishesEveryKeyAtItsZeroValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  []string
	}{
		{"CompareRun", CompareRun{}, compareRunKeys},
		{"ComparePkg", ComparePkg{}, comparePkgKeys},
		{"KeepGroup", KeepGroup{}, keepGroupKeys},
		{"CategoryCount", CategoryCount{}, categoryCountKeys},
		{"VerdictTally", VerdictTally{}, verdictTallyKeys},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertKeys(t, tc.value, tc.want)
		})
	}
}

// TestComparePayloadNestsItsListsUnderTheirOwnKeys pins the shape a consumer
// walks, one level down from the key set above.
//
// The keys alone do not say that `.keep[0]` is an object with a `package` in
// it, and a payload whose lists marshalled as strings — or as objects with a
// different vocabulary — would satisfy the zero-value test completely. This
// builds one fully populated run and reads a value back out of every nested
// type, which is also the only place the nested key sets are reachable: a zero
// CompareRun has nil slices, and nil marshals to null rather than to a row.
func TestComparePayloadNestsItsListsUnderTheirOwnKeys(t *testing.T) {
	run := CompareRun{
		Repository: "gentoo",
		Notes:      []string{"3 of the 3 packages compared were examined for divergences."},
		Scanned:    3,
		InBoth:     2,
		OnlyLocal:  1,
		Redundant: []ComparePkg{{
			Package: "app-editors/zed",
			Local:   "1.2.3",
			Remote:  "1.2.3",
			Status:  "up to date",
			Reading: "read",
			Diff:    "identical",
			Reason:  "the overlay copy matches the repository byte for byte",
			// The second finding and every one after it: a row holds one
			// reason, and what will not fit a cell is said beside the table
			// rather than dropped (S047-R6.1).
			FurtherFindings: []string{"the metadata differs on one axis: KEYWORDS"},
		}},
		Keep: []ComparePkg{{
			Package: "dev-lang/go",
			Local:   "1.27.0",
			Remote:  "1.26.0",
			Status:  "newer",
			Reading: "not requested",
			Diff:    "not compared",
		}},
		KeepGroups: []KeepGroup{{
			Label:      "1.27.0 over 1.26.0",
			Local:      "1.27.0",
			Remote:     "1.26.0",
			Members:    []string{"dev-lang/go", "dev-lang/rust"},
			ByCategory: []CategoryCount{{Category: "dev-lang", Count: 2}},
		}},
		Unknown: []ComparePkg{{
			Package: "sys-apps/portage",
			Status:  "error",
			Reading: "failed",
			Diff:    "unreadable",
			Reason:  "the remote repository could not be read",
		}},
		Verdicts: VerdictTally{Keep: 1, Redundant: 1, NeedsRebase: 0, Unknown: 1},
		Unread:   2,
	}

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshalling the payload: %v", err)
	}

	var document struct {
		Redundant  []map[string]any `json:"redundant"`
		Keep       []map[string]any `json:"keep"`
		KeepGroups []struct {
			Members    []string         `json:"members"`
			ByCategory []map[string]any `json:"by_category"`
		} `json:"keep_groups"`
		Unknown  []map[string]any `json:"unknown"`
		Verdicts map[string]any   `json:"verdicts"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("the payload did not produce the nested shape a consumer walks: %v\n%s", err, data)
	}

	for _, tc := range []struct {
		path string
		rows []map[string]any
		key  string
		want string
	}{
		{".redundant[0]", document.Redundant, "package", "app-editors/zed"},
		{".keep[0]", document.Keep, "package", "dev-lang/go"},
		{".unknown[0]", document.Unknown, "package", "sys-apps/portage"},
	} {
		if len(tc.rows) != 1 {
			t.Errorf("%s: the list holds %d entries, want 1 — an entry lost between the payload and the document is an entry no consumer can act on", tc.path, len(tc.rows))
			continue
		}
		if got, _ := tc.rows[0][tc.key].(string); got != tc.want {
			t.Errorf("%s[%q] = %q, want %q", tc.path, tc.key, got, tc.want)
		}
	}

	if len(document.KeepGroups) != 1 {
		t.Fatalf(".keep_groups holds %d entries, want 1", len(document.KeepGroups))
	}
	group := document.KeepGroups[0]
	if got := strings.Join(group.Members, ","); got != "dev-lang/go,dev-lang/rust" {
		t.Errorf(".keep_groups[0].members = %q, want the two atoms in the order the run established them — "+
			"the first member is what breaks ties between two groups of equal size (S047-R2.4)", got)
	}
	if len(group.ByCategory) != 1 {
		t.Fatalf(".keep_groups[0].by_category holds %d entries, want 1", len(group.ByCategory))
	}
	if got, _ := group.ByCategory[0]["count"].(float64); int(got) != 2 {
		t.Errorf(".keep_groups[0].by_category[0][\"count\"] = %v, want 2 — the breakdown is carried as data, "+
			"because a group counted only by a renderer is a group the exported document does not describe (S047-D2)", group.ByCategory[0]["count"])
	}

	if got, _ := document.Verdicts["needs_rebase"].(float64); int(got) != 0 {
		t.Errorf(`.verdicts["needs_rebase"] = %v, want 0 — the column with no list of its own is exactly the one `+
			`a dropped zero would hide`, document.Verdicts["needs_rebase"])
	}
}
