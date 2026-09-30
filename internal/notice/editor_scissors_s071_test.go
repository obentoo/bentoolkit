package notice

// Story 071, sub-task 9.1 (validation fixes, round 1): the editor's
// instructions end with a scissors line, and everything below it is the text,
// returned verbatim — `#` lines included (R5.1, R5.6, R2.2).
//
// The audit found the defect this pins: `revise` prefilled the editor with a
// body holding `# emerge --sync`, dropped that line as a comment on read-back,
// and so treated an untouched save as a revision that deleted it.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hashBodyS071 = "Run this as root:\n\n# emerge --sync\n\nThen reboot."

// untouchedS071 is an editor that saves the file exactly as it was opened.
func untouchedS071(context.Context, string, ...string) error { return nil }

// appendingS071 is an editor that appends text to the file it was given.
func appendingS071(t *testing.T, text string) Runner {
	t.Helper()
	return func(_ context.Context, _ string, args ...string) error {
		f, err := os.OpenFile(args[len(args)-1], os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(text); err != nil {
			_ = f.Close()
			return err
		}
		return f.Close()
	}
}

var editorEnvS071 = func(k string) string {
	if k == "EDITOR" {
		return "ed"
	}
	return ""
}

func TestScissors_UntouchedEditKeepsHashLines(t *testing.T) {
	got, err := NewBodyEditor(editorEnvS071, untouchedS071)(context.Background(), hashBodyS071)
	if err != nil {
		t.Fatalf("editing: %v", err)
	}
	if got != hashBodyS071 {
		t.Errorf("an untouched edit changed the text:\n got %q\nwant %q", got, hashBodyS071)
	}
}

func TestScissors_UntouchedReviseWritesNothing(t *testing.T) {
	for _, withSite := range []bool{true, false} {
		overlay := filepath.Join(t.TempDir(), "overlay")
		site := ""
		if withSite {
			site = filepath.Join(t.TempDir(), "site")
			if err := os.MkdirAll(SiteNoticesDir(site), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		pub := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
		n := Notice{
			ID: "2026-09-28-sync", Type: "news", Severity: "info", Title: "t", Summary: "s",
			Author: "A <a@b.c>", Body: hashBodyS071, Published: pub, Updated: pub, Revision: 1,
		}
		res, err := Publish(context.Background(), n, overlay, site)
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
		before := map[string]string{}
		for _, p := range res.Paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			before[p] = string(b)
		}

		_, err = Revise(context.Background(), n.ID, Changes{}, overlay, site,
			NewBodyEditor(editorEnvS071, untouchedS071), pub.Add(48*time.Hour))
		if !errors.Is(err, ErrNothingToRevise) {
			t.Errorf("site=%v: an untouched save: err = %v, want ErrNothingToRevise", withSite, err)
		}
		for p, want := range before {
			if b, _ := os.ReadFile(p); string(b) != want {
				t.Errorf("site=%v: %s changed although nothing was edited:\n%s", withSite, p, b)
			}
		}
	}
}

func TestScissors_TextBelowTheLineKeepsHashLines(t *testing.T) {
	got, err := ReadBody(context.Background(), "", editorEnvS071,
		appendingS071(t, "# Upgrading\n\nRun emerge.\n"))
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if want := "# Upgrading\n\nRun emerge."; got != want {
		t.Errorf("body = %q, want %q (a # line written below the scissors is text)", got, want)
	}
	if strings.Contains(got, ">8") {
		t.Errorf("the scissors line leaked into the body: %q", got)
	}
}

func TestScissors_UntouchedNewTemplateIsEmpty(t *testing.T) {
	if _, err := ReadBody(context.Background(), "", editorEnvS071, untouchedS071); !errors.Is(err, ErrEmptyBody) {
		t.Errorf("an untouched template: err = %v, want ErrEmptyBody", err)
	}
}
