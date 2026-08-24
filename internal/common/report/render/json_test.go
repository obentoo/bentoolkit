package render

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/report"
)

// TestJSONRoundTrip pins R9.4: a machine reader sees the fields the renderers
// saw. Round-tripping the fixture and comparing it whole is the only assertion
// that stays true as the model grows — a field-by-field list would be checked
// against the fields somebody remembered to add to it.
func TestJSONRoundTrip(t *testing.T) {
	want := fixtureReport()

	var buf bytes.Buffer
	if err := JSON(&buf, want); err != nil {
		t.Fatalf("JSON returned an error: %v", err)
	}

	var got report.Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("the document JSON produced is not valid JSON: %v\n%s", err, buf.String())
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("the report did not survive the round trip\n--- want ---\n%+v\n--- got ---\n%+v", want, got)
	}
}

// TestJSONRoundTripKeepsTheReasonWhole is R9.3 for the JSON path. The screen
// shows 96 cells; the record holds all 232.
func TestJSONRoundTripKeepsTheReasonWhole(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, fixtureReport()); err != nil {
		t.Fatalf("JSON returned an error: %v", err)
	}

	var got report.Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, entry := range got.Plan {
		if entry.Package == "sys-apps/portage" && entry.Reason != planReason {
			t.Errorf("the exported reason is %d characters, want %d — the export shortened it", len(entry.Reason), len(planReason))
		}
	}
}

// TestJSONNamesTheFourTallyCounts pins R5.1 at the machine surface. A consumer
// that cannot find "inconclusive" cannot tell a toolkit limitation from the
// operator's policy, which is the whole distinction this story adds.
func TestJSONNamesTheFourTallyCounts(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, fixtureReport()); err != nil {
		t.Fatalf("JSON returned an error: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	tally, ok := doc[jsonKeyFor(t, report.Report{}, "Tally")].(map[string]any)
	if !ok {
		t.Fatalf("the document has no tally object\n%s", buf.String())
	}

	for _, field := range []string{"Proved", "Errored", "Inconclusive", "Skipped"} {
		key := jsonKeyFor(t, report.Tally{}, field)
		if _, present := tally[key]; !present {
			t.Errorf("the tally object has no %q key", key)
		}
	}
}

// TestJSONDropsNoField is the assertion a round trip cannot make. A round trip
// survives `omitempty` intact — the zero value goes out as an absent key and
// comes back as the zero value, and nothing notices. But a consumer reading
// that document cannot tell "false" from "the producer did not say", which is
// the same conflation this story exists to remove from the tally.
//
// So: every exported field of every model type must appear as a key, even when
// its value is the zero one.
func TestJSONDropsNoField(t *testing.T) {
	// A report whose every field is the zero value — the case omitempty eats.
	var buf bytes.Buffer
	empty := report.Report{
		Scanned: []report.PackageResult{{}},
		Plan:    []report.PlanEntry{{}},
		Results: []report.ValidationRow{{}},
	}
	if err := JSON(&buf, empty); err != nil {
		t.Fatalf("JSON returned an error: %v", err)
	}
	doc := buf.String()

	types := []struct {
		name string
		v    any
	}{
		{"Report", report.Report{}},
		{"PackageResult", report.PackageResult{}},
		{"PlanEntry", report.PlanEntry{}},
		{"ValidationRow", report.ValidationRow{}},
		{"Tally", report.Tally{}},
	}

	for _, typ := range types {
		rt := reflect.TypeOf(typ.v)
		for i := range rt.NumField() {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			key := jsonKeyFor(t, typ.v, field.Name)
			if !strings.Contains(doc, `"`+key+`"`) {
				t.Errorf("%s.%s is absent from the document as key %q — omitempty makes a zero value indistinguishable from an unanswered one",
					typ.name, field.Name, key)
			}
		}
	}
}

// jsonKeyFor answers what key a field serializes under, reading the struct tag
// rather than assuming a convention. If the model carries no tags, the Go name
// IS the wire contract, and this returns it.
func jsonKeyFor(t *testing.T, v any, fieldName string) string {
	t.Helper()

	field, ok := reflect.TypeOf(v).FieldByName(fieldName)
	if !ok {
		t.Fatalf("%T has no field %s — the fixture and the model have diverged", v, fieldName)
	}

	tag := field.Tag.Get("json")
	if tag == "" {
		return fieldName
	}
	if name, _, _ := strings.Cut(tag, ","); name != "" && name != "-" {
		return name
	}
	return fieldName
}
