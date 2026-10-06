package validate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stagedDirMode is the mode every directory of a staged tree carries. It is the
// mode the applier already uses for its logs/ directory -- `NewApplier` in
// applier.go creates it with `os.MkdirAll(applier.logsDir, 0o750)` -- and it
// is chosen here for the same reason: a staged tree holds a candidate nobody has
// reviewed yet and, once a fixer runs against it, whatever that fixer wrote.
// Neither belongs in a world-readable directory.
const stagedDirMode fs.FileMode = 0o750

// stagedFileMode is the mode for the files staging WRITES — the candidate, the
// layout and the repo name. Files COPIED out of the overlay keep the mode they
// had there instead, because a copy that quietly changed a file's mode would
// make the staged repository differ from the published one in a way nobody
// asked for. Nothing is lost by the asymmetry: the 0750 directory above a file
// is what decides who can reach it at all.
const stagedFileMode fs.FileMode = 0o600

// stagedRepoPrefix opens every staged repository's name. It is a fixed,
// recognisable prefix so that a name appearing in emerge or pkgcheck output is
// immediately identifiable as a validation artefact rather than a repository
// anyone configured, and so that the generated name cannot collide with a real
// overlay's by accident.
const stagedRepoPrefix = "bentoolkit-staging"

// stagedMasters is the only masters line a staged tree ever declares. See the
// masters note on Stage for why the published overlay is deliberately NOT a master.
const stagedMasters = "masters = gentoo"

// carriedRepoDirs are the overlay directories copied wholesale into the staged
// tree so that the candidate can resolve what the published overlay defines:
// the eclasses it inherits and the profiles that describe the repository. They
// measured 32 KB and 56 KB, which is what makes copying them per
// package-version a non-question.
var carriedRepoDirs = []string{"eclass", "profiles"}

// carriedLayoutKeys are the layout.conf properties that travel from the
// published overlay into the staged one, in this order.
//
// It is a whitelist, not a filter, and that is the point: copying the source
// layout wholesale would carry `masters` (which stagedMasters replaces) and
// could carry a `repo-name` (which would reinstate the duplicate name the staged
// tree exists to avoid). These change how Portage digests and reads a repository, so a
// staged tree that dropped them would validate a bump under rules the published
// overlay does not use. A key the source does not set is not invented here: its
// absence is carried too, so the staged repository falls back to exactly the
// Portage default the published one falls back to.
//
// THIN-MANIFESTS IS NOT ON THIS LIST, and it used to be — see stagedThinManifests.
var carriedLayoutKeys = []string{"sign-manifests", "profile-formats"}

// stagedThinManifests is imposed on every staged tree rather than carried from
// the overlay — the one place the rule "validate under the published tree's own
// rules" is deliberately broken.
//
// Carrying Portage's default (thin-manifests=false) made the staged tree
// NON-THIN, and digestcheck.py then requires every .ebuild in the package
// directory to carry an EBUILD record in the Manifest, failing under `strict` (a
// default FEATURE). The staged Manifest describes distfiles only, so the tree
// was refused before any phase and every build gate reported SKIPPED for a
// package that would have built. Filtering the Manifest to DIST lines trips the
// same check; leaving it unfiltered trips FileNotFound on files Stage never
// copies. The carried property IS the defect.
//
// Imposing it is right: a staged tree holds one ebuild and nothing else to
// digest, so the non-thin checks are meaningless against it. Thin still verifies
// the DIST digests against the archive on disk, so the build still reads the
// archive the Manifest names.
const stagedThinManifests = "thin-manifests = true"

