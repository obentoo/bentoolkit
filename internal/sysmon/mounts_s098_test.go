package sysmon_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// Story 098, sub-task 2.2: ParseMountinfo reads the kernel mount table and
// Reader.Mounts reads it from <Root>/proc/self/mountinfo (R3.1). A mount is
// read-only when the per-mount options or the superblock options say `ro`;
// mount points and sources are unescaped; a malformed line, or a table that
// cannot be read, is an error naming the line or the path, so the caller can
// keep its conditions unchanged and warn (R3.7). Fixtures live in
// t.TempDir(): no test reads the host /proc (Q18).

// s098Mountinfo is a host-shaped table: zero to three optional fields, every
// read-only combination, pseudo and image filesystems.
const s098Mountinfo = "" +
	"22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
	"23 22 0:21 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw\n" +
	"30 22 259:3 / /home rw,relatime shared:20 master:3 propagate_from:2 - btrfs /dev/nvme0n1p3 rw,ssd,space_cache=v2,subvolid=5,subvol=/\n" +
	"31 22 0:30 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw,size=16G\n" +
	"40 22 8:17 / /mnt/backup ro,relatime shared:30 - ext4 /dev/sdb1 ro\n" +
	"41 22 8:33 / /mnt/data rw,relatime shared:31 - ext4 /dev/sdc1 ro,errors=remount-ro\n" +
	"43 22 259:2 /srv /srv/ro-bind ro,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
	"44 22 8:49 / /mnt/scratch rw,relatime - ext4 /dev/sdd1 rw,errors=remount-ro\n" +
	"45 22 7:0 / /mnt/iso ro,relatime - iso9660 /dev/loop0 ro\n"

var s098MountinfoWant = []sysmon.Mount{
	{Point: "/", Source: "/dev/nvme0n1p2", FSType: "ext4", ReadOnly: false},
	{Point: "/proc", Source: "proc", FSType: "proc", ReadOnly: false},
	{Point: "/home", Source: "/dev/nvme0n1p3", FSType: "btrfs", ReadOnly: false},
	{Point: "/tmp", Source: "tmpfs", FSType: "tmpfs", ReadOnly: false},
	{Point: "/mnt/backup", Source: "/dev/sdb1", FSType: "ext4", ReadOnly: true},
	{Point: "/mnt/data", Source: "/dev/sdc1", FSType: "ext4", ReadOnly: true},
	{Point: "/srv/ro-bind", Source: "/dev/nvme0n1p2", FSType: "ext4", ReadOnly: true},
	{Point: "/mnt/scratch", Source: "/dev/sdd1", FSType: "ext4", ReadOnly: false},
	{Point: "/mnt/iso", Source: "/dev/loop0", FSType: "iso9660", ReadOnly: true},
}

// s098MountsSame compares mount lists, naming the first difference.
func s098MountsSame(t *testing.T, what string, got, want []sysmon.Mount) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d mounts %+v, want %d %+v", what, len(got), got, len(want), want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("%s: mount %d = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// TestS098MountinfoParse_HostTable: every line comes back, in order, with its
// point, source, type and read-only state; the parser keeps pseudo and image
// filesystems (excluding them is the caller's policy). The optional-fields
// list has a different length on each line, so a parser that counted fields
// from the start would read the wrong type or source.
func TestS098MountinfoParse_HostTable(t *testing.T) {
	got, err := sysmon.ParseMountinfo([]byte(s098Mountinfo))
	if err != nil {
		t.Fatalf("ParseMountinfo: %v", err)
	}
	s098MountsSame(t, "host table", got, s098MountinfoWant)
}

// TestS098MountinfoParse_ReadOnlyIsAWholeOption is the hostile half of the
// read-only rule: `ro` must be a whole option. errors=remount-ro, a
// rootcontext= option and a per-mount option list that merely contains the
// letters "ro" say nothing about the mount being read-only. The converse:
// `ro` anywhere in either list, first or not, makes the mount read-only.
func TestS098MountinfoParse_ReadOnlyIsAWholeOption(t *testing.T) {
	cases := []struct {
		name, line string
		ro         bool
	}{
		{"errors=remount-ro in super options", "50 22 8:1 / /a rw,relatime - ext4 /dev/sda1 rw,errors=remount-ro", false},
		{"rootcontext in super options", "51 22 8:2 / /b rw,relatime - ext4 /dev/sda2 rw,rootcontext=system_u:object_r:root_t:s0", false},
		{"ro-like per-mount options", "52 22 8:3 / /c rw,nosuid,nodev,noatime,nosymfollow - xfs /dev/sda3 rw,attr2,inode64,noquota", false},
		{"ro last in super options", "53 22 8:4 / /d rw,relatime - ext4 /dev/sda4 errors=remount-ro,ro", true},
		{"ro in per-mount options only", "54 22 8:5 / /e ro,nosuid - ext4 /dev/sda5 rw", true},
		{"ro in super options only", "55 22 8:6 / /f rw,relatime - btrfs /dev/sda6 ro,ssd,subvol=/", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sysmon.ParseMountinfo([]byte(tc.line + "\n"))
			if err != nil {
				t.Fatalf("ParseMountinfo(%q): %v", tc.line, err)
			}
			if len(got) != 1 {
				t.Fatalf("ParseMountinfo(%q) = %d mounts, want 1", tc.line, len(got))
			}
			if got[0].ReadOnly != tc.ro {
				t.Errorf("ParseMountinfo(%q).ReadOnly = %v, want %v", tc.line, got[0].ReadOnly, tc.ro)
			}
		})
	}
}

