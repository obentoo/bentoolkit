package main

import (
	"context"
	"io/fs"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

// A start-up SyncTime error is one WARN naming the repository and its path,
// and the repository stays watched.
func TestS099ProbeExtra_SyncTimeErrorIsOneWarnAndKeepsTheRepository(t *testing.T) {
	f := newS099Host()
	h := f.health()
	bad := "/s099-no-such-root/var/db/repos/gentoo-zh"
	h.SyncTime = func(location string) (time.Time, bool, error) {
		if location == bad {
			return time.Time{}, false, &fs.PathError{Op: "stat", Path: bad + "/.git/FETCH_HEAD", Err: fs.ErrPermission}
		}
		return time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), true, nil
	}
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, h)
	if len(r.Repos) != 2 {
		t.Fatalf("Repos = %+v, want both kept after a SyncTime error", r.Repos)
	}
	var warns []string
	for _, l := range s099NewLines(t, strings.Split(buf.String(), "\n")) {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=sync") {
			warns = append(warns, l)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "repository=gentoo-zh") || !strings.Contains(warns[0], "path="+bad) {
		t.Errorf("sync WARN lines = %q, want exactly one naming gentoo-zh and its path", warns)
	}
}

// A glsa-check that is found but cannot be run is one WARN with the cause,
// never the "gentoolkit is absent" INFO, and the GLSA monitor is off.
func TestS099ProbeExtra_GLSACheckPermissionIsOneWarn(t *testing.T) {
	f := newS099Host()
	f.missing["glsa-check"] = fs.ErrPermission
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	if r.GLSA != nil {
		t.Errorf("GLSA = %+v, want nil", r.GLSA)
	}
	if r.Updates == nil {
		t.Error("Updates = nil; a glsa-check failure must not take emerge with it")
	}
	var warn, info int
	for _, l := range s099NewLines(t, strings.Split(buf.String(), "\n")) {
		if !strings.Contains(l, "monitor=glsa") {
			continue
		}
		switch {
		case strings.Contains(l, "level=WARN") && strings.Contains(l, "glsa-check"):
			warn++
		case strings.Contains(l, "level=INFO"):
			info++
		}
	}
	if warn != 1 || info != 0 {
		t.Errorf("glsa lines: %d WARN, %d INFO; want exactly one WARN naming glsa-check", warn, info)
	}
}

// ionice runs with -t before -c 3: where ioprio_set is refused (a seccomp or
// LSM filter) ionice -c 3 exits 1 without running the command, which the App
// would read as "@world does not resolve"; -t runs it anyway.
func TestS099ProbeExtra_IoniceIgnoresARefusedClass(t *testing.T) {
	for _, missing := range []string{"", "nice"} {
		t.Run("missing="+missing, func(t *testing.T) {
			f := newS099Host()
			if missing != "" {
				f.missing[missing] = exec.ErrNotFound
			}
			log, _ := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			for name, c := range map[string]*pending.Command{"Updates": r.Updates, "GLSA": r.GLSA} {
				if c == nil {
					t.Fatalf("%s = nil, want it resolved", name)
				}
				bases := s099Bases(c.Wrap)
				i := slices.Index(bases, "ionice")
				if i < 0 || !slices.Equal(bases[i+1:], []string{"-t", "-c", "3"}) {
					t.Errorf("%s Wrap = %q, want ionice -t -c 3 ending it", name, c.Wrap)
				}
			}
		})
	}
}

// Every start-up line of the new monitors carries exactly one state: off on
// a line that turns a monitor off or leaves it nothing to watch, ok on a line
// whose monitor keeps running from its starting level.
func TestS099ProbeExtra_StartupLinesCarryState(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *s099Host, h *hostHealth)
		match string
		state string
	}{
		{"emerge not found", func(f *s099Host, _ *hostHealth) { f.missing["emerge"] = exec.ErrNotFound }, "monitor=updates", "off"},
		{"glsa-check absent", func(f *s099Host, _ *hostHealth) { f.missing["glsa-check"] = exec.ErrNotFound }, "monitor=glsa", "off"},
		{"glsa-check refused", func(f *s099Host, _ *hostHealth) { f.missing["glsa-check"] = fs.ErrPermission }, "monitor=glsa", "off"},
		{"repos.conf unreadable", func(f *s099Host, _ *hostHealth) { f.reposErr = fs.ErrPermission }, "monitor=sync", "off"},
		{"wrapper missing", func(f *s099Host, _ *hostHealth) { f.missing["ionice"] = exec.ErrNotFound }, "command=ionice", "ok"},
		{"no sync time", func(f *s099Host, _ *hostHealth) {
			f.noSync["/s099-no-such-root/var/db/repos/gentoo-zh"] = true
		}, "repository=gentoo-zh", "ok"},
		{"sync time unreadable", func(_ *s099Host, h *hostHealth) {
			h.SyncTime = func(string) (time.Time, bool, error) { return time.Time{}, false, fs.ErrPermission }
		}, "repository=gentoo-zh", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newS099Host()
			h := f.health()
			tc.setup(f, &h)
			log, buf := s097Logger()
			resolveHealth(context.Background(), log, config.TrayConfig{}, h)
			var hits int
			for _, l := range s099NewLines(t, strings.Split(buf.String(), "\n")) {
				if n := strings.Count(l, " state="); n != 1 {
					t.Errorf("line carries %d state keys, want 1: %s", n, l)
				}
				if strings.Contains(l, tc.match) {
					hits++
					if !strings.Contains(l, " state="+tc.state+" ") && !strings.HasSuffix(l, " state="+tc.state) {
						t.Errorf("line %s, want state=%s", l, tc.state)
					}
				}
			}
			if hits != 1 {
				t.Errorf("%d lines match %s, want 1\n%s", hits, tc.match, buf.String())
			}
		})
	}
}
