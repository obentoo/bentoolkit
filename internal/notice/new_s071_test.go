package notice

// Story 071, sub-task 2.1: New turns raw flag values into a validated Notice,
// or an error naming the flag and the rule (R1.1, R1.2, R1.3, R1.4, R1.6,
// R1.9, R1.10, R1.11, R1.12).
//
// Every rejection is checked with errors.Is against its sentinel AND for the
// flag name in the message (Q4). The hostile halves come first in each test:
// the near-miss spelling that a case-folding or trimming validator would
// wrongly accept, the multibyte title that a byte-counting (or UTF-16
// counting) validator would wrongly reject, and the date that is "tomorrow"
// on the local calendar but still today in UTC.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var nowS071 = time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)

func validInputS071() Input {
	return Input{
		Type:      "news",
		Severity:  "info",
		Name:      "foo-cve",
		Title:     "foo 1.2 heap overflow",
		Summary:   "One-paragraph summary.",
		Published: "2026-10-02",
		Author:    "Jane Doe <jane@example.org>",
		Body:      "Some text.",
	}
}

func TestNew_BuildsTheIDFromTheDateAndTheName(t *testing.T) {
	n, err := New(validInputS071(), nowS071)
	if err != nil {
		t.Fatalf("New(valid input): %v", err)
	}
	if n.ID != "2026-10-02-foo-cve" {
		t.Errorf("ID = %q, want %q", n.ID, "2026-10-02-foo-cve")
	}
	if n.Revision != 1 {
		t.Errorf("Revision = %d, want 1 for a new notice", n.Revision)
	}
	wantPub := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	if !n.Published.Equal(wantPub) || n.Published.Location() != time.UTC {
		t.Errorf("Published = %v, want %v in UTC", n.Published, wantPub)
	}
	if !n.Updated.Equal(n.Published) {
		t.Errorf("Updated = %v, want it equal to Published %v for a new notice", n.Updated, n.Published)
	}
	if n.Type != "news" || n.Severity != "info" || n.Title != "foo 1.2 heap overflow" ||
		n.Summary != "One-paragraph summary." || n.Author != "Jane Doe <jane@example.org>" || n.Body != "Some text." {
		t.Errorf("fields were not carried through: %+v", n)
	}
}

// Hostile half: late evening at UTC-3 is already the next day in UTC. The
// default must follow UTC, not the local calendar.
func TestNew_DateDefaultsToTodayInUTC(t *testing.T) {
	in := validInputS071()
	in.Published = ""
	local := time.Date(2026, 10, 2, 23, 30, 0, 0, time.FixedZone("UTC-3", -3*3600))

	n, err := New(in, local)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n.ID != "2026-10-03-foo-cve" {
		t.Errorf("ID = %q; the default date must be today in UTC (2026-10-03), not the local date", n.ID)
	}
}

func TestNew_RejectsAnInvalidDate(t *testing.T) {
	for _, pub := range []string{"2026-02-30", "02-10-2026", "2026-10-2", "2026-10-02T14:00:00Z"} {
		in := validInputS071()
		in.Published = pub
		if _, err := New(in, nowS071); err == nil {
			t.Errorf("New accepted --published %q", pub)
		} else if !strings.Contains(err.Error(), "--published") {
			t.Errorf("the error for --published %q does not name the flag: %v", pub, err)
		}
	}
}

func TestNew_TypeAcceptsOnlyTheFourKinds(t *testing.T) {
	// Hostile first: spellings a folding or trimming check would let through.
	for _, bad := range []string{"Security", "SECURITY", " security", "security ", "advisory", ""} {
		in := validInputS071()
		in.Type = bad
		in.Affects = []string{"dev-libs/foo"}
		_, err := New(in, nowS071)
		if !errors.Is(err, ErrType) {
			t.Errorf("--type %q: err = %v, want ErrType", bad, err)
			continue
		}
		for _, want := range []string{"--type", "security", "release", "news", "announcement"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--type %q: the error %q does not name %q", bad, err, want)
			}
		}
	}
	for _, good := range []string{"security", "release", "news", "announcement"} {
		in := validInputS071()
		in.Type = good
		in.Affects = []string{"dev-libs/foo"}
		if _, err := New(in, nowS071); err != nil {
			t.Errorf("--type %q was rejected: %v", good, err)
		}
	}
}

func TestNew_SeverityAcceptsOnlyTheThreeLevels(t *testing.T) {
	for _, bad := range []string{"Critical", " warning", "warn", "error", "high", ""} {
		in := validInputS071()
		in.Severity = bad
		_, err := New(in, nowS071)
		if !errors.Is(err, ErrSeverity) {
			t.Errorf("--severity %q: err = %v, want ErrSeverity", bad, err)
			continue
		}
		for _, want := range []string{"--severity", "info", "warning", "critical"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--severity %q: the error %q does not name %q", bad, err, want)
			}
		}
	}
	for _, good := range []string{"info", "warning", "critical"} {
		in := validInputS071()
		in.Severity = good
		if _, err := New(in, nowS071); err != nil {
			t.Errorf("--severity %q was rejected: %v", good, err)
		}
	}
}

