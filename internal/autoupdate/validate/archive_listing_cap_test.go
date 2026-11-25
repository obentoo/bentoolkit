package validate

// A large source tree lists more member NAMES than the 4 MiB read cap, though
// the gates only ever look at a handful of them: thunderbird-161.0.source.tar.xz
// made every gate SKIP with "archive output exceeds the read cap" on
// 2026-11-25. The listing now keeps only the names a gate reads, so the cap
// still bounds memory but no longer counts the tree's size. Hostile half: an
// archive whose RELEVANT names alone pass the cap is still refused.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// bigTreeMembers returns n filler members whose names alone total more than
// maxArchiveBytes, under one wrapper directory.
func bigTreeMembers(n int, base string) map[string]string {
	members := make(map[string]string, n+2)
	pad := strings.Repeat("d", 60)
	for i := range n {
		members[fmt.Sprintf("tb-161.0/comm/third_party/%s/%06d/%s", pad, i, base)] = ""
	}
	return members
}

func TestListArchiveMembers_AHugeTreeKeepsTheNamesTheGatesRead(t *testing.T) {
	members := bigTreeMembers(50000, "file.c") // ~5 MiB of names
	members["tb-161.0/meson.build"] = "project('tb')\n"
	members["tb-161.0/meson.options"] = "option('x', type: 'boolean', value: true)\n"
	archive := buildTarGz(t, members)

	system, err := ArchiveBuildSystem(context.Background(), archive)
	if err != nil {
		t.Fatalf("ArchiveBuildSystem: %v (the tree's size must not trip the read cap)", err)
	}
	if system != "meson" {
		t.Errorf("system = %q, want meson", system)
	}
	declared, err := OptionsFromArchive(context.Background(), archive)
	if err != nil {
		t.Fatalf("OptionsFromArchive: %v", err)
	}
	if len(declared.Root) != 1 || declared.Root[0].Name != "x" {
		t.Errorf("root options = %+v, want the one option x", declared.Root)
	}
}

func TestListArchiveMembers_TooManyRelevantNamesIsStillRefused(t *testing.T) {
	archive := buildTarGz(t, bigTreeMembers(50000, "CMakeLists.txt"))
	_, err := listArchiveMembers(context.Background(), archive)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("err = %v, want ErrArchiveTooLarge: relevant names past the cap are a bomb", err)
	}
}
