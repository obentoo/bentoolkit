package autoupdate

import (
	"fmt"
	"sort"
)

// This file is the ONE-SHOT migration story 043 R1.5 asks for, and it exists
// only because the fail-safe direction chosen in R1.3 has a price.
//
// reconcilesAutomatically clears a disable only when the record says the CHECKER
// wrote it (disabled_by = "auto"). An ABSENT origin therefore reads as "a human
// decided", and that reading is what protects dev-libs/icu-compat and
// media-libs/libjxl-compat — the two deliberate pins a scan re-enabled and
// bumped, leaving www-client/orion-bin's slot dependency broken for ten days.
// The fail-safe needs no migration to hold, which is exactly why it was chosen
// that way round.
//
// Its price is that the ~90 entries the checker auto-disabled BEFORE the field
// existed are indistinguishable, on disk, from those two deliberate pins: they
// also carry `enabled = false` and no origin, so they also stop reconciling and
// would stay disabled forever after their ebuild returned. This tool pays that
// price by stamping the automatic origin onto the entries whose disable really
// WAS the checker's bookkeeping — and onto nothing else.
//
// The operator supplies the exclusion list, and the two entries it must contain
// are dev-libs/icu-compat and media-libs/libjxl-compat. Marking either of them
// re-arms the exact bug this story removes, which is why the plan below reports
// an exclusion naming no record at all: a typo in that list is silent, and its
// consequence is a published bump of a pin that exists to prevent one.
//
// THE DECISION COMES FROM THE PARSED REGISTRY, NEVER FROM A TEXT SCAN. The real
// packages.toml records what grep does to this question in its own header — 98
// hits unanchored, 95 anchored, 92 real — and the gap is doc bodies: a record's
// `comments` field is the documented place it explains itself, and several
// quote the very keys they describe. Only the TOML parser can tell a
// configuration line from a line about one. The WRITE still goes through
// editPackagesConfigSections, because a full re-encode would erase the
// hand-written prose that made the question hard in the first place.

// AutoDisableMigration is what MarkAutoDisabled would do, decided but not yet
// written. Every disabled record in the registry lands in exactly one of the
// first four groups, so the report accounts for all of them rather than only
// for the ones that change.
//
// It exists because the migration runs ONCE, unattended-capable, against a
// registry that auto-commits and pushes: the operator has to read the whole
// decision before approving it, and "N entries stamped" after the fact is not a
// decision anyone can review. Each group is sorted, so two runs over an
// unchanged registry print byte-identical reports.
type AutoDisableMigration struct {
	// Mark holds the atoms that WILL be stamped: disabled, not held, carrying no
	// origin, and not named in the exclusion list.
	Mark []string
	// Excluded holds the disabled atoms the caller's exclusion list protected —
	// the deliberate pins. These are the records the migration must not touch.
	Excluded []string
	// AlreadyStamped holds the disabled atoms that already carry an origin,
	// whatever its value. They are left as they are, which is what makes a
	// re-run a no-op rather than a rewrite.
	AlreadyStamped []string
	// Held holds the disabled atoms carrying hold = true. Hold already excludes
	// them from every scan, and stamping one would additionally CLAIM the
	// checker disabled it — a statement the registry has no evidence for.
	Held []string
	// UnmatchedExcept holds exclusion entries that name no record in the
	// registry at all. It is the typo signal, and the most valuable line in the
	// whole report: `--except dev-libs/icu-compact` protects nothing, and the
	// entry it was meant to protect gets stamped and later re-enabled.
	//
	// It is REPORTED here rather than refused, so the library keeps the contract
	// its tests pin; the refusal belongs to the command, where the operator is.
	UnmatchedExcept []string
}

// PlanAutoDisableMigration reads the overlay's packages.toml and reports what
// MarkAutoDisabled would do to it, writing nothing. It is the review half of
// the migration: the command prints this, asks, and only then writes.
//
// It shares one predicate with the write path (planAutoDisableMigration below),
// so the report cannot describe a different decision from the one that lands.
func PlanAutoDisableMigration(overlayPath string, except []string) (*AutoDisableMigration, error) {
	cfg, err := LoadPackagesConfig(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("reading the registry to plan the disable-origin migration: %w", err)
	}
	return planAutoDisableMigration(cfg, except), nil
}

