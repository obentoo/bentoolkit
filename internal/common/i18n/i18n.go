// Package i18n translates bentoolkit's user-facing text with GNU gettext
// catalogs.
//
// A message is marked in the source with N (a no-op that returns its
// argument) where the text is declared, and translated with T where it is
// shown. The two are separate because much of the text is declared before
// main runs: cobra commands, for instance, are built by package-level
// initialisers, before the catalog can be loaded. misc/i18n-extract collects
// every literal passed to N or T into po/bentoolkit.pot.
//
// Translations are compiled .mo files under LocaleDir, one per language:
// <LocaleDir>/<lang>/LC_MESSAGES/<domain>.mo. They live outside the binary so
// a distribution can install only the languages the user asked for (Gentoo's
// L10N). With no catalog loaded, T returns its argument: English is the
// source language and the fallback.
//
// Only a binary's func main calls Init. Library code and tests never do, so
// tests see English whatever the host's locale and installed catalogs are.
package i18n

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
)

// LocaleDir is where Init looks for catalogs. The build overrides it with
// -ldflags "-X github.com/obentoo/bentoolkit/internal/common/i18n.LocaleDir=..."
// when the binaries are installed under another prefix.
var LocaleDir = "/usr/share/locale"

// localeDirEnv names the environment variable that overrides LocaleDir at run
// time, so a build tree can be tried before it is installed.
const localeDirEnv = "BENTOO_LOCALEDIR"

// catalog is the loaded translation table; nil means English. It is written
// by Init, normally once before any goroutine starts, and read by every T;
// the atomic keeps a late Init safe all the same.
var catalog atomic.Pointer[map[string]string]

// N marks s for extraction and returns it unchanged. Use it where text is
// declared before the catalog is loaded; translate it later with T.
func N(s string) string { return s }

// T returns the translation of msgid in the loaded catalog, or msgid itself
// when there is no catalog, no entry, or an empty translation.
func T(msgid string) string {
	m := catalog.Load()
	if m == nil {
		return msgid
	}
	if s, ok := (*m)[msgid]; ok && s != "" {
		return s
	}
	return msgid
}

// Init loads the catalog of domain for the language the environment selects
// (LANGUAGE, LC_ALL, LC_MESSAGES, LANG, as gettext reads them). It returns the
// language whose catalog was loaded, or "" when the text stays in English:
// a C or POSIX locale, an English one, or no installed catalog for it. An
// error means a catalog was found but could not be read; the text then stays
// in English too.
func Init(domain string) (string, error) {
	return initWith(domain, os.Getenv)
}

func initWith(domain string, getenv func(string) string) (string, error) {
	catalog.Store(nil)
	dir := LocaleDir
	if d := getenv(localeDirEnv); d != "" {
		dir = d
	}
	for _, lang := range languages(getenv) {
		path := filepath.Join(dir, lang, "LC_MESSAGES", domain+".mo")
		data, err := os.ReadFile(path) //nolint:gosec // G304: the path is built from the locale dir, a language name stripped to [A-Za-z_] and the fixed domain.
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("reading catalog %s: %w", path, err)
		}
		m, err := parseMO(data)
		if err != nil {
			return "", fmt.Errorf("parsing catalog %s: %w", path, err)
		}
		catalog.Store(&m)
		return lang, nil
	}
	return "", nil
}

// Reset unloads the catalog, so T returns English again. Tests that call Init
// use it to leave the package as they found it.
func Reset() { catalog.Store(nil) }
