package logging

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// mask is what a redacted value is replaced with, the same mask secrets.Scrub
// writes.
const mask = "***"

// credentialWords are the trailing key segments that mark an attribute's value
// as a credential whatever the value is.
var credentialWords = []string{"token", "password", "secret", "api_key", "apikey", "authorization"}

// redactingHandler scrubs every resolved secret from a record before handing it
// to next, and masks the value of every credential-named key.
//
// Bound attributes and groups are kept here, never forwarded to
// next.WithAttrs/WithGroup: a value bound before a secret was resolved must
// still be scrubbed once it is, so everything is redacted at Handle time with
// that record's secret list.
type redactingHandler struct {
	next     slog.Handler
	resolved func() []string
	goas     []groupOrAttrs
}

// groupOrAttrs is one With or WithGroup call, in the order it was made.
type groupOrAttrs struct {
	group string
	attrs []slog.Attr
}

// NewRedactingHandler wraps next so that every value resolved returns is
// replaced with "***" in the message and in every attribute value — string,
// error, any other kind rendered as text, at any group depth, including
// attributes bound with With/WithGroup — and the value of every
// credential-named key is written as "***". resolved is called once per record,
// so a secret resolved after the handler was built is still redacted. A nil
// resolved means no resolved values.
func NewRedactingHandler(next slog.Handler, resolved func() []string) slog.Handler {
	if resolved == nil {
		resolved = func() []string { return nil }
	}
	return &redactingHandler{next: next, resolved: resolved}
}

// Enabled reports the wrapped handler's level gate.
func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// WithAttrs records attrs to be redacted and folded into every later record.
func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return h.with(groupOrAttrs{attrs: slices.Clone(attrs)})
}

// WithGroup records a group that qualifies every later attribute.
func (h *redactingHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return h.with(groupOrAttrs{group: name})
}

func (h *redactingHandler) with(g groupOrAttrs) *redactingHandler {
	h2 := *h
	h2.goas = append(slices.Clip(h.goas), g)
	return &h2
}

// Handle rebuilds r with its message and every attribute — its own and the
// bound ones — redacted, and passes it to next, returning next's error.
func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	s := newScrubber(h.resolved())

	var own []slog.Attr
	r.Attrs(func(a slog.Attr) bool {
		own = append(own, s.attr(a))
		return true
	})

	// Fold the bound attributes and groups around the record's own, innermost
	// first: a group wraps everything that followed it, attributes bound
	// before a group sit beside it.
	attrs := own
	for _, g := range slices.Backward(h.goas) {
		if g.group != "" {
			if len(attrs) == 0 {
				continue
			}
			attrs = []slog.Attr{{Key: g.group, Value: slog.GroupValue(attrs...)}}
			continue
		}
		bound := make([]slog.Attr, 0, len(g.attrs)+len(attrs))
		for _, a := range g.attrs {
			bound = append(bound, s.attr(a))
		}
		attrs = append(bound, attrs...)
	}

	out := slog.NewRecord(r.Time, r.Level, s.text(r.Message), r.PC)
	out.AddAttrs(attrs...)
	return h.next.Handle(ctx, out)
}

// scrubber holds one record's secret list, longest first.
type scrubber struct {
	secrets []string
}

func newScrubber(resolved []string) scrubber {
	var secrets []string
	for _, v := range resolved {
		// An empty value would insert the mask between every rune.
		if strings.TrimSpace(v) != "" {
			secrets = append(secrets, v)
		}
	}
	// Longest first, whatever order the caller gave: a shorter secret inside a
	// longer one must not be replaced first and leave the longer one's
	// fragments behind.
	slices.SortStableFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	return scrubber{secrets: secrets}
}

// text replaces every secret in s with the mask.
func (s scrubber) text(v string) string {
	for _, secret := range s.secrets {
		v = strings.ReplaceAll(v, secret, mask)
	}
	return v
}

// attr returns a redacted copy of a.
func (s scrubber) attr(a slog.Attr) slog.Attr {
	if isCredentialKey(a.Key) {
		return slog.String(a.Key, mask)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		group := v.Group()
		redacted := make([]slog.Attr, len(group))
		for i, ga := range group {
			redacted[i] = s.attr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redacted...)}
	case slog.KindString:
		return slog.String(a.Key, s.text(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, s.text(err.Error()))
		}
		rendered := fmt.Sprint(v.Any())
		if scrubbed := s.text(rendered); scrubbed != rendered {
			return slog.String(a.Key, scrubbed)
		}
		return slog.Attr{Key: a.Key, Value: v}
	default:
		// Numbers, bools, times and durations keep their kind.
		return slog.Attr{Key: a.Key, Value: v}
	}
}

// isCredentialKey reports whether key, lower-cased with '-' and '.' read as
// '_', equals one of the credential words or ends with '_' followed by one.
func isCredentialKey(key string) bool {
	k := strings.Map(func(r rune) rune {
		if r == '-' || r == '.' {
			return '_'
		}
		return r
	}, strings.ToLower(key))
	for _, w := range credentialWords {
		if k == w || strings.HasSuffix(k, "_"+w) {
			return true
		}
	}
	return false
}
