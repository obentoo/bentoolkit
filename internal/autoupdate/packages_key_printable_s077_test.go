package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Story 077: a quoted TOML key can hold any character, and a packages.toml key
// is printed raw by every message that names a record. A key holding a
// non-printable rune is refused by decodePackagesConfig — the one decoder every
// loader goes through — before anything downstream reads it.

// s077Record is a record body every check below accepts.
const s077Record = "url = \"https://example.test/releases\"\nparser = \"regex\"\npattern = 'v([0-9.]+)'\n"

// s077Table renders key as a TOML basic-string table header, escaping what a
// basic string cannot hold raw — the file stays valid TOML, so the decoder,
// not the syntax check, is what has to refuse it.
func s077Table(key, body string) string {
	return "[" + strconv.QuoteToASCII(key) + "]\n" + body + "\n"
}

var s077RawBytes = []string{"\x1b", "\n", "\u202e", "\u00a0"}

func s077AssertNoRaw(t *testing.T, text string) {
	t.Helper()
	for _, b := range s077RawBytes {
		if strings.Contains(text, b) {
			t.Errorf("the output carries the raw character %q: %q", b, text)
		}
	}
}

func TestS077DecodeRefusesNonPrintableKeys(t *testing.T) {
	for _, key := range []string{"cat/x\x1b[2J", "cat/x\nforged", "cat/\u202ex", "cat/x\u00a0y"} {
		_, err := decodePackagesConfig([]byte(s077Table(key, s077Record)))
		if err == nil {
			t.Errorf("the key %q was accepted", key)
			continue
		}
		if !errors.Is(err, ErrInvalidPackageKey) {
			t.Errorf("err = %v, want it to wrap ErrInvalidPackageKey", err)
		}
		if want := strconv.Quote(key); !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %q", want, err)
		}
		// The "\n" case is the forged-line attack: the only newline allowed
		// is none at all.
		s077AssertNoRaw(t, err.Error())
	}
}

func TestS077EveryOffenderIsNamedInSortedOrder(t *testing.T) {
	data := s077Table("cat/z\x1b", s077Record) + s077Table("cat/a\x1b", s077Record) + s077Table("cat/ok", s077Record)
	_, err := decodePackagesConfig([]byte(data))
	if err == nil {
		t.Fatal("the hostile keys were accepted")
	}
	msg := err.Error()
	a, z := strings.Index(msg, strconv.Quote("cat/a\x1b")), strings.Index(msg, strconv.Quote("cat/z\x1b"))
	if a < 0 || z < 0 || a > z {
		t.Errorf("both offenders are not named in sorted order: %q", msg)
	}
	if strings.Contains(msg, "cat/ok") {
		t.Errorf("a printable key is named as an offender: %q", msg)
	}
}

// UnknownKeysError prints the record name raw, so the printable check must win.
func TestS077NonPrintableKeyBeatsUnknownField(t *testing.T) {
	_, err := decodePackagesConfig([]byte(s077Table("cat/x\x1b", s077Record+"serie = \"1\"\n")))
	var unknown *UnknownKeysError
	if errors.As(err, &unknown) {
		t.Fatalf("an unknown-field error, which prints the key raw, won: %v", err)
	}
	if !errors.Is(err, ErrInvalidPackageKey) {
		t.Fatalf("err = %v, want ErrInvalidPackageKey", err)
	}
}

func TestS077LintNamesNoRawKey(t *testing.T) {
	overlay := t.TempDir()
	dir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := s077Table("cat/x\x1b[2J", s077Record)
	if err := os.WriteFile(filepath.Join(dir, "packages.toml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err := LintPackagesConfig(nil, overlay)
	if !errors.Is(err, ErrInvalidPackageKey) {
		t.Fatalf("LintPackagesConfig err = %v, want ErrInvalidPackageKey", err)
	}
	s077AssertNoRaw(t, err.Error())
	for _, i := range issues {
		s077AssertNoRaw(t, i.String())
	}
}

// The converse: printable keys load as before, and the other load errors keep
// their text and their precedence.
func TestS077PrintableKeysAndOtherErrorsUnchanged(t *testing.T) {
	data := s077Table("cat/pkg", s077Record) + s077Table("cat/pkg:1.2", s077Record) +
		s077Table("cat/pkg@stable", s077Record) + s077Table("../x", s077Record)
	cfg, err := decodePackagesConfig([]byte(data))
	if err != nil {
		t.Fatalf("printable keys were refused: %v", err)
	}
	if len(cfg.Packages) != 4 {
		t.Fatalf("decoded %d records, want 4", len(cfg.Packages))
	}
	// A printable but malformed key still loads and is refused per record.
	if verr := ValidatePackageConfig(nil, "../x", new(cfg.Packages["../x"])); !errors.Is(verr, ErrInvalidPackageKey) {
		t.Errorf("ValidatePackageConfig(nil, ../x) = %v, want ErrInvalidPackageKey", verr)
	}

	if _, err := decodePackagesConfig([]byte("[\"cat/pkg\"\nurl = 1\n")); err == nil || !strings.Contains(err.Error(), "failed to parse packages.toml") {
		t.Errorf("a syntax error lost its text: %v", err)
	}

	_, err = decodePackagesConfig([]byte(s077Table("cat/pkg", s077Record+"serie = \"1\"\n")))
	var unknown *UnknownKeysError
	if !errors.As(err, &unknown) {
		t.Errorf("an unknown field under a printable key = %v, want UnknownKeysError", err)
	}
}

// A raw ESC byte in the file is a TOML syntax error, but --lint's text scan
// reads the headers before the parser runs and names the record it found.
func TestS077LintTextScanNamesNoRawKey(t *testing.T) {
	overlay := t.TempDir()
	dir := filepath.Join(overlay, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	data := "[\"cat/x\x1b[2Jforged\"]\n" + s077Record
	if err := os.WriteFile(filepath.Join(dir, "packages.toml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	issues, err := LintPackagesConfig(nil, overlay)
	if err == nil {
		t.Fatal("a file holding a raw ESC byte loaded")
	}
	s077AssertNoRaw(t, err.Error())
	if len(issues) == 0 {
		t.Fatal("the text scan reported nothing, so this test no longer reaches the renderer")
	}
	for _, i := range issues {
		s077AssertNoRaw(t, i.String())
	}
	if got := (LintIssue{Package: "cat/pkg", Rule: "r", Message: "m"}).String(); got != "packages.toml: [cat/pkg] r: m" {
		t.Errorf("a printable key renders differently: %q", got)
	}
}
