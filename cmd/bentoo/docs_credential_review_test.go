package main

import (
	"regexp"
	"strings"
	"testing"
)

// Story 052, sub-task 5.3 — S052-R8.3 (v2), S052-R8.2: the README states what
// the BENTOO_* binding actually protects. A BENTOO_* variable goes to the host
// of the record's own url or base_url, and the record's author picks that url,
// so the binding does not keep a contributor's host from receiving it — the
// review of the packages.toml change does. The old text claimed the opposite.

// reservedSecretNeedles are the three names S052-R1.9 reserves, as the README
// is expected to spell them.
var reservedSecretNeedles = []struct {
	label string
	re    *regexp.Regexp
}{
	{"BENTOO_NTFY_TOKEN", regexp.MustCompile(`BENTOO_NTFY_TOKEN\b`)},
	{"BENTOO_SMTP_PASSWORD", regexp.MustCompile(`BENTOO_SMTP_PASSWORD\b`)},
	{"BENTOO_REPO_<NAME>_TOKEN", regexp.MustCompile(`BENTOO_REPO_<[A-Za-z]+>_TOKEN`)},
}

// collapseSpace joins s on single spaces, so a sentence hard-wrapped across
// README lines is matched as one string.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func TestREADME_StatesWhatBentooBindingProtects(t *testing.T) {
	readme := readDocSet(t)

	headers := sectionFrom(readme, "### Headers and environment variables")
	if headers == "" {
		t.Fatal(`README has no "### Headers and environment variables" section`)
	}
	flat := collapseSpace(headers)

	// Hostile first: the false guarantee. The old sentence spans a line break,
	// so it is matched after whitespace is collapsed — a raw substring check
	// would pass vacuously.
	t.Run("no claim that an allowed variable cannot reach a contributor's host", func(t *testing.T) {
		if stale := "must not be sendable to a host of that contributor's choosing"; strings.Contains(collapseSpace(readme), stale) {
			t.Errorf("README still claims an allowed variable %q", stale)
		}
		// Any rewording of the same claim. A sentence scoped to the GitHub/GitLab
		// tokens alone is true (they are pinned to vendor hosts) and is allowed.
		claim := regexp.MustCompile(`(?i)\b(must not|cannot|can ?not|can't|never|not)\s+(be\s+)?(sendable|sent|reach|go)\b[^.]*contributor'?s?'? choosing`)
		vendorOnly := regexp.MustCompile(`(?i)github|gitlab`)
		for _, sentence := range regexp.MustCompile(`[.!?]\s`).Split(flat, -1) {
			if claim.MatchString(sentence) && (!vendorOnly.MatchString(sentence) || strings.Contains(sentence, "BENTOO_")) {
				t.Errorf("README claims a variable cannot reach a host the contributor picks: %q", sentence)
			}
		}
		// The BENTOO_* host is chosen by whoever wrote the record, not by the
		// maintainer: "it is your server" states the same false guarantee.
		if strings.Contains(flat, "it is your server") {
			t.Error(`README still says the BENTOO_* host "is your server"`)
		}
	})

	t.Run("states that a BENTOO_* variable follows the record's own url", func(t *testing.T) {
		found := false
		for _, para := range strings.Split(headers, "\n\n") {
			p := collapseSpace(para)
			if strings.Contains(p, "BENTOO_") && strings.Contains(p, "`url`") && strings.Contains(p, "`base_url`") {
				found = true
			}
		}
		if !found {
			t.Error("no paragraph ties BENTOO_* to the record's own `url` / `base_url`")
		}
	})

	t.Run("states that reviewing a packages.toml change means checking that url", func(t *testing.T) {
		review := regexp.MustCompile(`(?i)\breview`)
		found := false
		for _, sentence := range regexp.MustCompile(`[.!?]\s`).Split(flat, -1) {
			if review.MatchString(sentence) && strings.Contains(sentence, "url") &&
				(strings.Contains(sentence, "BENTOO_") || strings.Contains(sentence, "packages.toml")) {
				found = true
			}
		}
		if !found {
			t.Error("the headers section never says that reviewing a packages.toml change means checking the record's url")
		}
	})

	// The reserved names appear elsewhere in the README (the notify section),
	// so the check is scoped to the headers section and to a paragraph that
	// says they are not expanded.
	t.Run("lists bentoolkit's own secrets as never expanded", func(t *testing.T) {
		notExpanded := regexp.MustCompile(`(?i)never\s+(be\s+)?expand|not\s+(be\s+)?expand|literal`)
		for _, n := range reservedSecretNeedles {
			found := false
			for _, para := range strings.Split(headers, "\n\n") {
				p := collapseSpace(para)
				if n.re.MatchString(p) && notExpanded.MatchString(p) {
					found = true
				}
			}
			if !found {
				t.Errorf("the headers section does not state that %s is never expanded", n.label)
			}
		}
	})

	t.Run("CHANGELOG [Unreleased] Security names the reserved names as BREAKING", func(t *testing.T) {
		changelog := readRepoDoc(t, "CHANGELOG.md")
		security := sectionFrom(shippingSection(changelog, "0.32.0"), "### Security")
		if security == "" {
			t.Fatal("CHANGELOG [0.32.0] (or [Unreleased] before the cut) has no ### Security entry")
		}
		bullets := strings.Split(security, "\n- ")
		for _, n := range reservedSecretNeedles {
			found := false
			for _, b := range bullets {
				if n.re.MatchString(b) && strings.Contains(b, "BREAKING") {
					found = true
				}
			}
			if !found {
				t.Errorf("CHANGELOG [0.32.0] Security has no BREAKING bullet naming %s", n.label)
			}
		}
	})
}
