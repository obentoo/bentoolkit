package overlay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// reviewCacheFileName is the file the notes live in, inside the directory
// newReviewCache is given. The layout — one JSON object holding every entry,
// replaced atomically — follows autoupdate/analysis_cache.go, which is this
// repository's existing shape for a cache of model output.
const reviewCacheFileName = "divergence_reviews.json"

// warnLogf emits the non-fatal warnings this package is allowed to print for
// itself. It defaults to logger.Warn and is a package var — the same seam
// internal/autoupdate and internal/snapshot already use — so tests can capture
// the warn-but-continue paths without reading stderr.
//
// It is a deliberate EXCEPTION to "a library returns errors, it does not log",
// and the exception is narrow. Everywhere else, internal/overlay hands its
// diagnostics back to the caller: Matcher collects them into
// MatchResult.Warnings, and the compare findings are rendered into the report.
// That works because those diagnostics are ABOUT the caller's subject. A cache
// failure is not: design.md's error table answers every one of them with "carry
// on without the cache", so by the time the run continues there is no error left
// to return and no result it belongs to. The alternative — threading a warning
// list out of a lookup that is otherwise a plain (note, ok) — would put a cache's
// bookkeeping in the signature of every caller for a line printed at most once.
var warnLogf = logger.Warn

// reviewCache stores one model classification per pair of compared ebuilds,
// indexed by their content (R5.7), so a repeated run prints the same commentary
// and issues no second request.
//
// THERE IS NO EXPIRY, and that is a decision rather than an omission.
// autoupdate.AnalysisCache expires after 24h because it caches a reading of a
// remote PAGE, which changes under the same URL. This caches a reading of two
// FILES and is keyed on their content: when either changes the index changes,
// the old entry becomes unreachable, and the new pair is a genuinely new
// question. An expiry could therefore only ever discard a still-correct answer
// and pay for it with a second request.
//
// NOTHING HERE FAILS A RUN. Every error is absorbed into a miss or a skipped
// write, which is why no method returns one: a cache is an optimisation, and a
// run that refused to proceed without one would have made it a dependency
// (design.md's error table).
//
// One cache per run. "Warn once" below is scoped to this value, so the count the
// operator sees is one line per failure per run — not one per package, which on
// the live overlay's eight divergences would be eight identical lines.
type reviewCache struct {
	// path is the file the notes are read from and written to. An EMPTY path
	// disables persistence: lookups still answer from memory, nothing is stored,
	// and nothing is warned. That is the shape a run gets when the home directory
	// cannot be resolved, and the caller that could not name a directory is the
	// one holding the reason to state.
	path string

	// mu guards notes and serialises the file replacement in save. The annotate
	// pass runs after CompareWithProvider returns, outside its goroutines, so
	// there is no contention to speak of — the lock is here so that a later
	// caller cannot make this the one unsynchronised map in a package whose
	// tests run under -race.
	mu sync.Mutex
	// notes is the whole cache in memory, indexed by contentFingerprint.
	notes map[string]ReviewNote

	// storeWarnOnce makes the store-side warning literally once per run. It is
	// the side that needs a guard: put is called per finding, so a directory that
	// cannot be written fails once per package. The READ side needs none — the
	// file is read exactly once, in newReviewCache, so a corrupt cache has one
	// place to warn from by construction.
	storeWarnOnce sync.Once
}

// reviewCacheFile is the on-disk shape.
//
// The map is wrapped in an object rather than being the document itself so the
// file can grow a field later without every existing entry becoming
// unreadable — the same reason analysisCacheFile exists, and it matters more
// here because nothing expires: a format change has to be survivable by files
// that will never be retired.
type reviewCacheFile struct {
	Notes map[string]ReviewNote `json:"notes"`
}

// newReviewCache opens the note cache stored in dir, reading whatever a previous
// run left there.
//
// It returns no error, on purpose: there is no failure here a caller could act
// on. A missing file is an ordinary first run, an unreadable or corrupt one is a
// warning and an empty cache, and dir == "" is a cache that answers from memory
// and persists nothing.
func newReviewCache(dir string) *reviewCache {
	c := &reviewCache{notes: make(map[string]ReviewNote)}
	if dir == "" {
		return c
	}
	c.path = filepath.Join(dir, reviewCacheFileName)
	c.load()
	return c
}

// defaultReviewCacheDir is where the notes live when the caller has no reason to
// put them elsewhere: ~/.cache/bentoo/compare, beside the per-provider caches
// the compare path already writes there (provider/github.go, provider/gitlab.go).
//
// It is spelled from os.UserHomeDir, like every other cache root in this
// repository, rather than from os.UserCacheDir. The two disagree whenever
// XDG_CACHE_HOME is set, and a review cache that landed somewhere else would
// split bentoo's cache root in exactly the setups that care where it goes.
//
// The error is returned rather than swallowed so the caller can say WHY the run
// is uncached; newReviewCache("") is the uncached shape it then asks for.
func defaultReviewCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the home directory for the review cache: %w", err)
	}
	return filepath.Join(home, ".cache", "bentoo", "compare"), nil
}

