package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// This file deletes records from packages.toml: the counterpart of
// DisablePackagesInConfig for a package that is not being disabled but removed
// from the overlay outright.
//
// It is built on parseRegistryLayout — the block model lintfix.go repairs
// through — because the registry is hand-written, mostly documentation, and a
// round trip through toml.Encoder would drop every comment, quoting choice and
// doc block.
//
// So a removal DROPS A BLOCK and re-emits every other one verbatim, tails
// included: every other record stays byte-identical by not being touched,
// which is cheaper to be sure of than careful editing. A block's tail holds its
// `# END` marker and the blank line before the next header, so a dropped record
// takes its own separator and each survivor keeps its own; deleting lines from
// header to header is where markers get lost and blank lines collapse.

// RemovePackagesFromConfig deletes every record of each named atom from the
// overlay's packages.toml, writing atomically and preserving the file's mode.
//
// Matching is by ATOM, not by key: "net-libs/webkit-gtk", "…:4.1" and
// "…@stable" are entries for one package DIRECTORY, so any spelling removes
// all of them. An entry left behind once its directory is gone is disabled by
// the next --check without saying why.
//
// Nothing matching (or an empty atom list) writes nothing, not identical
// bytes: the overlay auto-commits, and an mtime-only rewrite is an empty commit.
//
// The candidate is re-parsed BEFORE the rename and refused if it does not
// parse, if a record outside the requested atoms vanished, or if a survivor
// decodes differently (verifyRemoval).
//
// A missing packages.toml is ErrPackagesConfigNotFound, not an empty registry,
// so a wrong overlay is reported; a malformed atom is an error, since skipping
// it while its directory is deleted leaves an orphan. --keep-registry's whole
// effect is that this function is not called.
func RemovePackagesFromConfig(overlayPath string, atoms []string) error {
	targets, err := requestedAtoms(atoms)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath) //nolint:gosec // G304: configPath is <overlay>/.autoupdate/packages.toml, a constant join on the overlay path the user configured
	if err != nil {
		if os.IsNotExist(err) {
			return ErrPackagesConfigNotFound
		}
		return fmt.Errorf("failed to read packages.toml: %w", err)
	}
	original := string(data)

	// The file has to load before anything else, exactly as RepairPackagesConfig
	// requires: the gate below compares the candidate against this parse record by
	// record, and there is nothing to compare against when the original does not
	// load. It also keeps a removal from being the operation that "fixes" a file
	// that was already broken, by rewriting it into a shape nobody reviewed.
	before, err := decodePackagesConfig(data)
	if err != nil {
		return fmt.Errorf("refusing to remove records from packages.toml: it does not load as it stands: %w", err)
	}

	layout := parseRegistryLayout(original)
	// Parsing and rendering are exact inverses, so an untouched layout reproduces
	// its input byte for byte. Asserting that BEFORE dropping anything is what
	// catches the failure this whole approach is defensive about: if the block
	// model misreads a shape — a doc block it thinks closed on its opening line, a
	// multi-line value it reads as separate fields — then a neighbouring record's
	// lines are sitting inside the block about to be dropped, and they would go
	// with it. One string comparison, run before the first deletion.
	if round := layout.render(); round != original {
		return fmt.Errorf(
			"refusing to remove records from packages.toml: reading and rewriting it unchanged did not reproduce it (%s) — the removal does not understand this file's layout",
			describeFirstDifference(original, round))
	}

	kept := make([]recordBlock, 0, len(layout.records))
	for _, rec := range layout.records {
		if atom, ok := recordAtom(rec.name); ok && targets[atom] {
			continue
		}
		// A key this package cannot split into an atom is not a key it may delete.
		// Reporting a malformed record key is the linter's job; guessing
		// which package it meant, while deleting it, is nobody's.
		kept = append(kept, rec)
	}
	if len(kept) == len(layout.records) {
		return nil
	}
	layout.records = kept

	candidate := layout.render()
	if err := verifyRemoval(before, candidate, targets); err != nil {
		return err
	}

	// writePackagesConfigAtomically stats the file and re-applies its mode, so the
	// inode the rename installs keeps the permissions the registry had instead of
	// whatever the process umask allowed. Every writer of this file goes through
	// it; a second copy of that policy here is a second copy to keep correct.
	return writePackagesConfigAtomically(configPath, []byte(candidate))
}

