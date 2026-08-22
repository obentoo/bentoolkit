package component

import "github.com/obentoo/bentoolkit/misc/design/design-system/theme"

// Sample is one catalogue entry: a component, a name, and the sentence
// explaining what it replaces.
type Sample struct {
	Name string
	// Why names the existing call sites this shape was read out of, so the
	// catalogue can be checked against the tree rather than believed.
	Why string
	C   Component
}

// Catalogue is every component, with a representative instance of each.
//
// # It has exactly two consumers, and that is the point
//
// The contract test iterates it, and the gallery renders it. One list means the
// picture an operator looks at and the assertions CI runs cannot drift apart:
// a component that is not here is neither shown nor checked, and one that is
// here is both.
//
// Every sample must exercise at least one semantic role. The contract test
// asserts that Styled output carries escape sequences, and a colourless sample
// would satisfy that assertion vacuously.
func Catalogue() []Sample {
	return []Sample{
		{
			Name: "Status",
			Why:  "output.PrintSuccess / PrintError / PrintWarning / PrintInfo — four near-identical Printf wrappers",
			C: Status{
				Role: theme.OK, Label: "media-plugins/gst-plugins-qt6-1.29.2",
				Detail: "manifest regenerated", Align: true,
			},
		},
		{
			Name: "Transition",
			Why:  "every version bump, rename and realignment line: a literal → at each site",
			C:    Transition{Label: "version", From: "1.29.1", To: "1.29.2"},
		},
		{
			Name: "Empty",
			Why:  `snapshot_list.go prints "  (none)"; several commands print nothing, which reads as a bug`,
			// ASCII on purpose: see the mode-contract test. A component owns its
			// glyphs, not the caller's words.
			C: Empty{Note: "(none: every package is current)"},
		},
		{
			Name: "Rule",
			Why:  "the ─ glyph appears 572 times, every run built by hand",
			C:    Rule{Label: "Validation", Width: 56},
		},
		{
			Name: "Box",
			Why:  "output.Box, whose bottom edge is a fixed 16 dashes regardless of the title",
			C: Box{Title: "Staged tree", Width: 56, Lines: []string{
				"~/.config/bentoo/autoupdate/staging",
				"shared with --apply and validate --depth",
			}},
		},
		{
			Name: "Group",
			Why:  `overlay_prune.go prints "files (%d)" and "registry entries (%d)" by hand`,
			C: Group{Title: "files", Items: []string{
				"metadata.xml",
				"gst-plugins-qt6-1.29.2.ebuild",
				"Manifest",
			}},
		},
		{
			Name: "Group (empty)",
			Why:  "the same shape with nothing in it — an empty result is a result",
			C:    Group{Title: "registry entries", Empty: "(none)"},
		},
		{
			Name: "Tree",
			Why:  "overlay_prune.go descends 2 → 4 → 6 → 8 spaces with a separate Printf per level",
			C: Tree{Roots: []Node{{
				Label: "media-plugins", Detail: "1 package", Role: theme.Info,
				Children: []Node{{
					Label: "gst-plugins-qt6", Detail: "would be removed", Role: theme.Warn,
					Children: []Node{
						{Label: "versions: 1.28.0, 1.29.1", Role: theme.Skip},
						{Label: "files: 3", Role: theme.Skip},
					},
				}},
			}}},
		},
		{
			Name: "Table",
			Why:  `overlay_prune.go's "%-45s %s" — a width picked once, by hand`,
			C: Table{
				Head: []string{"PACKAGE", "DEPTH", "REASON"},
				Rows: [][]string{
					{"media-plugins/gst-plugins-qt6", "install", "series bump"},
					{"dev-libs/foo", "configure", "revision bump"},
					{"app-editors/zed", "options", "patch bump"},
				},
			},
		},
		{
			Name: "KV",
			Why:  `every "    %s: %s\n", whose colon column drifts per call site`,
			C: KV{Pairs: []Pair{
				{Key: "depth reached", Value: "install"},
				{Key: "gates", Value: "4 of 4"},
				{Key: "staged at", Value: "~/.config/bentoo/autoupdate/staging"},
			}},
		},
		{
			Name: "Gates",
			Why:  "the per-gate verdict list a validation run reports",
			C: Gates{Gates: []Gate{
				{Name: "patches", Outcome: Passed, Reason: "src_prepare completed"},
				{Name: "configure", Outcome: Passed, Reason: "src_configure completed"},
				{Name: "compile", Outcome: Passed, Reason: "src_install not covered"},
				{Name: "install", Outcome: Passed, Reason: "qmerge and src_test not covered"},
				{Name: "qa", Outcome: Skipped, Reason: "pkgcheck crashes on this overlay's git history"},
			}},
		},
		{
			Name: "Tally",
			Why:  `overlay_validate.go's "\n%d ebuilds: %d failed, %d passed, %d skipped\n"`,
			C: Tally{Noun: "ebuilds", Total: 21, Parts: []TallyPart{
				{Label: "failed", Count: 3, Role: theme.Fail},
				{Label: "passed", Count: 17, Role: theme.OK},
				{Label: "skipped", Count: 1, Role: theme.Skip},
			}},
		},
		{
			Name: "Progress",
			Why:  `overlay_compare.go's "\r  Checking: [%3d%%] %d/%d" plus a 66-space erase line`,
			C:    Progress{Label: "Checking", Done: 9, Total: 20, BarWidth: 24},
		},
		{
			Name: "Progress (indeterminate)",
			Why:  "the honest render for work whose size is not known yet",
			C:    Progress{Label: "Scanning", Done: 137},
		},
		{
			Name: "Prompt",
			Why:  `"[y/N]" spelled by hand in at least seven places`,
			C:    Confirm("Apply this bump?"),
		},
		{
			Name: "Prompt (batch)",
			Why:  `"[y/N/a/q]" — the per-item prompt with batch escapes`,
			C:    ConfirmAll("Apply this bump?"),
		},
		{
			Name: "Prompt (verbose)",
			Why:  `"[y]es / [e]dit / [c]ancel" — for answer sets whose letters are not obvious`,
			C: verbose(Choose("Publish the realignment?",
				Option{Key: 'y', Label: "yes", Default: true},
				Option{Key: 'e', Label: "edit"},
				Option{Key: 'c', Label: "cancel"},
			)),
		},
	}
}

// verbose adapts Prompt.RenderVerbose to the Component interface, so the
// spelled-out variant is one catalogue entry rather than a special case the
// gallery and the contract test each have to know about.
type verbosePrompt struct{ p Prompt }

func verbose(p Prompt) Component { return verbosePrompt{p: p} }

// Render draws the prompt with each option spelled out.
func (v verbosePrompt) Render(t theme.Theme) string { return v.p.RenderVerbose(t) }
