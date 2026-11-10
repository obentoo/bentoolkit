package autoupdate

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// A validated Manifest that changes the size or digest of a distfile the
// published Manifest already declares is refused, and the promotion leaves the
// package directory exactly as it was. Observed on dev-util/ai-memory
// (2026-11-10): a download killed by the manifest timeout left a truncated
// archive under its final name, a later `pkgdev manifest --force` digested it,
// and the merge silently replaced the correct published record.
func TestPromoteRefusesDistDigestChangeUnderSameName(t *testing.T) {
	published := s084Lines(
		"DIST a-1.tar.xz 1 BLAKE2B a SHA512 a",
		"DIST x-1.tar.xz 55897113 BLAKE2B good SHA512 good",
	)
	p := s084Promote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str(published),
		mode:      0o644,
		staged:    s084Str("DIST x-1.tar.xz 41772565 BLAKE2B trunc SHA512 trunc\n"),
		candidate: "1.29.2",
	})
	if !errors.Is(p.err, ErrDistDigestConflict) {
		t.Fatalf("promote err = %v, want ErrDistDigestConflict", p.err)
	}
	if !strings.Contains(p.err.Error(), "x-1.tar.xz") {
		t.Errorf("the error does not name the distfile: %v", p.err)
	}
	if p.undo != nil {
		t.Error("a refused promotion returned a rollback")
	}
	s084AssertManifest(t, p.manifest, published)
	if _, err := os.Stat(p.candidate); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the candidate %s was left published (stat err %v)", p.candidate, err)
	}
	var found bool
	for _, r := range p.rec.records(t) {
		if r["level"] == "ERROR" && r["distfile"] == "x-1.tar.xz" {
			found = true
		}
	}
	if !found {
		t.Errorf("no ERROR event names the conflicting distfile; records: %v", p.rec.records(t))
	}
}

// An identical record under the same name is the ordinary case (a revision
// bump, or a candidate sharing an archive) and still promotes.
func TestPromoteAcceptsIdenticalDistRecordUnderSameName(t *testing.T) {
	line := "DIST x-1.tar.xz 9 BLAKE2B same SHA512 same"
	p := s084MustPromote(t, s084Case{
		versions:  []string{"1.28.6"},
		published: s084Str(s084Lines(line)),
		staged:    s084Str(s084Lines(line)),
		candidate: "1.29.2",
	})
	s084AssertManifest(t, p.manifest, s084Lines(line))
}
