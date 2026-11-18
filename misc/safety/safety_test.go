package safety

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const guard = "30-bentoo-safety.bash"

// bashLookPath returns bash, or skips: the scripts are bash by design.
func bashLookPath(t *testing.T) string {
	t.Helper()
	b, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	return b
}

// assess sources the guard in an interactive bash (it does nothing in a
// non-interactive one) and reports _bentoo_assess's return code and label for
// one command line.
func assess(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	script := `source "$GUARD" || exit 99; _bentoo_assess "$@"; rc=$?; printf '%d|%s' "$rc" "$_BENTOO_LABEL"`
	cmd := exec.CommandContext(t.Context(), bashLookPath(t), append([]string{"--norc", "--noprofile", "-i", "-c", script, "bash"}, args...)...)
	home := t.TempDir()
	cmd.Env = append([]string{"GUARD=" + guard, "HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v (stdout %q)", args, err, out.String())
	}
	rcText, label, ok := strings.Cut(out.String(), "|")
	if !ok {
		t.Fatalf("%v: unexpected output %q", args, out.String())
	}
	rc, err := strconv.Atoi(rcText)
	if err != nil {
		t.Fatal(err)
	}
	return rc, label
}

// TestAssessPolicy pins the guard's decisions: 0 asks for a code, 1 lets the
// command through, 2 blocks it. The labels are the English text a C locale
// shows.
func TestAssessPolicy(t *testing.T) {
	cases := []struct {
		args  []string
		rc    int
		label string
	}{
		{[]string{"reboot"}, 0, "reboot requested"},
		{[]string{"rm", "file.txt"}, 1, ""},
		{[]string{"rm", "-r", "dir"}, 0, "rm -r: dir"},
		{[]string{"rm", "-rf", "dir"}, 0, "rm -rf: dir"},
		{[]string{"rm", "/etc/hosts"}, 0, "rm: /etc/hosts"},
		{[]string{"rm", "-rf", "/"}, 2, "\x1b[1;41m BLOCKED \x1b[0m critical target: /"},
		{[]string{"rm", "--no-preserve-root", "x"}, 2, "\x1b[1;41m BLOCKED \x1b[0m --no-preserve-root refused."},
		{[]string{"chmod", "-R", "755", "/usr/lib"}, 0, "chmod -R on /usr/lib"},
		{[]string{"chmod", "-R", "755", "mydir"}, 1, ""},
		{[]string{"emerge", "--depclean"}, 0, "emerge (removal): --depclean"},
		{[]string{"emerge", "-av", "foo"}, 1, ""},
		{[]string{"dd", "if=/dev/zero", "of=/dev/sda"}, 0, "dd if=/dev/zero of=/dev/sda"},
		{[]string{"cryptsetup", "luksOpen", "x"}, 1, ""},
		{[]string{"ls", "-la"}, 1, ""},
	}
	for _, c := range cases {
		rc, label := assess(t, nil, c.args...)
		if rc != c.rc || label != c.label {
			t.Errorf("%v: rc %d label %q, want rc %d label %q", c.args, rc, label, c.rc, c.label)
		}
	}
}

func TestHomeIsBlocked(t *testing.T) {
	script := `source "$GUARD"; _bentoo_assess rm -rf "$HOME"; printf '%d|%s' "$?" "$_BENTOO_LABEL"`
	cmd := exec.CommandContext(t.Context(), bashLookPath(t), "--norc", "--noprofile", "-i", "-c", script)
	home := t.TempDir()
	cmd.Env = []string{"GUARD=" + guard, "HOME=" + home, "PATH=" + os.Getenv("PATH"), "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "2|") || !strings.Contains(string(out), "deleting HOME: "+home) {
		t.Errorf("rm -rf $HOME: %q", out)
	}
}

// TestNonInteractiveShellsAreUntouched: the guard must define nothing in a
// script, cron job or unit.
func TestNonInteractiveShellsAreUntouched(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), bashLookPath(t), "--norc", "--noprofile", "-c", `source "$GUARD"; type -t rm`)
	cmd.Env = []string{"GUARD=" + guard, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "file" {
		t.Errorf("rm in a non-interactive shell is %q, want the real binary (file)", got)
	}
}