// ErrStageUnpreparable reports that a staged tree could not be built, whatever
// the cause: a missing overlay, a malformed atom, a staging root nothing may
// write into, a disk that filled halfway through the copy.
//
// IT IS A SENTINEL SO THE CALLER CAN RECOGNISE THE FAILURE BY TYPE, and that is
// not tidiness. What the caller does with it is withdraw the bump from
// promotion (see PromotionDecision) — so a caller that recognised it by matching
// message text would keep working exactly until somebody improved the wording,
// at which point unvalidated bumps would quietly start publishing again.
// errors.Is survives a reworded message; strings.Contains does not.
//
// ONE SENTINEL COVERS EVERY CAUSE, on purpose. The cause is still named, in the
// wrapped error's own message, because the operator has to fix it — but no
// caller has to enumerate the ways staging can fail in order to react to staging
// having failed.
var ErrStageUnpreparable = errors.New("the staged tree could not be prepared")

// StageRequest is everything Stage needs to materialise one candidate.
//
// Overlay is the published overlay the staged tree is built FROM. Staging only
// ever reads it; the candidate is never written there, which is the whole point
// of validating somewhere else.
//
// StagingRoot is the directory the staged trees live under, chosen by the caller
// (in production <configDir>/staging) so a dry run can point it elsewhere. The
// path is the only place the retention rule is recorded — no index file, no
// lock — which lets several packages be staged concurrently.
//
// Key is the registry key: category/package, possibly carrying ":slot" or
// "@label" — e.g. "app-editors/zed-bin@preview". Content paths derive from its
// suffix-stripped form (splitContentAtom); the stage root and the repository
// name keep the full key. Version is the PV; together they name the ebuild file.
//
// EbuildBytes is the candidate's body, written verbatim: a reformatted body
// would make a gate result describe a file that never existed anywhere else.
type StageRequest struct {
	Overlay, StagingRoot, Key, Version string
	EbuildBytes                        []byte
}

// Stage builds a self-consistent single-package Portage repository holding one
// candidate ebuild and returns its root, <StagingRoot>/<category>/<package>/
// <version>: eclass/ and profiles/ copied from the overlay, metadata/layout.conf,
// its own profiles/repo_name, and <category>/<package>/<package>-<version>.ebuild.
//
//   - It refuses a StagingRoot inside the overlay: a staged candidate there is an
//     unclaimed ebuild that `overlay autoupdate --clean` would delete.
//   - It masters onto gentoo with local COPIES of eclass/ and profiles/, never
//     onto the overlay, whose deployed /var/db/repos copy lags by a sync cycle.
//   - Its repo_name is unique per package-version (two repositories with one name
//     conflict) and written AFTER the profiles/ copy, which would clobber it.
//   - Restaging replaces the tree at the same path, which is why the applier's
//     copyEbuild, refusing an existing destination, is not reused.
//   - No Manifest and no other version travels; Run writes Options.StagedManifest
//     into the tree before the build gates, as only the caller knows its bytes.
//
// Every failure wraps ErrStageUnpreparable and returns an EMPTY path, because a
// path handed back is a path somebody will build in.
func Stage(req StageRequest) (string, error) {
	stagedRoot, err := stage(req)
	if err != nil {
		// The sentinel is applied HERE, at the single boundary, rather than at
		// each of the fourteen error returns inside stage. Both spellings make every
		// failure one failure today; only this one still does after the fifteenth
		// is added, because it leaves no site where the sentinel can be forgotten.
		// The wrapped cause keeps its own words — including the path it could not
		// prepare — so the type is for the caller and the message is for the
		// operator, and neither is paying for the other.
		return "", fmt.Errorf("%w: %w", ErrStageUnpreparable, err)
	}
	return stagedRoot, nil
}

