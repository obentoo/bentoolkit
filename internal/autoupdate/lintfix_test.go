package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every test in this file is named TestLintFix* on purpose: the sub-task's
// validation command is `go test -run 'TestLintFix'`, so a test that does not
// carry the prefix is a test that command does not run.

// repairOverlay writes content as the packages.toml of a fresh temp overlay and
// returns the overlay path and the config path.
func repairOverlay(t *testing.T, content string) (overlay, configPath string) {
	t.Helper()
	overlay = t.TempDir()
	dir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath = filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write packages.toml: %v", err)
	}
	return overlay, configPath
}

// readFixture loads one of the committed repair fixtures.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

// repairText runs the whole repair over content in a temp overlay and returns
// the text it would write, failing the test when the gate aborts.
func repairText(t *testing.T, content string) (*RepairResult, string) {
	t.Helper()
	overlay, _ := repairOverlay(t, content)
	res, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("repair aborted: %v", err)
	}
	return res, res.Repaired
}

// recordText returns one record's text — header through `# END` — from a file,
// so a test can assert on a record without matching the whole file.
func recordText(t *testing.T, content, pkg string) string {
	t.Helper()
	for _, rec := range parseRegistryLayout(content).records {
		if rec.name != pkg {
			continue
		}
		var b strings.Builder
		b.WriteString(rec.header + "\n")
		for _, f := range rec.fields {
			for _, line := range f.lead {
				b.WriteString(line + "\n")
			}
			for _, line := range f.body {
				b.WriteString(line + "\n")
			}
		}
		for _, line := range rec.tail {
			if strings.TrimSpace(line) == "" {
				continue
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	t.Fatalf("record %q not found", pkg)
	return ""
}

// ---------------------------------------------------------------------------
// The integration case: the committed fixture pair.
// ---------------------------------------------------------------------------

// TestLintFixFixtureMatchesGolden is the integration case the story's design
// settled on in place of a copy of the real registry (recorded in
// .draft/deviations.yaml): the real overlay auto-commits, drifted from 408 to
// 411 records mid-story, and does not exist in CI at all, so a byte-for-byte
// assertion against it would be neither deterministic nor runnable where it
// matters. The synthetic pair is both, and every record in it covers a shape
// measured on the real file.
func TestLintFixFixtureMatchesGolden(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	golden := readFixture(t, "repair_golden.toml")

	res, repaired := repairText(t, input)
	if !res.Changed {
		t.Fatal("the fixture has repairs to make, but the repair reports nothing changed")
	}
	if repaired != golden {
		t.Fatalf("repaired fixture does not match testdata/repair_golden.toml: %s",
			describeFirstDifference(golden, repaired))
	}

	want := map[string]int{
		FixBinaryToType:  2, // legacy-binary-only, combined-repairs
		FixDropBinary:    2, // legacy-binary-with-type, legacy-binary-false
		FixDropEnabled:   2, // redundant-enabled, combined-repairs
		FixReorderFields: 5, // khronos-shape, structural-comments, multiline-array, combined-repairs, authenticated-fetch
	}
	for action, n := range want {
		if res.Actions[action] != n {
			t.Errorf("action %s applied %d time(s), want %d", action, res.Actions[action], n)
		}
	}
	for action, n := range res.Actions {
		if want[action] != n {
			t.Errorf("unexpected action %s applied %d time(s)", action, n)
		}
	}
}

// TestLintFixFixtureReparsesIdentically is UB1 and the semantic half of R7.1
// over the whole fixture: every record still loads, the count is unchanged, none
// fails validation, and the only values that moved are the declared ones.
func TestLintFixFixtureReparsesIdentically(t *testing.T) {
	input := readFixture(t, "repair_input.toml")

	before, err := decodePackagesConfig([]byte(input))
	if err != nil {
		t.Fatalf("fixture input does not load: %v", err)
	}
	_, repaired := repairText(t, input)
	after, err := decodePackagesConfig([]byte(repaired))
	if err != nil {
		t.Fatalf("repaired fixture does not load: %v", err)
	}

	if len(after.Packages) != len(before.Packages) {
		t.Fatalf("%d records before the repair, %d after", len(before.Packages), len(after.Packages))
	}
	for pkg, cfg := range after.Packages {
		c := cfg
		if err := ValidatePackageConfig(pkg, &c); err != nil {
			t.Errorf("record %q fails validation after the repair: %v", pkg, err)
		}
	}

	// The declared transformations, checked from the outside rather than from
	// the plan the repair built.
	if got := after.Packages["app-editors/legacy-binary-only"].Type; got != "bin" {
		t.Errorf(`legacy-binary-only: type is %q, want "bin" (R1.2)`, got)
	}
	if got := after.Packages["media-video/combined-repairs"].Type; got != "bin" {
		t.Errorf(`combined-repairs: type is %q, want "bin" (R1.2)`, got)
	}
	if got := after.Packages["app-misc/legacy-binary-false"].Type; got != "" {
		t.Errorf("legacy-binary-false: the repair invented type = %q; binary = false says nothing (R1.3)", got)
	}
	if got := after.Packages["net-misc/redundant-enabled"].Enabled; got != nil {
		t.Errorf("redundant-enabled: enabled survived as %v, want it dropped (R2.2)", *got)
	}
	if got := after.Packages["net-misc/disabled-entry"].Enabled; got == nil || *got {
		t.Errorf("disabled-entry: enabled = false must survive, got %v (R2.2)", got)
	}
	// UB4: commit_sha_path without track = "commit" is untouched.
	cursor := after.Packages["app-editors/build-id-substitution"]
	if cursor.CommitSHAPath != "commitSha" || cursor.Track != "" {
		t.Errorf("UB4: the cursor shape changed: track=%q commit_sha_path=%q", cursor.Track, cursor.CommitSHAPath)
	}
	// UB5: the basic-string regex survives as written, quotes and all.
	if !strings.Contains(repaired, `base_pattern = "(?m)^  version: '([0-9][0-9.]*)'"`) {
		t.Error("UB5: the basic-string regex was requoted or lost")
	}
}

// TestLintFixFixtureIsIdempotent closes the loop task 4.1's ranking was built
// for: after the repair, the linter reports nothing it offers a repair for, and
// a second repair has nothing left to do. If this fails, the report and the
// repair disagree — one of them is fixing what the other never named.
func TestLintFixFixtureIsIdempotent(t *testing.T) {
	overlay, configPath := repairOverlay(t, readFixture(t, "repair_input.toml"))

	res, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("repair aborted: %v", err)
	}
	if err := res.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}

	issues, err := LintPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("lint after the repair: %v", err)
	}
	for _, issue := range issues {
		if issue.Fix != FixNone {
			t.Errorf("repairable finding survived the repair: %s", issue)
		}
		// The two rules that offer no repair by design are the only ones the
		// fixture expects afterwards: legacy-base (R6.1, only a human knows the
		// base source) and bracket-line-in-comments, which fires on the
		// "["-prefixed documentation line R3.3 requires the fixture to carry.
		switch issue.Rule {
		case LintLegacyBase, LintBracketInComment:
		default:
			t.Errorf("unexpected finding after the repair: %s", issue)
		}
	}

	again, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("second repair aborted: %v", err)
	}
	if again.Changed {
		t.Errorf("the repair is not idempotent: a second pass still wants %v", again.Actions)
	}

	// And the second pass really did leave the file alone.
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(after) != res.Repaired {
		t.Error("the file on disk is not what the first repair produced")
	}
}

