package notice

// Story 071, sub-task 6.1: Revise reads a published notice back, opens its
// text in the editor, applies field changes validated as in New, bumps the
// revision markers of both files together and replaces each file atomically
// (R5.1 to R5.7, Q13).
//
// The contract assumed here:
//
//	type Changes struct{ Severity, Title, Summary string; Affects []string } // "" / nil = not given
//	type BodyEditor func(ctx context.Context, current string) (string, error)
//
// Hostile halves first, for R5.6 ("unchanged → write nothing"):
//   - wrongly split: the news item stores the text WRAPPED at 72 columns while
//     the site file stores it as written. An editor that hands back exactly
//     what it was given, or adds a trailing newline, has changed nothing;
//   - wrongly collapse: a one-character edit is a change and must be written.
// And for reading the item back: a text line that looks like a header
// ("Revision: 9") is text, not the revision.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

var revisedAtS071 = time.Date(2026, 10, 5, 9, 30, 0, 0, time.FixedZone("UTC-3", -3*3600))

const longParaS071 = "This paragraph is deliberately much longer than seventy-two columns so that the news item has to wrap it while the site file keeps it on one line, version 1.2.3."

type reviseFixtureS071 struct {
	overlay, site string
	n             Notice
	newsPath      string
	sitePath      string
	newsBefore    string
	siteBefore    string
}

func reviseSetupS071(t *testing.T, withSite bool, body string) reviseFixtureS071 {
	t.Helper()
	f := reviseFixtureS071{overlay: filepath.Join(t.TempDir(), "overlay")}
	if err := os.MkdirAll(filepath.Join(f.overlay, "metadata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if withSite {
		f.site = filepath.Join(t.TempDir(), "site")
		if err := os.MkdirAll(filepath.Join(f.site, "src", "content", "notices"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pub := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	f.n = Notice{
		ID: "2026-10-02-foo-cve", Type: "security", Severity: "warning",
		Title: "foo heap overflow", Summary: "Summary.", Author: "Jane Doe <jane@example.org>",
		Body:      body,
		Affects:   []Affects{{CP: "dev-libs/foo", Ranges: []Range{{Op: "<", Ver: "1.2.3"}}}},
		Published: pub, Updated: pub, Revision: 1,
	}
	if _, err := Publish(context.Background(), f.n, f.overlay, f.site); err != nil {
		t.Fatalf("Publish (fixture): %v", err)
	}
	f.newsPath = filepath.Join(f.overlay, "metadata", "news", f.n.ID, f.n.ID+".en.txt")
	f.newsBefore = readS071(t, f.newsPath)
	if withSite {
		f.sitePath = filepath.Join(f.site, "src", "content", "notices", f.n.ID+".yaml")
		f.siteBefore = readS071(t, f.sitePath)
	}
	return f
}

func readS071(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func (f reviseFixtureS071) assertUntouched(t *testing.T, why string) {
	t.Helper()
	if got := readS071(t, f.newsPath); got != f.newsBefore {
		t.Errorf("the news item changed: %s\n got %q", why, got)
	}
	if f.sitePath != "" {
		if got := readS071(t, f.sitePath); got != f.siteBefore {
			t.Errorf("the site file changed: %s\n got %q", why, got)
		}
	}
}

type siteDocS071 struct {
	ID, Type, Severity, Title, Summary, Body string
	Published, Updated                       string
	Affects                                  []struct {
		CP     string `yaml:"cp"`
		Ranges []struct{ Op, Ver string }
	}
}

func siteDocOfS071(t *testing.T, path string) siteDocS071 {
	t.Helper()
	var d siteDocS071
	if err := yaml.Unmarshal([]byte(readS071(t, path)), &d); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return d
}

func headerS071(text, name string) string {
	head, _, _ := strings.Cut(text, "\n\n")
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, name+": "); ok {
			return v
		}
	}
	return ""
}

func returnsS071(text string) BodyEditor {
	return func(context.Context, string) (string, error) { return text, nil }
}

func echoS071(suffix string) BodyEditor {
	return func(_ context.Context, current string) (string, error) { return current + suffix, nil }
}

func TestRevise_UnchangedWrappedTextWritesNothing(t *testing.T) {
	for _, withSite := range []bool{true, false} {
		for _, suffix := range []string{"", "\n"} {
			f := reviseSetupS071(t, withSite, longParaS071)
			sitePath := ""
			if withSite {
				sitePath = f.site
			}
			_, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, sitePath, echoS071(suffix), revisedAtS071)
			if !errors.Is(err, ErrNothingToRevise) {
				t.Errorf("site=%v suffix=%q: err = %v, want ErrNothingToRevise", withSite, suffix, err)
			}
			f.assertUntouched(t, "nothing was revised")
		}
	}
}

func TestRevise_OneCharacterEditIsARevision(t *testing.T) {
	f := reviseSetupS071(t, true, longParaS071)
	edited := strings.Replace(longParaS071, "1.2.3", "1.2.4", 1)

	if _, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, f.site, returnsS071(edited), revisedAtS071); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	news := readS071(t, f.newsPath)
	if !strings.Contains(news, "1.2.4") || headerS071(news, "Revision") != "2" {
		t.Errorf("the one-character edit was not written as revision 2:\n%s", news)
	}
	if d := siteDocOfS071(t, f.sitePath); strings.TrimRight(d.Body, "\n") != edited {
		t.Errorf("site body = %q, want %q", d.Body, edited)
	}
}

func TestRevise_WritesBothFilesAndBumpsTheirMarkers(t *testing.T) {
	f := reviseSetupS071(t, true, "Original text.")
	var seen string
	edit := func(_ context.Context, current string) (string, error) {
		seen = current
		return "Revised text.", nil
	}

	res, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, f.site, edit, revisedAtS071)
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	if strings.TrimSpace(seen) != "Original text." {
		t.Errorf("the editor was handed %q, want the current text", seen)
	}

	news := readS071(t, f.newsPath)
	if headerS071(news, "Revision") != "2" {
		t.Errorf("Revision = %q, want 2", headerS071(news, "Revision"))
	}
	for _, h := range []string{"Title", "Author", "Posted", "News-Item-Format"} {
		if headerS071(news, h) != headerS071(f.newsBefore, h) {
			t.Errorf("header %s changed from %q to %q", h, headerS071(f.newsBefore, h), headerS071(news, h))
		}
	}
	if _, body, _ := strings.Cut(news, "\n\n"); strings.TrimSpace(body) != "Revised text." {
		t.Errorf("news text = %q", body)
	}

	d := siteDocOfS071(t, f.sitePath)
	if d.ID != f.n.ID || d.Published != "2026-10-02T00:00:00Z" {
		t.Errorf("id/published changed: %q %q", d.ID, d.Published)
	}
	if d.Updated != "2026-10-05T12:30:00Z" {
		t.Errorf("updated = %q, want the revision time in UTC 2026-10-05T12:30:00Z", d.Updated)
	}
	if strings.TrimRight(d.Body, "\n") != "Revised text." {
		t.Errorf("site body = %q", d.Body)
	}
	if len(res.Paths) != 2 {
		t.Errorf("Result.Paths = %q, want both files", res.Paths)
	}

	// A second revision counts on from the first.
	if _, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, f.site, returnsS071("Third text."), revisedAtS071.Add(time.Hour)); err != nil {
		t.Fatalf("second Revise: %v", err)
	}
	if got := headerS071(readS071(t, f.newsPath), "Revision"); got != "3" {
		t.Errorf("after two revisions Revision = %q, want 3", got)
	}
}

