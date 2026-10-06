package main

// bentoo's own version text is left exactly as it was when bentoo-tray gained
// a version of its own: same first line, same field lines, nothing about the
// tray or a separate bentoolkit line.

import (
	"io"
	"os"
	"runtime"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/version"
)

func TestVersionCommand_PrintsBentoosOutputUnchanged(t *testing.T) {
	oldV, oldC, oldB := version.Version, version.Commit, version.BuildDate
	t.Cleanup(func() { version.Version, version.Commit, version.BuildDate = oldV, oldC, oldB })
	version.Version, version.Commit, version.BuildDate = "0.33.0", "c0ffee1234", "2026-01-02T03:04:05Z"

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	cmd := newVersionCmd()
	runErr := cmd.RunE(cmd, nil)
	os.Stdout = oldStdout
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("version: %v", runErr)
	}
	want := "bentoo version 0.33.0\n" +
		"  commit: c0ffee1234\n" +
		"  built: 2026-01-02T03:04:05Z\n" +
		"  go: " + runtime.Version() + "\n" +
		"  os/arch: " + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if string(got) != want {
		t.Errorf("bentoo version printed\n%q\nwant\n%q", got, want)
	}
}
