//go:build unix

package main

// Authored for story 070, sub-task 2.2 — R4.2, R4.4 (and R4.1's signature).
//
// Written from story.md R4 and design.md "Interfaces" / "Testing Strategy",
// never from an implementation.
//
// Each scenario runs in a re-exec'd child of the test binary, for two reasons:
//   - the logger writes to the process's stderr, and only a child's stderr can
//     be read whole no matter when the logger was first built;
//   - net/http reads HTTPS_PROXY once per process, so pointing the registry
//     download at a never-answering proxy needs a process of its own.
//
// The two halves of "interrupted is not missing" are both here:
//   - an interrupted resolution must not read as a missing gentoo
//     (...InterruptedIsNotNotFound, ...SkipsNoWarningWhenInterrupted);
//   - a missing gentoo must not read as an interruption
//     (...NotFoundIsNotInterrupted, ...StillWarnsWhenGentooIsMissing).
//
// Red before the fix is a build failure: resolveGentooProvider and
// deps.resolveGentooProvider take no context.Context and registryInterruptedMsg
// is undefined.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

const s070ReviveModeEnv = "BENTOO_TEST_S070_REVIVE_MODE"

// s070RunReviveChild runs TestS070ReviveHelperChild in mode with HOME at a
// fresh temp dir and every proxy variable cleared, and returns its combined
// output and exit code.
func s070RunReviveChild(t *testing.T, mode string) (string, int) {
	t.Helper()
	home := t.TempDir()
	// The child bounds itself; this timeout is only a backstop.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestS070ReviveHelperChild$", "-test.count=1", "-test.v")
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(name) {
		case "HOME", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
			"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		s070ReviveModeEnv+"="+mode)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	outB, err := cmd.CombinedOutput()
	out := string(outB)
	if ctx.Err() != nil {
		t.Fatalf("revive child %q did not end within 60s:\n%s", mode, out)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running revive child %q: %v", mode, err)
		}
		code = ee.ExitCode()
	}
	return out, code
}

// TestS070ReviveHelperChild is the child role of every test in this file.
func TestS070ReviveHelperChild(t *testing.T) {
	mode := os.Getenv(s070ReviveModeEnv)
	if mode == "" {
		t.Skip("child role only")
	}
	switch mode {
	case "orphans-interrupted", "orphans-missing":
		resolveErr := fmt.Errorf("%s: %w", registryInterruptedMsg, context.Canceled)
		if mode == "orphans-missing" {
			resolveErr = fmt.Errorf("repository 'gentoo' not found: %w", provider.ErrRepositoryNotFound)
		}
		d := defaultDeps()
		d.resolveGentooProvider = func(ctx context.Context, cfg *config.Config) (provider.Provider, error) {
			return nil, resolveErr
		}
		ar := &autoupdateRun{opts: testAutoupdateOptions(), deps: d}
		ar.reportRevivableOrphans(context.Background(), nil, &config.Config{})
		fmt.Println("S070-REPORT-RETURNED")
	case "resolve-interrupted":
		s070ResolveInterrupted(t)
	case "resolve-missing":
		s070ResolveMissing(t)
	default:
		t.Fatalf("unknown mode %q", mode)
	}
}

// s070NeverAnsweringProxy starts a proxy that accepts and never answers,
// points every proxy variable of this (child) process at it and returns a
// channel closed on its first connection.
func s070NeverAnsweringProxy(t *testing.T) <-chan struct{} {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	reached := make(chan struct{})
	go func() {
		first := true
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
			if first {
				first = false
				close(reached)
			}
		}
	}()
	proxyURL := "http://" + ln.Addr().String()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(name, proxyURL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	return reached
}

func s070ResolveInterrupted(t *testing.T) {
	reached := s070NeverAnsweringProxy(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		prov provider.Provider
		err  error
	}
	res := make(chan result, 1)
	go func() {
		prov, err := resolveGentooProvider(ctx, &config.Config{})
		res <- result{prov, err}
	}()
	select {
	case <-reached:
	case r := <-res:
		t.Fatalf("resolveGentooProvider returned before the registry download reached the proxy: prov=%v err=%v", r.prov, r.err)
	case <-time.After(20 * time.Second):
		t.Fatal("the registry download never reached the proxy within 20s")
	}
	cancel()
	var r result
	select {
	case r = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("resolveGentooProvider was still waiting 5s after its context was cancelled")
	}
	if r.err == nil {
		t.Fatalf("resolveGentooProvider returned no error for an interrupted registry download (prov=%v)", r.prov)
	}
	if !errors.Is(r.err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) is false for an interrupted registry download: %v", r.err)
	}
	if errors.Is(r.err, provider.ErrRepositoryNotFound) {
		t.Errorf("an interrupted registry download is reported as ErrRepositoryNotFound: %v", r.err)
	}
	if !strings.Contains(r.err.Error(), registryInterruptedMsg) {
		t.Errorf("error does not say %q: %v", registryInterruptedMsg, r.err)
	}
	if strings.Contains(r.err.Error(), "not found") {
		t.Errorf("an interrupted registry download says \"not found\": %v", r.err)
	}
}