// MarkAutoDisabled stamps `disabled_by = "auto"` onto every entry of the
// overlay's packages.toml whose disable was the checker's own bookkeeping, so
// R1.3's fail-safe stops freezing them: an entry the reconciliation may clear
// says so, and the ones it must never clear keep saying nothing.
//
// except is the caller's protection list — the deliberate pins, which for the
// real registry are dev-libs/icu-compat and media-libs/libjxl-compat. Stamping
// either would re-arm the defect this story exists to remove.
//
// An entry is stamped only when the PARSED record says all four of:
//   - it is disabled (enabled = false), because the origin of a disable that did
//     not happen is nothing;
//   - it carries no origin yet, so a second run rewrites nothing;
//   - it is not held, because hold is a maintainer's decision and the checker
//     never wrote that disable — claiming it did would be false;
//   - it is not named in except.
//
// It returns the atoms it actually stamped, SORTED, and nil when it stamped
// nothing. "Actually" is not a formality: an entry the plan selected but whose
// `enabled` assignment the surgical editor cannot reach is reported as unmarked
// rather than as marked, so the caller can see the difference instead of
// trusting a count.
//
// The write is the atomic one every registry writer shares, so a failure leaves
// packages.toml exactly as it was and is returned rather than swallowed — this
// overlay auto-commits and pushes, and a half-written registry would be a
// published one.
func MarkAutoDisabled(overlayPath string, except []string) ([]string, error) {
	// Through the same entry point the review path uses, so the plan a human
	// approved and the change that lands cannot come from two readings of the
	// file. The error it returns already names the operation; wrapping it again
	// here would only repeat that.
	plan, err := PlanAutoDisableMigration(overlayPath, except)
	if err != nil {
		return nil, err
	}
	if len(plan.Mark) == 0 {
		// Nothing selected means no write at all, not a write of zero changes:
		// the file keeps its bytes and its mtime.
		return nil, nil
	}

	targets := make(map[string]bool, len(plan.Mark))
	for _, pkg := range plan.Mark {
		targets[pkg] = true
	}

	originAssign := fmt.Sprintf("disabled_by = %q", disabledByAuto)

	var marked []string
	err = editPackagesConfigSections(overlayPath, targets, func(name string, body []string, inComments []bool) ([]string, bool) {
		// A record already spelling the key in raw text is left exactly as it
		// is, even though the parsed value was empty and the plan selected it.
		// Inserting a second assignment would be a DUPLICATE KEY, and a
		// duplicate key does not degrade the record — it fails the decode, so
		// the whole registry stops loading for every command that reads it.
		for j, line := range body {
			if !inComments[j] && disabledByAssignRegex.MatchString(line) {
				return body, false
			}
		}

		// The origin goes immediately below `enabled`, the position
		// CanonicalFieldOrder declares and the linter checks: it qualifies that
		// key and nothing else, so the two read as one statement. The captured
		// indentation is reused so a record written with leading whitespace
		// keeps it.
		//
		// inComments is honoured on every line because a doc body may legally
		// quote `enabled = ...` while describing it — the hazard
		// PackageConfig.Comments documents — and an insertion made against a
		// quoted line would land inside the doc string, where the parser will
		// never see it.
		out := make([]string, 0, len(body)+1)
		inserted := false
		for j, line := range body {
			out = append(out, line)
			if inserted || inComments[j] {
				continue
			}
			if m := enabledAssignRegex.FindStringSubmatch(line); m != nil {
				out = append(out, m[1]+originAssign)
				inserted = true
			}
		}
		if !inserted {
			return body, false
		}
		marked = append(marked, name)
		return out, true
	})
	if err != nil {
		// editPackagesConfigSections writes atomically, so a failure here means
		// nothing landed: reporting no atoms is the honest answer, not a loss.
		return nil, fmt.Errorf("stamping the automatic origin onto %d entry(ies): %w", len(plan.Mark), err)
	}

	sort.Strings(marked)
	return marked, nil
}

// planAutoDisableMigration is the migration's whole policy, over a PARSED
// registry and nothing else. Both the review path and the write path call it, so
// the report a human approves and the change that lands are the same decision
// rather than two implementations of it.
//
// It takes the config rather than a path so the policy can be read — and
// mutated, during a mutation proof — without a fixture on disk, the same reason
// reconcilesAutomatically is a pure function of one record.
func planAutoDisableMigration(cfg *PackagesConfig, except []string) *AutoDisableMigration {
	excluded := make(map[string]bool, len(except))
	for _, pkg := range except {
		excluded[pkg] = true
	}

	plan := &AutoDisableMigration{}
	for name := range cfg.Packages {
		// A map value is not addressable, and IsEnabled/IsHeld take a pointer
		// receiver; the copy is what makes them callable.
		pkg := cfg.Packages[name]
		if pkg.IsEnabled() {
			// An enabled record has no disable to explain. It is not reported as
			// skipped either: every one of the registry's ~320 live entries
			// would be, and a report nobody finishes reading is not a report.
			continue
		}
		switch {
		case excluded[name]:
			plan.Excluded = append(plan.Excluded, name)
		case pkg.IsHeld():
			plan.Held = append(plan.Held, name)
		case pkg.DisabledBy != "":
			plan.AlreadyStamped = append(plan.AlreadyStamped, name)
		default:
			plan.Mark = append(plan.Mark, name)
		}
	}

	for _, pkg := range except {
		if _, ok := cfg.Packages[pkg]; !ok {
			plan.UnmatchedExcept = append(plan.UnmatchedExcept, pkg)
		}
	}

	// Map iteration order is deliberately randomised in Go, so without this the
	// report would shuffle between two runs over an identical registry and no
	// two operators could compare what they saw.
	for _, group := range [][]string{plan.Mark, plan.Excluded, plan.AlreadyStamped, plan.Held, plan.UnmatchedExcept} {
		sort.Strings(group)
	}
	return plan
}
