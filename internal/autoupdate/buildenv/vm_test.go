package buildenv

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeShidashi writes a shidashi that honours the contract the vm backend
// relies on, measured against the real one on 2026-11-14: `vm start --dev`
// prints one JSON line (exit 3 with no ISO), `vm run` hands the guest's shell
// its arguments joined by spaces and exits with the command's code (255 once
// the VM is gone), `vm stop` powers it off. The guest is this host.
func fakeShidashi(t *testing.T, withDev bool) (bin, state string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "state")
	help := "--dev  A VM ready to build ebuilds"
	if !withDev {
		help = "--uefi  Boot with OVMF"
	}
	script := `#!/bin/sh
state='` + state + `'
[ "$1" = vm ] || exit 2
shift
cmd=$1; shift
case "$cmd" in
start)
	[ "$1" = --help ] && { echo '` + help + `'; exit 0; }
	[ -n "$FAKE_SHIDASHI_NO_ISO" ] && { echo 'error: no minimal ISO' >&2; exit 3; }
	echo "starting" >&2
	echo running > "$state"
	echo '{"name": "x", "cid": 42, "iso": "/isos/bentoo-2026.11.12-minimal-systemd-v3.iso", "pin": "p20261101.28ad071a", "ready": true}'
	;;
run)
	while [ "$1" != -- ]; do shift; done
	shift
	[ "$(cat "$state" 2>/dev/null)" = running ] || { echo 'Failed to connect to vsock:42:22' >&2; exit 255; }
	exec sh -c "$*"
	;;
stop)
	echo stopped > "$state"
	;;
esac
`
	bin = filepath.Join(dir, "shidashi")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, state
}

func startedVM(t *testing.T) (*VM, string) {
	t.Helper()
	bin, state := fakeShidashi(t, true)
	v, err := NewVM(t.Context(), bin, "", VMSeams{})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	return v, state
}

// TestVM_TheArgvReachesTheGuestUnchanged: the guest's shell re-parses what
// shidashi hands it, so every word is quoted here; spaces, quotes, $ and
// newlines arrive as they left, and so do the environment and the directory.
func TestVM_TheArgvReachesTheGuestUnchanged(t *testing.T) {
	v, _ := startedVM(t)
	dir := filepath.Join(t.TempDir(), "a dir with 'quotes'")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := []string{"sh", "-c", `printf '%s|' "$@" "$PWD" "$GREETING" "${HOME-unset}"`, "sh", "a b", "$HOME", "it's", "two\nlines", ""}
	cmd := v.Command(t.Context(), argv, []string{"GREETING=hello world"}, dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	want := "a b|$HOME|it's|two\nlines||" + dir + "|hello world|unset|"
	if out.String() != want {
		t.Errorf("guest saw %q\nwant      %q", out.String(), want)
	}
}

// TestVM_TheCommandIsStreamedWithNoTimeout: a legitimate build runs for hours,
// so the run has no limit and its output is passed through as it is written.
func TestVM_TheCommandIsStreamedWithNoTimeout(t *testing.T) {
	v, _ := startedVM(t)
	got := strings.Join(v.Command(t.Context(), []string{"true"}, nil, "").Args, " ")
	for _, want := range []string{"vm run --name " + v.name, "--stream", "--timeout 0", "-- "} {
		if !strings.Contains(got, want) {
			t.Errorf("run %q lacks %q", got, want)
		}
	}
}

// TestVM_StartNamesWhatItBooted: the proof record names the ISO and its pin.
func TestVM_StartNamesWhatItBooted(t *testing.T) {
	v, _ := startedVM(t)
	if want := "bentoo-2026.11.12-minimal-systemd-v3.iso (p20261101.28ad071a)"; v.Identity() != want {
		t.Errorf("identity = %q, want %q", v.Identity(), want)
	}
}

// TestVM_NoISOToBootIsErrNoISO: Shidashi's exit 3, and a configured ISO that
// is not there, are both the missing image of this backend.
func TestVM_NoISOToBootIsErrNoISO(t *testing.T) {
	bin, _ := fakeShidashi(t, true)
	v, err := NewVM(t.Context(), bin, "", VMSeams{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SHIDASHI_NO_ISO", "1")
	if err := v.Start(t.Context()); !errors.Is(err, ErrNoISO) {
		t.Errorf("start with no ISO: err = %v, want ErrNoISO", err)
	}
	if _, err := NewVM(t.Context(), bin, filepath.Join(t.TempDir(), "absent.iso"), VMSeams{}); !errors.Is(err, ErrNoISO) {
		t.Errorf("an absent configured ISO: err = %v, want ErrNoISO", err)
	}
}

// TestVM_AShidashiWithoutDevIsRefused: the installed Shidashi may predate the
// build VM; that is said before anything boots.
func TestVM_AShidashiWithoutDevIsRefused(t *testing.T) {
	bin, _ := fakeShidashi(t, false)
	if _, err := NewVM(t.Context(), bin, "", VMSeams{}); err == nil || !strings.Contains(err.Error(), "update Shidashi") {
		t.Errorf("err = %v, want a refusal asking to update Shidashi", err)
	}
	if _, err := NewVM(t.Context(), filepath.Join(t.TempDir(), "shidashi"), "", VMSeams{}); err == nil {
		t.Error("an absent shidashi was accepted")
	}
}

// TestVM_ADeadVMIsNotAlive: after the VM is gone a command exits like a failed
// one would, so the session asks the VM itself.
func TestVM_ADeadVMIsNotAlive(t *testing.T) {
	bin, state := fakeShidashi(t, true)
	v, err := NewVM(t.Context(), bin, "", VMSeams{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSession(v)
	if err := s.startLocked(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !s.Alive(t.Context()) {
		t.Fatal("a running VM is not alive")
	}
	if err := os.WriteFile(state, []byte("crashed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Alive(t.Context()) {
		t.Error("a VM that stopped answering is still alive")
	}
	if err := s.Close(t.Context()); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain/path-1.0_p2": "plain/path-1.0_p2",
		"":                  "''",
		"a b":               "'a b'",
		"it's":              `'it'\''s'`,
		"$HOME":             "'$HOME'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
