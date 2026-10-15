package main

// Story 097, sub-task 6.1: the disk paths bentoo-tray watches. diskPaths is
// pure: the two portageq questions (PORTAGE_TMPDIR, DISTDIR) and the
// existence check are injected, so no test runs portageq or stats the host.
//
// Contract pinned here:
//
//	diskPaths(configured []string, tmpdir, distdir func() string,
//		stat func(string) (fs.FileInfo, error)) (paths, warnings []string)
//
// tmpdir and distdir answer "" when portageq cannot; each warning is one WARN
// line main logs.

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"
)

type s097Info struct{ name string }

func (i s097Info) Name() string       { return i.name }
func (i s097Info) Size() int64        { return 0 }
func (i s097Info) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (i s097Info) ModTime() time.Time { return time.Time{} }
func (i s097Info) IsDir() bool        { return true }
func (i s097Info) Sys() any           { return nil }

// s097Paths fakes the host: the portageq answers, the paths that exist, and
// how often each was asked.
type s097Paths struct {
	tmpdir, distdir string
	missing         map[string]bool
	tmpdirAsked     int
	distdirAsked    int
	statted         map[string]int
}

func newS097Paths() *s097Paths {
	return &s097Paths{tmpdir: "/var/tmp", distdir: "/var/cache/distfiles", missing: map[string]bool{}, statted: map[string]int{}}
}

func (p *s097Paths) resolve(configured []string) (paths, warnings []string) {
	return diskPaths(configured,
		func() string { p.tmpdirAsked++; return p.tmpdir },
		func() string { p.distdirAsked++; return p.distdir },
		func(path string) (fs.FileInfo, error) {
			p.statted[path]++
			if p.missing[path] {
				return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
			}
			return s097Info{name: path}, nil
		})
}

// TestS097DiskPaths_R2_1_DefaultsAreRootTmpdirAndDistdir: with no configured
// paths the tray watches /, PORTAGE_TMPDIR and DISTDIR in that order, asking
// portageq each question once.
func TestS097DiskPaths_R2_1_DefaultsAreRootTmpdirAndDistdir(t *testing.T) {
	p := newS097Paths()
	paths, warnings := p.resolve(nil)
	if want := []string{"/", "/var/tmp", "/var/cache/distfiles"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
	if p.tmpdirAsked != 1 || p.distdirAsked != 1 {
		t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times, want once each", p.tmpdirAsked, p.distdirAsked)
	}
}

// TestS097DiskPaths_R2_2_ConfiguredPathsReplaceTheDefaults: configured paths
// are watched exactly, nothing is merged in, and portageq is not asked.
func TestS097DiskPaths_R2_2_ConfiguredPathsReplaceTheDefaults(t *testing.T) {
	for name, configured := range map[string][]string{
		"only the root, a default too": {"/"},
		"paths outside the defaults":   {"/home", "/mnt/build"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newS097Paths()
			paths, warnings := p.resolve(configured)
			if !slices.Equal(paths, configured) {
				t.Errorf("paths = %q, want exactly the configured %q", paths, configured)
			}
			if len(warnings) != 0 {
				t.Errorf("warnings = %q, want none", warnings)
			}
			if p.tmpdirAsked+p.distdirAsked != 0 {
				t.Errorf("portageq was asked %d times with configured paths, want 0", p.tmpdirAsked+p.distdirAsked)
			}
		})
	}
}

// TestS097DiskPaths_R2_4_UnansweredVariableIsNamed: a portageq question left
// unanswered warns naming that variable, and only that one, and the other
// defaults are still watched.
func TestS097DiskPaths_R2_4_UnansweredVariableIsNamed(t *testing.T) {
	cases := []struct {
		name            string
		tmpdir, distdir string
		want            []string
		named, notNamed []string
	}{
		{"PORTAGE_TMPDIR unanswered", "", "/var/cache/distfiles",
			[]string{"/", "/var/cache/distfiles"}, []string{"PORTAGE_TMPDIR"}, []string{"DISTDIR"}},
		{"DISTDIR unanswered", "/var/tmp", "",
			[]string{"/", "/var/tmp"}, []string{"DISTDIR"}, []string{"PORTAGE_TMPDIR"}},
		{"both unanswered", "", "", []string{"/"}, []string{"PORTAGE_TMPDIR", "DISTDIR"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newS097Paths()
			p.tmpdir, p.distdir = c.tmpdir, c.distdir
			paths, warnings := p.resolve(nil)
			if !slices.Equal(paths, c.want) {
				t.Errorf("paths = %q, want %q", paths, c.want)
			}
			if len(warnings) != len(c.named) {
				t.Fatalf("warnings = %q, want one per unanswered variable %q", warnings, c.named)
			}
			joined := strings.Join(warnings, "\n")
			for _, v := range c.named {
				if !strings.Contains(joined, v) {
					t.Errorf("warnings %q do not name %s", warnings, v)
				}
			}
			for _, v := range c.notNamed {
				if strings.Contains(joined, v) {
					t.Errorf("warnings %q name %s, which was answered", warnings, v)
				}
			}
		})
	}
}

// TestS097DiskPaths_R2_5_MissingPathIsNamedAndDropped: a watched path that does
// not exist warns naming it and is dropped; each path is checked once.
func TestS097DiskPaths_R2_5_MissingPathIsNamedAndDropped(t *testing.T) {
	cases := []struct {
		name       string
		configured []string
		missing    string
		want       []string
		notNamed   string // a present path the warning must not name
	}{
		{"a default path", nil, "/var/cache/distfiles", []string{"/", "/var/tmp"}, "/var/tmp"},
		{"a configured path beside its longer sibling", []string{"/mnt/a", "/mnt/ab"}, "/mnt/a", []string{"/mnt/ab"}, "/mnt/ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newS097Paths()
			p.missing[c.missing] = true
			paths, warnings := p.resolve(c.configured)
			if !slices.Equal(paths, c.want) {
				t.Errorf("paths = %q, want %q", paths, c.want)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], c.missing) {
				t.Fatalf("warnings = %q, want one naming %s", warnings, c.missing)
			}
			if c.notNamed != "" && strings.Contains(warnings[0], c.notNamed) {
				t.Errorf("warning %q names %s, which exists", warnings[0], c.notNamed)
			}
			for path, n := range p.statted {
				if n != 1 {
					t.Errorf("%s checked %d times, want once", path, n)
				}
			}
			for _, w := range c.want {
				if p.statted[w] != 1 {
					t.Errorf("%s watched without being checked", w)
				}
			}
		})
	}
}
