package logging

// Authored for story 062, sub-task 1.2 (R4.1–R4.7).
//
// Contract, from the sub-task objective:
//
//	func NewRedactingHandler(next slog.Handler, resolved func() []string) slog.Handler
//
// wraps any handler and scrubs every value resolved() returns from the message
// and from every attribute value — string, error, any other kind rendered as
// text, at any group depth, including attributes bound with With/WithGroup —
// and writes `***` for the value of every credential-named key (R4.5).
//
// Every test drives the wrapper over a real slog.JSONHandler writing to a
// buffer, so what is asserted is what a sink would receive. Secrets are fake,
// alphanumeric (so JSON escaping cannot disguise them) and built at run time.
//
// This file shares its package compile with level_test.go (1.3) and
// logger_test.go (1.4); it uses only NewRedactingHandler so it can be
// materialized alone, before either of them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
	"time"
)

// redactFixture is one wrapped logger and the buffer its JSON lines land in.
type redactFixture struct {
	buf    *bytes.Buffer
	logger *slog.Logger
}

// newRedactFixture wraps a debug-level JSONHandler with the redactor.
func newRedactFixture(resolved func() []string) redactFixture {
	buf := &bytes.Buffer{}
	next := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return redactFixture{buf: buf, logger: slog.New(NewRedactingHandler(next, resolved))}
}

// fixed returns a resolved func that always answers values.
func fixed(values ...string) func() []string {
	return func() []string { return append([]string(nil), values...) }
}

// records returns the JSON records written so far, each decoded.
func (f redactFixture) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(f.buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("the wrapped handler wrote a line that is not one JSON object: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

// only returns the single record written so far.
func (f redactFixture) only(t *testing.T) map[string]any {
	t.Helper()
	recs := f.records(t)
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1:\n%s", len(recs), f.buf.String())
	}
	return recs[0]
}

// path walks nested groups: path(rec, "http", "req", "url").
func path(t *testing.T, rec map[string]any, keys ...string) any {
	t.Helper()
	var cur any = rec
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("at %q: not a group (%T) in %v", k, cur, rec)
		}
		cur, ok = m[k]
		if !ok {
			t.Fatalf("key %q absent in %v", strings.Join(keys, "."), rec)
		}
	}
	return cur
}

const (
	fakeSecret      = "s062FakeSecretQ7x9"
	fakeOtherSecret = "s062FakeOtherZ3k1"
)

type fakeStringer struct{ endpoint string }

func (s fakeStringer) String() string { return "endpoint " + s.endpoint }

type fakeRequest struct {
	URL    string
	Header map[string]string
}

type fakeValuer struct{ inner string }

func (v fakeValuer) LogValue() slog.Value { return slog.StringValue("resolved " + v.inner) }

// TestRedactingHandlerScrubsEveryValueKind is R4.1 + R4.2: the secret is
// replaced by `***` in the message and in each kind of attribute value, and no
// byte of it survives anywhere in the line.
func TestRedactingHandlerScrubsEveryValueKind(t *testing.T) {
	f := newRedactFixture(fixed(fakeSecret))

	f.logger.Info("fetch failed for "+fakeSecret,
		slog.String("url", "https://example.invalid/?token="+fakeSecret),
		slog.Any("err", fmt.Errorf("dial https://example.invalid/%s: connection refused", fakeSecret)),
		slog.Any("joined", errors.Join(errors.New("first"), fmt.Errorf("second %s", fakeSecret))),
		slog.Any("stringer", fakeStringer{endpoint: fakeSecret}),
		slog.Any("request", fakeRequest{URL: "https://example.invalid/" + fakeSecret, Header: map[string]string{"X-Probe": fakeSecret}}),
		slog.Any("valuer", fakeValuer{inner: fakeSecret}),
		slog.Group("http", slog.Group("req", slog.String("header", "Bearer "+fakeSecret))),
	)

	line := f.buf.String()
	if strings.Contains(line, fakeSecret) {
		t.Fatalf("the secret survived in the written line:\n%s", line)
	}
	rec := f.only(t)

	for _, tc := range []struct {
		name string
		keys []string
		want string
	}{
		{"message", []string{"msg"}, "fetch failed for ***"},
		{"string attribute", []string{"url"}, "https://example.invalid/?token=***"},
		{"error attribute", []string{"err"}, "dial https://example.invalid/***: connection refused"},
		{"LogValuer attribute", []string{"valuer"}, "resolved ***"},
		{"grouped attribute", []string{"http", "req", "header"}, "Bearer ***"},
	} {
		got := path(t, rec, tc.keys...)
		if got != tc.want {
			t.Errorf("%s: %q = %v, want %q", tc.name, strings.Join(tc.keys, "."), got, tc.want)
		}
	}
	for _, key := range []string{"joined", "stringer", "request"} {
		text := fmt.Sprint(path(t, rec, key))
		if !strings.Contains(text, "***") {
			t.Errorf("attribute %q (rendered %q) carries no `***` where the secret was", key, text)
		}
	}
}

// TestRedactingHandlerScrubsBoundAttributes: attributes bound with With and
// under WithGroup are part of every later line and must be scrubbed too.
func TestRedactingHandlerScrubsBoundAttributes(t *testing.T) {
	f := newRedactFixture(fixed(fakeSecret))

	l := f.logger.With("endpoint", "https://example.invalid/"+fakeSecret).
		WithGroup("provider").
		With(slog.String("auth_header", "Bearer "+fakeSecret))
	l.Warn("request refused", slog.String("detail", "echoed "+fakeSecret))

	if strings.Contains(f.buf.String(), fakeSecret) {
		t.Fatalf("a bound attribute carried the secret into the line:\n%s", f.buf.String())
	}
	rec := f.only(t)
	if got := path(t, rec, "endpoint"); got != "https://example.invalid/***" {
		t.Errorf("With attribute = %v, want %q", got, "https://example.invalid/***")
	}
	if got := path(t, rec, "provider", "auth_header"); got != "Bearer ***" {
		t.Errorf("WithGroup+With attribute = %v, want %q", got, "Bearer ***")
	}
	if got := path(t, rec, "provider", "detail"); got != "echoed ***" {
		t.Errorf("record attribute under WithGroup = %v, want %q", got, "echoed ***")
	}
}

// TestRedactingHandlerRedactsTheLongerOfTwoNestedSecretsWhole is R4.3. The
// shorter secret is handed over FIRST in one case: a redactor that trusts the
// order it is given replaces the short value inside the long one and leaves
// the long one's head and tail in the line.
func TestRedactingHandlerRedactsTheLongerOfTwoNestedSecretsWhole(t *testing.T) {
	short := fakeSecret
	long := "outer-" + fakeSecret + "-tail"

	for _, order := range []struct {
		name     string
		resolved []string
	}{
		{"shorter first", []string{short, long}},
		{"longer first", []string{long, short}},
	} {
		t.Run(order.name, func(t *testing.T) {
			f := newRedactFixture(fixed(order.resolved...))
			f.logger.Info("probe",
				slog.String("both", "long="+long+" short="+short),
				slog.String("long_only", "["+long+"]"),
			)
			rec := f.only(t)
			if got := path(t, rec, "both"); got != "long=*** short=***" {
				t.Errorf("both = %v, want %q", got, "long=*** short=***")
			}
			if got := path(t, rec, "long_only"); got != "[***]" {
				t.Errorf("long_only = %v, want %q — a fragment of the longer secret survived", got, "[***]")
			}
			for _, fragment := range []string{"outer-", "-tail"} {
				if strings.Contains(f.buf.String(), fragment) {
					t.Errorf("fragment %q of the longer secret is in the line:\n%s", fragment, f.buf.String())
				}
			}
		})
	}
}

