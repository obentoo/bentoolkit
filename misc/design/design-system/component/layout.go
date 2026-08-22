package component

import (
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// DefaultWidth is the fallback width for components that draw to an edge. 72
// rather than 80: it leaves room for the two-to-eight spaces of indentation the
// commands nest their output in without wrapping on a standard terminal.
const DefaultWidth = 72

// Rule is a horizontal separator, optionally naming the section it opens.
//
// "─" is the single most common glyph in this codebase's output — 572
// occurrences — and every one of them is a hand-built strings.Repeat or a
// literal run of dashes, so no two separators are reliably the same length.
type Rule struct {
	Label string `json:"label,omitempty"`
	Width int    `json:"width,omitempty"`
}

// Render draws "────────" or "── Label ────────".
func (r Rule) Render(t theme.Theme) string {
	w := r.Width
	if w <= 0 {
		w = DefaultWidth
	}
	h := t.Glyphs().HLine
	if r.Label == "" {
		return t.Paint(theme.Muted, strings.Repeat(h, w))
	}

	// Two leading rule glyphs, a space, the label, a space — that prefix is what
	// the remaining fill has to be measured against.
	fill := w - theme.Width(strings.Repeat(h, 2)+" "+r.Label+" ")
	if fill < 0 {
		fill = 0
	}
	return t.Paint(theme.Muted, strings.Repeat(h, 2)+" ") +
		t.Paint(theme.Heading, r.Label) +
		t.Paint(theme.Muted, " "+strings.Repeat(h, fill))
}

// Box is a titled panel, replacing output.Box.
//
// output.Box closes with a FIXED sixteen dashes regardless of how wide the
// title or content is, so the bottom edge almost never lines up with the top
// one. This one measures.
type Box struct {
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	Width int      `json:"width,omitempty"`
}

// Render draws the panel.
func (b Box) Render(t theme.Theme) string {
	g := t.Glyphs()
	w := b.Width
	if w <= 0 {
		w = DefaultWidth
	}

	head := g.TopLeft + g.HLine + " " + b.Title + " "
	fill := w - theme.Width(head)
	if fill < 0 {
		fill = 0
	}

	out := make([]string, 0, len(b.Lines)+2)
	out = append(out, t.Paint(theme.Muted, g.TopLeft+g.HLine+" ")+
		t.Paint(theme.Heading, b.Title)+
		t.Paint(theme.Muted, " "+strings.Repeat(g.HLine, fill)))
	for _, ln := range b.Lines {
		out = append(out, t.Paint(theme.Muted, g.VLine)+"  "+ln)
	}
	out = append(out, t.Paint(theme.Muted, g.BottomLeft+strings.Repeat(g.HLine, w-1)))
	return join(out)
}

// Group is a named collection: a header carrying the count, then the members,
// or an explicit empty note.
//
// This is overlay_prune.go's shape, printed there four times by hand —
// "files (%d)", "registry entries (%d)", each followed by a loop that indents
// its members by two more spaces.
type Group struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
	// Empty replaces the member list when Items is empty. Defaulted rather than
	// omitted, because a group that prints nothing is indistinguishable from a
	// group that failed to run.
	Empty string `json:"empty,omitempty"`
	// HideCount suppresses the "(n)" beside the title. The field is phrased
	// negatively so the ZERO VALUE shows the count: every command that prints
	// this shape today prints the count, so the default must be the common case.
	HideCount bool `json:"hide_count,omitempty"`
}

// Render draws "title (n):" followed by the indented members.
func (gr Group) Render(t theme.Theme) string {
	head := gr.Title
	if !gr.HideCount {
		head += " (" + strconv.Itoa(len(gr.Items)) + ")"
	}
	head = t.Paint(theme.Heading, head) + t.Paint(theme.Muted, ":")

	if len(gr.Items) == 0 {
		return head + "\n" + indent(Empty{Note: gr.Empty}.Render(t), 2)
	}
	return head + "\n" + indent(join(gr.Items), 2)
}

// Node is one entry of a Tree.
type Node struct {
	Label    string     `json:"label"`
	Detail   string     `json:"detail,omitempty"`
	Role     theme.Role `json:"role,omitempty"`
	Children []Node     `json:"children,omitempty"`
}

