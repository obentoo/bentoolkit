package validate

// Authored for story 031, sub-task 5.1 — R6, R6.3.
//
// This file is deliberately SHALLOW about pkgcheck's JSON schema, and that is
// the point. design.md D7 records that the `JsonStream` reporter exists
// (confirmed against `pkgcheck show --reporters`) but that no record was ever
// OBSERVED — scans of media-plugins/gst-plugins-qt6, dev-python/gst-python and
// the whole media-plugins category all returned empty with exit 0. The field
// carrying the level is therefore an assumption.
//
// So the assertions below run against a COMMITTED FIXTURE captured from a real
// scan, not against a schema written from memory. Sub-task 5.1 must capture
// that record first; until it exists this test fails on the fixture, which is
// the correct failure — it says "you have not looked yet".
//
// This file pins `decodePkgcheckRecord(line []byte) (Finding, error)`.
//
// Red is DEFERRED to Run mode: the package does not exist yet.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pkgcheckFixture = "pkgcheck-jsonstream-record.jsonl"

// TestDecodePkgcheckRecord_AgainstCapturedFixture is the whole reason 5.1 is a
// separate sub-task: the decode struct must be designed against an observed
// record, never against a guessed one.
func TestDecodePkgcheckRecord_AgainstCapturedFixture(t *testing.T) {
	path := filepath.Join("testdata", pkgcheckFixture)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("fixture %s is missing: %v\n"+
			"Capture one real record first:\n"+
			"  pkgcheck scan -R JsonStream <a package that actually reports findings>\n"+
			"Designing the struct without looking is the assumption D7 exists to remove.", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })

	scanner := bufio.NewScanner(f)
	var decoded int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		got, err := decodePkgcheckRecord([]byte(line))
		if err != nil {
			t.Fatalf("decoding captured record %q: %v", line, err)
		}
		decoded++

		if got.Gate != "qa" {
			t.Errorf("Gate: got %q, want \"qa\" — Report.ExitCode selects on this field to keep QA findings out of the exit code", got.Gate)
		}
		if got.Detail == "" {
			t.Error("Detail is empty; a finding with no text tells the operator nothing")
		}
		switch got.Severity {
		case "error", "warning", "info":
		default:
			t.Errorf("Severity: got %q, want one of error/warning/info — if pkgcheck carries no level, map every record to info and say so in the report rather than inventing one", got.Severity)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if decoded == 0 {
		t.Fatalf("fixture %s held no records; capture a scan that actually reports findings", path)
	}
}

// firstFixtureLine returns the first non-empty record of the captured fixture,
// so pkgcheck_test.go can feed the seam a line of the REAL shape instead of one
// invented from memory. It fails the test rather than returning a placeholder:
// a stubbed stream that does not match reality tests nothing.
func firstFixtureLine(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", pkgcheckFixture)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s is missing: %v — capture one real record first (see sub-task 5.1)", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	t.Fatalf("fixture %s held no records", path)
	return ""
}

// TestDecodePkgcheckRecord_UnparseableLineIsReported pins that a line the
// decoder cannot read becomes an error the caller turns into a named SKIPPED —
// never a silently dropped record, which would under-report QA and read as
// clean.
func TestDecodePkgcheckRecord_UnparseableLineIsReported(t *testing.T) {
	_, err := decodePkgcheckRecord([]byte("this is not json"))

	if err == nil {
		t.Fatal("decoding a malformed line returned no error; a dropped record makes a noisy package look clean")
	}
}
