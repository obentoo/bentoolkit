package registry

import (
	"reflect"
	"strings"
	"testing"
)

// Story 087, sub-task 3.3: the record lint reads what was written — a trailing
// inline comment after a track or type value (R5.1), an empty base_from
// (R5.2), a non-boolean binary (R5.3) — while the binary = true / false
// findings stay as they are (R10.5). Observed through LintRecordModel, the
// text scan `bentoo overlay autoupdate --lint` runs.

const s087LintPkg = "app-misc/foo"

const s087LegacyBaseMsg = `track = "commit" without base_from: the base version is whatever the ebuild already carries and can freeze there unnoticed; declare base_from = "file", "tag" or "commit_message" — or "none" when upstream publishes no version at all`

// s087LintRecord wraps body lines in a closed record; the first body line is
// line 2 of the file.
func s087LintRecord(body ...string) string {
	return `["` + s087LintPkg + `"]
` + strings.Join(body, "\n") + `
comments = """
foo — a test record.
"""
# END
`
}

func s087IssuesOfRule(issues []LintIssue, rule string) []LintIssue {
	var out []LintIssue
	for _, iss := range issues {
		if iss.Rule == rule {
			out = append(out, iss)
		}
	}
	return out
}

// TestS087_3_3_LegacyBaseReadsCommentedAndEmptyValues covers R5.1 and R5.2.
// Each case says whether the record is commit-tracked with no declared base,
// in which case exactly the legacy-base finding on the track line is due.
func TestS087_3_3_LegacyBaseReadsCommentedAndEmptyValues(t *testing.T) {
	tests := []struct {
		name  string
		body  []string
		fires bool
	}{
		// Hostile, wrongly collapsed: values that only LOOK like commit, or a
		// declared base_from that only looks empty, must stay apart.
		{"the comment is inside the quotes", []string{`track = "commit # pinned"`, `url = "https://example.com"`}, false},
		{"commented Commit is not commit", []string{`track = "Commit" # pinned`, `url = "https://example.com"`}, false},
		{"commented base_from none is declared", []string{`track = "commit"`, `url = "https://example.com"`, `base_from = "none" # no upstream version`}, false},
		{"commented base_from file is declared", []string{`track = "commit" # pinned`, `url = "https://example.com"`, `base_from = "file" # from the tarball`}, false},
		// Hostile, wrongly split: differently written values that mean commit
		// with no base must all fire.
		{"commented track commit", []string{`track = "commit" # pinned`, `url = "https://example.com"`}, true},
		{"commented track commit without a space", []string{`track = "commit"#pinned`, `url = "https://example.com"`}, true},
		{"commented literal track commit", []string{`track = 'commit' # pinned`, `url = "https://example.com"`}, true},
		{"empty base_from", []string{`track = "commit"`, `url = "https://example.com"`, `base_from = ""`}, true},
		{"empty literal base_from", []string{`track = "commit"`, `url = "https://example.com"`, `base_from = ''`}, true},
		{"commented empty base_from", []string{`track = "commit" # pinned`, `url = "https://example.com"`, `base_from = "" # unset`}, true},
		// Benign: plain spellings as today.
		{"plain track commit", []string{`track = "commit"`, `url = "https://example.com"`}, true},
		{"plain base_from none", []string{`track = "commit"`, `url = "https://example.com"`, `base_from = "none"`}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := s087IssuesOfRule(LintRecordModel(s087LintRecord(tc.body...)), LintLegacyBase)
			var want []LintIssue
			if tc.fires {
				want = []LintIssue{{Line: 2, Package: s087LintPkg, Rule: LintLegacyBase, Fix: FixNone, Message: s087LegacyBaseMsg}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("legacy-base findings for %q\n got: %#v\nwant: %#v", tc.body, got, want)
			}
		})
	}
}

