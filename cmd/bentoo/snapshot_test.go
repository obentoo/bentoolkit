package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// sentinelExit distinguishes our osExit panic from a real one in captureExit.
type sentinelExit struct{}

// captureExit stubs osExit, runs fn, and reports the exit code (if any). The
// run* verbs call osExit(1) then `return`; the stub panics so the `return` is
// never reached, and the panic is recovered here.
func captureExit(t *testing.T, fn func()) (code int, exited bool) {
	t.Helper()
	orig := osExit
	t.Cleanup(func() { osExit = orig })
	osExit = func(c int) {
		code = c
		exited = true
		panic(sentinelExit{})
	}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(sentinelExit); !ok {
				panic(r)
			}
		}
	}()
	fn()
	return code, exited
}

// captureStdout redirects os.Stdout (and fatih/color's Output) for the duration
// of fn and returns everything written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout, origColorOut, origNoColor := os.Stdout, color.Output, color.NoColor
	os.Stdout = w
	color.Output = w
	color.NoColor = true
	t.Cleanup(func() {
		os.Stdout, color.Output, color.NoColor = origStdout, origColorOut, origNoColor
	})

	fn()
	_ = w.Close()
	data, _ := io.ReadAll(r)
	return string(data)
}

// stubBinariesOnPath creates executable stubs for names in a temp dir prepended
// to PATH, so the snapshot package's real lookPath-based detection succeeds
// without those tools actually installed.
func stubBinariesOnPath(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeSnapshotConfig writes content to a temp snapshot.toml, points the --config
// flag at it, and resets the flag + runner seam after the test. Returns the dir
// and the config path.
func writeSnapshotConfig(t *testing.T, content string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "snapshot.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	origPath, origRunner := snapshotConfigPath, snapshotRunner
	snapshotConfigPath = path
	t.Cleanup(func() { snapshotConfigPath, snapshotRunner = origPath, origRunner })
	return dir, path
}

// redirectStateDir points snapshot.StateDir at a temp dir for the test.
func redirectStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := snapshot.StateDir
	t.Cleanup(func() { snapshot.StateDir = orig })
	snapshot.StateDir = func() string { return dir }
	return dir
}

const validSnapshotTOML = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"

[[ship]]
type = "ssh"
target = "user@host:/backup"
`

func TestSnapshotGroupWired(t *testing.T) {
	// Group registered on rootCmd (main.go init).
	var onRoot bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "snapshot" {
			onRoot = true
		}
	}
	if !onRoot {
		t.Fatal("snapshot group not registered on rootCmd")
	}

	// All four verbs registered under the group.
	want := map[string]bool{"apply": false, "run": false, "list": false, "status": false}
	for _, c := range snapshotCmd.Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("verb %q not registered under snapshot", name)
		}
	}

	// Persistent --config flag present.
	if snapshotCmd.PersistentFlags().Lookup("config") == nil {
		t.Error("--config persistent flag missing")
	}
}
