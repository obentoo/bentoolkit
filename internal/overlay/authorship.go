package overlay

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// AnnotateAuthorship records, for every package whose two ebuilds were compared
// and found to differ, whether the overlay's own content proves the difference
// originates here — and which file proves it (R2.1, R2.2).
//
// It runs ONLY over Verified == VerifiedDiffers, and that is not an
// optimisation. A package whose ebuilds are byte-identical holds no difference
// whose authorship there is anything to attribute, and one whose content was
// never compared holds no difference anyone has seen. Examining either would
// produce a claim about a divergence that does not exist, so every other result
// keeps the zero Authorship — "the report cannot tell" — which is also what an
// API-only run leaves on every package.
//
// It returns nothing, deliberately. Every way this can fail is a way of not
// knowing: an unreadable ebuild, an upstream tree that would not resolve. The
// design's error table gives all of them the same answer — unproved, never an
// error — because verifyAgainstLocalContent already reads an unreadable file as
// "nothing is known", and one unreadable file must not come to mean two
// different things in two neighbouring functions.
//
// It runs AFTER CompareWithProvider returns rather than inside its goroutines,
// so nothing here is concurrent and the report it annotates is already sorted.
//
// Nothing it writes can change a Verdict (025 R4.5): it adds two fields to a
// finished result and edits none.
//
// _Requirements: R2.1, R2.2, R2.3_
func AnnotateAuthorship(report *CompareReport, prov provider.Provider, opts CompareOptions) {
	for i := range report.Results {
		// Indexed rather than ranged over a copy: this pass exists to write two
		// fields back onto the report the caller is holding.
		r := &report.Results[i]
		if r.Verified != VerifiedDiffers {
			continue
		}
		r.Authorship, r.ProvedBy = proveAuthorship(*r, prov, opts)
	}
}

// proveAuthorship answers the question for one differing package: does our
// ebuild reference a file ::gentoo does not provide for it?
//
// Every way of not knowing returns (AuthorshipUnproved, ""), and none of them is
// an error — see AnnotateAuthorship. The failure direction matters more here
// than almost anywhere else in this package: an unproved package costs the
// operator one manual diff, while a claimed proof states with a filename that
// work of ours would be lost and makes `prune` refuse to remove the package on
// the strength of it. So nothing is ever concluded from a check that did not
// run.
func proveAuthorship(result CompareResult, prov provider.Provider, opts CompareOptions) (Authorship, string) {
	paths, ok := resolvePackagePaths(result, prov, opts)
	if !ok {
		return AuthorshipUnproved, ""
	}

	ours, err := os.ReadFile(paths.ourEbuild()) //nolint:gosec // path built from scanned overlay directory names, never from registry input
	if err != nil {
		return AuthorshipUnproved, ""
	}

	for _, ref := range ebuildFilesdirRefs(ours, result.Package, result.LocalVersion) {
		// ONE string is stat'ed and reported, so the report cannot name a path
		// the check never made. It is package-relative ("files/<name>") because
		// the finding line already carries the atom: printed that way, the
		// operator confirms the claim by pasting it after the package directory
		// rather than by knowing what ${FILESDIR} expands to (R2.2).
		//
		// pruneFilesDir is Portage's fixed name for that directory and is spelled
		// once in this package; prune.go already owns the constant, and R6 makes
		// prune consult this very check, so a second spelling would be a second
		// thing to keep in step.
		named := path.Join(pruneFilesDir, ref)
		// FromSlash, because a reference keeps its subdirectory: thunderbird
		// really writes "${FILESDIR}/icon/${PN}-r2.desktop", and joining the
		// slash form as one component would stat a filename with a slash in it —
		// a path no repository has, whose miss would be reported as proof.
		_, err := os.Stat(filepath.Join(paths.upstreamDir, filepath.FromSlash(named)))
		if err == nil {
			// ::gentoo ships it, so this reference proves nothing: it is exactly
			// what an ebuild copied from ::gentoo unchanged looks like.
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			// A permission or I/O error is not an absence. Reading "I could not
			// look" as "upstream does not have it" would manufacture a proof out
			// of a failure to check. The loop CONTINUES rather than returning:
			// another reference may still be provably absent, and one unreadable
			// path must not disarm the whole package's proof.
			continue
		}
		// The first reference ::gentoo lacks is already the whole proof — a file
		// upstream never had cannot have been inherited from upstream — and the
		// finding is one line. Listing the rest would restate the same fact in a
		// way that reads as a bigger claim.
		return AuthorshipOverlay, named
	}

	return AuthorshipUnproved, ""
}