// requestedAtoms reduces the caller's keys to the set of atoms to delete, so the
// three spellings of one package collapse into a single target and the match in
// RemovePackagesFromConfig compares like with like — the record keys in the file
// are normalised the same way.
//
// A key that is not a well-formed atom is an error rather than a skipped entry:
// this function is called while the package's directory is being deleted, and a
// silently ignored key leaves the registry pointing at a directory that is gone.
func requestedAtoms(keys []string) (map[string]bool, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	targets := make(map[string]bool, len(keys))
	for _, key := range keys {
		atom, ok := recordAtom(key)
		if !ok {
			return nil, fmt.Errorf("refusing to remove %q from packages.toml: %w", key, ErrInvalidPackageKey)
		}
		targets[atom] = true
	}
	return targets, nil
}

// recordAtom reduces a registry key to the "category/package" atom it names,
// dropping any ":slot" and "@label" suffix, and reports false when the key is
// not a well-formed atom.
//
// It goes through SplitPackageKey rather than splitting on "/" here, because a
// second, suffix-blind copy of that split is exactly the bug the suffixes
// invite: one caller that reads "net-libs/webkit-gtk:4.1" as a package named
// "webkit-gtk:4.1" and quietly matches nothing.
func recordAtom(key string) (string, bool) {
	category, name, ok := ebuilds.SplitPackageKey(key)
	if !ok {
		return "", false
	}
	return category + "/" + name, true
}

// verifyRemoval proves the candidate text is the original MINUS the requested
// atoms and nothing else, and returns an error — abort, write nothing — as soon
// as it is not. It runs before the rename: afterwards the damage is on disk
// and, in an overlay that auto-commits, probably published.
//
//  1. THE CANDIDATE PARSES. A block dropped across a `comments = """` boundary
//     spills doc text out of its string and the file stops being TOML.
//  2. EVERY REQUESTED ENTRY IS GONE AND EVERY OTHER RECORD SURVIVES — "still
//     parses" is not "still holds what it held".
//  3. EVERY SURVIVOR DECODES TO THE SAME VALUES. A boundary read one line wrong
//     takes a neighbour's field (say its `enabled = false`) and still parses.
//
// It does NOT compare the survivors' bytes: they are re-emitted from the slices
// they were parsed into, and RemovePackagesFromConfig's round-trip assertion
// already proved those slices reproduce the file exactly.
func verifyRemoval(before *PackagesConfig, candidate string, targets map[string]bool) error {
	after, err := decodePackagesConfig([]byte(candidate))
	if err != nil {
		return fmt.Errorf("removal aborted, packages.toml is untouched: the rewritten file does not parse: %w", err)
	}

	// sortedKeys, so a file with several problems always names the same one first
	// and two runs of the same bug read identically.
	for _, name := range sortedKeys(before.Packages) {
		atom, isAtom := recordAtom(name)
		requested := isAtom && targets[atom]
		want := before.Packages[name]
		got, survived := after.Packages[name]
		switch {
		case requested && survived:
			return fmt.Errorf("removal aborted, packages.toml is untouched: record %q is an entry of the removed atom %q and is still in the rewritten file", name, atom)
		case !requested && !survived:
			return fmt.Errorf("removal aborted, packages.toml is untouched: record %q disappeared from the rewritten file, and no requested atom names it", name)
		case !requested && !reflect.DeepEqual(want, got):
			return fmt.Errorf("removal aborted, packages.toml is untouched: record %q changed%s", name, describeRecordDifference(want, got))
		}
	}

	// And nothing was invented. The loop above can only see records the original
	// had, so a header the rewrite grew — a block emitted twice, a doc line read
	// as a header — would pass it unnoticed.
	for _, name := range sortedKeys(after.Packages) {
		if _, existed := before.Packages[name]; !existed {
			return fmt.Errorf("removal aborted, packages.toml is untouched: the rewritten file holds record %q, which the original did not", name)
		}
	}

	return nil
}
