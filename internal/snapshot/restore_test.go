package snapshot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
	catIdx := -1
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

// ---------------------------------------------------------------------------
// 038 T2.1 — the remote key carries the subvolume as a DIRECTORY (R3, R3.1,
// R3.3).
//
// The property under test is NOT the two literal strings these functions
// produce, it is that `sanitize` can never emit '/': every byte outside
// [A-Za-z0-9._-] becomes '-', so the single '/' ArchiveObjectName inserts is the
// ONLY separator in the key. That is what makes prefix and leaf unambiguous.
// The old flat scheme joined them with '-', a byte sanitize CAN emit, so the
// boundary was indistinguishable from sanitized content and nested subvolumes
// could render to the same key.
// ---------------------------------------------------------------------------

// TestArchivePrefix_SanitizeAppliedWithNoSpecialCase pins R3.3: the prefix is
// `sanitize(subvolume)` unchanged, with no special case whatsoever — the root
// subvolume "/" is not exempted and its prefix is therefore the directory "-".
// The final check is the load-bearing one: a prefix NEVER contains '/', which is
// the invariant every other test here rests on.
func TestArchivePrefix_SanitizeAppliedWithNoSpecialCase(t *testing.T) {
	cases := []struct{ subvolume, want string }{
		{"/", "-"},                     // R3.3: root gets no special case
		{"/home", "-home"},             //
		{"/home/otaku", "-home-otaku"}, // separators sanitize to '-' like any other byte
		{"/var/lib/portage", "-var-lib-portage"},
	}
	for _, c := range cases {
		got := ArchivePrefix(c.subvolume)
		if got != c.want {
			t.Errorf("ArchivePrefix(%q) = %q, want %q", c.subvolume, got, c.want)
		}
		if got != sanitize(c.subvolume) {
			t.Errorf("ArchivePrefix(%q) = %q, want sanitize(%q) = %q — the prefix must be the sanitize rule unchanged",
				c.subvolume, got, c.subvolume, sanitize(c.subvolume))
		}
		if strings.Contains(got, "/") {
			t.Errorf("ArchivePrefix(%q) = %q contains '/' — sanitize must never emit one, or the key separator stops being unambiguous",
				c.subvolume, got)
		}
	}
}

// TestArchiveObjectLeaf_IsRelativeToTheListedPath pins the distinction this
// three-function split exists to make visible: the leaf is what
// `rclone lsjson <remote>/<prefix>` reports in Name — relative to the LISTED
// path, carrying no prefix — while ArchiveObjectName is the full key relative to
// the remote ROOT. The two are NOT interchangeable, and a comparison between
// them fails silently: never true, no error, no empty result.
func TestArchiveObjectLeaf_IsRelativeToTheListedPath(t *testing.T) {
	const id = "snap1"

	if got, want := ArchiveObjectLeaf(id), "snap1.zst"; got != want {
		t.Errorf("ArchiveObjectLeaf(%q) = %q, want %q", id, got, want)
	}
	if strings.Contains(ArchiveObjectLeaf(id), "/") {
		t.Errorf("ArchiveObjectLeaf(%q) = %q carries a path separator; a scoped listing reports NO prefix",
			id, ArchiveObjectLeaf(id))
	}
	// The silent-mismatch hazard, asserted rather than merely documented.
	if leaf, key := ArchiveObjectLeaf(id), ArchiveObjectName("/home", id); leaf == key {
		t.Fatalf("leaf %q equals full key %q — the test can no longer detect the confusion it guards", leaf, key)
	}
}

// TestArchiveObjectName_KeyIsPrefixSlashLeaf pins R3.1: the full key is
// "<sanitize(subvolume)>/<id>.zst", composed from the other two functions. The
// root subvolume "/" is included deliberately — its prefix is the directory "-"
// with no special case (R3.3), a layout verified to work end to end.
func TestArchiveObjectName_KeyIsPrefixSlashLeaf(t *testing.T) {
	cases := []struct{ subvolume, id, want string }{
		{"/", "snap1", "-/snap1.zst"},
		{"/home", "snap1", "-home/snap1.zst"},
		{"/home/otaku", "snap1", "-home-otaku/snap1.zst"},
	}
	for _, c := range cases {
		got := ArchiveObjectName(c.subvolume, c.id)
		if got != c.want {
			t.Errorf("ArchiveObjectName(%q, %q) = %q, want %q", c.subvolume, c.id, got, c.want)
		}
		if want := ArchivePrefix(c.subvolume) + "/" + ArchiveObjectLeaf(c.id); got != want {
			t.Errorf("ArchiveObjectName(%q, %q) = %q, want ArchivePrefix+\"/\"+ArchiveObjectLeaf = %q",
				c.subvolume, c.id, got, want)
		}
		// Exactly ONE separator: the one this function inserts.
		if n := strings.Count(got, "/"); n != 1 {
			t.Errorf("ArchiveObjectName(%q, %q) = %q has %d '/', want exactly 1 — the prefix/leaf boundary must be unique",
				c.subvolume, c.id, got, n)
		}
	}
}

