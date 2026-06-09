package snapshot

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// ---------------------------------------------------------------------------
// T6.1 — restore dispatch + archive chain validation + confirmFunc seam.
//
// The tested deliverable (R5.2/G3) is the pure validateChain logic, the ordered
// application of an archive incremental chain, and the refuse-BEFORE-receive
// guarantee — plus the destructive-restore confirm gate (R5.4) and restic
// granular restore with secrets carried only as flag PATHS (R6.1).
// ---------------------------------------------------------------------------

// receiveTargets scans a MockRunner's calls for `btrfs receive <target>` and
// returns the target path of each, in order. It is how the archive tests prove
// btrfs receive ran once per chain link (and, for the broken-chain test, that it
// ran ZERO times — nothing was applied).
func receiveTargets(calls []RunnerCall) []string {
	var out []string
	for _, c := range calls {
		if c.Name == "btrfs" && len(c.Args) >= 2 && c.Args[0] == "receive" {
			out = append(out, c.Args[1])
		}
	}
	return out
}

// catObjects scans a MockRunner's calls for `rclone cat <remote>/<obj>` and
// returns the full <remote>/<obj> source of each, in order, so a test can assert
// each chain link's object was fetched (and in order).
func catObjects(calls []RunnerCall) []string {
	var out []string
	for _, c := range calls {
		if c.Name == "rclone" && len(c.Args) >= 2 && c.Args[0] == "cat" {
			out = append(out, c.Args[1])
		}
	}
	return out
}

// validChain is a contiguous full→d1→d2 chain used across the happy-path tests.
func validChain() []chainLink {
	return []chainLink{
		{ID: "full", ParentID: "", Object: "home-full.zst"},
		{ID: "d1", ParentID: "full", Object: "home-d1.zst"},
		{ID: "d2", ParentID: "d1", Object: "home-d2.zst"},
	}
}

// TestValidateChain_Valid: a contiguous full→d1→d2 chain validates (nil).
func TestValidateChain_Valid(t *testing.T) {
	if err := validateChain(validChain()); err != nil {
		t.Errorf("validateChain(valid) = %v, want nil", err)
	}
}

// TestValidateChain_Empty: an empty chain is broken (no full base to restore).
func TestValidateChain_Empty(t *testing.T) {
	if err := validateChain(nil); !errors.Is(err, ErrBrokenChain) {
		t.Errorf("validateChain(empty) = %v, want ErrBrokenChain", err)
	}
}

// TestValidateChain_FirstNotFull: the chain must begin with a FULL (ParentID==""),
// otherwise its base is missing and the chain is broken.
func TestValidateChain_FirstNotFull(t *testing.T) {
	chain := []chainLink{
		{ID: "d1", ParentID: "full", Object: "home-d1.zst"}, // starts mid-chain
		{ID: "d2", ParentID: "d1", Object: "home-d2.zst"},
	}
	if err := validateChain(chain); !errors.Is(err, ErrBrokenChain) {
		t.Errorf("validateChain(first-not-full) = %v, want ErrBrokenChain", err)
	}
}

// TestValidateChain_Gap: a missing delta (d2.ParentID does not equal d1.ID) breaks
// the chain — the link's base is absent.
func TestValidateChain_Gap(t *testing.T) {
	chain := []chainLink{
		{ID: "full", ParentID: "", Object: "home-full.zst"},
		{ID: "d1", ParentID: "full", Object: "home-d1.zst"},
		{ID: "d2", ParentID: "GONE", Object: "home-d2.zst"}, // gap: parent is not d1
	}
	if err := validateChain(chain); !errors.Is(err, ErrBrokenChain) {
		t.Errorf("validateChain(gap) = %v, want ErrBrokenChain", err)
	}
}

