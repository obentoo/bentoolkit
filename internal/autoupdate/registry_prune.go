package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// This file deletes records from packages.toml (R5.2, R5.4): the counterpart of
// DisablePackagesInConfig for a package that is not being disabled but removed
// from the overlay outright.
//
// It is built on parseRegistryLayout — the same block model lintfix.go repairs
// through — and for the reason that file states at length: the registry is 6224
// hand-written lines, more than half of them documentation, and a round trip
// through toml.Encoder returns the same RECORDS with every comment, every
// quoting choice and every doc block gone. DisablePackagesInConfig's doc comment
// records the same decision for the same file.
//
// So a removal here DROPS A BLOCK and re-emits every other one verbatim, tails
// included. R5.4's "every other record byte-identical" is then a property of not
// touching them, not of editing them carefully — which is a much cheaper thing
// to be sure of.
//
// The tail is what makes that work. A block's tail holds its `# END` marker and
// the blank line separating it from the next header, so a dropped record takes
// its own separator with it and each survivor keeps its own. The alternative —
// deleting lines from a record's header up to the next one — is exactly where a
// marker is lost or the blank lines collapse or double up, because the spacing
// belongs to no line's record until the block model says which.

// RemovePackagesFromConfig deletes every record of each named atom from the
// overlay's packages.toml, writing atomically and preserving the file's mode.
//
// Matching is by ATOM, not by key (R5.2). "net-libs/webkit-gtk",
// "net-libs/webkit-gtk:4.1" and "net-libs/webkit-gtk@stable" are three entries
// for one package DIRECTORY, so a caller holding any one of the three spellings
// removes all three — 90 of the registry's 321 atoms carry two or more entries,
// one per slot or per release channel. An entry left behind once its directory
// is gone promises an endpoint for a package that no longer exists, and the next
// --check disables it without saying why.
//
// Nothing matching means nothing is written — not "written with identical
// bytes". The overlay auto-commits and pushes, so a rewrite that only moves the
// mtime becomes a commit with no content in the one history someone would read
// to understand a bad removal. An empty or nil atom list is the same no-op.
//
// The candidate text is re-parsed BEFORE the rename and the write is refused if
// it does not parse, if a record outside the requested atoms vanished, or if a
// surviving record decodes to different values (see verifyRemoval). Losing a
// record from this file is losing the knowledge that the package was ever
// tracked, and the diff is where that damage would be hiding.
//
// A missing packages.toml is reported as ErrPackagesConfigNotFound rather than
// treated as an empty registry, so a caller pointed at the wrong overlay hears
// about it instead of concluding there was nothing to remove. A malformed atom
// is an error for the same reason: skipping it silently, while its package
// directory is deleted around it, produces the orphan R5.2 exists to prevent.
//
// R5.3 (--keep-registry) needs no parameter here. The flag's whole effect is
// that this function is not called.
func RemovePackagesFromConfig(overlayPath string, atoms []string) error {
	targets, err := requestedAtoms(atoms)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}

	configPath := filepath.Join(overlayPath, ".autoupdate", "packages.toml")
	data, err := os.ReadFile(configPath)
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
		// Reporting a malformed record key is the linter's job (R4.1); guessing
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
	category, name, ok := SplitPackageKey(key)
	if !ok {
		return "", false
	}
	return category + "/" + name, true
}

// verifyRemoval proves the candidate text is the original MINUS the requested
// atoms and nothing else, and returns an error — meaning abort, write nothing —
// as soon as it is not. It runs before the rename, which is the only point where
// the check buys anything: afterwards the damage is on disk and, in an overlay
// that auto-commits, probably already published.
//
// Three claims, of which the third is the one a successful parse cannot make:
//
//  1. THE CANDIDATE PARSES. A block dropped across a `comments = """` boundary
//     spills doc text out of its string and the file stops being TOML.
//  2. EVERY REQUESTED ENTRY IS GONE AND EVERY OTHER RECORD SURVIVES (R5.2,
//     R5.4). "The file still parses" and "the file still holds what it held" are
//     different claims, and only the second is what a removal may promise.
//  3. EVERY SURVIVOR DECODES TO THE SAME VALUES. A block boundary read one line
//     wrong takes a neighbour's field with it — the `enabled = false` that keeps
//     an orphaned entry out of the run, say — and the result parses perfectly
//     while meaning something else.
//
// It does NOT compare the survivors' bytes, because nothing rewrites them: their
// blocks are re-emitted from the very slices they were parsed into, and the
// round-trip assertion in RemovePackagesFromConfig already proved those slices
// reproduce the file exactly.
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