// TestArchiveObjectName_NestedSubvolumesCannotCollide is the regression proper.
// Under the OLD flat scheme "<sanitize(subvolume)>-<id>.zst", subvolume "/home"
// with id "otaku-42" and subvolume "/home/otaku" with id "42" both rendered as
// "-home-otaku-42.zst" — one object key for two different snapshots, because the
// '-' joining prefix to id is a byte sanitize itself emits, so the boundary was
// unrecoverable. Separating them with '/', which sanitize can NEVER emit, makes
// the boundary unambiguous for every input.
func TestArchiveObjectName_NestedSubvolumesCannotCollide(t *testing.T) {
	const (
		parentSubvol, parentID = "/home", "otaku-42"
		childSubvol, childID   = "/home/otaku", "42"
	)

	// The old scheme's collision, reconstructed to show the pair is genuinely
	// adversarial and not just two arbitrary inputs.
	oldParent := sanitize(parentSubvol) + "-" + parentID + ".zst"
	oldChild := sanitize(childSubvol) + "-" + childID + ".zst"
	if oldParent != oldChild {
		t.Fatalf("fixture no longer collides under the old flat scheme (%q vs %q); it must, or this test proves nothing",
			oldParent, oldChild)
	}

	gotParent := ArchiveObjectName(parentSubvol, parentID)
	gotChild := ArchiveObjectName(childSubvol, childID)

	if gotParent == gotChild {
		t.Fatalf("ArchiveObjectName(%q, %q) and ArchiveObjectName(%q, %q) both = %q — two subvolumes still share one key",
			parentSubvol, parentID, childSubvol, childID, gotParent)
	}
	if want := "-home/otaku-42.zst"; gotParent != want {
		t.Errorf("ArchiveObjectName(%q, %q) = %q, want %q", parentSubvol, parentID, gotParent, want)
	}
	if want := "-home-otaku/42.zst"; gotChild != want {
		t.Errorf("ArchiveObjectName(%q, %q) = %q, want %q", childSubvol, childID, gotChild, want)
	}

	// Neither prefix is a prefix of the other AS A DIRECTORY, so a scoped listing
	// of one subvolume can never reach the other's objects.
	pParent, pChild := ArchivePrefix(parentSubvol)+"/", ArchivePrefix(childSubvol)+"/"
	if strings.HasPrefix(gotChild, pParent) {
		t.Errorf("child key %q lives under the parent's prefix %q — a scoped listing would still mix the two", gotChild, pParent)
	}
	if strings.HasPrefix(gotParent, pChild) {
		t.Errorf("parent key %q lives under the child's prefix %q — a scoped listing would still mix the two", gotParent, pChild)
	}
}

// ---------------------------------------------------------------------------
// T4.1 — ResolveRestoreSubvolume: which subvolume a restore reads from (R5).
//
// Restore is destructive, so the rule that picks the subvolume must fail on an
// ambiguous request BEFORE any subprocess rather than silently read the wrong
// one. Holding the rule in the package (not in the cobra handler) is what makes
// these branch-by-branch assertions possible without driving a command.
//
// The error MESSAGES are part of the deliverable, not decoration: they are read
// by an operator mid-restore, so each test asserts the message CONTENT. An
// `err != nil` assertion alone would pass for a message that helps nobody.
// ---------------------------------------------------------------------------

// engineWith builds a Config whose engine has exactly the given subvolumes.
func engineWith(subvolumes ...string) *Config {
	return &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: subvolumes}}
}

