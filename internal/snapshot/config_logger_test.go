package snapshot

// Authored for story 062, sub-task 3.4 (R5.2, R5.5).
//
// Contract, from the sub-task objective: `(*Config).ValidateWith(*slog.Logger)
// error`, with `Validate()` kept as `ValidateWith(nil)`.
//
// The probe is Validate's non-fatal warning for an empty engine.subvolumes. The
// error ValidateWith may return AFTER that warning (driver detection on a host
// without btrbk) is not this test's subject and is not asserted.

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestValidateWithWarnsThroughTheInjectedLogger: the empty-subvolumes warning
// reaches the logger passed to ValidateWith, and not stderr.
func TestValidateWithWarnsThroughTheInjectedLogger(t *testing.T) {
	s062IsolateSnapshot(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk"}}

	stderr := s062SnapshotCaptureFD2(t, func() { _ = cfg.ValidateWith(log) })

	var warns int
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "subvolumes") {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("the injected logger got %d WARN records about the empty subvolume list, want 1\n%s", warns, buf.String())
	}
	if strings.TrimSpace(stderr) != "" {
		t.Errorf("with a logger injected, ValidateWith still wrote to stderr:\n%s", stderr)
	}
}

// TestValidateWithRefusesWhatValidateRefuses: adding the logger parameter
// changes no verdict — the same invalid config fails the same way through
// both entry points, with or without a logger.
func TestValidateWithRefusesWhatValidateRefuses(t *testing.T) {
	s062IsolateSnapshot(t)
	cfg := &Config{Engine: EngineConfig{Driver: "s062-no-such-driver", Subvolumes: []string{"/"}}}
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	plain := cfg.Validate()
	for name, err := range map[string]error{"ValidateWith(log)": cfg.ValidateWith(log), "ValidateWith(nil)": cfg.ValidateWith(nil)} {
		if !errors.Is(err, ErrInvalidDriver) {
			t.Errorf("%s = %v, want ErrInvalidDriver", name, err)
		}
		if plain == nil || err == nil || err.Error() != plain.Error() {
			t.Errorf("%s = %v, Validate() = %v: the two entry points disagree", name, err, plain)
		}
	}
}
