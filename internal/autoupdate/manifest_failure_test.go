package autoupdate

import (
	"bytes"
	"errors"
	"fmt"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// errPkgdevExit stands in for the *exec.ExitError a real `pkgdev manifest`
// returns. It is a stable value so every test can assert that the ORIGINAL
// error is still findable with errors.Is inside whatever the classifier
// returns — which is what keeps the operator's diagnostic intact when the gate
// fires (D5).
var errPkgdevExit = errors.New("exit status 1")

// The two outputs below are DATA, not prose: they are the bytes pkgdev writes,
// and the whole point of this file is that the classifier must not read them.
//
// localizedManifestOutput is the shape this defect produces on the machine it
// was measured on (M5: LANG=LC_MESSAGES=pt_BR.UTF-8). englishManifestOutput is
// the same failure under a C locale, and it deliberately contains the two
// phrases a message-matching classifier would key on — "Cannot write to" and
// "No space left on device" — so a test can hand it to a HEALTHY distdir and
// prove the verdict does not come from the text.
const (
	localizedManifestOutput = "Não foi possível gravar em '/tmp/bentoo-distfiles-2526879428/zed-1.16.0_pre20260806.tar.gz' (Sucesso).\n" +
		"  * falha ao obter os arquivos do pacote app-editors/zed::bentoo\n" +
		"pkgdev manifest: erro: falha ao obter os distfiles necessários\n"

	englishManifestOutput = "Cannot write to '/tmp/bentoo-distfiles-2526879428/zed-1.16.0_pre20260806.tar.gz' (Success).\n" +
		"  * failed fetching files for package app-editors/zed::bentoo\n" +
		"pkgdev manifest: error: failed fetching required distfiles\n" +
		"wget: No space left on device\n"
)

// distfileUnderTest is the artefact name the measured failure names.
const distfileUnderTest = "zed-1.16.0_pre20260806.tar.gz"

// manifestFailure builds an error shaped like the one runManifest returns:
// a wrapped exit error plus the captured output.
func manifestFailure(output string) error {
	return fmt.Errorf("command failed: %w\nOutput: %s", errPkgdevExit, output)
}

// roomySpace is a SpaceFunc reporting a filesystem with 64 GiB free, i.e. one
// that cannot make the free-space check fire. Tests about the OTHER checks use
// it so a real disk's fullness can never decide their verdict.
func roomySpace(string) (uint64, error) { return 64 << 30, nil }

// fixedSpace is a SpaceFunc that always answers free.
func fixedSpace(free uint64) SpaceFunc {
	return func(string) (uint64, error) { return free, nil }
}

// refusingSpace is a SpaceFunc that cannot answer. It reports zero ALONGSIDE
// the error on purpose: a classifier that read the count without checking the
// error would see "0 bytes free" and declare an environment failure, which is
// exactly the R3.5 violation this seam exists to make visible.
func refusingSpace(string) (uint64, error) {
	return 0, errors.New("statfs is unavailable on this filesystem")
}

// unwritableDistdir returns a directory the invoking user cannot write into,
// restoring its mode afterwards so t.TempDir's own cleanup can still remove it.
// Mode 0500 keeps it readable and searchable — the classifier stats its
// contents — while denying the probe's create.
//
// It SKIPS as root: mode bits do not apply to uid 0, the probe would succeed,
// and the test would pass for a reason unrelated to the code. It also skips
// when the filesystem does not enforce 0500, for the same reason. A green that
// measured nothing is the same shape of defect this story is about.
func unwritableDistdir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipping: running as root (euid 0), where mode 0500 does not deny a write; " +
			"passing here would be vacuous. CI runs unprivileged.")
	}

	dir := filepath.Join(t.TempDir(), "distfiles")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("create distdir: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod %q to 0500: %v", dir, err)
	}
	// Registered after t.TempDir's cleanup, so it runs BEFORE it (cleanups are
	// LIFO) and the temp tree can still be removed.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	canary := filepath.Join(dir, "canary")
	if err := os.WriteFile(canary, []byte("x"), 0o600); err == nil {
		_ = os.Remove(canary)
		t.Skipf("skipping: %q is still writable at mode 0500, so this filesystem does not enforce "+
			"the permission this test depends on", dir)
	}
	return dir
}

