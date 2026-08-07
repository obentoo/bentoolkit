package distfiles

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// =============================================================================
// Test harness — the portageq query is always scripted, never real
// =============================================================================
//
// Every test below stubs the exec seam. That is not only about hermeticity: on
// a Gentoo host the real `portageq distdir` answers /var/cache/distfiles, which
// is also the last rung of the precedence, so an unstubbed test could not tell
// the two apart and would pass for the wrong reason.

// portageqCall records what the exec seam was asked to run. Tests assert on the
// argv, and on the call count — precedence means a higher rung must not ask the
// host anything at all.
type portageqCall struct {
	calls int
	name  string
	args  []string
}

// argv renders the recorded invocation as a single command line, so a test can
// state the expected one the way a user would type it.
func (c *portageqCall) argv() string {
	return strings.TrimSpace(c.name + " " + strings.Join(c.args, " "))
}

// stubPortageqExec points the exec seam at an arbitrary command for the rest of
// the test, restoring the real one afterwards. The returned portageqCall
// captures the argv the production code asked for (not the one actually run).
func stubPortageqExec(t *testing.T, name string, args ...string) *portageqCall {
	t.Helper()
	call := &portageqCall{}
	orig := execCommand
	execCommand = func(ctx context.Context, wantName string, wantArgs ...string) *exec.Cmd {
		call.calls++
		call.name = wantName
		call.args = append([]string(nil), wantArgs...)
		return exec.CommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { execCommand = orig })
	return call
}

// stubPortageqOutput makes the seam answer with exactly out, byte for byte, so
// a fixture can carry the trailing newline portageq really prints.
func stubPortageqOutput(t *testing.T, out string) *portageqCall {
	t.Helper()
	return stubPortageqExec(t, "printf", "%s", out)
}

// stubPortageqUnavailable makes the query fail the way a non-portage host fails
// it: there is no such binary.
func stubPortageqUnavailable(t *testing.T) *portageqCall {
	t.Helper()
	return stubPortageqExec(t, filepath.Join(t.TempDir(), "no-such-portageq"))
}

// stubPortageqTimeout shortens the query's deadline so the timeout branch can be
// exercised in milliseconds instead of seconds.
func stubPortageqTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := portageqTimeout
	portageqTimeout = d
	t.Cleanup(func() { portageqTimeout = orig })
}

// assertChosePath checks WHICH path Resolve chose — which is what precedence is
// about — without requiring that this machine be able to create it.
//
// The last rung of the precedence is a system path: /var/cache/distfiles is
// present on the Gentoo hosts this tool targets, but on a CI runner it neither
// exists nor is creatable by an unprivileged user. Resolve deliberately returns
// the chosen path alongside that failure, so the assertion stays about the
// resolution instead of about the runner's filesystem. When preparation does
// fail, the failure must at least name the directory (R1.4); when it succeeds,
// the directory must actually be there.
func assertChosePath(t *testing.T, dir Dir, err error, want string) {
	t.Helper()
	if dir.Path != want {
		t.Errorf("Resolve chose %q, want %q", dir.Path, want)
	}
	if err != nil {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a preparation failure must name the directory it could not prepare; err = %v", err)
		}
		t.Logf("chosen path %q is not preparable on this machine (%v); the choice is what this test asserts", want, err)
		return
	}
	if info, statErr := os.Stat(dir.Path); statErr != nil || !info.IsDir() {
		t.Errorf("Resolve returned no error, so %q must exist as a directory; stat err = %v", dir.Path, statErr)
	}
}

// =============================================================================
// D2 precedence
// =============================================================================

