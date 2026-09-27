package overlay

// Authored for story 057, sub-task 1.4 — regression guard (GREEN today).
// A StatusError result with NO recorded cause keeps today's sentence
// (meta.yaml: "old sentence when no cause recorded").

import "testing"

func TestComparedDetailWithoutCauseIsUnchanged(t *testing.T) {
	const want = "the comparison failed, so nothing is known about how the two versions relate"
	if got := comparedDetail(CompareResult{Category: "cat", Package: "pkg", LocalVersion: "1.0", Status: StatusError}); got != want {
		t.Errorf("a StatusError with no cause reads %q, want the unchanged %q", got, want)
	}
}
