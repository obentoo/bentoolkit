package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditCommentsEOFBlockNamesItsOwnFile pins the file a long block is
// reported against when the block runs to the end of a file and another file
// follows it in the same scan: awk has already moved on to the next file by the
// time the block ends, so naming the current file there would blame the wrong one.
func TestAuditCommentsEOFBlockNamesItsOwnFile(t *testing.T) {
	acRequireMake(t)
	root := t.TempDir()
	lines := append([]string{"package p", ""}, acCommentBlock(20, "")...)
	acWriteTree(t, root, map[string]string{
		"p/a.go": acGoFile(lines...),
		"p/b.go": acGoFile("package p", "", "var B = 1"),
	})

	out, code := acAuditDirs(t, filepath.Join(root, "p"))
	if code == 0 {
		t.Fatalf("a 20-line block at the end of a.go passed:\n%s", out)
	}
	if want := filepath.Join(root, "p", "a.go") + ":3: comment block of 20 lines"; !strings.Contains(out, want) {
		t.Errorf("output does not report %q:\n%s", want, out)
	}
	if strings.Contains(out, "b.go:") {
		t.Errorf("the block is blamed on the file after it:\n%s", out)
	}
}
