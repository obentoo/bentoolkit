package main

// Story 099, sub-task 6.1: bentoo-tray resolves the update commands, their
// priority wrappers and the synced repositories once at start-up, through
// the injected hostHealth. Every test passes fakes, so none looks a command
// up on the host PATH, reads /etc/portage/repos.conf or stats a repository.
//
// Contract (design.md "cmd/bentoo-tray"; story 098's resolveHealth):
//
//	hostHealth gains:
//		LookPath    func(string) (string, error)            // exec.LookPath
//		SyncedRepos func(string) ([]news.Repo, error)       // news.SyncedRepos
//		SyncTime    func(string) (time.Time, bool, error)   // news.SyncTime
//	A nil field is a capability the host lacks: nothing is looked up or
//	logged for it.
//
//	resolveHealth(ctx, log, cfg, h) gains the tray.Deps fields
//		Updates, GLSA *pending.Command; Repos []news.Repo;
//		RepoRevisions string; EmergeRunning func() (bool, error)
//
// Updates: Path = LookPath("emerge"), Args --pretend --update --deep
// --newuse --ask=n --color=n --quiet @world, Timeout 10 min. GLSA: Path =
// LookPath("glsa-check"), Args --list affected --nocolor, Timeout 2 min.
// Wrap = nice -n 19 ionice -c 3, each wrapper dropped with one WARN when its
// lookup fails. Every new start-up line carries monitor=<updates|glsa|sync>
// and exactly one level key.
//
// Hostile halves first: two near-identical repositories must both be kept
// and asked about separately, and a lookup failure of one command must not
// take the other command with it.

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

const s099Bin = "/s099-no-such-root/usr/bin/"

// s099Host is story 097's fake host plus the three story 099 capabilities:
// every command found under s099Bin unless listed missing, two synced
// repositories with a known sync time.
type s099Host struct {
	*s097Host
	mu         sync.Mutex
	missing    map[string]error
	looked     []string
	repos      []news.Repo
	reposErr   error
	reposAsked []string
	noSync     map[string]bool
	syncAsked  []string
}

func newS099Host() *s099Host {
	return &s099Host{
		s097Host: newS097Host(),
		missing:  map[string]error{},
		repos: []news.Repo{
			{Name: "gentoo", Location: "/s099-no-such-root/var/db/repos/gentoo"},
			{Name: "gentoo-zh", Location: "/s099-no-such-root/var/db/repos/gentoo-zh"},
		},
		noSync: map[string]bool{},
	}
}

func (f *s099Host) health() hostHealth {
	h := f.s097Host.health()
	h.LookPath = func(name string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.looked = append(f.looked, name)
		if err := f.missing[name]; err != nil {
			return "", &exec.Error{Name: name, Err: err}
		}
		return s099Bin + name, nil
	}
	h.SyncedRepos = func(path string) ([]news.Repo, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.reposAsked = append(f.reposAsked, path)
		if f.reposErr != nil {
			return nil, f.reposErr
		}
		return slices.Clone(f.repos), nil
	}
	h.SyncTime = func(location string) (time.Time, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.syncAsked = append(f.syncAsked, location)
		if f.noSync[location] {
			return time.Time{}, false, nil
		}
		return time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), true, nil
	}
	return h
}

func (f *s099Host) lookedUp() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.looked)
}

func (f *s099Host) asked() (repos, syncs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reposAsked), slices.Clone(f.syncAsked)
}

// s099Bases maps a Wrap to the base names of its commands and its flags.
func s099Bases(wrap []string) []string {
	out := make([]string, len(wrap))
	for i, w := range wrap {
		out[i] = filepath.Base(w)
	}
	return out
}

// s099NewLines are the start-up lines of the three new monitors.
func s099NewLines(t *testing.T, all []string) []string {
	t.Helper()
	var out []string
	for _, l := range all {
		if strings.Contains(l, "monitor=updates") || strings.Contains(l, "monitor=glsa") || strings.Contains(l, "monitor=sync") {
			out = append(out, l)
			if n := strings.Count(l, "level="); n != 1 {
				t.Errorf("line carries %d level keys, want 1: %s", n, l)
			}
		}
	}
	return out
}

// --- hostile halves ---