// stage is Stage's body, minus the sentinel. It reports causes in their own
// words, and Stage is its only caller: keeping the two apart is what lets "every
// failure path wraps ErrStageUnpreparable" be a property of the code rather than
// a habit of whoever last edited it.
func stage(req StageRequest) (stagedRoot string, err error) {
	overlayRoot := strings.TrimSpace(req.Overlay)
	stagingRoot := strings.TrimSpace(req.StagingRoot)
	version := strings.TrimSpace(req.Version)

	if overlayRoot == "" {
		return "", fmt.Errorf("staging %s-%s: no overlay given to copy eclasses and profiles from", req.Key, version)
	}
	if stagingRoot == "" {
		return "", fmt.Errorf("staging %s-%s: no staging root given; Stage never picks one, because the path is where the retention rule is recorded", req.Key, version)
	}
	if len(req.EbuildBytes) == 0 {
		return "", fmt.Errorf("staging %s-%s: no ebuild body given; an empty candidate stages a tree whose gates fail for a reason that has nothing to do with the bump", req.Key, version)
	}

	// The key is split TWICE, because a registry key has two roles and each
	// split answers for one of them. splitContentAtom answers role
	// B: the category and package that name the staged repository's CONTENT —
	// the package directory, the ebuild filename, files/ — which must be free of
	// a key's ":slot" or "@label" suffix because Portage accepts neither
	// character in a package name. splitStagedAtom, below, answers role A: the
	// retention identity, which KEEPS the suffix.
	category, pkg, err := splitContentAtom(req.Key)
	if err != nil {
		return "", err
	}
	// Role A's other half in this function: the repository NAME derives from the
	// SUFFIXED package, so two release lines of one package staged at the
	// same version never answer to one repository name — a duplicate name is a
	// repository-level conflict in Portage regardless of which two repositories
	// collide. stagedRepoName folds the ':' or '@' to '_', which is what keeps
	// the name Portage-legal even though the raw key is not.
	_, suffixedPkg, err := splitStagedAtom(req.Key)
	if err != nil {
		return "", err
	}
	if err := usableAsPathElement("version", version); err != nil {
		return "", fmt.Errorf("staging %s: %w", req.Key, err)
	}

	// The overlay is checked before anything is created, so a mistyped overlay
	// path fails as a mistyped overlay path instead of quietly producing a tree
	// with no eclasses that fails a gate three steps later.
	info, err := os.Stat(overlayRoot)
	if err != nil {
		return "", fmt.Errorf("reading the published overlay %s: %w", overlayRoot, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the published overlay %s is not a directory", overlayRoot)
	}

	// Through StagedTreePath rather than joined here, so that the layout a
	// promoting run looks a retained tree up by and the layout this function
	// creates are the same line of code.
	stagedRoot, err = StagedTreePath(stagingRoot, req.Key, version)
	if err != nil {
		return "", err
	}
	if err := ensureOutsideOverlay(overlayRoot, stagedRoot); err != nil {
		return "", err
	}

	// Replace, never accumulate. RemoveAll on a path that does not exist
	// is a no-op, so the first staging and every restaging take one code path.
	if err := os.RemoveAll(stagedRoot); err != nil {
		return "", fmt.Errorf("removing the retained staged tree %s: %w", stagedRoot, err)
	}
	if err := mkdirStaged(stagedRoot); err != nil {
		return "", err
	}

	// The eclasses and profiles of the published overlay, resolvable from
	// the staged tree because they are in it.
	for _, dir := range carriedRepoDirs {
		if err := carryRepoDir(filepath.Join(overlayRoot, dir), filepath.Join(stagedRoot, dir)); err != nil {
			return "", fmt.Errorf("carrying %s/ from overlay %s into staged tree %s: %w", dir, overlayRoot, stagedRoot, err)
		}
	}

	if err := writeStagedLayout(overlayRoot, stagedRoot); err != nil {
		return "", err
	}

	// After the profiles/ copy, never before: see the repo_name note above. The
	// SUFFIXED package, never the content one — see the split at the top.
	if err := writeStagedRepoName(stagedRoot, suffixedPkg, version); err != nil {
		return "", err
	}

	if err := writeCandidate(stagedRoot, category, pkg, version, req.EbuildBytes); err != nil {
		return "", err
	}

	// The package's own files/ travels with the candidate. eclass/ and profiles/
	// are carried because those are the REPOSITORY-level resources a staged
	// tree has to resolve; ${FILESDIR} is the PACKAGE-level one, and the patch
	// gate cannot tell the difference.
	//
	// Leave it behind and an ebuild carrying PATCHES=( "${FILESDIR}"/foo.patch )
	// dies in src_prepare — and RunBuildGates attributes a failure to the last
	// phase that started, so the patches gate reports FAILED. That is a
	// confident false failure about a bump that is fine, on exactly the class of
	// package the patch gate exists for.
	//
	// carryRepoDir treats an absent source as nothing to do, which is the
	// ordinary case: most ebuilds carry no patches at all.
	pkgFiles := filepath.Join(category, pkg, "files")
	if err := carryRepoDir(filepath.Join(overlayRoot, pkgFiles), filepath.Join(stagedRoot, pkgFiles)); err != nil {
		return "", fmt.Errorf("carrying %s from overlay %s into staged tree %s: %w", pkgFiles, overlayRoot, stagedRoot, err)
	}

	return stagedRoot, nil
}

// splitStagedAtom reads "category/package" into its two halves.
//
// Both halves become directory names, so both are checked for the shapes that
// would let a malformed atom write outside the staged tree. The atom reaches
// Stage from the registry, which is a file a maintainer edits by hand; a typo
// there should produce a named error, not a directory in an unexpected place.
func splitStagedAtom(atom string) (category, pkg string, err error) {
	category, pkg, found := strings.Cut(strings.TrimSpace(atom), "/")
	if !found {
		return "", "", fmt.Errorf("staging %q: an atom must be category/package", atom)
	}
	if err := usableAsPathElement("category", category); err != nil {
		return "", "", fmt.Errorf("staging %q: %w", atom, err)
	}
	if err := usableAsPathElement("package", pkg); err != nil {
		return "", "", fmt.Errorf("staging %q: %w", atom, err)
	}
	return category, pkg, nil
}

// splitContentAtom reads a registry key into the category and package that name
// the staged repository's CONTENT, dropping any ":slot" or "@label" suffix
// first.
//
// A registry key has two roles. Role A, the retention identity (the stage root
// under which one key's trees are retained), KEEPS the suffix so slot 4.1 and
// slot 6 never collapse into one tree; that is splitStagedAtom. Role B is
// everything Portage READS — the package directory, the ebuild filename, files/,
// the Manifest, every atom handed to a Portage tool — and Portage accepts
// neither ':' nor '@' in a package name.
//
// The strip mirrors splitPkgAtom (internal/autoupdate/ebuild_select.go), which
// the promoting side derives its paths from: everything from the first '@',
// then everything from the first ':'. When the two sides disagreed, every later
// gate chdir-failed into a directory that never existed. A ':' or '@' that
// SURVIVES the strip is refused here by name, so a future change to the strip
// fails at this seam rather than as a ghost directory three gates later.
func splitContentAtom(key string) (category, pkg string, err error) {
	stripped := strings.TrimSpace(key)
	if i := strings.IndexByte(stripped, '@'); i >= 0 {
		stripped = stripped[:i]
	}
	if i := strings.IndexByte(stripped, ':'); i >= 0 {
		stripped = stripped[:i]
	}

	category, pkg, found := strings.Cut(stripped, "/")
	if !found {
		return "", "", fmt.Errorf("the package key %q does not name category/package once its \":slot\" or \"@label\" suffix is dropped", key)
	}
	for _, half := range []struct{ kind, value string }{{"category", category}, {"package", pkg}} {
		if err := usableAsPathElement(half.kind, half.value); err != nil {
			return "", "", fmt.Errorf("the package key %q: %w", key, err)
		}
		if strings.ContainsAny(half.value, ":@") {
			return "", "", fmt.Errorf("the package key %q still carries ':' or '@' in its %s %q after the slot/label strip; "+
				"Portage accepts neither character in a name it reads, so the key is refused here rather than staged as content no later gate can address",
				key, half.kind, half.value)
		}
	}
	return category, pkg, nil
}

// usableAsPathElement refuses the values that would make a joined path mean
// something other than "one directory named this": empty, the two relative
// directory names, a separator of either flavour, and a NUL byte.
//
// It deliberately says nothing about ':' or '@'. Role A's path elements — the
// stage root's — legitimately carry both, because the retention identity is the
// full registry key; only role B refuses them, and it does so in
// splitContentAtom where the refusal can name the seam it guards.
func usableAsPathElement(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("the %s is empty", kind)
	case value == "." || value == "..":
		return fmt.Errorf("the %s is %q, which names a directory other than itself", kind, value)
	case strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0):
		return fmt.Errorf("the %s %q contains a path separator, so it cannot be one directory name", kind, value)
	}
	return nil
}

