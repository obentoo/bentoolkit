package notice

// Translated news items: GLEP 42 lets a news item carry other languages as
// `<id>.<lang>.txt` next to `<id>.en.txt`. Each copies every English header
// but Title, adds its Translator lines, and must keep the item's Revision.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func ptBRTranslation() Translation {
	return Translation{
		Lang: "pt_BR", Title: "Estouro de heap no foo 1.2",
		Translators: []string{"João Silva <joao@example.org>"},
		Body: "Um arquivo malicioso estoura um buffer de heap na libfoo quando é descompactado, " +
			"o que permite a um atacante executar código como o usuário que faz a extração.\n\n" +
			"O que fazer\n===========\n\n\tAtualize para dev-libs/foo-1.2.3 ou posterior e recompile todos os pacotes ligados a ela.",
	}
}

func zhCNTranslation() Translation {
	return Translation{
		Lang: "zh_CN", Title: "foo 1.2 堆溢出",
		Body: "精心构造的归档文件在解压时会使 libfoo 中的堆缓冲区溢出，攻击者可借此以执行解压操作的用户身份运行代码。" +
			"请立即升级到 dev-libs/foo-1.2.3 或更高版本，然后重新构建所有链接到它的软件包。",
	}
}

func TestRenderTranslation_MatchesTheGoldenFiles(t *testing.T) {
	n := goldenNotice()
	english, _ := RenderNews(n)
	for _, tr := range []Translation{ptBRTranslation(), zhCNTranslation()} {
		got := RenderTranslation(n, tr)
		compareGolden(t, "news."+tr.Lang+".golden.txt", []byte(got))

		// Every header but Title and Translator is the English item's.
		strip := func(s string) []string {
			head, _, _ := strings.Cut(s, "\n\n")
			var out []string
			for l := range strings.SplitSeq(head, "\n") {
				if !strings.HasPrefix(l, "Title: ") && !strings.HasPrefix(l, "Translator: ") {
					out = append(out, l)
				}
			}
			return out
		}
		if a, b := strip(got), strip(english); !slices.Equal(a, b) {
			t.Errorf("%s headers %q differ from the English %q", tr.Lang, a, b)
		}
		for i, l := range strings.Split(got, "\n") {
			if w := ansi.StringWidth(l); w > newsWidth {
				t.Errorf("%s line %d is %d columns wide: %q", tr.Lang, i+1, w, l)
			}
		}
	}
}

func TestWrapBody_BreaksCJKBetweenCharactersNotBeforeClosingPunctuation(t *testing.T) {
	line := strings.Repeat("中文，", 40) // 120 columns, a comma every two characters
	got := wrapBody(line)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("a 120-column line was not wrapped: %q", got)
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > newsWidth {
			t.Errorf("line is %d columns: %q", w, l)
		}
		if strings.HasPrefix(l, "，") {
			t.Errorf("line starts with closing punctuation: %q", l)
		}
	}
	if strings.Join(lines, "") != line {
		t.Errorf("wrapping changed the text:\n got %q\nwant %q", strings.Join(lines, ""), line)
	}
}

func TestWrapBody_OpeningPunctuationNeverEndsALine(t *testing.T) {
	// Without the rule, the break falls between "（" and "blocker".
	line := strings.Repeat("中", 34) + "（blocker）" + strings.Repeat("文", 30)
	for _, l := range strings.Split(strings.TrimSuffix(wrapBody(line), "\n"), "\n") {
		if strings.HasSuffix(l, "（") {
			t.Errorf("line ends with opening punctuation: %q", l)
		}
	}
}

func TestWrapBody_MixedScriptKeepsTheSpacesItHad(t *testing.T) {
	line := "请运行 emerge --ask dev-libs/foo 命令。" + strings.Repeat("这是一个很长的句子", 6)
	got := strings.TrimSuffix(wrapBody(line), "\n")
	if !strings.Contains(strings.ReplaceAll(got, "\n", ""), "请运行 emerge --ask dev-libs/foo 命令。") {
		t.Errorf("the command lost its spaces or was split: %q", got)
	}
}