func TestS099Probe_R4_1_NearIdenticalRepositoriesAreBothKept(t *testing.T) {
	f := newS099Host()
	log, _ := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	if len(r.Repos) != 2 || r.Repos[0].Name == r.Repos[1].Name {
		t.Fatalf("Repos = %+v, want gentoo and gentoo-zh both kept", r.Repos)
	}
	_, syncs := f.asked()
	slices.Sort(syncs)
	want := []string{"/s099-no-such-root/var/db/repos/gentoo", "/s099-no-such-root/var/db/repos/gentoo-zh"}
	if !slices.Equal(syncs, want) {
		t.Errorf("SyncTime asked for %q, want each repository once: %q", syncs, want)
	}
}

func TestS099Probe_R2_11_R3_7_OneCommandMissingKeepsTheOther(t *testing.T) {
	t.Run("emerge missing", func(t *testing.T) {
		f := newS099Host()
		f.missing["emerge"] = exec.ErrNotFound
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
		if r.Updates != nil {
			t.Errorf("Updates = %+v with emerge missing, want nil", r.Updates)
		}
		if r.GLSA == nil || r.GLSA.Path != s099Bin+"glsa-check" {
			t.Errorf("GLSA = %+v, want glsa-check kept when only emerge is missing", r.GLSA)
		}
	})
	t.Run("glsa-check missing", func(t *testing.T) {
		f := newS099Host()
		f.missing["glsa-check"] = exec.ErrNotFound
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
		if r.GLSA != nil {
			t.Errorf("GLSA = %+v with glsa-check missing, want nil", r.GLSA)
		}
		if r.Updates == nil || r.Updates.Path != s099Bin+"emerge" {
			t.Errorf("Updates = %+v, want emerge kept when only glsa-check is missing", r.Updates)
		}
	})
}

// --- the commands ---

func TestS099Probe_R2_1_R3_1_R1_6_R1_8_ExactCommands(t *testing.T) {
	f := newS099Host()
	log, buf := s097Logger()
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	if r.Updates == nil || r.GLSA == nil {
		t.Fatalf("Updates = %+v, GLSA = %+v; want both with every command found", r.Updates, r.GLSA)
	}
	wantWrap := []string{"nice", "-n", "19", "ionice", "-t", "-c", "3"}
	check := func(name string, c *pending.Command, path string, args []string, timeout time.Duration) {
		t.Helper()
		if c.Path != path {
			t.Errorf("%s Path = %q, want the looked-up %q", name, c.Path, path)
		}
		if !slices.Equal(c.Args, args) {
			t.Errorf("%s Args = %q, want %q", name, c.Args, args)
		}
		if c.Timeout != timeout {
			t.Errorf("%s Timeout = %v, want %v", name, c.Timeout, timeout)
		}
		if got := s099Bases(c.Wrap); !slices.Equal(got, wantWrap) {
			t.Errorf("%s Wrap = %q, want nice -n 19 ionice -c 3", name, c.Wrap)
		}
	}
	check("Updates", r.Updates, s099Bin+"emerge",
		[]string{"--pretend", "--update", "--deep", "--newuse", "--ask=n", "--color=n", "--quiet", "@world"}, 10*time.Minute)
	check("GLSA", r.GLSA, s099Bin+"glsa-check", []string{"--list", "affected", "--nocolor"}, 2*time.Minute)
	if r.RepoRevisions != "/var/lib/portage/repo_revisions" {
		t.Errorf("RepoRevisions = %q, want /var/lib/portage/repo_revisions", r.RepoRevisions)
	}
	if r.EmergeRunning == nil {
		t.Error("EmergeRunning = nil; the emerge hold needs it when every resource monitor is off")
	}
	if l := s099NewLines(t, strings.Split(buf.String(), "\n")); len(l) != 0 {
		t.Errorf("start-up lines %q, want none with every command and repository healthy", l)
	}
	for _, name := range []string{"emerge", "glsa-check", "nice", "ionice"} {
		if n := countOf(f.lookedUp(), name); n != 1 {
			t.Errorf("%s looked up %d times, want once", name, n)
		}
	}
}

func countOf(list []string, v string) int {
	n := 0
	for _, s := range list {
		if s == v {
			n++
		}
	}
	return n
}

func TestS099Probe_R2_11_EmergeLookupErrorIsOneWarn(t *testing.T) {
	for name, cause := range map[string]error{"not found": exec.ErrNotFound, "permission": fs.ErrPermission} {
		t.Run(name, func(t *testing.T) {
			f := newS099Host()
			f.missing["emerge"] = cause
			log, buf := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			if r.Updates != nil {
				t.Errorf("Updates = %+v, want nil", r.Updates)
			}
			lines := s099NewLines(t, strings.Split(buf.String(), "\n"))
			var warns []string
			for _, l := range lines {
				if strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=updates") {
					warns = append(warns, l)
				}
			}
			if len(warns) != 1 || !strings.Contains(warns[0], "emerge") {
				t.Errorf("updates WARN lines = %q, want exactly one naming emerge", warns)
			}
		})
	}
}

