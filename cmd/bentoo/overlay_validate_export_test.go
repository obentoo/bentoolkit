package main

// Authored for story 046, sub-task 8.1 — R4.3.
//
// Written from the contract: R4.3 — "WHEN `overlay validate --json` is used,
// THE SYSTEM SHALL produce a document of THE SAME SHAPE --export produces for a
// .json path" — and design.md D8, which makes --json an alias for --export at
// stdout rather than a second format with a schema of its own.
//
// Story.md's Success Metrics state the point in one line: export formats that
// cannot read each other, 2 → 1.
//
// # What this story does and does not move
//
// Out of Scope is explicit: "overlay validate's report content. This story
// moves the meaning of its --json flag and RESERVES ITS KIND; the report itself
// migrates in 047." So nothing here asserts what the payload contains — only
// that the document is the envelope, that it names the validate kind, and that
// a failed write costs no exit status.
//
// The name assumed: report.KindOverlayValidate, the kind being reserved.
//
// Red on arrival: --json writes validate.Report.Normalized() at the document
// root, in the schema no other command shares.
//
// stubValidateRunner comes from overlay_validate_test.go and mixedReport from
// overlay_validate_render_test.go — both this package's own. The runner is a
// package variable rather than a flag, so it survives the harness rebuilding
// the command tree for each run.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// validateDocumentKeys is one document's top-level shape, sorted.
func validateDocumentKeys(t *testing.T, source string, body []byte) []string {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the %s document is not valid JSON: %v\n%s", source, err, body)
	}

	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestValidateExportJSONFlagProducesTheEnvelope pins the half a consumer meets
// first: the document `--json` writes to stdout is the envelope, root and all.
func TestValidateExportJSONFlagProducesTheEnvelope(t *testing.T) {
	c := newTestCLI(t)
	stubValidateRunner(c.deps, mixedReport())

	stdout, stderr, _ := c.Run("overlay", "validate", "--json")
	if strings.Contains(stderr, "unknown flag") {
		t.Fatalf("--json was rejected: %s", stderr)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("`overlay validate --json` did not write one JSON document: %v\n%s", err, stdout)
	}

	if got, ok := doc["schema"].(float64); !ok || int(got) != 2 {
		t.Errorf(`root["schema"] = %v, want 2 — --json still writes the schema no other command shares (D8)`, doc["schema"])
	}
	if got, _ := doc["kind"].(string); got != string(report.KindOverlayValidate) {
		t.Errorf(`root["kind"] = %q, want %q`, got, report.KindOverlayValidate)
	}
	if _, ok := doc["payload"]; !ok {
		t.Errorf(`the document has no "payload" — the validate report is still at the root, so a consumer cannot read it the way it reads every other export (R4.3)`)
	}
}

// TestValidateExportMatchesTheJSONFlagShape is R4.3 itself. Two ways of asking
// for the same document must not produce two documents.
//
// The comparison is on SHAPE, not on bytes: the two runs are two runs, and a
// timestamp or an ordering that differed between them would make a byte
// comparison fail for a reason that has nothing to do with the requirement.
func TestValidateExportMatchesTheJSONFlagShape(t *testing.T) {
	fromFlag := newTestCLI(t)
	stubValidateRunner(fromFlag.deps, mixedReport())
	stdout, _, flagCode := fromFlag.Run("overlay", "validate", "--json")

	toFile := newTestCLI(t)
	stubValidateRunner(toFile.deps, mixedReport())
	path := filepath.Join(t.TempDir(), "validate.json")
	_, _, exportCode := toFile.Run("overlay", "validate", "--export="+path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("`--export=%s` wrote nothing: %v", path, err)
	}

	flagKeys := validateDocumentKeys(t, "--json", []byte(stdout))
	fileKeys := validateDocumentKeys(t, "--export", body)

	if got, want := strings.Join(flagKeys, ","), strings.Join(fileKeys, ","); got != want {
		t.Errorf("--json and --export produce different documents (R4.3)\n  --json:   %s\n  --export: %s", got, want)
	}
	if flagCode != exportCode {
		t.Errorf("the two ways of exporting the same run exited differently: --json %d, --export %d", flagCode, exportCode)
	}
}

