package notice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Errors of the translations.
var (
	ErrLang        = errors.New("invalid translation language")
	ErrTranslation = errors.New("invalid translation")
)

// langRe is a translation's language as `eselect news` looks it up: the
// user's locale without codeset or modifier (`pt_BR`), or its bare language
// (`pt`). GLEP 42 names the file after an IETF language tag, but the
// reader builds the file name from $LANG, so a BCP 47 `pt-BR` file is never
// shown; for es, ru and the other bare languages both spellings agree.
var langRe = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)

// Translation is one translated news item: a file next to the English one,
// with every header copied from it except Title, plus its Translator lines.
// Only the English item is authoritative (GLEP 42), and only it feeds the
// site.
type Translation struct {
	// Lang is the file's language, `es` or `pt_BR` (see langRe).
	Lang string
	// Title is the translated title, 1..50 code points.
	Title string
	// Translators are the Translator headers, "Name <email>"; may be empty.
	Translators []string
	// Body is the translated text, wrapped like the English one.
	Body string
}

// ValidateLang checks a translation's language: a locale name as `eselect
// news` reads it, and not English, which is the notice itself.
func ValidateLang(s string) error {
	switch {
	case s == "en" || strings.HasPrefix(s, "en_"):
		return fmt.Errorf("translation %q: English is the notice itself: %w", s, ErrLang)
	case strings.Contains(s, "-"):
		return fmt.Errorf("translation %q: write it as %q, the name eselect news looks up from $LANG: %w",
			s, strings.Replace(s, "-", "_", 1), ErrLang)
	case !langRe.MatchString(s):
		return fmt.Errorf("translation %q: want a language such as es or pt_BR: %w", s, ErrLang)
	}
	return nil
}

// ParseTranslation reads a translation as the maintainer writes it: a
// `Title:` header, optional `Translator:` headers, a blank line, then the
// text. Any other header is refused, since the rest are copied from the
// English item. The result is validated like a notice's own fields.
func ParseTranslation(lang, text string) (Translation, error) {
	if err := ValidateLang(lang); err != nil {
		return Translation{}, err
	}
	head, body, _ := strings.Cut(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	t := Translation{Lang: lang, Body: trimBlankLines(body)}
	for line := range strings.SplitSeq(strings.TrimRight(head, "\n"), "\n") {
		name, value, ok := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch {
		case ok && name == "Title":
			t.Title = value
		case ok && name == "Translator":
			t.Translators = append(t.Translators, value)
		default:
			return Translation{}, fmt.Errorf("translation %s: header line %q: only Title and Translator are allowed: %w",
				lang, line, ErrTranslation)
		}
	}
	return t, validateTranslation(t)
}

// ReadTranslationFile reads a `--translation <lang>=<file>` value.
func ReadTranslationFile(spec string) (Translation, error) {
	lang, path, ok := strings.Cut(spec, "=")
	if !ok || lang == "" || path == "" {
		return Translation{}, fmt.Errorf("--translation %q: want <lang>=<file>: %w", spec, ErrTranslation)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the user's own input file, read and never executed
	if err != nil {
		return Translation{}, fmt.Errorf("reading --translation %s file %s: %w", lang, path, err)
	}
	return ParseTranslation(lang, string(data))
}

func validateTranslation(t Translation) error {
	if err := ValidateLang(t.Lang); err != nil {
		return err
	}
	if err := ValidateTitle(t.Title); err != nil {
		return fmt.Errorf("translation %s: %w", t.Lang, err)
	}
	for _, tr := range t.Translators {
		if tr == "" {
			return fmt.Errorf("translation %s: empty Translator header: %w", t.Lang, ErrTranslation)
		}
		if err := checkText("Translator", tr, true); err != nil {
			return fmt.Errorf("translation %s: %w", t.Lang, err)
		}
	}
	if t.Body == "" {
		return fmt.Errorf("translation %s: %w", t.Lang, ErrEmptyBody)
	}
	if err := checkText("body", t.Body, false); err != nil {
		return fmt.Errorf("translation %s: %w", t.Lang, err)
	}
	return nil
}

// validateTranslations checks every translation and refuses a language
// given twice.
func validateTranslations(ts []Translation) error {
	seen := map[string]bool{}
	for _, t := range ts {
		if err := validateTranslation(t); err != nil {
			return err
		}
		if seen[t.Lang] {
			return fmt.Errorf("translation %s given twice: %w", t.Lang, ErrTranslation)
		}
		seen[t.Lang] = true
	}
	return nil
}

// sortTranslations orders translations by language, so files are written
// and listed in a stable order.
func sortTranslations(ts []Translation) {
	slices.SortFunc(ts, func(a, b Translation) int { return strings.Compare(a.Lang, b.Lang) })
}

// TranslationPath is the news item of id in language lang:
// metadata/news/<id>/<id>.<lang>.txt.
func TranslationPath(overlay, id, lang string) string {
	return filepath.Join(overlay, "metadata", "news", id, id+"."+lang+".txt")
}

// RenderTranslation renders t as a news item of n: the English item's
// headers, with t's title and its Translator lines after Author, then t's
// body wrapped as the English one is.
func RenderTranslation(n Notice, t Translation) string {
	text, _ := renderItem(n, t.Title, t.Translators, t.Body)
	return text
}

// parseTranslationItem reads back a translated news item this package
// wrote: its title, translators and text. The other headers are the English
// item's and are rewritten from it.
func parseTranslationItem(lang, text string) (Translation, error) {
	head, body, ok := strings.Cut(text, "\n\n")
	if !ok {
		return Translation{}, fmt.Errorf("no blank line after the headers: %w", ErrMalformedNews)
	}
	t := Translation{Lang: lang, Body: strings.TrimRight(body, "\n")}
	for line := range strings.SplitSeq(head, "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok {
			return Translation{}, fmt.Errorf("header line %q: %w", line, ErrMalformedNews)
		}
		switch name {
		case "Title":
			t.Title = value
		case "Translator":
			t.Translators = append(t.Translators, value)
		}
	}
	if t.Title == "" {
		return Translation{}, fmt.Errorf("missing Title header: %w", ErrMalformedNews)
	}
	return t, nil
}

// readTranslations reads every translated item of id in newsDir: each
// `<id>.<lang>.txt` but the English one. A language not in langRe's form —
// a file added by hand — is still read, so a revision keeps its headers in
// step too.
func readTranslations(newsDir, id string) ([]Translation, error) {
	entries, err := os.ReadDir(newsDir)
	if err != nil {
		return nil, fmt.Errorf("listing news item directory %s: %w", newsDir, err)
	}
	var out []Translation
	for _, e := range entries {
		lang, ok := strings.CutPrefix(e.Name(), id+".")
		if !ok {
			continue
		}
		lang, ok = strings.CutSuffix(lang, ".txt")
		if !ok || lang == "en" || lang == "" || strings.ContainsAny(lang, "./") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(newsDir, e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // G304: a file listed in the news item's own directory
		if err != nil {
			return nil, fmt.Errorf("reading translated news item %s: %w", path, err)
		}
		t, err := parseTranslationItem(lang, string(data))
		if err != nil {
			return nil, fmt.Errorf("translated news item %s: %w", path, err)
		}
		out = append(out, t)
	}
	sortTranslations(out)
	return out, nil
}
