package render

import "github.com/charmbracelet/lipgloss"

// borderStyle is the single line segment set every renderer in this package
// draws with, so a report never contains two frames that disagree about how a
// corner is joined.
//
// rounded reads better in modern terminals; `block` (lipgloss.BlockBorder) is
// the only style immune to the gap some fonts leave between line segments, so
// this is the one line to change if that gap shows up.
//
// # Plain uses it for the character, never for the styling
//
// A box-drawing character is printable Unicode and carries no escape sequence;
// a lipgloss STYLE applied to one does. Plain therefore takes segments from
// this value and assembles them itself (see rule), and never asks lipgloss to
// render anything — which is what keeps R2.1 true by construction rather than
// by remembering not to set a colour. The boxed modes style it freely.
var borderStyle = lipgloss.RoundedBorder()
