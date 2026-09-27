package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Story 054, R5.7 extended to the two remaining argvs that carry a caller's
// value after the subcommand: Fetch's remote (it comes from the overlay config)
// and CountRange's range. Read as an option, a remote such as
// "--upload-pack=<cmd>" runs <cmd>, and a range such as "--output=<file>..HEAD"
// writes <file>.

// A remote whose name starts with "-" is a valid git remote and must fetch.
func TestFetchReadsTheRemoteAsAName(t *testing.T) {
	upstream := newRepo(t)
	local := newRepo(t)
	gitOut(t, local, "remote", "add", "--", "-evil", upstream)

	if err := NewGitRunner(local).Fetch(context.Background(), "-evil"); err != nil {
		t.Fatalf("Fetch of a remote named -evil: %v (the name was read as an option)", err)
	}
}

// Hostile half: a remote value that is an option must never run as one.
func TestFetchNeverRunsARemoteAsAnOption(t *testing.T) {
	local := newRepo(t)
	marker := filepath.Join(t.TempDir(), "PWNED")

	err := NewGitRunner(local).Fetch(context.Background(), "--upload-pack=touch "+marker+"; git-upload-pack")
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatalf("Fetch ran the remote value as --upload-pack: %s exists", marker)
	}
	if err == nil {
		t.Fatal("Fetch of an option-shaped remote succeeded, want an error")
	}
}

// A range value that is an option must never run as one; an ordinary range
// still counts.
func TestCountRangeNeverRunsARangeAsAnOption(t *testing.T) {
	dir := newRepo(t)
	marker := filepath.Join(t.TempDir(), "OUT")

	if _, err := NewGitRunner(dir).CountRange(context.Background(), "--output="+marker, "HEAD"); err == nil {
		t.Error("CountRange of an option-shaped range succeeded, want an error")
	}
	if matches, _ := filepath.Glob(marker + "*"); len(matches) > 0 {
		t.Fatalf("CountRange ran the range as --output: %v exists", matches)
	}

	n, err := NewGitRunner(dir).CountRange(context.Background(), "HEAD", "HEAD")
	if err != nil || n != 0 {
		t.Fatalf("CountRange(HEAD, HEAD) = %d, %v; want 0, nil", n, err)
	}
}
