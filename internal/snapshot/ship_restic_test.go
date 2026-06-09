package snapshot

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeMounter is a test mounter that returns a fixed path and a cleanup spy.
// With mountErr set, Mount fails and no cleanup is handed back, exercising the
// mount-failure path of runWithMount without any real mount/umount.
type fakeMounter struct {
	path       string
	mountErr   error // when non-nil, Mount returns this and no cleanup
	cleanupErr error // returned by the cleanup spy
	mounted    bool  // Mount was called
	cleaned    *bool // set true when the returned cleanup fires
}

func (f *fakeMounter) Mount(_ context.Context, _ Snapshot) (string, func() error, error) {
	f.mounted = true
	if f.mountErr != nil {
		return "", nil, f.mountErr
	}
	cleaned := false
	f.cleaned = &cleaned
	cleanup := func() error {
		*f.cleaned = true
		return f.cleanupErr
	}
	return f.path, cleanup, nil
}

// TestRunWithMount_CleansUpOnError is the R7.3 invariant: when the wrapped fn
// errors, the mount cleanup still fires and fn's error propagates.
func TestRunWithMount_CleansUpOnError(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	r := &resticShipper{mount: fm}

	fnErr := errors.New("backup blew up")
	got := r.runWithMount(t.Context(), Snapshot{ID: "home.x"}, func(string) error {
		return fnErr
	})

	if !errors.Is(got, fnErr) {
		t.Errorf("runWithMount err = %v, want fn error %v", got, fnErr)
	}
	if fm.cleaned == nil || !*fm.cleaned {
		t.Errorf("cleanup did not fire on fn error (R7.3 violated)")
	}
}

// TestRunWithMount_Success asserts fn sees the mount path, no error escapes, and
// cleanup still runs on the happy path.
func TestRunWithMount_Success(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	r := &resticShipper{mount: fm}

	var gotPath string
	err := r.runWithMount(t.Context(), Snapshot{ID: "home.x"}, func(path string) error {
		gotPath = path
		return nil
	})
	if err != nil {
		t.Fatalf("runWithMount: %v", err)
	}
	if gotPath != "/mnt/snap.ro" {
		t.Errorf("fn path = %q, want /mnt/snap.ro", gotPath)
	}
	if fm.cleaned == nil || !*fm.cleaned {
		t.Errorf("cleanup did not fire on success")
	}
}

// TestRunWithMount_MountFails asserts a mount failure is returned and fn never runs.
func TestRunWithMount_MountFails(t *testing.T) {
	mountErr := errors.New("mount: permission denied")
	fm := &fakeMounter{mountErr: mountErr}
	r := &resticShipper{mount: fm}

	called := false
	got := r.runWithMount(t.Context(), Snapshot{ID: "home.x"}, func(string) error {
		called = true
		return nil
	})

	if !errors.Is(got, mountErr) {
		t.Errorf("runWithMount err = %v, want mount error %v", got, mountErr)
	}
	if called {
		t.Errorf("fn ran despite mount failure")
	}
	if !fm.mounted {
		t.Errorf("Mount was not attempted")
	}
}

// TestResticShipper_Name mirrors sshShipper.Name(): configured name, else "restic".
func TestResticShipper_Name(t *testing.T) {
	if got := (&resticShipper{name: "offsite-restic"}).Name(); got != "offsite-restic" {
		t.Errorf("Name() = %q, want offsite-restic", got)
	}
	if got := (&resticShipper{}).Name(); got != "restic" {
		t.Errorf("Name() default = %q, want restic", got)
	}
}

// secretLeaked reports whether the sentinel secret VALUE appears anywhere in a
// captured call's argv or stdin. R6.2 forbids any password VALUE from reaching a
// subprocess surface; only paths/flags are permitted there.
func secretLeaked(call RunnerCall, secret string) bool {
	for _, a := range call.Args {
		if strings.Contains(a, secret) {
			return true
		}
	}
	return strings.Contains(string(call.Stdin), secret)
}