func TestParseTranslation(t *testing.T) {
	ok := "Title: Título\nTranslator: A <a@example.org>\nTranslator: B <b@example.org>\n\n\nTexto.\n\n"
	tr, err := ParseTranslation("pt_BR", ok)
	if err != nil {
		t.Fatalf("ParseTranslation: %v", err)
	}
	want := Translation{Lang: "pt_BR", Title: "Título", Translators: []string{"A <a@example.org>", "B <b@example.org>"}, Body: "Texto."}
	if tr.Lang != want.Lang || tr.Title != want.Title || tr.Body != want.Body || !slices.Equal(tr.Translators, want.Translators) {
		t.Errorf("got %+v, want %+v", tr, want)
	}
	if tr, err := ParseTranslation("ru", "Title: Заголовок\r\n\r\nТекст.\r\n"); err != nil || tr.Body != "Текст." {
		t.Errorf("CRLF file: %+v, %v", tr, err)
	}

	for _, tc := range []struct {
		name, lang, text string
		want             error
	}{
		{"BCP 47 spelling", "pt-BR", ok, ErrLang},
		{"English", "en", ok, ErrLang},
		{"English region", "en_GB", ok, ErrLang},
		{"not a language", "Portuguese", ok, ErrLang},
		{"path in the language", "../x", ok, ErrLang},
		{"other header", "es", "Title: T\nPosted: 2026-01-01\n\nTexto.", ErrTranslation},
		{"no title", "es", "Translator: A <a@b>\n\nTexto.", ErrTitle},
		{"long title", "es", "Title: " + strings.Repeat("á", 51) + "\n\nTexto.", ErrTitle},
		{"empty translator", "es", "Title: T\nTranslator:\n\nTexto.", ErrTranslation},
		{"no body", "es", "Title: T\n", ErrEmptyBody},
		{"control character", "es", "Title: T\n\nTe\x1bxto.", ErrControlChar},
	} {
		if _, err := ParseTranslation(tc.lang, tc.text); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := ParseTranslation("pt-BR", ok); err == nil || !strings.Contains(err.Error(), `"pt_BR"`) {
		t.Errorf("the pt-BR error does not suggest pt_BR: %v", err)
	}
}

func TestReadTranslationFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "es.txt")
	if err := os.WriteFile(path, []byte("Title: Título\n\nTexto.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tr, err := ReadTranslationFile("es=" + path); err != nil || tr.Lang != "es" || tr.Title != "Título" {
		t.Errorf("got %+v, %v", tr, err)
	}
	for _, spec := range []string{"es", "=" + path, "es="} {
		if _, err := ReadTranslationFile(spec); !errors.Is(err, ErrTranslation) {
			t.Errorf("%q: err = %v, want ErrTranslation", spec, err)
		}
	}
	if _, err := ReadTranslationFile("es=" + path + ".missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
}

func TestNew_TranslationsAreValidatedAndSorted(t *testing.T) {
	in := Input{Type: "news", Severity: "info", Name: "x", Title: "T", Summary: "S",
		Author: "A <a@b>", Body: "B", Published: "2026-10-02",
		Translations: []Translation{zhCNTranslation(), ptBRTranslation()}}
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	n, err := New(in, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if n.Translations[0].Lang != "pt_BR" || n.Translations[1].Lang != "zh_CN" {
		t.Errorf("translations not sorted by language: %+v", n.Translations)
	}
	if in.Translations[0].Lang != "zh_CN" {
		t.Error("New reordered the caller's slice")
	}
	in.Translations = []Translation{ptBRTranslation(), ptBRTranslation()}
	if _, err := New(in, now); !errors.Is(err, ErrTranslation) {
		t.Errorf("duplicate language: err = %v", err)
	}
}

func publishTranslatedS(t *testing.T, withSite bool) reviseFixtureS071 {
	t.Helper()
	f := reviseSetupS071(t, withSite, "English text.")
	// reviseSetupS071 published without translations; start over with them.
	if err := os.RemoveAll(filepath.Dir(f.newsPath)); err != nil {
		t.Fatal(err)
	}
	if f.sitePath != "" {
		if err := os.Remove(f.sitePath); err != nil {
			t.Fatal(err)
		}
	}
	f.n.Translations = []Translation{ptBRTranslation(), zhCNTranslation()}
	res, err := Publish(context.Background(), f.n, f.overlay, f.site)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	want := []string{f.newsPath,
		TranslationPath(f.overlay, f.n.ID, "pt_BR"), TranslationPath(f.overlay, f.n.ID, "zh_CN")}
	if withSite {
		want = append(want, f.sitePath)
	}
	if !slices.Equal(res.Paths, want) {
		t.Errorf("Paths = %q, want %q", res.Paths, want)
	}
	f.newsBefore = readS071(t, f.newsPath)
	if withSite {
		f.siteBefore = readS071(t, f.sitePath)
	}
	return f
}

func TestPublish_WritesEveryTranslation(t *testing.T) {
	f := publishTranslatedS(t, true)
	got := readS071(t, TranslationPath(f.overlay, f.n.ID, "pt_BR"))
	if !strings.HasPrefix(got, "Title: Estouro de heap no foo 1.2\nAuthor: Jane Doe <jane@example.org>\n"+
		"Translator: João Silva <joao@example.org>\nPosted: 2026-10-02\nRevision: 1\n") {
		t.Errorf("pt_BR item:\n%s", got)
	}
	if strings.Contains(readS071(t, f.sitePath), "Estouro") {
		t.Error("a translation reached the site file, which is English only")
	}
}

func TestPublish_RefusesAnUnsafeLanguageBeforeWriting(t *testing.T) {
	f := reviseSetupS071(t, false, "x")
	n := f.n
	n.ID = "2026-10-02-other"
	n.Translations = []Translation{{Lang: "../../evil", Title: "T", Body: "B"}}
	if _, err := Publish(context.Background(), n, f.overlay, ""); !errors.Is(err, ErrLang) {
		t.Fatalf("err = %v, want ErrLang", err)
	}
	if _, err := os.Lstat(filepath.Join(f.overlay, "metadata", "news", n.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the item directory was created: %v", err)
	}
}

func TestRevise_KeepsEveryLanguageAtTheSameRevision(t *testing.T) {
	f := publishTranslatedS(t, true)
	// A language added by hand, outside langRe's form, is kept in step too.
	manual := TranslationPath(f.overlay, f.n.ID, "de")
	if err := os.WriteFile(manual, []byte("Title: Deutsch\nRevision: 1\n\nText.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newES := Translation{Lang: "es", Title: "Desbordamiento", Body: "Texto en español."}
	newPT := Translation{Lang: "pt_BR", Title: "Novo título", Body: "Texto novo."}
	res, err := Revise(context.Background(), f.n.ID,
		Changes{Affects: []string{"dev-libs/foo <2.0"}, Translations: []Translation{newES, newPT}},
		f.overlay, f.site, returnsS071("English text, revised."), revisedAtS071)
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	en := readS071(t, f.newsPath)
	for _, lang := range []string{"de", "es", "pt_BR", "zh_CN"} {
		got := readS071(t, TranslationPath(f.overlay, f.n.ID, lang))
		if r := headerS071(got, "Revision"); r != "2" {
			t.Errorf("%s Revision = %q, want 2", lang, r)
		}
		if d := headerS071(got, "Display-If-Installed"); d != headerS071(en, "Display-If-Installed") || d != "<dev-libs/foo-2.0" {
			t.Errorf("%s Display-If-Installed = %q, English %q", lang, d, headerS071(en, "Display-If-Installed"))
		}
		if !slices.Contains(res.Paths, TranslationPath(f.overlay, f.n.ID, lang)) {
			t.Errorf("%s not in Paths %q", lang, res.Paths)
		}
	}
	if got := readS071(t, TranslationPath(f.overlay, f.n.ID, "pt_BR")); headerS071(got, "Title") != "Novo título" ||
		!strings.HasSuffix(got, "\n\nTexto novo.\n") || headerS071(got, "Translator") != "" {
		t.Errorf("pt_BR was not replaced:\n%s", got)
	}
	if got := readS071(t, TranslationPath(f.overlay, f.n.ID, "zh_CN")); headerS071(got, "Title") != "foo 1.2 堆溢出" {
		t.Errorf("zh_CN lost its title:\n%s", got)
	}
	if got := readS071(t, manual); headerS071(got, "Title") != "Deutsch" || !strings.HasSuffix(got, "\n\nText.\n") {
		t.Errorf("de lost its text:\n%s", got)
	}
	var stale []string
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "translation ") {
			stale = append(stale, strings.Fields(w)[1])
		}
	}
	if !slices.Equal(stale, []string{"de", "zh_CN"}) {
		t.Errorf("stale-translation warnings for %q, want de and zh_CN: %q", stale, res.Warnings)
	}
}

func TestRevise_OnlyATranslationIsAChange(t *testing.T) {
	f := publishTranslatedS(t, false)
	res, err := Revise(context.Background(), f.n.ID,
		Changes{Translations: []Translation{{Lang: "ru", Title: "Заголовок", Body: "Текст."}}},
		f.overlay, "", echoS071(""), revisedAtS071)
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if r := headerS071(readS071(t, f.newsPath), "Revision"); r != "2" {
		t.Errorf("English Revision = %q, want 2", r)
	}
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "translation ") {
			t.Errorf("the English text did not change, yet: %q", w)
		}
	}
}

func TestRevise_RollsBackEveryFileWhenATranslationCannotBeWritten(t *testing.T) {
	f := publishTranslatedS(t, true)
	ptPath := TranslationPath(f.overlay, f.n.ID, "pt_BR")
	ptBefore := readS071(t, ptPath)
	// A directory where the zh_CN file goes: listing skips it, the rename onto
	// it fails, after the site, English and pt_BR files were replaced.
	zhPath := TranslationPath(f.overlay, f.n.ID, "zh_CN")
	if err := os.Remove(zhPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(zhPath, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Revise(context.Background(), f.n.ID,
		Changes{Translations: []Translation{zhCNTranslation()}},
		f.overlay, f.site, returnsS071("Changed."), revisedAtS071)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want a rolled-back write error", err)
	}
	f.assertUntouched(t, "a failed revision must restore every file")
	if got := readS071(t, ptPath); got != ptBefore {
		t.Errorf("pt_BR was not restored:\n%s", got)
	}
}

func TestRevise_RemovesANewTranslationOnRollback(t *testing.T) {
	f := publishTranslatedS(t, false)
	zhPath := TranslationPath(f.overlay, f.n.ID, "zh_CN")
	if err := os.Remove(zhPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(zhPath, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	esPath := TranslationPath(f.overlay, f.n.ID, "es")
	_, err := Revise(context.Background(), f.n.ID,
		Changes{Translations: []Translation{{Lang: "es", Title: "T", Body: "B"}, zhCNTranslation()}},
		f.overlay, "", echoS071(""), revisedAtS071)
	if err == nil {
		t.Fatal("Revise succeeded onto a directory")
	}
	if _, err := os.Lstat(esPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the new es file survived the rollback: %v", err)
	}
	f.assertUntouched(t, "rollback")
}

func TestRevise_MalformedTranslationIsRefused(t *testing.T) {
	f := publishTranslatedS(t, false)
	if err := os.WriteFile(TranslationPath(f.overlay, f.n.ID, "ru"), []byte("no headers"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Revise(context.Background(), f.n.ID, Changes{Title: "New"}, f.overlay, "",
		echoS071(""), revisedAtS071); !errors.Is(err, ErrMalformedNews) {
		t.Errorf("err = %v, want ErrMalformedNews", err)
	}
	f.assertUntouched(t, "malformed translation")
}
