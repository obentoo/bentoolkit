package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/httpx"
)

// registryBodyServer streams a 200 body of exactly size bytes.
func registryBodyServer(t *testing.T, size int64) *httptest.Server {
	t.Helper()
	chunk := strings.Repeat("x", 64*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for written := int64(0); written < size; {
			n := min(int64(len(chunk)), size-written)
			if _, err := io.WriteString(w, chunk[:n]); err != nil {
				return
			}
			written += n
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// seedEselectFallback writes the eselect cache the registry falls back to and
// returns its content.
func seedEselectFallback(t *testing.T) []byte {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	fallback := []byte("<repositories><!-- eselect fallback --></repositories>")
	path := filepath.Join(home, eselectCachePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir fallback dir: %v", err)
	}
	if err := os.WriteFile(path, fallback, 0o600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	return fallback
}

// TestRegistryDownloadBodyCapped pins R5.2: a registry body over 10 MiB fails
// with ErrResponseTooLarge and writes NO cache file, and the lookup falls back
// to the eselect cache. Hostile half: a body of exactly MaxBodyBytes is within
// the cap and is cached whole.
func TestRegistryDownloadBodyCapped(t *testing.T) {
	t.Run("over the cap", func(t *testing.T) {
		reg := newTestRegistry(t, registryBodyServer(t, httpx.MaxBodyBytes+1))
		err := reg.Sync(context.Background())
		if !errors.Is(err, httpx.ErrResponseTooLarge) {
			t.Errorf("Sync() = %v, want errors.Is(err, httpx.ErrResponseTooLarge)", err)
		}
		if _, statErr := os.Stat(reg.XMLPath); !os.IsNotExist(statErr) {
			t.Errorf("cache file %s exists after an over-cap download (stat: %v); want none written", reg.XMLPath, statErr)
		}
	})

	t.Run("over the cap falls back to the eselect cache", func(t *testing.T) {
		fallback := seedEselectFallback(t)
		reg := newTestRegistry(t, registryBodyServer(t, 64<<20))
		data, err := reg.ensureXML(context.Background())
		if err != nil {
			t.Fatalf("ensureXML: %v; want the eselect fallback", err)
		}
		if !bytes.Equal(data, fallback) {
			t.Errorf("ensureXML returned %d bytes, want the %d-byte eselect fallback", len(data), len(fallback))
		}
		if _, statErr := os.Stat(reg.XMLPath); !os.IsNotExist(statErr) {
			t.Errorf("cache file written from an over-cap body (stat: %v)", statErr)
		}
	})

	t.Run("exactly at the cap", func(t *testing.T) {
		reg := newTestRegistry(t, registryBodyServer(t, httpx.MaxBodyBytes))
		if err := reg.Sync(context.Background()); err != nil {
			t.Fatalf("Sync() = %v for a body of exactly %d bytes", err, httpx.MaxBodyBytes)
		}
		info, err := os.Stat(reg.XMLPath)
		if err != nil || info.Size() != httpx.MaxBodyBytes {
			t.Errorf("cache file stat = %v, %v; want %d bytes", info, err, httpx.MaxBodyBytes)
		}
	})
}

// TestRegistryDownloadTimesOut pins R5.3: a registry host that accepts the
// connection and never answers is abandoned within 30 s, and the lookup uses
// the eselect cache. This test runs in real time (about 30 s); the 45 s bound
// is an assertion, not synchronisation.
func TestRegistryDownloadTimesOut(t *testing.T) {
	fallback := seedEselectFallback(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close (LIFO)

	reg := newTestRegistry(t, srv)
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		data, err := reg.ensureXML(context.Background())
		done <- result{data, err}
	}()

	select {
	case r := <-done:
		if elapsed := time.Since(start); elapsed > 35*time.Second {
			t.Errorf("download abandoned after %v, want within 30s", elapsed)
		}
		if r.err != nil {
			t.Fatalf("ensureXML: %v; want the eselect fallback after the timeout", r.err)
		}
		if !bytes.Equal(r.data, fallback) {
			t.Errorf("ensureXML returned %d bytes, want the eselect fallback", len(r.data))
		}
	case <-time.After(45 * time.Second):
		t.Fatal("registry download still running 45s after start; want it abandoned at 30s")
	}
	if _, statErr := os.Stat(reg.XMLPath); !os.IsNotExist(statErr) {
		t.Errorf("cache file written by a download that never completed (stat: %v)", statErr)
	}
}
