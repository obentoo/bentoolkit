package autoupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The serial pair is optional, and it is optional AS A PAIR. These tests pin
// both halves of that sentence, because each half fails differently: dropping
// the requirement without the pair rule turns a typo into a form posted without
// its credential, and keeping the requirement leaves every credential-free
// gated download outside the authenticated path.

// TestParseAuthFetchSpecSerialIsOptionalAsAPair covers the three shapes a
// [meta] block can give the pair: both, neither, and exactly one.
func TestParseAuthFetchSpecSerialIsOptionalAsAPair(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			metaFetchURL:      "https://example.test/dl",
			metaFetchFilename: "Foo_{version}.tar.xz",
			metaFetchForm:     "platform=linux",
		}
	}

	t.Run("neither half parses, and the spec says it carries no serial", func(t *testing.T) {
		spec, ok, err := parseAuthFetchSpec(base())
		if err != nil || !ok {
			t.Fatalf("got (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if spec.usesSerial() {
			t.Fatalf("usesSerial() = true for a spec that declares neither half: %+v", spec)
		}
	})

	// Each case names the half that is MISSING: the operator wrote the other
	// one, so echoing what they wrote tells them nothing they do not know.
	for _, tc := range []struct {
		name        string
		present     string
		wantMissing string
	}{
		{"env without field", metaFetchSerialEnv, metaFetchSerialField},
		{"field without env", metaFetchSerialField, metaFetchSerialEnv},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			meta := base()
			meta[tc.present] = "value"

			_, ok, err := parseAuthFetchSpec(meta)
			if err == nil || ok {
				t.Fatalf("got (ok=%v, err=%v), want (false, error)", ok, err)
			}
			if !errors.Is(err, ErrAuthFetchFailed) {
				t.Errorf("err = %v, want it to wrap ErrAuthFetchFailed", err)
			}
			if !strings.Contains(err.Error(), tc.wantMissing) {
				t.Errorf("err = %q, want it to name the missing half %q", err, tc.wantMissing)
			}
		})
	}
}

// TestFetchDistfileWithoutSerialPostsTheFormAlone is the behavioural half: a
// spec with no serial must reach the endpoint with exactly the configured form
// — no extra field, no lookup of a secret nobody named.
func TestFetchDistfileWithoutSerialPostsTheFormAlone(t *testing.T) {
	const payload = "BINARY-DISTFILE-CONTENT"

	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// The secrets file is emptied so that a regression resolving a secret here
	// fails the test rather than silently reading the operator's own file.
	withSecretsFile(t, "")

	spec, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      srv.URL,
		metaFetchFilename: "Foo_{version}.tar.xz",
		metaFetchForm:     "platform=linux&submit=Go",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	dir := t.TempDir()
	got, err := spec.fetchDistfile(context.Background(), "1.2.3", dir)
	if err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}
	if filepath.Base(got) != "Foo_1.2.3.tar.xz" {
		t.Fatalf("written file = %q", got)
	}
	data, err := os.ReadFile(got) //nolint:gosec // path produced by the call under test
	if err != nil || string(data) != payload {
		t.Fatalf("file content = %q (err %v), want %q", data, err, payload)
	}

	if len(gotForm) != 2 || gotForm.Get("platform") != "linux" || gotForm.Get("submit") != "Go" {
		t.Fatalf("server saw form %v, want exactly platform=linux&submit=Go", gotForm)
	}
	// The specific regression: a serial field set unconditionally posts a pair
	// under the empty name, which is what "" would select here.
	if _, blank := gotForm[""]; blank {
		t.Errorf("server saw a form field under the empty name: %v", gotForm)
	}
}