// ensureOutsideOverlay is the deletion hazard, enforced rather than hoped for.
// See the note on Stage: an ebuild under the overlay root is an ebuild
// `--clean` deletes.
func ensureOutsideOverlay(overlayRoot, stagedRoot string) error {
	overlayAbs, err := filepath.Abs(overlayRoot)
	if err != nil {
		return fmt.Errorf("resolving the overlay path %s: %w", overlayRoot, err)
	}
	stagedAbs, err := filepath.Abs(stagedRoot)
	if err != nil {
		return fmt.Errorf("resolving the staged tree path %s: %w", stagedRoot, err)
	}
	if stagedAbs == overlayAbs || strings.HasPrefix(stagedAbs, overlayAbs+string(os.PathSeparator)) {
		return fmt.Errorf("the staged tree %s would sit inside the published overlay %s, where the candidate is an unclaimed ebuild that `overlay autoupdate --clean` deletes; stage outside the overlay", stagedAbs, overlayAbs)
	}
	return nil
}

// mkdirStaged creates dir and any missing parent with the staging mode, then
// sets that mode on dir itself.
//
// The second step is not redundant. MkdirAll subtracts the process umask, so
// without it the mode of a staged tree would depend on the shell that launched
// the run — a maintainer with a stricter umask would get a different tree from
// the same command, and the one mode this package states as a rule would be the
// one thing it did not control.
func mkdirStaged(dir string) error {
	if err := os.MkdirAll(dir, stagedDirMode); err != nil {
		return fmt.Errorf("creating staged directory %s: %w", dir, err)
	}
	if err := os.Chmod(dir, stagedDirMode); err != nil {
		return fmt.Errorf("setting mode %04o on staged directory %s: %w", stagedDirMode.Perm(), dir, err)
	}
	return nil
}

