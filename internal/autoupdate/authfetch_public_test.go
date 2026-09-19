package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// authFetchOverlay builds a temp overlay holding a packages.toml and, for every
// named ebuild, an empty file in the package directory. The ebuilds exist
// because the version this call resolves when the caller names none comes from
// the OVERLAY, not from the registry — a fixture without them would exercise
// the error path while claiming to exercise the resolution.
func authFetchOverlay(t *testing.T, toml string, ebuilds ...string) string {
	t.Helper()
	overlay, _ := writePackagesTOML(t, toml)
	for _, rel := range ebuilds {
		path := filepath.Join(overlay, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte("EAPI=8\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return overlay
}

// authFetchServer serves a fixed payload as a binary attachment and records the
// path that was asked for, so a test can tell "the download happened" from "the
// file was there already".
func authFetchServer(t *testing.T, payload string) (url string, hits *int) {
	t.Helper()
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &count
}

// authFetchRecord renders one registry record with a [meta] block pointing at
// the test server. Only the keys this path reads are set; the rest of the
// schema is what an ordinary version-tracking record carries.
func authFetchRecord(key, url string) string {
	return fmt.Sprintf(`[%q]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
meta = { fetch_url = %q, fetch_filename = "Foo_{version}.tar.xz", fetch_form = "platform=linux" }
comments = """a package whose distfile no mirror may carry"""
`, key, url)
}

func TestFetchAuthDistfileResolvesTheOverlayVersion(t *testing.T) {
	const payload = "BINARY"
	url, hits := authFetchServer(t, payload)
	overlay := authFetchOverlay(t, authFetchRecord("app-misc/example", url),
		"app-misc/example/example-1.2.3.ebuild",
		// The older ebuild is here so a regression that takes the first entry
		// the directory listing yields, rather than the highest version, fails.
		"app-misc/example/example-1.0.0.ebuild",
	)

	dest := t.TempDir()
	res, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
		OverlayPath: overlay, Package: "app-misc/example", DestDir: dest,
	})
	if err != nil {
		t.Fatalf("FetchAuthDistfile: %v", err)
	}
	if res.Version != "1.2.3" {
		t.Errorf("version = %q, want the highest ebuild in the overlay (1.2.3)", res.Version)
	}
	if filepath.Base(res.Path) != "Foo_1.2.3.tar.xz" {
		t.Errorf("wrote %q, want the name fetch_filename resolves to", res.Path)
	}
	if res.SerialEnv != "" {
		t.Errorf("SerialEnv = %q, want empty for a record that configures no serial", res.SerialEnv)
	}
	data, err := os.ReadFile(res.Path) //nolint:gosec // path produced by the call under test
	if err != nil || string(data) != payload {
		t.Errorf("file content = %q (err %v), want %q", data, err, payload)
	}
	if *hits != 1 {
		t.Errorf("the endpoint was asked %d times, want exactly 1", *hits)
	}
}

func TestFetchAuthDistfileHonoursAnExplicitVersion(t *testing.T) {
	url, _ := authFetchServer(t, "BINARY")
	overlay := authFetchOverlay(t, authFetchRecord("app-misc/example", url),
		"app-misc/example/example-1.2.3.ebuild")

	dest := t.TempDir()
	res, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
		OverlayPath: overlay, Package: "app-misc/example", Version: "1.0.0", DestDir: dest,
	})
	if err != nil {
		t.Fatalf("FetchAuthDistfile: %v", err)
	}
	// The whole point of the flag: somebody installing an older ebuild needs
	// the file THAT ebuild's Manifest names, not the newest one's.
	if filepath.Base(res.Path) != "Foo_1.0.0.tar.xz" {
		t.Fatalf("wrote %q, want Foo_1.0.0.tar.xz", res.Path)
	}
}

func TestFetchAuthDistfileRequestErrors(t *testing.T) {
	url, _ := authFetchServer(t, "BINARY")

	t.Run("no record for the package", func(t *testing.T) {
		overlay := authFetchOverlay(t, authFetchRecord("app-misc/example", url))
		_, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
			OverlayPath: overlay, Package: "app-misc/absent", Version: "1", DestDir: t.TempDir(),
		})
		if !errors.Is(err, ErrPackageNotInRegistry) {
			t.Fatalf("err = %v, want ErrPackageNotInRegistry", err)
		}
	})

	t.Run("a record that configures no fetch", func(t *testing.T) {
		overlay := authFetchOverlay(t, `["app-misc/plain"]
url = "https://upstream.test/releases.json"
parser = "json"
path = "tag_name"
comments = """an ordinary package"""
`)
		_, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
			OverlayPath: overlay, Package: "app-misc/plain", Version: "1", DestDir: t.TempDir(),
		})
		if !errors.Is(err, ErrNoAuthFetch) {
			t.Fatalf("err = %v, want ErrNoAuthFetch", err)
		}
	})

	t.Run("an atom matching two records", func(t *testing.T) {
		overlay := authFetchOverlay(t,
			authFetchRecord("app-misc/example@stable", url)+authFetchRecord("app-misc/example@testing", url))

		_, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
			OverlayPath: overlay, Package: "app-misc/example", Version: "1", DestDir: t.TempDir(),
		})
		if !errors.Is(err, ErrAmbiguousPackageKey) {
			t.Fatalf("err = %v, want ErrAmbiguousPackageKey", err)
		}
		// Both keys must be named, or the operator cannot act on the refusal.
		for _, want := range []string{"app-misc/example@stable", "app-misc/example@testing"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("the full key disambiguates", func(t *testing.T) {
		overlay := authFetchOverlay(t,
			authFetchRecord("app-misc/example@stable", url)+authFetchRecord("app-misc/example@testing", url))

		res, err := FetchAuthDistfile(context.Background(), AuthDistfileRequest{
			OverlayPath: overlay, Package: "app-misc/example@testing", Version: "1", DestDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("FetchAuthDistfile: %v", err)
		}
		if res.Package != "app-misc/example@testing" {
			t.Fatalf("acted on %q, want the record that was named", res.Package)
		}
	})
}
