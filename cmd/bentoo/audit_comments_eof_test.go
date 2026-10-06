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

// TestAuditCommentsFlagsIDsFollowedByUnderscore pins the italic requirement
// trailers planning tools leave behind ("_Requirements: R7, R7.3_"): the closing
// underscore is a word character, so an ID right before it has no word boundary
// after it and a pattern ending in \b alone never sees it.
func TestAuditCommentsFlagsIDsFollowedByUnderscore(t *testing.T) {
	acRequireMake(t)
	root := t.TempDir()
	acWriteTree(t, root, map[string]string{
		"p/a.go": acGoFile("package p", "", "// _Requirements: R7, R7.3_", "var A = 1"),
		"q/b.go": acGoFile("package q", "", "// _Story: S046_", "var B = 1"),
	})

	out, code := acAuditDirs(t, filepath.Join(root, "p"), filepath.Join(root, "q"))
	if code == 0 {
		t.Fatalf("IDs followed by an underscore passed:\n%s", out)
	}
	for _, want := range []string{"p/a.go:3: tracker ID", "q/b.go:3: tracker ID"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not report %q:\n%s", want, out)
		}
	}
}

// TestAuditCommentsReportsANestedDirectoryOnce pins that listing a directory and
// one of its subdirectories scans each file once: a finding printed twice reads
// as two defects and inflates every count taken from the output.
func TestAuditCommentsReportsANestedDirectoryOnce(t *testing.T) {
	acRequireMake(t)
	root := t.TempDir()
	acWriteTree(t, root, map[string]string{
		"top/sub/c.go": acGoFile("package sub", "", "// see story 060", "var C = 1"),
	})

	out, _ := acAuditDirs(t, filepath.Join(root, "top"), filepath.Join(root, "top", "sub"))
	if n := strings.Count(out, "sub/c.go:3: tracker ID"); n != 1 {
		t.Errorf("the finding is reported %d times, want 1:\n%s", n, out)
	}
}