// filesdirMarker is the only spelling of a ${FILESDIR} reference this resolver
// recognises. The unbraced $FILESDIR form is legal bash and appears in none of
// the overlay's 254 references; missing one would cost a package its proof,
// which is the harmless direction — inventing a filename is not.
//
// It is a MARKER and not a "token" because gosec's G101 matches the IDENTIFIER
// rather than the value: a constant whose name carries "token" and whose value
// is a string literal reads as a hardcoded credential and fails the CI lint,
// which runs gosec and is the only check that sees it. The name is what caused
// the finding, so the name is what fixes it — a //nolint here would leave behind
// a suppression every later reader has to re-evaluate for a value that is a
// shell variable spelled out in public ebuilds.
const filesdirMarker = "${FILESDIR}"

// filesdirRefTerminators end the path that follows the token: the characters
// after which a shell word — and therefore a filename — cannot continue.
//
// The double quote is the load-bearing one. "${FILESDIR}/name" closes its quote
// HERE, after the filename, while "${FILESDIR}"/name closed it before the slash;
// net-libs/nodejs-26.7.0.ebuild uses both forms three lines apart, so neither
// may be assumed. Whitespace is what keeps a trailing comment out of the name
// (dev-libs/libixion writes `…-boost-m4.patch # bug 961528`) without this
// needing to know what a comment is.
const filesdirRefTerminators = " \t\r\n\"'()<>;&|`"

// filesdirRefGlobChars are the pattern characters that make a reference name a
// SET rather than a file: app-admin/rsyslog writes "${FILESDIR}"/rsyslog/* and
// www-client/librewolf writes a {,-extra} brace expansion.
//
// Resolving those to their literal text would be worse than useless. "rsyslog/*"
// exists in no repository, so the caller would find it missing from ::gentoo and
// report that as PROOF the divergence is ours — for an ebuild that copied the
// line from ::gentoo unchanged. A pattern is as unresolvable as an unknown
// variable and is dropped on the same grounds.
//
// The check runs AFTER expansion, so the braces of ${PN} are long gone by then
// and only literal ones are left to catch.
const filesdirRefGlobChars = "*?[]{}"

// ebuildVar is one variable this resolver can expand, paired with the value it
// expands to for the ebuild being read.
type ebuildVar struct{ token, value string }

// ebuildFilesdirRefs resolves the filenames an ebuild references under
// ${FILESDIR}, from its text alone — no sourcing, no bash, no filesystem.
//
// Each returned string is a path RELATIVE TO ${FILESDIR}, in slash form, which
// for all but a handful of references is a plain basename. The caller joins it
// onto a files/ directory (filepath.Join with filepath.FromSlash) and asks
// whether that repository ships it; a name ::gentoo does not have is proof the
// divergence originates here, because a reference to a patch upstream never had
// cannot be inherited from upstream (R2.1).
//
// It is why the subdirectory is KEPT rather than flattened to the basename the
// design's summary names. mail-client/thunderbird-153.0.2.ebuild:1056 really
// writes "${FILESDIR}/icon/${PN}-r2.desktop"; returning "thunderbird-r2.desktop"
// would send the caller to stat files/thunderbird-r2.desktop — a path no
// repository has — and the miss would be reported as proof of our authorship
// when the ebuild is upstream's verbatim. The flat case is unaffected: with no
// subdirectory the relative path IS the basename.
//
// EXPANSION IS THE POINT, not decoration. 108 of the overlay's 254 ${FILESDIR}
// references spell the filename through ${PN}, including both packages this
// check exists to prove: kde-plasma/spectacle writes "${FILESDIR}/${PN}-opencv5.patch"
// and net-libs/nodejs writes "${FILESDIR}"/${PN}-26.5.1-paxmarking.patch. A
// matcher that skipped expansion would look for "${PN}-opencv5.patch", find it
// nowhere, and report both packages as unproved — inverting the whole result.
//
// Only ${PN}, ${P}, ${PV} and ${PVR} are expanded, because those four are the
// only ones derivable from the ebuild's own filename, which the caller already
// holds as pkg and version. Every other variable — ${MOZ_PN}, ${PATCH_VER}, a
// loop variable — would need the ebuild's environment, so its reference resolves
// to NOTHING: no filename, no guess, and no error, since an unresolvable
// reference is the ordinary unproved case (R2.3) rather than a failure. Only the
// reference carrying the unknown variable is dropped; the resolvable ones beside
// it still resolve, so one exotic line cannot disarm a package's whole proof.
//
// The result is DEDUPLICATED, in first-seen source order. Deduplicated because
// the caller asks a set question — does upstream ship this file? — and an ebuild
// that references one patch from two USE branches would otherwise make a single
// missing file read as two findings. Source order because the order must not
// come from map iteration: the caller's report and its tests would then differ
// run to run.
//
// This function is pure. That is what lets `prune` (5.1) call the same resolver,
// so the two commands cannot disagree about what proves authorship, and what
// lets the whole thing be tested without a filesystem.
//
// _Requirements: R2.1, R2.4 — it reads references only, and asserts nothing from
// the size of a difference, a timestamp, or which side the file belongs to._
func ebuildFilesdirRefs(ebuild []byte, pkg, version string) []string {
	// PVR is the version as the filename spells it, revision included; PV is the
	// same with the revision removed; P is built from PV, never from PVR. Getting
	// that backwards would be invisible on the ~90% of packages carrying no -rN
	// and wrong on every one that does.
	pv := versionWithoutRevision(version)
	vars := []ebuildVar{
		{"${PN}", pkg},
		{"${P}", pkg + "-" + pv},
		{"${PV}", pv},
		{"${PVR}", version},
	}

	var refs []string
	seen := make(map[string]struct{})

	for line := range strings.Lines(string(ebuild)) {
		// A commented-out reference is not a reference the ebuild makes.
		// media-libs/opencv keeps two PATCHES entries commented out and
		// www-client/librewolf does the same with newins lines; counting them
		// would prove authorship from a patch the build never applies. Only the
		// line-leading form is recognised, which is the shape all eight commented
		// references in the overlay take — telling a MID-line '#' from a '#' inside
		// quotes needs a real shell parser, and the inline comments that do occur
		// sit after the reference, where the whitespace terminator already ends it.
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}

		// Every occurrence on the line, not just the first: a single-line
		// PATCHES=( … ) array holds several.
		for rest := line; ; {
			i := strings.Index(rest, filesdirMarker)
			if i < 0 {
				break
			}
			rest = rest[i+len(filesdirMarker):]

			name, ok := resolveFilesdirRef(rest, vars)
			if !ok {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			refs = append(refs, name)
		}
	}

	return refs
}