func TestNew_NameFollowsTheGLEP42ShortNameRule(t *testing.T) {
	for _, bad := range []string{
		"",
		strings.Repeat("a", 21),
		"Foo",
		"foo.bar",
		"foo bar",
		"foo/bar",
		"café",
		"../x",
	} {
		in := validInputS071()
		in.Name = bad
		_, err := New(in, nowS071)
		if !errors.Is(err, ErrName) {
			t.Errorf("--name %q: err = %v, want ErrName", bad, err)
			continue
		}
		if !strings.Contains(err.Error(), "--name") {
			t.Errorf("--name %q: the error %q does not name the flag", bad, err)
		}
	}
	for _, good := range []string{"a", strings.Repeat("z", 20), "a+b_c-1", "gcc-15"} {
		in := validInputS071()
		in.Name = good
		n, err := New(in, nowS071)
		if err != nil {
			t.Errorf("--name %q was rejected: %v", good, err)
			continue
		}
		if n.ID != "2026-10-02-"+good {
			t.Errorf("--name %q gave ID %q", good, n.ID)
		}
	}
}

func TestNew_TitleIsAtMostFiftyCharacters(t *testing.T) {
	// Hostile first: 50 characters of which many are two bytes long. A byte
	// count reads 100 and wrongly rejects it.
	multibyte := strings.Repeat("é", 50)
	in := validInputS071()
	in.Title = multibyte
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("a 50-character title of multibyte runes was rejected: %v", err)
	}

	in = validInputS071()
	in.Title = strings.Repeat("t", 50)
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("a 50-character title was rejected: %v", err)
	}

	in = validInputS071()
	in.Title = strings.Repeat("t", 51)
	_, err := New(in, nowS071)
	if !errors.Is(err, ErrTitle) {
		t.Fatalf("a 51-character title: err = %v, want ErrTitle", err)
	}
	for _, want := range []string{"--title", "50", "51"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the title error %q does not state %q (flag, limit and length given)", err, want)
		}
	}

	in = validInputS071()
	in.Title = ""
	if _, err := New(in, nowS071); !errors.Is(err, ErrTitle) {
		t.Errorf("an empty --title: err = %v, want ErrTitle", err)
	} else if !strings.Contains(err.Error(), "--title") {
		t.Errorf("the empty-title error does not name the flag: %v", err)
	}
}

// Code points, not bytes and not UTF-16 units: an emoji outside the BMP is
// four bytes and two UTF-16 units, but one code point.
func TestNew_TitleCountsCodePoints(t *testing.T) {
	in := validInputS071()
	in.Title = strings.Repeat("\U0001F600", 50)
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("a 50-code-point title of non-BMP emoji was rejected: %v", err)
	}
	in.Title = strings.Repeat("\U0001F600", 51)
	_, err := New(in, nowS071)
	if !errors.Is(err, ErrTitle) {
		t.Fatalf("a 51-code-point title: err = %v, want ErrTitle", err)
	}
	if !strings.Contains(err.Error(), "51") {
		t.Errorf("the error %q does not state the length given in code points (51)", err)
	}
}

func TestNew_SummaryIsRequiredAndAtMostThreeHundredCharacters(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("s", 301)} {
		in := validInputS071()
		in.Summary = bad
		if _, err := New(in, nowS071); err == nil {
			t.Errorf("a summary of %d characters was accepted; the feed requires 1..300", len(bad))
		} else if !strings.Contains(err.Error(), "--summary") || !strings.Contains(err.Error(), "300") {
			t.Errorf("the summary error does not name --summary and the limit 300: %v", err)
		}
	}
	for _, good := range []string{strings.Repeat("s", 300), strings.Repeat("\U0001F600", 300)} {
		in := validInputS071()
		in.Summary = good
		if _, err := New(in, nowS071); err != nil {
			t.Errorf("a 300-code-point summary was rejected: %v", err)
		}
	}
	in := validInputS071()
	in.Summary = strings.Repeat("\U0001F600", 301)
	if _, err := New(in, nowS071); err == nil {
		t.Error("a 301-code-point summary was accepted")
	}
}

// R1.10: C0 controls other than tab and line feed, and the noncharacters
// U+FFFE and U+FFFF, are refused in each text field, naming the field.
func TestNew_ControlCharactersAreRefusedPerField(t *testing.T) {
	bad := []string{"\x00", "\x07", "\x1b", "\r", "\x0b", "\x0c", "\x1f", "\uFFFE", "\uFFFF"}
	fields := []struct {
		name string
		set  func(*Input, string)
	}{
		{"--title", func(in *Input, v string) { in.Title = "a" + v + "b" }},
		{"--summary", func(in *Input, v string) { in.Summary = "a" + v + "b" }},
		{"body", func(in *Input, v string) { in.Body = "a" + v + "b" }},
	}
	for _, f := range fields {
		for _, c := range bad {
			in := validInputS071()
			f.set(&in, c)
			_, err := New(in, nowS071)
			if !errors.Is(err, ErrControlChar) {
				t.Errorf("%s with %q: err = %v, want ErrControlChar", f.name, c, err)
				continue
			}
			if !strings.Contains(err.Error(), f.name) {
				t.Errorf("%s with %q: the error %q does not name the field", f.name, c, err)
			}
		}
	}
}

