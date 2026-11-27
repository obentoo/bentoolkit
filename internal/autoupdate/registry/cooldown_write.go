package registry

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// This file writes a record's cooldown override (`cooldown_days` +
// `cooldown_reason`) from the command line, so changing a wait no longer means
// hand-editing an 18k-line registry.
//
// Like the --lint --fix repair it is TEXTUAL: the file is split with
// parseRegistryLayout, only the two cooldown fields of the target records are
// replaced, inserted or deleted, and every other line comes out as it went in.
// Before anything is written the candidate must decode to the original with
// exactly the requested change, and must lint with no finding the original did
// not already have; after the write the file on disk is linted again and the
// original is restored when that fails.

// AmbiguousKeyError is a bare cat/pkg that is not itself a record key but
// matches several records — release lines (`@series`) or slots (`:slot`) of
// one package. Matches is sorted.
type AmbiguousKeyError struct {
	Arg     string
	Matches []string
}

func (e *AmbiguousKeyError) Error() string {
	return fmt.Sprintf("%s matches %d records (%s): name the record key", e.Arg, len(e.Matches), strings.Join(e.Matches, ", "))
}

// ErrUnknownRecord is a record key that names no record of packages.toml.
var ErrUnknownRecord = errors.New("no record in packages.toml")

// CooldownRequest is one `cooldown set` or `cooldown unset`: the record keys as
// typed, and the override to write. Days nil removes the override (unset), and
// Reason is then ignored.
type CooldownRequest struct {
	Keys []string
	// AllLines lets a bare cat/pkg stand for every record of that package
	// instead of failing as ambiguous.
	AllLines bool
	Days     *int
	Reason   string
}

// CooldownChange is one record's override before and after the edit. A nil
// pointer is an absent key: the record takes the block's days.
type CooldownChange struct {
	Key       string
	OldDays   *int
	OldReason *string
	NewDays   *int
	NewReason *string
}

// CooldownEdit is a computed, verified cooldown edit of packages.toml; Write
// writes it.
type CooldownEdit struct {
	// Path is the registry the edit was computed from.
	Path string
	// Original is the file as read, Updated the file as it would be written.
	Original string
	Updated  string
	// Targets is every record the edit covers, sorted: the resolved keys plus
	// the members their cooldown_with groups pulled in.
	Targets []string
	// GroupAdded names the targets that were not asked for but share a
	// cooldown_with group with one that was: a group is released whole, so
	// its members always share one wait.
	GroupAdded []string
	// Siblings maps a key given exactly to the other records of the same
	// package the edit leaves alone, so the caller can say so.
	Siblings map[string][]string
	// Changes lists the targets whose override actually changes, sorted.
	Changes []CooldownChange

	overlayPath string
	baseline    []LintIssue
}

// Changed reports whether the edit has anything to write.
func (e *CooldownEdit) Changed() bool { return e != nil && e.Original != e.Updated }