// TestS098MountinfoParse_SeparatorIsTheFirstDashField: the fields after the
// optional list are found from the ` - ` separator. A source that is itself
// "-" (mount -t tmpfs - /x) must not be taken for the separator, and an
// optional field never shifts the type or source.
func TestS098MountinfoParse_SeparatorIsTheFirstDashField(t *testing.T) {
	cases := []struct {
		line string
		want sysmon.Mount
	}{
		{"60 22 0:40 / /run/x rw,relatime - tmpfs - rw,size=1024k",
			sysmon.Mount{Point: "/run/x", Source: "-", FSType: "tmpfs"}},
		{"61 22 8:7 / /g rw shared:1 master:2 propagate_from:3 unbindable - ext4 /dev/sda7 rw",
			sysmon.Mount{Point: "/g", Source: "/dev/sda7", FSType: "ext4"}},
		{"62 22 8:8 / /h rw - ext4 /dev/sda8 rw",
			sysmon.Mount{Point: "/h", Source: "/dev/sda8", FSType: "ext4"}},
	}
	for _, tc := range cases {
		got, err := sysmon.ParseMountinfo([]byte(tc.line + "\n"))
		if err != nil {
			t.Errorf("ParseMountinfo(%q): %v", tc.line, err)
			continue
		}
		s098MountsSame(t, tc.line, got, []sysmon.Mount{tc.want})
	}
}

// TestS098MountinfoParse_EscapesAreDecodedOnce: space, tab, newline and
// backslash are unescaped in the mount point and the source. Two encoded
// points that differ only in an escaped backslash must stay distinct: `\134`
// followed by "040" is a backslash and the text 040, never a space, so a
// decoder that ran twice would merge it with the real space.
func TestS098MountinfoParse_EscapesAreDecodedOnce(t *testing.T) {
	table := "" +
		`70 22 8:9 / /mnt/My\040Disk rw - ext4 /dev/disk/by-label/My\040Disk rw` + "\n" +
		`71 22 8:10 / /mnt/a\134040b rw - ext4 /dev/disk/by-label/A\134B rw` + "\n" +
		`72 22 8:11 / /mnt/a\040b rw - ext4 /dev/sdb2 rw` + "\n" +
		`73 22 8:12 / /mnt/t\011n\012e rw - ext4 /dev/sdb3 rw` + "\n"
	got, err := sysmon.ParseMountinfo([]byte(table))
	if err != nil {
		t.Fatalf("ParseMountinfo: %v", err)
	}
	s098MountsSame(t, "escaped table", got, []sysmon.Mount{
		{Point: "/mnt/My Disk", Source: "/dev/disk/by-label/My Disk", FSType: "ext4"},
		{Point: `/mnt/a\040b`, Source: `/dev/disk/by-label/A\B`, FSType: "ext4"},
		{Point: "/mnt/a b", Source: "/dev/sdb2", FSType: "ext4"},
		{Point: "/mnt/t\tn\ne", Source: "/dev/sdb3", FSType: "ext4"},
	})
}