func TestRevise_HeaderLookalikesInTheTextStayText(t *testing.T) {
	f := reviseSetupS071(t, false, "First paragraph.\n\nRevision: 9\nTitle: not the title")

	if _, err := Revise(context.Background(), f.n.ID, Changes{Title: "new title"}, f.overlay, "", echoS071(""), revisedAtS071); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	news := readS071(t, f.newsPath)
	if headerS071(news, "Revision") != "2" || headerS071(news, "Title") != "new title" {
		t.Errorf("headers were read from the text: Revision=%q Title=%q\n%s", headerS071(news, "Revision"), headerS071(news, "Title"), news)
	}
	if _, body, _ := strings.Cut(news, "\n\n"); !strings.Contains(body, "Revision: 9") || !strings.Contains(body, "Title: not the title") {
		t.Errorf("text lines were lost:\n%s", body)
	}
}

func TestRevise_FieldChangesReachBothFiles(t *testing.T) {
	f := reviseSetupS071(t, true, "Text.")
	ch := Changes{Severity: "critical", Title: "foo heap overflow, fixed", Summary: "New summary.", Affects: []string{"dev-libs/foo:1 >=2.0"}}

	if _, err := Revise(context.Background(), f.n.ID, ch, f.overlay, f.site, echoS071(""), revisedAtS071); err != nil {
		t.Fatalf("Revise with field changes and unchanged text: %v", err)
	}
	news := readS071(t, f.newsPath)
	if headerS071(news, "Title") != ch.Title || headerS071(news, "Display-If-Installed") != ">=dev-libs/foo-2.0:1" || headerS071(news, "Revision") != "2" {
		t.Errorf("news headers not updated:\n%s", news)
	}
	d := siteDocOfS071(t, f.sitePath)
	if d.Severity != "critical" || d.Title != ch.Title || d.Summary != "New summary." {
		t.Errorf("site fields not updated: %+v", d)
	}
	if len(d.Affects) != 1 || len(d.Affects[0].Ranges) != 1 || d.Affects[0].Ranges[0].Ver != "2.0" {
		t.Errorf("site affects not updated: %+v", d.Affects)
	}
}

