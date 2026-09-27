package report

// Authored for story 057, sub-task 4.1 — S057-R4.2, S057-R4.3, S057-R4.4,
// S057-R4.5, S057-R4.6 (S057-R6.10 and S057-R6.11 are pinned by the existing
// boundary, citation and JSON-envelope tests).
//
// Contract names used here: ComparePkg.Cause, ComparePkg.Error (json "cause",
// "error") and CompareRun.ReadingFailures (json "reading_failures", a list of
// {"cause","count"}). The list is built from JSON so this file does not pin the
// Go name of its element type, which story.md never names.
//
// Hostile first: a LOOKUP cause on a row whose reading is not "failed" must not
// grow a reading marker, and a run whose unread rows failed for no recorded cause
// must not invent one.
//
// RED ON ARRIVAL: Cause, Error and ReadingFailures do not exist.

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCompareDetailNamesTheReadingCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkg  ComparePkg
		want string
	}{
		// Hostile: the cause field is shared with the lookup axis (S057-R4.4), so
		// a status:error row carries a cause too — and it is NOT a reading failure.
		{"a lookup cause on an unread row grows no marker",
			ComparePkg{Reason: "the upstream lookup failed (rate-limited): 403", Reading: "not requested", Cause: "rate-limited", Error: "403"},
			"the upstream lookup failed (rate-limited): 403"},
		{"a cause on a row that was read grows no marker",
			ComparePkg{Reason: "adds a patch", Reading: "read", Cause: "timed out"},
			"adds a patch"},
		// Benign.
		{"a failed reading names its cause",
			ComparePkg{Reason: "the two ebuilds differ", Reading: "failed", Cause: "timed out", Error: "claude ran out of time"},
			"the two ebuilds differ [reading failed: timed out]"},
		{"with no reason the marker stands alone",
			ComparePkg{Reading: "failed", Cause: "could not start"},
			"[reading failed: could not start]"},
		{"with no cause recorded the marker is the bare one",
			ComparePkg{Reason: "the two ebuilds differ", Reading: "failed"},
			"the two ebuilds differ [reading failed]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareDetail(tc.pkg); got != tc.want {
				t.Errorf("compareDetail\n got: %q\nwant: %q (S057-R4.2)", got, tc.want)
			}
		})
	}
}

// s057RunFromJSON builds a CompareRun through its published JSON keys.
func s057RunFromJSON(t *testing.T, doc string) CompareRun {
	t.Helper()
	var run CompareRun
	if err := json.Unmarshal([]byte(doc), &run); err != nil {
		t.Fatalf("decoding the fixture run: %v", err)
	}
	return run
}

func s057ScopeNote(r CompareRun) string {
	return strings.Join(compareScopeSection(r).Notes, "\n")
}

func TestCompareScopeNoteCountsFailuresPerCause(t *testing.T) {
	t.Run("an unread run with no failure recorded names no cause", func(t *testing.T) {
		note := s057ScopeNote(s057RunFromJSON(t, `{"scanned":4,"in_both":4,"unread":4}`))
		if !strings.Contains(note, "never read") {
			t.Fatalf("the unread note is gone: %q", note)
		}
		for _, w := range []string{"timed out", "could not start", "exited non-zero", "empty or unusable reply", "cancelled", "ebuild unreadable"} {
			if strings.Contains(note, w) {
				t.Errorf("the note names the cause %q, which no row recorded: %q", w, note)
			}
		}
	})

	t.Run("highest count first, ties in vocabulary order", func(t *testing.T) {
		// Given in the WRONG order on purpose: the note orders, not the producer.
		run := s057RunFromJSON(t, `{"scanned":9,"in_both":9,"unread":6,"reading_failures":[
			{"cause":"other","count":1},
			{"cause":"cancelled","count":1},
			{"cause":"could not start","count":1},
			{"cause":"timed out","count":3}]}`)
		note := s057ScopeNote(run)
		const want = "3 timed out, 1 could not start, 1 cancelled, 1 other"
		if !strings.Contains(note, want) {
			t.Errorf("the unread note does not carry %q (S057-R4.3):\n%s", want, note)
		}
		if !strings.Contains(note, "never read") {
			t.Errorf("the existing unread sentence is gone: %q", note)
		}
	})

	t.Run("the example of the requirement", func(t *testing.T) {
		run := s057RunFromJSON(t, `{"scanned":4,"in_both":4,"unread":4,"reading_failures":[
			{"cause":"could not start","count":1},{"cause":"timed out","count":3}]}`)
		if note := s057ScopeNote(run); !strings.Contains(note, "3 timed out, 1 could not start") {
			t.Errorf("the unread note does not carry %q:\n%s", "3 timed out, 1 could not start", note)
		}
	})
}

func s057Keys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	return m
}

func s057SortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestComparePayloadPublishesCauseErrorAndReadingFailures is S057-R4.4 and
// S057-R4.5 at the zero value, where a stray omitempty would show.
func TestComparePayloadPublishesCauseErrorAndReadingFailures(t *testing.T) {
	t.Run("ComparePkg has exactly the eight old keys plus cause and error, both empty strings", func(t *testing.T) {
		m := s057Keys(t, ComparePkg{})
		want := []string{"cause", "diff", "error", "further_findings", "local_version", "package", "reading", "reason", "remote_version", "status"}
		if got := s057SortedKeys(m); !reflect.DeepEqual(got, want) {
			t.Errorf("ComparePkg keys\n got: %v\nwant: %v", got, want)
		}
		for _, k := range []string{"cause", "error"} {
			if string(m[k]) != `""` {
				t.Errorf("%q at the zero value is %s, want \"\"", k, m[k])
			}
		}
	})

	t.Run("CompareRun gains reading_failures and loses nothing", func(t *testing.T) {
		want := []string{"in_both", "keep", "keep_groups", "needs_rebase", "notes", "only_local",
			"reading_failures", "redundant", "repository", "scanned", "unknown", "unread", "verdicts"}
		if got := s057SortedKeys(s057Keys(t, CompareRun{})); !reflect.DeepEqual(got, want) {
			t.Errorf("CompareRun keys\n got: %v\nwant: %v", got, want)
		}
	})

	t.Run("each reading_failures entry is exactly {cause, count}", func(t *testing.T) {
		run := s057RunFromJSON(t, `{"reading_failures":[{"cause":"timed out","count":3}]}`)
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(s057Keys(t, run)["reading_failures"], &entries); err != nil {
			t.Fatalf("reading_failures is not a list of objects: %v", err)
		}
		if len(entries) != 1 || string(entries[0]["cause"]) != `"timed out"` || string(entries[0]["count"]) != "3" || len(entries[0]) != 2 {
			t.Errorf("reading_failures = %v, want [{\"cause\":\"timed out\",\"count\":3}]", entries)
		}
	})

	t.Run("the error text is kept at full length in the model and in JSON", func(t *testing.T) {
		long := strings.Repeat("rate limit resets later; ", 220) // ~5.5 KB
		m := s057Keys(t, ComparePkg{Status: "error", Cause: "rate-limited", Error: long})
		var got string
		if err := json.Unmarshal(m["error"], &got); err != nil {
			t.Fatalf("error is not a string: %v", err)
		}
		if got != long {
			t.Errorf("the JSON error is %d bytes, want the full %d (S057-R4.6: only a table cell may cut it)", len(got), len(long))
		}
		if string(m["cause"]) != `"rate-limited"` {
			t.Errorf("cause = %s, want \"rate-limited\"", m["cause"])
		}
	})
}