// s070ResolveMissing: a fresh registry cache that does not list gentoo, no
// cancellation — a missing gentoo, which must stay "not found".
func s070ResolveMissing(t *testing.T) {
	reached := s070NeverAnsweringProxy(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(home, ".cache", "bentoo")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<repositories version="1.0">
  <repo quality="experimental" status="unofficial">
    <name>s070-other</name>
    <source type="git">https://github.com/s070/other.git</source>
  </repo>
</repositories>
`
	if err := os.WriteFile(filepath.Join(cacheDir, "repositories.xml"), []byte(xml), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	prov, err := resolveGentooProvider(ctx, &config.Config{})
	if err == nil {
		t.Fatalf("resolveGentooProvider found a gentoo the registry does not list (prov=%v)", prov)
	}
	select {
	case <-reached:
		t.Errorf("a fresh registry cache still made resolveGentooProvider reach the network")
	default:
	}
	if !errors.Is(err, provider.ErrRepositoryNotFound) {
		t.Errorf("a missing gentoo is not ErrRepositoryNotFound: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("a missing gentoo is reported as context.Canceled: %v", err)
	}
	if strings.Contains(err.Error(), registryInterruptedMsg) {
		t.Errorf("a missing gentoo is reported as an interruption: %v", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing gentoo no longer says \"not found\": %v", err)
	}
}

func s070RequireChildPassed(t *testing.T, mode, out string, code int) {
	t.Helper()
	if code != 0 || !strings.Contains(out, "--- PASS: TestS070ReviveHelperChild") {
		t.Fatalf("revive child %q failed (exit %d):\n%s", mode, code, out)
	}
}

// TestS070ResolveGentooProviderInterruptedIsNotNotFound is R4.2.
func TestS070ResolveGentooProviderInterruptedIsNotNotFound(t *testing.T) {
	t.Parallel()
	out, code := s070RunReviveChild(t, "resolve-interrupted")
	s070RequireChildPassed(t, "resolve-interrupted", out, code)
}

// TestS070ResolveGentooProviderNotFoundIsNotInterrupted is R4.2's converse.
func TestS070ResolveGentooProviderNotFoundIsNotInterrupted(t *testing.T) {
	t.Parallel()
	out, code := s070RunReviveChild(t, "resolve-missing")
	s070RequireChildPassed(t, "resolve-missing", out, code)
}

// TestS070RevivableOrphansSkipsNoWarningWhenInterrupted is R4.4.
func TestS070RevivableOrphansSkipsNoWarningWhenInterrupted(t *testing.T) {
	t.Parallel()
	out, code := s070RunReviveChild(t, "orphans-interrupted")
	s070RequireChildPassed(t, "orphans-interrupted", out, code)
	if !strings.Contains(out, "S070-REPORT-RETURNED") {
		t.Fatalf("reportRevivableOrphans did not return:\n%s", out)
	}
	if strings.Contains(out, "revivable-orphan scan skipped") {
		t.Errorf("an interrupted --check --revivable still warns \"revivable-orphan scan skipped\":\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("an interrupted --check --revivable prints \"not found\":\n%s", out)
	}
}

// TestS070RevivableOrphansStillWarnsWhenGentooIsMissing is R4.4's converse.
func TestS070RevivableOrphansStillWarnsWhenGentooIsMissing(t *testing.T) {
	t.Parallel()
	out, code := s070RunReviveChild(t, "orphans-missing")
	s070RequireChildPassed(t, "orphans-missing", out, code)
	if !strings.Contains(out, "revivable-orphan scan skipped") || !strings.Contains(out, "not found") {
		t.Errorf("a missing gentoo no longer warns \"revivable-orphan scan skipped: ... not found\":\n%s", out)
	}
}
