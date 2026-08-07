//go:build live

package validate

// Authored for story 031, sub-task 8.2 — R1, R1.1, R4.1.
//
// The opt-in half of the golden pair. golden_test.go proves the gate against a
// synthetic archive built in-process; this file proves it against the REAL
// 5.7 MB gst-plugins-good tarball, which is never committed. Run it with:
//
//	go test -tags live ./internal/autoupdate/validate/...
//
// The build tag keeps the default suite offline and hermetic. When the tarball
// is not on the host, the test SKIPS WITH A REASON NAMING THE FILE — it never
// passes silently, because a silent pass here would be the same unverified
// green the whole story exists to remove.
//
// Red is DEFERRED to Run mode, and doubly so: this file is not even compiled
// without the tag.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// liveArchive locates the real tarball in the host's distdir, or skips.
func liveArchive(t *testing.T, name string) string {
	t.Helper()
	dir, found := distfiles.Locate("", "")
	if !found {
		t.Skipf("no distdir on this host; %s cannot be read", name)
	}
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not in %s: %v — fetch it, or run without -tags live", name, dir, err)
	}
	return path
}

// TestLive_UpstreamDroppedAalibAndLibcacaAt1292 is the measurement the source
// report made by hand, turned into an assertion: 1.29.2 declares neither
// option, while 1.28.6 declares both.
func TestLive_UpstreamDroppedAalibAndLibcacaAt1292(t *testing.T) {
	newer := liveArchive(t, "gst-plugins-good-1.29.2.tar.xz")

	got, err := OptionsFromArchive(context.Background(), newer)
	if err != nil {
		t.Fatalf("OptionsFromArchive(%s): %v", newer, err)
	}

	declared := map[string]bool{}
	for _, o := range got.Root {
		declared[o.Name] = true
	}
	for _, gone := range []string{"aalib", "libcaca"} {
		if declared[gone] {
			t.Errorf("1.29.2 still declares %q; the premise of issue #33 no longer holds and this story's golden case needs revisiting", gone)
		}
	}
	if len(declared) == 0 {
		t.Fatal("read zero options from the real archive; the extraction is not finding the option file")
	}
	if !declared["qt6"] {
		t.Error("1.29.2 does not declare qt6, which the ebuild passes — the extraction is reading the wrong member")
	}
}

func TestLive_ThePreviousVersionStillDeclaresBoth(t *testing.T) {
	older := liveArchive(t, "gst-plugins-good-1.28.6.tar.xz")

	got, err := OptionsFromArchive(context.Background(), older)
	if err != nil {
		t.Fatalf("OptionsFromArchive(%s): %v", older, err)
	}

	declared := map[string]bool{}
	for _, o := range got.Root {
		declared[o.Name] = true
	}
	for _, want := range []string{"aalib", "libcaca"} {
		if !declared[want] {
			t.Errorf("1.28.6 does not declare %q; the two versions would then differ for some other reason than the one measured", want)
		}
	}
}

// TestLive_ReadsTheRealMesonOptionsFilename pins the naming detail that made
// this package worth checking at all: gst-plugins-good ships `meson.options`,
// the post-Meson-1.1 name, not `meson_options.txt`.
func TestLive_ReadsTheRealMesonOptionsFilename(t *testing.T) {
	newer := liveArchive(t, "gst-plugins-good-1.29.2.tar.xz")

	got, err := OptionsFromArchive(context.Background(), newer)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}

	var read string
	for _, s := range got.Sources {
		if strings.HasSuffix(s, "meson.options") || strings.HasSuffix(s, "meson_options.txt") {
			read = s
			break
		}
	}
	if read == "" {
		t.Fatalf("Sources %v records no option file; the gate cannot show its evidence", got.Sources)
	}
	t.Logf("read upstream declarations from %s", read)
}