func TestS099Probe_R3_7_GLSACheckAbsentIsOneInfo(t *testing.T) {
	f := newS099Host()
	f.missing["glsa-check"] = exec.ErrNotFound
	log, buf := s097Logger()
	resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	lines := s099NewLines(t, strings.Split(buf.String(), "\n"))
	var info, warn int
	for _, l := range lines {
		if !strings.Contains(l, "monitor=glsa") {
			continue
		}
		switch {
		case strings.Contains(l, "level=INFO"):
			info++
			if !strings.Contains(l, "glsa-check") && !strings.Contains(l, "gentoolkit") {
				t.Errorf("GLSA INFO %q names neither glsa-check nor gentoolkit", l)
			}
		case strings.Contains(l, "level=WARN"):
			warn++
		}
	}
	if info != 1 || warn != 0 {
		t.Errorf("glsa lines: %d INFO, %d WARN; want exactly one INFO", info, warn)
	}
}

func TestS099Probe_R1_6_MissingWrapperIsDroppedWithOneWarn(t *testing.T) {
	cases := []struct {
		missing []string
		want    []string
	}{
		{[]string{"nice"}, []string{"ionice", "-t", "-c", "3"}},
		{[]string{"ionice"}, []string{"nice", "-n", "19"}},
		{[]string{"nice", "ionice"}, nil},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.missing, "+"), func(t *testing.T) {
			f := newS099Host()
			for _, m := range tc.missing {
				f.missing[m] = exec.ErrNotFound
			}
			log, buf := s097Logger()
			r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
			//nolint:staticcheck // QF1001: the Wrap check reads as "unequal unless both are empty"
			for name, c := range map[string]*pending.Command{"Updates": r.Updates, "GLSA": r.GLSA} {
				if c == nil {
					t.Fatalf("%s = nil; a missing wrapper must never drop the command", name)
				}
				if got := s099Bases(c.Wrap); !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
					t.Errorf("%s Wrap = %q, want %q", name, c.Wrap, tc.want)
				}
			}
			all := strings.Split(buf.String(), "\n")
			for _, m := range tc.missing {
				n := 0
				word := regexp.MustCompile(`\b` + m + `\b`) // "nice" must not match "ionice"
				for _, l := range all {
					if strings.Contains(l, "level=WARN") && word.MatchString(l) {
						n++
					}
				}
				if n != 1 {
					t.Errorf("WARN lines naming %s = %d, want exactly 1", m, n)
				}
			}
			s099NewLines(t, all)
		})
	}
}

// --- repositories ---

func TestS099Probe_R4_7_R4_8_SyncedReposReadOnceAndErrorIsOneWarn(t *testing.T) {
	t.Run("read once from repos.conf", func(t *testing.T) {
		f := newS099Host()
		log, _ := s097Logger()
		resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
		repos, _ := f.asked()
		if len(repos) != 1 || repos[0] != "/etc/portage/repos.conf" {
			t.Errorf("SyncedRepos asked %q, want once for /etc/portage/repos.conf", repos)
		}
	})
	t.Run("error", func(t *testing.T) {
		f := newS099Host()
		f.reposErr = errors.New("open /etc/portage/repos.conf: permission denied")
		log, buf := s097Logger()
		r := resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
		if len(r.Repos) != 0 {
			t.Errorf("Repos = %+v after a repos.conf error, want none", r.Repos)
		}
		var warns []string
		for _, l := range s099NewLines(t, strings.Split(buf.String(), "\n")) {
			if strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=sync") {
				warns = append(warns, l)
			}
		}
		if len(warns) != 1 || !strings.Contains(warns[0], "repos.conf") {
			t.Errorf("sync WARN lines = %q, want exactly one naming repos.conf", warns)
		}
		if _, syncs := f.asked(); len(syncs) != 0 {
			t.Errorf("SyncTime asked %q after repos.conf failed, want nothing", syncs)
		}
	})
}