// TestRedactingHandlerRedactsASecretResolvedAfterItWasBuilt is R4.4: the
// registry is read when a record is handled, not when the handler (or a
// logger derived with With) was built.
func TestRedactingHandlerRedactsASecretResolvedAfterItWasBuilt(t *testing.T) {
	var (
		mu    sync.Mutex
		known []string
	)
	resolved := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), known...)
	}
	f := newRedactFixture(resolved)
	derived := f.logger.With("bound", "carries "+fakeOtherSecret)

	f.logger.Info("before", slog.String("v", fakeOtherSecret))
	if !strings.Contains(f.buf.String(), fakeOtherSecret) {
		t.Fatalf("control: a value that is not yet a secret was altered:\n%s", f.buf.String())
	}
	f.buf.Reset()

	mu.Lock()
	known = append(known, fakeOtherSecret)
	mu.Unlock()

	f.logger.Info("after", slog.String("v", fakeOtherSecret))
	derived.Info("after, from a logger derived before the secret was resolved")
	if strings.Contains(f.buf.String(), fakeOtherSecret) {
		t.Errorf("a secret resolved after the handler was built survived:\n%s", f.buf.String())
	}
	if strings.Count(f.buf.String(), "***") < 2 {
		t.Errorf("want `***` in both lines after the resolution:\n%s", f.buf.String())
	}
}

// TestRedactingHandlerMasksCredentialNamedKeys is R4.5 and R4.6, hostile halves
// first. A key whose last `_`-separated segment is merely CONTAINING a word
// (tokens_used, secret_file) must not be masked; a key spelled with other case,
// `-` or `.` that does end in a credential word must be.
func TestRedactingHandlerMasksCredentialNamedKeys(t *testing.T) {
	// 1. Must NOT fire: the word inside a longer segment, or not at the end.
	for _, key := range []string{
		"tokens_used", "token_count", "tokenizer", "mytoken", "secret_file",
		"secretary", "password_hint", "passwords", "api_keys", "apikeys",
		"authorization_url", "api_key_env",
	} {
		t.Run("keeps/"+key, func(t *testing.T) {
			f := newRedactFixture(fixed(fakeSecret))
			f.logger.Info("probe", slog.String(key, "visible-value"))
			rec := f.only(t)
			if got := path(t, rec, key); got != "visible-value" {
				t.Errorf("%s = %v, want the value unchanged (R4.6)", key, got)
			}
		})
	}

	// R4.6's second clause: a non-credential key still has a resolved secret
	// scrubbed from its value — the key rule does not switch value scrubbing off.
	t.Run("keeps/secret_file with a secret inside", func(t *testing.T) {
		f := newRedactFixture(fixed(fakeSecret))
		f.logger.Info("probe", slog.String("secret_file", "/tmp/"+fakeSecret+"/secrets"))
		if got := path(t, f.only(t), "secret_file"); got != "/tmp/***/secrets" {
			t.Errorf("secret_file = %v, want %q", got, "/tmp/***/secrets")
		}
	})

	// 2. Must fire: every spelling that normalizes to one of the six words.
	for _, key := range []string{
		"GITHUB_TOKEN", "Github-Token", "ntfy.token", "auth.token",
		"smtp_password", "SMTP-Password", "client-secret", "Client.Secret",
		"API-KEY", "openai.api_key", "my_apikey", "Proxy-Authorization",
	} {
		t.Run("masks/"+key, func(t *testing.T) {
			f := newRedactFixture(fixed())
			f.logger.Info("probe", slog.String(key, "not-a-resolved-value"))
			if got := path(t, f.only(t), key); got != "***" {
				t.Errorf("%s = %v, want %q (R4.5)", key, got, "***")
			}
		})
	}

	// 3. Benign: the bare words, any value kind, at any depth.
	for _, key := range []string{"token", "password", "secret", "api_key", "apikey", "authorization"} {
		t.Run("masks/"+key, func(t *testing.T) {
			f := newRedactFixture(fixed())
			f.logger.Info("probe",
				slog.String(key, "plain"),
				slog.Group("nested", slog.Int(key, 12345)),
			)
			rec := f.only(t)
			if got := path(t, rec, key); got != "***" {
				t.Errorf("%s = %v, want %q", key, got, "***")
			}
			if got := path(t, rec, "nested", key); got != "***" {
				t.Errorf("nested.%s = %v, want %q whatever the value kind", key, got, "***")
			}
		})
	}
}