// writeFileOfSize creates name inside dir with size bytes and returns its path.
func writeFileOfSize(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, size), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}

// TestClassifyUnwritableDistdirIsEnvironment is D5's first check: a distdir
// that passed the pre-flight and cannot be written to now has changed under the
// run, and no edit to an ebuild can fix that.
func TestClassifyUnwritableDistdirIsEnvironment(t *testing.T) {
	t.Run("directory the user cannot write into", func(t *testing.T) {
		dir := unwritableDistdir(t)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{distfileUnderTest}, roomySpace)

		if !errors.Is(got, ErrManifestEnvironment) {
			t.Fatalf("ClassifyManifestFailure() = %v, want an error satisfying errors.Is(_, ErrManifestEnvironment)", got)
		}
		// The reason travels too: the gate reports WHY, and the order test below
		// relies on being able to tell the three verdicts apart.
		if !errors.Is(got, distfiles.ErrDistdirNotWritable) {
			t.Errorf("verdict does not carry distfiles.ErrDistdirNotWritable; got %v", got)
		}
		if !strings.Contains(got.Error(), dir) {
			t.Errorf("verdict does not name the directory %q: %v", dir, got)
		}
	})

	t.Run("directory that no longer exists", func(t *testing.T) {
		// A distdir on a mount that went away mid-run is the same class of
		// failure: state, not text, says so.
		dir := filepath.Join(t.TempDir(), "vanished")
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, nil, roomySpace)

		if !errors.Is(got, ErrManifestEnvironment) {
			t.Fatalf("ClassifyManifestFailure() on a missing distdir = %v, want ErrManifestEnvironment", got)
		}
	})
}

// TestClassifyInsufficientFreeSpaceIsEnvironment is D5's second check. The
// filesystem is never filled: the query is injected, which is what the seam is
// for (design "Tooling Decisions").
func TestClassifyInsufficientFreeSpaceIsEnvironment(t *testing.T) {
	tests := []struct {
		name string
		free uint64
		want bool // want the environment verdict
	}{
		{name: "device completely full", free: 0, want: true},
		{name: "one block left", free: 4096, want: true},
		{name: "one byte below the floor", free: minimumUsableFreeBytes - 1, want: true},
		{name: "exactly at the floor", free: minimumUsableFreeBytes, want: false},
		{name: "well above the floor", free: 64 << 30, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			orig := manifestFailure(englishManifestOutput)

			got := ClassifyManifestFailure(dir, orig, nil, fixedSpace(tc.free))

			if tc.want {
				if !errors.Is(got, ErrManifestEnvironment) {
					t.Fatalf("free=%d: ClassifyManifestFailure() = %v, want ErrManifestEnvironment", tc.free, got)
				}
				if !errors.Is(got, errDistdirFull) {
					t.Errorf("free=%d: verdict does not carry errDistdirFull: %v", tc.free, got)
				}
				return
			}
			if got != orig { //nolint:errorlint // identity is the assertion: R3.5 wants the SAME value back
				t.Fatalf("free=%d: ClassifyManifestFailure() = %v, want the original error unchanged", tc.free, got)
			}
		})
	}

	t.Run("the query is asked about the distdir", func(t *testing.T) {
		dir := t.TempDir()
		var asked []string
		space := func(d string) (uint64, error) {
			asked = append(asked, d)
			return 64 << 30, nil
		}

		ClassifyManifestFailure(dir, manifestFailure(englishManifestOutput), nil, space) //nolint:errcheck // verdict irrelevant here

		if len(asked) != 1 || asked[0] != dir {
			t.Fatalf("space func was asked about %v, want exactly [%q]", asked, dir)
		}
	})
}