func TestS099Probe_R4_3_RepositoryWithoutSyncTimeIsOneInfo(t *testing.T) {
	f := newS099Host()
	f.noSync["/s099-no-such-root/var/db/repos/gentoo-zh"] = true
	log, buf := s097Logger()
	resolveHealth(context.Background(), log, config.TrayConfig{}, f.health())
	var infos []string
	for _, l := range s099NewLines(t, strings.Split(buf.String(), "\n")) {
		if strings.Contains(l, "level=INFO") && strings.Contains(l, "monitor=sync") {
			infos = append(infos, l)
		}
	}
	if len(infos) != 1 || !strings.Contains(infos[0], "gentoo-zh") {
		t.Errorf("sync INFO lines = %q, want exactly one naming gentoo-zh", infos)
	}
}

// --- disabled and absent ---

func TestS099Probe_R6_2_DisabledMonitorsLookNothingUp(t *testing.T) {
	t.Run("updates off", func(t *testing.T) {
		f := newS099Host()
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log, s097TrayCfg(t, "monitors:\n  updates:\n    enabled: false\n"), f.health())
		if r.Updates != nil || countOf(f.lookedUp(), "emerge") != 0 {
			t.Errorf("updates disabled: Updates = %+v, emerge looked up %d times", r.Updates, countOf(f.lookedUp(), "emerge"))
		}
		if r.GLSA == nil {
			t.Error("updates disabled took the GLSA command with it")
		}
	})
	t.Run("glsa off", func(t *testing.T) {
		f := newS099Host()
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log, s097TrayCfg(t, "monitors:\n  glsa:\n    enabled: false\n"), f.health())
		if r.GLSA != nil || countOf(f.lookedUp(), "glsa-check") != 0 {
			t.Errorf("glsa disabled: GLSA = %+v, glsa-check looked up %d times", r.GLSA, countOf(f.lookedUp(), "glsa-check"))
		}
		if r.Updates == nil {
			t.Error("glsa disabled took the updates command with it")
		}
	})
	t.Run("sync off", func(t *testing.T) {
		f := newS099Host()
		log, buf := s097Logger()
		r := resolveHealth(context.Background(), log, s097TrayCfg(t, "monitors:\n  sync:\n    enabled: false\n"), f.health())
		repos, syncs := f.asked()
		if len(r.Repos) != 0 || len(repos) != 0 || len(syncs) != 0 {
			t.Errorf("sync disabled: Repos %+v, SyncedRepos asked %q, SyncTime asked %q; want nothing", r.Repos, repos, syncs)
		}
		if l := s099NewLines(t, strings.Split(buf.String(), "\n")); len(l) != 0 {
			t.Errorf("start-up lines %q with sync disabled, want none", l)
		}
	})
	t.Run("updates and glsa off", func(t *testing.T) {
		f := newS099Host()
		log, _ := s097Logger()
		r := resolveHealth(context.Background(), log,
			s097TrayCfg(t, "monitors:\n  updates:\n    enabled: false\n  glsa:\n    enabled: false\n"), f.health())
		if r.Updates != nil || r.GLSA != nil {
			t.Errorf("Updates = %+v, GLSA = %+v, want both nil", r.Updates, r.GLSA)
		}
		if l := f.lookedUp(); len(l) != 0 {
			t.Errorf("looked up %q with both commands disabled, want nothing (wrappers included)", l)
		}
		if r.RepoRevisions != "" {
			t.Errorf("RepoRevisions = %q with no check to trigger, want empty (R1.10)", r.RepoRevisions)
		}
	})
}

func TestS099Probe_NilCapabilitiesAreAbsent(t *testing.T) {
	f := newS099Host()
	log, buf := s097Logger()
	h := f.s097Host.health() // no LookPath, SyncedRepos or SyncTime
	r := resolveHealth(context.Background(), log, config.TrayConfig{}, h)
	if r.Updates != nil || r.GLSA != nil || len(r.Repos) != 0 {
		t.Errorf("nil capabilities: Updates %+v, GLSA %+v, Repos %+v; want all absent", r.Updates, r.GLSA, r.Repos)
	}
	if l := s099NewLines(t, strings.Split(buf.String(), "\n")); len(l) != 0 {
		t.Errorf("start-up lines %q for absent capabilities, want none", l)
	}
	// story 097's PORTAGE_TMPDIR / DISTDIR questions are unchanged
	if a, b := f.tmpAsked.Load(), f.distAsked.Load(); a != 1 || b != 1 {
		t.Errorf("portageq asked TMPDIR %d and DISTDIR %d times, want once each as in story 097", a, b)
	}
}
