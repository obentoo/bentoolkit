package snapshot

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 038 — LIVE acceptance against a real rclone binary and a real directory tree.
//
// Every other test in this package proves the fix against the mock Runner. That
// is the right rung for logic, and it is the wrong rung for the four rclone
// BEHAVIOURS the fix is built on. Those were measured by hand during design
// (rclone 1.75.0) and, until this file, were pinned nowhere: if rclone changed
// any of them the whole suite would stay green while backups broke again in
// silence — which is precisely the failure mode 038 exists to remove.
//
// What these tests use that the mock ones cannot:
//
//   - the PRODUCTION Runner (defaultRunner → execRunner), so the error a caller
//     sees is the one os/exec really builds, with the child's stderr joined onto
//     it and LC_ALL=C pinned by runnerEnv;
//   - a real `rclone` process against a real directory tree as the remote.
//
// Local paths ARE rclone remotes, so no cloud account, no credentials and no
// network are involved, and no btrfs subvolume is needed: the defect lives in
// the prune, and pruneRemote/PruneRemoteOnDemand are driven directly.
//
// HONEST LIMIT ON WHERE THIS RUNS. These tests SKIP when rclone is absent, and
// GitHub's ubuntu-latest runner does not ship rclone — so today they are local
// evidence, not a CI gate, and a green CI run does not mean they passed. Adding
// rclone to the test job would turn them into one.
// ---------------------------------------------------------------------------

// requireRclone skips the test when no rclone binary is on PATH, and otherwise
// returns nothing but the assurance that exec will find one. The skip is loud on
// purpose: a silent skip is indistinguishable from a pass in a CI log.
func requireRclone(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skipf("SKIPPED, NOT PASSED: no rclone on PATH (%v). These are the only tests that verify rclone's actual behaviour; without them the fix rests on assumptions measured by hand during design.", err)
	}
}

// liveRcat writes payload to key under remoteDir through the REAL `rclone rcat`,
// then stamps the object with modTime so calendar bucketing is deterministic
// (rclone's local backend reports the file's mtime as ModTime). It deliberately
// does not create the parent directory: that rcat does so on its own is one of
// the behaviours under test.
func liveRcat(t *testing.T, remoteDir, key, payload string, modTime time.Time) {
	t.Helper()
	cmd := exec.Command("rclone", "rcat", remoteDir+"/"+key)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = runnerEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rclone rcat %s: %v\n%s", key, err, out)
	}
	if err := os.Chtimes(filepath.Join(remoteDir, filepath.FromSlash(key)), modTime, modTime); err != nil {
		t.Fatalf("stamping %s: %v", key, err)
	}
}

// exists reports whether key is present under remoteDir on disk. The assertions
// below check the FILESYSTEM rather than what the mock recorded, because "the
// backup is still there" is the property, and a recorded call is only a proxy.
func exists(t *testing.T, remoteDir, key string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(remoteDir, filepath.FromSlash(key)))
	return err == nil
}

