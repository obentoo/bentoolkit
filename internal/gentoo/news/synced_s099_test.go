package news_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/news"
)

// Story 099, sub-task 2.1: SyncedRepos lists every repository repos.conf
// defines with a non-empty sync-uri (R4.1), reading the file or every *.conf
// of the directory in lexical order with the same section parser as
// RepoLocation, later files overriding earlier keys; SyncTime is the newest
// modification time of <location>/metadata/timestamp.chk and
// <location>/.git/FETCH_HEAD (R4.2), not-ok when neither exists (R4.3); a
// repos.conf that cannot be read is an error naming the path (R4.8). Every
// fixture lives under t.TempDir: no host /etc/portage or /var/db/repos (Q18).

// s099Repos renders repos as "name=location", in order, so a mismatch reads
// at a glance and the test does not depend on Repo carrying only two fields.
func s099Repos(repos []news.Repo) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		out = append(out, r.Name+"="+r.Location)
	}
	return out
}

// s099Synced calls SyncedRepos and fails on an error.
func s099Synced(t *testing.T, reposConf string) []string {
	t.Helper()
	repos, err := news.SyncedRepos(reposConf)
	if err != nil {
		t.Fatalf("SyncedRepos(%s): %v", reposConf, err)
	}
	return s099Repos(repos)
}

// TestS099SyncedRepos_DistinctSectionsStayApart is the hostile half that would
// wrongly collapse: sections whose names are near-identical, or that share a
// location, are different repositories; and a section's sync-uri never
// carries over to the section that follows it.
func TestS099SyncedRepos_DistinctSectionsStayApart(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, conf, ""+
		"[bentoo]\nsync-uri = https://example.org/bentoo.git\nlocation = /srv/bentoo\n\n"+
		"[bentoo-extra]\nsync-uri = https://example.org/extra.git\nlocation = /srv/bentoo-extra\n\n"+
		"[Bentoo]\nsync-uri = https://example.org/upper.git\nlocation = /srv/upper\n\n"+
		"[mirror]\nsync-uri = https://example.org/mirror.git\nlocation = /srv/bentoo\n\n"+
		"[local]\nlocation = /srv/local\n")
	got := s099Synced(t, conf)
	want := []string{"Bentoo=/srv/upper", "bentoo=/srv/bentoo", "bentoo-extra=/srv/bentoo-extra", "mirror=/srv/bentoo"}
	if !slices.Equal(got, want) {
		t.Errorf("SyncedRepos = %q, want %q (distinct sections, sorted by name; [local] has no sync-uri)", got, want)
	}
}

