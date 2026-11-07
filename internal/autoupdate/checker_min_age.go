package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/jsonpath"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// minAgeVersionPlaceholder is replaced, inside published_path, by the candidate
// version. It is substituted per segment AFTER the path is split, so a version
// holding dots ("2.1.310") stays one member name instead of becoming three.
const minAgeVersionPlaceholder = "{version}"

// WithNowFunc sets the clock the min_age_days gate measures a release's age
// against. Tests pin it so the seven-day arithmetic is exact.
func WithNowFunc(fn func() time.Time) CheckerOption {
	return func(c *Checker) error {
		c.nowFunc = fn
		return nil
	}
}

// now is the gate's clock: the injected one, or the wall clock.
func (c *Checker) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// minAgeHold decides whether a record's min_age_days gate holds version, and
// returns the report line when it does ("" when the candidate may proceed, or
// the record sets no gate).
//
// The publication time is read from the record's url — the response the
// version came from, and the per-run body cache answers it without a second
// request on a fresh check. On a version-cache hit it is fetched again, which
// is what keeps a held candidate held on every later run until it is old
// enough.
//
// Any doubt holds: a time that is missing, not a string, or not RFC 3339 is an
// age nobody can show to be old enough, and a release published in the future
// (clock skew) has a negative age. Only the younger version is ever held; the
// gate never steps down to an older release that would pass, so the current
// version stays until the latest one ages in.
func (c *Checker) minAgeHold(ctx context.Context, cfg *registry.PackageConfig, version string) string {
	if cfg.MinAgeDays <= 0 || cfg.PublishedPath == "" {
		return ""
	}
	published, err := c.publishedAt(ctx, cfg, version)
	if err != nil {
		return fmt.Sprintf("held by min_age_days: %s publication time unreadable at %q: %v", version, cfg.PublishedPath, err)
	}
	minAge := time.Duration(cfg.MinAgeDays) * 24 * time.Hour
	if c.now().Sub(published) >= minAge {
		return ""
	}
	return fmt.Sprintf("held by min_age_days: %s published %s, eligible %s",
		version, published.UTC().Format(time.RFC3339), published.Add(minAge).UTC().Format(time.RFC3339))
}

// publishedAt reads version's publication time at the record's published_path.
func (c *Checker) publishedAt(ctx context.Context, cfg *registry.PackageConfig, version string) (time.Time, error) {
	segments, err := jsonpath.Parse(cfg.PublishedPath)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing published_path: %w", err)
	}
	content, err := c.fetchContent(ctx, cfg.URL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
	if err != nil {
		return time.Time{}, fmt.Errorf("fetching %s: %w", hostForError(cfg.URL), err)
	}
	var doc any
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return time.Time{}, fmt.Errorf("decoding the response: %w", err)
	}

	node := doc
	for _, seg := range segments {
		switch seg.Kind {
		case jsonpath.KindIndex:
			list, ok := node.([]any)
			if !ok || seg.Index >= len(list) {
				return time.Time{}, fmt.Errorf("no element [%d]", seg.Index)
			}
			node = list[seg.Index]
		default:
			name := strings.ReplaceAll(seg.Field, minAgeVersionPlaceholder, version)
			obj, ok := node.(map[string]any)
			if !ok {
				return time.Time{}, fmt.Errorf("no object holding %q", name)
			}
			if node, ok = obj[name]; !ok {
				return time.Time{}, fmt.Errorf("no member %q", name)
			}
		}
	}

	text, ok := node.(string)
	if !ok {
		return time.Time{}, fmt.Errorf("the value is %T, not an RFC 3339 string", node)
	}
	published, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing %q as RFC 3339: %w", text, err)
	}
	return published, nil
}
