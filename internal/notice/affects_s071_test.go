package notice

// Story 071, sub-task 2.2: ParseAffects accepts
// `category/package[:slot]` followed by zero or more comma-separated ranges,
// each an operator from <, <=, =, >=, > immediately followed by a valid
// Gentoo version, and rejects anything else quoting the value and the part
// that failed (R1.5).
//
// Hostile halves first: `<=1.0` must not collapse into `<` + `=1.0`, a slot
// must not be swallowed into the package name, a subslot is refused, and a whitespace-padded
// version must never survive into the stored range.

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAffects_LongestOperatorWins(t *testing.T) {
	cases := []struct {
		in     string
		op, ve string
	}{
		{"dev-libs/foo <=1.0", "<=", "1.0"},
		{"dev-libs/foo >=1.0", ">=", "1.0"},
		{"dev-libs/foo <1.0", "<", "1.0"},
		{"dev-libs/foo >1.0", ">", "1.0"},
		{"dev-libs/foo =1.0", "=", "1.0"},
	}
	for _, c := range cases {
		a, err := ParseAffects(c.in)
		if err != nil {
			t.Errorf("ParseAffects(%q): %v", c.in, err)
			continue
		}
		want := []Range{{Op: c.op, Ver: c.ve}}
		if !reflect.DeepEqual(a.Ranges, want) {
			t.Errorf("ParseAffects(%q).Ranges = %+v, want %+v", c.in, a.Ranges, want)
		}
	}
}

func TestParseAffects_SlotIsKeptApartFromThePackage(t *testing.T) {
	cases := []struct{ in, cp, slot string }{
		{"dev-libs/foo:1", "dev-libs/foo", "1"},
		{"dev-libs/libfoo+:0.1 >=1.0_rc1_p2,<1.0b-r0", "dev-libs/libfoo+", "0.1"},
		{"dev-libs/foo", "dev-libs/foo", ""},
		{"sys-devel/gcc:15 >=15.1", "sys-devel/gcc", "15"},
		{"dev-cpp/c++-foo", "dev-cpp/c++-foo", ""},
	}
	for _, c := range cases {
		a, err := ParseAffects(c.in)
		if err != nil {
			t.Errorf("ParseAffects(%q): %v", c.in, err)
			continue
		}
		if a.CP != c.cp || a.Slot != c.slot {
			t.Errorf("ParseAffects(%q) = {CP:%q Slot:%q}, want {CP:%q Slot:%q}", c.in, a.CP, a.Slot, c.cp, c.slot)
		}
	}
}

func TestParseAffects_ManyRangesKeepTheirOrder(t *testing.T) {
	a, err := ParseAffects("dev-libs/foo:1 >=1.0,<1.2.3")
	if err != nil {
		t.Fatalf("ParseAffects: %v", err)
	}
	want := Affects{CP: "dev-libs/foo", Slot: "1", Ranges: []Range{{Op: ">=", Ver: "1.0"}, {Op: "<", Ver: "1.2.3"}}}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("ParseAffects = %+v, want %+v", a, want)
	}

	b, err := ParseAffects("dev-libs/foo >=1.0_rc1-r2")
	if err != nil {
		t.Fatalf("ParseAffects with a suffixed version: %v", err)
	}
	if len(b.Ranges) != 1 || b.Ranges[0].Ver != "1.0_rc1-r2" {
		t.Errorf("Ranges = %+v, want one range with version 1.0_rc1-r2", b.Ranges)
	}
}

// A version separated from its operator by a space is not the documented
// syntax. Whatever the parser does with it, the stored version must never
// carry whitespace: the site's version pattern rejects it.
func TestParseAffects_NoWhitespaceSurvivesIntoAVersion(t *testing.T) {
	a, err := ParseAffects("dev-libs/foo >= 1.0")
	if err != nil {
		if !errors.Is(err, ErrAffects) {
			t.Errorf("rejected, but not with ErrAffects: %v", err)
		}
		return
	}
	for _, r := range a.Ranges {
		if r.Ver != strings.TrimSpace(r.Ver) || strings.ContainsAny(r.Ver, " \t") || r.Op != strings.TrimSpace(r.Op) {
			t.Errorf("ParseAffects(%q) stored a range with whitespace: %+v", "dev-libs/foo >= 1.0", r)
		}
	}
}

func TestParseAffects_RejectsQuotingTheValueAndThePart(t *testing.T) {
	cases := []struct {
		in   string
		part string // text the error must quote besides the value
	}{
		{"foo", "foo"},
		{"dev-libs/", "dev-libs/"},
		{"dev-libs/foo:", ":"},
		{"dev-libs/foo::bentoo", "bentoo"},
		{">=dev-libs/foo-1.0", ">=dev-libs/foo-1.0"},
		{"dev-libs/foo >=abc", "abc"},
		{"dev-libs/foo >=1..2", "1..2"},
		{"dev-libs/foo 1.0", "1.0"},
		{"dev-libs/foo ==1.0", "==1.0"},
		{"dev-libs/foo =<1.0", "=<1.0"},
		{"dev-libs/foo ~1.0", "~1.0"},
		{"dev-libs/foo !=1.0", "!=1.0"},
		{"dev-libs/foo >=1.0,", ","},
		{"dev-libs/foo >=1.0 <2", "<2"},
		{"", ""},
		// R1.5: a slot without a subslot — the feed and the tray compare the
		// slot only, so a subslot would be silently ignored downstream.
		{"dev-libs/foo:1/2", "1/2"},
		{"dev-libs/foo:1/2.3 >=1.0", "1/2.3"},
	}
	for _, c := range cases {
		_, err := ParseAffects(c.in)
		if !errors.Is(err, ErrAffects) {
			t.Errorf("ParseAffects(%q): err = %v, want ErrAffects", c.in, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "--affects") {
			t.Errorf("ParseAffects(%q): the error %q does not name --affects", c.in, msg)
		}
		if c.in != "" && !strings.Contains(msg, c.in) {
			t.Errorf("ParseAffects(%q): the error %q does not quote the value", c.in, msg)
		}
		if c.part != "" && !strings.Contains(msg, c.part) {
			t.Errorf("ParseAffects(%q): the error %q does not quote the failing part %q", c.in, msg, c.part)
		}
	}
}

// New surfaces a malformed --affects with the same sentinel.
func TestParseAffects_NewPropagatesTheRejection(t *testing.T) {
	in := Input{
		Type: "security", Severity: "critical", Name: "foo-cve",
		Title: "foo", Summary: "s", Published: "2026-10-02", Author: "A <a@b.c>", Body: "b",
		Affects: []string{"dev-libs/foo >=1.0", "dev-libs/bar >=oops"},
	}
	_, err := New(in, time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC))
	if !errors.Is(err, ErrAffects) {
		t.Fatalf("New with a malformed second --affects: err = %v, want ErrAffects", err)
	}
	if !strings.Contains(err.Error(), "dev-libs/bar >=oops") {
		t.Errorf("the error %q does not quote the failing --affects value", err)
	}
}
