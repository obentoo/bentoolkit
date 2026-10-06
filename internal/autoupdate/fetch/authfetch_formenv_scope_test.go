package fetch

import (
	"errors"
	"strings"
	"testing"
)

// Story 068, sub-task 1.2 — R1.2, R1.3, R1.4, R1.5, R1.6: every variable a
// record's fetch_form_env names must be a BENTOO_FETCH_ variable.
//
// fetch_form_env is the serial pair generalised — a field here, a variable
// there, resolved through the same chain and posted to the record's own
// fetch_url — so it is the same leak with more fields. The helpers
// (newS068Server, s068ParseThenFetch, s068ChainCarries, s068Sentinel) live in
// authfetch_serial_scope_test.go (sub-task 1.1).

func TestParseFormEnv_RefusesUnprefixedNames(t *testing.T) {
	// Hostile first: each name, put in the "email" field, must be refused.
	cases := []struct {
		name string
		raw  string // the fetch_form_env value, urlencoded as in the record
		want string // the variable name after decoding and trimming
	}{
		{"a foreign secret", "email=GITHUB_TOKEN", "GITHUB_TOKEN"},
		{"bentoolkit's own smtp password", "email=BENTOO_SMTP_PASSWORD", "BENTOO_SMTP_PASSWORD"},
		{"lower-case prefix", "email=bentoo_fetch_email", "bentoo_fetch_email"},
		{"prefix without its underscore", "email=BENTOO_FETCHEMAIL", "BENTOO_FETCHEMAIL"},
		{"bare prefix, nothing after it", "email=BENTOO_FETCH_", "BENTOO_FETCH_"},
		{"foreign secret padded with whitespace", "email=+GITHUB_TOKEN%09", "GITHUB_TOKEN"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newS068Server(t)
			meta := map[string]string{
				MetaFetchURL:      srv.URL + "/download",
				MetaFetchFilename: "leak-{version}.tar.gz",
				metaFetchFormEnv:  tc.raw,
			}

			withSecretsFile(t, "")
			t.Setenv(tc.want, "")
			unsetParse, unsetFetch := s068ParseThenFetch(t, meta)

			withSecretsFile(t, tc.want+"="+s068Sentinel+"\n")
			t.Setenv(tc.want, s068Sentinel)
			setParse, setFetch := s068ParseThenFetch(t, meta)

			if got := srv.received(); len(got) != 0 {
				t.Errorf("the endpoint received %d request(s) for fetch_form_env = %q; want none. Forms: %v", len(got), tc.raw, got)
			}
			for _, e := range []error{unsetFetch, setFetch} {
				if leaked, ok := s068ChainCarries(e, s068Sentinel); ok {
					t.Errorf("a fetch error carries the resolved value: %q", leaked)
				}
			}
			for _, run := range []struct {
				label string
				err   error
			}{{"unset", unsetParse}, {"set", setParse}} {
				err := run.err
				if err == nil {
					t.Errorf("[%s] parseAuthFetchSpec accepted fetch_form_env = %q; want a refusal naming BENTOO_FETCH_%s", run.label, tc.raw, tc.want)
					continue
				}
				if !errors.Is(err, ErrAuthFetchFailed) {
					t.Errorf("[%s] err = %v; want errors.Is(ErrAuthFetchFailed)", run.label, err)
				}
				if errors.Is(err, ErrAuthFetchSecretMissing) {
					t.Errorf("[%s] err = %v is ErrAuthFetchSecretMissing: the refusal must come from the name, before any lookup", run.label, err)
				}
				msg := err.Error()
				for _, needle := range []string{tc.want, "email", metaFetchFormEnv, "BENTOO_FETCH_" + tc.want} {
					if !strings.Contains(msg, needle) {
						t.Errorf("[%s] refusal %q does not name %q", run.label, msg, needle)
					}
				}
				if leaked, ok := s068ChainCarries(err, s068Sentinel); ok {
					t.Errorf("[%s] the refusal carries the variable's value: %q", run.label, leaked)
				}
			}
			if unsetParse != nil && setParse != nil && unsetParse.Error() != setParse.Error() {
				t.Errorf("the refusal depends on whether the variable is set:\n  unset: %q\n  set:   %q", unsetParse, setParse)
			}
		})
	}

	// One bad field refuses the whole record, even beside good ones and a good
	// serial: the good values must not go out either.
	t.Run("one bad field beside good ones refuses the record", func(t *testing.T) {
		srv := newS068Server(t)
		withSecretsFile(t, "")
		t.Setenv("BENTOO_FETCH_SERIAL_068", "good-serial-068")
		t.Setenv("BENTOO_FETCH_NAME_068", "good-name-068")
		t.Setenv("GITHUB_TOKEN", s068Sentinel)

		parseErr, fetchErr := s068ParseThenFetch(t, map[string]string{
			MetaFetchURL:         srv.URL + "/download",
			MetaFetchFilename:    "leak-{version}.tar.gz",
			MetaFetchSerialEnv:   "BENTOO_FETCH_SERIAL_068",
			MetaFetchSerialField: "key",
			metaFetchFormEnv:     "name=BENTOO_FETCH_NAME_068&email=GITHUB_TOKEN",
		})
		if parseErr == nil || !errors.Is(parseErr, ErrAuthFetchFailed) {
			t.Errorf("parse err = %v; want a refusal wrapping ErrAuthFetchFailed", parseErr)
		} else if !strings.Contains(parseErr.Error(), "BENTOO_FETCH_GITHUB_TOKEN") {
			t.Errorf("refusal %q does not name BENTOO_FETCH_GITHUB_TOKEN", parseErr)
		}
		if leaked, ok := s068ChainCarries(fetchErr, s068Sentinel); ok {
			t.Errorf("a fetch error carries the resolved value: %q", leaked)
		}
		if got := srv.received(); len(got) != 0 {
			t.Errorf("the endpoint received %d request(s); want none. Forms: %v", len(got), got)
		}
	})

	// Converse (R1.5): prefixed names — padded, or only in the secrets file —
	// resolve and are sent exactly as before.
	t.Run("prefixed names resolve and are sent", func(t *testing.T) {
		srv := newS068Server(t)
		withSecretsFile(t, "BENTOO_FETCH_BMD_EMAIL=someone@example.org\n")
		t.Setenv("BENTOO_FETCH_BMD_EMAIL", "")
		t.Setenv("BENTOO_FETCH_BMD_FIRSTNAME", "Ada")

		parseErr, fetchErr := s068ParseThenFetch(t, map[string]string{
			MetaFetchURL:      srv.URL + "/download",
			MetaFetchFilename: "ok-{version}.tar.gz",
			metaFetchFormEnv:  "firstname=+BENTOO_FETCH_BMD_FIRSTNAME+&email=BENTOO_FETCH_BMD_EMAIL",
		})
		if parseErr != nil || fetchErr != nil {
			t.Fatalf("a record naming only BENTOO_FETCH_ variables failed: parse=%v fetch=%v", parseErr, fetchErr)
		}
		got := srv.received()
		if len(got) != 1 {
			t.Fatalf("the endpoint received %d requests, want 1", len(got))
		}
		if got[0].Get("firstname") != "Ada" || got[0].Get("email") != "someone@example.org" {
			t.Errorf("form = %v; want firstname=Ada, email=someone@example.org", got[0])
		}
	})
}

