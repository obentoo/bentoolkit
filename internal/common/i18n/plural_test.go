package i18n

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/i18n/i18ntest"
)

// The rules po/*.po declare, checked against the forms each language's
// grammar requires.
const (
	ruleRU = "nplurals=3; plural=(n%10==1 && n%100!=11 ? 0 : n%10>=2 && n%10<=4 && (n%100<10 || n%100>=20) ? 1 : 2);"
	rulePT = "nplurals=2; plural=(n > 1);"
	ruleES = "nplurals=2; plural=(n != 1);"
	ruleZH = "nplurals=1; plural=0;"
)

func TestPluralRules(t *testing.T) {
	cases := []struct {
		rule string
		want map[uint64]int
	}{
		{ruleRU, map[uint64]int{0: 2, 1: 0, 2: 1, 4: 1, 5: 2, 11: 2, 12: 2, 14: 2, 21: 0, 22: 1, 25: 2, 101: 0, 111: 2, 1000000: 2}},
		{rulePT, map[uint64]int{0: 0, 1: 0, 2: 1, 100: 1}},
		{ruleES, map[uint64]int{0: 1, 1: 0, 2: 1}},
		{ruleZH, map[uint64]int{0: 0, 1: 0, 7: 0}},
	}
	for _, c := range cases {
		np, rule, err := parsePluralForms("Plural-Forms: " + c.rule + "\n")
		if err != nil {
			t.Fatalf("%s: %v", c.rule, err)
		}
		for n, want := range c.want {
			if got := rule(n); got != want || got >= np {
				t.Errorf("%s: n=%d -> form %d, want %d", c.rule, n, got, want)
			}
		}
	}
}

func TestPluralFormsDefaultAndErrors(t *testing.T) {
	np, rule, err := parsePluralForms("Content-Type: text/plain; charset=UTF-8\n")
	if err != nil || np != 2 || rule(1) != 0 || rule(2) != 1 || rule(0) != 1 {
		t.Errorf("default rule: %d, %v", np, err)
	}
	for _, bad := range []string{
		"nplurals=0; plural=0;",
		"nplurals=9; plural=0;",
		"nplurals=x; plural=0;",
		"nplurals=2;",
		"nplurals=2; plural=n ? 1;",
		"nplurals=2; plural=(n > 1;",
		"nplurals=2; plural=n $ 1;",
		"nplurals=2; plural=n > ;",
		"nplurals=2; plural=n 1;",
		"nplurals=2; plural=99999999999999999999999;",
		"nplurals=2; plural=" + strings.Repeat("(", 70) + "n" + strings.Repeat(")", 70) + ";",
	} {
		if _, _, err := parsePluralForms("Plural-Forms: " + bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPluralRuleOutOfRangeFallsBackToFirstForm(t *testing.T) {
	_, rule, err := parsePluralForms("Plural-Forms: nplurals=2; plural=n*10;")
	if err != nil {
		t.Fatal(err)
	}
	if rule(3) != 0 {
		t.Error("an index past nplurals must clamp to form 0")
	}
	_, rule, _ = parsePluralForms("Plural-Forms: nplurals=2; plural=n/0 + n%0 + !n - 1 * 1;")
	_ = rule(5) // division by zero must not panic
}

func FuzzPluralForms(f *testing.F) {
	f.Add(ruleRU)
	f.Add(rulePT)
	f.Add("nplurals=6; plural=(n==0 ? 0 : n==1 ? 1 : n==2 ? 2 : n%100>=3 && n%100<=10 ? 3 : n%100>=11 ? 4 : 5);")
	f.Fuzz(func(t *testing.T, s string) {
		np, rule, err := parsePluralForms("Plural-Forms: " + s)
		if err != nil {
			return
		}
		for _, n := range []uint64{0, 1, 2, 5, 11, 21, 101, ^uint64(0)} {
			if i := rule(n); i < 0 || i >= np {
				t.Fatalf("%q: n=%d -> %d outside [0,%d)", s, n, i, np)
			}
		}
	})
}

func TestTPluralAndTCount(t *testing.T) {
	t.Cleanup(Reset)
	if got := TCount("%d package(s)", 3); got != "%d package(s)" {
		t.Errorf("TCount without catalog = %q", got)
	}
	if TPlural("%d day", "%d days", 1) != "%d day" || TPlural("%d day", "%d days", int64(0)) != "%d days" {
		t.Error("TPlural without catalog must pick the English form by n == 1")
	}

	dir := t.TempDir()
	i18ntest.WriteCatalog(t, dir, "ru", "dom", map[string]string{
		"":                               "Plural-Forms: " + ruleRU + "\n",
		"%d package(s)\x00%d package(s)": "%d пакет\x00%d пакета\x00%d пакетов",
		"%d day\x00%d days":              "%d день\x00%d дня\x00%d дней",
		"%d empty\x00%d empties":         "\x00\x00",
		"Plain":                          "Простой",
	})
	if _, err := initWith("dom", env(map[string]string{localeDirEnv: dir, "LANG": "ru_RU.UTF-8"})); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[int]string{1: "%d пакет", 3: "%d пакета", 5: "%d пакетов", 21: "%d пакет", -2: "%d пакета"} {
		if got := TCount("%d package(s)", n); got != want {
			t.Errorf("TCount(%d) = %q, want %q", n, got, want)
		}
	}
	if got := TPlural("%d day", "%d days", uint8(11)); got != "%d дней" {
		t.Errorf("TPlural(11) = %q", got)
	}
	if got := TPlural("%d empty", "%d empties", 2); got != "%d empties" {
		t.Errorf("an empty form must fall back to English, got %q", got)
	}
	if got := TPlural("Unknown", "Unknowns", 2); got != "Unknowns" {
		t.Errorf("a message absent from the catalog must stay English, got %q", got)
	}
	if T("Plain") != "Простой" {
		t.Error("singular entries still load alongside plural ones")
	}
	if NPlural("a", "b") != "a" {
		t.Error("NPlural must return the singular")
	}
}

func TestParseMORejectsABadPluralHeader(t *testing.T) {
	data := i18ntest.EncodeMO(map[string]string{"": "Plural-Forms: nplurals=2; plural=n ? ;\n"}, binary.LittleEndian)
	if _, err := parseMO(data); err == nil {
		t.Error("a catalog with an unparseable Plural-Forms was accepted")
	}
}