// carryRepoDir copies one of the overlay's repository directories into the
// staged tree.
//
// A source directory that does not exist is not an error: an overlay with no
// eclass/ has no eclasses to make resolvable, and refusing to stage such a
// package would fail a bump over a directory it never needed. The overlay root
// itself was checked by the caller, so this cannot silently swallow a mistyped
// overlay path.
func carryRepoDir(src, dst string) error {
	info, err := os.Stat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}

	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s: %w", path, err)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("relating %s to %s: %w", path, src, err)
		}
		target := filepath.Join(dst, rel)

		switch {
		case entry.IsDir():
			return mkdirStaged(target)
		case entry.Type()&fs.ModeSymlink != 0:
			// Profiles legitimately use symlinks. The link is recreated as a
			// link rather than dereferenced, so a relative link keeps pointing
			// at the staged copy of its target instead of reaching back into
			// the published overlay.
			link, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("reading the link %s: %w", path, err)
			}
			//nolint:gosec // G122: Readlink does not follow the link, and the write goes
			// to target -- inside the staged destination this function owns -- not to the
			// walked path. Same threat model as internal/autoupdate/portage_access.go.
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("recreating the link %s -> %s at %s: %w", path, link, target, err)
			}
			return nil
		case entry.Type().IsRegular():
			return copyRegularFile(path, target)
		default:
			// A socket, device or fifo in a repository directory is not
			// something staging can meaningfully copy, and a tree that is
			// quietly incomplete fails a gate for a reason nobody can trace.
			return fmt.Errorf("%s is a %s, which staging cannot copy into a repository", path, entry.Type())
		}
	})
}