// TestParseFormEnv_ListsEveryOffenderInFieldOrder pins R1.2's "every such
// variable ... in field order": the operator fixes the record in one pass, and
// the text is stable across runs even though url.Values is a map.
func TestParseFormEnv_ListsEveryOffenderInFieldOrder(t *testing.T) {
	withSecretsFile(t, "")
	// Field names are chosen so none is a substring of a variable name, which
	// keeps the per-offender grouping check below honest.
	meta := map[string]string{
		MetaFetchURL:      "http://127.0.0.1:1/download",
		MetaFetchFilename: "leak-{version}.tar.gz",
		metaFetchFormEnv:  "zip=BENTOO_FETCHX&email=GITHUB_TOKEN&name=BENTOO_FETCH_NAME&town=bentoo_fetch_x",
	}
	// Sorted by field: email, town, zip. "name" is correctly prefixed.
	offenders := []struct{ field, variable string }{
		{"email", "GITHUB_TOKEN"},
		{"town", "bentoo_fetch_x"},
		{"zip", "BENTOO_FETCHX"},
	}

	_, _, err := ParseAuthFetchSpec(meta)
	if err == nil {
		t.Fatalf("parseAuthFetchSpec accepted fetch_form_env = %q; want a refusal naming every unprefixed variable", meta[metaFetchFormEnv])
	}
	if !errors.Is(err, ErrAuthFetchFailed) {
		t.Errorf("err = %v; want errors.Is(ErrAuthFetchFailed)", err)
	}
	msg := err.Error()

	// Stable text: map iteration order must not decide it.
	for i := 0; i < 40; i++ {
		_, _, again := ParseAuthFetchSpec(meta)
		if again == nil || again.Error() != msg {
			t.Fatalf("parse #%d gave a different error:\n  first: %q\n  now:   %q", i+2, msg, again)
		}
	}

	if strings.Contains(msg, "BENTOO_FETCH_BENTOO_FETCH_NAME") {
		t.Errorf("the correctly prefixed field was flagged: %q", msg)
	}

	// Each offender's replacement appears, in field order.
	starts := make([]int, len(offenders))
	from := 0
	for i, o := range offenders {
		replacement := "BENTOO_FETCH_" + o.variable
		idx := strings.Index(msg[from:], replacement)
		if idx < 0 {
			t.Fatalf("refusal %q does not name %s (field %q) after the offenders before it — every offender, in field order", msg, replacement, o.field)
		}
		starts[i] = from + idx
		from = starts[i] + len(replacement)
	}

	// And each is named with its field and its own variable: the text between
	// the previous offender's replacement and the next one's must hold both,
	// the variable outside its replacement. Word order inside one entry is free.
	for i, o := range offenders {
		lo := 0
		if i > 0 {
			lo = starts[i-1] + len("BENTOO_FETCH_"+offenders[i-1].variable)
		}
		hi := len(msg)
		if i+1 < len(offenders) {
			hi = starts[i+1]
		}
		window := strings.Replace(msg[lo:hi], "BENTOO_FETCH_"+o.variable, "", 1)
		if !strings.Contains(window, o.field) || !strings.Contains(window, o.variable) {
			t.Errorf("the entry for field %q does not name both the field and %s: %q", o.field, o.variable, msg[lo:hi])
		}
	}
}