// TestLive_RcloneAssumptionsStillHold pins the four rclone behaviours 038's
// design rests on. Each has a comment naming what breaks if it changes, because
// a bare assertion here would look like trivia to a later reader.
func TestLive_RcloneAssumptionsStillHold(t *testing.T) {
	requireRclone(t)
	remote := t.TempDir()
	run := defaultRunner()
	stamp := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)

	// (1) rcat creates the parent directory on its own — including one named "-",
	// which is the root subvolume "/" after sanitize (R3.3). If this stopped
	// holding, every first ship of a subvolume would fail: archivePipeStages emits
	// no mkdir stage precisely because of this.
	liveRcat(t, remote, ArchiveObjectName("/", "root.A"), "PAYLOAD-ROOT", stamp)
	liveRcat(t, remote, ArchiveObjectName("/home", "home.A"), "PAYLOAD-HOME", stamp)
	if !exists(t, remote, "-/root.A.zst") {
		t.Fatalf("rclone rcat did not create the parent directory %q — archivePipeStages emits no mkdir stage and relies on it", "-")
	}

	// (2) A SCOPED listing reports leaf names, relative to the listed path — NOT
	// the full key. pruneRemote's active-parent guard compares
	// ArchiveObjectLeaf(snap.ID) because of this. If rclone started reporting full
	// keys, the guard would silently match nothing and the lineage head would be
	// deleted on every prune.
	out, err := run.Run(t.Context(), "rclone", []string{"lsjson", remote + "/" + ArchivePrefix("/home")}, nil)
	if err != nil {
		t.Fatalf("scoped lsjson: %v", err)
	}
	scoped, err := decodeLsjson(out)
	if err != nil {
		t.Fatalf("decoding scoped lsjson: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Name != ArchiveObjectLeaf("home.A") {
		t.Errorf("scoped listing = %+v, want exactly one entry named %q (the LEAF, not the key)", scoped, ArchiveObjectLeaf("home.A"))
	}

	// (3) A ROOT listing reports directories with IsDir true, and a scoped one
	// contains no directories at all. Together these are why D5's filter exists
	// and why it is defensive rather than load-bearing: nothing on the scoped path
	// can produce a directory entry, but a caller listing the root would get one
	// per subvolume and hand it to deletefile.
	rootOut, err := run.Run(t.Context(), "rclone", []string{"lsjson", remote}, nil)
	if err != nil {
		t.Fatalf("root lsjson: %v", err)
	}
	var raw []rcloneObject
	if err := json.Unmarshal(rootOut, &raw); err != nil {
		t.Fatalf("decoding root lsjson: %v", err)
	}
	var dirs int
	for _, o := range raw {
		if o.IsDir {
			dirs++
		}
	}
	if dirs != 2 {
		t.Errorf("root listing reported %d directory entries, want 2 (-, -home) — D5's IsDir filter guards exactly this", dirs)
	}
	if filtered, err := decodeLsjson(rootOut); err != nil {
		t.Fatalf("decodeLsjson: %v", err)
	} else if len(filtered) != 0 {
		t.Errorf("decodeLsjson kept %+v from a listing of only directories — every entry must be dropped (R4.3)", filtered)
	}

	// (4) Listing a path that does not exist fails; the error must be recognised
	// BEFORE parsing, because rclone writes a bare "[" to stdout, which is invalid
	// JSON. isRemoteDirNotFound must classify the error the PRODUCTION runner
	// builds — not a hand-made one. This is the assertion that retires the doubt
	// recorded in 3.2 about whether the exit code and the message text really
	// arrive the way the predicate expects.
	missingOut, err := run.Run(t.Context(), "rclone", []string{"lsjson", remote + "/never-shipped"}, nil)
	if err == nil {
		t.Fatal("lsjson on a missing path returned no error; R4.2's whole benign branch assumes it fails")
	}
	if !isRemoteDirNotFound(err) {
		t.Errorf("isRemoteDirNotFound(%v) = false — a missing prefix would surface as a failed prune on every freshly installed system (R4.2)", err)
	}
	var parsed []rcloneObject
	if err := json.Unmarshal(missingOut, &parsed); err == nil {
		t.Errorf("stdout %q parsed as JSON; the error must be handled before decoding precisely because it does not", missingOut)
	}
}

