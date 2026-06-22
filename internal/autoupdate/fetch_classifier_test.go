package autoupdate

// Story 011 — Phase-3 authored (RED-first) tests for the fetch classifier:
//   3.1 verdict types + FetchClassifier interface (zero-value ⇒ fail-open)
//   3.2 manifest-output availability parser (table-driven)
//   3.3 httpFetchClassifier.Classify (availability + GitHub discovery)
//
// Target: internal/autoupdate/fetch_classifier_test.go. RED until the executor
// adds fetch_classifier.go (FetchVerdictKind/FetchVerdict/FetchClassifyRequest,
// the FetchClassifier interface, the output parser, and httpFetchClassifier with
// an injectable asset-lister). Go compile error "undefined: X" is valid RED.
//
// Behavior-level: assertions pin the verdict KIND and the discovered/current URLs,
// not internal parser shapes or constructor signatures. The classifier never makes
// a real network call — GitHub discovery is exercised through an injected fake
// asset lister (matching the real GetReleaseAssets shape: name + download URL).

import (
	"context"
	"strings"
	"testing"
)

// =============================================================================
// 3.1 — verdict zero-value (Unit) — AD3 fail-open by construction
// =============================================================================

func TestFetchVerdict_ZeroValueIsInconclusive(t *testing.T) {
	var v FetchVerdict
	if v.Kind != FetchInconclusive {
		t.Errorf("zero-value FetchVerdict.Kind = %v, want FetchInconclusive (AD3 fail-open)", v.Kind)
	}
}

// =============================================================================
// 3.2 — manifest-output availability parser (Unit, table-driven)
//        — R2.1/R2.2/R2.4, design AD1
// =============================================================================
//
// The executor exposes the parser as a package-level helper. We pin behavior via
// the publicly observable contract: a helper that, given the manifest Output:,
// reports the availability signal. We assert through the three-state outcome:
//   gone         → all recognized URLs are explicit not-found, none transient/unknown
//   inconclusive → any transient/unknown token, a 200/partial, or zero parseable URLs
//
// Because the exact helper name is part of the executor's audited surface, this
// test calls `classifyAvailability(output) availability` and compares against the
// exported availability constants. Drift in the helper name is absorbed by the
// surface exception; the three-state behavior is the load-bearing contract.

func TestClassifyAvailability_Table(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   availability
	}{
		{
			name: "all 404 wget/ftp dump → gone",
			output: `Resolving example.com... 1.2.3.4
HTTP request sent, awaiting response... 404 Not Found
ftp://ftp.example.com/pub/foo-1.0.tar.gz: No such file or directory
 * failed fetching required distfiles`,
			want: availabilityGone,
		},
		{
			name: "explicit 410 Gone → gone",
			output: `https://example.com/foo-1.0.tar.gz
HTTP request sent, awaiting response... 410 Gone`,
			want: availabilityGone,
		},
		{
			name:   "timed out → inconclusive",
			output: "Connecting to example.com... failed: Connection timed out.",
			want:   availabilityInconclusive,
		},
		{
			name:   "unable to resolve → inconclusive",
			output: "wget: unable to resolve host address 'example.com'",
			want:   availabilityInconclusive,
		},
		{
			name:   "connection refused → inconclusive",
			output: "Connecting to example.com|1.2.3.4|:443... failed: Connection refused.",
			want:   availabilityInconclusive,
		},
		{
			name:   "5xx → inconclusive",
			output: "HTTP request sent, awaiting response... 503 Service Unavailable",
			want:   availabilityInconclusive,
		},
		{
			name:   "200/partial success → inconclusive",
			output: "HTTP request sent, awaiting response... 200 OK\nLength: 1234 (1.2K)",
			want:   availabilityInconclusive,
		},
		{
			name:   "unrecognized format → inconclusive",
			output: "some fetcher we have never seen emitting opaque progress",
			want:   availabilityInconclusive,
		},
		{
			name:   "empty → inconclusive",
			output: "",
			want:   availabilityInconclusive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAvailability(tc.output); got != tc.want {
				t.Errorf("classifyAvailability() = %v, want %v\noutput:\n%s", got, tc.want, tc.output)
			}
		})
	}
}

// =============================================================================
// 3.3 — httpFetchClassifier.Classify (Integration: fake asset lister) — R2.x
// =============================================================================

// fakeAssetLister records its inputs and returns canned assets/err, standing in
// for the GitHub GetReleaseAssets dependency so no network call is made. Its
// signature mirrors the discovery dependency: (owner, repo, tag) → assets, error.
// ReleaseAsset is the github.ReleaseAsset shape (Name + DownloadURL); the
// classifier maps it internally. We model it with the fields the classifier reads.
type fakeAsset struct {
	name string
	url  string
}

// newClassifierWithAssets constructs an httpFetchClassifier whose GitHub discovery
// is backed by the supplied fake lister. The exact constructor/injection point is
// part of the executor's audited surface; this test pins the BEHAVIOR (verdict +
// URLs) reachable through Classify.
func newClassifierWithAssets(t *testing.T, assets []fakeAsset, listErr error) FetchClassifier {
	t.Helper()
	lister := func(_ context.Context, owner, repo, tag string) ([]fetchReleaseAsset, error) {
		if listErr != nil {
			return nil, listErr
		}
		out := make([]fetchReleaseAsset, 0, len(assets))
		for _, a := range assets {
			out = append(out, fetchReleaseAsset{Name: a.name, DownloadURL: a.url})
		}
		return out, nil
	}
	return newHTTPFetchClassifierWithLister(lister)
}