// PlanCooldownEdit resolves the request against the overlay's packages.toml
// and computes the rewrite, verified but not written.
//
// A key is taken exactly when a record has it; otherwise a bare cat/pkg
// stands for the records of that package, and more than one of them is an
// *AmbiguousKeyError unless AllLines is set. A record in a cooldown_with group
// brings its whole group: the loader requires every member to share one
// cooldown_days, so editing one member alone could only produce a registry
// that no longer loads. `cooldown_days = 0` on a group is refused for the same
// reason the loader refuses it.
func PlanCooldownEdit(overlayPath string, req CooldownRequest) (*CooldownEdit, error) {
	if err := validateCooldownRequest(req); err != nil {
		return nil, err
	}
	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath) //nolint:gosec // G304: configPath is <overlay>/.autoupdate/packages.toml, a constant join on the overlay path the user configured
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrPackagesConfigNotFound
		}
		return nil, fmt.Errorf("failed to read packages.toml: %w", err)
	}
	original := string(data)
	cfg, err := decodePackagesConfig(data)
	if err != nil {
		return nil, fmt.Errorf("refusing to edit packages.toml: it does not load as it stands: %w", err)
	}

	resolved, siblings, err := resolveCooldownKeys(cfg.Packages, req.Keys, req.AllLines)
	if err != nil {
		return nil, err
	}
	targets := expandCooldownGroups(cfg.Packages, resolved)
	var groupAdded []string
	for _, k := range targets {
		if !slices.Contains(resolved, k) {
			groupAdded = append(groupAdded, k)
		}
	}
	if req.Days != nil && *req.Days == 0 {
		for _, k := range targets {
			if rec := cfg.Packages[k]; len(rec.CooldownWith) > 0 {
				members := append([]string{keyAtom(k)}, rec.CooldownWith...)
				return nil, fmt.Errorf("package %s: cooldown_days = 0 cannot apply to a cooldown_with group (%s): a group is masked and released together, so no member can skip the cooldown alone; drop cooldown_with from every member first",
					k, strings.Join(sortedCopy(members), ", "))
			}
		}
	}

	var reason *string
	if req.Days != nil {
		r := strings.TrimSpace(req.Reason)
		reason = &r
	}
	updated, err := rewriteCooldownFields(original, targets, req.Days, reason)
	if err != nil {
		return nil, err
	}

	edit := &CooldownEdit{
		Path: configPath, Original: original, Updated: updated,
		Targets: targets, GroupAdded: groupAdded, Siblings: siblings,
		overlayPath: overlayPath,
	}
	for _, k := range targets {
		rec := cfg.Packages[k]
		ch := CooldownChange{Key: k, OldDays: rec.CooldownDays, OldReason: rec.CooldownReason, NewDays: req.Days, NewReason: reason}
		if !reflect.DeepEqual(ch.OldDays, ch.NewDays) || !reflect.DeepEqual(ch.OldReason, ch.NewReason) {
			edit.Changes = append(edit.Changes, ch)
		}
	}
	if !edit.Changed() {
		return edit, nil
	}

	if err := verifyCooldownEdit(cfg, updated, targets, req.Days, reason); err != nil {
		return nil, err
	}
	quiet := slog.New(slog.DiscardHandler)
	edit.baseline, err = lintRegistryData(quiet, overlayPath, data)
	if err != nil {
		return nil, fmt.Errorf("refusing to edit packages.toml: it does not lint as it stands: %w", err)
	}
	after, err := lintRegistryData(quiet, overlayPath, []byte(updated))
	if err != nil {
		return nil, fmt.Errorf("refusing to edit packages.toml: the edited registry does not load: %w", err)
	}
	if fresh := newLintIssues(edit.baseline, after); len(fresh) > 0 {
		return nil, fmt.Errorf("refusing to edit packages.toml: the edit introduces %d lint finding(s), first: %s", len(fresh), fresh[0])
	}
	return edit, nil
}

// cooldownPostWriteLint is the lint run on the written file; a variable so a
// test can make it fail and watch the rollback.
var cooldownPostWriteLint = LintPackagesConfig

// Write replaces packages.toml with the edit, then lints the file on disk and
// restores the original when that lint fails or finds anything the original
// did not have. It refuses to write when the file changed since the plan was
// computed. An edit with nothing to change writes nothing.
func (e *CooldownEdit) Write() error {
	if !e.Changed() {
		return nil
	}
	packagesConfigEditMu.Lock()
	defer packagesConfigEditMu.Unlock()

	current, err := os.ReadFile(e.Path)
	if err != nil {
		return fmt.Errorf("failed to read packages.toml before writing: %w", err)
	}
	if string(current) != e.Original {
		return fmt.Errorf("packages.toml changed since the edit was computed; nothing written, run the command again")
	}
	if err := writePackagesConfigAtomically(e.Path, []byte(e.Updated)); err != nil {
		return err
	}

	quiet := slog.New(slog.DiscardHandler)
	after, lintErr := cooldownPostWriteLint(quiet, e.overlayPath)
	if lintErr == nil {
		fresh := newLintIssues(e.baseline, after)
		if len(fresh) == 0 {
			return nil
		}
		lintErr = fmt.Errorf("%d new lint finding(s), first: %s", len(fresh), fresh[0])
	}
	if rbErr := writePackagesConfigAtomically(e.Path, []byte(e.Original)); rbErr != nil {
		return fmt.Errorf("packages.toml failed its lint after the cooldown edit (%w) and the rollback failed too: %w", lintErr, rbErr)
	}
	return fmt.Errorf("packages.toml failed its lint after the cooldown edit, original restored: %w", lintErr)
}

