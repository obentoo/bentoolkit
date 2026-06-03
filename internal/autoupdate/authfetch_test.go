package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAuthFetchSpec(t *testing.T) {
	full := map[string]string{
		metaFetchMethod:      "post",
		metaFetchURL:         "https://example.test/dl",
		metaFetchSerialEnv:   "EXAMPLE_KEY",
		metaFetchSerialField: "key",
		metaFetchFilename:    "Foo_{version}_linux.tar.xz",
		metaFetchForm:        "platform=linux&submit=Go",
	}

	t.Run("no fetch_url disables fetch", func(t *testing.T) {
		spec, ok, err := parseAuthFetchSpec(map[string]string{"requires_serial": "true"})
		if err != nil || ok || spec != nil {
			t.Fatalf("got (%v, %v, %v), want (nil, false, nil)", spec, ok, err)
		}
	})

	t.Run("nil meta disables fetch", func(t *testing.T) {
		_, ok, err := parseAuthFetchSpec(nil)
		if err != nil || ok {
			t.Fatalf("got (ok=%v, err=%v), want (false, nil)", ok, err)
		}
	})

	t.Run("full spec parses", func(t *testing.T) {
		spec, ok, err := parseAuthFetchSpec(full)
		if err != nil || !ok {
			t.Fatalf("got (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if spec.method != "post" || spec.serialField != "key" || spec.serialEnv != "EXAMPLE_KEY" {
			t.Fatalf("unexpected spec: %+v", spec)
		}
		if got := spec.form.Get("platform"); got != "linux" {
			t.Fatalf("form platform = %q, want linux", got)
		}
	})

	t.Run("method defaults to post", func(t *testing.T) {
		m := cloneMeta(full)
		delete(m, metaFetchMethod)
		spec, _, err := parseAuthFetchSpec(m)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if spec.method != "post" {
			t.Fatalf("default method = %q, want post", spec.method)
		}
	})

	required := []string{metaFetchSerialEnv, metaFetchSerialField, metaFetchFilename}
	for _, key := range required {
		t.Run("missing "+key+" errors", func(t *testing.T) {
			m := cloneMeta(full)
			delete(m, key)
			if _, ok, err := parseAuthFetchSpec(m); err == nil || ok {
				t.Fatalf("got (ok=%v, err=%v), want (false, error)", ok, err)
			}
		})
	}

	t.Run("invalid method errors", func(t *testing.T) {
		m := cloneMeta(full)
		m[metaFetchMethod] = "delete"
		if _, _, err := parseAuthFetchSpec(m); err == nil {
			t.Fatal("expected error for invalid method")
		}
	})
}

func TestResolvedFilename(t *testing.T) {
	s := &authFetchSpec{filename: "Foo_{version}_x86_64.tar.xz"}
	if got := s.resolvedFilename("3.70.5"); got != "Foo_3.70.5_x86_64.tar.xz" {
		t.Fatalf("resolvedFilename = %q", got)
	}
}

func TestResolveSecret(t *testing.T) {
	t.Run("env var wins", func(t *testing.T) {
		t.Setenv("FZ_TEST_KEY", "from-env")
		got, err := resolveSecret("FZ_TEST_KEY")
		if err != nil || got != "from-env" {
			t.Fatalf("got (%q, %v), want (from-env, nil)", got, err)
		}
	})

	t.Run("file fallback", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "secrets")
		content := "# a comment\nOTHER=nope\nFZ_FILE_KEY = \"from-file\"\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		withSecretsFile(t, path)
		// Ensure env is unset so the file path is exercised.
		t.Setenv("FZ_FILE_KEY", "")
		got, err := resolveSecret("FZ_FILE_KEY")
		if err != nil || got != "from-file" {
			t.Fatalf("got (%q, %v), want (from-file, nil)", got, err)
		}
	})

	t.Run("missing everywhere errors", func(t *testing.T) {
		withSecretsFile(t, filepath.Join(t.TempDir(), "does-not-exist"))
		t.Setenv("FZ_ABSENT_KEY", "")
		if _, err := resolveSecret("FZ_ABSENT_KEY"); !errors.Is(err, ErrAuthFetchSecretMissing) {
			t.Fatalf("err = %v, want ErrAuthFetchSecretMissing", err)
		}
	})
}