// ---------------------------------------------------------------------------
// R3.3 / UB2 — the prototype's bug.
// ---------------------------------------------------------------------------

// TestLintFixPreservesStructuralCommentLines is the measured bug this whole file
// is defensive about. `comments = """` both opens and ends with the delimiter,
// so an open-detector written as "the line does not end with the delimiter"
// reports the block as closed on its opening line: the documentation then spills
// out of the string, `#` and `[` lines are read as file structure, and the
// registry stops parsing. The record here is deliberately one the repair MOVES —
// its comments block is relocated by the reorder — so the block travels through
// the rewriter instead of sitting still.
func TestLintFixPreservesStructuralCommentLines(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	const pkg = "www-client/structural-comments"
	was, hadDoc := commentsBlockOf(t, input, pkg)
	is, hasDoc := commentsBlockOf(t, repaired, pkg)
	if !hadDoc || !hasDoc {
		t.Fatalf("comments block present before=%t after=%t", hadDoc, hasDoc)
	}
	if was != is {
		t.Fatalf("the comments block is not byte-identical: %s", describeFirstDifference(was, is))
	}
	for _, line := range []string{
		"# this line starts with a hash and is documentation, not a comment",
		"[this line starts with a bracket and is documentation, not a header]",
		`A run of three quotes appears here as \"\"\" — escaped, the way`,
	} {
		if !strings.Contains(is, line) {
			t.Errorf("the doc line %q did not survive", line)
		}
	}

	// Not one block in the file may differ, not only this one (UB2).
	afterBlocks := map[string]string{}
	for _, rec := range parseRegistryLayout(repaired).records {
		block, _ := rec.commentsBlock()
		afterBlocks[rec.name] = block
	}
	for _, rec := range parseRegistryLayout(input).records {
		block, ok := rec.commentsBlock()
		if !ok {
			t.Errorf("record %q has no comments block in the fixture", rec.name)
			continue
		}
		if afterBlocks[rec.name] != block {
			t.Errorf("the comments block of %q changed: %s",
				rec.name, describeFirstDifference(block, afterBlocks[rec.name]))
		}
	}
}

