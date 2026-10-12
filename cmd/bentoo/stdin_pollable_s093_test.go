package main

// Story 093: the stdinPollable seam and the live-region gates that consult it.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

func TestStdinPollableDefaultSeam(t *testing.T) {
	d := defaultDeps()
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()

	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	os.Stdin = f
	if d.stdinPollable() {
		t.Error("a regular file was read as a pollable stdin")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	os.Stdin = r
	if !d.stdinPollable() {
		t.Error("a pipe was read as a stdin that cannot be polled")
	}
}

func TestStdinPollableApplyGate(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	for _, tc := range []struct {
		pollable bool
		want     bool
	}{{true, true}, {false, false}} {
		d := defaultDeps()
		stubUIIsTerminal(d, true)
		d.stdinPollable = func() bool { return tc.pollable }
		ar := &autoupdateRun{opts: testAutoupdateOptions(), deps: d, uiConfig: &config.Config{}}
		if got := ar.tuiEnabledForApply(); got != tc.want {
			t.Errorf("terminal stdout, pollable stdin=%v: live region for --apply = %v, want %v", tc.pollable, got, tc.want)
		}
	}
}

func TestStdinPollableManifestGate(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	d := defaultDeps()
	stubUIIsTerminal(d, true)
	d.stdinPollable = func() bool { return false }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, teardown := chooseManifestReporter(discardLog(), d, &config.Config{}, false, ctx, cancel)
	defer teardown()
	if typ := fmt.Sprintf("%T", r); !strings.Contains(typ, "plainReporter") {
		t.Errorf("terminal stdout with a stdin that cannot be polled built %s, want the plain reporter", typ)
	}
}