// TestS099SyncedRepos_OneSectionAcrossFilesIsOneRepo is the hostile half that
// would wrongly split: the same section in two files, or twice in one file,
// is one repository whose keys merge, the later definition of a key winning.
// A sync-uri from the first file and a location from the second still make a
// synced repository, in both orders.
func TestS099SyncedRepos_OneSectionAcrossFilesIsOneRepo(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"later file overrides location", map[string]string{
			"a.conf": "[bentoo]\nsync-uri = https://example.org/bentoo.git\nlocation = /srv/first\n",
			"b.conf": "[bentoo]\nlocation = /srv/second\n",
		}, []string{"bentoo=/srv/second"}},
		{"sync-uri first, location later", map[string]string{
			"10-uri.conf": "[bentoo]\nsync-uri = https://example.org/bentoo.git\n",
			"20-loc.conf": "[bentoo]\nlocation = /srv/bentoo\n",
		}, []string{"bentoo=/srv/bentoo"}},
		{"location first, sync-uri later", map[string]string{
			"10-loc.conf": "[bentoo]\nlocation = /srv/bentoo\n",
			"20-uri.conf": "[bentoo]\nsync-uri = https://example.org/bentoo.git\n",
		}, []string{"bentoo=/srv/bentoo"}},
		{"lexical, not creation, order", map[string]string{
			"z.conf": "[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /srv/z\n",
			"a.conf": "[gentoo]\nlocation = /srv/a\n",
		}, []string{"gentoo=/srv/z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "repos.conf")
			for name, body := range tc.files {
				mustWrite(t, filepath.Join(dir, name), body)
			}
			if got := s099Synced(t, dir); !slices.Equal(got, tc.want) {
				t.Errorf("SyncedRepos = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("same section twice in one file", func(t *testing.T) {
		conf := filepath.Join(t.TempDir(), "repos.conf")
		mustWrite(t, conf, "[bentoo]\nsync-uri = https://example.org/bentoo.git\n\n"+
			"[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /srv/gentoo\n\n"+
			"[bentoo]\nlocation = /srv/bentoo\n")
		want := []string{"bentoo=/srv/bentoo", "gentoo=/srv/gentoo"}
		if got := s099Synced(t, conf); !slices.Equal(got, want) {
			t.Errorf("SyncedRepos = %q, want %q", got, want)
		}
	})
}

// TestS099SyncedRepos_KeySpellingsAreOneKey is the same converse for keys: the
// parser RepoLocation uses reads keys case-insensitively, with "=" or ":" and
// any spacing, so each spelling of sync-uri is the one key.
func TestS099SyncedRepos_KeySpellingsAreOneKey(t *testing.T) {
	for name, line := range map[string]string{
		"no spaces":   "sync-uri=https://example.org/b.git",
		"colon":       "sync-uri : https://example.org/b.git",
		"upper case":  "SYNC-URI = https://example.org/b.git",
		"mixed, tabs": "\tSync-Uri\t=\thttps://example.org/b.git",
	} {
		t.Run(name, func(t *testing.T) {
			conf := filepath.Join(t.TempDir(), "repos.conf")
			mustWrite(t, conf, "# comment\n; comment\n[bentoo]\n"+line+"\nLOCATION = /srv/bentoo\n")
			want := []string{"bentoo=/srv/bentoo"}
			if got := s099Synced(t, conf); !slices.Equal(got, want) {
				t.Errorf("SyncedRepos = %q, want %q", got, want)
			}
		})
	}
}

// TestS099SyncedRepos_UnsyncedSectionsAreSkipped is R4.1's filter and the
// out-of-scope rule: a section without a sync-uri, with an empty one, with
// only sync-type, or with a sync-uri named only in a comment is not watched;
// nor is one without a location. A file with no synced section yields none.
func TestS099SyncedRepos_UnsyncedSectionsAreSkipped(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, conf, ""+
		"[DEFAULT]\nmain-repo = gentoo\n\n"+
		"[nouri]\nlocation = /srv/nouri\n\n"+
		"[emptyuri]\nsync-uri =\nlocation = /srv/emptyuri\n\n"+
		"[typeonly]\nsync-type = git\nlocation = /srv/typeonly\n\n"+
		"[commented]\n# sync-uri = https://example.org/c.git\nlocation = /srv/commented\n\n"+
		"[nolocation]\nsync-uri = https://example.org/n.git\n\n"+
		"[kept]\nsync-type = git\nsync-uri = https://example.org/kept.git\nlocation = /srv/kept\n")
	want := []string{"kept=/srv/kept"}
	if got := s099Synced(t, conf); !slices.Equal(got, want) {
		t.Errorf("SyncedRepos = %q, want %q", got, want)
	}

	none := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, none, "[local]\nlocation = /srv/local\n")
	if got := s099Synced(t, none); len(got) != 0 {
		t.Errorf("SyncedRepos = %q for a repos.conf with no sync-uri, want none", got)
	}
}

// TestS099SyncedRepos_DirectoryReadsOnlyConfFiles is R4.1's directory form:
// only *.conf entries count; hidden files, other suffixes and subdirectories
// named *.conf are not configuration.
func TestS099SyncedRepos_DirectoryReadsOnlyConfFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, filepath.Join(dir, "gentoo.conf"), "[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /srv/gentoo\n")
	mustWrite(t, filepath.Join(dir, ".hidden.conf"), "[hidden]\nsync-uri = https://example.org/h.git\nlocation = /srv/hidden\n")
	mustWrite(t, filepath.Join(dir, "zz-backup.conf.bak"), "[backup]\nsync-uri = https://example.org/b.git\nlocation = /srv/backup\n")
	mustWrite(t, filepath.Join(dir, "README"), "[readme]\nsync-uri = https://example.org/r.git\nlocation = /srv/readme\n")
	mustWrite(t, filepath.Join(dir, "nested.conf", "inner.conf"), "[nested]\nsync-uri = https://example.org/n.git\nlocation = /srv/nested\n")
	want := []string{"gentoo=/srv/gentoo"}
	if got := s099Synced(t, dir); !slices.Equal(got, want) {
		t.Errorf("SyncedRepos = %q, want %q", got, want)
	}
}

// TestS099SyncedRepos_UnreadableFileIsAnErrorNamingIt is R4.8 and Q4: a
// *.conf entry that cannot be read (here a dangling symlink, which fails the
// same way for every user) is an error that names its path and keeps the
// cause reachable with errors.Is.
func TestS099SyncedRepos_UnreadableFileIsAnErrorNamingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, filepath.Join(dir, "gentoo.conf"), "[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /srv/gentoo\n")
	broken := filepath.Join(dir, "overlay.conf")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), broken); err != nil {
		t.Fatal(err)
	}
	_, err := news.SyncedRepos(dir)
	if err == nil {
		t.Fatal("SyncedRepos read a directory with an unreadable overlay.conf without an error (R4.8)")
	}
	if !strings.Contains(err.Error(), broken) {
		t.Errorf("error %q does not name the unreadable file %s", err, broken)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error %q does not wrap its cause with %%w (errors.Is fs.ErrNotExist is false)", err)
	}
}