// validateCooldownRequest applies the loader's per-value rules before the file
// is even read: a wait from 0 to the bound, and a one-line reason.
func validateCooldownRequest(req CooldownRequest) error {
	if len(req.Keys) == 0 {
		return errors.New("no record key given")
	}
	if req.Days == nil {
		return nil
	}
	if d := *req.Days; d < 0 || d > maxCooldownDays {
		return fmt.Errorf("cooldown_days = %d must be a whole number of days from 0 (no cooldown) to %d", d, maxCooldownDays)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return errors.New("cooldown_days needs a cooldown_reason saying why the package does not wait the block's days")
	}
	if strings.ContainsAny(req.Reason, "\n\r") || hasTOMLControlChar(req.Reason) {
		return errors.New("cooldown_reason must be one line of printable text")
	}
	return nil
}

// resolveCooldownKeys turns the typed keys into record keys, sorted and
// distinct, and reports for each exact key the sibling records it leaves out.
func resolveCooldownKeys(pkgs map[string]PackageConfig, args []string, allLines bool) ([]string, map[string][]string, error) {
	byAtom := map[string][]string{}
	for _, key := range sortedKeys(pkgs) {
		byAtom[keyAtom(key)] = append(byAtom[keyAtom(key)], key)
	}
	var out []string
	siblings := map[string][]string{}
	for _, arg := range args {
		lines := byAtom[arg]
		_, exact := pkgs[arg]
		switch {
		case exact && (!allLines || len(lines) <= 1):
			out = append(out, arg)
			for _, other := range byAtom[keyAtom(arg)] {
				if other != arg && !slices.Contains(args, other) {
					siblings[arg] = append(siblings[arg], other)
				}
			}
		case len(lines) == 1 || (allLines && len(lines) > 1):
			out = append(out, lines...)
		case len(lines) > 1:
			return nil, nil, &AmbiguousKeyError{Arg: arg, Matches: lines}
		default:
			return nil, nil, fmt.Errorf("%s: %w", arg, ErrUnknownRecord)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), siblings, nil
}

// expandCooldownGroups adds to keys every record whose package shares a
// cooldown_with group with one of them, to closure, sorted.
func expandCooldownGroups(pkgs map[string]PackageConfig, keys []string) []string {
	byAtom := map[string][]string{}
	for _, key := range sortedKeys(pkgs) {
		byAtom[keyAtom(key)] = append(byAtom[keyAtom(key)], key)
	}
	seen := map[string]bool{}
	queue := slices.Clone(keys)
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		if seen[k] {
			continue
		}
		seen[k] = true
		for _, member := range pkgs[k].CooldownWith {
			queue = append(queue, byAtom[member]...)
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// rewriteCooldownFields sets (days non-nil) or removes (days nil) the two
// cooldown fields of each target record. An existing assignment is replaced in
// place keeping its indentation and the comment lines above it; a missing one
// is inserted at its CanonicalFieldOrder position. A removed field's leading
// comment lines are kept, moved onto whatever follows.
func rewriteCooldownFields(content string, targets []string, days *int, reason *string) (string, error) {
	layout := parseRegistryLayout(content)
	if round := layout.render(); round != content {
		return "", fmt.Errorf("refusing to edit packages.toml: reading and rewriting it unchanged did not reproduce it (%s)",
			describeFirstDifference(content, round))
	}
	found := map[string]bool{}
	for i := range layout.records {
		rec := &layout.records[i]
		if !slices.Contains(targets, rec.name) {
			continue
		}
		if found[rec.name] {
			return "", fmt.Errorf("refusing to edit packages.toml: record %s appears twice", rec.name)
		}
		found[rec.name] = true
		if days == nil {
			removeField(rec, "cooldown_reason")
			removeField(rec, "cooldown_days")
			continue
		}
		setField(rec, "cooldown_days", "cooldown_days = "+strconv.Itoa(*days))
		setField(rec, "cooldown_reason", "cooldown_reason = "+tomlBasicString(*reason))
	}
	for _, k := range targets {
		if !found[k] {
			return "", fmt.Errorf("refusing to edit packages.toml: record %s has no [%q] header the editor can find", k, k)
		}
	}
	return layout.render(), nil
}

// setField replaces the assignment of key with line, or inserts it before the
// first field that CanonicalFieldOrder ranks after key.
func setField(rec *recordBlock, key, line string) {
	for i := range rec.fields {
		if rec.fields[i].key == key {
			rec.fields[i].body = []string{lineIndent(rec.fields[i].body[0]) + line}
			return
		}
	}
	rank := canonicalFieldRank[key]
	at := len(rec.fields)
	for i, f := range rec.fields {
		if r, ok := canonicalFieldRank[f.key]; ok && r > rank {
			at = i
			break
		}
	}
	rec.fields = slices.Insert(rec.fields, at, fieldBlock{key: key, body: []string{line}})
}

// removeField deletes the assignment of key, handing its leading blank and
// comment lines to the next field (or the record's tail).
func removeField(rec *recordBlock, key string) {
	for i, f := range rec.fields {
		if f.key != key {
			continue
		}
		if i+1 < len(rec.fields) {
			rec.fields[i+1].lead = append(slices.Clone(f.lead), rec.fields[i+1].lead...)
		} else {
			rec.tail = append(slices.Clone(f.lead), rec.tail...)
		}
		rec.fields = slices.Delete(rec.fields, i, i+1)
		return
	}
}

// verifyCooldownEdit proves the candidate decodes to the original registry
// with exactly the requested cooldown fields changed on exactly the targets.
func verifyCooldownEdit(orig *PackagesConfig, updated string, targets []string, days *int, reason *string) error {
	cfg, err := decodePackagesConfig([]byte(updated))
	if err != nil {
		return fmt.Errorf("refusing to edit packages.toml: the edited text does not parse: %w", err)
	}
	if len(cfg.Packages) != len(orig.Packages) {
		return fmt.Errorf("refusing to edit packages.toml: the edit changed the record count (%d -> %d)", len(orig.Packages), len(cfg.Packages))
	}
	for key, want := range orig.Packages {
		got, ok := cfg.Packages[key]
		if !ok {
			return fmt.Errorf("refusing to edit packages.toml: the edit lost record %s", key)
		}
		if slices.Contains(targets, key) {
			want.CooldownDays, want.CooldownReason = days, reason
		}
		if !reflect.DeepEqual(want, got) {
			return fmt.Errorf("refusing to edit packages.toml: record %s changed beyond its cooldown fields: %s", key, describeRecordDifference(want, got))
		}
	}
	return nil
}

// newLintIssues lists the issues of after that before does not have, counted
// as a multiset of their rendered lines without line numbers: an inserted
// line shifts every later finding's position without changing it.
func newLintIssues(before, after []LintIssue) []LintIssue {
	seen := map[string]int{}
	for _, i := range before {
		i.Line = 0
		seen[i.String()]++
	}
	var fresh []LintIssue
	for _, i := range after {
		j := i
		j.Line = 0
		if seen[j.String()] > 0 {
			seen[j.String()]--
			continue
		}
		fresh = append(fresh, i)
	}
	return fresh
}
