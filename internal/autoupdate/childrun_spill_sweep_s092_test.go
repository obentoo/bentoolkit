package autoupdate

// Authored for story 092 (autoupdate-apply-observable), sub-task 7.1, test
// first (R3.5): when --apply starts, every child-run spill file directly in the
// logs directory whose last modification is older than one hour is removed. A
// younger spill may belong to a live run and is kept; nothing else in the logs
// directory is touched; a removal that fails is a WARN, never a failed run.
//
// Assumed surface: (*Applier).sweepChildRunSpills(), no arguments, no return
// value, logging through the applier's logger. The tests call it directly; the
// call from the start of an --apply run is the implementer's wiring.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// s092v2SpillRecords collects every record a logger emits, attrs from With
// included, so "a WARN names the file" is checkable.
type s092v2SpillRecords struct {
	mu   sync.Mutex
	recs []s092v2SpillRecord
}

type s092v2SpillRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

type s092v2SpillHandler struct {
	store *s092v2SpillRecords
	pre   []slog.Attr
}

func (h *s092v2SpillHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *s092v2SpillHandler) Handle(_ context.Context, r slog.Record) error {
	rec := s092v2SpillRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	for _, a := range h.pre {
		rec.attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.store.mu.Lock()
	h.store.recs = append(h.store.recs, rec)
	h.store.mu.Unlock()
	return nil
}

func (h *s092v2SpillHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &s092v2SpillHandler{store: h.store, pre: append(append([]slog.Attr{}, h.pre...), as...)}
}

func (h *s092v2SpillHandler) WithGroup(string) slog.Handler { return h }

func (s *s092v2SpillRecords) warns() []s092v2SpillRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []s092v2SpillRecord
	for _, r := range s.recs {
		if r.level == slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

// s092v2SpillApplier builds an applier whose logs directory is a fresh temp
// directory, with a temp distdir and an isolated XDG_CONFIG_HOME.
func s092v2SpillApplier(t *testing.T) (*Applier, *s092v2SpillRecords) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tmp := t.TempDir()
	store := &s092v2SpillRecords{}
	a, err := NewApplier(filepath.Join(tmp, "overlay"), filepath.Join(tmp, "config"),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierLogger(slog.New(&s092v2SpillHandler{store: store})),
		WithLogsDir(filepath.Join(tmp, "logs")),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if err := os.MkdirAll(a.LogsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	return a, store
}

// s092v2RealSpill creates a spill file the way a child run does, so its name
// has exactly the shape a run leaves behind, and backdates it by age.
func s092v2RealSpill(t *testing.T, logsDir string, age time.Duration) string {
	t.Helper()
	sp := newSweeper(t.TempDir(), withSweeperChildLogsDir(logsDir)).openChildRunSpill()
	if sp == nil || sp.err != nil || sp.file == nil {
		t.Fatalf("fixture: openChildRunSpill gave no file (%+v)", sp)
	}
	if _, err := sp.Write([]byte("unmasked child output\n")); err != nil {
		t.Fatal(err)
	}
	name := sp.file.Name()
	// A hard-killed run never discards its spill: the file is simply left.
	if err := sp.file.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(name) != logsDir || !strings.HasPrefix(filepath.Base(name), ".childrun-spill-") {
		t.Fatalf("fixture: spill %q is not a .childrun-spill-* file directly in %s", name, logsDir)
	}
	s092v2Backdate(t, name, age)
	return name
}

func s092v2Backdate(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func s092v2PlainFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s092v2Backdate(t, path, age)
}

func s092v2Exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return false
}

// Hostile halves first. A file that only LOOKS like a spill, or a real spill
// that is still young or sits below the logs directory, must survive the
// sweep; then two real spills from different runs, both older than an hour,
// must both go. A clean sweep logs no WARN.
func TestChildRunSpillSweepRemovesOnlyOldSpills(t *testing.T) {
	a, store := s092v2SpillApplier(t)
	logs := a.LogsDir()
	const old = 2 * time.Hour

	// Must stay: near-identical names that are not spill files, all old.
	lookalikes := []string{
		filepath.Join(logs, "childrun-spill-123.log"),      // no leading dot
		filepath.Join(logs, ".childrun-spill-123.log.bak"), // not a .log
		filepath.Join(logs, ".childrun-spill-123.txt"),     // wrong extension
		filepath.Join(logs, ".childrun-other-123.log"),     // other prefix
		filepath.Join(logs, "notes.log"),
	}
	for _, p := range lookalikes {
		s092v2PlainFile(t, p, old)
	}
	// Must stay: the per-package log tree, even an old spill-shaped file in it.
	pkgTree := []string{
		filepath.Join(logs, "dev-games", "godot", "4.7", "compile.log"),
		filepath.Join(logs, "dev-games", "godot", "4.7", ".childrun-spill-999.log"),
		filepath.Join(logs, "dev-games", "godot", "4.7", "manifest-fix-agent.jsonl"),
	}
	for _, p := range pkgTree {
		s092v2PlainFile(t, p, old)
	}
	// Must stay: real spills younger than an hour, which a live run may hold.
	fresh := s092v2RealSpill(t, logs, 0)
	recent := s092v2RealSpill(t, logs, 30*time.Minute)
	// Must go: real spills of two dead runs, older than an hour.
	stale1 := s092v2RealSpill(t, logs, old)
	stale2 := s092v2RealSpill(t, logs, 26*time.Hour)

	a.sweepChildRunSpills()

	for _, p := range append(append([]string{}, lookalikes...), pkgTree...) {
		if !s092v2Exists(t, p) {
			t.Errorf("the sweep removed %s, which is not a spill file directly in the logs directory", p)
		}
	}
	for _, p := range []string{fresh, recent} {
		if !s092v2Exists(t, p) {
			t.Errorf("the sweep removed %s, a spill younger than one hour (a live run's)", p)
		}
	}
	for _, p := range []string{stale1, stale2} {
		if s092v2Exists(t, p) {
			t.Errorf("the sweep left %s, a spill older than one hour", p)
		}
	}
	if fi, err := os.Stat(filepath.Join(logs, "dev-games", "godot", "4.7")); err != nil || !fi.IsDir() {
		t.Errorf("the per-package log directory is gone or not a directory: %v", err)
	}
	if w := store.warns(); len(w) != 0 {
		t.Errorf("a sweep where every removal succeeds logged %d WARN record(s): %+v", len(w), w)
	}
}

// A spill that cannot be removed is a WARN naming it; the sweep goes on to the
// next spill and returns instead of failing the run.
func TestChildRunSpillSweepRemovalErrorWarnsAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the removal cannot be made to fail")
	}
	a, store := s092v2SpillApplier(t)
	logs := a.LogsDir()
	stale1 := s092v2RealSpill(t, logs, 2*time.Hour)
	stale2 := s092v2RealSpill(t, logs, 3*time.Hour)

	if err := os.Chmod(logs, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(logs, 0o750); err != nil {
			t.Logf("restoring %s: %v", logs, err)
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.sweepChildRunSpills()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not return after a removal failed")
	}

	if err := os.Chmod(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stale1, stale2} {
		if !s092v2Exists(t, p) {
			t.Fatalf("fixture: %s was removed from a read-only directory", p)
		}
	}
	warns := store.warns()
	if len(warns) == 0 {
		t.Fatal("a failed spill removal logged no WARN record")
	}
	for _, p := range []string{stale1, stale2} {
		base := filepath.Base(p)
		named := false
		for _, w := range warns {
			if strings.Contains(w.msg, base) {
				named = true
			}
			for _, v := range w.attrs {
				if strings.Contains(v, base) {
					named = true
				}
			}
		}
		if !named {
			t.Errorf("no WARN record names %s; the sweep stopped at the first failure or did not say which file: %+v", base, warns)
		}
	}
}