// TestRedactingHandlerLeavesAnInnocentLineByteForByte is R4.7: with no resolved
// secret in it and no credential-named key, the wrapped handler writes exactly
// what the bare handler writes — including numbers staying numbers and objects
// staying objects. The hostile values come first: a secret minus its last
// byte, and the secret in another letter case, are not the secret.
func TestRedactingHandlerLeavesAnInnocentLineByteForByte(t *testing.T) {
	when := time.Date(2026, 9, 28, 12, 0, 0, 123456789, time.UTC)
	attrs := []slog.Attr{
		slog.String("near_miss", fakeSecret[:len(fakeSecret)-1]),
		slog.String("other_case", strings.ToUpper(fakeSecret)),
		slog.String("package", "app-misc/jq"),
		slog.Int("attempts", 3),
		slog.Bool("cached", true),
		slog.Float64("ratio", 0.5),
		slog.Duration("took", 1500*time.Millisecond),
		slog.Any("sizes", map[string]int{"a": 1, "b": 2}),
		slog.Group("upstream", slog.String("host", "example.invalid"), slog.Int("status", 200)),
	}

	render := func(h slog.Handler) {
		r := slog.NewRecord(when, slog.LevelInfo, "fetched upstream", 0)
		r.AddAttrs(attrs...)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatalf("Handle: %v", err)
		}
	}

	var bare, wrapped bytes.Buffer
	render(slog.NewJSONHandler(&bare, nil))
	render(NewRedactingHandler(slog.NewJSONHandler(&wrapped, nil), fixed(fakeSecret, fakeOtherSecret)))

	if wrapped.String() != bare.String() {
		t.Errorf("the redactor changed a line that holds no secret and no credential key\nbare:    %s\nwrapped: %s", bare.String(), wrapped.String())
	}
}

// TestRedactingHandlerDelegatesEnabled: the wrapper reports the level gate of
// the handler it wraps, so a record below the sink's level is never built.
func TestRedactingHandlerDelegatesEnabled(t *testing.T) {
	next := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewRedactingHandler(next, fixed(fakeSecret))
	ctx := context.Background()
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Error("Enabled(Info) = true over a Warn-level handler")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("Enabled(Warn) = false over a Warn-level handler")
	}
}

// TestRedactingHandlerConformsToSlogtest runs the standard library's handler
// conformance suite over the wrapper: groups, empty attributes, inlined empty
// groups and With/WithGroup chains must reach the sink as slog defines them.
func TestRedactingHandlerConformsToSlogtest(t *testing.T) {
	var buf bytes.Buffer
	newHandler := func(*testing.T) slog.Handler {
		buf.Reset()
		return NewRedactingHandler(slog.NewJSONHandler(&buf, nil), fixed("s062-never-in-any-slogtest-line"))
	}
	result := func(t *testing.T) map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("not one JSON object: %v\n%s", err, buf.String())
		}
		return m
	}
	slogtest.Run(t, newHandler, result)
}

// TestRedactingHandlerIsSafeForConcurrentUse: one wrapped logger shared by many
// goroutines (run under -race); every line is whole and scrubbed.
func TestRedactingHandlerIsSafeForConcurrentUse(t *testing.T) {
	var mu sync.Mutex
	known := []string{fakeSecret}
	resolved := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), known...)
	}
	f := newRedactFixture(resolved)
	derived := f.logger.With("bound", fakeSecret)

	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%8 == 0 {
				mu.Lock()
				known = append(known, fmt.Sprintf("s062FakeLate%02d", i))
				mu.Unlock()
			}
			derived.Info("concurrent", slog.Int("i", i), slog.String("v", fakeSecret))
		}(i)
	}
	wg.Wait()

	recs := f.records(t)
	if len(recs) != 32 {
		t.Fatalf("got %d records, want 32", len(recs))
	}
	if strings.Contains(f.buf.String(), fakeSecret) {
		t.Error("the secret survived in a line written concurrently")
	}
}
