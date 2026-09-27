package overlay

// Authored for story 057, sub-task 1.4 — R4.1, R4.6.
//
// The row's reason (the FindingCompared Detail) must read
// `the upstream lookup failed (<cause>): <error text>`, on one line, uncut.
//
// RED ON ARRIVAL: comparedDetail returns the generic sentence for every
// StatusError (assertion failure, no missing symbol).

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// s057DetailProvider fails each listed atom with its error.
type s057DetailProvider struct{ errs map[string]error }

func (p *s057DetailProvider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	if err, ok := p.errs[category+"/"+pkg]; ok {
		return nil, err
	}
	return nil, provider.ErrNotFound
}
func (p *s057DetailProvider) GetName() string   { return "s057-detail" }
func (p *s057DetailProvider) SupportsAPI() bool { return true }
func (p *s057DetailProvider) Close() error      { return nil }

func s057ComparedDetail(t *testing.T, report *CompareReport, atom string) string {
	t.Helper()
	for _, f := range report.Findings {
		if f.Kind == FindingCompared && f.Atom == atom {
			return f.Detail
		}
	}
	t.Fatalf("no compared finding for %s", atom)
	return ""
}

func TestComparedDetailNamesTheLookupCause(t *testing.T) {
	limited := fmt.Errorf("%w: rate limit resets at 1790000000", provider.ErrRateLimit)
	multiline := errors.New("upstream said:\nline two\r\nline three\rend")
	long := errors.New("start " + strings.Repeat("abcdefghij", 500) + " TAIL-MARKER")
	prov := &s057DetailProvider{errs: map[string]error{
		"cat/limited": limited, "cat/multiline": multiline, "cat/long": long,
	}}
	opts := CompareOptions{IncludeSynced: true, IncludeNotInRemote: true}
	report, err := CompareWithProvider([]PackageInfo{
		{Category: "cat", Package: "limited", LatestVersion: "1.0"},
		{Category: "cat", Package: "multiline", LatestVersion: "1.0"},
		{Category: "cat", Package: "long", LatestVersion: "1.0"},
	}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider: %v", err)
	}

	if got, want := s057ComparedDetail(t, report, "cat/limited"),
		"the upstream lookup failed (rate-limited): API rate limit exceeded: rate limit resets at 1790000000"; got != want {
		t.Errorf("reason\n got: %q\nwant: %q (R4.1)", got, want)
	}

	const prefix = "the upstream lookup failed (other): "
	ml := s057ComparedDetail(t, report, "cat/multiline")
	if strings.ContainsAny(ml, "\n\r") {
		t.Errorf("the reason carries a line break: %q (R4.6)", ml)
	}
	if !strings.HasPrefix(ml, prefix) {
		t.Errorf("reason %q does not start with %q", ml, prefix)
	} else if got, want := strings.Fields(strings.TrimPrefix(ml, prefix)), strings.Fields(multiline.Error()); !reflect.DeepEqual(got, want) {
		t.Errorf("folding lost or reordered words: got %q, want %q", got, want)
	}

	lg := s057ComparedDetail(t, report, "cat/long")
	if lg != prefix+long.Error() {
		t.Errorf("a %d-byte error was not carried whole into the reason (R4.6: only a table cell may cut it); got %d bytes",
			len(long.Error()), len(lg))
	}
}