// TestResolvePrefersExplicitPathOverPortageq covers the top two rungs: the
// --distdir flag outranks everything, and the configured path outranks the host.
// Both are asserted twice over — by the path that comes back, and by the fact
// that the host was never asked. A rung that wins must also stop the query.
func TestResolvePrefersExplicitPathOverPortageq(t *testing.T) {
	t.Run("the flag wins over the config and the host", func(t *testing.T) {
		hostAnswer := filepath.Join(t.TempDir(), "host-distdir")
		call := stubPortageqOutput(t, hostAnswer+"\n")
		explicit := filepath.Join(t.TempDir(), "flag-distdir")
		configured := filepath.Join(t.TempDir(), "config-distdir")

		dir, err := Resolve(explicit, configured)
		if err != nil {
			t.Fatalf("Resolve(%q, %q) error = %v", explicit, configured, err)
		}
		t.Cleanup(dir.Cleanup)

		if dir.Path != explicit {
			t.Errorf("Resolve Path = %q, want the flag's %q", dir.Path, explicit)
		}
		if call.calls != 0 {
			t.Errorf("portageq was queried %d time(s); the flag already answered the question", call.calls)
		}
		for _, loser := range []string{configured, hostAnswer} {
			if _, statErr := os.Stat(loser); !os.IsNotExist(statErr) {
				t.Errorf("a losing candidate %q must not be created, stat err = %v", loser, statErr)
			}
		}
	})

	t.Run("the config wins over the host", func(t *testing.T) {
		hostAnswer := filepath.Join(t.TempDir(), "host-distdir")
		call := stubPortageqOutput(t, hostAnswer+"\n")
		configured := filepath.Join(t.TempDir(), "config-distdir")

		dir, err := Resolve("", configured)
		if err != nil {
			t.Fatalf("Resolve(\"\", %q) error = %v", configured, err)
		}
		t.Cleanup(dir.Cleanup)

		if dir.Path != configured {
			t.Errorf("Resolve Path = %q, want the configured %q", dir.Path, configured)
		}
		if call.calls != 0 {
			t.Errorf("portageq was queried %d time(s); the config already answered the question", call.calls)
		}
		if _, statErr := os.Stat(hostAnswer); !os.IsNotExist(statErr) {
			t.Errorf("the host's answer must not be created when the config wins, stat err = %v", statErr)
		}
	})
}

// TestResolveFallsBackToPortageqDistdir is R1.2's default: with nothing
// configured, the distdir is the one the host's own package manager reports.
// It also pins the two things that make that answer usable — the output is
// trimmed, and a directory that was already there is never marked Created, so
// Cleanup cannot delete the DISTDIR the whole system shares.
func TestResolveFallsBackToPortageqDistdir(t *testing.T) {
	t.Run("the host's answer is trimmed, used, and left alone", func(t *testing.T) {
		answer := filepath.Join(t.TempDir(), "distfiles")
		if err := os.MkdirAll(answer, 0o750); err != nil {
			t.Fatalf("seed host distdir: %v", err)
		}
		// portageq prints one line ending in a newline; the fixture pads it
		// with spaces as well so trimming is pinned rather than assumed.
		call := stubPortageqOutput(t, "  "+answer+"  \n")

		dir, err := Resolve("", "")
		if err != nil {
			t.Fatalf("Resolve(\"\", \"\") error = %v", err)
		}
		t.Cleanup(dir.Cleanup)

		if dir.Path != answer {
			t.Errorf("Resolve Path = %q, want the host's %q", dir.Path, answer)
		}
		if call.calls != 1 {
			t.Errorf("portageq calls = %d, want exactly 1", call.calls)
		}
		if got := call.argv(); got != "portageq distdir" {
			t.Errorf("queried %q, want %q", got, "portageq distdir")
		}
		if dir.Created {
			t.Error("Created = true for a directory that already existed; Cleanup would then delete the host's DISTDIR")
		}

		// The host's DISTDIR holds everything portage has ever downloaded.
		// Cleanup running over it is the worst outcome this package can have,
		// so the survival is asserted with a file in it, not just a stat.
		payload := filepath.Join(answer, "hello-1.0.0.tar.gz")
		if err := os.WriteFile(payload, []byte("cached"), 0o644); err != nil {
			t.Fatalf("seed distfile: %v", err)
		}
		dir.Cleanup()
		if data, err := os.ReadFile(payload); err != nil || string(data) != "cached" {
			t.Errorf("the host's DISTDIR must survive Cleanup untouched; read = %q, err = %v", data, err)
		}
	})

	t.Run("a directory this call had to create is marked Created", func(t *testing.T) {
		answer := filepath.Join(t.TempDir(), "nested", "distfiles")
		stubPortageqOutput(t, answer+"\n")

		dir, err := Resolve("", "")
		if err != nil {
			t.Fatalf("Resolve(\"\", \"\") error = %v", err)
		}
		if dir.Path != answer {
			t.Fatalf("Resolve Path = %q, want %q", dir.Path, answer)
		}
		if info, statErr := os.Stat(dir.Path); statErr != nil || !info.IsDir() {
			t.Fatalf("distdir not created: stat err = %v", statErr)
		}
		if !dir.Created {
			t.Error("Created = false for a directory this call made; nothing would ever remove it")
		}

		dir.Cleanup()
		if _, statErr := os.Stat(answer); !os.IsNotExist(statErr) {
			t.Errorf("a directory we created must be removed by Cleanup, stat err = %v", statErr)
		}
	})
}

