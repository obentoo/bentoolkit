package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fetch_form_env is the answer to a form that asks for a PERSON. These tests
// hold it to the two properties that make it worth having: the values reach the
// vendor, and they never appear in anything written down.

func TestFormEnvValuesReachTheVendorWithoutEnteringTheRecord(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("PK\x03\x04payload"))
	}))
	defer srv.Close()

	withSecretsFile(t, "")
	t.Setenv("TEST_BMD_FIRSTNAME", "Ada")
	t.Setenv("TEST_BMD_EMAIL", "ada@example.com")

	meta := map[string]string{
		metaFetchURL:      srv.URL,
		metaFetchBody:     fetchBodyJSON,
		metaFetchFilename: "x-{version}.zip",
		metaFetchForm:     "product=Example&policy=true",
		metaFetchFormEnv:  "firstname=TEST_BMD_FIRSTNAME&email=TEST_BMD_EMAIL",
	}
	spec, ok, err := parseAuthFetchSpec(meta)
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	// The record holds NAMES, never values. This is the whole point: the record
	// is committed to a public overlay.
	for _, value := range []string{"Ada", "ada@example.com"} {
		for key, raw := range meta {
			if strings.Contains(raw, value) {
				t.Errorf("the %s value %q appears in the record under %s", metaFetchFormEnv, value, key)
			}
		}
	}

	if _, err := spec.fetchDistfile(context.Background(), "1.0", t.TempDir()); err != nil {
		t.Fatalf("fetchDistfile: %v", err)
	}
	if sent["firstname"] != "Ada" || sent["email"] != "ada@example.com" {
		t.Errorf("the vendor did not receive the resolved identity: %#v", sent)
	}
	if sent["product"] != "Example" || sent["policy"] != true {
		t.Errorf("the static fields did not survive alongside them: %#v", sent)
	}
}

func TestFormEnvMissingVariableNamesBothTheFieldAndTheVariable(t *testing.T) {
	withSecretsFile(t, "")
	t.Setenv("TEST_BMD_PHONE", "")

	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
		metaFetchFormEnv:  "phone=TEST_BMD_PHONE",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
	if !errors.Is(err, ErrAuthFetchSecretMissing) {
		t.Fatalf("err = %v, want ErrAuthFetchSecretMissing", err)
	}
	// Both halves, because "a secret is missing" without saying which field
	// needed it leaves the operator reading the whole record to find out.
	for _, want := range []string{"TEST_BMD_PHONE", `"phone"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to mention %s", err, want)
		}
	}
}

// TestCredentialsScrubRedactsResolvedValues pins the redaction rule, including
// the length threshold: a value too short to identify anybody is also too short
// to substitute without blanking out unrelated text.
func TestCredentialsScrubRedactsResolvedValues(t *testing.T) {
	withSecretsFile(t, "")
	t.Setenv("TEST_SCRUB_SERIAL", "AB") // short, but a credential
	t.Setenv("TEST_SCRUB_STREET", "Rua Exemplo 100")
	t.Setenv("TEST_SCRUB_STATE", "SP") // short identity value

	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:         "https://vendor.test/dl",
		metaFetchFilename:    "x-{version}.zip",
		metaFetchSerialEnv:   "TEST_SCRUB_SERIAL",
		metaFetchSerialField: "key",
		metaFetchFormEnv:     "street=TEST_SCRUB_STREET&state=TEST_SCRUB_STATE",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	creds, err := spec.resolveCredentials()
	if err != nil {
		t.Fatalf("resolveCredentials: %v", err)
	}

	got := creds.scrub("post to https://vendor.test/dl failed for AB at Rua Exemplo 100 in SP")
	if strings.Contains(got, "AB") {
		t.Errorf("the serial survived scrubbing regardless of its length: %q", got)
	}
	if strings.Contains(got, "Rua Exemplo 100") {
		t.Errorf("an identity value survived scrubbing: %q", got)
	}
	// Deliberately NOT redacted, and the assertion records why: substituting a
	// two-character value would blank out fragments of the message somebody
	// needs, and it identifies nobody on its own.
	if !strings.Contains(got, "SP") {
		t.Errorf("a two-character identity value was redacted, mangling the message: %q", got)
	}
}

func TestFormEnvAndTimeoutRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		meta  map[string]string
		wants []string
	}{
		{
			name:  "identity fields in a query string",
			meta:  map[string]string{metaFetchMethod: "get", metaFetchFormEnv: "phone=V"},
			wants: []string{metaFetchFormEnv, "query string"},
		},
		{
			name:  "one field from two sources",
			meta:  map[string]string{metaFetchForm: "email=a@b.test", metaFetchFormEnv: "email=V"},
			wants: []string{`"email"`, "exactly one"},
		},
		{
			name:  "the serial field repeated",
			meta:  map[string]string{metaFetchSerialEnv: "S", metaFetchSerialField: "key", metaFetchFormEnv: "key=V"},
			wants: []string{`"key"`, metaFetchSerialField},
		},
		{
			name:  "two variables for one field",
			meta:  map[string]string{metaFetchFormEnv: "phone=A&phone=B"},
			wants: []string{"one field takes one value"},
		},
		{
			name:  "a field with no variable",
			meta:  map[string]string{metaFetchFormEnv: "phone="},
			wants: []string{"empty variable name"},
		},
		{
			name:  "a budget that is not a number",
			meta:  map[string]string{metaFetchTimeout: "10m"},
			wants: []string{metaFetchTimeout, "whole number of seconds"},
		},
		{
			name:  "a budget of zero",
			meta:  map[string]string{metaFetchTimeout: "0"},
			wants: []string{metaFetchTimeout, "must be positive"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]string{metaFetchURL: "https://vendor.test/dl", metaFetchFilename: "x-{version}.zip"}
			for k, v := range tc.meta {
				meta[k] = v
			}
			_, ok, err := parseAuthFetchSpec(meta)
			if err == nil || ok {
				t.Fatalf("got (ok=%v, err=%v), want (false, error)", ok, err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// TestFetchTimeoutIsHonoured is the behavioural half: the budget must reach the
// client, not merely be parsed. The default is five minutes, so a test that
// only checked parsing would pass against a spec whose value nothing reads.
func TestFetchTimeoutIsHonoured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write([]byte("too late"))
	}))
	defer srv.Close()

	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      srv.URL,
		metaFetchFilename: "x-{version}.zip",
		metaFetchTimeout:  "1",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.timeout != time.Second {
		t.Fatalf("spec.timeout = %v, want 1s", spec.timeout)
	}

	start := time.Now()
	_, err = spec.fetchDistfile(context.Background(), "1.0", t.TempDir())
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Fatalf("err = %v, want ErrAuthFetchFailed", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the fetch took %v, so the 1s budget was not the one in force", elapsed)
	}
}

// The default must stay what every record written before fetch_timeout existed
// relied on.
func TestFetchTimeoutDefaultsToFiveMinutes(t *testing.T) {
	spec, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if spec.timeout != authFetchTimeout {
		t.Errorf("default timeout = %v, want %v", spec.timeout, authFetchTimeout)
	}
}
