package news_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/notices"
)

// testHost is the configured feed URL's host passed to NewReader.
const testHost = "obentoo.org"

// newsItem renders a GLEP 42 news item with the given title. The body carries
// a decoy "Title:" line that is not a header.
func newsItem(title string) string {
	return "Title: " + title + "\n" +
		"Author: Bentoo Maintainer <maint@obentoo.org>\n" +
		"Posted: 2026-10-02\n" +
		"Revision: 1\n" +
		"News-Item-Format: 2.0\n" +
		"\n" +
		"Title: this body line is not the header\n" +
		"Body text.\n"
}

// fixture lays out an unread list and a repository with news items.
type fixture struct {
	unread, repo string
}

func newFixture(t *testing.T, unreadContent string, items map[string]string) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{unread: filepath.Join(base, "news", "news-bentoo.unread"), repo: filepath.Join(base, "repo")}
	mustWrite(t, f.unread, unreadContent)
	for id, content := range items {
		mustWrite(t, filepath.Join(f.repo, "metadata", "news", id, id+".en.txt"), content)
	}
	return f
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byID(ns []notices.Notice) map[string]notices.Notice {
	m := map[string]notices.Notice{}
	for _, n := range ns {
		m[n.ID] = n
	}
	return m
}

// TestUnread_EachListedIDBecomesANewsNotice is R4.1/R4.2: one notice per
// unread ID, titled from the item's Title header, type news, severity info.
func TestUnread_EachListedIDBecomesANewsNotice(t *testing.T) {
	f := newFixture(t, "2026-08-02-nodejs-slotted\n\n2026-10-02-foo-gone\n", map[string]string{
		"2026-08-02-nodejs-slotted": newsItem("Node.js is now slotted"),
		"2026-10-02-foo-gone":       newsItem("foo & <bar> removed"),
	})
	got, err := news.NewReader(f.unread, f.repo, testHost, nil).Unread(context.Background())
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Unread = %+v, want two notices (a blank line is not an ID)", got)
	}
	m := byID(got)
	want := map[string]string{
		"2026-08-02-nodejs-slotted": "Node.js is now slotted",
		"2026-10-02-foo-gone":       "foo & <bar> removed",
	}
	for id, title := range want {
		n, ok := m[id]
		if !ok {
			t.Errorf("missing %s", id)
			continue
		}
		if n.Title != title {
			t.Errorf("%s title = %q, want %q (the header, verbatim)", id, n.Title, title)
		}
		if n.Type != "news" || n.Severity != "info" {
			t.Errorf("%s type/severity = %q/%q, want news/info", id, n.Type, n.Severity)
		}
		if n.Source != notices.SourceNews {
			t.Errorf("%s source = %v, want SourceNews", id, n.Source)
		}
		// Posted: 2026-10-02 is the item's date, at midnight UTC.
		posted := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
		if !n.Published.Equal(posted) || !n.Updated.Equal(posted) {
			t.Errorf("%s published/updated = %v/%v, want %v from the Posted header", id, n.Published, n.Updated, posted)
		}
	}
}

// TestUnread_TitleComesFromTheHeaderOnly: the Title header may follow other
// headers, and a "Title:" line in the body is not a header.
func TestUnread_TitleComesFromTheHeaderOnly(t *testing.T) {
	item := "Author: Bentoo Maintainer <maint@obentoo.org>\n" +
		"Posted: 2026-10-02\n" +
		"Title: The real title\n" +
		"News-Item-Format: 2.0\n" +
		"\n" +
		"Title: decoy in the body\n"
	f := newFixture(t, "2026-10-02-order\n", map[string]string{"2026-10-02-order": item})
	got, err := news.NewReader(f.unread, f.repo, testHost, nil).Unread(context.Background())
	if err != nil {
		t.Fatalf("Unread: %v", err)
	}
	if len(got) != 1 || got[0].Title != "The real title" {
		t.Errorf("Unread = %+v, want one notice titled \"The real title\"", got)
	}
}