// assertNamesSubvolumes fails unless msg names every want subvolume AS ITS OWN
// entry. Plain substring containment is NOT enough: with "/home" and
// "/home/otaku" configured, a message naming only the second would "contain"
// the first and the assertion would pass while the operator is still missing an
// option. The quoted rendering is the entry boundary, so it is what is checked.
func assertNamesSubvolumes(t *testing.T, msg string, want []string) {
	t.Helper()
	for _, sv := range want {
		if !strings.Contains(msg, fmt.Sprintf("%q", sv)) {
			t.Errorf("error %q does not name configured subvolume %q — the operator cannot retry without opening the config",
				msg, sv)
		}
	}
}

// TestResolveRestoreSubvolume_SingleConfiguredNeedsNoFlag pins R5.1 and
// Unchanged Behavior #6: the ONLY deployed configuration is subvolumes = ["/"],
// and it must keep restoring with no new flag. Breaking this branch breaks the
// only real deployment, so the deployed spelling is a case in its own right.
func TestResolveRestoreSubvolume_SingleConfiguredNeedsNoFlag(t *testing.T) {
	for _, only := range []string{"/", "/home", "/var/lib/portage"} {
		got, err := ResolveRestoreSubvolume(engineWith(only), "")
		if err != nil {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=[%q], no flag) = error %v, want %q with no flag required (R5.1)",
				only, err, only)
			continue
		}
		if got != only {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=[%q], no flag) = %q, want %q", only, got, only)
		}
	}
}

// TestResolveRestoreSubvolume_AmbiguousNamesEveryConfigured pins R5.2: with two
// or more configured and no flag, the request is ambiguous and must FAIL —
// never guess — and the failure must name EVERY configured subvolume plus the
// flag that resolves it. A message that states a problem without stating the fix
// wastes the reader's time at the worst possible moment.
func TestResolveRestoreSubvolume_AmbiguousNamesEveryConfigured(t *testing.T) {
	cases := [][]string{
		{"/", "/home"},
		{"/", "/home", "/var/lib/portage"},
		{"/home", "/home/otaku"}, // adversarial: one name contains the other
	}
	for _, configured := range cases {
		got, err := ResolveRestoreSubvolume(engineWith(configured...), "")
		if err == nil {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, no flag) = %q with no error; an ambiguous restore must fail, not guess (R5.2)",
				configured, got)
			continue
		}
		if got != "" {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, no flag) returned subvolume %q alongside an error; a failed resolution must yield nothing usable",
				configured, got)
		}
		msg := err.Error()
		assertNamesSubvolumes(t, msg, configured)
		if !strings.Contains(msg, "--subvolume") {
			t.Errorf("error %q does not name --subvolume — it states the problem without stating the fix", msg)
		}
	}
}

// TestResolveRestoreSubvolume_UnknownFlagNamesItAndTheAlternatives pins R5.3: a
// --subvolume that is not configured fails naming THE VALUE PASSED, and listing
// the configured ones is what turns "wrong" into "here is the right spelling".
func TestResolveRestoreSubvolume_UnknownFlagNamesItAndTheAlternatives(t *testing.T) {
	configured := []string{"/", "/home"}
	for _, flag := range []string{"/hom", "/var", "home", "/HOME"} {
		got, err := ResolveRestoreSubvolume(engineWith(configured...), flag)
		if err == nil {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, flag=%q) = %q with no error; an unconfigured subvolume must fail (R5.3)",
				configured, flag, got)
			continue
		}
		if got != "" {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, flag=%q) returned %q alongside an error", configured, flag, got)
		}
		msg := err.Error()
		if !strings.Contains(msg, fmt.Sprintf("%q", flag)) {
			t.Errorf("error %q does not name the value passed (%q) — the operator cannot see what was rejected", msg, flag)
		}
		assertNamesSubvolumes(t, msg, configured)
	}
}