// TestClassifyZeroLengthArtefactIsEnvironment is D5's third check: a download
// killed before its first byte landed leaves the name behind with nothing in
// it. The sub-tests that must NOT fire are the more important half — each is a
// state a wrong implementation would misread as "empty".
func TestClassifyZeroLengthArtefactIsEnvironment(t *testing.T) {
	t.Run("empty artefact under an expected name", func(t *testing.T) {
		dir := t.TempDir()
		writeFileOfSize(t, dir, distfileUnderTest, 0)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{distfileUnderTest}, roomySpace)

		if !errors.Is(got, ErrManifestEnvironment) {
			t.Fatalf("ClassifyManifestFailure() = %v, want ErrManifestEnvironment", got)
		}
		if !errors.Is(got, errArtefactEmpty) {
			t.Errorf("verdict does not carry errArtefactEmpty: %v", got)
		}
		if !strings.Contains(got.Error(), distfileUnderTest) {
			t.Errorf("verdict does not name the artefact %q: %v", distfileUnderTest, got)
		}
	})

	t.Run("expected name with a directory component is reduced, not refused", func(t *testing.T) {
		// The classifier must inspect the SAME file internal/common/distfiles
		// manages under this name — its distfileName reduces with filepath.Base
		// too, so Quarantine and RecordFetchScope would both be talking about
		// <distdir>/zed-….tar.gz. A classifier that refused any name carrying a
		// separator instead of reducing it would silently inspect a different
		// set than the one being fetched, and never see the empty artefact.
		dir := t.TempDir()
		writeFileOfSize(t, dir, distfileUnderTest, 0)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{"mirror://gentoo/" + distfileUnderTest}, roomySpace)

		if !errors.Is(got, errArtefactEmpty) {
			t.Fatalf("ClassifyManifestFailure() = %v, want the empty-artefact verdict: %q must reduce to %q, "+
				"which is the name the distfiles package manages", got, "mirror://gentoo/"+distfileUnderTest, distfileUnderTest)
		}
	})

	t.Run("empty artefact found among several expected names", func(t *testing.T) {
		dir := t.TempDir()
		writeFileOfSize(t, dir, "first.tar.gz", 12)
		writeFileOfSize(t, dir, distfileUnderTest, 0)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{"first.tar.gz", "absent.tar.gz", distfileUnderTest}, roomySpace)

		if !errors.Is(got, errArtefactEmpty) {
			t.Fatalf("ClassifyManifestFailure() = %v, want the empty artefact to be found behind other names", got)
		}
	})

	// Everything below is a state that must fall through to "repairable".
	repairable := []struct {
		name        string
		setup       func(t *testing.T, dir string) []string // returns expected
		whyNotEmpty string
	}{
		{
			name: "artefact with bytes in it",
			setup: func(t *testing.T, dir string) []string {
				writeFileOfSize(t, dir, distfileUnderTest, 4096)
				return []string{distfileUnderTest}
			},
			whyNotEmpty: "a partial download is not an empty one",
		},
		{
			name: "artefact absent",
			setup: func(t *testing.T, dir string) []string {
				return []string{distfileUnderTest}
			},
			whyNotEmpty: "nothing there is not evidence of anything",
		},
		{
			name: "directory under the expected name",
			setup: func(t *testing.T, dir string) []string {
				if err := os.Mkdir(filepath.Join(dir, "git3-src"), 0o750); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				return []string{"git3-src"}
			},
			whyNotEmpty: "a distdir holds host-owned subdirectories; a directory is not a distfile",
		},
		{
			// A directory's reported size is filesystem-dependent — 40 bytes on
			// tmpfs, 0 on btrfs — so the case above only exercises the mode check
			// on some hosts. A FIFO reports size 0 everywhere, which is what
			// makes this the deterministic half: an entry with no bytes that is
			// not a regular file is still not this fetch's artefact.
			name: "zero-length entry that is not a regular file",
			setup: func(t *testing.T, dir string) []string {
				if err := syscall.Mkfifo(filepath.Join(dir, distfileUnderTest), 0o600); err != nil {
					t.Skipf("skipping: cannot create a FIFO here (%v)", err)
				}
				info, err := os.Lstat(filepath.Join(dir, distfileUnderTest))
				if err != nil {
					t.Fatalf("lstat fifo: %v", err)
				}
				if info.Size() != 0 {
					t.Skipf("skipping: the FIFO reports %d bytes here, so it does not separate the size "+
						"check from the mode check", info.Size())
				}
				return []string{distfileUnderTest}
			},
			whyNotEmpty: "only a regular file with no bytes is an artefact a fetch left behind",
		},
		{
			name: "symlink to an empty cached file",
			setup: func(t *testing.T, dir string) []string {
				cache := t.TempDir()
				writeFileOfSize(t, cache, distfileUnderTest, 0)
				if err := os.Symlink(filepath.Join(cache, distfileUnderTest), filepath.Join(dir, distfileUnderTest)); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				return []string{distfileUnderTest}
			},
			whyNotEmpty: "PrepopulateFromCache leaves links here; following one asks about a file this run never fetched",
		},
		{
			name: "dangling symlink",
			setup: func(t *testing.T, dir string) []string {
				if err := os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, distfileUnderTest)); err != nil {
					t.Fatalf("symlink: %v", err)
				}
				return []string{distfileUnderTest}
			},
			whyNotEmpty: "an unresolvable link answers nothing about size",
		},
		{
			name: "expected name that traverses out of the distdir",
			setup: func(t *testing.T, dir string) []string {
				// An empty file OUTSIDE the distdir, reachable only by escaping
				// it. Reducing the name to its base keeps the lookup inside,
				// where nothing exists — so the verdict must be repairable.
				writeFileOfSize(t, filepath.Dir(dir), "escaped.tar.gz", 0)
				return []string{"../escaped.tar.gz"}
			},
			whyNotEmpty: "an untrusted name must not let the classifier read a file outside the distdir",
		},
		{
			name: "names that do not reduce to a filename",
			setup: func(t *testing.T, dir string) []string {
				return []string{"", ".", "..", "/", `sub\dir.tar.gz`}
			},
			whyNotEmpty: "none of these names a file in the distdir",
		},
		{
			name: "no expected names at all",
			setup: func(t *testing.T, dir string) []string {
				writeFileOfSize(t, dir, distfileUnderTest, 0)
				return nil
			},
			whyNotEmpty: "an empty file nobody expected is not this run's artefact",
		},
	}

	for _, tc := range repairable {
		t.Run(tc.name, func(t *testing.T) {
			// A nested directory so "traverses out of the distdir" writes into a
			// temp dir rather than anywhere real.
			dir := filepath.Join(t.TempDir(), "distfiles")
			if err := os.Mkdir(dir, 0o750); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			expected := tc.setup(t, dir)
			orig := manifestFailure(englishManifestOutput)

			got := ClassifyManifestFailure(dir, orig, expected, roomySpace)

			if got != orig { //nolint:errorlint // identity is the assertion
				t.Fatalf("ClassifyManifestFailure() = %v, want the original error unchanged (%s)", got, tc.whyNotEmpty)
			}
		})
	}
}