func TestRevise_InvalidFieldChangesAreRefused(t *testing.T) {
	cases := []struct {
		ch   Changes
		want error
	}{
		{Changes{Severity: "urgent"}, ErrSeverity},
		{Changes{Title: strings.Repeat("t", 51)}, ErrTitle},
		{Changes{Affects: []string{"dev-libs/foo >=oops"}}, ErrAffects},
	}
	for _, c := range cases {
		f := reviseSetupS071(t, true, "Text.")
		_, err := Revise(context.Background(), f.n.ID, c.ch, f.overlay, f.site, returnsS071("Changed text."), revisedAtS071)
		if !errors.Is(err, c.want) {
			t.Errorf("%+v: err = %v, want %v", c.ch, err, c.want)
		}
		f.assertUntouched(t, "a rejected change writes nothing")
	}
}

func TestRevise_UnknownIDNamesTheExpectedPath(t *testing.T) {
	f := reviseSetupS071(t, true, "Text.")
	// Hostile: an existing ID that starts with the requested one is not it.
	id := "2026-10-02-foo"
	called := false
	edit := func(context.Context, string) (string, error) { called = true; return "x", nil }

	_, err := Revise(context.Background(), id, Changes{}, f.overlay, f.site, edit, revisedAtS071)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if want := filepath.Join(f.overlay, "metadata", "news", id, id+".en.txt"); !strings.Contains(err.Error(), want) {
		t.Errorf("the error %q does not name the expected path %s", err, want)
	}
	if called {
		t.Error("the editor opened for a notice that does not exist")
	}
	f.assertUntouched(t, "another notice was revised")
}

func TestRevise_MissingSiteFileChangesNothing(t *testing.T) {
	f := reviseSetupS071(t, true, "Text.")
	if err := os.Remove(f.sitePath); err != nil {
		t.Fatal(err)
	}
	f.sitePath = ""

	_, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, f.site, returnsS071("Changed."), revisedAtS071)
	if !errors.Is(err, ErrSiteMissing) {
		t.Fatalf("err = %v, want ErrSiteMissing", err)
	}
	if want := filepath.Join(f.site, "src", "content", "notices", f.n.ID+".yaml"); !strings.Contains(err.Error(), want) {
		t.Errorf("the error %q does not name %s", err, want)
	}
	f.assertUntouched(t, "the site file is missing")
	if entries, _ := os.ReadDir(filepath.Join(f.site, "src", "content", "notices")); len(entries) != 0 {
		t.Errorf("a site file was created: %v", entries)
	}
}

func TestRevise_EditorFailureChangesNothing(t *testing.T) {
	f := reviseSetupS071(t, true, "Text.")
	boom := errors.New("editor exploded")
	edit := func(context.Context, string) (string, error) { return "", boom }

	_, err := Revise(context.Background(), f.n.ID, Changes{Title: "new"}, f.overlay, f.site, edit, revisedAtS071)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap the editor's error", err)
	}
	f.assertUntouched(t, "the editor failed")
}

// Q13 / R5.7: a revision interrupted while replacing the site file leaves the
// previous site file intact and no temporary file beside it, and the news
// item holds either its old or its complete new content.
func TestRevise_InterruptedReplacementKeepsThePreviousContent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := reviseSetupS071(t, true, "Text.")
	notices := filepath.Join(f.site, "src", "content", "notices")
	if err := os.Chmod(notices, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(notices, 0o755) })

	if _, err := Revise(context.Background(), f.n.ID, Changes{}, f.overlay, f.site, returnsS071("Changed."), revisedAtS071); err == nil {
		t.Fatal("Revise succeeded although the site directory is read-only")
	}
	if got := readS071(t, f.sitePath); got != f.siteBefore {
		t.Errorf("the site file was altered by an interrupted revision:\n%s", got)
	}
	if entries, _ := os.ReadDir(notices); len(entries) != 1 {
		t.Errorf("the notices directory holds %d entries, want only the site file (no temporary left)", len(entries))
	}
	news := readS071(t, f.newsPath)
	if news != f.newsBefore && (headerS071(news, "Revision") != "2" || !strings.Contains(news, "Changed.")) {
		t.Errorf("the news item is neither the old nor the complete new content:\n%s", news)
	}
	newsDir := filepath.Dir(f.newsPath)
	if entries, _ := os.ReadDir(newsDir); len(entries) != 1 {
		t.Errorf("the news item directory holds %d entries, want only the item", len(entries))
	}
}