// TestRestore_Archive_ReceivesInOrder is the core happy path (R5.2): a valid
// 3-link chain applies one `rclone cat | zstd -d | btrfs receive` per link IN
// ORDER, and the data chains stage→stage through the pipe (mirrors the T3.1
// pipe-chaining assertion).
func TestRestore_Archive_ReceivesInOrder(t *testing.T) {
	// Script each pipe stage to emit a marker so we can prove stdin chaining:
	// rclone cat → "CAT", zstd -d → "PLAIN", btrfs receive → "" (sink).
	mr := &MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			switch {
			case name == "rclone" && len(args) > 0 && args[0] == "cat":
				return []byte("CAT"), nil
			case name == "zstd":
				return []byte("PLAIN"), nil
			case name == "btrfs" && len(args) > 0 && args[0] == "receive":
				return nil, nil
			}
			t.Fatalf("unexpected stage: %s %v", name, args)
			return nil, nil
		},
	}
	opts := RestoreOptions{
		Driver: "archive",
		Yes:    true,
		Remote: "gdrive:bentoo-backups",
		Chain:  validChain(),
		Run:    mr,
	}
	if err := Restore(t.Context(), "home.d2", "/mnt/restore", opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// btrfs receive ran once per link, all targeting /mnt/restore, in chain order.
	gotRecv := receiveTargets(mr.Calls)
	wantRecv := []string{"/mnt/restore", "/mnt/restore", "/mnt/restore"}
	if !slices.Equal(gotRecv, wantRecv) {
		t.Errorf("btrfs receive targets = %v, want %v (one per link, in order)", gotRecv, wantRecv)
	}

	// rclone cat fetched each link's object, in chain order (full → d1 → d2).
	gotCat := catObjects(mr.Calls)
	wantCat := []string{
		"gdrive:bentoo-backups/home-full.zst",
		"gdrive:bentoo-backups/home-d1.zst",
		"gdrive:bentoo-backups/home-d2.zst",
	}
	if !slices.Equal(gotCat, wantCat) {
		t.Errorf("rclone cat objects = %v, want %v (in chain order)", gotCat, wantCat)
	}

	// Stage chaining within a link: zstd stdin == rclone cat stdout ("CAT"),
	// btrfs receive stdin == zstd stdout ("PLAIN"). Find the first link's stages.
	var catIdx int = -1
	for i, c := range mr.Calls {
		if c.Name == "rclone" && len(c.Args) > 0 && c.Args[0] == "cat" {
			catIdx = i
			break
		}
	}
	if catIdx < 0 || catIdx+2 >= len(mr.Calls) {
		t.Fatalf("could not locate a full cat→zstd→receive stage triple in %d calls", len(mr.Calls))
	}
	if got := string(mr.Calls[catIdx+1].Stdin); got != "CAT" {
		t.Errorf("zstd stdin = %q, want CAT (rclone cat stdout)", got)
	}
	if got := string(mr.Calls[catIdx+2].Stdin); got != "PLAIN" {
		t.Errorf("btrfs receive stdin = %q, want PLAIN (zstd stdout)", got)
	}

	// The decompressor stage must be `zstd -d` (the decompress switch, R5.2).
	zstdCall := mr.Calls[catIdx+1]
	if zstdCall.Name != "zstd" || !slices.Contains(zstdCall.Args, "-d") {
		t.Errorf("decompress stage = %s %v, want `zstd -d`", zstdCall.Name, zstdCall.Args)
	}
}

// TestRestore_Archive_BrokenChainRefusedPreReceive is the G3 deliverable: a broken
// chain is refused with ErrBrokenChain and NOTHING is applied — the MockRunner
// records ZERO btrfs receive (and ideally zero subprocess) calls. Validation
// happens BEFORE any receive (R5.2).
func TestRestore_Archive_BrokenChainRefusedPreReceive(t *testing.T) {
	broken := []chainLink{
		{ID: "full", ParentID: "", Object: "home-full.zst"},
		{ID: "d2", ParentID: "GONE", Object: "home-d2.zst"}, // gap → missing base
	}
	mr := &MockRunner{} // any subprocess call would be a violation
	opts := RestoreOptions{
		Driver: "archive",
		Yes:    true,
		Remote: "gdrive:bentoo-backups",
		Chain:  broken,
		Run:    mr,
	}
	err := Restore(t.Context(), "home.d2", "/mnt/restore", opts)
	if !errors.Is(err, ErrBrokenChain) {
		t.Fatalf("Restore(broken chain) = %v, want ErrBrokenChain", err)
	}
	if got := receiveTargets(mr.Calls); len(got) != 0 {
		t.Errorf("btrfs receive ran %v on a broken chain — G3 violated (must refuse pre-receive)", got)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("broken chain ran %d subprocess(es); want 0 — nothing must be applied", len(mr.Calls))
	}
}

