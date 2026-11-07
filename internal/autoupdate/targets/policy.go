// Package targets checks that ebuilds offer the interpreter implementations an
// overlay requires — PYTHON_TARGETS, PYTHON_SINGLE_TARGET, LUA_TARGETS,
// LUA_SINGLE_TARGET and RUBY_TARGETS.
//
// A system that sets PYTHON_SINGLE_TARGET to a single implementation cannot
// install a python-single-r1 package whose PYTHON_COMPAT omits it: every
// python_single_target_* flag is off, `^^ ( ... )` in REQUIRED_USE is
// unsatisfiable, and emerge stops before it resolves anything. The overlay
// declares the implementations it moves to in .autoupdate/targets.toml, and this
// package answers, for one ebuild, which of them it does not offer.
package targets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// PolicyFile is the overlay-relative path of the declaration.
const PolicyFile = ".autoupdate/targets.toml"

// ErrNoPolicy reports that the overlay declares no required implementations.
// It is not a failure: an overlay without the file has opted out.
var ErrNoPolicy = errors.New("the overlay declares no required implementations (" + PolicyFile + " is absent)")

// Groups are the USE_EXPAND groups this package understands, in report order.
var Groups = []string{
	"python_targets",
	"python_single_target",
	"lua_targets",
	"lua_single_target",
	"ruby_targets",
}

// Exemption excuses one package from some groups, with the reason recorded so
// the next sweep does not reopen the same investigation.
type Exemption struct {
	Atom   string   `toml:"atom"`
	Groups []string `toml:"groups"`
	Reason string   `toml:"reason"`
}

// Policy is the decoded .autoupdate/targets.toml.
type Policy struct {
	// Required maps a group to the implementations every package using that
	// group must offer.
	Required map[string][]string `toml:"required"`
	Exempt   []Exemption         `toml:"exempt"`
}

// LoadPolicy reads <overlay>/.autoupdate/targets.toml. A missing file returns
// ErrNoPolicy; a key the struct does not claim, an unknown group or an
// exemption without a reason fails the load, because each would silently
// narrow what the check enforces.
func LoadPolicy(overlayPath string) (*Policy, error) {
	path := filepath.Join(overlayPath, PolicyFile)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a constant join on the overlay path the user configured
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoPolicy
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return DecodePolicy(data)
}

// DecodePolicy parses a policy from memory with the same strictness as
// LoadPolicy.
func DecodePolicy(data []byte) (*Policy, error) {
	var p Policy
	md, err := toml.Decode(string(data), &p)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", PolicyFile, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("parse %s: unknown keys: %s", PolicyFile, strings.Join(keys, ", "))
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", PolicyFile, err)
	}
	return &p, nil
}

func (p *Policy) validate() error {
	for group, impls := range p.Required {
		if !slices.Contains(Groups, group) {
			return fmt.Errorf("[required] names unknown group %q (known: %s)", group, strings.Join(Groups, ", "))
		}
		if len(impls) == 0 {
			return fmt.Errorf("[required] %s lists no implementation", group)
		}
	}
	for i, e := range p.Exempt {
		if e.Atom == "" || !strings.Contains(e.Atom, "/") {
			return fmt.Errorf("[[exempt]] #%d: atom %q is not category/package", i+1, e.Atom)
		}
		if strings.TrimSpace(e.Reason) == "" {
			return fmt.Errorf("[[exempt]] %s: reason is required", e.Atom)
		}
		if len(e.Groups) == 0 {
			return fmt.Errorf("[[exempt]] %s: groups is empty", e.Atom)
		}
		for _, g := range e.Groups {
			if !slices.Contains(Groups, g) {
				return fmt.Errorf("[[exempt]] %s: unknown group %q", e.Atom, g)
			}
		}
	}
	return nil
}

// exempted reports whether atom is excused from group.
func (p *Policy) exempted(atom, group string) bool {
	for _, e := range p.Exempt {
		if e.Atom == atom && slices.Contains(e.Groups, group) {
			return true
		}
	}
	return false
}

// Gap is one group in which a package does not offer a required
// implementation.
type Gap struct {
	Group   string   `json:"group"`
	Missing []string `json:"missing"`
	Offered []string `json:"offered"`
}

func (g Gap) String() string {
	return fmt.Sprintf("%s: missing %s (offers %s)", g.Group, strings.Join(g.Missing, " "), strings.Join(g.Offered, " "))
}

// Check returns the gaps of atom given what it offers. A group the package does
// not use at all is never a gap: a package without python_single_target_*
// flags does not care what PYTHON_SINGLE_TARGET is set to.
func (p *Policy) Check(atom string, offered Offered) []Gap {
	var gaps []Gap
	for _, group := range Groups {
		required := p.Required[group]
		have, uses := offered[group]
		if len(required) == 0 || !uses || p.exempted(atom, group) {
			continue
		}
		var missing []string
		for _, impl := range required {
			if !slices.Contains(have, impl) {
				missing = append(missing, impl)
			}
		}
		if len(missing) > 0 {
			sorted := slices.Clone(have)
			sort.Strings(sorted)
			gaps = append(gaps, Gap{Group: group, Missing: missing, Offered: sorted})
		}
	}
	return gaps
}

// MentionsRequiredGroup reports whether a REQUIRED_USE constraint Portage
// printed involves a group this policy requires, which is what makes an
// unsatisfied constraint the candidate's fault under this overlay's own
// declaration rather than an accident of the host's USE.
func (p *Policy) MentionsRequiredGroup(constraint string) bool {
	for group, impls := range p.Required {
		if len(impls) > 0 && strings.Contains(constraint, group+"_") {
			return true
		}
	}
	return false
}