// TestS099SyncedRepos_UnreadableSingleFileIsAnError is R4.8 for the file
// form: a repos.conf file without read permission is an error naming it.
func TestS099SyncedRepos_UnreadableSingleFileIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 file; the permission case needs an unprivileged user")
	}
	conf := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, conf, "[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /srv/gentoo\n")
	if err := os.Chmod(conf, 0); err != nil {
		t.Fatal(err)
	}
	_, err := news.SyncedRepos(conf)
	if err == nil {
		t.Fatal("SyncedRepos read an unreadable repos.conf without an error (R4.8)")
	}
	if !strings.Contains(err.Error(), conf) || !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error %q does not name %s and wrap the permission error", err, conf)
	}
}

// TestS099RepoLocation_BehaviourUnchanged pins RepoLocation over the fixture
// shape SyncedRepos now shares its parser with: the exact section only, keys
// read case-insensitively, an empty location never overriding, the later file
// winning, and the default location for a section without one. For every
// synced repository, both functions agree on the location.
func TestS099RepoLocation_BehaviourUnchanged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos.conf")
	mustWrite(t, filepath.Join(dir, "a.conf"), ""+
		"[gentoo]\nsync-uri = rsync://example.org/gentoo\nLOCATION : /srv/gentoo-first\n\n"+
		"[bentoo]\nsync-uri = https://example.org/bentoo.git\nlocation = /srv/bentoo\n\n"+
		"[bentoo-extra]\nlocation = /srv/extra\n\n"+
		"[nolocation]\nsync-uri = https://example.org/n.git\n")
	mustWrite(t, filepath.Join(dir, "b.conf"), ""+
		"[gentoo]\nlocation = /srv/gentoo\n\n"+
		"[bentoo]\nlocation =\n")
	for name, want := range map[string]string{
		"gentoo":       "/srv/gentoo",
		"bentoo":       "/srv/bentoo",
		"bentoo-extra": "/srv/extra",
		"nolocation":   filepath.Join(news.DefaultReposDir, "nolocation"),
		"absent":       filepath.Join(news.DefaultReposDir, "absent"),
	} {
		got, err := news.RepoLocation(dir, name)
		if err != nil {
			t.Errorf("RepoLocation(%s): %v", name, err)
		}
		if got != want {
			t.Errorf("RepoLocation(%s) = %q, want %q (behaviour must not change)", name, got, want)
		}
	}
	repos, err := news.SyncedRepos(dir)
	if err != nil {
		t.Fatalf("SyncedRepos: %v", err)
	}
	if len(repos) == 0 {
		t.Fatal("SyncedRepos found no repository in a fixture with two synced ones")
	}
	for _, r := range repos {
		if loc, _ := news.RepoLocation(dir, r.Name); loc != r.Location {
			t.Errorf("SyncedRepos gives %s at %q but RepoLocation at %q; the parser is not shared", r.Name, r.Location, loc)
		}
	}
}

// s099Base is the reference instant the SyncTime fixtures are dated from,
// whole seconds so every filesystem stores it exactly.
var s099Base = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// s099Touch writes path and sets its modification time to mtime.
func s099Touch(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	mustWrite(t, path, content)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestS099SyncTime_NewestOfTheTwoFiles is R4.2 in both directions: the newer
// file wins whichever of the two it is, so neither file is preferred
// blindly. A git overlay has only FETCH_HEAD; an rsync tree only
// timestamp.chk. The time is the file's modification time, never the date
// written inside timestamp.chk.
func TestS099SyncTime_NewestOfTheTwoFiles(t *testing.T) {
	older, newer := s099Base.Add(-8*24*time.Hour), s099Base
	cases := []struct {
		name           string
		timestamp, git *time.Time
		want           time.Time
	}{
		{"FETCH_HEAD newer", &older, &newer, newer},
		{"timestamp.chk newer", &newer, &older, newer},
		{"only FETCH_HEAD (git overlay)", nil, &older, older},
		{"only timestamp.chk (rsync tree)", &older, nil, older},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc := t.TempDir()
			if tc.timestamp != nil {
				s099Touch(t, filepath.Join(loc, "metadata", "timestamp.chk"), "Thu, 01 Jan 2004 00:00:00 +0000\n", *tc.timestamp)
			}
			if tc.git != nil {
				s099Touch(t, filepath.Join(loc, ".git", "FETCH_HEAD"), "0000000000000000000000000000000000000000\t\tbranch 'master'\n", *tc.git)
			}
			got, ok, err := news.SyncTime(loc)
			if err != nil {
				t.Fatalf("SyncTime: %v", err)
			}
			if !ok {
				t.Fatal("SyncTime ok = false with a sync file present")
			}
			if !got.Equal(tc.want) {
				t.Errorf("SyncTime = %v, want %v (the newest modification time)", got, tc.want)
			}
		})
	}
}