// TestS087_3_3_TypeReadsCommentedValue covers R5.1 for type: the dropped
// binary line names the type the record declares even when a comment follows.
func TestS087_3_3_TypeReadsCommentedValue(t *testing.T) {
	tests := []struct {
		name    string
		typeRaw string
		want    string
	}{
		{"commented type", `type = "bin" # prebuilt`, `binary is retired: the record already declares type = "bin", so the line is deleted`},
		{"commented literal type", `type = 'source' # from git`, `binary is retired: the record already declares type = "source", so the line is deleted`},
		{"comment inside the quotes is part of the value", `type = "bin # prebuilt"`, `binary is retired: the record already declares type = "bin # prebuilt", so the line is deleted`},
		{"plain type", `type = "bin"`, `binary is retired: the record already declares type = "bin", so the line is deleted`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := s087IssuesOfRule(LintRecordModel(s087LintRecord(`url = "https://example.com"`, tc.typeRaw, `binary = true`)), LintLegacyBinary)
			want := []LintIssue{{Line: 4, Package: s087LintPkg, Rule: LintLegacyBinary, Fix: FixDropBinary, Message: tc.want}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("legacy-binary findings with %s\n got: %#v\nwant: %#v", tc.typeRaw, got, want)
			}
		})
	}
}

// TestS087_3_3_NonBooleanBinaryIsNamed covers R5.3: a binary that is not a
// TOML boolean gets a finding that quotes the value and says it is not a
// boolean. "true" as a string is the near-identical hostile case.
func TestS087_3_3_NonBooleanBinaryIsNamed(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		quoted []string // any one of these quotings of the value is accepted
	}{
		{"string yes", `"yes"`, []string{`"yes"`}},
		{"string true", `"true"`, []string{`"true"`}},
		{"literal string false", `'false'`, []string{`"false"`, `'false'`}},
		{"capitalised True", `True`, []string{`True`}},
		{"number", `1`, []string{`1`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := s087IssuesOfRule(LintRecordModel(s087LintRecord(`url = "https://example.com"`, `binary = `+tc.raw)), LintLegacyBinary)
			if len(got) != 1 {
				t.Fatalf("binary = %s: got %d legacy-binary findings, want 1: %#v", tc.raw, len(got), got)
			}
			msg := got[0].Message
			if got[0].Line != 3 || got[0].Package != s087LintPkg {
				t.Errorf("binary = %s: finding at line %d of %q, want line 3 of %q", tc.raw, got[0].Line, got[0].Package, s087LintPkg)
			}
			quoted := false
			for _, q := range tc.quoted {
				quoted = quoted || strings.Contains(msg, q)
			}
			if !quoted {
				t.Errorf("binary = %s: message %q does not quote the value as any of %q", tc.raw, msg, tc.quoted)
			}
			if !strings.Contains(msg, "not a boolean") {
				t.Errorf("binary = %s: message %q does not say the value is not a boolean", tc.raw, msg)
			}
		})
	}
}

// TestS087_3_3_BooleanBinaryFindingsUnchanged pins R10.5: the findings and
// repairs for a real boolean binary are those reported today, a commented
// boolean included (the converse of R5.3: it must not be read as non-boolean).
func TestS087_3_3_BooleanBinaryFindingsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		body []string
		fix  string
		msg  string
	}{
		{"true without type", []string{`url = "https://example.com"`, `binary = true`}, FixBinaryToType,
			`binary is retired: the record declares no type, so it becomes type = "bin"`},
		{"commented true without type", []string{`url = "https://example.com"`, `binary = true # prebuilt`}, FixBinaryToType,
			`binary is retired: the record declares no type, so it becomes type = "bin"`},
		{"false without type", []string{`url = "https://example.com"`, `binary = false`}, FixDropBinary,
			"binary is retired: it says nothing, auto-detection is the default, so the line is deleted"},
		{"false with type", []string{`url = "https://example.com"`, `type = "source"`, `binary = false`}, FixDropBinary,
			`binary is retired: the record already declares type = "source", so the line is deleted`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := s087IssuesOfRule(LintRecordModel(s087LintRecord(tc.body...)), LintLegacyBinary)
			want := []LintIssue{{Line: 1 + len(tc.body), Package: s087LintPkg, Rule: LintLegacyBinary, Fix: tc.fix, Message: tc.msg}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("legacy-binary findings for %q\n got: %#v\nwant: %#v", tc.body, got, want)
			}
		})
	}
}
