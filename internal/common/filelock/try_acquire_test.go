package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTryAcquire_RefusesAtOnceNamingTheHolder: a held lock is refused without
// waiting, and the error names who holds it and since when; once released it
// can be taken.
func TestTryAcquire_RefusesAtOnceNamingTheHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.lock")
	held, err := TryAcquire(path, "build session")
	if err != nil {
		t.Fatalf("TryAcquire on a free lock: %v", err)
	}

	start := time.Now()
	_, err = TryAcquire(path, "build session")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("TryAcquire waited %s for a live holder; it must refuse at once", elapsed)
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	for _, want := range []string{"pid " + strconv.Itoa(os.Getpid()), "running since "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}

	held.Release()
	again, err := TryAcquire(path, "build session")
	if err != nil {
		t.Fatalf("TryAcquire after the holder released: %v", err)
	}
	again.Release()
}