// TestClassifyUnknownFailureStaysRepairable is R3.5, the fall-through. Every
// state here is one the classifier cannot read an answer out of, and every one
// of them must leave today's repair path open — returning the SAME error value,
// so a caller that already tests it keeps finding what it tested for.
func TestClassifyUnknownFailureStaysRepairable(t *testing.T) {
	t.Run("healthy distdir, nothing observable wrong", func(t *testing.T) {
		dir := t.TempDir()
		writeFileOfSize(t, dir, distfileUnderTest, 1024)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{distfileUnderTest}, roomySpace)

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("ClassifyManifestFailure() = %v, want the original error value unchanged", got)
		}
		if errors.Is(got, ErrManifestEnvironment) {
			t.Errorf("a healthy distdir produced an environment verdict: %v", got)
		}
	})

	t.Run("the space query cannot answer", func(t *testing.T) {
		// The check is unanswerable, and unanswerable is not "full". This is the
		// single most important fall-through: refusingSpace reports 0 free
		// alongside its error, so an implementation that ignored the error would
		// gate the fixer away on the strength of knowing nothing.
		dir := t.TempDir()
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, nil, refusingSpace)

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("ClassifyManifestFailure() = %v, want the original error back when free space is unknowable", got)
		}
	})

	t.Run("nil space func falls back to the real query, not to a verdict", func(t *testing.T) {
		// A nil seam must not silently disable a safety check, and must not
		// manufacture one either: on a healthy temp dir the real statfs answers
		// plenty, so the verdict is repairable.
		dir := t.TempDir()
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, nil, nil)

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("ClassifyManifestFailure() with a nil SpaceFunc = %v, want the original error unchanged", got)
		}
	})

	t.Run("nil space func still RUNS the check", func(t *testing.T) {
		// The sub-test above cannot tell "asked the real query and got plenty"
		// apart from "skipped the check": both answer repairable on a healthy
		// machine. Swapping the default query is the only way to see the
		// difference, and the difference is whether the full-disk gate exists at
		// all in production, where nobody injects a SpaceFunc.
		original := defaultSpaceQuery
		t.Cleanup(func() { defaultSpaceQuery = original })
		asked := 0
		defaultSpaceQuery = func(string) (uint64, error) {
			asked++
			return 0, nil
		}

		got := ClassifyManifestFailure(t.TempDir(), manifestFailure(englishManifestOutput), nil, nil)

		if asked != 1 {
			t.Fatalf("the default space query was asked %d time(s), want exactly 1; a nil seam disabled the check", asked)
		}
		if !errors.Is(got, errDistdirFull) {
			t.Fatalf("ClassifyManifestFailure() with a nil SpaceFunc on a full filesystem = %v, want the free-space verdict", got)
		}
	})

	t.Run("nil error is not a failure to classify", func(t *testing.T) {
		if got := ClassifyManifestFailure(t.TempDir(), nil, nil, nil); got != nil {
			t.Fatalf("ClassifyManifestFailure(_, nil, _, _) = %v, want nil", got)
		}
		// Even with a broken environment: a step that did not fail has nothing
		// to classify.
		dir := unwritableDistdir(t)
		if got := ClassifyManifestFailure(dir, nil, []string{distfileUnderTest}, fixedSpace(0)); got != nil {
			t.Fatalf("ClassifyManifestFailure() with a nil error on a broken distdir = %v, want nil", got)
		}
	})

	t.Run("empty distdir is unanswerable, never a verdict", func(t *testing.T) {
		// It must also never inspect anything: with distdir "",
		// filepath.Join("", name) would resolve against the WORKING directory —
		// this package's own source tree, where an empty file would otherwise
		// decide the verdict.
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure("", orig, []string{distfileUnderTest}, fixedSpace(0))

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("ClassifyManifestFailure(\"\", ...) = %v, want the original error unchanged", got)
		}
	})

	t.Run("a distdir path that is a file, not a directory", func(t *testing.T) {
		// The probe cannot create anything inside a regular file, so this is an
		// environment verdict rather than a fall-through — asserted here because
		// it is the one "malformed input" case that legitimately fires.
		dir := t.TempDir()
		file := writeFileOfSize(t, dir, "not-a-directory", 3)
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(file, orig, nil, roomySpace)

		if !errors.Is(got, ErrManifestEnvironment) {
			t.Fatalf("ClassifyManifestFailure() on a non-directory distdir = %v, want ErrManifestEnvironment", got)
		}
	})
}

