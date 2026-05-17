package autoupdate

import (
	"bytes"
	"errors"
	"testing"
)

// someType is a minimal payload type used to verify BatchResult is generic.
type someType struct {
	Name string
}

// TestBatchResult_FieldsExported verifies that the Items and Failures fields
// are exported and that BatchResult can be constructed for an arbitrary type.
func TestBatchResult_FieldsExported(t *testing.T) {
	br := BatchResult[someType]{
		Items: []someType{{Name: "a"}, {Name: "b"}},
		Failures: map[string]error{
			"cat/pkg": errors.New("boom"),
		},
	}

	if len(br.Items) != 2 {
		t.Errorf("Items: got len %d, want 2", len(br.Items))
	}
	if len(br.Failures) != 1 {
		t.Errorf("Failures: got len %d, want 1", len(br.Failures))
	}
	if br.Items[0].Name != "a" {
		t.Errorf("Items[0].Name: got %q, want %q", br.Items[0].Name, "a")
	}
}

func TestBatchResult_ExitCode_AllOk(t *testing.T) {
	br := BatchResult[someType]{
		Items:    []someType{{Name: "a"}},
		Failures: nil,
	}
	if got := br.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0", got)
	}
}

func TestBatchResult_ExitCode_Partial(t *testing.T) {
	br := BatchResult[someType]{
		Items: []someType{{Name: "a"}},
		Failures: map[string]error{
			"cat/pkg": errors.New("boom"),
		},
	}
	if got := br.ExitCode(); got != 1 {
		t.Errorf("ExitCode: got %d, want 1", got)
	}
}

func TestBatchResult_ExitCode_TotalFail(t *testing.T) {
	br := BatchResult[someType]{
		Items: nil,
		Failures: map[string]error{
			"cat/pkg":  errors.New("boom"),
			"cat/pkg2": errors.New("bang"),
		},
	}
	if got := br.ExitCode(); got != 2 {
		t.Errorf("ExitCode: got %d, want 2", got)
	}
}

func TestBatchResult_ExitCode_Empty(t *testing.T) {
	// No items and no failures must be treated as success.
	br := BatchResult[someType]{}
	if got := br.ExitCode(); got != 0 {
		t.Errorf("ExitCode: got %d, want 0", got)
	}
}

func TestBatchResult_HasFailures(t *testing.T) {
	withFailures := BatchResult[someType]{
		Failures: map[string]error{"cat/pkg": errors.New("boom")},
	}
	if !withFailures.HasFailures() {
		t.Error("HasFailures: got false, want true")
	}

	withoutFailures := BatchResult[someType]{
		Items: []someType{{Name: "a"}},
	}
	if withoutFailures.HasFailures() {
		t.Error("HasFailures: got true, want false")
	}
}

func TestBatchResult_FormatFailures_SortedDeterministic(t *testing.T) {
	// Insert failures out of lexical order; output must be sorted.
	br := BatchResult[someType]{
		Failures: map[string]error{
			"cat/zebra": errors.New("z error"),
			"cat/apple": errors.New("a error"),
			"cat/mango": errors.New("m error"),
		},
	}

	var buf bytes.Buffer
	br.FormatFailures(&buf)

	want := "ERROR cat/apple: a error\n" +
		"ERROR cat/mango: m error\n" +
		"ERROR cat/zebra: z error\n"
	if got := buf.String(); got != want {
		t.Errorf("FormatFailures output mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestBatchResult_FormatFailures_MultilineErrors(t *testing.T) {
	br := BatchResult[someType]{
		Failures: map[string]error{
			"cat/pkg": errors.New("line one\nline two\nline three"),
		},
	}

	var buf bytes.Buffer
	br.FormatFailures(&buf)

	// Each embedded newline gets a two-space continuation indent; the
	// record itself is terminated with a single trailing newline.
	want := "ERROR cat/pkg: line one\n  line two\n  line three\n"
	if got := buf.String(); got != want {
		t.Errorf("FormatFailures multiline mismatch:\ngot:\n%q\nwant:\n%q", got, want)
	}
}