// TestUnread_MissingUnreadListIsAnErrorNamingIt is R4.4's trigger.
func TestUnread_MissingUnreadListIsAnErrorNamingIt(t *testing.T) {
	base := t.TempDir()
	unread := filepath.Join(base, "news-bentoo.unread")
	got, err := news.NewReader(unread, filepath.Join(base, "repo"), testHost, nil).Unread(context.Background())
	if err == nil {
		t.Fatalf("Unread of a missing list returned %+v and no error", got)
	}
	if !strings.Contains(err.Error(), unread) {
		t.Errorf("error %q does not name %s", err, unread)
	}
	if len(got) != 0 {
		t.Errorf("Unread returned notices %+v without a list", got)
	}
}

// TestUnread_MissingItemIsReportedAndOthersSurvive is R4.4 for one item: the
// error names the item's path, and the readable items are still returned.
func TestUnread_MissingItemIsReportedAndOthersSurvive(t *testing.T) {
	f := newFixture(t, "2026-10-02-present\n2026-10-02-absent\n", map[string]string{
		"2026-10-02-present": newsItem("Present"),
	})
	got, err := news.NewReader(f.unread, f.repo, testHost, nil).Unread(context.Background())
	if err == nil {
		t.Fatal("a missing news item produced no error, so nothing can WARN about it")
	}
	missing := filepath.Join(f.repo, "metadata", "news", "2026-10-02-absent")
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the missing item's path under %s", err, missing)
	}
	m := byID(got)
	if m["2026-10-02-present"].Title != "Present" {
		t.Errorf("the readable item was lost: %+v", got)
	}
	if n, ok := m["2026-10-02-absent"]; !ok || n.Title != "2026-10-02-absent" {
		t.Errorf("a missing item = %+v (present %v), want it kept with its ID as the title", n, ok)
	}
}

// TestUnread_IDIsNeverAPath is the hostile half of "each line is an ID": a
// line that walks out of metadata/news must not be read.
func TestUnread_IDIsNeverAPath(t *testing.T) {
	f := newFixture(t, "../../../outside\n2026-10-02-ok\n", map[string]string{
		"2026-10-02-ok": newsItem("OK"),
	})
	// The file a traversal would reach: repo/metadata/news/../../../outside/outside.en.txt
	mustWrite(t, filepath.Join(filepath.Dir(f.repo), "outside", "outside.en.txt"), newsItem("LEAKED"))
	got, _ := news.NewReader(f.unread, f.repo, testHost, nil).Unread(context.Background())
	for _, n := range got {
		if n.Title == "LEAKED" || strings.Contains(n.ID, "/") {
			t.Fatalf("Unread followed a path in the unread list: %+v", n)
		}
	}
	if byID(got)["2026-10-02-ok"].Title != "OK" {
		t.Errorf("the legitimate item was lost: %+v", got)
	}
}

// TestUnread_NeverWritesPortageFiles is R4.5: read-only files stay readable
// and byte- and mtime-identical after a read.
func TestUnread_NeverWritesPortageFiles(t *testing.T) {
	f := newFixture(t, "2026-10-02-ro\n", map[string]string{"2026-10-02-ro": newsItem("RO")})
	item := filepath.Join(f.repo, "metadata", "news", "2026-10-02-ro", "2026-10-02-ro.en.txt")
	before := map[string]os.FileInfo{}
	for _, p := range []string{f.unread, item} {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = fi
	}
	got, err := news.NewReader(f.unread, f.repo, testHost, nil).Unread(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("Unread of read-only files = %+v, %v", got, err)
	}
	for p, fi := range before {
		after, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) || after.Mode() != fi.Mode() {
			t.Errorf("%s changed during Unread", p)
		}
	}
}

// --- RepoLocation ---

func writeConf(t *testing.T, path, content string) { t.Helper(); mustWrite(t, path, content) }