// s098LineRE matches an error that names line n of the table.
func s098LineRE(n string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)line\D{0,3}` + n + `\b`)
}

// TestS098MountinfoParse_MalformedLineIsErrorNamingIt is R3.7's base: a line
// that is not a mountinfo line makes the whole parse an error naming its line
// number — never a silently shortened table the caller would read as
// "mount gone" and clear a condition on.
func TestS098MountinfoParse_MalformedLineIsErrorNamingIt(t *testing.T) {
	good := "22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
		"23 22 0:21 / /proc rw,nosuid shared:12 - proc proc rw\n"
	bad := []struct{ name, line string }{
		{"too few fields", "1 2 3"},
		{"no separator", "24 22 8:1 / /x rw,relatime shared:1 ext4 /dev/sda1 rw"},
		{"nothing after the separator", "24 22 8:1 / /x rw -"},
		{"no source after the separator", "24 22 8:1 / /x rw - ext4"},
		{"separator before the mount options", "24 22 8:1 / - ext4 /dev/sda1 rw"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sysmon.ParseMountinfo([]byte(good + tc.line + "\n"))
			if err == nil {
				t.Fatalf("ParseMountinfo with a malformed line 3 %q returned no error", tc.line)
			}
			if !s098LineRE("3").MatchString(err.Error()) {
				t.Errorf("error %q does not name line 3", err)
			}
		})
	}
}

// TestS098MountinfoParse_TrailingNewlineAddsNothing: the final newline ends
// the last line; it is not an empty, malformed line.
func TestS098MountinfoParse_TrailingNewlineAddsNothing(t *testing.T) {
	one := "22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw"
	for _, in := range []string{one, one + "\n"} {
		got, err := sysmon.ParseMountinfo([]byte(in))
		if err != nil {
			t.Errorf("ParseMountinfo(%q): %v", in, err)
			continue
		}
		if len(got) != 1 {
			t.Errorf("ParseMountinfo(%q) = %d mounts, want 1", in, len(got))
		}
	}
}

// s098MountRoot writes content as <root>/proc/self/mountinfo and returns root.
func s098MountRoot(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "proc", "self")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mountinfo"), []byte(content), 0o600); err != nil {
		t.Fatalf("write mountinfo: %v", err)
	}
	return root
}

// TestS098MountinfoReader_ReadsUnderRoot is R3.1: Mounts reads the table under
// Root. The fixture's mount point exists on no real host, so a reader that
// ignored Root and read the host table fails here.
func TestS098MountinfoReader_ReadsUnderRoot(t *testing.T) {
	root := s098MountRoot(t, "90 22 8:65 / /mnt/s098-fixture-only rw,relatime - ext4 /dev/sde1 ro,errors=remount-ro\n")
	got, err := sysmon.Reader{Root: root}.Mounts()
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	s098MountsSame(t, "Mounts under Root", got, []sysmon.Mount{
		{Point: "/mnt/s098-fixture-only", Source: "/dev/sde1", FSType: "ext4", ReadOnly: true},
	})
}

// TestS098MountinfoReader_ErrorsNameThePath is R3.7 and Q4: an absent or a
// malformed table is an error that names the file and wraps the cause; the
// reader returns no partial table and does not log.
func TestS098MountinfoReader_ErrorsNameThePath(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "proc", "self", "mountinfo")
		got, err := sysmon.Reader{Root: root}.Mounts()
		if err == nil {
			t.Fatalf("Mounts with no mountinfo returned %+v and no error", got)
		}
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), path) {
			t.Errorf("error %q: want it to wrap fs.ErrNotExist and name %s", err, path)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		root := s098MountRoot(t, "22 1 259:2 / / rw - ext4 /dev/nvme0n1p2 rw\ngarbage\n")
		path := filepath.Join(root, "proc", "self", "mountinfo")
		got, err := sysmon.Reader{Root: root}.Mounts()
		if err == nil {
			t.Fatalf("Mounts of a malformed table returned %+v and no error", got)
		}
		if !strings.Contains(err.Error(), path) || !s098LineRE("2").MatchString(err.Error()) {
			t.Errorf("error %q: want it to name %s and line 2", err, path)
		}
		if len(got) != 0 {
			t.Errorf("Mounts returned a partial table %+v beside its error", got)
		}
	})
}

// FuzzParseMountinfo is Q17: no input panics, a parse is deterministic, an
// accepted table has no more mounts than lines, and a failed one returns no
// mounts.
func FuzzParseMountinfo(f *testing.F) {
	f.Add([]byte(s098Mountinfo))
	f.Add([]byte(`70 22 8:9 / /mnt/My\040Disk rw - ext4 /dev/disk/by-label/A\134B rw` + "\n"))
	f.Add([]byte("60 22 0:40 / /run/x rw - tmpfs - rw\n"))
	f.Add([]byte(`1 2 3:4 / /x\0 rw - a b c` + "\n"))
	f.Add([]byte(`1 2 3:4 / /x\777\ rw - a b c`))
	f.Add([]byte("1 2 3\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n - - - -\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := sysmon.ParseMountinfo(data)
		again, err2 := sysmon.ParseMountinfo(data)
		if (err == nil) != (err2 == nil) || !reflect.DeepEqual(got, again) {
			t.Fatalf("ParseMountinfo(%q) is not deterministic: %+v/%v then %+v/%v", data, got, err, again, err2)
		}
		if err != nil {
			if len(got) != 0 {
				t.Errorf("ParseMountinfo(%q) failed (%v) but returned %d mounts", data, err, len(got))
			}
			return
		}
		if lines := strings.Count(string(data), "\n") + 1; len(got) > lines {
			t.Errorf("ParseMountinfo(%q) returned %d mounts from %d lines", data, len(got), lines)
		}
	})
}
