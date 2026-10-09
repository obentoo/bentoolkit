package registry

import (
	"reflect"
	"testing"
)

// These tests pin the CURRENT findings of lintRecordFields — their rule, fix,
// line, message and order — for the branches the rest of the suite does not
// reach. They call the function on a hand-built record so each case states
// exactly which assignments the scanner handed over.

const s086Rec = "app-misc/foo"

func s086Field(key, value string, line int) recordField {
	return recordField{key: key, value: value, line: line}
}

func s086Issue(line int, rule, fix, msg string) LintIssue {
	return LintIssue{Line: line, Package: s086Rec, Rule: rule, Fix: fix, Message: msg}
}

const (
	s086MsgBinaryToType = `binary is retired: the record declares no type, so it becomes type = "bin"`
	s086MsgBinaryNoType = "binary is retired: it says nothing, auto-detection is the default, so the line is deleted"
	s086MsgBinaryTyped  = "binary is retired: the record already declares type, so the line is deleted"
	s086MsgEnabled      = "enabled = true is redundant: an absent enabled already means enabled"
	s086MsgLegacyBase   = `track = "commit" without base_from: the base version is whatever the ebuild already carries and can freeze there unnoticed; declare base_from = "file", "tag" or "commit_message" — or "none" when upstream publishes no version at all`
)

