package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const s053BrokenBashrc = "export A=1\n" + emergeHookBlockBegin + "\nexport B=2\n"

func s053HookRoot(t *testing.T) (script, bashrc string) {
	t.Helper()
	root := t.TempDir()
	orig := EmergeHookRoot
	EmergeHookRoot = root
	t.Cleanup(func() { EmergeHookRoot = orig })
	bashrc = filepath.Join(root, "etc", "portage", "bashrc")
	if err := os.MkdirAll(filepath.Dir(bashrc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bashrc, []byte(s053BrokenBashrc), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "etc", "portage", "bashrc.d", "50-bentoo-snapshot.sh"), bashrc
}

func s053AssertRefusal(t *testing.T, err error, bashrc string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil, want a broken-hook-block refusal")
	}
	msg := err.Error()
	for _, want := range []string{bashrc, emergeHookBlockBegin, emergeHookBlockEnd} {
		if !strings.Contains(msg, want) {
			t.Errorf("err %q must contain %q", msg, want)
		}
	}
	if !strings.Contains(msg, "line 2") && !strings.Contains(msg, ":2") {
		t.Errorf("err %q must name line 2", msg)
	}
	got, rerr := os.ReadFile(bashrc)
	if rerr != nil || string(got) != s053BrokenBashrc {
		t.Errorf("bashrc changed: (%q, %v), want byte-identical %q", got, rerr, s053BrokenBashrc)
	}
}

func TestInstallEmergeHook_RefusesUnterminatedBlock(t *testing.T) {
	script, bashrc := s053HookRoot(t)
	s053AssertRefusal(t, InstallEmergeHook(), bashrc)
	if _, err := os.Stat(script); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("hook script written despite the refusal (stat err %v)", err)
	}
}

func TestUninstallEmergeHook_RefusesUnterminatedBlock(t *testing.T) {
	script, bashrc := s053HookRoot(t)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("SCRIPT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s053AssertRefusal(t, UninstallEmergeHook(), bashrc)
	if got, err := os.ReadFile(script); err != nil || string(got) != "SCRIPT\n" {
		t.Errorf("hook script removed or changed despite the refusal: (%q, %v)", got, err)
	}
}