// TestResolveRejectsNonAbsolutePortageqOutput states that output which is not an
// absolute path is an UNANSWERED query, not a path: portageq may print nothing,
// or print a diagnostic, and neither is a directory. Each case also runs inside
// a sandbox working directory, so a regression that resolved the output
// relatively would be caught creating something there.
func TestResolveRejectsNonAbsolutePortageqOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"relative path", "var/cache/distfiles\n"},
		{"bare word", "distfiles\n"},
		{"empty output", ""},
		{"whitespace only", "   \n"},
		{"a diagnostic instead of a path", "!!! Unable to parse profile\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandbox := t.TempDir()
			t.Chdir(sandbox)
			stubPortageqOutput(t, tc.output)

			dir, err := Resolve("", "")
			t.Cleanup(dir.Cleanup)

			assertChosePath(t, dir, err, DefaultCache)

			entries, readErr := os.ReadDir(sandbox)
			if readErr != nil {
				t.Fatalf("read sandbox: %v", readErr)
			}
			if len(entries) != 0 {
				t.Errorf("Resolve created %d entry/entries under the working directory (%v); non-absolute output was treated as a path", len(entries), entries)
			}
		})
	}
}

// TestResolveFallsBackToDefaultWhenPortageqMissing covers the three ways the
// query fails outright — no binary, a non-zero exit, and a query that never
// returns. All three are "unanswered": they fall through to DefaultCache, and
// none of them is surfaced as an error, because a host without portage is not a
// broken run.
func TestResolveFallsBackToDefaultWhenPortageqMissing(t *testing.T) {
	t.Run("the binary is not on the host", func(t *testing.T) {
		stubPortageqUnavailable(t)

		dir, err := Resolve("", "")
		t.Cleanup(dir.Cleanup)

		assertChosePath(t, dir, err, DefaultCache)
	})

	t.Run("a non-zero exit discards whatever it printed", func(t *testing.T) {
		// stdout carries a perfectly well-formed absolute path; the exit code
		// is what makes it untrustworthy.
		stubPortageqExec(t, "sh", "-c", "printf '%s' /should/never/be/used; exit 3")

		dir, err := Resolve("", "")
		t.Cleanup(dir.Cleanup)

		assertChosePath(t, dir, err, DefaultCache)
	})

	t.Run("a query that never returns is bounded by the timeout", func(t *testing.T) {
		stubPortageqTimeout(t, 50*time.Millisecond)
		stubPortageqExec(t, "sleep", "30")

		start := time.Now()
		dir, err := Resolve("", "")
		elapsed := time.Since(start)
		t.Cleanup(dir.Cleanup)

		assertChosePath(t, dir, err, DefaultCache)
		if elapsed > 5*time.Second {
			t.Errorf("Resolve took %v: the query must be bounded by its own timeout, not by the host answering", elapsed)
		}
	})
}

