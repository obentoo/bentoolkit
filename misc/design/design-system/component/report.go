package component

import (
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// Outcome is what a gate decided. It is deliberately a closed set of THREE:
// this ladder's central rule is that a gate which could not run reports Skipped
// and never Passed, and a two-valued outcome has nowhere to say that.
type Outcome int

const (
	// Skipped is the zero value on purpose: a gate nobody set an outcome on has
	// not passed.
	Skipped Outcome = iota
	Passed
	Failed
)

// String names the outcome in the spelling the reports already use.
func (o Outcome) String() string {
	switch o {
	case Passed:
		return "PASS"
	case Failed:
		return "FAIL"
	default:
		return "SKIP"
	}
}

// Role maps an outcome to its meaning, so no call site picks a colour.
func (o Outcome) Role() theme.Role {
	switch o {
	case Passed:
		return theme.OK
	case Failed:
		return theme.Fail
	default:
		return theme.Skip
	}
}

// Gate is one decision with the reason attached.
//
// Reason is not optional in practice and the type says so by convention rather
// than by tag: a SKIP whose reason is empty is the failure mode the validation
// ladder exists to prevent — "we did not look" rendered as if it were "there
// was nothing to find".
type Gate struct {
	Name    string  `json:"name"`
	Outcome Outcome `json:"outcome"`
	Reason  string  `json:"reason,omitempty"`
}

// Gates is the per-gate verdict list a validation run reports.
type Gates struct {
	Gates []Gate `json:"gates"`
}

// Render draws one aligned line per gate: token, name, reason.
func (gs Gates) Render(t theme.Theme) string {
	w := 0
	for _, g := range gs.Gates {
		if n := theme.Width(g.Name); n > w {
			w = n
		}
	}
	out := make([]string, 0, len(gs.Gates))
	for _, g := range gs.Gates {
		out = append(out, Status{
			Role:   g.Outcome.Role(),
			Label:  pad(g.Name, w),
			Detail: g.Reason,
			Align:  true,
		}.Render(t))
	}
	return join(out)
}

// TallyPart is one labelled bucket of a Tally.
type TallyPart struct {
	Label string     `json:"label"`
	Count int        `json:"count"`
	Role  theme.Role `json:"role,omitempty"`
}

// Tally is the closing count — overlay_validate.go's
// "\n%d ebuilds: %d failed, %d passed, %d skipped\n", which every batch command
// reinvents with its own noun and its own bucket order.
type Tally struct {
	Noun  string      `json:"noun"`
	Total int         `json:"total"`
	Parts []TallyPart `json:"parts"`
	// HideZero drops buckets whose count is 0. Off by default: "0 failed" is
	// information, and a summary that silently omits the bucket a reader was
	// looking for reads as if the run never checked.
	HideZero bool `json:"hide_zero,omitempty"`
}

// Render draws "21 ebuilds: 3 failed, 17 passed, 1 skipped".
func (ty Tally) Render(t theme.Theme) string {
	var b strings.Builder
	b.WriteString(t.Paint(theme.Heading, strconv.Itoa(ty.Total)+" "+ty.Noun))

	parts := make([]string, 0, len(ty.Parts))
	for _, p := range ty.Parts {
		if ty.HideZero && p.Count == 0 {
			continue
		}
		parts = append(parts, t.Paint(p.Role, strconv.Itoa(p.Count)+" "+p.Label))
	}
	if len(parts) == 0 {
		return b.String()
	}
	b.WriteString(t.Paint(theme.Muted, ": "))
	b.WriteString(strings.Join(parts, t.Paint(theme.Muted, ", ")))
	return b.String()
}
