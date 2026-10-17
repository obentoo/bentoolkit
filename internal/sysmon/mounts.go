package sysmon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Mount is one line of the kernel mount table.
type Mount struct {
	Point    string // the mount point, unescaped
	Source   string // the mounted device or pseudo source, unescaped
	FSType   string
	ReadOnly bool // `ro` in the per-mount options or in the superblock options
}

// mountinfoPath is the mount table under a Reader's Root.
const mountinfoPath = "proc/self/mountinfo"

// Mounts reads <Root>/proc/self/mountinfo. A table that cannot be read or
// parsed is an error naming the file; no partial table is returned.
func (r Reader) Mounts() ([]Mount, error) {
	path := filepath.Join(r.Root, mountinfoPath)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a fixed name under the reader's root
	if err != nil {
		return nil, fmt.Errorf("reading the mount table %s: %w", path, err)
	}
	mounts, err := ParseMountinfo(data)
	if err != nil {
		return nil, fmt.Errorf("parsing the mount table %s: %w", path, err)
	}
	return mounts, nil
}

// ParseMountinfo parses a mountinfo table (proc(5)). The fields after the
// variable-length optional list are found from its " - " separator, never by
// position. A malformed line makes the whole parse an error naming its line
// number, so a caller never mistakes a shortened table for unmounted
// filesystems.
func ParseMountinfo(data []byte) ([]Mount, error) {
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	mounts := make([]Mount, 0, len(lines))
	ids := make([]mountIDs, 0, len(lines))
	for i, line := range lines {
		m, id, err := parseMountLine(string(line))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		mounts = append(mounts, m)
		ids = append(ids, id)
	}
	stackOrder(mounts, ids)
	return mounts, nil
}

// mountIDs are a line's mount ID and parent ID.
type mountIDs struct{ id, parent string }

// stackOrder reorders the entries that share a mount point so each one comes
// after the entry it covers, its parent; the last entry of a point is then the
// one a path there reaches. The kernel lists mounts in creation order, so a
// mount moved over an existing mount point (mount --move) is listed before the
// mount it covers. Entries at different points keep their places.
func stackOrder(mounts []Mount, ids []mountIDs) {
	byPoint := map[string][]int{}
	for i, m := range mounts {
		byPoint[m.Point] = append(byPoint[m.Point], i)
	}
	for _, slots := range byPoint {
		if len(slots) < 2 {
			continue
		}
		byID := make(map[string]int, len(slots))
		for _, i := range slots {
			byID[ids[i].id] = i
		}
		depth := make(map[int]int, len(slots))
		for _, i := range slots {
			depth[i] = stackDepth(i, len(slots), ids, byID)
		}
		order := slices.Clone(slots)
		slices.SortStableFunc(order, func(x, y int) int { return depth[x] - depth[y] })
		picked := make([]Mount, len(order))
		for k, i := range order {
			picked[k] = mounts[i]
		}
		for k, i := range slots {
			mounts[i] = picked[k]
		}
	}
}

// stackDepth counts how many entries of the same point lie below entry i,
// following parent IDs through byID; a loop in a malformed table stops after
// limit steps.
func stackDepth(i, limit int, ids []mountIDs, byID map[string]int) int {
	d := 0
	for cur := i; d < limit; d++ {
		parent, ok := byID[ids[cur].parent]
		if !ok || parent == cur {
			break
		}
		cur = parent
	}
	return d
}

// parseMountLine reads "id parent maj:min root point options [optional...] -
// fstype source superoptions".
func parseMountLine(line string) (Mount, mountIDs, error) {
	f := strings.Split(line, " ")
	if len(f) < 10 {
		return Mount{}, mountIDs{}, fmt.Errorf("%d fields, want at least 10", len(f))
	}
	sep := slices.Index(f[6:], "-")
	if sep < 0 {
		return Mount{}, mountIDs{}, fmt.Errorf("no \" - \" separator after the mount options")
	}
	sep += 6
	if len(f)-sep-1 < 3 {
		return Mount{}, mountIDs{}, fmt.Errorf("want fstype, source and superblock options after the separator")
	}
	return Mount{
		Point:    unescapeMount(f[4]),
		Source:   unescapeMount(f[sep+2]),
		FSType:   f[sep+1],
		ReadOnly: hasOption(f[5], "ro") || hasOption(f[sep+3], "ro"),
	}, mountIDs{id: f[0], parent: f[1]}, nil
}

// hasOption reports whether the comma-separated list holds opt as a whole
// option, so errors=remount-ro is not ro.
func hasOption(list, opt string) bool {
	return slices.Contains(strings.Split(list, ","), opt)
}

// unescapeMount decodes the kernel's \ooo octal escapes (space, tab, newline,
// backslash) in one pass; anything else stays as written.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '3' && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }
