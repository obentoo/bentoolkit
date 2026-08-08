package overlay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureReviewWarnings redirects the package's warning sink for one test and
// returns an accessor for the lines it collected.
//
// It is the only way to assert the "warn once and carry on" contract: the cache
// returns no error by design — every failure is absorbed into a miss or a
// skipped write — so the warning IS the observable, and a test that could not
// see it would be asserting the absence of an error the code cannot produce.
func captureReviewWarnings(t *testing.T) func() []string {
	t.Helper()
	var lines []string
	previous := warnLogf
	warnLogf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { warnLogf = previous })
	return func() []string { return lines }
}

// The two ebuilds one finding compares. They are realistic enough that a reader
// can see what a divergence is, and short enough to read: ours carries a patch
// ::gentoo does not ship.
var (
	reviewOurs = []byte("EAPI=8\nDESCRIPTION=\"a package\"\nPATCHES=( \"${FILESDIR}\"/fix.patch )\n")
	reviewThem = []byte("EAPI=8\nDESCRIPTION=\"a package\"\n")
)

// reviewFixtureRequest is the request the cache is asked about, unless a subtest
// varies part of it.
func reviewFixtureRequest() ReviewRequest {
	return ReviewRequest{
		Category: "net-libs",
		Package:  "nodejs",
		Version:  "26.7.0",
		Ours:     reviewOurs,
		Theirs:   reviewThem,
	}
}

// reviewFixtureNote is a filled note: an overlay-origin classification with a
// proposed declaration, which is the only combination R5.4 produces text for.
func reviewFixtureNote() ReviewNote {
	return ReviewNote{
		Origin:      OriginOverlay,
		Summary:     "adds a PaX marking step ::gentoo does not carry",
		Declaration: "patched = true\npatched_reason = \"PaX marking\"",
	}
}

