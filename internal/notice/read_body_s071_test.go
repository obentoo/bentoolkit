package notice

// Story 071, sub-task 4.1: ReadBody takes the text from --body-file, else from
// $VISUAL, else from $EDITOR, run on a prefilled temporary file with its
// command split into arguments and never through a shell (R2.1 to R2.6).
//
// The Runner is the seam: it receives the program and its arguments exactly
// as they would be exec'd. The contract assumed here is
//
//	type Runner func(ctx context.Context, name string, args ...string) error
//
// with the temporary file as the last argument.
//
// Hostile halves first: a `#` in the middle of a line is text, not a comment;
// a VISUAL that is set but blank is not an editor; and shell punctuation in
// $EDITOR reaches the program as literal arguments.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func envS071(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

type editorCallS071 struct {
	name string
	args []string
	seen string // the file content before the fake editor wrote
}

// fakeEditorS071 records the call and replaces the temporary file's content
// with write (unless write is nil).
func fakeEditorS071(t *testing.T, calls *[]editorCallS071, write *string, result error) Runner {
	t.Helper()
	return func(_ context.Context, name string, args ...string) error {
		c := editorCallS071{name: name, args: append([]string(nil), args...)}
		if len(args) > 0 {
			b, err := os.ReadFile(args[len(args)-1])
			if err != nil {
				t.Errorf("the editor's last argument is not a readable file: %v", err)
			}
			c.seen = string(b)
			if write != nil {
				if err := os.WriteFile(args[len(args)-1], []byte(*write), 0o600); err != nil {
					t.Errorf("writing the temporary file: %v", err)
				}
			}
		}
		*calls = append(*calls, c)
		return result
	}
}

func noEditorS071(t *testing.T) Runner {
	return func(context.Context, string, ...string) error {
		t.Error("the editor ran although it must not")
		return nil
	}
}

func assertGoneS071(t *testing.T, calls []editorCallS071) {
	t.Helper()
	for _, c := range calls {
		if len(c.args) == 0 {
			continue
		}
		tmp := c.args[len(c.args)-1]
		if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the temporary file %s was left behind (stat err %v)", tmp, err)
		}
	}
}

func TestReadBody_FromFileRunsNoEditor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(path, []byte("From the file.\n\nSecond paragraph.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBody(context.Background(), path, envS071(map[string]string{"VISUAL": "vim", "EDITOR": "nano"}), noEditorS071(t))
	if err != nil {
		t.Fatalf("ReadBody(--body-file): %v", err)
	}
	if strings.TrimSpace(got) != "From the file.\n\nSecond paragraph." {
		t.Errorf("body = %q", got)
	}
}

func TestReadBody_MissingFileNamesThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.txt")
	_, err := ReadBody(context.Background(), path, envS071(nil), noEditorS071(t))
	if err == nil || !strings.Contains(err.Error(), path) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want a wrapped not-exist error naming %s", err, path)
	}
}

func TestReadBody_EmptyFileIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, []byte(" \n\n\t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBody(context.Background(), path, envS071(nil), noEditorS071(t)); !errors.Is(err, ErrEmptyBody) {
		t.Errorf("err = %v, want ErrEmptyBody", err)
	}
}

func TestReadBody_VisualWinsOverEditor(t *testing.T) {
	var calls []editorCallS071
	text := "Written in the editor."
	got, err := ReadBody(context.Background(), "",
		envS071(map[string]string{"VISUAL": "myvisual --wait", "EDITOR": "myeditor"}),
		fakeEditorS071(t, &calls, &text, nil))
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if len(calls) != 1 || calls[0].name != "myvisual" || len(calls[0].args) != 2 || calls[0].args[0] != "--wait" {
		t.Fatalf("editor calls = %+v, want one call to myvisual --wait <file>", calls)
	}
	if got != text {
		t.Errorf("body = %q, want %q", got, text)
	}
	assertGoneS071(t, calls)
}

func TestReadBody_BlankVisualFallsBackToEditor(t *testing.T) {
	for _, visual := range []string{"", "   "} {
		var calls []editorCallS071
		text := "x"
		_, err := ReadBody(context.Background(), "",
			envS071(map[string]string{"VISUAL": visual, "EDITOR": "myeditor"}),
			fakeEditorS071(t, &calls, &text, nil))
		if err != nil {
			t.Errorf("VISUAL=%q: %v", visual, err)
			continue
		}
		if len(calls) != 1 || calls[0].name != "myeditor" {
			t.Errorf("VISUAL=%q: calls = %+v, want myeditor", visual, calls)
		}
	}
}