// Tree draws a hierarchy by indentation, which is how every nesting command in
// this codebase already draws one: overlay_prune.go descends 2 → 4 → 6 → 8
// spaces with a separate Printf per level, so the structure lives in the format
// strings rather than in the data.
type Tree struct {
	Roots []Node `json:"roots"`
	// Step is the indent per level. Zero means 2, which is what the commands use.
	Step int `json:"step,omitempty"`
	// Bullets prefixes each node with the bullet glyph.
	Bullets bool `json:"bullets,omitempty"`
}

// Render draws the whole hierarchy.
func (tr Tree) Render(t theme.Theme) string {
	step := tr.Step
	if step <= 0 {
		step = 2
	}
	out := make([]string, 0, len(tr.Roots))
	for _, n := range tr.Roots {
		out = append(out, renderNode(t, n, step, tr.Bullets))
	}
	return join(out)
}

func renderNode(t theme.Theme, n Node, step int, bullets bool) string {
	line := Status{Role: n.Role, Label: n.Label, Detail: n.Detail}.Render(t)
	if bullets && n.Role == theme.Default {
		line = t.Paint(theme.Muted, t.Glyphs().Bullet) + " " + line
	}
	if len(n.Children) == 0 {
		return line
	}
	kids := make([]string, 0, len(n.Children))
	for _, c := range n.Children {
		kids = append(kids, renderNode(t, c, step, bullets))
	}
	return line + "\n" + indent(join(kids), step)
}

// Table is a grid whose columns are as wide as their widest cell.
//
// overlay_prune.go reaches for "%-45s %s" — a width picked once, by hand, that
// truncates nothing and misaligns the moment an atom exceeds it. Measuring is
// strictly better and costs one pass.
type Table struct {
	Head []string   `json:"head,omitempty"`
	Rows [][]string `json:"rows"`
	// Gap is the spacing between columns; zero means 2.
	Gap int `json:"gap,omitempty"`
}

// Render draws the grid. The LAST column is never padded, so no line carries
// trailing whitespace into a golden file or a diff.
func (tb Table) Render(t theme.Theme) string {
	gap := tb.Gap
	if gap <= 0 {
		gap = 2
	}
	widths := tb.columnWidths()
	sep := strings.Repeat(" ", gap)

	out := make([]string, 0, len(tb.Rows)+1)
	if len(tb.Head) > 0 {
		out = append(out, renderRow(t, tb.Head, widths, sep, theme.Heading))
	}
	for _, row := range tb.Rows {
		out = append(out, renderRow(t, row, widths, sep, theme.Default))
	}
	return join(out)
}

func (tb Table) columnWidths() []int {
	n := len(tb.Head)
	for _, r := range tb.Rows {
		if len(r) > n {
			n = len(r)
		}
	}
	widths := make([]int, n)
	measure := func(row []string) {
		for i, c := range row {
			if w := theme.Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	measure(tb.Head)
	for _, r := range tb.Rows {
		measure(r)
	}
	return widths
}

func renderRow(t theme.Theme, row []string, widths []int, sep string, role theme.Role) string {
	cells := make([]string, 0, len(row))
	for i, c := range row {
		// The LAST cell is never padded, so no row carries trailing whitespace
		// into a golden file or a `git diff` of captured output.
		if i == len(row)-1 {
			cells = append(cells, t.Paint(role, c))
			continue
		}
		// Pad the RAW cell before painting. Padding painted text would measure
		// the escape sequences as content on any profile that emitted them.
		cells = append(cells, t.Paint(role, pad(c, widths[i])))
	}
	return strings.Join(cells, sep)
}

// Pair is one KV entry.
type Pair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KV is a block of aligned "key: value" lines — the shape behind every
// "    %s: %s\n" in the tree, where the colon column drifts per call site.
type KV struct {
	Pairs []Pair `json:"pairs"`
}

// Render draws the block with the values aligned on one column.
func (k KV) Render(t theme.Theme) string {
	w := 0
	for _, p := range k.Pairs {
		if n := theme.Width(p.Key); n > w {
			w = n
		}
	}
	out := make([]string, 0, len(k.Pairs))
	for _, p := range k.Pairs {
		key := t.Paint(theme.Muted, pad(p.Key+":", w+1))
		out = append(out, key+" "+t.Paint(theme.Default, p.Value))
	}
	return join(out)
}
