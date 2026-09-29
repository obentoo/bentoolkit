package notice

import (
	"errors"
	"reflect"
	"testing"
)

// parseAtom must read back every atom newsAtom writes, so a revision without
// a site file keeps the news item's Display-If-Installed headers as they were.
func TestParseAtom_ReversesNewsAtom(t *testing.T) {
	for _, a := range []Affects{
		{CP: "dev-libs/foo"},
		{CP: "dev-libs/foo", Slot: "1"},
		{CP: "dev-libs/foo-bar", Ranges: []Range{{Op: "<", Ver: "1.2.3"}}},
		{CP: "dev-libs/eq", Ranges: []Range{{Op: "=", Ver: "2.0-r1"}}},
		{CP: "dev-libs/libfoo+", Slot: "0.1", Ranges: []Range{{Op: ">=", Ver: "1.0_rc1_p2"}}},
		{CP: "dev-cpp/c++-2d", Ranges: []Range{{Op: "<=", Ver: "3"}}},
	} {
		atom, _ := newsAtom(a)
		got, err := parseAtom(atom)
		if err != nil {
			t.Errorf("parseAtom(%q): %v", atom, err)
			continue
		}
		if !reflect.DeepEqual(got, a) {
			t.Errorf("parseAtom(%q) = %+v, want %+v", atom, got, a)
		}
	}
}

func TestParseAtom_RefusesWhatNewsAtomNeverWrites(t *testing.T) {
	for _, atom := range []string{"", "foo", ">=dev-libs/foo", "<dev-libs/foo-bar", "dev-libs/foo::gentoo", "dev-libs/foo:1/2"} {
		if _, err := parseAtom(atom); !errors.Is(err, ErrMalformedNews) {
			t.Errorf("parseAtom(%q): err = %v, want ErrMalformedNews", atom, err)
		}
	}
}