// copyRegularFile copies src to dst, keeping the source's permission bits. dst
// is created exclusively: the staged tree was removed and rebuilt, so a
// destination that already exists means two sources claim one path, which is
// worth an error rather than a silent last-writer-wins.
func copyRegularFile(src, dst string) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}

	in, err := os.Open(src) //nolint:gosec // G304: the path comes from walking the overlay being staged, not from input
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer in.Close() //nolint:errcheck // read-only handle: a failed close cannot lose data; the write side checks its close below

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()) //nolint:gosec // G304: dst is the walked relative path joined under the staged root this function creates; O_EXCL refuses an existing entry
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	// A write is not durable until the handle closes, so the close error is the
	// last chance to notice a truncated copy. It only replaces the returned
	// error when the copy itself succeeded — an earlier failure is the more
	// informative one.
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing %s: %w", dst, cerr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return nil
}

// writeStagedLayout writes the staged metadata/layout.conf: the stagedMasters
// line, followed by the properties carried from the published overlay.
//
// A published overlay with no layout.conf carries nothing, which is correct
// rather than lenient — there is nothing to disagree with.
func writeStagedLayout(overlayRoot, stagedRoot string) error {
	source := filepath.Join(overlayRoot, "metadata", "layout.conf")
	carried, err := carriedLayout(source)
	if err != nil {
		return err
	}

	var body strings.Builder
	body.WriteString("# Generated for validating one candidate ebuild. Not a published repository.\n")
	body.WriteString(stagedMasters + "\n")
	// Imposed, not carried — see stagedThinManifests for what the carried
	// absence did to digestcheck, and why this tree is the one place that rule
	// is deliberately broken.
	body.WriteString("# Imposed: this tree holds one ebuild and no repository files to digest.\n")
	body.WriteString(stagedThinManifests + "\n")
	for _, line := range carried {
		body.WriteString(line + "\n")
	}

	return writeStagedFile(filepath.Join(stagedRoot, "metadata"), "layout.conf", []byte(body.String()))
}

// writeStagedFile writes one of the files staging generates, creating its
// directory with the staging mode first.
//
// Any file already at that path is REMOVED rather than truncated, and that is
// the whole reason this is one helper instead of three call sites: profiles/
// arrives from the published overlay with its own repo_name in it, and
// os.WriteFile onto an existing file keeps that file's mode. Truncating would
// leave a generated file wearing the mode it inherited from the overlay, which
// is not a mode this package chose.
func writeStagedFile(dir, name string, body []byte) error {
	if err := mkdirStaged(dir); err != nil {
		return err
	}

	path := filepath.Join(dir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the carried %s before writing the staged one: %w", path, err)
	}
	if err := os.WriteFile(path, body, stagedFileMode); err != nil {
		return fmt.Errorf("writing the staged %s: %w", path, err)
	}
	return nil
}

// carriedLayout reads the published overlay's layout.conf and returns the
// carried properties, normalised to "key = value" and ordered by
// carriedLayoutKeys so that two runs over one overlay produce identical bytes.
//
// The reader is deliberately narrow: it understands "key = value" and
// hash-comments, and ignores everything else. A line this cannot read is a line
// Portage will read, and staging is not the place to have an opinion about it.
func carriedLayout(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is metadata/layout.conf under the overlay being staged
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the overlay layout %s: %w", path, err)
	}

	values := make(map[string]string)
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	var carried []string
	for _, key := range carriedLayoutKeys {
		if value, ok := values[key]; ok {
			carried = append(carried, key+" = "+value)
		}
	}
	return carried, nil
}

