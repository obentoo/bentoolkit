package autoupdate

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repairReadCall matches a call to any function that reads a repair bundle.
var repairReadCall = regexp.MustCompile(`\brepair\.(ReadBundle|FindBundle|ListBundles)\(`)

// repairWriteCall matches the one call that writes a repair bundle.
var repairWriteCall = regexp.MustCompile(`\brepair\.WriteBundle\(`)

// TestRepairBundleReadOnlyFromRepairMode is R6.2: no code on the --apply or
// --check path reads a repair bundle. Every non-test Go file of the module is
// swept, and a read is allowed only in the --repair mode's own file. The
// non-vacuity half: the bundle IS written somewhere in internal/autoupdate, so
// a sweep that found no file at all cannot pass.
func TestRepairBundleReadOnlyFromRepairMode(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const allowedReader = "cmd/bentoo/overlay_autoupdate_repair.go"
	var readers, writers []string
	swept := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			// Agent worktrees under .claude are whole copies of the repo.
			switch d.Name() {
			case ".claude", ".epic", ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			if rel == "internal/autoupdate/repair" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		swept++
		if repairReadCall.Match(body) && rel != allowedReader {
			readers = append(readers, rel)
		}
		if repairWriteCall.Match(body) && strings.HasPrefix(rel, "internal/autoupdate/") {
			writers = append(writers, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("sweeping %s: %v", root, err)
	}
	if swept < 50 {
		t.Fatalf("swept only %d Go files under %s; the sweep is not looking at the module", swept, root)
	}
	if len(writers) == 0 {
		t.Fatalf("no repair.WriteBundle call found in internal/autoupdate; the sweep is vacuous")
	}
	if len(readers) > 0 {
		t.Errorf("a repair bundle is read outside %s (R6.2): %v", allowedReader, readers)
	}
}
