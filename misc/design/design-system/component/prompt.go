package component

import (
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// Option is one answer a Prompt accepts.
type Option struct {
	Key   rune   `json:"key"`
	Label string `json:"label"`
	// Default marks the answer taken on a bare Enter. Exactly one option should
	// carry it; Render upper-cases that key, which is the convention every
	// prompt in this codebase already follows.
	Default bool `json:"default,omitempty"`
}

// Prompt is a question with a fixed answer set.
//
// The tree spells three variants by hand today — "[y/N]", "[y/N/a/q]" and
// "[y]es / [e]dit / [c]ancel" — in at least seven places, each building its own
// bracket string. The constructors below are those three, named.
//
// Prompt RENDERS the question. It does not read the answer: reading needs the
// real terminal, and internal/common/tui.RunAttached already owns the business
// of handing the TTY to something that reads from it.
type Prompt struct {
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

// Confirm is the yes/no prompt, defaulting to no.
//
// Defaulting to NO is not a style choice. Every caller of this shape in bentoo
// guards something that writes: a bump being applied, a package being removed,
// an overlay that auto-commits and pushes. A bare Enter must not be the answer
// that acts.
func Confirm(question string) Prompt {
	return Prompt{Question: question, Options: []Option{
		{Key: 'y', Label: "yes"},
		{Key: 'n', Label: "no", Default: true},
	}}
}

// ConfirmAll is the per-item prompt with batch escapes: yes, no, all, quit.
func ConfirmAll(question string) Prompt {
	return Prompt{Question: question, Options: []Option{
		{Key: 'y', Label: "yes"},
		{Key: 'n', Label: "no", Default: true},
		{Key: 'a', Label: "all"},
		{Key: 'q', Label: "quit"},
	}}
}

// Choose is the open-ended variant, for answer sets that are not yes/no.
func Choose(question string, options ...Option) Prompt {
	return Prompt{Question: question, Options: options}
}

// Keys returns the accepted keys, so the code that reads the answer validates
// against the same set the question advertised rather than its own copy.
func (p Prompt) Keys() []rune {
	out := make([]rune, 0, len(p.Options))
	for _, o := range p.Options {
		out = append(out, o.Key)
	}
	return out
}

// Default returns the key taken on a bare Enter, and whether there is one.
func (p Prompt) Default() (rune, bool) {
	for _, o := range p.Options {
		if o.Default {
			return o.Key, true
		}
	}
	return 0, false
}

// Render draws "question [y/N]" — the default key upper-cased.
func (p Prompt) Render(t theme.Theme) string {
	keys := make([]string, 0, len(p.Options))
	for _, o := range p.Options {
		k := string(o.Key)
		if o.Default {
			k = strings.ToUpper(k)
		}
		keys = append(keys, k)
	}
	return t.Paint(theme.Default, p.Question) + " " +
		t.Paint(theme.Muted, "["+strings.Join(keys, "/")+"]")
}

// RenderVerbose draws the question with each option spelled out, for answer
// sets whose single letters are not self-explanatory: "[y]es / [e]dit / [c]ancel".
func (p Prompt) RenderVerbose(t theme.Theme) string {
	parts := make([]string, 0, len(p.Options))
	for _, o := range p.Options {
		parts = append(parts, t.Paint(theme.Accent, "["+string(o.Key)+"]")+
			t.Paint(theme.Default, strings.TrimPrefix(o.Label, string(o.Key))))
	}
	return t.Paint(theme.Default, p.Question) + " " + strings.Join(parts, t.Paint(theme.Muted, " / "))
}