// writeStagedRepoName writes profiles/repo_name. It is called after the
// profiles/ copy, which brings the published overlay's own repo_name with it:
// see the note on Stage.
func writeStagedRepoName(stagedRoot, pkg, version string) error {
	return writeStagedFile(filepath.Join(stagedRoot, "profiles"), "repo_name", []byte(stagedRepoName(pkg, version)+"\n"))
}

// stagedRepoName derives the staged repository's name from the package and
// version it holds.
//
// Portage accepts letters, digits, underscore and dash in a repository name, so
// every other character — a version's dots, most obviously — is folded to an
// underscore. Deriving the name rather than fixing it means two staged trees
// never share a name either, which matters because a validation run registers
// the staged repository with Portage and a duplicate name is a conflict
// regardless of which two repositories collide.
func stagedRepoName(pkg, version string) string {
	raw := stagedRepoPrefix + "-" + pkg + "-" + version

	var name strings.Builder
	name.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			name.WriteRune(r)
		default:
			name.WriteRune('_')
		}
	}
	return name.String()
}

// writeCandidate writes the candidate ebuild into the staged tree's package
// directory, exactly as it was handed to Stage.
func writeCandidate(stagedRoot, category, pkg, version string, body []byte) error {
	return writeStagedFile(filepath.Join(stagedRoot, category, pkg), pkg+"-"+version+".ebuild", body)
}

// buildGates names the gate each rung of the ladder ADDS, for the rungs that
// need a staged tree to answer at all.
//
// options and qa are absent, and their absence is the rule rather than an
// oversight: both read files that already exist — the ebuild's own text and the
// published package directory — so a tree that was never built takes nothing
// away from them. Reporting them as skipped for a tree they never wanted would
// blame the wrong thing, and would bury the option gate's real PASS or FAILED
// under a skip. review is absent for the same reason: it reads, it does not
// build.
var buildGates = map[Depth]string{
	DepthPatches:   GatePatches,
	DepthConfigure: GateConfigure,
	DepthCompile:   GateCompile,
	DepthInstall:   GateInstall,
}

// SkippedGates reports one SKIPPED gate, carrying reason, for every build gate
// depth d covers — and for no other gate.
//
// It is EXPORTED because the applier's dependency pre-check is a second caller:
// the gates that owe an outcome when this host cannot answer must be the set
// RunBuildGates would have reported. Which gates those are is eachBuildGate's
// answer, shared with the runner, so the set a depth owes an outcome for does
// not depend on whether the run happened. THE LADDER IS CUMULATIVE: a
// compile-deep request reports patches, configure AND compile, each said out
// loud, because an unreported gate is indistinguishable from one that passed. A
// depth below DepthPatches yields nothing: an options-deep run needed no tree.
//
// The reason is a required argument, as in report.go's skippedResult, so
// "SKIPPED always carries a reason" stays structural. The SIGNATURE IS FIXED
// because callers outside this package hold it: declinedGates is the sibling
// that names a cause, and this passes DeclineUnrecorded.
func SkippedGates(d Depth, reason string) []GateResult {
	return declinedGates(d, reason, DeclineUnrecorded)
}

// declinedGates is SkippedGates with the cause named.
//
// It is unexported and SkippedGates delegates to it, rather than the two being
// written out separately, so that "which gates does this depth owe an outcome
// for" keeps having exactly one answer — the property SkippedGates' own note
// explains at length.
func declinedGates(d Depth, reason string, cause DeclineCause) []GateResult {
	var gates []GateResult
	eachBuildGate(d, func(gate string, _ buildPhase) {
		gates = append(gates, GateResult{Gate: gate, Outcome: OutcomeSkipped, Reason: reason, Declined: cause})
	})
	return gates
}