// goneGitHubOutput is a manifest output where the only recognized URL is a GitHub
// release asset that 404s → availability gone, GitHub discovery applies.
const goneGitHubOutput = `Resolving github.com... 1.2.3.4
https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz
HTTP request sent, awaiting response... 404 Not Found
 * failed fetching required distfiles`

// goneNonGitHubOutput is an all-404 output for a non-GitHub (mirror) distfile.
const goneNonGitHubOutput = `ftp://ftp.imagemagick.org/pub/ImageMagick/imagemagick-7.1.2.26.tar.xz: No such file or directory
HTTP request sent, awaiting response... 404 Not Found
 * failed fetching required distfiles`

func reqFor(output string) FetchClassifyRequest {
	return FetchClassifyRequest{
		Package:       "dev-games/godot",
		Version:       "4.7",
		EbuildPath:    "/overlay/dev-games/godot/godot-4.7.ebuild",
		ManifestError: "command failed: exit status 1\nOutput: " + output,
	}
}

func TestClassify_Gone_NonGitHub_Irreparable(t *testing.T) {
	c := newClassifierWithAssets(t, nil, nil)
	v := c.Classify(context.Background(), reqFor(goneNonGitHubOutput))
	if v.Kind != FetchIrreparable {
		t.Errorf("gone + non-GitHub SRC_URI → want FetchIrreparable, got %v", v.Kind)
	}
}

func TestClassify_Gone_GitHub_RenamedAsset_Recoverable(t *testing.T) {
	found := "https://github.com/godotengine/godot/releases/download/4.7/godot-4.7.tar.gz"
	c := newClassifierWithAssets(t, []fakeAsset{
		{name: "godot-4.7.tar.gz", url: found},
		{name: "checksums.txt", url: "https://github.com/godotengine/godot/releases/download/4.7/checksums.txt"},
	}, nil)

	v := c.Classify(context.Background(), reqFor(goneGitHubOutput))
	if v.Kind != FetchRecoverable {
		t.Fatalf("gone + GitHub + compatible asset → want FetchRecoverable, got %v", v.Kind)
	}
	if v.DiscoveredURL != found {
		t.Errorf("DiscoveredURL = %q, want %q", v.DiscoveredURL, found)
	}
	if !strings.Contains(v.CurrentURL, "old.tar.gz") {
		t.Errorf("CurrentURL = %q, want the failing current URL (old.tar.gz)", v.CurrentURL)
	}
}

func TestClassify_Gone_GitHub_NoCompatibleAsset_Irreparable(t *testing.T) {
	// Tag exists but holds only an incompatible asset (different archive type).
	c := newClassifierWithAssets(t, []fakeAsset{
		{name: "source-code.zip", url: "https://github.com/godotengine/godot/releases/download/4.7/source-code.zip"},
	}, nil)
	v := c.Classify(context.Background(), reqFor(goneGitHubOutput))
	if v.Kind != FetchIrreparable {
		t.Errorf("gone + GitHub + no compatible asset → want FetchIrreparable, got %v", v.Kind)
	}
}

func TestClassify_Gone_GitHub_TagNotFound_Irreparable(t *testing.T) {
	// Tag/repo 404 from discovery → Irreparable (the tag truly does not exist).
	c := newClassifierWithAssets(t, nil, errTagNotFound)
	v := c.Classify(context.Background(), reqFor(goneGitHubOutput))
	if v.Kind != FetchIrreparable {
		t.Errorf("gone + GitHub + tag 404 → want FetchIrreparable, got %v", v.Kind)
	}
}

func TestClassify_DiscoveryError_Inconclusive(t *testing.T) {
	// A non-404 listing error (rate-limit/network/auth) → fail-open Inconclusive.
	c := newClassifierWithAssets(t, nil, errDiscoveryUnavailable)
	v := c.Classify(context.Background(), reqFor(goneGitHubOutput))
	if v.Kind != FetchInconclusive {
		t.Errorf("gone + GitHub + discovery error → want FetchInconclusive (fail-open), got %v", v.Kind)
	}
}

func TestClassify_TransientAvailability_Inconclusive(t *testing.T) {
	transient := "Connecting to github.com... failed: Connection timed out."
	c := newClassifierWithAssets(t, nil, nil)
	v := c.Classify(context.Background(), reqFor(transient))
	if v.Kind != FetchInconclusive {
		t.Errorf("transient availability → want FetchInconclusive, got %v", v.Kind)
	}
}

// TestClassify_CancelledContext_Inconclusive covers R2.5/AD3: a cancelled ctx
// folds to Inconclusive (never a non-Inconclusive verdict).
func TestClassify_CancelledContext_Inconclusive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Classify runs
	c := newClassifierWithAssets(t, []fakeAsset{
		{name: "godot-4.7.tar.gz", url: "https://github.com/godotengine/godot/releases/download/4.7/godot-4.7.tar.gz"},
	}, nil)
	v := c.Classify(ctx, reqFor(goneGitHubOutput))
	if v.Kind != FetchInconclusive {
		t.Errorf("cancelled ctx must yield FetchInconclusive (R2.5), got %v", v.Kind)
	}
}