// TestClassifyIgnoresLocalizedMessageText is M5 made executable. The classifier
// must reach the same verdict whatever language the child process printed in,
// which means the text can neither produce a verdict nor prevent one.
func TestClassifyIgnoresLocalizedMessageText(t *testing.T) {
	t.Run("portuguese text, broken environment, still environment", func(t *testing.T) {
		dir := unwritableDistdir(t)

		got := ClassifyManifestFailure(dir, manifestFailure(localizedManifestOutput), nil, roomySpace)

		if !errors.Is(got, ErrManifestEnvironment) {
			t.Fatalf("localized failure on an unwritable distdir = %v, want ErrManifestEnvironment; "+
				"a classifier that greps 'Cannot write to' answers repairable here", got)
		}
	})

	t.Run("english ENOSPC text, healthy environment, still repairable", func(t *testing.T) {
		// This is the assertion that kills a message-matching implementation:
		// the output literally says "Cannot write to" and "No space left on
		// device", the machine is fine, and the verdict must be repairable.
		dir := t.TempDir()
		orig := manifestFailure(englishManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{distfileUnderTest}, roomySpace)

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("healthy distdir with ENOSPC-shaped English text = %v, want the original error unchanged; "+
				"the verdict came from the message, not from state", got)
		}
	})

	t.Run("portuguese text, healthy environment, repairable", func(t *testing.T) {
		dir := t.TempDir()
		orig := manifestFailure(localizedManifestOutput)

		got := ClassifyManifestFailure(dir, orig, []string{distfileUnderTest}, roomySpace)

		if got != orig { //nolint:errorlint // identity is the assertion
			t.Fatalf("healthy distdir with localized text = %v, want the original error unchanged", got)
		}
	})

	t.Run("same state, two languages, same verdict", func(t *testing.T) {
		for _, output := range []string{localizedManifestOutput, englishManifestOutput, ""} {
			dir := t.TempDir()
			writeFileOfSize(t, dir, distfileUnderTest, 0)

			got := ClassifyManifestFailure(dir, manifestFailure(output), []string{distfileUnderTest}, roomySpace)

			if !errors.Is(got, errArtefactEmpty) {
				t.Fatalf("output %q: verdict = %v, want the empty-artefact verdict regardless of the text "+
					"(an empty message is the extreme case: there is nothing to match on at all)", output, got)
			}
		}
	})

	t.Run("the source cannot read messages at all", func(t *testing.T) {
		// A structural assertion, because the behavioural ones above can only
		// sample states. Comments are stripped before scanning: a gate that
		// matched the comment documenting the rule would fail on prose and pass
		// on code, which is the wrong way round.
		const src = "manifest_failure.go"

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, file); err != nil {
			t.Fatalf("print %s: %v", src, err)
		}
		code := buf.String()

		for _, forbidden := range []string{".Error()", "strings.", "regexp.", "fmt.Sscan", "bytes.Contains"} {
			if strings.Contains(code, forbidden) {
				t.Errorf("%s contains %q: the verdict must come from state, never from message text (M5, R3.2)", src, forbidden)
			}
		}
		for _, imp := range file.Imports {
			switch imp.Path.Value {
			case `"strings"`, `"regexp"`:
				t.Errorf("%s imports %s; a classifier that cannot be tempted to read text is one that does not import a text search", src, imp.Path.Value)
			}
		}
		// Guard against the scan going vacuous: if the file ever stops being
		// found or stops containing the function, everything above passes for
		// the wrong reason.
		if !strings.Contains(code, "func ClassifyManifestFailure(") {
			t.Fatalf("%s no longer defines ClassifyManifestFailure; the assertions above measured nothing", src)
		}
	})
}

