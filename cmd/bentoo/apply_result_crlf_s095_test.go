package main

// Story 095: a child line ending in CRLF keeps its text in the Apply Result tail.

import (
	"slices"
	"testing"
)

func TestApplyResultCRLFTail(t *testing.T) {
	lines, omitted := childRunTail("fetching\r\nerror: x\r\n10%\r20%\rdone\r\n\r\n")
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	want := []string{"fetching", "error: x", "done"}
	if !slices.Equal(lines, want) {
		t.Errorf("childRunTail lines = %q, want %q", lines, want)
	}
}