// PromotionDecision answers the one question that must be settled before a bump
// is written into the published overlay: may this candidate be promoted, and if
// not, why not. It is pure, so the rule can be asserted directly.
//
// A bump is promoted when every deciding gate reports PASS or SKIPPED — but a
// staging failure skips every build gate, so "nothing failed" alone would
// publish a candidate no gate ever read. A staging failure (stageErr) therefore
// WITHDRAWS the bump, and so does a list where nothing passed and a gate
// declined over the CANDIDATE (GateResult.Declined). A skip because THIS HOST
// could not answer — a missing dependency, no privilege to build — still
// promotes with the unreached depth named; refusing it would stop publishing on
// most workstations. A cause nobody recorded is not read as the candidate's
// (see DeclineUnrecorded).
//
// It does not judge whether the gates COVER the selected depth: it is not given
// the depth. The QA gate never decides, as in Report.ExitCode, or pre-existing
// pkgcheck findings would stop every bump. The string explains the bool and is
// non-empty in BOTH directions, so no caller reads a promotion out of an empty
// reason.
func PromotionDecision(gates []GateResult, stageErr error) (bool, string) {
	if stageErr != nil {
		// The refusal states the cause in this package's own words rather than
		// deferring to the error's, so it names the staged tree whatever wording
		// the wrapped cause happens to carry. The cause rides along because the
		// operator is the one who has to fix it.
		return false, fmt.Sprintf("not promoted: the staged tree could not be prepared, so no build gate ever read this candidate (%v)", stageErr)
	}

	var failed, skipped, passed []string
	// Whether ANY deciding gate declined over the candidate itself. One is
	// enough: if nothing measured this ebuild and one of the things that stopped
	// a gate was the ebuild's own, promoting on the strength of the host's
	// excuses for the others would publish it.
	candidateDeclined := false
	for _, gate := range gates {
		if gate.Gate == GateQA {
			// The one gate that never decides — the same exclusion, for the same
			// reason, that Report.ExitCode and WorstOutcome already make. It
			// is excluded in BOTH directions: a QA PASS is not evidence either,
			// or one metadata.xml verdict would stand in for a build nobody ran.
			continue
		}
		switch gate.Outcome {
		case OutcomeFailed:
			failed = append(failed, gate.Gate)
		case OutcomeSkipped:
			skipped = append(skipped, gate.Gate)
			if gate.Declined == DeclineCandidate {
				candidateDeclined = true
			}
		case OutcomePass:
			passed = append(passed, gate.Gate)
		}
	}

	switch {
	case len(failed) > 0:
		return false, fmt.Sprintf("not promoted: %s reported FAILED", gateList(failed))
	case len(skipped) > 0 && len(passed) == 0 && candidateDeclined:
		// The sentence names the VACUITY, not the gates: an operator told
		// "the patches, configure, compile gates reported SKIPPED" goes looking
		// for three problems when there is one — nothing ran. It also stays well
		// clear of the FAILED wording above, because the two send that operator
		// to different places: one to a gate's findings, the other to whatever
		// stopped the run before any gate could speak.
		return false, "not promoted: every gate that could have decided reported SKIPPED, so nothing was measured about this candidate — see each gate's own reason for what stopped it"
	case len(skipped) > 0:
		return true, fmt.Sprintf("promoted: every gate reported PASS or SKIPPED, and %s did not run — see each gate's own reason", gateList(skipped))
	default:
		// Also the DEPTH-NONE shape: a run that was never meant to build
		// covers no build gate, so the deciding list is EMPTY and lands here.
		// Nothing declined, because nothing was asked — which is why the vacuity
		// branch keys on a skip that named the candidate rather than on the
		// absence of a pass.
		return true, "promoted: every gate reported PASS"
	}
}

// gateList names one or more gates for a sentence a human reads: "the configure
// gate", "the patches, configure gates". It exists so the refusal and the
// promotion statement cannot drift into two spellings of the same list.
func gateList(names []string) string {
	if len(names) == 1 {
		return "the " + names[0] + " gate"
	}
	return "the " + strings.Join(names, ", ") + " gates"
}