// TestResolveRestoreSubvolume_ValidFlagSelectsIt is R5.3's inverse, the plain
// success path: a flag naming a configured subvolume resolves to exactly that
// subvolume — including when several are configured, which is the whole point of
// the flag.
func TestResolveRestoreSubvolume_ValidFlagSelectsIt(t *testing.T) {
	configured := []string{"/", "/home", "/home/otaku"}
	for _, flag := range configured {
		got, err := ResolveRestoreSubvolume(engineWith(configured...), flag)
		if err != nil {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, flag=%q) = error %v, want %q", configured, flag, err, flag)
			continue
		}
		if got != flag {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, flag=%q) = %q, want %q", configured, flag, got, flag)
		}
	}
	// A single configured subvolume named explicitly resolves the same way.
	if got, err := ResolveRestoreSubvolume(engineWith("/"), "/"); err != nil || got != "/" {
		t.Errorf(`ResolveRestoreSubvolume(subvolumes=["/"], flag="/") = (%q, %v), want ("/", nil)`, got, err)
	}
}

// TestResolveRestoreSubvolume_MatchesExactSpelling pins the matching rule:
// EXACT string equality against the configured entries, no normalisation. The
// configured spelling is the key ArchivePrefix sanitizes into the remote prefix,
// so accepting "/home/" for a configured "/home" would send the restore to the
// prefix "-home-", which holds nothing — a silently wrong read on a destructive
// verb. Rejecting it is the safe answer, and R5.3 already says how to report it.
func TestResolveRestoreSubvolume_MatchesExactSpelling(t *testing.T) {
	configured := []string{"/home"}
	for _, flag := range []string{"/home/", "home", " /home", "/home "} {
		if ArchivePrefix(flag) == ArchivePrefix(configured[0]) {
			continue // not a distinguishable spelling; nothing to prove
		}
		got, err := ResolveRestoreSubvolume(engineWith(configured...), flag)
		if err == nil {
			t.Errorf("ResolveRestoreSubvolume(subvolumes=%q, flag=%q) = %q with no error; %q sanitizes to prefix %q, not %q, so accepting it would read the wrong objects",
				configured, flag, got, flag, ArchivePrefix(flag), ArchivePrefix(configured[0]))
		}
	}
}

// TestResolveRestoreSubvolume_NoneConfigured covers a config that legitimately
// reaches the resolver with an EMPTY subvolume list: Validate only WARNS about
// it (config.go, R1.4). The old `Subvolumes[0]` guess was length-guarded, so it
// did not panic — it silently produced the EMPTY subvolume, whose prefix is ""
// and whose key is "/<id>.zst": a restore pointed at a path nothing ever wrote.
// There is nothing to restore from and no list to suggest, so both the flagged
// and unflagged forms must fail — naming the empty setting so the operator fixes
// the config rather than the command.
func TestResolveRestoreSubvolume_NoneConfigured(t *testing.T) {
	for _, cfg := range []*Config{engineWith(), {Engine: EngineConfig{Driver: "btrbk"}}} {
		for _, flag := range []string{"", "/home"} {
			got, err := ResolveRestoreSubvolume(cfg, flag)
			if err == nil {
				t.Errorf("ResolveRestoreSubvolume(no subvolumes, flag=%q) = %q with no error; there is no subvolume to restore from", flag, got)
				continue
			}
			if got != "" {
				t.Errorf("ResolveRestoreSubvolume(no subvolumes, flag=%q) returned %q alongside an error", flag, got)
			}
			if msg := err.Error(); !strings.Contains(msg, "engine.subvolumes") {
				t.Errorf("error %q does not name engine.subvolumes — the fix is in the config, and the message must say so", msg)
			}
			if flag != "" {
				if msg := err.Error(); !strings.Contains(msg, fmt.Sprintf("%q", flag)) {
					t.Errorf("error %q does not name the value passed (%q)", msg, flag)
				}
			}
		}
	}
}

// TestResolveRestoreSubvolume_NilConfig: a nil *Config is only reachable through
// a programming error upstream, but in a DESTRUCTIVE verb a nil dereference is
// worth one line to avoid. It must return an error, not panic (the panic is what
// this test would report as a failure).
func TestResolveRestoreSubvolume_NilConfig(t *testing.T) {
	got, err := ResolveRestoreSubvolume(nil, "/home")
	if err == nil {
		t.Fatalf("ResolveRestoreSubvolume(nil, %q) = %q with no error; a nil config cannot resolve a subvolume", "/home", got)
	}
	if got != "" {
		t.Errorf("ResolveRestoreSubvolume(nil, ...) returned %q alongside an error", got)
	}
	if got, err := ResolveRestoreSubvolume(nil, ""); err == nil {
		t.Errorf("ResolveRestoreSubvolume(nil, \"\") = %q with no error", got)
	}
}