func TestReadBody_NoEditorAsksForOne(t *testing.T) {
	_, err := ReadBody(context.Background(), "", envS071(map[string]string{"VISUAL": "", "EDITOR": " "}), noEditorS071(t))
	if !errors.Is(err, ErrNoEditor) {
		t.Fatalf("err = %v, want ErrNoEditor", err)
	}
	for _, want := range []string{"--body-file", "VISUAL", "EDITOR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not mention %s", err, want)
		}
	}
}

// R2.6: shell punctuation is an argument, never an instruction.
func TestReadBody_EditorCommandIsSplitNeverShelled(t *testing.T) {
	var calls []editorCallS071
	text := "x"
	_, err := ReadBody(context.Background(), "",
		envS071(map[string]string{"EDITOR": "nano; touch $HOME/pwned && true"}),
		fakeEditorS071(t, &calls, &text, nil))
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	c := calls[0]
	for _, shell := range []string{"sh", "bash", "/bin/sh", "/bin/bash", "zsh"} {
		if c.name == shell {
			t.Errorf("the editor ran through a shell: %s %q", c.name, c.args)
		}
	}
	if c.name != "nano;" {
		t.Errorf("program = %q, want the first field %q", c.name, "nano;")
	}
	want := []string{"touch", "$HOME/pwned", "&&", "true"}
	if len(c.args) != len(want)+1 || !reflect.DeepEqual(c.args[:len(want)], want) {
		t.Errorf("args = %q, want %q followed by the temporary file", c.args, want)
	}
}

func TestReadBody_CommentLinesAreDroppedAndInlineHashesKept(t *testing.T) {
	var calls []editorCallS071
	text := "# a comment\nSee issue #42 for details.\n#another\n\nC# is not a comment.\n# trailing\n"
	got, err := ReadBody(context.Background(), "", envS071(map[string]string{"EDITOR": "ed"}), fakeEditorS071(t, &calls, &text, nil))
	if err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if want := "See issue #42 for details.\n\nC# is not a comment."; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	// The file the editor opened carried commented instructions.
	var nonBlank int
	for _, l := range strings.Split(calls[0].seen, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		nonBlank++
		if !strings.HasPrefix(l, "#") {
			t.Errorf("the prefilled file has an uncommented line %q", l)
		}
	}
	if nonBlank == 0 {
		t.Error("the temporary file was not prefilled with commented instructions")
	}
	assertGoneS071(t, calls)
}

func TestReadBody_OnlyCommentsIsAnEmptyBody(t *testing.T) {
	var calls []editorCallS071
	_, err := ReadBody(context.Background(), "", envS071(map[string]string{"EDITOR": "ed"}), fakeEditorS071(t, &calls, nil, nil))
	if !errors.Is(err, ErrEmptyBody) {
		t.Errorf("an untouched (all-comment) file: err = %v, want ErrEmptyBody", err)
	}
	assertGoneS071(t, calls)
}

func TestReadBody_EditorFailureReportsItsStatus(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 3").Run()
	var ee *exec.ExitError
	if !errors.As(exitErr, &ee) {
		t.Fatalf("could not build an exit error: %v", exitErr)
	}
	var calls []editorCallS071
	text := "text that must not be used"
	_, err := ReadBody(context.Background(), "", envS071(map[string]string{"EDITOR": "ed"}), fakeEditorS071(t, &calls, &text, exitErr))
	if !errors.Is(err, ErrEditorFailed) {
		t.Fatalf("err = %v, want ErrEditorFailed", err)
	}
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("the error %q does not carry the editor's exit status", err)
	}
	assertGoneS071(t, calls)
}

func TestReadBody_ContextReachesTheEditor(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "marker")
	var seen any
	run := func(c context.Context, _ string, args ...string) error {
		seen = c.Value(key{})
		return os.WriteFile(args[len(args)-1], []byte("x"), 0o600)
	}
	if _, err := ReadBody(ctx, "", envS071(map[string]string{"EDITOR": "ed"}), run); err != nil {
		t.Fatalf("ReadBody: %v", err)
	}
	if seen != "marker" {
		t.Error("the editor did not receive the caller's context, so Ctrl+C cannot stop it")
	}
}
