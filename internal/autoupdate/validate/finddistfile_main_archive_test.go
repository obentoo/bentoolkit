package validate

import (
	"strings"
	"testing"
)

// TestFindDistfile_TheReleaseArchiveWinsOverItsBundles: the shapes the
// 2026-11-13 run declined. A vendored or crates bundle beside the release is
// not the release, and of per-architecture builds the host's is read.
func TestFindDistfile_TheReleaseArchiveWinsOverItsBundles(t *testing.T) {
	cases := []struct {
		desc, version string
		names         []string
		want          string
	}{
		{"trufflehog: a vendor bundle", "3.97.6", []string{"trufflehog-3.97.6-vendor.tar.xz", "trufflehog-3.97.6.tar.gz"}, "trufflehog-3.97.6.tar.gz"},
		{"codex: a crates bundle", "0.169.0", []string{"codex-0.169.0.tar.gz", "codex-rust-v0.169.0-crates.tar.xz"}, "codex-0.169.0.tar.gz"},
		{"a single .crate beside the release", "1.2.0", []string{"m-1.2.0.tar.gz", "m-1.2.0.crate"}, "m-1.2.0.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			pkgDir, distdir := s089vFixture(t, c.names, c.names)
			s089vChosen(t, pkgDir, distdir, c.version, c.want)
		})
	}
}

func TestMainArchive_TheHostArchitectureWins(t *testing.T) {
	names := []string{"dart-3.13.3-amd64.zip", "dart-3.13.3-arm64.zip"}
	for goarch, want := range map[string]string{"amd64": "dart-3.13.3-amd64.zip", "arm64": "dart-3.13.3-arm64.zip"} {
		if got, ok := mainArchive(names, goarch); !ok || got != want {
			t.Errorf("mainArchive(%s) = %q, %v; want %q", goarch, got, ok, want)
		}
	}
	if got, ok := mainArchive(names, "riscv64"); ok {
		t.Errorf("no build for riscv64, yet %q was picked", got)
	}
	if got, ok := mainArchive([]string{"m-1.0.tar.gz", "m-1.0-docs.tar.gz"}, "amd64"); ok {
		t.Errorf("two primary archives and no architecture to tell them apart, yet %q was picked", got)
	}
}

// TestFindDistfile_OnlyBundlesPresentSaysTheReleaseIsMissing: mesa's case —
// what is on disk is crates of other versions, and the refusal says the
// release's own archive is missing rather than that it could not choose.
func TestFindDistfile_OnlyBundlesPresentSaysTheReleaseIsMissing(t *testing.T) {
	present := []string{"paste-1.0.14.crate", "syn-2.0.87.crate"}
	pkgDir, distdir := s089vFixture(t, append([]string{"mesa-26.3.0_pre20260821.tar.gz"}, present...), present)
	_, err := findDistfile(pkgDir, distdir, "26.3.0_pre20260821")
	if err == nil || !strings.Contains(err.Error(), "this release's own archive is not on disk") {
		t.Errorf("err = %v; want a refusal saying the release's archive is missing", err)
	}
}

// TestFindDistfile_AnUpstreamSpellingOfTheSuffixIsThisVersion: mesa publishes
// 27.0.0_pre20261112 as mesa-27.0.0-pre20261112.tar.gz.
func TestFindDistfile_AnUpstreamSpellingOfTheSuffixIsThisVersion(t *testing.T) {
	for _, name := range []string{"mesa-27.0.0-pre20261112.tar.gz", "mesa-27.0.0.pre20261112.tar.gz", "mesa-27.0.0pre20261112.tar.gz"} {
		t.Run(name, func(t *testing.T) {
			names := []string{name, "paste-1.0.14.crate", "syn-2.0.87.crate"}
			pkgDir, distdir := s089vFixture(t, names, names)
			s089vChosen(t, pkgDir, distdir, "27.0.0_pre20261112", name)
		})
	}
	names := []string{"mesa-27.0.0-pre20261111.tar.gz", "syn-2.0.87.crate"}
	pkgDir, distdir := s089vFixture(t, names, names)
	if got, err := findDistfile(pkgDir, distdir, "27.0.0_pre20261112"); err == nil {
		t.Errorf("another snapshot's archive %q was read as 27.0.0_pre20261112", got)
	}
}