// TestRestore_Restic_Granular asserts the restic path runs
// `restic restore <id> --target <target> --include <path> --repo ... --password-file ...`
// for a granular single-file/subdir restore (R5.3), and that the secret VALUE
// never reaches argv/stdin — only the password-file PATH does (R6.1).
func TestRestore_Restic_Granular(t *testing.T) {
	const secret = "SECRET" // sentinel password VALUE that must never appear
	mr := &MockRunner{}
	opts := RestoreOptions{
		Driver:       "restic",
		Yes:          true,
		Repo:         "rest:https://repo.example/bentoo",
		PasswordFile: "/etc/bentoo/restic.pass",
		Include:      "etc/foo.conf",
		Run:          mr,
	}
	if err := Restore(t.Context(), "home.2026", "/mnt/restore", opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(mr.Calls) != 1 {
		t.Fatalf("got %d calls, want exactly 1 (restic restore)", len(mr.Calls))
	}
	call := mr.Calls[0]
	if call.Name != "restic" {
		t.Errorf("call name = %q, want restic", call.Name)
	}
	want := [][]string{
		{"restore", "home.2026"},
		{"--target", "/mnt/restore"},
		{"--include", "etc/foo.conf"},
		{"--repo", "rest:https://repo.example/bentoo"},
		{"--password-file", "/etc/bentoo/restic.pass"},
	}
	for _, w := range want {
		if !containsSubslice(call.Args, w) {
			t.Errorf("restic args %v missing %v", call.Args, w)
		}
	}
	// R6.1/R6.2: no captured call may carry the secret VALUE; the file PATH may.
	if secretLeaked(call, secret) {
		t.Errorf("restic call leaked secret value: args=%v stdin=%q", call.Args, call.Stdin)
	}
	if !slices.Contains(call.Args, "/etc/bentoo/restic.pass") {
		t.Errorf("restic args %v missing password-file path", call.Args)
	}
}

// TestRestore_Restic_NoIncludeOmitsFlag asserts --include is omitted entirely when
// opts.Include is empty (a full restic restore, not granular).
func TestRestore_Restic_NoIncludeOmitsFlag(t *testing.T) {
	mr := &MockRunner{}
	opts := RestoreOptions{
		Driver:       "restic",
		Yes:          true,
		Repo:         "repo",
		PasswordFile: "/pw",
		Run:          mr,
	}
	if err := Restore(t.Context(), "id1", "/mnt/restore", opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(mr.Calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(mr.Calls))
	}
	if slices.Contains(mr.Calls[0].Args, "--include") {
		t.Errorf("restic args %v must not contain --include when Include is empty", mr.Calls[0].Args)
	}
}

// TestRestore_ConfirmDenied_NoOp is the R5.4 gate: with Yes=false and a confirm
// func that DENIES, Restore returns ErrRestoreDeclined and runs NOTHING — the
// MockRunner records ZERO calls. The gate fires before any subprocess.
func TestRestore_ConfirmDenied_NoOp(t *testing.T) {
	mr := &MockRunner{}
	opts := RestoreOptions{
		Driver:  "archive",
		Yes:     false,
		Confirm: func(string) bool { return false },
		Remote:  "r:bkt",
		Chain:   validChain(),
		Run:     mr,
	}
	err := Restore(t.Context(), "home.d2", "/mnt/restore", opts)
	if !errors.Is(err, ErrRestoreDeclined) {
		t.Fatalf("Restore(declined) = %v, want ErrRestoreDeclined", err)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("declined restore ran %d subprocess(es); want 0 (no-op)", len(mr.Calls))
	}
}

// TestRestore_ConfirmApproved_Proceeds: with Yes=false and a confirm func that
// APPROVES, an archive restore of a valid chain proceeds — btrfs receive runs.
func TestRestore_ConfirmApproved_Proceeds(t *testing.T) {
	mr := &MockRunner{}
	opts := RestoreOptions{
		Driver:  "archive",
		Yes:     false,
		Confirm: func(string) bool { return true },
		Remote:  "r:bkt",
		Chain:   validChain(),
		Run:     mr,
	}
	if err := Restore(t.Context(), "home.d2", "/mnt/restore", opts); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := receiveTargets(mr.Calls); len(got) != 3 {
		t.Errorf("approved restore ran %d btrfs receive, want 3 (one per link)", len(got))
	}
}

// TestRestore_InvalidDriver: an unknown driver is rejected with ErrInvalidDriver
// (R5.1 dispatch default), and nothing is applied.
func TestRestore_InvalidDriver(t *testing.T) {
	mr := &MockRunner{}
	opts := RestoreOptions{Driver: "zfs", Yes: true, Run: mr}
	err := Restore(t.Context(), "id", "/mnt/restore", opts)
	if !errors.Is(err, ErrInvalidDriver) {
		t.Fatalf("Restore(zfs driver) = %v, want ErrInvalidDriver", err)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("invalid driver ran %d subprocess(es); want 0", len(mr.Calls))
	}
}