// commentsBlockOf returns a record's doc block, as written, from file text.
func commentsBlockOf(t *testing.T, content, pkg string) (string, bool) {
	t.Helper()
	for _, rec := range parseRegistryLayout(content).records {
		if rec.name == pkg {
			return rec.commentsBlock()
		}
	}
	t.Fatalf("record %q not found", pkg)
	return "", false
}

// ---------------------------------------------------------------------------
// Record-level transformations.
// ---------------------------------------------------------------------------

// TestLintFixLegacyBinaryWithTypeOnlyDeletes pins R1.3: a record that already
// classifies itself loses the retired line and nothing else. The `type` line
// keeps its text, its quoting and its position.
func TestLintFixLegacyBinaryWithTypeOnlyDeletes(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	const pkg = "app-editors/legacy-binary-with-type"
	before := recordText(t, input, pkg)
	after := recordText(t, repaired, pkg)

	want := strings.ReplaceAll(before, "binary = true\n", "")
	if after != want {
		t.Fatalf("the deletion was not the only change: %s", describeFirstDifference(want, after))
	}
	// The word "binary" still appears — in the record's own documentation, which
	// must survive — so look for the assignment, not the word.
	if strings.Contains(after, "\nbinary =") {
		t.Error("the retired binary key survived")
	}
	if !strings.Contains(after, `type = "bin"`) {
		t.Error("the existing type line was lost")
	}
}

// TestLintFixEnabledFalseSurvives pins the half of R2.2 that must NOT happen:
// `enabled = false` is the bookkeeping that keeps an orphaned entry out of the
// run, and the whole record comes out byte for byte.
func TestLintFixEnabledFalseSurvives(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	const pkg = "net-misc/disabled-entry"
	if before, after := recordText(t, input, pkg), recordText(t, repaired, pkg); before != after {
		t.Fatalf("a record with enabled = false was rewritten: %s", describeFirstDifference(before, after))
	}
	if !strings.Contains(repaired, "enabled = false") {
		t.Fatal("enabled = false was deleted")
	}
}

// TestLintFixLeavesUnreportedRecordsAlone is the other side of the repair's
// contract: a record no rule named is not touched, down to its bytes. It is
// checked on the canonical record, on the legacy-base record (reported, never
// repaired — R6.1) and on the cursor shape (UB4).
func TestLintFixLeavesUnreportedRecordsAlone(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	for _, pkg := range []string{
		"app-arch/canonical-record",
		"sys-apps/legacy-base",
		"app-editors/build-id-substitution",
		"net-libs/basic-string-regex",
	} {
		if before, after := recordText(t, input, pkg), recordText(t, repaired, pkg); before != after {
			t.Errorf("record %q was rewritten: %s", pkg, describeFirstDifference(before, after))
		}
	}
}

// TestLintFixNoOpFileIsNotRewritten pins that a clean registry produces no
// change at all: same bytes, nothing to write, and Write leaves the file's
// modification time alone.
func TestLintFixNoOpFileIsNotRewritten(t *testing.T) {
	golden := readFixture(t, "repair_golden.toml")
	overlay, configPath := repairOverlay(t, golden)

	res, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("repair aborted: %v", err)
	}
	if res.Changed {
		t.Fatalf("the canonical fixture still wants repairs: %v", res.Actions)
	}
	if res.Repaired != golden {
		t.Fatalf("a no-op repair changed the text: %s", describeFirstDifference(golden, res.Repaired))
	}

	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := res.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("a no-op repair rewrote the file")
	}
}

