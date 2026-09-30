package autoupdate

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// Story 075: a record's variable name is attacker-shaped text — fetch_form_env
// goes through url.ParseQuery, so %0A or %1B decode to raw control bytes — and
// the BENTOO_FETCH_ refusals print it into --lint, the sweep report and
// `bentoo distfile`. Every name they print must be quoted and escaped.

// s075ControlBytes are the bytes that forge a report line or drive a terminal.
var s075ControlBytes = []string{"\n", "\r", "\x1b", "\x07"}

func s075AssertQuotedName(t *testing.T, err error, name string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the record naming %q was accepted", name)
	}
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Errorf("err = %v, want it to wrap ErrAuthFetchFailed", err)
	}
	msg := err.Error()
	for _, b := range s075ControlBytes {
		if strings.Contains(msg, b) {
			t.Errorf("the refusal carries the raw control byte %q: %q", b, msg)
		}
	}
	for _, want := range []string{strconv.Quote(name), strconv.Quote(authFetchSecretPrefix + name)} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not print %s: %q", want, msg)
		}
	}
}

func TestS075FormEnvRefusalQuotesHostileNames(t *testing.T) {
	for _, name := range []string{"X\nforged line", "X\x1b[31m"} {
		_, _, err := parseAuthFetchSpec(map[string]string{
			metaFetchURL:      "https://vendor.test/dl",
			metaFetchFilename: "x-{version}.zip",
			metaFetchFormEnv:  "email=" + url.QueryEscape(name),
		})
		s075AssertQuotedName(t, err, name)
	}
}

func TestS075SerialEnvRefusalQuotesHostileName(t *testing.T) {
	const name = "X\x1b]0;t\x07"
	_, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:         "https://vendor.test/dl",
		metaFetchFilename:    "x-{version}.zip",
		metaFetchSerialEnv:   name,
		metaFetchSerialField: "key",
	})
	s075AssertQuotedName(t, err, name)
}

// The converse: the fix changes how names are printed, never which records are
// refused or the order the offenders are listed in.
func TestS075FormEnvOffendersStayInSortedFieldOrder(t *testing.T) {
	_, _, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://vendor.test/dl",
		metaFetchFilename: "x-{version}.zip",
		metaFetchFormEnv:  "zeta=HOME&alpha=PATH",
	})
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Fatalf("err = %v, want ErrAuthFetchFailed", err)
	}
	msg := err.Error()
	alpha, zeta := strings.Index(msg, `field "alpha"`), strings.Index(msg, `field "zeta"`)
	if alpha < 0 || zeta < 0 || alpha > zeta {
		t.Errorf("offenders are not both listed in sorted field order: %q", msg)
	}
}

func TestS075BentooFetchNamesStillParse(t *testing.T) {
	_, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:         "https://vendor.test/dl",
		metaFetchFilename:    "x-{version}.zip",
		metaFetchSerialEnv:   "BENTOO_FETCH_TEST_075_SERIAL",
		metaFetchSerialField: "key",
		metaFetchFormEnv:     "email=BENTOO_FETCH_TEST_075_EMAIL",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}
}