// TestLive_PruneDoesNotDeleteAnotherSubvolumesBackup is 038's defect itself,
// replayed at the highest fidelity available without a btrfs filesystem: real
// rclone, real files, the production Runner, and the production prune. It
// asserts on the FILESYSTEM — the bytes are either there or they are not.
//
// The fixture is built so three DIFFERENT regressions each turn it red, which
// took two attempts to get right and is worth recording:
//
//   - /home's object must survive a /root prune (the defect, R1.1).
//   - /root's stale object must be DELETED, proving the prune actually ran. A
//     first version omitted this and could not detect an unscoped listing: on a
//     real remote the root holds only DIRECTORIES, which D5's IsDir filter drops,
//     so the prune silently sees an empty listing and deletes nothing. The old
//     whole-remote bug is inert at this rung precisely because of that filter —
//     the mock test is what pins the scoping, and this assertion is what stops
//     "nothing was deleted" from reading as success.
//   - /root's JUST-SHIPPED object must survive even though it is out of policy,
//     which is the only thing the active-parent guard does. A first version had a
//     single object under /root, so retention kept it anyway and a full-key guard
//     (the silent leaf-vs-key regression) went undetected.
func TestLive_PruneDoesNotDeleteAnotherSubvolumesBackup(t *testing.T) {
	requireRclone(t)
	remote := t.TempDir()

	// One calendar day holds three of these, so Retention{Daily: 1} keeps a single
	// representative — the newest — and drops the rest of that day plus every
	// older bucket.
	homeAt := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	shippedAt := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	newerAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	staleAt := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	homeKey := ArchiveObjectName("/home", "home.A")    // another subvolume: must survive
	shippedKey := ArchiveObjectName("/root", "root.B") // just shipped: out of policy, spared by the guard
	newerKey := ArchiveObjectName("/root", "root.C")   // wins the day bucket
	staleKey := ArchiveObjectName("/root", "root.OLD") // older bucket: must be deleted

	liveRcat(t, remote, homeKey, "HOME-BACKUP", homeAt)
	liveRcat(t, remote, shippedKey, "ROOT-HEAD", shippedAt)
	liveRcat(t, remote, newerKey, "ROOT-NEWER", newerAt)
	liveRcat(t, remote, staleKey, "ROOT-STALE", staleAt)

	a := &archiveShipper{
		name:      "cloud",
		remote:    remote,
		mode:      "incremental",
		compress:  "zstd",
		run:       defaultRunner(),
		parents:   newMapParentStore(),
		retention: Retention{Daily: 1},
	}

	// Fixture integrity: prove that if the prune HAD seen /home's object together
	// with /root's — the pre-038 flat namespace — /home's would be the one
	// selected for deletion. Otherwise "it survived" says nothing.
	//
	// The ModTimes are read back off the real remote through real rclone, so the
	// calendar bucketing under test is the filesystem's, not a literal typed here.
	// Only their COMBINATION into one listing is synthetic, and that is exactly
	// what the old whole-remote listing produced.
	//
	// `lsjson -R` is deliberately NOT used to build it: with -R rclone puts the
	// nested path in Path and leaves Name as the bare leaf, and rcloneObject
	// decodes only Name — so a flat listing built that way holds leaves, matches
	// no full key, and the guard would fail for a reason unrelated to the defect.
	// That is the same leaf-vs-key hazard design.md records when rejecting the
	// recursive-listing option, and it caught a real mistake in this test.
	modTimeOf := func(subvolume, leaf string) time.Time {
		t.Helper()
		out, err := a.run.Run(t.Context(), "rclone", []string{"lsjson", remote + "/" + ArchivePrefix(subvolume)}, nil)
		if err != nil {
			t.Fatalf("lsjson %s: %v", subvolume, err)
		}
		objs, err := decodeLsjson(out)
		if err != nil {
			t.Fatalf("decoding lsjson %s: %v", subvolume, err)
		}
		for _, o := range objs {
			if o.Name == leaf {
				return o.ModTime
			}
		}
		t.Fatalf("lsjson %s = %+v, missing %q", subvolume, objs, leaf)
		return time.Time{}
	}
	flat := []rcloneObject{
		{Name: homeKey, ModTime: modTimeOf("/home", ArchiveObjectLeaf("home.A"))},
		{Name: newerKey, ModTime: modTimeOf("/root", ArchiveObjectLeaf("root.C"))},
	}
	_, del := gfsSelect(flat, a.retention)
	var wouldDelete bool
	for _, d := range del {
		if d.Name == homeKey {
			wouldDelete = true
		}
	}
	if !wouldDelete {
		t.Fatalf("fixture does not reproduce the defect: a flat listing %+v would not select %q, so surviving proves nothing", flat, homeKey)
	}

	// Prune after /root's ship, exactly as Send does once the upload succeeded and
	// the head was recorded.
	a.pruneRemote(t.Context(), Snapshot{ID: "root.B", Subvolume: "/root", Path: "/snaps/root.B"})

	if !exists(t, remote, homeKey) {
		t.Errorf("%s is GONE after pruning /root — a ship of one subvolume destroyed another's backup (R1.1, R2.1)", homeKey)
	}
	if exists(t, remote, staleKey) {
		t.Errorf("%s survived: the prune deleted nothing, so the two assertions around it cannot show anything", staleKey)
	}
	if !exists(t, remote, shippedKey) {
		t.Errorf("%s is gone — the just-shipped object is out of policy and ONLY the active-parent guard spares it; a guard comparing a full key against a scoped listing's leaf matches nothing, silently (R2.1)", shippedKey)
	}
	if !exists(t, remote, newerKey) {
		t.Errorf("%s is gone — it is the newest in its bucket and retention must keep it", newerKey)
	}
}

// TestLive_PruneOnDemandSkipsAnUnshippedSubvolume is R4.2 against a real rclone:
// a manual prune over a subvolume nothing has been shipped for is the ordinary
// first-run state, and it must not fail the command. The mock tests script that
// error; this one provokes the real one.
func TestLive_PruneOnDemandSkipsAnUnshippedSubvolume(t *testing.T) {
	requireRclone(t)
	remote := t.TempDir()

	shippedKey := ArchiveObjectName("/home", "home.A")
	staleKey := ArchiveObjectName("/home", "home.old")
	liveRcat(t, remote, shippedKey, "HEAD", time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC))
	liveRcat(t, remote, staleKey, "STALE", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))

	a := &archiveShipper{
		name:      "cloud",
		remote:    remote,
		mode:      "incremental",
		compress:  "zstd",
		run:       defaultRunner(),
		parents:   newMapParentStore(),
		retention: Retention{Daily: 1},
	}
	warnings := captureWarn(t)

	// "/var" has no directory on the remote at all: nothing was ever shipped for
	// it. It is listed FIRST, so a `return` where the code must `continue` would
	// leave /home unpruned and be caught here.
	if err := a.PruneRemoteOnDemand(t.Context(), []string{"/var", "/home"}); err != nil {
		t.Fatalf("PruneRemoteOnDemand returned %v, want nil — a never-shipped subvolume is nothing to prune, not a failure (R4.2)", err)
	}

	if !exists(t, remote, shippedKey) {
		t.Errorf("%s was deleted; it is the newest in its bucket and must be kept", shippedKey)
	}
	if exists(t, remote, staleKey) {
		t.Errorf("%s survived: /home was not pruned at all, so the missing /var prefix aborted the loop instead of skipping it (R4.2)", staleKey)
	}

	var named bool
	for _, m := range warnings() {
		if strings.Contains(m, "/var") {
			named = true
		}
	}
	if !named {
		t.Errorf("warnings %v do not name the skipped subvolume /var (R4.2)", warnings())
	}
}
