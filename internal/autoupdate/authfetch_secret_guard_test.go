package autoupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// authFetchErrorChain returns every error reachable from err through Unwrap,
// both the single and the multi-error forms.
func authFetchErrorChain(err error) []error {
	var out []error
	queue := []error{err}
	for len(queue) > 0 {
		e := queue[0]
		queue = queue[1:]
		if e == nil {
			continue
		}
		out = append(out, e)
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			queue = append(queue, u.Unwrap()...)
		case interface{ Unwrap() error }:
			queue = append(queue, u.Unwrap())
		}
	}
	return out
}

// TestAuthFetchCauseNeverCarriesSecret pins R4.5 (regression guard, green
// today): whatever cause R4.4 exposes, no error anywhere in the Unwrap chain may
// carry a resolved credential. The fixture forces the hostile case — the
// credential value is also part of the request URL, so the transport's own
// *url.Error text contains it; only a scrubbed or text-free cause is allowed
// to be reachable.
func TestAuthFetchCauseNeverCarriesSecret(t *testing.T) {
	withSecretsFile(t, "")
	const serial = "SERIAL-055-DO-NOT-LEAK-7f3a"
	const street = "Rua-Exemplo-055-Numero-100"
	t.Setenv("BENTOO_FETCH_TEST_055_SERIAL", serial)
	t.Setenv("BENTOO_FETCH_TEST_055_STREET", street)

	spec, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:         "http://127.0.0.1:1/dl/" + street + "/" + serial,
		metaFetchFilename:    "x-{version}.zip",
		metaFetchSerialEnv:   "BENTOO_FETCH_TEST_055_SERIAL",
		metaFetchSerialField: "key",
		metaFetchFormEnv:     "street=BENTOO_FETCH_TEST_055_STREET",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"cancelled", cancelled},
		{"connection refused", context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, fetchErr := spec.fetchDistfile(tc.ctx, "1.2.3", t.TempDir())
			if !errors.Is(fetchErr, ErrAuthFetchFailed) {
				t.Fatalf("err = %v, want errors.Is(ErrAuthFetchFailed)", fetchErr)
			}
			for _, e := range authFetchErrorChain(fetchErr) {
				for _, secret := range []string{serial, street} {
					if strings.Contains(e.Error(), secret) {
						t.Errorf("error %T in the Unwrap chain carries a resolved credential: %q", e, e.Error())
					}
				}
			}
		})
	}
}