// get returns the note stored for the two ebuilds in req, if there is one.
//
// The lookup reads req.Ours and req.Theirs and nothing else: R5.7 keys a
// classification on the content of the two compared files, so the same pair of
// files under a different atom is the same question and hits. In practice two
// packages never carry identical ebuilds; when they do, the reading of their
// difference is identical too.
func (c *reviewCache) get(req ReviewRequest) (ReviewNote, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	note, ok := c.notes[contentFingerprint(req.Ours, req.Theirs)]
	return note, ok
}

// put records a note for the two ebuilds in req and persists the cache.
//
// A failure to persist is a warning and nothing more — the note stays in memory
// for the rest of the run, and the next run simply asks again.
func (c *reviewCache) put(req ReviewRequest, note ReviewNote) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Recorded in memory FIRST, and unconditionally, so a cache that cannot
	// persist still answers within the run it was built for.
	c.notes[contentFingerprint(req.Ours, req.Theirs)] = note
	if c.path == "" {
		return
	}

	if err := c.save(); err != nil {
		c.storeWarnOnce.Do(func() {
			warnLogf("overlay: review notes cannot be cached in %s (%v); this run is unaffected, but the next one will ask again",
				filepath.Dir(c.path), err)
		})
	}
}

// contentFingerprint is the index R5.7 specifies: sha256(ours) + ":" +
// sha256(theirs), hex-encoded because it is used as a JSON object member name.
//
// The ORDER is part of the identity. ReviewNote.Origin names a SIDE, so the same
// two files read the other way round is a different question with a different
// answer, and the two must not share an entry. The separator is there for the
// reader rather than for correctness — both halves are fixed-width — so that a
// human looking at the file can see two hashes instead of one long one.
//
// The atom is deliberately absent. What the model is asked to explain is the
// difference between two files, and that is entirely determined by the files.
func contentFingerprint(ours, theirs []byte) string {
	ourSum := sha256.Sum256(ours)
	theirSum := sha256.Sum256(theirs)
	return hex.EncodeToString(ourSum[:]) + ":" + hex.EncodeToString(theirSum[:])
}

// load reads the cache file into memory. It runs ONCE per cache, from
// newReviewCache, which is what makes the warnings below once-per-run without a
// guard around them.
//
// Every outcome is the same for the run — an empty map, so every lookup misses —
// and the two failures are told apart only by whether they are worth a word. A
// file that is not there is the ordinary first run and says nothing. A file that
// is there and will not read, or will not parse, is a real (if harmless) failure
// and gets the one warning design.md's error table allows.
func (c *reviewCache) load() {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			warnLogf("overlay: review cache %s could not be read (%v); every note will be recomputed this run", c.path, err)
		}
		return
	}

	var stored reviewCacheFile
	if err := json.Unmarshal(data, &stored); err != nil {
		// The next successful store replaces the file wholesale, so a cache
		// corrupted by an interrupted write or an older format heals itself rather
		// than needing to be deleted by hand. Nothing recoverable is lost: what
		// could not be parsed could not have been used.
		warnLogf("overlay: review cache %s could not be parsed (%v); every note will be recomputed this run and the file replaced", c.path, err)
		return
	}
	if stored.Notes != nil {
		c.notes = stored.Notes
	}
}

// save writes the whole cache out and replaces the file atomically. The caller
// holds c.mu.
//
// Two concurrent compare runs can each write a complete file and the later
// rename wins, so one of them loses the entries the other added. That is a lost
// optimisation and never a corruption — the reader either sees the previous file
// or this one, never a half-written one — and the price of the alternative
// (locking a file for the length of a run) is paid by every run to protect a
// case that costs one recomputation.
func (c *reviewCache) save() error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create the review cache directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(reviewCacheFile{Notes: c.notes}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the review cache: %w", err)
	}

	// os.CreateTemp rather than a fixed "<path>.tmp": it creates the file with
	// 0600 — fileutil.CacheFileMode, which the rename below carries to the real
	// file — and gives each writer its own name, so two runs writing at once
	// cannot interleave inside one temp file.
	tmp, err := os.CreateTemp(dir, reviewCacheFileName+".*")
	if err != nil {
		return fmt.Errorf("create a review cache temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write the review cache temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close the review cache temp file %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace the review cache %s: %w", c.path, err)
	}

	return nil
}
