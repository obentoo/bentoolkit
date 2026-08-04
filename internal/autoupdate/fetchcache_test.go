package autoupdate

import (
	"strings"
	"testing"
)

// =============================================================================
// Story 024 / task 1.1 — fetch identity (S024-R1.1 … S024-R1.4)
// =============================================================================

// TestBodyKey pins the identity rule the whole deduplication rests on: two
// upstream reads share a body ONLY when the requests they would issue are
// byte-identical — same URL, same declared headers (S024-R1.1).
//
// The interesting half of the table is the NEGATIVE half. A key that collides
// where it must not is not a lost optimisation, it is one entry silently
// receiving another entry's bytes — which is exactly the failure the Range case
// (S024-R1.3) describes: a 206 fragment is indistinguishable from a successful
// full read to every downstream parser.
func TestBodyKey(t *testing.T) {
	const tagsURL = "https://gitlab.example.test/api/v4/projects/1/repository/tags?per_page=20"
	const otherURL = "https://gitlab.example.test/api/v4/projects/2/repository/tags?per_page=20"

	tests := []struct {
		name string
		aURL string
		aHdr map[string]string
		bURL string
		bHdr map[string]string
		same bool
	}{
		{
			name: "same URL, no headers on either side",
			aURL: tagsURL, aHdr: nil,
			bURL: tagsURL, bHdr: nil,
			same: true,
		},
		{
			name: "nil headers and an empty header map are the same request",
			aURL: tagsURL, aHdr: nil,
			bURL: tagsURL, bHdr: map[string]string{},
			same: true,
		},
		{
			name: "identical headers written in a different key order (S024-R1.2)",
			aURL: tagsURL, aHdr: map[string]string{"Accept": "application/json", "X-Trace": "on"},
			bURL: tagsURL, bHdr: map[string]string{"X-Trace": "on", "Accept": "application/json"},
			same: true,
		},
		{
			name: "identical headers with a different header-name casing (S024-R1.2)",
			aURL: tagsURL, aHdr: map[string]string{"accept": "application/json"},
			bURL: tagsURL, bHdr: map[string]string{"Accept": "application/json"},
			same: true,
		},
		{
			name: "header VALUE casing is significant: a value is opaque, not a MIME name",
			aURL: tagsURL, aHdr: map[string]string{"Accept": "Application/JSON"},
			bURL: tagsURL, bHdr: map[string]string{"Accept": "application/json"},
			same: false,
		},
		{
			name: "a differing header value is a different fetch (S024-R1.1)",
			aURL: tagsURL, aHdr: map[string]string{"Authorization": "Bearer aaa"},
			bURL: tagsURL, bHdr: map[string]string{"Authorization": "Bearer bbb"},
			same: false,
		},
		{
			name: "one extra declared header is a different fetch (S024-R1.1)",
			aURL: tagsURL, aHdr: map[string]string{"Accept": "application/json"},
			bURL: tagsURL, bHdr: map[string]string{"Accept": "application/json", "Private-Token": "x"},
			same: false,
		},
		{
			name: "a Range read never shares with a full read (S024-R1.3)",
			aURL: tagsURL, aHdr: map[string]string{"Range": "bytes=0-4095"},
			bURL: tagsURL, bHdr: nil,
			same: false,
		},
		{
			name: "two different Range windows are two different fetches (S024-R1.3)",
			aURL: tagsURL, aHdr: map[string]string{"Range": "bytes=0-4095"},
			bURL: tagsURL, bHdr: map[string]string{"Range": "bytes=4096-8191"},
			same: false,
		},
		{
			name: "a different URL with identical headers is a different fetch",
			aURL: tagsURL, aHdr: map[string]string{"Accept": "application/json"},
			bURL: otherURL, bHdr: map[string]string{"Accept": "application/json"},
			same: false,
		},
		{
			name: "URLs differing only in the query string are different fetches",
			aURL: tagsURL, aHdr: nil,
			bURL: tagsURL + "&page=2", bHdr: nil,
			same: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := bodyKey(tt.aURL, tt.aHdr)
			b := bodyKey(tt.bURL, tt.bHdr)
			if tt.same && a != b {
				t.Errorf("bodyKey produced two keys for what is one fetch:\n a = %q\n b = %q", a, b)
			}
			if !tt.same && a == b {
				t.Errorf("bodyKey collapsed two DIFFERENT fetches onto one key %q — "+
					"one entry would receive another entry's bytes", a)
			}
		})
	}

	// Identity must be stable across calls. Go randomises map iteration order,
	// so a key built by ranging over the map without sorting passes once and
	// fails later; repeating the call is what catches it.
	t.Run("the key is stable across repeated calls on the same map", func(t *testing.T) {
		hdr := map[string]string{
			"Accept": "application/json", "X-A": "1", "X-B": "2", "X-C": "3",
			"Private-Token": "tok", "User-Agent": "bentoo", "X-D": "4",
		}
		want := bodyKey(tagsURL, hdr)
		for i := 0; i < 50; i++ {
			if got := bodyKey(tagsURL, hdr); got != want {
				t.Fatalf("bodyKey is not deterministic: call %d gave %q, first call gave %q", i, got, want)
			}
		}
	})

	// S024-R1.4: identity is derived from the URL about to be requested, so the
	// URL is carried in the key verbatim — no record has to declare that it
	// shares a source with another record.
	t.Run("the key is derived from the URL about to be requested (S024-R1.4)", func(t *testing.T) {
		key := bodyKey(tagsURL, map[string]string{"Accept": "application/json"})
		if !strings.HasPrefix(key, tagsURL+"\x00") {
			t.Errorf("key %q does not start with the raw URL and the NUL separator; "+
				"identity must come from the URL itself", key)
		}
	})

	// A key is DEBUG-loggable, so the header term is hashed rather than inlined:
	// a registry that one day writes a literal credential instead of a ${VAR}
	// reference must not put it in a log line.
	t.Run("a header value never appears verbatim in the key", func(t *testing.T) {
		const secret = "glpat-SUPER-SECRET-VALUE"
		key := bodyKey(tagsURL, map[string]string{"Private-Token": secret})
		if strings.Contains(key, secret) {
			t.Errorf("the header value leaked into the cache key: %q", key)
		}
	})
}