// TestResolveNeverUsesOsTempDir is R1.1, asserted twice.
//
// Behaviourally: with nothing configured and no answer from the host, the
// fallback is the documented disk path and not somewhere under os.TempDir().
// Structurally: no function on Resolve's path can even reach a temporary
// directory, because the only one in the package that names one is
// ResolveOrTemp — the entry point whose command documents that default.
//
// The behavioural half alone would only describe today's code; the structural
// half is what a future edit has to get past.
func TestResolveNeverUsesOsTempDir(t *testing.T) {
	t.Run("the fallback is a disk path, not a temporary one", func(t *testing.T) {
		stubPortageqUnavailable(t)

		dir, err := Resolve("", "")
		t.Cleanup(dir.Cleanup)

		assertChosePath(t, dir, err, DefaultCache)

		tmp := os.TempDir()
		sep := string(os.PathSeparator)
		if dir.Path == tmp || strings.HasPrefix(dir.Path, strings.TrimRight(tmp, sep)+sep) {
			t.Errorf("Resolve landed inside os.TempDir() (%q): %q — on the host this story is about that path is a 31 GB tmpfs, so every distfile would be downloaded into RAM", tmp, dir.Path)
		}
	})

	t.Run("only ResolveOrTemp may reach for a temporary directory", func(t *testing.T) {
		owners := functionsUsingTempDir(t)

		// Guard against a vacuous pass: if the scan cannot see the one call
		// that IS allowed, it cannot prove anything about the ones that are not.
		if !owners["ResolveOrTemp"] {
			t.Fatalf("the scan found no temporary-directory call at all (found: %v); it is not looking at what it thinks it is", owners)
		}
		for name := range owners {
			if name != "ResolveOrTemp" {
				t.Errorf("%s calls os.TempDir/os.MkdirTemp; only ResolveOrTemp may, because only `overlay manifest` documents a temporary distdir as its default", name)
			}
		}
	})
}

// TestResolveExpandsTildeAndRelativePaths pins that the D2 entry point performs
// the same expansion the transplanted code always did — a "~" path lands under
// the user's home rather than in a directory literally named "~", and a relative
// path is made absolute. pkgdev is handed this value on a command line, so a
// half-expanded one would be a real bug, not a cosmetic one.
func TestResolveExpandsTildeAndRelativePaths(t *testing.T) {
	t.Run("a ~ path lands under the user's home", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("UserHomeDir unavailable: %v", err)
		}
		// Stubbed unavailable so that a regression falling through to the host
		// rung fails loudly instead of quietly resolving to DefaultCache.
		stubPortageqUnavailable(t)

		rel := filepath.Join(".cache", "bentoo-test-distdir-resolve-d2")
		input := "~/" + rel
		expected := filepath.Join(home, rel)
		t.Cleanup(func() { _ = os.RemoveAll(expected) })

		dir, err := Resolve(input, "")
		if err != nil {
			t.Fatalf("Resolve(%q, \"\") error = %v", input, err)
		}
		if dir.Path != expected {
			t.Errorf("Resolve(%q) Path = %q, want %q", input, dir.Path, expected)
		}
	})

	t.Run("a relative path is made absolute against the working directory", func(t *testing.T) {
		t.Chdir(t.TempDir())
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		stubPortageqUnavailable(t)

		dir, err := Resolve("distfiles", "")
		if err != nil {
			t.Fatalf("Resolve(\"distfiles\", \"\") error = %v", err)
		}
		t.Cleanup(dir.Cleanup)

		want := filepath.Join(cwd, "distfiles")
		if dir.Path != want {
			t.Errorf("Resolve(\"distfiles\") Path = %q, want %q", dir.Path, want)
		}
		if !filepath.IsAbs(dir.Path) {
			t.Errorf("Resolve returned a relative path %q; pkgdev is given this on a command line", dir.Path)
		}
		if !dir.Created {
			t.Error("Created = false for a directory this call made; nothing would ever remove it")
		}
	})
}

