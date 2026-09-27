package autoupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Story 054, R6: AnalyzeAll is a bounded, cancellable, panic-safe pool with
// sorted Items. The per-package work goes through the analyzer's analyzeFn
// seam (func(pkg string, opts AnalyzeOptions) (*AnalyzeResult, error)), which
// defaults to Analyze.

func analyzeAllFixture(t *testing.T, n int, opts ...AnalyzerOption) (*Analyzer, []string) {
	t.Helper()
	dir := t.TempDir()
	var pkgs []string
	for i := range n {
		name := "pkg-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		pkgDir := filepath.Join(dir, "app-misc", name)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, name+"-1.0.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, "app-misc/"+name)
	}
	a, err := NewAnalyzer(dir, opts...)
	if err != nil {
		t.Fatalf("NewAnalyzer: %v", err)
	}
	return a, pkgs
}

func runAnalyzeAll(t *testing.T, a *Analyzer) BatchResult[AnalyzeResult] {
	t.Helper()
	done := make(chan BatchResult[AnalyzeResult], 1)
	go func() { done <- a.AnalyzeAll(AnalyzeOptions{NoCache: true}) }()
	select {
	case b := <-done:
		return b
	case <-time.After(20 * time.Second):
		t.Fatal("AnalyzeAll did not return")
		return BatchResult[AnalyzeResult]{}
	}
}

// R6.1: at most 3 workers alive — measured as goroutines, not only as
// concurrent calls (a semaphore acquired INSIDE the goroutine also caps calls
// at 3 while 50 goroutines wait). Converse: the pool really runs 3 at once.
func TestAnalyzeAllBoundsGoroutines(t *testing.T) {
	a, pkgs := analyzeAllFixture(t, 50)
	gate := make(chan struct{})
	inFlight := make(chan struct{}, len(pkgs))
	var cur, peak atomic.Int32
	a.analyzeFn = func(pkg string, _ AnalyzeOptions) (*AnalyzeResult, error) {
		n := cur.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		inFlight <- struct{}{}
		<-gate
		cur.Add(-1)
		return &AnalyzeResult{Package: pkg}, nil
	}

	base := runtime.NumGoroutine()
	done := make(chan BatchResult[AnalyzeResult], 1)
	go func() { done <- a.AnalyzeAll(AnalyzeOptions{NoCache: true}) }()
	for range 3 {
		select {
		case <-inFlight:
		case <-time.After(10 * time.Second):
			close(gate)
			t.Fatal("fewer than 3 packages ever ran at once: the pool does not use its 3 slots")
		}
	}
	extra := runtime.NumGoroutine() - base
	close(gate)
	b := <-done

	if extra > 3+2 { // 3 workers + the AnalyzeAll caller + slack
		t.Errorf("%d extra goroutines while 3 packages ran: AnalyzeAll starts a goroutine per package before taking a slot (R6.1)", extra)
	}
	if p := peak.Load(); p != 3 {
		t.Errorf("peak concurrent analyses = %d, want exactly 3", p)
	}
	if len(b.Items) != len(pkgs) {
		t.Errorf("Items = %d, want %d", len(b.Items), len(pkgs))
	}
}

// R6.2: a context done before a package obtains a slot means Analyze is never
// called for it and the package is a failure wrapping ctx.Err().
func TestAnalyzeAllCancelledStartsNothing(t *testing.T) {
	t.Run("cancelled before the call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		a, pkgs := analyzeAllFixture(t, 5, WithAnalyzerContext(ctx))
		var calls atomic.Int32
		a.analyzeFn = func(pkg string, _ AnalyzeOptions) (*AnalyzeResult, error) {
			calls.Add(1)
			return &AnalyzeResult{Package: pkg}, nil
		}
		b := runAnalyzeAll(t, a)
		if n := calls.Load(); n != 0 {
			t.Errorf("Analyze called %d times on a cancelled context", n)
		}
		for _, p := range pkgs {
			if err := b.Failures[p]; !errors.Is(err, context.Canceled) {
				t.Errorf("Failures[%q] = %v, want an error wrapping context.Canceled", p, err)
			}
		}
	})
	// Converse: work already started completes; nothing new starts after the
	// cancel, even when a slot frees up afterwards.
	t.Run("cancelled mid-flight", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, pkgs := analyzeAllFixture(t, 8, WithAnalyzerContext(ctx))
		gate := make(chan struct{})
		started := make(chan struct{}, len(pkgs))
		var calls atomic.Int32
		a.analyzeFn = func(pkg string, _ AnalyzeOptions) (*AnalyzeResult, error) {
			calls.Add(1)
			started <- struct{}{}
			<-gate
			return &AnalyzeResult{Package: pkg}, nil
		}
		done := make(chan BatchResult[AnalyzeResult], 1)
		go func() { done <- a.AnalyzeAll(AnalyzeOptions{NoCache: true}) }()
		for range 3 {
			<-started
		}
		cancel()
		close(gate)
		b := <-done
		if n := calls.Load(); n != 3 {
			t.Errorf("Analyze called %d times, want 3 (only the in-flight ones)", n)
		}
		if len(b.Items) != 3 || len(b.Failures) != len(pkgs)-3 {
			t.Errorf("Items=%d Failures=%d, want 3 and %d", len(b.Items), len(b.Failures), len(pkgs)-3)
		}
		for p, err := range b.Failures {
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Failures[%q] = %v, want context.Canceled", p, err)
			}
		}
	})
}

// R6.3: a panic is one package's failure, not the process's.
func TestAnalyzeAllRecoversPanic(t *testing.T) {
	a, pkgs := analyzeAllFixture(t, 4)
	victim := pkgs[1]
	a.analyzeFn = func(pkg string, _ AnalyzeOptions) (*AnalyzeResult, error) {
		if pkg == victim {
			panic("boom")
		}
		return &AnalyzeResult{Package: pkg}, nil
	}
	b := runAnalyzeAll(t, a)
	err := b.Failures[victim]
	if err == nil || !strings.Contains(err.Error(), "panic: boom") {
		t.Errorf("Failures[%q] = %v, want one reading \"panic: boom\"", victim, err)
	}
	if len(b.Items) != len(pkgs)-1 || len(b.Failures) != 1 {
		t.Errorf("Items=%d Failures=%d, want %d and 1: every other package must complete", len(b.Items), len(b.Failures), len(pkgs)-1)
	}
}

// R6.4: Items are sorted by Package even when completion order is reversed.
func TestAnalyzeAllSortsItems(t *testing.T) {
	a, pkgs := analyzeAllFixture(t, 3) // all three fit the 3 slots at once
	finished := map[string]chan struct{}{}
	for _, p := range pkgs {
		finished[p] = make(chan struct{})
	}
	var mu sync.Mutex
	a.analyzeFn = func(pkg string, _ AnalyzeOptions) (*AnalyzeResult, error) {
		for i, p := range pkgs { // pkgs[i] waits for pkgs[i+1]: reverse completion
			if p == pkg && i+1 < len(pkgs) {
				<-finished[pkgs[i+1]]
			}
		}
		mu.Lock()
		defer mu.Unlock()
		close(finished[pkg])
		return &AnalyzeResult{Package: pkg}, nil
	}
	b := runAnalyzeAll(t, a)
	var got []string
	for _, it := range b.Items {
		got = append(got, it.Package)
	}
	if strings.Join(got, ",") != strings.Join(pkgs, ",") {
		t.Errorf("Items order = %v, want %v (Package ascending, not completion order)", got, pkgs)
	}
}
