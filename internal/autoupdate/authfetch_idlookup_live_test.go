package autoupdate

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestResolveEndpointIDLiveBlackmagic runs the id lookup against Blackmagic's
// real download catalogue — the case fetch_id_url/fetch_id_pattern were written
// for, and the one whose shape no local fixture can keep honest.
//
// It is gated because it reaches the network, following
// TestFetchDistfileLiveFileZillaPro next door:
//
//	BLACKMAGIC_ID_E2E=1
//
// It is READ-ONLY and carries no personal data: the catalogue is a public GET.
// The registration POST that turns an id into a download URL is deliberately
// NOT exercised here — that one submits a person's name, address and telephone
// number to the vendor, which is not something a test suite may do on its own.
//
// What it proves is the half that silently rots: that the pattern still finds
// the per-platform id in the document the vendor publishes today. Measured
// 2026-09-19, DaVinci Resolve 21.1 answers 9ecf221a3a1f47cca35a4e4c34133865 for
// Linux — note that this is NOT the id in the download page's own URL, which is
// the release id (59dd4eef1f4941c29fb8dc48b33f5c87) and would post to the wrong
// endpoint.
func TestResolveEndpointIDLiveBlackmagic(t *testing.T) {
	if os.Getenv("BLACKMAGIC_ID_E2E") != "1" {
		t.Skip("set BLACKMAGIC_ID_E2E=1 to run the live Blackmagic id lookup (read-only, public endpoint)")
	}

	const (
		version = "21.1"
		wantID  = "9ecf221a3a1f47cca35a4e4c34133865"
	)

	spec, ok, err := parseAuthFetchSpec(map[string]string{
		metaFetchURL:      "https://www.blackmagicdesign.com/api/register/us/download/{id}",
		metaFetchBody:     fetchBodyJSON,
		metaFetchResponse: fetchResponseURL,
		metaFetchFilename: "DaVinci_Resolve_{version}_Linux.zip",
		metaFetchForm:     "product=DaVinci Resolve&platform=Linux&policy=true&origin=www.blackmagicdesign.com",
		metaFetchIDURL:    "https://www.blackmagicdesign.com/api/support/us/downloads.json",
		// Anchored on the platform and on the release title at once: the
		// catalogue carries one entry per platform per release, and four
		// platforms share every downloadTitle.
		metaFetchIDPattern:   `"Linux":\[\{"releaseId":"[0-9a-f]+","downloadId":"([0-9a-f]+)","downloadTitle":"DaVinci Resolve {version}"`,
		metaFetchContentType: "application/zip",
	})
	if err != nil || !ok {
		t.Fatalf("parseAuthFetchSpec: ok=%v err=%v", ok, err)
	}

	got, err := spec.resolveEndpointID(context.Background(), version)
	if err != nil {
		t.Fatalf("resolveEndpointID: %v", err)
	}
	if got != wantID {
		t.Errorf("resolved id = %q, want %q.\n"+
			"A different id is not necessarily a bug: the vendor re-mints it per release, and %s may have been superseded.\n"+
			"Check the catalogue before changing this constant — an id that no longer matches the version is exactly what this lookup exists to prevent.",
			got, wantID, version)
	}

	// The decoy this guards against has no local equivalent: an unquoted "21.1"
	// is a regex matching "2101", and a catalogue with 1200 entries is a good
	// place for one to hide.
	if strings.ContainsAny(got, "^$.*+?()[]{}|\\") {
		t.Errorf("the captured id %q contains regex metacharacters, which an id never does", got)
	}
}