// functionsUsingTempDir parses the package's non-test sources and returns the
// name of every function whose body calls os.TempDir or os.MkdirTemp
// (os.MkdirTemp("") is os.TempDir() wearing a hat, so both count).
//
// It walks whole files rather than Resolve's body alone on purpose: Resolve
// delegates to helpers, and a temporary directory reintroduced inside one of
// them would be just as wrong and much easier to miss.
func functionsUsingTempDir(t *testing.T) map[string]bool {
	t.Helper()

	// The package directory is taken from this file's own path rather than
	// from the working directory, which other tests here change.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	pkgDir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package directory %q: %v", pkgDir, err)
	}

	found := map[string]bool{}
	sources := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources++
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			owner := "<package level>"
			if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name != nil {
				owner = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, isSelector := n.(*ast.SelectorExpr)
				if !isSelector {
					return true
				}
				pkg, isIdent := sel.X.(*ast.Ident)
				if !isIdent || pkg.Name != "os" {
					return true
				}
				if sel.Sel.Name == "TempDir" || sel.Sel.Name == "MkdirTemp" {
					found[owner] = true
				}
				return true
			})
		}
	}
	if sources == 0 {
		t.Fatalf("no non-test Go source found in %q; the scan would pass vacuously", pkgDir)
	}
	return found
}

// TestResolveCreationFailureCarriesErrDistdirNotWritable is S030 sub-task 7.1.
//
// R1.4 groups three conditions into one requirement — the distdir "does not
// exist, cannot be created, or is not writable" — and the gate in
// internal/autoupdate acts on all three identically: fail the package, and do
// not hand it to the LLM fixer. Before 7.1 only the third produced a sentinel;
// a directory that could not be CREATED came back as a bare os error, so the
// gate could not recognise it and the fixer ran against a machine fault.
//
// ENOTDIR is the failure staged here: a path under a regular file can never be
// created, it needs no privileges, and unlike a 0500 directory it behaves the
// same when the tests run as root.
func TestResolveCreationFailureCarriesErrDistdirNotWritable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("regular file"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	target := filepath.Join(blocker, "distfiles")

	t.Run("Resolve wraps the sentinel around a creation failure", func(t *testing.T) {
		dir, err := Resolve(target, "")
		if err == nil {
			t.Fatalf("Resolve(%q, \"\") error = nil; a path under a regular file cannot be created", target)
		}
		if !errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("errors.Is(err, ErrDistdirNotWritable) = false for %v; the gate in internal/autoupdate reads this sentinel", err)
		}
		// The underlying os error has to stay reachable too, or an operator
		// loses the only sentence that says WHY.
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("the wrap lost the underlying error; errors.Is(err, ENOTDIR) = false for %v", err)
		}
		if !strings.Contains(err.Error(), target) {
			t.Errorf("the error does not name the directory: %v", err)
		}
		// R1.4's other half, unchanged by 7.1: the path travels for the
		// diagnostic, and Created stays false so Cleanup is a no-op.
		if dir.Created {
			t.Error("Dir.Created = true on a failure; Cleanup would then remove a directory this call never made")
		}
	})

	// The Unchanged Behavior guard. expandAndCreate is shared with
	// ResolveOrTemp, which `overlay manifest` uses and whose behaviour this
	// story freezes — so the wrap must live in Resolve and nowhere lower.
	t.Run("ResolveOrTemp is untouched by the wrap", func(t *testing.T) {
		_, err := ResolveOrTemp(target)
		if err == nil {
			t.Fatalf("ResolveOrTemp(%q) error = nil", target)
		}
		if errors.Is(err, ErrDistdirNotWritable) {
			t.Errorf("ResolveOrTemp gained ErrDistdirNotWritable: %v\n"+
				"overlay manifest's error shape is frozen by the story's Unchanged Behavior section; "+
				"the wrap belongs in Resolve, not in expandAndCreate", err)
		}
		if !errors.Is(err, syscall.ENOTDIR) {
			t.Errorf("ResolveOrTemp lost the underlying error: %v", err)
		}
	})
}