// TestClassifyWrapsOriginalErrorForErrorsIs pins the contract D5 states: the
// verdict is testable with errors.Is AND the original failure survives inside
// it, so gating the fixer never costs the operator the diagnostic.
func TestClassifyWrapsOriginalErrorForErrorsIs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (distdir string, expected []string, space SpaceFunc)
	}{
		{
			name: "unwritable distdir",
			setup: func(t *testing.T) (string, []string, SpaceFunc) {
				return unwritableDistdir(t), nil, roomySpace
			},
		},
		{
			name: "no free space",
			setup: func(t *testing.T) (string, []string, SpaceFunc) {
				return t.TempDir(), nil, fixedSpace(0)
			},
		},
		{
			name: "empty artefact",
			setup: func(t *testing.T) (string, []string, SpaceFunc) {
				dir := t.TempDir()
				writeFileOfSize(t, dir, distfileUnderTest, 0)
				return dir, []string{distfileUnderTest}, roomySpace
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, expected, space := tc.setup(t)
			orig := manifestFailure(localizedManifestOutput)

			got := ClassifyManifestFailure(dir, orig, expected, space)

			if !errors.Is(got, ErrManifestEnvironment) {
				t.Fatalf("errors.Is(_, ErrManifestEnvironment) = false for %v", got)
			}
			if !errors.Is(got, errPkgdevExit) {
				t.Errorf("the original error is no longer findable with errors.Is: %v", got)
			}
			if !errors.Is(got, orig) {
				t.Errorf("the original error value is no longer in the chain: %v", got)
			}
			// The operator still reads what pkgdev printed, in whatever language
			// it printed it.
			if !strings.Contains(got.Error(), "falha ao obter os distfiles necessários") {
				t.Errorf("the verdict dropped the original output text: %v", got)
			}
		})
	}
}

