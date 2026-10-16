package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s097SaveBoundLaughs builds a tray.monitors section whose aliases nest
// levels deep, each level repeating the one before ten times.
func s097SaveBoundLaughs(levels int) string {
	var b strings.Builder
	b.WriteString(s097SaveAliasBase + "tray:\n  monitors:\n    l0: &l0 x\n")
	for i := 1; i <= levels; i++ {
		refs := strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*l%d, ", i-1), 10), ", ")
		fmt.Fprintf(&b, "    l%d: &l%d [%s]\n", i, i, refs)
	}
	return b.String()
}

// TestS097SaveBound_NestedAliasesFailFastInsteadOfExpanding: a section of a
// few hundred bytes that would resolve into gigabytes is refused by SaveTo
// with errMonitorsTooLarge, quickly, and writes no file.
func TestS097SaveBound_NestedAliasesFailFastInsteadOfExpanding(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveBoundLaughs(9))
	path := filepath.Join(t.TempDir(), "saved.yaml")
	start := time.Now()
	err := cfg.SaveTo(path)
	if !errors.Is(err, errMonitorsTooLarge) {
		t.Fatalf("SaveTo of nine nested alias levels = %v, want errMonitorsTooLarge", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("SaveTo took %v to refuse; the bound must stop it early", elapsed)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a refused save left %s behind (stat: %v)", path, statErr)
	}
}

// TestS097SaveBound_ShallowAliasesStillSave is the converse: a section whose
// aliases resolve well under the bound still saves and loads back.
func TestS097SaveBound_ShallowAliasesStillSave(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveBoundLaughs(3))
	reloaded, _ := s097SaveAliasRoundTrip(t, cfg)
	if reloaded == nil {
		t.Fatal("a section of three alias levels did not save and load back")
	}
}

// TestS097SaveBound_AliasOfMergeKeyKeepsItsMeaning: a key written as an alias
// of a merge key is not a merge to the decoder, so the saved file must not
// turn it into one.
func TestS097SaveBound_AliasOfMergeKeyKeepsItsMeaning(t *testing.T) {
	cfg := s097SaveAliasLoad(t, s097SaveAliasBase+
		"tray:\n  notice:\n    &m <<: {x: 1}\n  monitors:\n    disk:\n      *m : {warn_percent: 7}\n")
	before := s097SaveAliasMonitors(t, "before save", cfg)
	after, saved := s097SaveAliasRoundTrip(t, cfg)
	s097SaveAliasSame(t, before, s097SaveAliasMonitors(t, "after save", after), saved)
}
