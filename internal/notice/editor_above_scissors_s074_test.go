package notice

// Story 074: text typed ABOVE the scissors line is part of the body. Most
// editors open on line 1, so a user who writes there must not lose the text;
// only the `#` lines above the scissors line are instructions. Everything the
// scissors line guarantees (story 071, 9.1) still holds: text below it is
// verbatim, `#` lines included, and an untouched save is empty (new) or
// unchanged (revise).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// prependingS074 is an editor that inserts text at the very top of the file,
// where the cursor starts in most editors.
func prependingS074(t *testing.T, text string) Runner {
	t.Helper()
	return func(_ context.Context, _ string, args ...string) error {
		path := args[len(args)-1]
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, append([]byte(text), old...), 0o600)
	}
}

func TestAboveScissors_TextTypedAtTheTopIsTheBody(t *testing.T) {
	got, err := ReadBody(context.Background(), "", editorEnvS071,
		prependingS074(t, "Typed at the top.\n\nSecond paragraph.\n"))
	if err != nil {
		t.Fatalf("ReadBody: %v (the text typed above the scissors line was lost)", err)
	}
	if want := "Typed at the top.\n\nSecond paragraph."; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if strings.Contains(got, "Write the notice body") || strings.Contains(got, ">8") {
		t.Errorf("an instruction line leaked into the body: %q", got)
	}
}

func TestAboveScissors_AboveAndBelowAreKeptInOrder(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) error {
		path := args[len(args)-1]
		content := "Above.\n# a comment above is an instruction\n" + scissorsLine + "\n# Below keeps its hash\nBelow.\n"
		return os.WriteFile(path, []byte(content), 0o600)
	}
	got, err := ReadBody(context.Background(), "", editorEnvS071, run)
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if want := "Above.\n# Below keeps its hash\nBelow."; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestAboveScissors_UntouchedNewTemplateIsStillEmpty(t *testing.T) {
	if _, err := ReadBody(context.Background(), "", editorEnvS071, untouchedS071); !errors.Is(err, ErrEmptyBody) {
		t.Errorf("an untouched template: err = %v, want ErrEmptyBody", err)
	}
}

func reviseFixtureS074(t *testing.T) (overlay, site string, n Notice, before map[string]string) {
	t.Helper()
	overlay = filepath.Join(t.TempDir(), "overlay")
	site = filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(SiteNoticesDir(site), 0o755); err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	n = Notice{
		ID: "2026-09-28-above", Type: "news", Severity: "info", Title: "t", Summary: "s",
		Author: "A <a@b.c>", Body: hashBodyS071, Published: pub, Updated: pub, Revision: 1,
	}
	res, err := Publish(context.Background(), n, overlay, site)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	before = map[string]string{}
	for _, p := range res.Paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		before[p] = string(b)
	}
	return overlay, site, n, before
}

func TestAboveScissors_UntouchedReviseStillWritesNothing(t *testing.T) {
	overlay, site, n, before := reviseFixtureS074(t)
	_, err := Revise(context.Background(), n.ID, Changes{}, overlay, site,
		NewBodyEditor(editorEnvS071, untouchedS071), n.Published.Add(48*time.Hour))
	if !errors.Is(err, ErrNothingToRevise) {
		t.Errorf("an untouched revise: err = %v, want ErrNothingToRevise", err)
	}
	for p, want := range before {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Errorf("%s changed although nothing was edited", p)
		}
	}
}

func TestAboveScissors_ReviseKeepsALineTypedAtTheTop(t *testing.T) {
	overlay, site, n, _ := reviseFixtureS074(t)
	res, err := Revise(context.Background(), n.ID, Changes{}, overlay, site,
		NewBodyEditor(editorEnvS071, prependingS074(t, "Update: fixed in 1.2.4.\n\n")), n.Published.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("Revise: %v", err)
	}
	want := "Update: fixed in 1.2.4.\n\n" + hashBodyS071
	for _, p := range res.Paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "Update: fixed in 1.2.4.") || !strings.Contains(string(b), "# emerge --sync") {
			t.Errorf("%s lacks the prepended line or the kept # line:\n%s", p, b)
		}
	}
	site0, _ := os.ReadFile(SitePath(site, n.ID))
	if !strings.Contains(string(site0), "  Update: fixed in 1.2.4.\n\n  Run this as root:") {
		t.Errorf("the site body is not %q in order:\n%s", want, site0)
	}
}