// =============================================================================
// TempRoot — the root a THROWAWAY distdir is made under (S030 sub-task 7.4)
// =============================================================================

// TestTempRootAsksThePackageManagerForPortageTmpdir pins the query itself.
//
// The argv matters as much as the answer: `portageq envvar PORTAGE_TMPDIR` is a
// different question from `portageq distdir`, and asking the wrong one would
// return the shared, persistent download directory for a caller that is about to
// create a private directory and then delete it.
func TestTempRootAsksThePackageManagerForPortageTmpdir(t *testing.T) {
	call := stubPortageqOutput(t, "/var/tmp\n")

	if got := TempRoot(); got != "/var/tmp" {
		t.Errorf("TempRoot() = %q, want %q", got, "/var/tmp")
	}
	if want := "portageq envvar PORTAGE_TMPDIR"; call.argv() != want {
		t.Errorf("TempRoot asked %q, want %q", call.argv(), want)
	}
}

// TestTempRootIsEmptyWhenTheQuestionCannotBeAnswered is the contract that lets
// the caller write os.MkdirTemp(TempRoot(), …) with no branch of its own.
//
// "" is not a failure here and must never become one: os.MkdirTemp already reads
// it as "use os.TempDir()", so an unanswerable query leaves the caller with
// exactly the behaviour it had before this function existed. Returning an error
// instead would force every caller to decide again, and a caller that got that
// decision wrong would fail an apply over a machine that simply has no portageq.
func TestTempRootIsEmptyWhenTheQuestionCannotBeAnswered(t *testing.T) {
	t.Run("portageq is not installed", func(t *testing.T) {
		stubPortageqUnavailable(t)
		if got := TempRoot(); got != "" {
			t.Errorf("TempRoot() = %q, want \"\" so os.MkdirTemp falls back to its default", got)
		}
	})

	t.Run("the answer is not an absolute path", func(t *testing.T) {
		// A relative path is meaningless: portage's working directory is unknown.
		stubPortageqOutput(t, "var/tmp\n")
		if got := TempRoot(); got != "" {
			t.Errorf("TempRoot() = %q, want \"\": a relative answer is not a directory", got)
		}
	})

	t.Run("the answer is empty", func(t *testing.T) {
		stubPortageqOutput(t, "\n")
		if got := TempRoot(); got != "" {
			t.Errorf("TempRoot() = %q, want \"\"", got)
		}
	})

	t.Run("the query times out", func(t *testing.T) {
		stubPortageqTimeout(t, time.Millisecond)
		stubPortageqExec(t, "sleep", "5")
		if got := TempRoot(); got != "" {
			t.Errorf("TempRoot() = %q, want \"\"", got)
		}
	})
}

// TestTempRootAndHostDistdirAskDifferentQuestions guards the refactor that gave
// them a shared implementation.
//
// portageqPath is now one function behind two callers, and the only thing left
// separating them is the argv each passes. A copy-paste that pointed TempRoot at
// "distdir" would still return an absolute path, still pass every test above,
// and would hand the LLM fixer the host's real DISTDIR to write into — the one
// directory the sandbox exists to keep it out of.
func TestTempRootAndHostDistdirAskDifferentQuestions(t *testing.T) {
	distdirCall := stubPortageqOutput(t, "/var/cache/distfiles\n")
	_ = hostDistdir()
	distdirArgv := distdirCall.argv()

	tempCall := stubPortageqOutput(t, "/var/tmp\n")
	_ = TempRoot()
	tempArgv := tempCall.argv()

	if distdirArgv == tempArgv {
		t.Fatalf("hostDistdir and TempRoot ask the SAME question (%q); the fixer sandbox would be created inside the host's DISTDIR", distdirArgv)
	}
	if distdirArgv != "portageq distdir" {
		t.Errorf("hostDistdir asked %q, want %q", distdirArgv, "portageq distdir")
	}
}
