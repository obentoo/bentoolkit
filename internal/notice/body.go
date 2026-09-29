package notice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Errors of ReadBody (R2.3 to R2.5).
var (
	ErrNoEditor     = errors.New("no editor: pass --body-file, or set VISUAL or EDITOR")
	ErrEditorFailed = errors.New("the editor failed")
	ErrEmptyBody    = errors.New("the notice body is empty")
)

// Runner runs the editor: name and args exactly as they are exec'd, the file
// to edit being the last argument. Tests inject a fake; the command passes
// TerminalRunner.
type Runner func(ctx context.Context, name string, args ...string) error

// BodyEditor opens current in the user's editor and returns the edited text.
// Revise takes one, so the command decides how the body is edited.
type BodyEditor func(ctx context.Context, current string) (string, error)

// editorInstructions prefills the temporary file. Every line is a comment, so
// saving the file untouched is an empty body (R2.4).
const editorInstructions = `# Write the notice body below. Lines starting with '#' are removed.
# Separate paragraphs with a blank line; the news item is wrapped at 72
# columns. Save and quit to continue, or leave the body empty to abort.
`

// TerminalRunner runs the editor on the user's terminal. It never goes through
// a shell (R2.6), and it stays in bentoo's process group on purpose: an
// interactive editor that is moved out of the foreground group loses the
// terminal's signals and its job control.
func TerminalRunner(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: the user's own VISUAL/EDITOR, split into argv, never through a shell
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run() // EditBody wraps it with the editor's name
}

// ReadBody returns the notice body (R2): the content of bodyFile when it is
// given, otherwise what the user writes in $VISUAL, else $EDITOR, with comment
// lines removed. env reads the environment (os.Getenv in production).
func ReadBody(ctx context.Context, bodyFile string, env func(string) string, run Runner) (string, error) {
	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile) //nolint:gosec // G304: --body-file is the user's own input file, read and never executed
		if err != nil {
			return "", fmt.Errorf("reading --body-file %s: %w", bodyFile, err)
		}
		body := trimBlankLines(string(data))
		if body == "" {
			return "", fmt.Errorf("--body-file %s: %w", bodyFile, ErrEmptyBody)
		}
		return body, nil
	}
	return EditBody(ctx, editorInstructions, env, run)
}

// EditBody opens initial in the user's editor on a temporary file and returns
// the text saved, with comment lines removed and surrounding blank lines
// trimmed. The temporary file is removed on every path.
func EditBody(ctx context.Context, initial string, env func(string) string, run Runner) (body string, err error) {
	argv := editorCommand(env)
	if len(argv) == 0 {
		return "", ErrNoEditor
	}

	tmp, err := os.CreateTemp("", "bentoo-notice-*.txt")
	if err != nil {
		return "", fmt.Errorf("creating the editor's temporary file: %w", err)
	}
	path := tmp.Name()
	defer func() {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("removing the editor's temporary file %s: %w", path, rmErr))
		}
	}()
	_, werr := tmp.WriteString(initial)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("writing the editor's temporary file %s: %w", path, werr)
	}

	if err := run(ctx, argv[0], append(argv[1:], path)...); err != nil {
		return "", fmt.Errorf("editor %s: %w: %w", argv[0], ErrEditorFailed, err)
	}

	data, err := os.ReadFile(path) //nolint:gosec // G304: the temporary file this function created
	if err != nil {
		return "", fmt.Errorf("reading the edited body from %s: %w", path, err)
	}
	body = trimBlankLines(stripComments(string(data)))
	if body == "" {
		return "", ErrEmptyBody
	}
	return body, nil
}

// editorCommand splits $VISUAL, else $EDITOR, into argv. A variable that is set
// but blank is not an editor.
func editorCommand(env func(string) string) []string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if argv := strings.Fields(env(name)); len(argv) > 0 {
			return argv
		}
	}
	return nil
}

// stripComments drops every line that starts with '#'. A '#' later in a line
// (`issue #42`, `C#`) is text.
func stripComments(s string) string {
	var kept []string
	for line := range strings.SplitSeq(s, "\n") {
		if !strings.HasPrefix(line, "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// trimBlankLines removes whitespace-only lines at both ends and trailing
// whitespace on every line, keeping the indentation of the first line.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// NewBodyEditor returns the BodyEditor `notice revise` uses: the current text,
// under the usual commented instructions, opened in $VISUAL or $EDITOR.
func NewBodyEditor(env func(string) string, run Runner) BodyEditor {
	return func(ctx context.Context, current string) (string, error) {
		return EditBody(ctx, editorInstructions+"\n"+current+"\n", env, run)
	}
}
