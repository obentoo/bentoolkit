package snapshot

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTailBuffer_KeepsLastBytes pins the R5.4 cap at the buffer: whatever is
// written, only the last limit bytes are kept, in order.
func TestTailBuffer_KeepsLastBytes(t *testing.T) {
	b := &tailBuffer{limit: 8}
	for _, w := range []string{"abc", "defg", "hij", "0123456789ABCDEF", "xy"} {
		if n, err := b.Write([]byte(w)); err != nil || n != len(w) {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", w, n, err, len(w))
		}
	}
	if got, want := string(b.buf), "ABCDEFxy"; got != want {
		t.Errorf("buf = %q, want %q", got, want)
	}
}

// TestExecRunner_PipeCapsStderr pins R5.4 end to end: a stage that floods
// stderr before failing yields an error carrying at most 64 KiB of it, and
// the tail rather than the head.
func TestExecRunner_PipeCapsStderr(t *testing.T) {
	s053NeedTools(t, "sh", "head", "tr", "cat")
	stages := []pipeStage{
		{name: "sh", args: []string{"-c", `head -c 300000 /dev/zero | tr '\0' a >&2; printf END-OF-STDERR >&2; exit 1`}},
		{name: "cat"},
	}
	_, err := runPipe(t.Context(), execRunner{}, stages)
	if err == nil {
		t.Fatal("runPipe = nil, want the failing stage's error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "END-OF-STDERR") {
		t.Errorf("error does not carry the tail of the stage's stderr")
	}
	if n := strings.Count(msg, "a"); n > 64<<10 {
		t.Errorf("error carries %d bytes of stderr, want at most %d", n, 64<<10)
	}
}

// TestSendMailBounded_CRLFErrorNamesAddress pins Q4 on the CR/LF refusal: the
// error names, quoted, the address that carried the line break.
func TestSendMailBounded_CRLFErrorNamesAddress(t *testing.T) {
	for _, tc := range []struct{ from, to, bad string }{
		{"a@x\r\nRCPT TO:<evil>", "b@x", "a@x\r\nRCPT TO:<evil>"},
		{"a@x", "b@x\nDATA", "b@x\nDATA"},
	} {
		err := sendMailBounded(t.Context(), "127.0.0.1:1", nil, tc.from, []string{tc.to}, []byte("x"))
		if err == nil || !strings.Contains(err.Error(), strconv.Quote(tc.bad)) {
			t.Errorf("err = %v, want it to name %s", err, strconv.Quote(tc.bad))
		}
	}
}

// TestTransientMounter_UmountBoundedByTimeout pins R4.1's bound: the unmount
// runs under a deadline no later than umountTimeout.
func TestTransientMounter_UmountBoundedByTimeout(t *testing.T) {
	s053Tmp(t)
	orig := umountTimeout
	umountTimeout = 2 * time.Second
	t.Cleanup(func() { umountTimeout = orig })
	s := &s053MountScript{}
	m := &transientMounter{run: s.runner()}
	_, cleanup, err := m.Mount(t.Context(), s053Snap)
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if !s.umountHasDeadline || s.umountDeadline <= 0 || s.umountDeadline > umountTimeout {
		t.Errorf("umount deadline = (%v, set %v), want within %v", s.umountDeadline, s.umountHasDeadline, umountTimeout)
	}
}

// TestArchiveShipper_DeleteBoundedByTimeout pins the R5.5 deletion's bound: it
// outlives Send's context but runs under a deadline no later than
// archiveDeleteTimeout.
func TestArchiveShipper_DeleteBoundedByTimeout(t *testing.T) {
	orig := archiveDeleteTimeout
	archiveDeleteTimeout = 2 * time.Second
	t.Cleanup(func() { archiveDeleteTimeout = orig })
	var left time.Duration
	var hasDeadline bool
	mr := &MockRunner{RunFunc: func(ctx context.Context, name string, args []string, _ []byte) ([]byte, error) {
		switch {
		case name == "btrfs":
			return nil, errors.New("send failed")
		case name == "rclone" && len(args) > 0 && args[0] == "deletefile":
			var dl time.Time
			dl, hasDeadline = ctx.Deadline()
			left = time.Until(dl)
		}
		return nil, nil
	}}
	a := &archiveShipper{name: "offsite", remote: "r:bkt", mode: "full", compress: "zstd", run: mr, parents: &fakeParentStore{}}
	if _, err := a.Send(t.Context(), Snapshot{ID: "42", Subvolume: "/home", Path: "/p"}); err == nil {
		t.Fatal("Send = nil, want the pipe error")
	}
	if !hasDeadline || left <= 0 || left > archiveDeleteTimeout {
		t.Errorf("deletefile deadline = (%v, set %v), want within %v", left, hasDeadline, archiveDeleteTimeout)
	}
}