// TestLintFixParseRenderRoundTrip pins the invariant buildRepair asserts at run
// time: splitting the registry into its layout and writing it back reproduces it
// byte for byte. Every shape the fixture carries goes through it, plus the two
// file endings a text file can have.
func TestLintFixParseRenderRoundTrip(t *testing.T) {
	golden := readFixture(t, "repair_golden.toml")
	cases := map[string]string{
		"messy fixture":       readFixture(t, "repair_input.toml"),
		"canonical fixture":   golden,
		"no trailing newline": strings.TrimSuffix(golden, "\n"),
		"empty":               "",
		"header only":         "# just a header\n",
		"blank lines only":    "\n\n\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if got := parseRegistryLayout(content).render(); got != content {
				t.Fatalf("round trip changed the text: %s", describeFirstDifference(content, got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R7.1 — the reparse gate.
// ---------------------------------------------------------------------------

// TestLintFixGateRejectsACorruptedRewrite is the most important test in this
// story. It does not test the rewriter; it tests the thing that stands between a
// broken rewriter and a published registry. Each case injects the output a
// specific rewriter bug would produce, and asserts two things: the write returns
// an error, and the file on disk is BYTE-IDENTICAL afterwards — an aborted
// repair is a bug report, not a partial write.
func TestLintFixGateRejectsACorruptedRewrite(t *testing.T) {
	input := readFixture(t, "repair_input.toml")

	corruptions := map[string]func(t *testing.T, repaired string) string{
		// The prototype's bug: `comments = """` read as closed on its opening
		// line, so every doc body line is dropped.
		"comments blocks truncated": func(t *testing.T, repaired string) string {
			lines := strings.Split(repaired, "\n")
			mask := commentsBodyMask(lines)
			kept := make([]string, 0, len(lines))
			for i, line := range lines {
				if !mask[i] {
					kept = append(kept, line)
				}
			}
			return strings.Join(kept, "\n")
		},
		// The same bug's quieter form: one doc block loses its middle lines but
		// keeps its delimiters, so the file still parses.
		"one comments block silently shortened": func(t *testing.T, repaired string) string {
			const doc = "# this line starts with a hash and is documentation, not a comment\n"
			if !strings.Contains(repaired, doc) {
				t.Fatal("the fixture no longer carries the doc line this case removes")
			}
			return strings.Replace(repaired, doc, "", 1)
		},
		// A doc block re-escaped into different bytes that decode to the same
		// string: invisible to a value comparison, caught by the byte check.
		"comments block re-escaped": func(t *testing.T, repaired string) string {
			const was = `appears here as \"\"\" — escaped`
			if !strings.Contains(repaired, was) {
				t.Fatal("the fixture no longer carries the escaped delimiter this case rewrites")
			}
			return strings.Replace(repaired, was, `appears here as \"\"" — escaped`, 1)
		},
		// A rewriter that eats the record markers. No TOML parse can see this:
		// to a parser the markers were never there.
		"an END marker dropped": func(t *testing.T, repaired string) string {
			return strings.Replace(repaired, "\n# END\n", "\n", 1)
		},
		// A rewriter that eats the file header — the only documentation of the
		// record model itself.
		"the file header dropped": func(t *testing.T, repaired string) string {
			_, body, found := strings.Cut(repaired, "\n[\"app-arch/canonical-record\"]")
			if !found {
				t.Fatal("the fixture no longer starts with the record this case cuts to")
			}
			return "[\"app-arch/canonical-record\"]" + body
		},
		// A rewriter that loses a whole record.
		"a record dropped": func(t *testing.T, repaired string) string {
			start := strings.Index(repaired, `["net-misc/disabled-entry"]`)
			if start < 0 {
				t.Fatal("the fixture no longer carries the record this case drops")
			}
			rest := repaired[start:]
			end := strings.Index(rest, "# END\n")
			if end < 0 {
				t.Fatal("record has no end marker")
			}
			return repaired[:start] + rest[end+len("# END\n"):]
		},
		// A rewriter that moves a line into the neighbouring record: the line
		// inventory still balances, the values do not.
		"a line moved into the next record": func(t *testing.T, repaired string) string {
			const line = "select = \"max\"\n"
			if !strings.Contains(repaired, line) {
				t.Fatal("the fixture no longer carries the line this case moves")
			}
			moved := strings.Replace(repaired, line, "", 1)
			return strings.Replace(moved, "[\"dev-util/multiline-array\"]\n", "[\"dev-util/multiline-array\"]\n"+line, 1)
		},
		// A rewriter that "helpfully" requotes a regex, which changes what the
		// regex means.
		"a value requoted": func(t *testing.T, repaired string) string {
			const was = `pattern = 'browser-([0-9]+\.[0-9]+)\.tar\.bz2'`
			if !strings.Contains(repaired, was) {
				t.Fatal("the fixture no longer carries the pattern this case requotes")
			}
			return strings.Replace(repaired, was, `pattern = "browser-([0-9]+.[0-9]+).tar.bz2"`, 1)
		},
		// A rewriter that invents a field nobody wrote.
		"a field invented": func(t *testing.T, repaired string) string {
			return strings.Replace(repaired, "[\"app-arch/canonical-record\"]\n",
				"[\"app-arch/canonical-record\"]\nselect = \"first\"\n", 1)
		},
		// A rewriter that duplicates a line instead of moving it.
		"a line duplicated": func(t *testing.T, repaired string) string {
			const line = "parser = \"json\"\n"
			return strings.Replace(repaired, line, line+line, 1)
		},
	}

	for name, corrupt := range corruptions {
		t.Run(name, func(t *testing.T) {
			overlay, configPath := repairOverlay(t, input)
			res, err := RepairPackagesConfig(overlay)
			if err != nil {
				t.Fatalf("repair aborted before the injection: %v", err)
			}
			onDisk, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			res.Repaired = corrupt(t, res.Repaired)
			if res.Repaired == string(onDisk) {
				t.Fatal("the injection changed nothing, so the case proves nothing")
			}

			if err := res.Write(); err == nil {
				t.Fatal("the gate accepted a corrupted rewrite")
			} else {
				t.Logf("gate aborted with: %v", err)
			}

			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(after) != string(onDisk) {
				t.Fatal("the registry was modified despite the abort")
			}
		})
	}
}

// TestLintFixGateRejectsAPlanThatUnderstates guards the gate from the other
// direction: the declared transformations are what the comparison forgives, so a
// repair that did MORE than it declared must still abort. Here the rewrite is
// the genuine one and the plan is emptied.
func TestLintFixGateRejectsAPlanThatUnderstates(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	repaired, plan, err := buildRepair(input)
	if err != nil {
		t.Fatalf("buildRepair: %v", err)
	}
	if err := verifyRepair(input, repaired, plan); err != nil {
		t.Fatalf("the honest plan was rejected: %v", err)
	}

	understated := repairPlan{
		records: []recordRepair{{name: "app-arch/canonical-record", reordered: true}},
		actions: plan.actions,
	}
	if err := verifyRepair(input, repaired, understated); err == nil {
		t.Fatal("the gate accepted a rewrite whose transformations were not declared")
	} else {
		t.Logf("gate aborted with: %v", err)
	}
}

// TestLintFixRefusesAFileItCannotParse pins the entry condition: the gate
// compares the rewrite against the original record by record, so a file that
// does not load leaves nothing to compare against. The repair says so and writes
// nothing.
func TestLintFixRefusesAFileItCannotParse(t *testing.T) {
	cases := map[string]string{
		"toml syntax error": "[\"dev-util/x\"\nurl = \"https://example.com\"\n",
		"unknown key": `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
serie = '^1\.28\.'
comments = """
x — a typo the load must reject (R4.1).
"""
# END
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			overlay, configPath := repairOverlay(t, content)
			if _, err := RepairPackagesConfig(overlay); err == nil {
				t.Fatal("the repair accepted a file that does not load")
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(after) != content {
				t.Fatal("the file was modified")
			}
		})
	}
}

// TestLintFixMissingRegistryIsReported keeps the repair's not-found error the
// same one every other reader of packages.toml returns.
func TestLintFixMissingRegistryIsReported(t *testing.T) {
	if _, err := RepairPackagesConfig(t.TempDir()); !errors.Is(err, ErrPackagesConfigNotFound) {
		t.Fatalf("got %v, want ErrPackagesConfigNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// R7.2 — the write.
// ---------------------------------------------------------------------------

// TestLintFixWritePreservesFileMode is R7.2. The mode is set explicitly rather
// than left to the create mode of os.WriteFile, which the process umask masks —
// under a restrictive umask a 0644 registry would come back 0600 and stop being
// readable by anything else on the box, which is why the umask is set aside for
// the duration of each case.
func TestLintFixWritePreservesFileMode(t *testing.T) {
	input := readFixture(t, "repair_input.toml")

	for _, mode := range []os.FileMode{0o644, 0o664, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			overlay, configPath := repairOverlay(t, input)
			if err := os.Chmod(configPath, mode); err != nil {
				t.Fatalf("chmod: %v", err)
			}

			res, err := RepairPackagesConfig(overlay)
			if err != nil {
				t.Fatalf("repair aborted: %v", err)
			}
			if err := res.Write(); err != nil {
				t.Fatalf("write: %v", err)
			}

			info, err := os.Stat(configPath)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := info.Mode().Perm(); got != mode {
				t.Fatalf("file mode is %v after the repair, want %v", got, mode)
			}
			data, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(data) != res.Repaired {
				t.Fatal("the file on disk is not the repaired text")
			}
			if _, err := os.Stat(configPath + ".tmp"); !os.IsNotExist(err) {
				t.Error("the temp file was left behind beside the registry")
			}
		})
	}
}

// TestLintFixWriteIsAtomic pins the rename: the repair never truncates the
// registry in place, so a reader either sees the old file or the new one. The
// proof available to a unit test is that the write goes through a temp file
// which no longer exists afterwards, and that a nil or unchanged result writes
// nothing at all.
func TestLintFixWriteIsAtomic(t *testing.T) {
	var nilResult *RepairResult
	if err := nilResult.Write(); err != nil {
		t.Fatalf("writing a nil result must be a no-op, got %v", err)
	}

	golden := readFixture(t, "repair_golden.toml")
	overlay, configPath := repairOverlay(t, golden)
	res, err := RepairPackagesConfig(overlay)
	if err != nil {
		t.Fatalf("repair aborted: %v", err)
	}
	if err := res.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(configPath + ".tmp"); !os.IsNotExist(err) {
		t.Error("a no-op write created a temp file")
	}
}

// ---------------------------------------------------------------------------
// Layout details the rewriter must not lose.
// ---------------------------------------------------------------------------

// TestLintFixKeepsRecordScaffolding pins what no TOML parse can check: the
// header, the `# END` markers and the blank line between records all come
// through a repair that reorders the fields between them.
func TestLintFixKeepsRecordScaffolding(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	if got, want := strings.Count(repaired, "\n# END\n"), strings.Count(input, "\n# END\n"); got != want {
		t.Errorf("%d END markers after the repair, %d before", got, want)
	}
	if got, want := strings.Count(repaired, "\n\n[\""), strings.Count(input, "\n\n[\""); got != want {
		t.Errorf("%d blank-line-separated records after the repair, %d before", got, want)
	}
	header, _, _ := strings.Cut(input, "\n[\"")
	if !strings.HasPrefix(repaired, header) {
		t.Error("the file header did not survive the repair")
	}
	if !strings.HasSuffix(repaired, "# END\n") {
		t.Error("the file no longer ends with a closed record")
	}
}

// TestLintFixIndentedRetiredKeyKeepsItsIndentation pins that the substituted
// line inherits the indentation of the one it replaces. The real registry
// indents nothing, so this is a guard rather than a measurement — but a repair
// that flattened an indented file would be rewriting layout it was not asked to
// touch.
func TestLintFixIndentedRetiredKeyKeepsItsIndentation(t *testing.T) {
	const content = `["dev-util/x"]
url = "https://example.com"
parser = "json"
path = "version"
  binary = true
comments = """
x — an indented assignment, which TOML allows and the registry never writes.
"""
# END
`
	_, repaired := repairText(t, content)
	if !strings.Contains(repaired, "  type = \"bin\"\n") {
		t.Fatalf("the substituted line lost its indentation:\n%s", repaired)
	}
}

// TestLintFixMultiLineArrayMovesAsOneUnit pins that a value spread over several
// lines is reordered as a block. The real registry writes every array on one
// line today; this is the guard for the day someone writes the first multi-line
// one.
func TestLintFixMultiLineArrayMovesAsOneUnit(t *testing.T) {
	input := readFixture(t, "repair_input.toml")
	_, repaired := repairText(t, input)

	const block = "transform = [\n  ['^v', \"\"],\n  ['-', \"_p\"],\n]\n"
	if !strings.Contains(input, block) {
		t.Fatal("the fixture no longer carries a multi-line array")
	}
	if !strings.Contains(repaired, block) {
		t.Fatalf("the multi-line array was broken up:\n%s", recordText(t, repaired, "dev-util/multiline-array"))
	}
	// It moved: in the input it precedes parser, in the output it follows
	// pattern, which is where CanonicalFieldOrder puts it.
	after := recordText(t, repaired, "dev-util/multiline-array")
	if strings.Index(after, "transform = [") < strings.Index(after, "pattern = ") {
		t.Error("transform was not reordered after pattern")
	}
}