// TestRepoLocation_OnlyTheExactSectionCounts is the hostile half, authored
// first: sections named like bentoo, or another repository's location, must
// not be taken for [bentoo].
func TestRepoLocation_OnlyTheExactSectionCounts(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "repos.conf")
	writeConf(t, conf, "[DEFAULT]\nmain-repo = gentoo\n\n"+
		"[bentoo-extra]\nlocation = /srv/wrong-extra\n\n"+
		"[gentoo]\nlocation = /var/db/repos/gentoo\n\n"+
		"[bentoo]\nsync-type = git\nlocation = /srv/bentoo\n\n"+
		"[xbentoo]\nlocation = /srv/wrong-x\n")
	got, err := news.RepoLocation(conf, "bentoo")
	if err != nil {
		t.Fatalf("RepoLocation: %v", err)
	}
	if got != "/srv/bentoo" {
		t.Errorf("RepoLocation = %q, want /srv/bentoo", got)
	}
	if g, _ := news.RepoLocation(conf, "gentoo"); g != "/var/db/repos/gentoo" {
		t.Errorf("RepoLocation(gentoo) = %q; the name argument is not honoured", g)
	}
}

// TestRepoLocation_SpacingVariantsAreTheSameKey is the converse: INI spacing
// differences denote the same key and value.
func TestRepoLocation_SpacingVariantsAreTheSameKey(t *testing.T) {
	for name, body := range map[string]string{
		"no spaces":        "[bentoo]\nlocation=/srv/bentoo\n",
		"extra spaces":     "[bentoo]\n  location   =   /srv/bentoo  \n",
		"comment and tabs": "# comment\n[bentoo]\n\tlocation\t=\t/srv/bentoo\n",
	} {
		t.Run(name, func(t *testing.T) {
			conf := filepath.Join(t.TempDir(), "repos.conf")
			writeConf(t, conf, body)
			if got, err := news.RepoLocation(conf, "bentoo"); err != nil || got != "/srv/bentoo" {
				t.Errorf("RepoLocation = %q, %v; want /srv/bentoo", got, err)
			}
		})
	}
}

// TestRepoLocation_DirectoryForm: repos.conf may be a directory; every *.conf
// file in it is read and other files are ignored.
func TestRepoLocation_DirectoryForm(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos.conf")
	writeConf(t, filepath.Join(dir, "gentoo.conf"), "[gentoo]\nlocation = /var/db/repos/gentoo\n")
	writeConf(t, filepath.Join(dir, "eselect-repo.conf"), "[bentoo]\nlocation = /srv/bentoo\n")
	writeConf(t, filepath.Join(dir, "zz-backup.conf.bak"), "[bentoo]\nlocation = /srv/stale-backup\n")
	got, err := news.RepoLocation(dir, "bentoo")
	if err != nil {
		t.Fatalf("RepoLocation: %v", err)
	}
	if got != "/srv/bentoo" {
		t.Errorf("RepoLocation = %q, want /srv/bentoo (a non-.conf file is not config)", got)
	}
}

// TestRepoLocation_LaterFileOverridesEarlier follows portage: files are read in
// lexical order and a later definition of the same key wins.
func TestRepoLocation_LaterFileOverridesEarlier(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos.conf")
	writeConf(t, filepath.Join(dir, "a.conf"), "[bentoo]\nlocation = /srv/first\n")
	writeConf(t, filepath.Join(dir, "b.conf"), "[bentoo]\nlocation = /srv/second\n")
	if got, _ := news.RepoLocation(dir, "bentoo"); got != "/srv/second" {
		t.Errorf("RepoLocation = %q, want /srv/second (the lexically later file)", got)
	}
}

// TestRepoLocation_FallsBackToTheDefaultLocation: no repos.conf, or one without
// a [bentoo] section, means the default path.
func TestRepoLocation_FallsBackToTheDefaultLocation(t *testing.T) {
	base := t.TempDir()
	conf := filepath.Join(base, "repos.conf")
	writeConf(t, conf, "[gentoo]\nlocation = /var/db/repos/gentoo\n")
	for name, path := range map[string]string{
		"missing file": filepath.Join(base, "absent.conf"),
		"no [bentoo]":  conf,
		"empty dir":    t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := news.RepoLocation(path, "bentoo")
			if err != nil {
				t.Fatalf("RepoLocation: %v", err)
			}
			if got != "/var/db/repos/bentoo" {
				t.Errorf("RepoLocation = %q, want /var/db/repos/bentoo", got)
			}
		})
	}
}

