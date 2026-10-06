package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAuthFetchCancelledKeepsTypedCause pins R4.4: an authenticated distfile
// fetch ended by cancellation or by its deadline returns an error on which
// errors.Is finds ErrAuthFetchFailed AND the context error.
func TestAuthFetchCancelledKeepsTypedCause(t *testing.T) {
	withSecretsFile(t, "")
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close (LIFO)

	spec, ok, err := ParseAuthFetchSpec(map[string]string{
		MetaFetchURL:      srv.URL + "/dl",
		MetaFetchFilename: "Foo_{version}.tar.xz",
		MetaFetchForm:     "platform=linux",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled before the request", cancelled, context.Canceled},
		{"deadline passes while the server is silent", short, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := spec.FetchDistfile(tc.ctx, "1.2.3", t.TempDir())
			if !errors.Is(err, ErrAuthFetchFailed) {
				t.Errorf("err = %v, want errors.Is(ErrAuthFetchFailed)", err)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want errors.Is(%v)", err, tc.want)
			}
		})
	}
}
