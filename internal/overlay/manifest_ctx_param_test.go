package overlay

// Story 059, sub-task 6.2 (R1.6, R3.7): RegenerateManifests takes the context
// as its first parameter and spawns EVERY `pkgdev manifest` child with a
// context derived from it. ManifestOptions carries no context.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

type manifestCtx059Key struct{}

func TestRegenerateManifestsSpawnsFromTheCallersContext(t *testing.T) {
	overlay := t.TempDir()
	targets := []ManifestUpdate{{Category: "c", Package: "a"}, {Category: "c", Package: "b"}}
	for _, u := range targets {
		if err := os.MkdirAll(filepath.Join(overlay, u.Category, u.Package), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	oldLook := lookPath
	t.Cleanup(func() { lookPath = oldLook })
	lookPath = func(string) (string, error) { return "/usr/bin/pkgdev", nil }

	var (
		mu     sync.Mutex
		labels []any
		ctxs   []context.Context
	)
	oldExec := execCommand
	t.Cleanup(func() { execCommand = oldExec })
	execCommand = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		mu.Lock()
		labels = append(labels, ctx.Value(manifestCtx059Key{}))
		ctxs = append(ctxs, ctx)
		mu.Unlock()
		return exec.CommandContext(ctx, "true")
	}

	for _, label := range []string{"first-run", "second-run"} {
		mu.Lock()
		labels, ctxs = nil, nil
		mu.Unlock()

		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), manifestCtx059Key{}, label))
		RegenerateManifests(ctx, overlay, targets, &ManifestOptions{Jobs: 2})

		mu.Lock()
		gotLabels := append([]any(nil), labels...)
		gotCtxs := append([]context.Context(nil), ctxs...)
		mu.Unlock()

		if len(gotLabels) < len(targets) {
			t.Errorf("%s: %d child(ren) spawned for %d packages", label, len(gotLabels), len(targets))
		}
		for i, got := range gotLabels {
			if got != label {
				t.Errorf("%s: child %d was spawned with context label %v; want %q (R3.7)", label, i, got, label)
			}
		}
		cancel()
		for i, c := range gotCtxs {
			if c.Err() == nil {
				t.Errorf("%s: child %d's context is live after the caller's context was cancelled; it is not derived from it (R3.7)", label, i)
			}
		}
	}
}