// TestReviewCache is R5.7: a classification is cached on the content of the two
// ebuilds that produced it, and reused while both files are unchanged, so a
// repeated run prints the same text and issues no second request.
//
// Every failure path here is asserted to cost NOTHING but a recomputation. A
// cache is an optimisation; a run that refused to proceed without one would have
// made it a dependency.
//
// _Requirements: R5.7_
func TestReviewCache(t *testing.T) {
	t.Run("the index is the two files' hashes, ours first", func(t *testing.T) {
		// R5.7 spelled out, computed independently of the implementation.
		ours := sha256.Sum256(reviewOurs)
		theirs := sha256.Sum256(reviewThem)
		want := hex.EncodeToString(ours[:]) + ":" + hex.EncodeToString(theirs[:])

		if got := contentFingerprint(reviewOurs, reviewThem); got != want {
			t.Errorf("contentFingerprint = %q, want %q", got, want)
		}
		// The ORDER is load-bearing: ReviewNote.Origin names a side, so the pair
		// read the other way round is a different question with a different answer
		// and must not share an entry.
		if got := contentFingerprint(reviewThem, reviewOurs); got == want {
			t.Error("the two files' hashes commute; a swapped pair would reuse a note that names the wrong side")
		}
	})

	t.Run("a miss is followed by a hit for the same two files", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		cache := newReviewCache(t.TempDir())
		req := reviewFixtureRequest()

		if _, ok := cache.get(req); ok {
			t.Fatal("an empty cache reported a hit")
		}

		cache.put(req, reviewFixtureNote())

		got, ok := cache.get(req)
		if !ok {
			t.Fatal("the note just stored did not come back; every run would issue a second request")
		}
		if got != reviewFixtureNote() {
			t.Errorf("the cache returned %+v, want %+v", got, reviewFixtureNote())
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("an ordinary miss-then-hit warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("a note survives into the next run", func(t *testing.T) {
		// This is the requirement itself: the SECOND run is where a cache earns its
		// place, and an in-memory hit would pass the subtest above while issuing a
		// fresh request every time the operator ran the command.
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()
		req := reviewFixtureRequest()

		newReviewCache(dir).put(req, reviewFixtureNote())

		got, ok := newReviewCache(dir).get(req)
		if !ok {
			t.Fatal("a second run over the same directory missed; the note was not persisted")
		}
		if got != reviewFixtureNote() {
			t.Errorf("the second run read %+v, want %+v", got, reviewFixtureNote())
		}

		// The file follows the house convention for a cache: owner-only.
		info, err := os.Stat(filepath.Join(dir, reviewCacheFileName))
		if err != nil {
			t.Fatalf("stat the cache file: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("the cache file is mode %04o, want 0600 (fileutil.CacheFileMode)", perm)
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("a successful store-then-reload warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("a changed ebuild misses", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		cache := newReviewCache(t.TempDir())
		stored := reviewFixtureRequest()
		cache.put(stored, reviewFixtureNote())

		// Each variation is a different pair of files, so each is a different
		// question — and the old answer must be unreachable rather than merely
		// expired. There is no TTL here BECAUSE of this: the key is the content.
		ourEdit := reviewFixtureRequest()
		ourEdit.Ours = append(append([]byte{}, reviewOurs...), []byte("# our new line\n")...)

		theirEdit := reviewFixtureRequest()
		theirEdit.Theirs = append(append([]byte{}, reviewThem...), []byte("# upstream revised in place\n")...)

		swapped := reviewFixtureRequest()
		swapped.Ours, swapped.Theirs = reviewThem, reviewOurs

		for _, c := range []struct {
			name string
			req  ReviewRequest
		}{
			{"our ebuild changed", ourEdit},
			{"::gentoo's ebuild changed", theirEdit},
			{"the two sides swapped", swapped},
		} {
			if _, ok := cache.get(c.req); ok {
				t.Errorf("%s: the cache hit anyway, so a stale note would be printed as this run's finding", c.name)
			}
		}

		// The original pair still hits: a miss on a neighbour must not evict it.
		if _, ok := cache.get(stored); !ok {
			t.Error("the unchanged pair stopped hitting")
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("ordinary misses warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("the entry is keyed on content and not on the package", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		cache := newReviewCache(t.TempDir())

		cache.put(reviewFixtureRequest(), reviewFixtureNote())

		// A different atom, at a different version, with the SAME two files. R5.7
		// keys on the content hashes and on nothing else, so this is a hit: the
		// classification is a reading of the two files, and the files are the same.
		elsewhere := ReviewRequest{
			Category: "kde-plasma",
			Package:  "spectacle",
			Version:  "1.0",
			Ours:     reviewOurs,
			Theirs:   reviewThem,
		}
		if _, ok := cache.get(elsewhere); !ok {
			t.Error("a different package with identical files missed; the atom has leaked into the index, and R5.7 keys on content alone")
		}

		// The same atom with different files is a different question.
		sameAtom := reviewFixtureRequest()
		sameAtom.Ours = []byte("EAPI=8\n# something else entirely\n")
		if _, ok := cache.get(sameAtom); ok {
			t.Error("the same package with different files hit; the index is following the name rather than the content")
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("this subtest warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("a stored note round-trips exactly", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()

		// Two notes that between them cover what a model actually returns: prose
		// carrying a format verb and a newline, and the empty Declaration every
		// non-overlay origin has. A note mangled on the way to disk would print
		// differently on the second run than on the first, which is precisely what
		// R5.7 promises cannot happen.
		notes := map[string]struct {
			req  ReviewRequest
			note ReviewNote
		}{
			"an upstream note carries no declaration": {
				req: ReviewRequest{Ours: []byte("a"), Theirs: []byte("b")},
				note: ReviewNote{
					Origin:      OriginUpstream,
					Summary:     "::gentoo revised the ebuild in place: 100%s of the change is theirs\nsecond line",
					Declaration: "",
				},
			},
			"an overlay note carries one": {
				req:  reviewFixtureRequest(),
				note: reviewFixtureNote(),
			},
			"an unclassified note stays unclassified": {
				req:  ReviewRequest{Ours: []byte("c"), Theirs: []byte("d")},
				note: ReviewNote{Origin: OriginUnknown},
			},
			"a both-sides note": {
				req:  ReviewRequest{Ours: []byte("e"), Theirs: []byte("f")},
				note: ReviewNote{Origin: OriginBoth, Summary: "each side moved"},
			},
		}

		writer := newReviewCache(dir)
		for _, c := range notes {
			writer.put(c.req, c.note)
		}

		reader := newReviewCache(dir)
		for name, c := range notes {
			got, ok := reader.get(c.req)
			if !ok {
				t.Errorf("%s: missed after a reload", name)
				continue
			}
			if got != c.note {
				t.Errorf("%s: read back %+v, want %+v", name, got, c.note)
			}
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("round-tripping four notes warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("a corrupt cache reads as a miss, and the next store heals it", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()
		path := filepath.Join(dir, reviewCacheFileName)
		if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
			t.Fatalf("plant a corrupt cache: %v", err)
		}

		cache := newReviewCache(dir)
		req := reviewFixtureRequest()
		if _, ok := cache.get(req); ok {
			t.Fatal("a corrupt cache reported a hit")
		}
		// The whole contract: a miss, one warning, and no error anywhere — get
		// returns none, and there is no path by which the run could fail.
		if lines := warnings(); len(lines) != 1 {
			t.Errorf("a corrupt cache warned %d times, want exactly 1: %v", len(lines), lines)
		}

		cache.put(req, reviewFixtureNote())
		got, ok := newReviewCache(dir).get(req)
		if !ok {
			t.Fatal("the run after a corrupt cache still missed; the corruption is permanent")
		}
		if got != reviewFixtureNote() {
			t.Errorf("the healed cache returned %+v, want %+v", got, reviewFixtureNote())
		}
	})

	t.Run("an entry with an unrecognised origin reads as a miss", func(t *testing.T) {
		// A stored origin is a WORD, not the integer behind it, precisely because
		// nothing here expires: an entry written today is still read after the
		// constants have been reordered. A word this build does not know is
		// therefore treated as corruption — one warning, recompute — rather than
		// degraded to OriginUnknown, which would keep a note whose classification
		// has been erased and print no proposed declaration for it, forever.
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()
		req := reviewFixtureRequest()
		planted := fmt.Sprintf(`{"notes":{%q:{"origin":"sideways","summary":"s","declaration":""}}}`,
			contentFingerprint(req.Ours, req.Theirs))
		if err := os.WriteFile(filepath.Join(dir, reviewCacheFileName), []byte(planted), 0o600); err != nil {
			t.Fatalf("plant the entry: %v", err)
		}

		if _, ok := newReviewCache(dir).get(req); ok {
			t.Error("an entry whose origin this build cannot read was served anyway")
		}
		if lines := warnings(); len(lines) != 1 {
			t.Errorf("an unreadable entry warned %d times, want exactly 1: %v", len(lines), lines)
		}
	})

	t.Run("a corrupt cache warns once for the run, not once per package", func(t *testing.T) {
		// Eight divergences is the live overlay's real number. Eight identical
		// warnings would bury the report the operator actually asked for.
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, reviewCacheFileName), []byte("[]"), 0o600); err != nil {
			t.Fatalf("plant a corrupt cache: %v", err)
		}

		cache := newReviewCache(dir)
		for i := range 8 {
			req := reviewFixtureRequest()
			req.Ours = fmt.Appendf(nil, "EAPI=8\n# package %d\n", i)
			if _, ok := cache.get(req); ok {
				t.Fatalf("lookup %d hit a corrupt cache", i)
			}
		}

		if lines := warnings(); len(lines) != 1 {
			t.Errorf("eight lookups against a corrupt cache produced %d warnings, want exactly 1: %v", len(lines), lines)
		}
	})

	t.Run("a directory that cannot be created does not fail the lookup", func(t *testing.T) {
		// The parent is a regular FILE, so creating the directory fails with
		// ENOTDIR for every user — including root, which `act` runs tests as and
		// which ignores the permission bits the sibling subtest relies on.
		warnings := captureReviewWarnings(t)
		blocker := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("plant the blocking file: %v", err)
		}

		cache := newReviewCache(filepath.Join(blocker, "reviews"))
		req := reviewFixtureRequest()
		if _, ok := cache.get(req); ok {
			t.Fatal("a cache that cannot exist reported a hit")
		}
		beforeStores := len(warnings())

		// Eight stores, as eight findings would produce.
		for i := range 8 {
			stored := reviewFixtureRequest()
			stored.Ours = fmt.Appendf(nil, "EAPI=8\n# package %d\n", i)
			cache.put(stored, reviewFixtureNote())
		}

		if got := len(warnings()) - beforeStores; got != 1 {
			t.Errorf("eight stores into an uncreatable directory produced %d warnings, want exactly 1: %v", got, warnings())
		}
		// And the run carries on: the lookup still answers, in memory, for the
		// notes this run computed.
		if _, ok := cache.get(req); ok {
			t.Error("a note that was never stored came back")
		}
	})

	t.Run("an unwritable directory does not fail the lookup", func(t *testing.T) {
		warnings := captureReviewWarnings(t)
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("make the directory unwritable: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		// Root ignores the mode bits, so prove the directory really is unwritable
		// before claiming to have tested an unwritable one. `act` runs as root and
		// would otherwise report a green for a case it never exercised.
		probe := filepath.Join(dir, ".writable-probe")
		if err := os.WriteFile(probe, []byte("x"), 0o600); err == nil {
			_ = os.Remove(probe)
			t.Skip("the directory is writable despite mode 0500 (running as root); the ENOTDIR subtest covers this path for every user")
		}

		cache := newReviewCache(dir)
		req := reviewFixtureRequest()
		if _, ok := cache.get(req); ok {
			t.Fatal("an empty unwritable cache reported a hit")
		}
		beforeStores := len(warnings())

		cache.put(req, reviewFixtureNote())
		cache.put(req, reviewFixtureNote())

		if got := len(warnings()) - beforeStores; got != 1 {
			t.Errorf("two stores into an unwritable directory produced %d warnings, want exactly 1: %v", got, warnings())
		}
	})

	t.Run("a cache with nowhere to live is silent", func(t *testing.T) {
		// An empty directory is the shape a run gets when the home directory
		// cannot be resolved. The caller that could not name a directory is the one
		// holding the reason, so the cache itself says nothing — and still answers
		// every call.
		warnings := captureReviewWarnings(t)
		cache := newReviewCache("")
		req := reviewFixtureRequest()

		if _, ok := cache.get(req); ok {
			t.Fatal("a cache with no directory reported a hit")
		}
		cache.put(req, reviewFixtureNote())
		if _, ok := cache.get(req); !ok {
			t.Error("a cache with no directory forgot a note within the same run; a second finding on the same files would be reviewed twice")
		}
		if lines := warnings(); len(lines) != 0 {
			t.Errorf("a deliberately disabled cache warned %d times: %v", len(lines), lines)
		}
	})

	t.Run("the default directory sits under the user cache root", func(t *testing.T) {
		dir, err := defaultReviewCacheDir()
		if err != nil {
			t.Skipf("no home directory in this environment: %v", err)
		}
		if !strings.Contains(filepath.ToSlash(dir), ".cache/bentoo") {
			t.Errorf("defaultReviewCacheDir = %q, want it under the ~/.cache/bentoo root the compare providers already use", dir)
		}
	})
}