// TestUnread_URLPointsAtTheFeedHost: a news-only notice links to its page on
// the configured feed host, so Open's host check (R7.3) accepts it.
func TestUnread_URLPointsAtTheFeedHost(t *testing.T) {
	f := newFixture(t, "2026-10-02-foo+bar\n", map[string]string{"2026-10-02-foo+bar": newsItem("Foo")})
	for _, host := range []string{"obentoo.org", "mirror.example.org"} {
		got, err := news.NewReader(f.unread, f.repo, host, nil).Unread(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("Unread = %+v, %v", got, err)
		}
		if want := "https://" + host + "/notices/2026-10-02-foo+bar/"; got[0].URL != want {
			t.Errorf("URL = %q, want %q", got[0].URL, want)
		}
	}
}

// changedWithin reports whether ch fires within d.
func changedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// newWatchedReader returns a reader over a fresh unread list, the tick channel
// that drives its poll, and the list's path. Sending on tick blocks until the
// reader takes the tick.
func newWatchedReader(t *testing.T) (*news.Reader, chan time.Time, string) {
	t.Helper()
	f := newFixture(t, "2026-10-02-a\n", map[string]string{"2026-10-02-a": newsItem("A")})
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(f.unread, old, old); err != nil {
		t.Fatal(err)
	}
	tick := make(chan time.Time)
	t.Cleanup(func() { close(tick) })
	return news.NewReader(f.unread, f.repo, testHost, tick), tick, f.unread
}

func sendTick(t *testing.T, tick chan time.Time) {
	t.Helper()
	select {
	case tick <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never took a tick from its tick channel")
	}
}

// TestChanged_QuietWhileTheListIsUnchanged is the hostile half of R4.1,
// authored first: a poll that finds nothing new must not wake the App.
func TestChanged_QuietWhileTheListIsUnchanged(t *testing.T) {
	r, tick, _ := newWatchedReader(t)
	ch := r.Changed()
	for i := 0; i < 3; i++ {
		sendTick(t, tick)
	}
	if changedWithin(ch, 300*time.Millisecond) {
		t.Error("Changed fired although the unread list never changed")
	}
}

// TestChanged_FiresOnTheFirstTickAfterAChange is R4.1: a change is reported on
// the next tick, once, and not again while nothing else changes. Size and
// mtime are each enough on their own.
func TestChanged_FiresOnTheFirstTickAfterAChange(t *testing.T) {
	t.Run("size and mtime", func(t *testing.T) {
		r, tick, path := newWatchedReader(t)
		ch := r.Changed()
		mustWrite(t, path, "2026-10-02-a\n2026-10-02-b\n")
		if changedWithin(ch, 200*time.Millisecond) {
			t.Fatal("Changed fired before any tick")
		}
		sendTick(t, tick)
		if !changedWithin(ch, 2*time.Second) {
			t.Fatal("Changed did not fire on the first tick after the list changed")
		}
		sendTick(t, tick)
		sendTick(t, tick)
		if changedWithin(ch, 300*time.Millisecond) {
			t.Error("Changed fired again with no further change")
		}
	})
	t.Run("mtime only", func(t *testing.T) {
		r, tick, path := newWatchedReader(t)
		ch := r.Changed()
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			t.Fatal(err)
		}
		sendTick(t, tick)
		if !changedWithin(ch, 2*time.Second) {
			t.Error("a new mtime with the same size was not reported")
		}
	})
	t.Run("size only", func(t *testing.T) {
		r, tick, path := newWatchedReader(t)
		ch := r.Changed()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, path, "2026-10-02-z\n2026-10-02-y\n")
		if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
			t.Fatal(err)
		}
		sendTick(t, tick)
		if !changedWithin(ch, 2*time.Second) {
			t.Error("a new size with the old mtime was not reported")
		}
	})
}

// TestChanged_StopsWhenTheTickCloses: closing the tick channel ends the poll
// goroutine, so a reader never outlives its ticker.
func TestChanged_StopsWhenTheTickCloses(t *testing.T) {
	f := newFixture(t, "2026-10-02-a\n", map[string]string{"2026-10-02-a": newsItem("A")})
	baseline := runtime.NumGoroutine()
	tick := make(chan time.Time)
	r := news.NewReader(f.unread, f.repo, testHost, tick)
	_ = r.Changed()
	sendTick(t, tick) // the poller is running
	close(tick)
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d after closing the tick, want back to %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