// R1.12: title and summary are single-line, so a tab or line feed in either
// is refused naming the field; the body keeps both (R1.10).
func TestNew_TitleAndSummaryAreSingleLine(t *testing.T) {
	for _, c := range []string{"\t", "\n"} {
		for _, f := range []struct {
			name string
			set  func(*Input)
		}{
			{"--title", func(in *Input) { in.Title = "foo" + c + "bar" }},
			{"--summary", func(in *Input) { in.Summary = "foo" + c + "bar" }},
		} {
			in := validInputS071()
			f.set(&in)
			_, err := New(in, nowS071)
			if !errors.Is(err, ErrControlChar) {
				t.Errorf("%s with %q: err = %v, want ErrControlChar", f.name, c, err)
				continue
			}
			if !strings.Contains(err.Error(), f.name) {
				t.Errorf("%s with %q: the error %q does not name the field", f.name, c, err)
			}
		}
	}
	in := validInputS071()
	in.Body = "Para one.\n\n\tIndented line."
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("tab and line feed in the body were refused: %v", err)
	}
}

// R1.11: a --published date after today in UTC is refused, stating today.
// Both hostile halves: at 23:30 in UTC-3 the local tomorrow is already today
// in UTC and must be accepted; at 01:00 in UTC+3 the local today is still
// tomorrow in UTC and must be refused.
func TestNew_FutureDateIsRefused(t *testing.T) {
	west := time.Date(2026, 10, 2, 23, 30, 0, 0, time.FixedZone("UTC-3", -3*3600)) // 2026-10-03 02:30 UTC
	in := validInputS071()
	in.Published = "2026-10-03"
	if _, err := New(in, west); err != nil {
		t.Errorf("local tomorrow that is today in UTC was refused: %v", err)
	}

	east := time.Date(2026, 10, 3, 1, 0, 0, 0, time.FixedZone("UTC+3", 3*3600)) // 2026-10-02 22:00 UTC
	in = validInputS071()
	in.Published = "2026-10-03"
	_, err := New(in, east)
	if !errors.Is(err, ErrFuture) {
		t.Errorf("local today that is tomorrow in UTC: err = %v, want ErrFuture", err)
	} else if !strings.Contains(err.Error(), "2026-10-02") {
		t.Errorf("the error %q does not state today's UTC date 2026-10-02", err)
	}

	in = validInputS071()
	in.Published = "2026-10-03"
	_, err = New(in, nowS071)
	if !errors.Is(err, ErrFuture) {
		t.Errorf("--published tomorrow: err = %v, want ErrFuture", err)
	} else if !strings.Contains(err.Error(), "--published") || !strings.Contains(err.Error(), "2026-10-02") {
		t.Errorf("the error %q does not name --published and today's date", err)
	}

	in = validInputS071()
	in.Published = "2026-10-02"
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("--published today was refused: %v", err)
	}
	in.Published = "2020-01-01"
	if _, err := New(in, nowS071); err != nil {
		t.Errorf("a past --published was refused: %v", err)
	}
}

func TestNew_SecurityAndReleaseNeedAnAffectedPackage(t *testing.T) {
	for _, typ := range []string{"security", "release"} {
		in := validInputS071()
		in.Type = typ
		in.Affects = nil
		_, err := New(in, nowS071)
		if !errors.Is(err, ErrAffectsRequired) {
			t.Errorf("--type %s with no --affects: err = %v, want ErrAffectsRequired", typ, err)
		}

		in.Affects = []string{"dev-libs/foo:1 >=1.0,<1.2.3"}
		n, err := New(in, nowS071)
		if err != nil {
			t.Errorf("--type %s with one --affects was rejected: %v", typ, err)
			continue
		}
		if len(n.Affects) != 1 || n.Affects[0].CP != "dev-libs/foo" || n.Affects[0].Slot != "1" || len(n.Affects[0].Ranges) != 2 {
			t.Errorf("--type %s: Affects = %+v, want one parsed entry for dev-libs/foo:1 with two ranges", typ, n.Affects)
		}
	}
	for _, typ := range []string{"news", "announcement"} {
		in := validInputS071()
		in.Type = typ
		in.Affects = nil
		if _, err := New(in, nowS071); err != nil {
			t.Errorf("--type %s needs no --affects, but was rejected: %v", typ, err)
		}
	}
}
