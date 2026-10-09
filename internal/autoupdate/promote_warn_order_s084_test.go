package autoupdate

import "testing"

// TestS084DroppedRecordsWarnOnlyAfterTheManifestIsPublished pins where the
// dropped-records WARN is logged: after the merged Manifest was written, which
// the merge INFO marks. Logged before the write, a failed write rolled back by
// undo would leave a WARN about records dropped from a Manifest that was
// restored with them.
func TestS084DroppedRecordsWarnOnlyAfterTheManifestIsPublished(t *testing.T) {
	p := s084MustPromote(t, s084Case{
		versions: []string{"1.28.6"},
		published: s084Str(s084Lines(
			"DIST ../escape.tar.xz 1 BLAKE2B x SHA512 x",
			"DIST gst-plugins-qt6-1.28.6.tar.xz 100 BLAKE2B aa SHA512 bb",
		)),
		staged:    s084Str("DIST gst-plugins-qt6-1.29.2.tar.xz 200 BLAKE2B cc SHA512 dd\n"),
		candidate: "1.29.2",
	})

	infoAt, warnAt := -1, -1
	for i, r := range p.rec.records(t) {
		switch {
		case r["level"] == "INFO" && r["kept"] != nil && r["written"] != nil && infoAt < 0:
			infoAt = i
		case r["level"] == "WARN" && r["manifest"] == p.manifest && warnAt < 0:
			warnAt = i
		}
	}
	if infoAt < 0 || warnAt < 0 {
		t.Fatalf("want both the merge INFO and the dropped-records WARN, got INFO at %d and WARN at %d; records: %v",
			infoAt, warnAt, p.rec.records(t))
	}
	if warnAt < infoAt {
		t.Errorf("the dropped-records WARN (record %d) was logged before the merge INFO (record %d): it must follow the write, "+
			"or a rolled-back write leaves a WARN about records the restored Manifest still holds", warnAt, infoAt)
	}
}