// TestS099SyncTime_NeitherFileIsNotOK is R4.3: with neither file, or with no
// repository directory at all, the result is not ok and not an error; files
// of the right name in the wrong place do not count.
func TestS099SyncTime_NeitherFileIsNotOK(t *testing.T) {
	loc := t.TempDir()
	s099Touch(t, filepath.Join(loc, "timestamp.chk"), "x\n", s099Base)
	s099Touch(t, filepath.Join(loc, "FETCH_HEAD"), "x\n", s099Base)
	s099Touch(t, filepath.Join(loc, "metadata", "timestamp.x"), "x\n", s099Base)
	for name, path := range map[string]string{
		"misplaced files": loc,
		"missing dir":     filepath.Join(t.TempDir(), "gone"),
	} {
		t.Run(name, func(t *testing.T) {
			got, ok, err := news.SyncTime(path)
			if err != nil {
				t.Fatalf("SyncTime: %v, want no error when neither file exists", err)
			}
			if ok {
				t.Errorf("SyncTime = %v, ok = true; want ok = false when neither file exists", got)
			}
		})
	}
}

// TestS099SyncTime_OtherStatErrorIsWrapped is Q4: a stat failure other than
// not-exist is an error naming the path and wrapping the cause, even when the
// other file is readable, so a broken repository is never silently dated
// from half its evidence.
func TestS099SyncTime_OtherStatErrorIsWrapped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; the case needs an unprivileged user")
	}
	loc := t.TempDir()
	s099Touch(t, filepath.Join(loc, ".git", "FETCH_HEAD"), "x\n", s099Base)
	metadata := filepath.Join(loc, "metadata")
	s099Touch(t, filepath.Join(metadata, "timestamp.chk"), "x\n", s099Base)
	if err := os.Chmod(metadata, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(metadata, 0o755) })

	_, _, err := news.SyncTime(loc)
	if err == nil {
		t.Fatal("SyncTime hid a permission error on metadata/timestamp.chk")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("error %q does not wrap the permission error with %%w", err)
	}
	if !strings.Contains(err.Error(), filepath.Join(metadata, "timestamp.chk")) {
		t.Errorf("error %q does not name %s", err, filepath.Join(metadata, "timestamp.chk"))
	}
}

// FuzzSyncedRepos is Q17 over one repos.conf file body: it never panics or
// errors on a readable file, returns repositories sorted by name without a
// repeat, each with a location, and agrees with RepoLocation on every
// location, since both read through one section parser.
func FuzzSyncedRepos(f *testing.F) {
	for _, seed := range []string{
		"",
		"[gentoo]\nsync-uri = rsync://example.org/gentoo\nlocation = /var/db/repos/gentoo\n",
		"[DEFAULT]\nmain-repo = gentoo\n[b]\nSYNC-URI: x\nLOCATION : /b\n[a]\nsync-uri=y\nlocation=/a\n[b]\nlocation=/b2\n",
		"[]\nsync-uri = x\nlocation = /empty\n",
		"[a]\nsync-uri = x\nlocation = /l\nlocation =\n",
		"[\n]\n[[x]]\nsync-uri\n=\n;x\n#y\n\r\n[a] \nlocation=\t/a\r\nsync-uri=u\r\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1<<20 {
			t.Skip("larger than the 1 MiB a repos.conf file may have")
		}
		conf := filepath.Join(t.TempDir(), "repos.conf")
		if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		repos, err := news.SyncedRepos(conf)
		if err != nil {
			t.Fatalf("SyncedRepos failed on a readable file: %v\nbody: %q", err, body)
		}
		for i, r := range repos {
			if r.Location == "" {
				t.Errorf("repository %q has an empty location\nbody: %q", r.Name, body)
			}
			if i > 0 && repos[i-1].Name >= r.Name {
				t.Errorf("repositories not sorted by name without a repeat: %q then %q\nbody: %q", repos[i-1].Name, r.Name, body)
			}
			if loc, _ := news.RepoLocation(conf, r.Name); loc != r.Location {
				t.Errorf("SyncedRepos gives %q at %q, RepoLocation at %q\nbody: %q", r.Name, r.Location, loc, body)
			}
		}
	})
}
