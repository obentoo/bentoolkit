package i18n

import "strings"

// languages returns the catalog names to try, best first, the way GNU gettext
// chooses them: the locale comes from the first non-empty of LC_ALL,
// LC_MESSAGES and LANG; a C or POSIX locale (or none) disables translation,
// LANGUAGE included; otherwise LANGUAGE, a colon-separated priority list,
// takes precedence over the locale. Each entry contributes its full name and
// then its bare language ("pt_BR.UTF-8" gives "pt_BR", "pt"). An English entry
// ends the list: English is the source text, so a user who ranks it above
// another language must get it.
func languages(getenv func(string) string) []string {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(name); v != "" {
			locale = v
			break
		}
	}
	if base := stripLocale(locale); base == "" || base == "C" || base == "POSIX" {
		return nil
	}

	var entries []string
	if v := getenv("LANGUAGE"); v != "" {
		entries = append(entries, strings.Split(v, ":")...)
	}
	entries = append(entries, locale)

	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		full := stripLocale(e)
		if full == "" || !validName(full) {
			continue
		}
		lang, _, _ := strings.Cut(full, "_")
		if lang == "en" {
			break
		}
		for _, c := range []string{full, lang} {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// stripLocale drops the codeset and modifier: "pt_BR.UTF-8@euro" -> "pt_BR".
func stripLocale(s string) string {
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	return s
}

// validName accepts only letters and underscores, so an entry read from the
// environment can never climb out of the locale directory.
func validName(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && r != '_' {
			return false
		}
	}
	return true
}
