package distfiles

// Authored for story 031, sub-task 1.1 — R4, R4.1.
//
// Written from the contract, not the implementation: design.md D2 fixes the
// signature `Locate(explicit, configured string) (string, bool)` and states the
// two things that separate it from Resolve — it creates nothing and it proves
// nothing writable. Everything below asserts one of those.
//
// Red is DEFERRED to Run mode: `Locate` does not exist yet, so this file does
// not compile, and a Go test cannot be compiled outside its package directory.
//
// The portageq seam helpers (stubPortageqOutput, stubPortageqUnavailable) come
// from resolve_test.go in this same package.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLocate_ExplicitWinsOverConfigured pins the top of the precedence. The
// host is stubbed to answer with a third directory that also exists, so a
// wrong precedence cannot pass by returning something plausible.
func TestLocate_ExplicitWinsOverConfigured(t *testing.T) {
	explicit := t.TempDir()
	configured := t.TempDir()
	call := stubPortageqOutput(t, t.TempDir())

	got, found := Locate(explicit, configured)

	if !found {
		t.Fatalf("Locate(%q, %q): got found=false, want true", explicit, configured)
	}
	if got != explicit {
		t.Errorf("Locate: got %q, want the explicit dir %q", got, explicit)
	}
	if call.calls != 0 {
		t.Errorf("portageq was asked %d times; an explicit distdir must not consult the host", call.calls)
	}
}

// TestLocate_ConfiguredWinsOverHost pins the middle rung. As above, the host
// answer is a real directory, so only the precedence can make this pass.
func TestLocate_ConfiguredWinsOverHost(t *testing.T) {
	configured := t.TempDir()
	call := stubPortageqOutput(t, t.TempDir())

	got, found := Locate("", configured)

	if !found {
		t.Fatalf("Locate(\"\", %q): got found=false, want true", configured)
	}
	if got != configured {
		t.Errorf("Locate: got %q, want the configured dir %q", got, configured)
	}
	if call.calls != 0 {
		t.Errorf("portageq was asked %d times; a configured distdir must not consult the host", call.calls)
	}
}

// TestLocate_FallsBackToHost pins the last rung.
func TestLocate_FallsBackToHost(t *testing.T) {
	host := t.TempDir()
	stubPortageqOutput(t, host)

	got, found := Locate("", "")

	if !found {
		t.Fatalf("Locate(\"\", \"\"): got found=false, want true")
	}
	if got != host {
		t.Errorf("Locate: got %q, want the host dir %q", got, host)
	}
}

// TestLocate_MissingDirectoryIsNotFound is the read-only contract's core case:
// a named directory that does not exist is reported absent, never conjured.
func TestLocate_MissingDirectoryIsNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-distdir")
	stubPortageqUnavailable(t)

	_, found := Locate(missing, "")

	if found {
		t.Errorf("Locate(%q): got found=true, want false for a directory that does not exist", missing)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("Locate created %q; the read-only locator must create nothing", missing)
	}
}

// TestLocate_NoCandidateIsNotFound covers the host having no answer either.
func TestLocate_NoCandidateIsNotFound(t *testing.T) {
	stubPortageqUnavailable(t)

	got, found := Locate("", "")

	if found {
		t.Errorf("Locate(\"\", \"\"): got found=true (%q), want false when no rung answers", got)
	}
}

// TestLocate_UnwritableDirectoryIsStillFound is the whole reason Locate exists
// next to Resolve. Resolve probes writability and fails here; Locate must not,
// because reading a distfile needs no write permission.
func TestLocate_UnwritableDirectoryIsStillFound(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: every directory is writable, so this assertion would pass for the wrong reason")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %q: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	got, found := Locate(dir, "")

	if !found {
		t.Fatalf("Locate(%q): got found=false; an unwritable but readable distdir is usable", dir)
	}
	if got != dir {
		t.Errorf("Locate: got %q, want %q", got, dir)
	}
}
