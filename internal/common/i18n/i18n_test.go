package i18n

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/i18n/i18ntest"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLanguages(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"nothing set", nil, nil},
		{"C locale", map[string]string{"LANG": "C.UTF-8"}, nil},
		{"POSIX ignores LANGUAGE", map[string]string{"LC_ALL": "POSIX", "LANGUAGE": "pt_BR"}, nil},
		{"LANG with codeset", map[string]string{"LANG": "pt_BR.UTF-8"}, []string{"pt_BR", "pt"}},
		{"modifier stripped", map[string]string{"LANG": "ru_RU.UTF-8@euro"}, []string{"ru_RU", "ru"}},
		{"LC_ALL beats LANG", map[string]string{"LC_ALL": "es_ES.UTF-8", "LANG": "pt_BR.UTF-8"}, []string{"es_ES", "es"}},
		{"LC_MESSAGES beats LANG", map[string]string{"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "pt_BR.UTF-8"}, []string{"zh_CN", "zh"}},
		{"LANGUAGE first", map[string]string{"LANGUAGE": "es:ru", "LANG": "pt_BR.UTF-8"}, []string{"es", "ru", "pt_BR", "pt"}},
		{"English stops the list", map[string]string{"LANGUAGE": "en:pt_BR", "LANG": "pt_BR.UTF-8"}, nil},
		{"English locale", map[string]string{"LANG": "en_US.UTF-8"}, nil},
		{"English after a language", map[string]string{"LANGUAGE": "ru:en_GB:es", "LANG": "pt_BR.UTF-8"}, []string{"ru"}},
		{"path traversal rejected", map[string]string{"LANGUAGE": "../../etc:es", "LANG": "pt_BR.UTF-8"}, []string{"es", "pt_BR", "pt"}},
		{"duplicates collapsed", map[string]string{"LANGUAGE": "pt_BR:pt", "LANG": "pt_BR.UTF-8"}, []string{"pt_BR", "pt"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := languages(env(c.env)); !slices.Equal(got, c.want) {
				t.Errorf("languages() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseMOBothByteOrders(t *testing.T) {
	msgs := map[string]string{"Usage:": "Uso:", "help for %s": "ajuda para %s", "multi\nline": "várias\nlinhas"}
	for _, order := range []i18ntest.Order{binary.LittleEndian, binary.BigEndian} {
		m, err := parseMO(i18ntest.EncodeMO(msgs, order))
		if err != nil {
			t.Fatalf("%v: parseMO: %v", order, err)
		}
		for k, v := range msgs {
			if m.msgs[k] != v {
				t.Errorf("%v: %q -> %q, want %q", order, k, m.msgs[k], v)
			}
		}
		if _, ok := m.msgs[""]; ok {
			t.Errorf("%v: header entry was kept", order)
		}
	}
}

func TestParseMOSkipsContextAndTakesSingular(t *testing.T) {
	m, err := parseMO(i18ntest.EncodeMO(map[string]string{
		"menu\x04Open":         "Abrir (menu)",
		"one file\x00%d files": "um arquivo\x00%d arquivos",
	}, binary.LittleEndian))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.msgs["Open"]; ok {
		t.Error("a msgctxt entry was loaded")
	}
	if got := m.msgs["one file"]; got != "um arquivo" {
		t.Errorf("plural entry -> %q, want the first form", got)
	}
	if got := m.plurals["one file"]; len(got) != 2 || got[1] != "%d arquivos" {
		t.Errorf("plural forms = %q", got)
	}
}

func TestParseMORejectsCorruptFiles(t *testing.T) {
	good := i18ntest.EncodeMO(map[string]string{"a": "b"}, binary.LittleEndian)
	hugeCount := slices.Clone(good)
	binary.LittleEndian.PutUint32(hugeCount[8:], 0xffffffff)
	badOffset := slices.Clone(good)
	binary.LittleEndian.PutUint32(badOffset[12:], 0x7fffffff)
	badRevision := slices.Clone(good)
	binary.LittleEndian.PutUint32(badRevision[4:], 2<<16)
	cases := map[string][]byte{
		"empty":          nil,
		"short":          good[:20],
		"bad magic":      append([]byte{1, 2, 3, 4}, good[4:]...),
		"huge count":     hugeCount,
		"table offset":   badOffset,
		"major revision": badRevision,
		"truncated strs": good[:len(good)-3],
	}
	for name, data := range cases {
		if _, err := parseMO(data); err == nil {
			t.Errorf("%s: parseMO accepted a corrupt file", name)
		}
	}
}

func FuzzParseMO(f *testing.F) {
	f.Add(i18ntest.EncodeMO(map[string]string{"Usage:": "Uso:"}, binary.LittleEndian))
	f.Add(i18ntest.EncodeMO(map[string]string{"x": "y"}, binary.BigEndian))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseMO(data) // must not panic or allocate without bound
	})
}

func TestInitLoadsTheFirstAvailableLanguage(t *testing.T) {
	t.Cleanup(Reset)
	dir := t.TempDir()
	i18ntest.WriteCatalog(t, dir, "pt_BR", "dom", map[string]string{"Usage:": "Uso:", "Empty": ""})
	lang, err := initWith("dom", env(map[string]string{
		localeDirEnv: dir, "LANGUAGE": "ru:pt_BR", "LANG": "pt_BR.UTF-8",
	}))
	if err != nil || lang != "pt_BR" {
		t.Fatalf("initWith = %q, %v; want pt_BR, nil", lang, err)
	}
	if got := T("Usage:"); got != "Uso:" {
		t.Errorf("T(Usage:) = %q", got)
	}
	if got := T("Empty"); got != "Empty" {
		t.Errorf("an empty translation must fall back to the msgid, got %q", got)
	}
	if got := T("Missing"); got != "Missing" {
		t.Errorf("T(Missing) = %q", got)
	}
	Reset()
	if got := T("Usage:"); got != "Usage:" {
		t.Errorf("after Reset, T(Usage:) = %q", got)
	}
}

func TestInitFallsBackToTheBareLanguage(t *testing.T) {
	t.Cleanup(Reset)
	dir := t.TempDir()
	i18ntest.WriteCatalog(t, dir, "es", "dom", map[string]string{"Usage:": "Uso:"})
	lang, err := initWith("dom", env(map[string]string{localeDirEnv: dir, "LANG": "es_AR.UTF-8"}))
	if err != nil || lang != "es" {
		t.Fatalf("initWith = %q, %v; want es", lang, err)
	}
}

func TestInitWithoutCatalogStaysEnglish(t *testing.T) {
	t.Cleanup(Reset)
	lang, err := initWith("dom", env(map[string]string{localeDirEnv: t.TempDir(), "LANG": "ru_RU.UTF-8"}))
	if err != nil || lang != "" {
		t.Fatalf("initWith = %q, %v; want \"\", nil", lang, err)
	}
	if T("Usage:") != "Usage:" {
		t.Error("translated with no catalog")
	}
}

func TestInitReportsACorruptCatalog(t *testing.T) {
	t.Cleanup(Reset)
	dir := t.TempDir()
	p := filepath.Join(dir, "ru", "LC_MESSAGES")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "dom.mo"), []byte("not a catalog at all, clearly"), 0o644); err != nil {
		t.Fatal(err)
	}
	lang, err := initWith("dom", env(map[string]string{localeDirEnv: dir, "LANG": "ru_RU.UTF-8"}))
	if err == nil || !strings.Contains(err.Error(), "dom.mo") {
		t.Fatalf("initWith = %q, %v; want an error naming the file", lang, err)
	}
	if T("Usage:") != "Usage:" {
		t.Error("a failed Init left a catalog loaded")
	}
}

func TestInitUsesLocaleDirWithoutOverride(t *testing.T) {
	t.Cleanup(Reset)
	old := LocaleDir
	t.Cleanup(func() { LocaleDir = old })
	LocaleDir = i18ntest.WriteCatalog(t, t.TempDir(), "zh_CN", "dom", map[string]string{"Usage:": "用法："})
	if lang, err := initWith("dom", env(map[string]string{"LANG": "zh_CN.UTF-8"})); err != nil || lang != "zh_CN" {
		t.Fatalf("initWith = %q, %v", lang, err)
	}
	if T("Usage:") != "用法：" {
		t.Error("catalog under LocaleDir not used")
	}
}

func TestNReturnsItsArgument(t *testing.T) {
	if N("x") != "x" {
		t.Error("N changed its argument")
	}
}

func TestInitReadsTheProcessEnvironment(t *testing.T) {
	t.Cleanup(Reset)
	dir := i18ntest.WriteCatalog(t, t.TempDir(), "es", "dom", map[string]string{"Usage:": "Uso:"})
	t.Setenv(localeDirEnv, dir)
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "es_ES.UTF-8")
	if lang, err := Init("dom"); err != nil || lang != "es" {
		t.Fatalf("Init = %q, %v", lang, err)
	}
}