func TestFetchDistfileSuccess(t *testing.T) {
	const wantSerial = "SECRET-123"
	const payload = "BINARY-DISTFILE-CONTENT"

	var gotSerial, gotPlatform string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotSerial = r.PostForm.Get("key")
		gotPlatform = r.PostForm.Get("platform")
		if gotSerial != wantSerial {
			// Mimic the vendor: re-show an HTML form on a bad/absent serial.
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			_, _ = w.Write([]byte("<form>...</form>"))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename=Foo_1.2.3_linux.tar.xz`)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	spec := &authFetchSpec{
		method:      "post",
		url:         srv.URL,
		serialEnv:   "FZ_OK_KEY",
		serialField: "key",
		filename:    "Foo_{version}_linux.tar.xz",
		form:        mustForm(t, "platform=linux&download_program=Go"),
	}
	t.Setenv("FZ_OK_KEY", wantSerial)

	dir := t.TempDir()
	got, err := spec.fetchDistfile(context.Background(), "1.2.3", dir)
	if err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}
	if filepath.Base(got) != "Foo_1.2.3_linux.tar.xz" {
		t.Fatalf("written file = %q", got)
	}
	data, err := os.ReadFile(got)
	if err != nil || string(data) != payload {
		t.Fatalf("file content = %q (err %v), want %q", data, err, payload)
	}
	if gotSerial != wantSerial || gotPlatform != "linux" {
		t.Fatalf("server saw serial=%q platform=%q", gotSerial, gotPlatform)
	}
}

func TestFetchDistfileHTMLRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte("<form>invalid serial</form>"))
	}))
	defer srv.Close()

	spec := &authFetchSpec{
		method: "post", url: srv.URL, serialEnv: "FZ_BAD_KEY", serialField: "key",
		filename: "Foo_{version}.tar.xz", form: mustForm(t, "platform=linux"),
	}
	t.Setenv("FZ_BAD_KEY", "whatever")

	dir := t.TempDir()
	_, err := spec.fetchDistfile(context.Background(), "1.0.0", dir)
	if !errors.Is(err, ErrAuthFetchFailed) || !strings.Contains(err.Error(), "HTML") {
		t.Fatalf("err = %v, want ErrAuthFetchFailed mentioning HTML", err)
	}
	// No file must be left behind in the distdir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("distdir not empty after failure: %v", entries)
	}
}

func TestFetchDistfileMissingSerial(t *testing.T) {
	spec := &authFetchSpec{
		method: "post", url: "https://unused.test", serialEnv: "FZ_NONE_KEY", serialField: "key",
		filename: "Foo.tar.xz", form: mustForm(t, ""),
	}
	t.Setenv("FZ_NONE_KEY", "")
	withSecretsFile(t, filepath.Join(t.TempDir(), "absent"))

	if _, err := spec.fetchDistfile(context.Background(), "1.0.0", t.TempDir()); !errors.Is(err, ErrAuthFetchSecretMissing) {
		t.Fatalf("err = %v, want ErrAuthFetchSecretMissing", err)
	}
}

func TestFetchDistfileRejectsUnsafeFilename(t *testing.T) {
	spec := &authFetchSpec{
		method: "post", url: "https://unused.test", serialEnv: "FZ_UNSAFE_KEY", serialField: "key",
		filename: "../escape-{version}.tar.xz", form: mustForm(t, ""),
	}
	t.Setenv("FZ_UNSAFE_KEY", "x")
	if _, err := spec.fetchDistfile(context.Background(), "1.0.0", t.TempDir()); !errors.Is(err, ErrAuthFetchFailed) {
		t.Fatalf("err = %v, want ErrAuthFetchFailed for unsafe filename", err)
	}
}

func TestScrubSecret(t *testing.T) {
	if got := scrubSecret("url?key=ABC123&x=1", "ABC123"); strings.Contains(got, "ABC123") {
		t.Fatalf("scrubSecret left the secret in: %q", got)
	}
	if got := scrubSecret("no secret here", ""); got != "no secret here" {
		t.Fatalf("scrubSecret with empty secret altered the string: %q", got)
	}
}

// --- helpers ---

func cloneMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustForm(t *testing.T, raw string) (v map[string][]string) {
	t.Helper()
	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL: "https://x.test", metaFetchSerialEnv: "E", metaFetchSerialField: "k",
		metaFetchFilename: "f", metaFetchForm: raw,
	})
	if err != nil {
		t.Fatalf("mustForm: %v", err)
	}
	return spec.form
}

// withSecretsFile points the secrets-file resolver at path for the duration of
// the test, restoring the original afterwards.
func withSecretsFile(t *testing.T, path string) {
	t.Helper()
	orig := secretsFilePath
	secretsFilePath = func() string { return path }
	t.Cleanup(func() { secretsFilePath = orig })
}