// TestValidateExportWriteFailurePreservesTheExitStatus pins R3.5 on this path
// too. An export is an additional copy; failing to write it must not change
// what the run DECIDED, because the exit status is what a CI job branches on.
func TestValidateExportWriteFailurePreservesTheExitStatus(t *testing.T) {
	readOnly := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("creating the read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	plain := newTestCLI(t)
	stubValidateRunner(plain.deps, mixedReport())
	plainOut, _, plainCode := plain.Run("overlay", "validate")

	failing := newTestCLI(t)
	stubValidateRunner(failing.deps, mixedReport())
	unwritable := filepath.Join(readOnly, "validate.json")
	failedOut, failedStderr, failedCode := failing.Run("overlay", "validate", "--export="+unwritable)

	if failedCode != plainCode {
		t.Errorf("an unwritable export changed the exit status: %d with --export, %d without (R3.5)", failedCode, plainCode)
	}
	if strings.TrimSpace(failedOut) == "" && strings.TrimSpace(plainOut) != "" {
		t.Error("an unwritable export cost the operator the terminal render as well as the file (R3.5)")
	}
	if strings.TrimSpace(failedStderr) == "" {
		t.Error("the failed write was not reported at all — a copy that silently did not happen is worse than one that failed loudly (R3.5)")
	}
	if _, err := os.Stat(unwritable); err == nil {
		t.Errorf("%s was written after all — the fixture did not make the path unwritable, so nothing above was tested", unwritable)
	}
}

// --------------------------------------------------- story 046, sub-task 10.1
//
// Authored for R2.3 — "WHEN a report is rendered in any mode, THE SYSTEM SHALL
// state what it omitted and why, rather than omitting it silently."
//
// # What the cases below are about
//
// The three cases above settled where `overlay validate`'s export GOES. These
// settle what it SAYS in the two syntaxes that read the report through its
// sections. `overlay validate --export=report.md` and `--export=report.txt`
// write a file of zero bytes: the Markdown and plain renderers draw sections,
// the validate payload has none yet, and an empty file is the result. Zero bytes
// is the silent omission R2.3 names — the operator asked for a report, got a
// file, and nothing anywhere says the two are not the same thing.
//
// # This is the DECLARATION of an absence and not the content
//
// The story's Out of Scope is explicit: "overlay validate's report content ...
// migrates in 047". So nothing here asks the export to carry a finding, a gate
// or a package. What it asks is that the file SAY SO — name whose content is
// absent and why — which is a fact about this story's own split and is knowable
// today, unlike the content itself.
//
// Nothing here pins the sentence either. R2.3 asks for two things to be stated,
// what and why; the wording is the implementer's, so omissionGaps checks the two
// ASPECTS against several spellings each. A test that pinned the sentence would
// be a copy of the implementation rather than a reading of the requirement.
//
// stubValidateRunner comes from overlay_validate_test.go and mixedReport from
// overlay_validate_render_test.go, as above.
//
// Red on arrival: both files are zero bytes, measured — validatePayload.Sections
// returns nil (overlay_validate_report.go:99).

// containsAny reports whether text carries any one of the spellings.
func containsAny(text string, spellings []string) bool {
	for _, s := range spellings {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// omissionGaps names what a rendered export still fails to state, and returns
// nothing when the declaration is complete.
//
// The three aspects are R2.3 read literally: an omission stated is one whose
// SUBJECT, ABSENCE and REASON a reader can find. Each is satisfied by any one of
// several spellings, because which word an implementer reaches for is not a
// requirement and a test that made it one would fail on a rewrite that changed
// nothing an operator can see.
func omissionGaps(body string) []string {
	text := strings.ToLower(body)

	aspects := []struct {
		missing   string
		spellings []string
	}{
		{
			"never names WHOSE content is absent — a file that declares an omission without naming it leaves the reader guessing which report they are not holding",
			[]string{"validat"},
		},
		{
			"never states that content is ABSENT — R2.3's \"what it omitted\"",
			[]string{"omit", "not included", "not here", "not yet", "no content", "nothing", "absent", "left out", "missing", "empty"},
		},
		{
			"never states WHY it is absent — R2.3's \"and why\", which is the half that turns an apology into an answer",
			[]string{"047", "future", "later", "migrat", "another story", "subsequent", "next story", "not yet"},
		},
	}

	var gaps []string
	for _, a := range aspects {
		if !containsAny(text, a.spellings) {
			gaps = append(gaps, a.missing)
		}
	}
	return gaps
}

// exportValidateTo runs one `overlay validate --export=<path>` over rep and
// returns what landed in the file.
//
// It reads the file back rather than trusting the exit status, because the
// defect this sub-task closes is invisible from the status: the run succeeds,
// the write succeeds, and the file is empty.
func exportValidateTo(t *testing.T, rep validate.Report, name string) string {
	t.Helper()

	c := newTestCLI(t)
	stubValidateRunner(c.deps, rep)
	path := filepath.Join(t.TempDir(), name)

	_, stderr, _ := c.Run("overlay", "validate", "--export="+path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("`--export=%s` wrote no file at all: %v (stderr: %q)", path, err, stderr)
	}
	if strings.TrimSpace(string(body)) == "" {
		t.Fatalf("`--export=%s` wrote %d byte(s) of nothing — the operator asked for a report and received a file that does not say it is not one (R2.3)", path, len(body))
	}
	return string(body)
}

// TestValidateExportMarkdownAndPlainDeclareTheOmission is R2.3 on the two
// section-consuming syntaxes: each writes a file, and each file says what it
// does not carry and why.
func TestValidateExportMarkdownAndPlainDeclareTheOmission(t *testing.T) {
	for _, ext := range []string{".md", ".txt"} {
		t.Run(ext, func(t *testing.T) {
			body := exportValidateTo(t, mixedReport(), "validate"+ext)

			for _, gap := range omissionGaps(body) {
				t.Errorf("the %s export %s\n--- what it wrote ---\n%s", ext, gap, body)
			}
		})
	}
}

// TestValidateExportDeclaresTheOmissionWhateverTheRunFound is the hostile half.
//
// The declaration is a fact about THIS STORY'S SPLIT — the content migrates in
// 047 — and not about what any run found, so it is owed to every run equally. A
// declaration derived from the payload instead would be the same defect back
// again for the run least able to survive it: a clean overlay, where the report
// has nothing to list, is exactly the case an implementation keyed on "is there
// something to say" leaves at zero bytes, and it is also the case where an empty
// file is most easily mistaken for a clean result.
//
// The extension-less path is the third reader of the same renderer. `.txt` is
// not a case in exportFormatFor — plain is what EVERY extension that is not .md
// or .json selects — so a declaration that arrived through an extension the
// implementation happened to name would answer `report.txt` and leave `report`
// silent.
func TestValidateExportDeclaresTheOmissionWhateverTheRunFound(t *testing.T) {
	clean := validate.Report{Overlay: "/var/db/repos/bentoo"}

	markdown := exportValidateTo(t, clean, "validate.md")
	for _, gap := range omissionGaps(markdown) {
		t.Errorf("a run that found nothing exported a .md that %s\n--- what it wrote ---\n%s", gap, markdown)
	}

	noExtension := exportValidateTo(t, clean, "validate")
	for _, gap := range omissionGaps(noExtension) {
		t.Errorf("a run that found nothing exported an extension-less path that %s\n--- what it wrote ---\n%s", gap, noExtension)
	}
}

// validateExportJSONBeforeTheDeclaration is the document
// `overlay validate --export=<path>.json` wrote over mixedReport BEFORE this
// sub-task, captured from a run of the shipped code.
//
// It is recorded verbatim rather than described, because the promise being kept
// is byte-identity and a description of a document is not one.
const validateExportJSONBeforeTheDeclaration = `{
  "schema": 2,
  "kind": "overlay.validate",
  "title": "Overlay validation",
  "complete": true,
  "not_evaluated": 0,
  "payload": {
    "overlay": "/var/db/repos/bentoo",
    "results": [
      {
        "package": "media-plugins/gst-plugins-qt6",
        "version": "1.29.2",
        "depth": "options",
        "depth_requested": "options",
        "gates": [
          {
            "gate": "options",
            "outcome": "FAILED",
            "findings": [
              {
                "gate": "options",
                "severity": "error",
                "detail": "-Daalib= is passed but upstream 1.29.2 declares no such option"
              }
            ]
          },
          {
            "gate": "qa",
            "outcome": "PASS"
          }
        ],
        "sources": [
          "gst-plugins-good-1.29.2/meson.options"
        ]
      },
      {
        "package": "media-plugins/gst-plugins-qt6",
        "version": "1.28.6",
        "depth": "options",
        "depth_requested": "options",
        "gates": [
          {
            "gate": "options",
            "outcome": "PASS"
          },
          {
            "gate": "qa",
            "outcome": "SKIPPED",
            "reason": "pkgcheck was not found on PATH"
          }
        ],
        "sources": [
          "gst-plugins-good-1.28.6/meson.options"
        ]
      },
      {
        "package": "dev-libs/cmakeproj",
        "version": "1.0",
        "depth": "options",
        "depth_requested": "configure",
        "depth_reason": "the option gate could not read the archive, so nothing deeper could run",
        "gates": [
          {
            "gate": "options",
            "outcome": "SKIPPED",
            "reason": "build system is not Meson: cmake"
          },
          {
            "gate": "configure",
            "outcome": "SKIPPED",
            "reason": "the staged tree could not be prepared: permission denied"
          }
        ],
        "sources": []
      }
    ]
  }
}
`

// TestValidateExportJSONIsUnchangedByTheDeclaration is the promise the other two
// cases would otherwise be free to break.
//
// render.JSON serializes the whole report.Run and reaches the payload's fields
// through encoding/json — it never calls Sections. So giving the payload a
// section to declare its own absence must reach the .md and .txt files and no
// other, and the consumer that has just been asked, once, to add a `.payload`
// hop must not find a second change riding along with it.
//
// This case is expected to be GREEN before the sub-task and green after: it
// measures what must not move, and a green here only means something because the
// two cases above are red.
func TestValidateExportJSONIsUnchangedByTheDeclaration(t *testing.T) {
	c := newTestCLI(t)
	stubValidateRunner(c.deps, mixedReport())
	path := filepath.Join(t.TempDir(), "validate.json")

	_, stderr, _ := c.Run("overlay", "validate", "--export="+path)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("`--export=%s` wrote nothing: %v (stderr: %q)", path, err, stderr)
	}

	if got, want := string(body), validateExportJSONBeforeTheDeclaration; got != want {
		t.Errorf("the .json export changed (%d bytes, was %d) — declaring the omission was supposed to reach the section-consuming syntaxes and no other\n%s", len(got), len(want), firstDifference(got, want))
	}
}

// firstDifference names the line where two documents part company, so a failure
// above points at a change rather than at two thousand bytes.
func firstDifference(got, want string) string {
	gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")

	for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
		if gotLines[i] != wantLines[i] {
			return fmt.Sprintf("  line %d:\n    got:  %q\n    want: %q", i+1, gotLines[i], wantLines[i])
		}
	}
	if len(gotLines) != len(wantLines) {
		return fmt.Sprintf("  the documents agree for %d line(s); one then has %d and the other %d", min(len(gotLines), len(wantLines)), len(gotLines), len(wantLines))
	}
	return "  the documents differ in a way lines cannot show"
}