// TestClassifyChecksStateInDesignOrder pins D5's ORDER, not just its verdicts.
// The table is written "in order" because an unwritable distdir explains an
// empty artefact: reporting the artefact would name the symptom and hide the
// cause, and an operator would go looking at the wrong thing.
func TestClassifyChecksStateInDesignOrder(t *testing.T) {
	t.Run("unwritable wins over full and over empty", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "distfiles")
		if err := os.Mkdir(dir, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeFileOfSize(t, dir, distfileUnderTest, 0)
		if os.Geteuid() == 0 {
			t.Skip("skipping: mode 0500 does not deny a write to root; the ordering could not be observed")
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if err := os.WriteFile(filepath.Join(dir, "canary"), []byte("x"), 0o600); err == nil {
			t.Skip("skipping: this filesystem does not enforce mode 0500")
		}

		// All three checks would fire.
		got := ClassifyManifestFailure(dir, manifestFailure(localizedManifestOutput), []string{distfileUnderTest}, fixedSpace(0))

		if !errors.Is(got, distfiles.ErrDistdirNotWritable) {
			t.Fatalf("verdict = %v, want the writability verdict to win", got)
		}
		if errors.Is(got, errDistdirFull) || errors.Is(got, errArtefactEmpty) {
			t.Errorf("verdict = %v, want ONLY the writability verdict; a later check reported over an earlier one", got)
		}
	})

	t.Run("full wins over empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFileOfSize(t, dir, distfileUnderTest, 0)

		got := ClassifyManifestFailure(dir, manifestFailure(localizedManifestOutput), []string{distfileUnderTest}, fixedSpace(0))

		if !errors.Is(got, errDistdirFull) {
			t.Fatalf("verdict = %v, want the free-space verdict to win over the empty artefact", got)
		}
		if errors.Is(got, errArtefactEmpty) {
			t.Errorf("verdict = %v, want ONLY the free-space verdict", got)
		}
	})

	t.Run("empty is reached when the two before it are silent", func(t *testing.T) {
		dir := t.TempDir()
		writeFileOfSize(t, dir, distfileUnderTest, 0)

		got := ClassifyManifestFailure(dir, manifestFailure(localizedManifestOutput), []string{distfileUnderTest}, roomySpace)

		if !errors.Is(got, errArtefactEmpty) {
			t.Fatalf("verdict = %v, want the empty-artefact verdict", got)
		}
		if errors.Is(got, distfiles.ErrDistdirNotWritable) || errors.Is(got, errDistdirFull) {
			t.Errorf("verdict = %v, want ONLY the empty-artefact verdict", got)
		}
	})
}

// TestClassifyDefaultSpaceQueryUsesStatfs covers the production SpaceFunc, which
// is what a nil seam falls back to and therefore what runs in production. It is
// asserted on its own because no injected stub can prove statfs was read
// correctly — a wrong unit or the wrong field would pass every test above.
func TestClassifyDefaultSpaceQueryUsesStatfs(t *testing.T) {
	dir := t.TempDir()

	free, err := availableSpace(dir)
	if err != nil {
		t.Fatalf("availableSpace(%q) error = %v, want a byte count for a real directory", dir, err)
	}
	// Bytes, not blocks: a temp directory's filesystem with less than a mebibyte
	// free would make the whole suite unrunnable, so anything under the floor
	// here means the unit is wrong.
	if free < minimumUsableFreeBytes {
		t.Fatalf("availableSpace(%q) = %d, which is below the %d-byte floor; the value is not a byte count "+
			"(or this filesystem is genuinely full)", dir, free, minimumUsableFreeBytes)
	}

	t.Run("a path that cannot be queried reports the error, not zero", func(t *testing.T) {
		missing := filepath.Join(dir, "no-such-directory")

		free, err := availableSpace(missing)
		if err == nil {
			t.Fatalf("availableSpace(%q) = %d, nil; want an error for a path that does not exist", missing, free)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("availableSpace error = %v, want it to wrap the operating system's reason", err)
		}
	})
}

// =============================================================================
// The two name reductions must agree (S030 sub-task 7.3)
// =============================================================================