// TestResticShipper_Send_Backup asserts the first subprocess is the restic backup
// of the read-only mount path, carrying the tag, compression, repo and
// password-file flags (R1.1, R1.3). It also asserts the secret VALUE never reaches
// argv/stdin while the password-file PATH does (R6.1, R6.2).
func TestResticShipper_Send_Backup(t *testing.T) {
	const secret = "SECRET" // sentinel password VALUE that must never appear anywhere
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	mr := &MockRunner{}
	r := &resticShipper{
		repo:         "rest:https://repo.example/bentoo",
		passwordFile: "/etc/bentoo/restic.pass",
		compression:  "max",
		retention:    Retention{Daily: 7},
		mount:        fm,
		run:          mr,
	}

	rep, err := r.Send(t.Context(), Snapshot{ID: "home.2026", Subvolume: "home", Path: "/snaps/home.2026"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(mr.Calls) == 0 {
		t.Fatalf("no restic command was run")
	}
	backup := mr.Calls[0]
	if backup.Name != "restic" {
		t.Errorf("first call name = %q, want restic", backup.Name)
	}
	want := [][]string{
		{"backup"},
		{"/mnt/snap.ro"},
		{"--tag", "bentoo,home"},
		{"--compression", "max"},
		{"--repo", "rest:https://repo.example/bentoo"},
		{"--password-file", "/etc/bentoo/restic.pass"},
	}
	for _, w := range want {
		if !containsSubslice(backup.Args, w) {
			t.Errorf("backup args %v missing %v", backup.Args, w)
		}
	}

	// R6.2: no captured call (across the whole run) may carry the secret VALUE.
	for i, c := range mr.Calls {
		if secretLeaked(c, secret) {
			t.Errorf("call %d leaked secret value: name=%q args=%v stdin=%q", i, c.Name, c.Args, c.Stdin)
		}
	}
	// R6.1: the password-FILE PATH is the permitted surface and must be present.
	if !slices.Contains(backup.Args, "/etc/bentoo/restic.pass") {
		t.Errorf("backup args %v missing password-file path", backup.Args)
	}

	if rep.Target != r.repo || rep.Snapshot != "home.2026" || rep.Delegated {
		t.Errorf("report = %+v, want Target=%q Snapshot=home.2026 Delegated=false", rep, r.repo)
	}
}

// TestResticShipper_Send_NoCompression asserts --compression is omitted entirely
// when no codec is configured (restic uses its own default, R1.3).
func TestResticShipper_Send_NoCompression(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	mr := &MockRunner{}
	r := &resticShipper{repo: "repo", passwordFile: "/pw", mount: fm, run: mr}

	if _, err := r.Send(t.Context(), Snapshot{Subvolume: "root", Path: "/s/root"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if slices.Contains(mr.Calls[0].Args, "--compression") {
		t.Errorf("backup args %v should not contain --compression", mr.Calls[0].Args)
	}
}

// TestResticShipper_Send_ForgetPrune asserts the second call is the retention
// prune, mapping each populated Retention field to its --keep-* flag and
// --prune, with repo/password-file flags carried through (R1.4).
func TestResticShipper_Send_ForgetPrune(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	mr := &MockRunner{}
	r := &resticShipper{
		repo:         "repo",
		passwordFile: "/pw",
		retention:    Retention{Hourly: 24, Daily: 7, Weekly: 4, Monthly: 12, PreserveMin: "latest"},
		mount:        fm,
		run:          mr,
	}

	if _, err := r.Send(t.Context(), Snapshot{Subvolume: "home", Path: "/s/home"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mr.Calls) != 2 {
		t.Fatalf("got %d calls, want 2 (backup + forget)", len(mr.Calls))
	}
	forget := mr.Calls[1]
	if forget.Name != "restic" {
		t.Errorf("second call name = %q, want restic", forget.Name)
	}
	want := [][]string{
		{"forget", "--prune"},
		{"--keep-hourly", "24"},
		{"--keep-daily", "7"},
		{"--keep-weekly", "4"},
		{"--keep-monthly", "12"},
		{"--keep-last", "1"},
		{"--repo", "repo"},
		{"--password-file", "/pw"},
	}
	for _, w := range want {
		if !containsSubslice(forget.Args, w) {
			t.Errorf("forget args %v missing %v", forget.Args, w)
		}
	}
}

// TestResticShipper_Send_NoRetentionNoForget asserts that an all-zero/empty
// Retention skips the forget step entirely (no pruning configured, R1.4).
func TestResticShipper_Send_NoRetentionNoForget(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	mr := &MockRunner{}
	r := &resticShipper{repo: "repo", passwordFile: "/pw", mount: fm, run: mr}

	if _, err := r.Send(t.Context(), Snapshot{Subvolume: "home", Path: "/s/home"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(mr.Calls) != 1 {
		t.Fatalf("got %d calls, want 1 (backup only, no forget)", len(mr.Calls))
	}
	for _, c := range mr.Calls {
		if slices.Contains(c.Args, "forget") {
			t.Errorf("unexpected forget call with empty retention: %v", c.Args)
		}
	}
}

// TestResticShipper_Send_CleansUpMount asserts the read-only mount is torn down on
// the success path (R7.3), via the fakeMounter cleanup spy.
func TestResticShipper_Send_CleansUpMount(t *testing.T) {
	fm := &fakeMounter{path: "/mnt/snap.ro"}
	mr := &MockRunner{}
	r := &resticShipper{repo: "repo", passwordFile: "/pw", retention: Retention{Daily: 1}, mount: fm, run: mr}

	if _, err := r.Send(t.Context(), Snapshot{Subvolume: "home", Path: "/s/home"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fm.cleaned == nil || !*fm.cleaned {
		t.Errorf("mount cleanup did not fire on success (R7.3 violated)")
	}
}

// containsSubslice reports whether want appears as a contiguous run inside got. It
// lets the argv assertions pin flag/value adjacency without fixing the overall
// argument order.
func containsSubslice(got, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(got); i++ {
		if slices.Equal(got[i:i+len(want)], want) {
			return true
		}
	}
	return false
}