func TestS086LintRecordFields(t *testing.T) {
	tests := []struct {
		name   string
		fields []recordField // nil with nilRec set means a nil *recordLintState
		nilRec bool
		want   []LintIssue
	}{
		{name: "nil record", nilRec: true},
		{name: "record with no fields", fields: []recordField{}},

		// binary
		{name: "binary true without type becomes type", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("binary", "true", 11),
		}, want: []LintIssue{s086Issue(11, LintLegacyBinary, FixBinaryToType, s086MsgBinaryToType)}},
		{name: "binary true with a quoted type is dropped and names the type", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("type", `"source"`, 11),
			s086Field("binary", "true", 12),
		}, want: []LintIssue{s086Issue(12, LintLegacyBinary, FixDropBinary,
			`binary is retired: the record already declares type = "source", so the line is deleted`)}},
		{name: "binary false with a literal-quoted type", fields: []recordField{
			s086Field("binary", "false", 10),
			s086Field("type", `'bin'`, 11),
		}, want: []LintIssue{s086Issue(10, LintLegacyBinary, FixDropBinary,
			`binary is retired: the record already declares type = "bin", so the line is deleted`)}},
		{name: "binary true with an unquoted type does not name it", fields: []recordField{
			s086Field("type", "bin", 10),
			s086Field("binary", "true", 11),
		}, want: []LintIssue{s086Issue(11, LintLegacyBinary, FixDropBinary, s086MsgBinaryTyped)}},
		{name: "binary false without type is dropped", fields: []recordField{
			s086Field("binary", "false", 10),
		}, want: []LintIssue{s086Issue(10, LintLegacyBinary, FixDropBinary, s086MsgBinaryNoType)}},
		{name: "binary with a non-boolean value without type is dropped", fields: []recordField{
			s086Field("binary", `"yes"`, 10),
		}, want: []LintIssue{s086Issue(10, LintLegacyBinary, FixDropBinary, s086MsgBinaryNoType)}},
		{name: "binary true with a trailing comment becomes type", fields: []recordField{
			s086Field("binary", "true # prebuilt", 10),
		}, want: []LintIssue{s086Issue(10, LintLegacyBinary, FixBinaryToType, s086MsgBinaryToType)}},
		{name: "binary rewritten into type is ranked as type", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("series", `"^1\."`, 11),
			s086Field("binary", "true", 12),
		}, want: []LintIssue{
			s086Issue(12, LintLegacyBinary, FixBinaryToType, s086MsgBinaryToType),
			s086Issue(12, LintFieldOrder, FixReorderFields,
				`field "type" (written as the retired "binary") is out of canonical order: it belongs before "series"`),
		}},
		{name: "dropped binary is not ranked", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("type", `"bin"`, 11),
			s086Field("comments", `"""`, 12),
			s086Field("binary", "false", 13),
		}, want: []LintIssue{s086Issue(13, LintLegacyBinary, FixDropBinary,
			`binary is retired: the record already declares type = "bin", so the line is deleted`)}},

		// enabled: only a bare true is redundant
		{name: "enabled true is redundant", fields: []recordField{
			s086Field("enabled", "true", 10),
			s086Field("url", `"https://example.com"`, 11),
		}, want: []LintIssue{s086Issue(10, LintRedundantEnabled, FixDropEnabled, s086MsgEnabled)}},
		{name: "enabled true with a trailing comment is redundant", fields: []recordField{
			s086Field("enabled", "true # legacy", 10),
		}, want: []LintIssue{s086Issue(10, LintRedundantEnabled, FixDropEnabled, s086MsgEnabled)}},
		{name: "enabled true after comments is not an order finding", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("comments", `"""`, 11),
			s086Field("enabled", "true", 14),
		}, want: []LintIssue{s086Issue(14, LintRedundantEnabled, FixDropEnabled, s086MsgEnabled)}},
		{name: "enabled false is kept and ranked", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("enabled", "false", 11),
		}, want: []LintIssue{s086Issue(11, LintFieldOrder, FixReorderFields,
			`field "enabled" is out of canonical order: it belongs before "url"`)}},
		{name: "enabled spelled True is not a boolean and is ranked", fields: []recordField{
			s086Field("url", `"https://example.com"`, 10),
			s086Field("enabled", "True", 11),
		}, want: []LintIssue{s086Issue(11, LintFieldOrder, FixReorderFields,
			`field "enabled" is out of canonical order: it belongs before "url"`)}},
		{name: "enabled as the string true is not redundant", fields: []recordField{
			s086Field("enabled", `"true"`, 10),
		}},
		{name: "enabled with an empty value is not redundant", fields: []recordField{
			s086Field("enabled", "", 10),
		}},

		// order
		{name: "only the first out-of-order field is reported", fields: []recordField{
			s086Field("parser", `"json"`, 10),
			s086Field("url", `"https://example.com"`, 11),
			s086Field("select", `"max"`, 12),
			s086Field("transform", "[", 13),
		}, want: []LintIssue{s086Issue(11, LintFieldOrder, FixReorderFields,
			`field "url" is out of canonical order: it belongs before "parser"`)}},
		{name: "a repeated field is not out of order", fields: []recordField{
			s086Field("url", `"https://a.example.com"`, 10),
			s086Field("url", `"https://b.example.com"`, 11),
		}},
		{name: "an unknown key is skipped", fields: []recordField{
			s086Field("parser", `"json"`, 10),
			s086Field("urll", `"https://example.com"`, 11),
			s086Field("path", `"tag"`, 12),
		}},

		// legacy base
		{name: "track commit without base_from", fields: []recordField{
			s086Field("track", `"commit"`, 10),
			s086Field("url", `"https://example.com"`, 11),
		}, want: []LintIssue{s086Issue(10, LintLegacyBase, FixNone, s086MsgLegacyBase)}},
		{name: "literal-quoted track commit without base_from", fields: []recordField{
			s086Field("track", `'commit'`, 10),
		}, want: []LintIssue{s086Issue(10, LintLegacyBase, FixNone, s086MsgLegacyBase)}},
		{name: "track commit with base_from none", fields: []recordField{
			s086Field("track", `"commit"`, 10),
			s086Field("base_from", `"none"`, 11),
		}},
		{name: "track commit with an empty base_from", fields: []recordField{
			s086Field("track", `"commit"`, 10),
			s086Field("base_from", `""`, 11),
		}},
		{name: "unbalanced track quote is not commit", fields: []recordField{
			s086Field("track", `"commit`, 10),
		}},
		{name: "track Commit is not commit", fields: []recordField{
			s086Field("track", `"Commit"`, 10),
		}},
		{name: "track commit with a trailing comment is not commit", fields: []recordField{
			s086Field("track", `"commit" # snapshot`, 10),
		}},
		{name: "the last track line wins", fields: []recordField{
			s086Field("track", `"commit"`, 10),
			s086Field("track", `"commit"`, 12),
		}, want: []LintIssue{s086Issue(12, LintLegacyBase, FixNone, s086MsgLegacyBase)}},

		// Several findings: sorted by line whatever rule produced them.
		{name: "findings are emitted in line order", fields: []recordField{
			s086Field("track", `"commit"`, 5),
			s086Field("parser", `"json"`, 6),
			s086Field("url", `"https://example.com"`, 7),
			s086Field("enabled", "true", 8),
			s086Field("binary", "false", 9),
		}, want: []LintIssue{
			s086Issue(5, LintLegacyBase, FixNone, s086MsgLegacyBase),
			s086Issue(7, LintFieldOrder, FixReorderFields,
				`field "url" is out of canonical order: it belongs before "parser"`),
			s086Issue(8, LintRedundantEnabled, FixDropEnabled, s086MsgEnabled),
			s086Issue(9, LintLegacyBinary, FixDropBinary, s086MsgBinaryNoType),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rec *recordLintState
			if !tt.nilRec {
				rec = &recordLintState{name: s086Rec, fields: tt.fields}
			}
			got := lintRecordFields(rec)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("lintRecordFields()\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}