// TestDistfileNameReductionsAgreeAcrossPackages pins the one property that the
// duplication between distfileEntryName (here) and distfiles.distfileName
// (internal/common/distfiles/quarantine.go) depends on.
//
// # Why there are two of them
//
// Both reduce an untrusted name — from a parsed Manifest, or from a resolved
// version — to the single filename their package is willing to join to a shared
// distdir. They are deliberately duplicated rather than exported, with the
// documented trigger being a third consumer. That is a defensible trade, but it
// buys a real hazard: nothing stops one from being edited and the other not, and
// the drift would be silent because each package's own tests would still pass.
//
// # What drift would cost
//
// This side only READS (an Lstat, to see whether an expected artefact is zero
// length), so a name that escapes here cannot delete anything. What it can do is
// reach a file nobody asked about and return an "environment" verdict from it —
// the exact failure the doc comment above distfileEntryName names — which then
// suppresses a fixer invocation that should have happened.
//
// # How the other side is observed
//
// distfileName is unexported, so this asserts its BEHAVIOUR through the one
// exported function whose output reveals it: LockFetch skips a name that does
// not reduce, so a lock file appears in the directory if and only if the name
// was accepted, and its filename carries the reduced name back.
func TestDistfileNameReductionsAgreeAcrossPackages(t *testing.T) {
	// Inputs chosen to separate the two implementations rather than to be
	// representative. The backslash case is the one that matters most on Linux:
	// it is a LEGAL filename, filepath.Base leaves it untouched, and only the
	// explicit separator check refuses it — so it is the input that tells a real
	// reduction apart from a bare filepath.Base (see .draft/deviations.yaml, 3.2).
	inputs := []string{
		"zed-1.16.0_pre20260806.tar.gz", // ordinary
		"tdesktop-7.0.9-full.tar.gz",    // ordinary, hyphens and dots
		`weird\name.tar.xz`,             // backslash: legal here, must be refused
		`a\b\c`,                         // backslashes only
		"",                              // Base("") is "."
		".",                             //
		"..",                            // Base is "..", the distdir's PARENT
		"/",                             //
		"../..",                         //
		"../../etc/passwd",              // Base neutralises this to "passwd"
		"sub/dir/file.tar.gz",           // Base takes the last element
		"trailing/",                     // Base strips the separator
		"ünïcôdé-1.0.tar.zst",           // non-ASCII is a perfectly good filename
	}

	// Absolute expectations, so a mutation that makes BOTH sides accept
	// everything still fails. Agreement alone would not catch that.
	mustRefuse := map[string]bool{
		"": true, ".": true, "..": true, "/": true, "../..": true,
		`weird\name.tar.xz`: true, `a\b\c`: true,
	}

	for _, raw := range inputs {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			gotName, gotOK := distfileEntryName(raw)

			wantRefused := mustRefuse[raw]
			if gotOK == wantRefused {
				t.Errorf("distfileEntryName(%q) accepted = %v, want accepted = %v", raw, gotOK, !wantRefused)
			}

			// The other side, observed rather than called.
			dir := t.TempDir()
			lock, err := distfiles.LockFetch(dir, []string{raw})
			if err != nil {
				t.Fatalf("LockFetch(%q) error = %v; a name that does not reduce is skipped, never an error", raw, err)
			}
			t.Cleanup(lock.Release)

			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			distfilesOK := len(entries) == 1
			if len(entries) > 1 {
				t.Fatalf("LockFetch(%q) created %d entries, want 0 or 1: %v", raw, len(entries), entries)
			}

			if gotOK != distfilesOK {
				t.Fatalf("the two reductions disagree on %q: distfileEntryName accepted = %v, distfiles accepted = %v\n"+
					"these are duplicated on purpose (see the doc comment above distfileEntryName) and have drifted",
					raw, gotOK, distfilesOK)
			}

			if !gotOK {
				return
			}

			// Both accepted — they must also have reduced to the SAME name, or a
			// quarantine and a classification would be talking about two files.
			locked := strings.TrimSuffix(strings.TrimPrefix(entries[0].Name(), "."), ".bentoo_lockfile")
			if locked != gotName {
				t.Errorf("the two reductions disagree on the RESULT for %q: distfileEntryName = %q, distfiles locked %q",
					raw, gotName, locked)
			}
		})
	}
}
