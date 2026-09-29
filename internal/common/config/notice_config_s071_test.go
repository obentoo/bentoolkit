package config

// Story 071, sub-task 1.1: the `notice` configuration section.
//
// R6.1: a config carrying notice.site_path loads without an unknown-key
// warning. R4.6: a site_path that begins with `~/` expands to the home
// directory.
//
// The hostile halves come first. The unknown-key check is a strict re-decode
// into probeConfig, so the key can be silenced two wrong ways: by forgetting
// probeConfig (a false warning on every run), or by making the section so
// lenient that a typo inside it no longer warns. Both are pinned. The `~`
// expansion is pinned against the paths that merely CONTAIN a tilde, which
// must never be rewritten.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNoticeConfigS071(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the config at %s: %v", path, err)
	}
	return path
}

const noticeConfigBaseS071 = "overlay:\n  path: /tmp/overlay\n  remote: origin\n" +
	"git:\n  user: Test\n  email: test@test.com\n"

// Hostile half (would wrongly collapse): a misspelled key inside `notice:`
// must still be reported. A section decoded leniently (a map, an inline
// catch-all) would make the typo and the real key indistinguishable.
func TestNoticeConfig_TypoInsideTheSectionStillWarns(t *testing.T) {
	path := writeNoticeConfigS071(t, noticeConfigBaseS071+"notice:\n  site_pth: /srv/site\n")

	var loadErr error
	var cfg *Config
	stderr := captureStderr(t, func() { cfg, loadErr = LoadFrom(path) })

	if loadErr != nil {
		t.Fatalf("an unknown key under notice: must not be fatal, got %v", loadErr)
	}
	if !strings.Contains(stderr, "site_pth") {
		t.Errorf("the typo notice.site_pth drew no warning naming it; stderr = %q", stderr)
	}
	if cfg != nil && cfg.Notice.SitePath != "" {
		t.Errorf("the misspelled key was applied as site_path = %q", cfg.Notice.SitePath)
	}
}

// Benign half: the real key loads, and draws no warning at all (the strict
// probe must know the section too, not only Config).
func TestNoticeConfig_SitePathLoadsWithoutAWarning(t *testing.T) {
	path := writeNoticeConfigS071(t, noticeConfigBaseS071+"notice:\n  site_path: /srv/site\n")

	var loadErr error
	var cfg *Config
	stderr := captureStderr(t, func() { cfg, loadErr = LoadFrom(path) })

	if loadErr != nil {
		t.Fatalf("LoadFrom: %v", loadErr)
	}
	if stderr != "" {
		t.Errorf("notice.site_path drew a warning (is it missing from probeConfig?): %q", stderr)
	}
	if cfg.Notice.SitePath != "/srv/site" {
		t.Errorf("Notice.SitePath = %q, want %q", cfg.Notice.SitePath, "/srv/site")
	}
}

// Hostile half (would wrongly rewrite): only a LEADING `~/` is the home
// directory. `~other/x` names another user's home, and a tilde anywhere else
// is an ordinary character.
func TestGetNoticeSitePath_OnlyALeadingTildeSlashExpands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, raw := range []string{
		"~other/site",
		"/srv/~/site",
		"site~/x",
		"/srv/site~",
	} {
		cfg := &Config{Notice: NoticeConfig{SitePath: raw}}
		got, err := cfg.GetNoticeSitePath()
		if err != nil {
			t.Errorf("GetNoticeSitePath(%q): unexpected error %v", raw, err)
			continue
		}
		if got != raw {
			t.Errorf("GetNoticeSitePath(%q) = %q; a path not starting with ~/ must be returned unchanged", raw, got)
		}
		if strings.HasPrefix(got, home) {
			t.Errorf("GetNoticeSitePath(%q) = %q was rewritten into the home directory", raw, got)
		}
	}
}

// Benign half: `~/` expands to $HOME, and an unset key stays empty (the
// command then prints the YAML instead of writing it, R4.2).
func TestGetNoticeSitePath_ExpandsHomeAndLeavesUnsetEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{Notice: NoticeConfig{SitePath: "~/Projects/site"}}
	got, err := cfg.GetNoticeSitePath()
	if err != nil {
		t.Fatalf("GetNoticeSitePath: %v", err)
	}
	if want := filepath.Join(home, "Projects", "site"); got != want {
		t.Errorf("GetNoticeSitePath(~/Projects/site) = %q, want %q", got, want)
	}

	abs := &Config{Notice: NoticeConfig{SitePath: "/srv/site"}}
	if got, err := abs.GetNoticeSitePath(); err != nil || got != "/srv/site" {
		t.Errorf("GetNoticeSitePath(/srv/site) = %q, %v; want it unchanged", got, err)
	}

	unset := &Config{}
	if got, err := unset.GetNoticeSitePath(); err != nil || got != "" {
		t.Errorf("GetNoticeSitePath() with no key = %q, %v; want \"\", nil", got, err)
	}
}