// resolveFilesdirRef resolves the path that follows a ${FILESDIR} token, given
// the text after it. ok is false when the reference resolves to no filename at
// all, which is a normal outcome and never an error.
func resolveFilesdirRef(tail string, vars []ebuildVar) (string, bool) {
	// One optional closing quote covers the "${FILESDIR}"/name form; in the
	// "${FILESDIR}/name" form the quote comes at the end instead, where it is a
	// terminator. Both appear in net-libs/nodejs-26.7.0.ebuild.
	tail = strings.TrimPrefix(tail, "\"")

	// A separator must follow. Without one there is no path here — a bare
	// "${FILESDIR}" assigned to a variable names the directory, not a file.
	if !strings.HasPrefix(tail, "/") {
		return "", false
	}

	var b strings.Builder
	for i := 0; i < len(tail); {
		c := tail[i]
		if strings.IndexByte(filesdirRefTerminators, c) >= 0 {
			break
		}
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		// A '$' begins something only the ebuild's own environment could resolve,
		// unless it is one of the four derivable from its filename. Anything else
		// — another variable, a $( ) substitution — makes the WHOLE reference
		// unresolvable: a partially expanded name is a name nothing ships, and
		// the caller would read that as proof.
		value, width, ok := expandFilesdirVar(tail[i:], vars)
		if !ok {
			return "", false
		}
		b.WriteString(value)
		i += width
	}

	// The leading separators go before path.Clean, never after: Clean swallows a
	// ".." that follows a leading "/" ("/../x" becomes "/x"), which would turn an
	// escape out of ${FILESDIR} into a plausible-looking filename. Cleaned as a
	// relative path it stays "../x" and is refused below.
	rel := strings.TrimLeft(b.String(), "/")
	if rel == "" {
		return "", false
	}
	if strings.ContainsAny(rel, filesdirRefGlobChars) {
		return "", false
	}

	name := path.Clean(rel)
	// Whatever this returns is joined onto a real directory and stat'ed by the
	// caller. A reference that leaves ${FILESDIR} is not a ${FILESDIR} reference,
	// and nothing about it could prove anything about the package's files/ tree.
	if name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", false
	}

	return name, true
}

// expandFilesdirVar expands the variable at the start of s, returning its value
// and the width of the token consumed. ok is false for any variable this
// resolver cannot derive from the ebuild's filename.
//
// The list is walked in order rather than looked up in a map so that nothing
// here depends on map iteration. No token can shadow another in any case: each
// ends in '}', and none of the four names contains one.
func expandFilesdirVar(s string, vars []ebuildVar) (value string, width int, ok bool) {
	for _, v := range vars {
		if strings.HasPrefix(s, v.token) {
			return v.value, len(v.token), true
		}
	}
	return "", 0, false
}

// versionWithoutRevision turns a PVR into a PV: "0.20.0-r1" into "0.20.0",
// leaving a version with no revision untouched.
//
// The rule is spelled out here rather than borrowed from internal/common/ebuild,
// whose equivalent regexp is unexported — and it is deliberately strict about
// what a revision is (a literal "-r" followed by digits only, at the very end),
// so a version that merely ends in something r-shaped, such as "1.0-rc1", keeps
// every character it had.
func versionWithoutRevision(version string) string {
	i := strings.LastIndex(version, "-r")
	if i <= 0 {
		return version
	}
	digits := version[i+len("-r"):]
	if digits == "" {
		return version
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return version
		}
	}
	return version[:i]
}
