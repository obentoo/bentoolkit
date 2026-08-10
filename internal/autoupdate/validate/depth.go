package validate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Depth is how far validation runs for a given bump — the ladder a policy points
// at when it decides what a bump has earned (R2, R2.1).
//
// THE LADDER IS CUMULATIVE. Each depth includes every depth before it:
// DepthConfigure means "run everything up to and including configure", not "run
// configure". A reader who takes the constants as a menu rather than a ladder
// writes a runner that skips the patch gate on a configure request — which is a
// validation hole that still reports as a pass.
//
// THE INTEGER ORDERING IS THE CONTRACT, NOT A LABELLING. The rungs are declared
// shallowest first so that `<` means "shallower than" and the policy resolver can
// combine a floor with a reviewer's proposal as max(floor, proposed). Reordering
// the constants does not rename anything — it silently changes which validation
// a bump gets.
//
// INSTALL IS DELIBERATELY ABSENT. The ladder stops at DepthCompile because
// src_install is out of scope for this story, not because anybody forgot it.
// R6.4 makes a compile pass state that src_install went uncovered, which names
// the gap without building a gate for it; adding the rung is a story of its own,
// not a parser change.
type Depth int

const (
	// DepthNone runs no validation at all. It is the shallowest rung and the
	// zero value, but it is never reached by accident: ParseDepth refuses the
	// empty string rather than answering DepthNone, so a mistyped config key
	// cannot switch validation off in silence.
	DepthNone Depth = iota

	// DepthOptions compares the build options an ebuild declares against the
	// options the new upstream archive actually offers. Nothing is unpacked
	// beyond reading the archive, so this is the cheapest rung that can still
	// fail a bump.
	DepthOptions

	// DepthPatches additionally applies the ebuild's patches to the new
	// sources, catching the patch that stopped applying when upstream moved.
	// It includes DepthOptions.
	DepthPatches

	// DepthConfigure additionally runs the package's configure phase, which is
	// where a changed dependency or a dropped build option surfaces as an
	// error. It includes DepthPatches, and therefore DepthOptions.
	DepthConfigure

	// DepthCompile additionally compiles the sources — the deepest rung and the
	// most expensive. It includes DepthConfigure, and therefore everything
	// below it. It stops short of src_install: see the note on Depth.
	DepthCompile
)

// depthLadder is the whole ordered set, shallowest first. ParseDepth and the
// error it returns both read it, so a new rung is added in exactly one place and
// cannot end up parseable but unlisted.
var depthLadder = []Depth{DepthNone, DepthOptions, DepthPatches, DepthConfigure, DepthCompile}

// ErrUnknownDepth reports that a depth name came from a flag or a config key and
// does not name a rung of the ladder.
//
// It is a sentinel so a caller can tell a bad depth apart from a validation
// failure: the first is a typo the operator must fix, the second is a bump that
// must not land, and they deserve different exit paths.
var ErrUnknownDepth = errors.New("unknown validation depth")

// String is the name the ladder is known by outside this package. It is
// lower-case and unpadded because the very same spelling is what `--depth` and
// the config key accept — String and ParseDepth are exact inverses, and a report
// that printed "Configure" would name a depth nobody could request.
func (d Depth) String() string {
	switch d {
	case DepthNone:
		return "none"
	case DepthOptions:
		return "options"
	case DepthPatches:
		return "patches"
	case DepthConfigure:
		return "configure"
	case DepthCompile:
		return "compile"
	default:
		return "Depth(" + strconv.Itoa(int(d)) + ")"
	}
}

// ParseDepth reads a depth by name, as written on the `--depth` flag or in a
// config key.
//
// It matches against String() rather than a second table of literals, so the two
// directions cannot drift apart: any name a report can print is a name this
// accepts, and nothing else is.
//
// The match is exact — no trimming, no case folding. Depth decides how much
// machine time a bump costs; accepting " Compile " as compile would mean the
// name in the config and the name in the report are no longer the same string,
// and the first surprise there is a config that reads as one depth and runs as
// another.
//
// Every rejection names the offender AND lists the valid set (R2.1). An error
// that said only "invalid depth" would send the operator to read the source for
// five short words.
func ParseDepth(s string) (Depth, error) {
	for _, d := range depthLadder {
		if s == d.String() {
			return d, nil
		}
	}

	// DepthNone rides along with the error only because a Go function must
	// return something; it is not a fallback. A caller that ignores the error
	// and uses the value has switched validation off, which is why every call
	// site is expected to fail loudly instead.
	return DepthNone, fmt.Errorf("%w: %s; valid depths are %s, shallowest first, and each one includes every depth before it",
		ErrUnknownDepth, strconv.Quote(s), strings.Join(depthNames(), ", "))
}

// depthNames lists every rung by name, shallowest first. It exists so the set
// offered to an operator is derived from the ladder rather than retyped: the
// parser's error message and, later, the `--depth` help text must not be able to
// disagree about which depths exist.
func depthNames() []string {
	names := make([]string, len(depthLadder))
	for i, d := range depthLadder {
		names[i] = d.String()
	}
	return names
}