// fakeKdialog writes a kdialog that answers the --password prompt with the
// code it shows (or with a wrong one) and records every call.
func fakeKdialog(t *testing.T, dir string, rightCode bool) string {
	t.Helper()
	answer := `printf '%s' "$code"`
	if !rightCode {
		answer = `printf 'NOPE'`
	}
	script := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$KDIALOG_LOG"
for a in "$@"; do
  if [[ $a == *"code "* ]]; then code=${a##*code }; code=${code%% *}; fi
done
[[ " $* " == *" --password "* ]] && ` + answer + `
exit 0
`
	p := filepath.Join(dir, "kdialog")
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func runSecureDelete(t *testing.T, rightCode bool, env []string) (log string, survivors int) {
	t.Helper()
	bin := t.TempDir()
	fakeKdialog(t, bin, rightCode)
	victims := t.TempDir()
	var paths []string
	for _, n := range []string{"a", "b"} {
		p := filepath.Join(victims, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	logFile := filepath.Join(bin, "log")
	cmd := exec.CommandContext(t.Context(), bashLookPath(t), append([]string{"bentoo-secure-delete"}, paths...)...)
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "KDIALOG_LOG=" + logFile, "LC_ALL=C"}, env...)
	_ = cmd.Run() // a wrong code exits 1 by design
	data, _ := os.ReadFile(logFile)
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			survivors++
		}
	}
	return string(data), survivors
}

func TestSecureDeleteNeedsTheCode(t *testing.T) {
	log, left := runSecureDelete(t, false, nil)
	if left != 2 {
		t.Errorf("a wrong code deleted %d file(s)", 2-left)
	}
	if !strings.Contains(log, "Wrong code. Nothing was deleted.") {
		t.Errorf("no wrong-code dialog:\n%s", log)
	}
	log, left = runSecureDelete(t, true, nil)
	if left != 0 {
		t.Errorf("the right code left %d file(s)", left)
	}
	for _, want := range []string{"Bentoo — Permanent deletion", "Delete 2 item(s) PERMANENTLY?", "Deleted 2 item(s)."} {
		if !strings.Contains(log, want) {
			t.Errorf("kdialog was not shown %q:\n%s", want, log)
		}
	}
}

// localeAvailable reports whether glibc has name generated: the gettext
// command translates only under an installed locale.
func localeAvailable(name string) bool {
	out, err := exec.Command("locale", "-a").Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Fields(string(out)) {
		if strings.EqualFold(strings.ReplaceAll(l, "utf8", "UTF-8"), name) {
			return true
		}
	}
	return false
}

// TestTranslatedWithACatalog compiles po/pt_BR.po and checks the scripts
// speak it. It needs msgfmt, gettext and a generated pt_BR.UTF-8 locale.
func TestTranslatedWithACatalog(t *testing.T) {
	for _, tool := range []string{"msgfmt", "gettext", "ngettext"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	if !localeAvailable("pt_BR.UTF-8") {
		t.Skip("the pt_BR.UTF-8 locale is not generated on this host")
	}
	dir := t.TempDir()
	mo := filepath.Join(dir, "pt_BR", "LC_MESSAGES", "bentoolkit.mo")
	if err := os.MkdirAll(filepath.Dir(mo), 0o750); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(t.Context(), "msgfmt", "-o", mo, filepath.Join("..", "..", "po", "pt_BR.po")).CombinedOutput(); err != nil {
		t.Fatalf("msgfmt: %v\n%s", err, out)
	}
	env := []string{"BENTOO_LOCALEDIR=" + dir, "LC_ALL=pt_BR.UTF-8"}

	_, label := assess(t, env, "reboot")
	if label == "reboot requested" || !strings.Contains(label, "reboot") {
		t.Errorf("reboot label not translated: %q", label)
	}
	log, _ := runSecureDelete(t, false, env)
	if strings.Contains(log, "Wrong code. Nothing was deleted.") {
		t.Errorf("the wrong-code dialog stayed English:\n%s", log)
	}
}

// TestShellcheck lints both scripts when shellcheck is installed.
func TestShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed")
	}
	out, err := exec.CommandContext(t.Context(), sc, "-s", "bash", guard, "bentoo-secure-delete").CombinedOutput()
	if err != nil {
		t.Errorf("shellcheck: %v\n%s", err, out)
	}
}
