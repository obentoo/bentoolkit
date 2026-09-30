package autoupdate

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Story 076: a BENTOO_FETCH_ name holding anything outside [A-Za-z0-9_] can
// never resolve — no shell exports it and the secrets file is read line by
// line — yet it used to pass every refusal and reach the missing-secret error
// raw. It is now refused from the name alone, and every name or key the
// authenticated fetch prints is quoted.

var s076ControlBytes = []string{"\n", "\r", "\x1b", "\x07"}

func s076AssertEscaped(t *testing.T, err error, sentinel error, quoted ...string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
	msg := err.Error()
	for _, b := range s076ControlBytes {
		if strings.Contains(msg, b) {
			t.Errorf("the error carries the raw control byte %q: %q", b, msg)
		}
	}
	for _, s := range quoted {
		if want := strconv.Quote(s); !strings.Contains(msg, want) {
			t.Errorf("the error does not print %s: %q", want, msg)
		}
	}
}

func TestS076FormEnvRefusesNonVariableNames(t *testing.T) {
	for _, name := range []string{"BENTOO_FETCH_X\nforged", "BENTOO_FETCH_A-B", "BENTOO_FETCH_A.B"} {
		_, _, err := parseAuthFetchSpec(map[string]string{
			metaFetchURL:      "https://vendor.test/dl",
			metaFetchFilename: "x-{version}.zip",
			metaFetchFormEnv:  "email=" + url.QueryEscape(name),
		})
		if err == nil {
			t.Errorf("the record naming %q was accepted", name)
			continue
		}
		s076AssertEscaped(t, err, ErrAuthFetchFailed, name)
		if strings.Contains(err.Error(), strconv.Quote(authFetchSecretPrefix+name)) {
			t.Errorf("a name that is not a variable name is told to rename itself to %q: %q", authFetchSecretPrefix+name, err)
		}
	}
}

func TestS076SerialEnvRefusesNonVariableName(t *testing.T) {
	const name = "BENTOO_FETCH_X\x1b[31m"
	_, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:         "https://vendor.test/dl",
		metaFetchFilename:    "x-{version}.zip",
		metaFetchSerialEnv:   name,
		metaFetchSerialField: "key",
	})
	if err == nil {
		t.Fatalf("the record naming %q was accepted", name)
	}
	s076AssertEscaped(t, err, ErrAuthFetchFailed, name)
}

func TestS076UnknownFetchKeysAreQuoted(t *testing.T) {
	const key = "fetch_x\x1b[2J"
	err := validateMetaFetch("app-misc/x", map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
		key:               "1",
	})
	if err == nil {
		t.Fatalf("the unknown key %q was accepted", key)
	}
	s076AssertEscaped(t, err, ErrUnknownMetaFetchKey, key)
}

func TestS076MissingSecretNameIsQuoted(t *testing.T) {
	withSecretsFile(t, "")
	const name = "BENTOO_FETCH_X\x07"
	_, err := resolveSecret(name)
	if err == nil {
		t.Fatalf("resolveSecret(%q) found a secret", name)
	}
	s076AssertEscaped(t, err, ErrAuthFetchSecretMissing, name)
}

// The converse: a real variable name is accepted and resolved, an unprefixed
// one keeps its rename suggestion, and a missing one still points at the
// secrets files.
func TestS076VariableNamesStillWork(t *testing.T) {
	withSecretsFile(t, "")
	t.Setenv("BENTOO_FETCH_TEST_076_OK9", "value-076")
	spec, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
		metaFetchFormEnv:  "email=BENTOO_FETCH_TEST_076_OK9",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}
	creds, err := spec.resolveCredentials()
	if err != nil || creds.fields.Get("email") != "value-076" {
		t.Fatalf("resolveCredentials: fields=%v err=%v", creds.fields, err)
	}

	_, _, err = parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
		metaFetchFormEnv:  "email=HOME",
	})
	if err == nil || !strings.Contains(err.Error(), `(rename to "BENTOO_FETCH_HOME")`) {
		t.Errorf("an unprefixed name lost its rename suggestion: %v", err)
	}

	_, err = resolveSecret("BENTOO_FETCH_TEST_076_ABSENT")
	if !errors.Is(err, ErrAuthFetchSecretMissing) || !strings.Contains(err.Error(), "BENTOO_FETCH_TEST_076_ABSENT") {
		t.Errorf("a missing variable no longer reports itself: %v", err)
	}
}
