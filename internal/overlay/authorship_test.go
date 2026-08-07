package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// This file pins the resolver half of the authorship proof (R2.1, R2.4): which
// filenames under ${FILESDIR} an ebuild actually references. The comparison
// against ::gentoo's files/ tree is a separate step; this one only has to answer
// "which names?" — and answer it without ever inventing one.
//
// The two failure directions are NOT symmetric, and every case below is placed
// on purpose:
//
//   - MISSING a real reference downgrades a proved package to unproved. That is
//     the cost of the expansion cases. Measured on the live overlay, 108 of the
//     254 ${FILESDIR} references spell the filename through ${PN}; a matcher
//     that skips expansion resolves kde-plasma/spectacle to the literal
//     "${PN}-opencv5.patch", finds it nowhere, and reports as unproved the two
//     packages the story exists to prove — silently inverting its headline.
//   - INVENTING a reference is worse: a name nothing ships resolves to "::gentoo
//     does not have it", which the report states as PROOF that the divergence is
//     ours. That is what the glob, brace, traversal, comment and subdirectory
//     cases are here for. Each is a shape measured in the live overlay that a
//     naive scanner turns into a confident false claim.
//
// So the rule the table encodes throughout: anything this resolver cannot
// resolve EXACTLY yields no filename at all, and never an error (R2.3's unproved
// state is the correct outcome, not a failure).

// TestEbuildFilesdirRefs covers the reference shapes the live overlay actually
// contains, plus the ones a proof must refuse to draw.
//
// _Requirements: R2.1, R2.4_
func TestEbuildFilesdirRefs(t *testing.T) {
	tests := []struct {
		name    string
		ebuild  string
		pkg     string
		version string
		want    []string
	}{
		{
			// VERBATIM from kde-plasma/spectacle-6.7.4.ebuild:72, one of the two
			// packages this story proves. files/spectacle-opencv5.patch exists in
			// the overlay and in no ::gentoo tree.
			name:    "${PN} expansion, quote closing after the path",
			ebuild:  "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n",
			pkg:     "spectacle",
			version: "6.7.4",
			want:    []string{"spectacle-opencv5.patch"},
		},
		{
			// The shape app-text/libetonyek-0.1.13-r1.ebuild:43 uses. ${P} carries
			// the version, so a resolver that expanded only ${PN} would produce a
			// name missing its middle.
			name:    "${P} expansion",
			ebuild:  "PATCHES=(\n\t\"${FILESDIR}/${P}-mdds-3.0.patch\"\n)\n",
			pkg:     "libetonyek",
			version: "0.1.13-r1",
			want:    []string{"libetonyek-0.1.13-mdds-3.0.patch"},
		},
		{
			// The four variables in one ebuild, on a REVISION so that ${PV} and
			// ${PVR} hold different strings. Without a -rN the two are equal and a
			// resolver that confused them would pass while being wrong about every
			// revbumped package — and ${P} would silently be built from the wrong
			// one (P is pkg-PV, never pkg-PVR).
			name: "${PV} and ${PVR} are distinct on a revision, and ${P} follows ${PV}",
			ebuild: "PATCHES=(\n" +
				"\t\"${FILESDIR}/${PN}-boost.patch\"\n" +
				"\t\"${FILESDIR}/${P}-configure.patch\"\n" +
				"\t\"${FILESDIR}/${PV}-tests.patch\"\n" +
				"\t\"${FILESDIR}/${PVR}-revbump.patch\"\n" +
				")\n",
			pkg:     "libixion",
			version: "0.20.0-r1",
			want: []string{
				"libixion-boost.patch",
				"libixion-0.20.0-configure.patch",
				"0.20.0-tests.patch",
				"0.20.0-r1-revbump.patch",
			},
		},
		{
			// "-r" followed by anything but digits is not a revision, so nothing
			// may be stripped: a resolver that took "-rc1" for one would build
			// ${P} and ${PV} out of a version that never existed and lose the
			// reference. Gentoo spells release candidates "_rc1", but the ebuild
			// filename parser accepts hyphens in a version, so the shape reaches
			// here.
			name:    "a -rc suffix is not a revision",
			ebuild:  "PATCHES=( \"${FILESDIR}/${PV}.patch\" \"${FILESDIR}/${P}.patch\" )\n",
			pkg:     "hello",
			version: "1.0-rc1",
			want:    []string{"1.0-rc1.patch", "hello-1.0-rc1.patch"},
		},
		{
			// A bare "-r" with no number is not a revision either. Degenerate, but
			// the alternative is a ${PV} silently missing its last two characters.
			name:    "a bare -r with no number is not a revision",
			ebuild:  "PATCHES=( \"${FILESDIR}/${PV}.patch\" )\n",
			pkg:     "hello",
			version: "1.0-r",
			want:    []string{"1.0-r.patch"},
		},
		{
			// VERBATIM from net-libs/nodejs-26.7.0.ebuild:213 and :216 — the second
			// package this story proves, and the reason quoting cannot be assumed:
			// ONE file uses both forms. The first closes the quote before the
			// slash, the second after the filename. Both files exist under
			// net-libs/nodejs/files/.
			name: "both quoting forms in one file",
			ebuild: "src_prepare() {\n" +
				"\tuse pax-kernel &&\n" +
				"\t\tPATCHES+=( \"${FILESDIR}\"/${PN}-26.5.1-paxmarking.patch )\n" +
				"\n" +
				"\tuse ppc64 &&\n" +
				"\t\tPATCHES+=(\t\"${FILESDIR}/${PN}-24.11.1-restore-ppc64be.patch\" )\n" +
				"\n" +
				"\tdefault\n" +
				"}\n",
			pkg:     "nodejs",
			version: "26.7.0",
			want: []string{
				"nodejs-26.5.1-paxmarking.patch",
				"nodejs-24.11.1-restore-ppc64be.patch",
			},
		},
		{
			// No quotes at all. The path ends at whitespace, not at end of line —
			// every real reference is followed by something (a ")", a second
			// argument, a comment).
			name:    "unquoted form ends at whitespace",
			ebuild:  "src_prepare() {\n\teapply ${FILESDIR}/fix-build.patch\n\tdefault\n}\n",
			pkg:     "hello",
			version: "1.0",
			want:    []string{"fix-build.patch"},
		},
		{
			// dev-libs/libixion-0.20.0-r1.ebuild:38. A trailing comment must not
			// become part of the filename, which the whitespace terminator handles
			// without knowing anything about comments.
			name:    "a trailing comment does not enter the filename",
			ebuild:  "PATCHES=(\n\t\"${FILESDIR}\"/${PN}-0.20.0-boost-m4.patch # bug 961528\n)\n",
			pkg:     "libixion",
			version: "0.20.0-r1",
			want:    []string{"libixion-0.20.0-boost-m4.patch"},
		},
		{
			name:    "a literal reference with no variable at all",
			ebuild:  "PATCHES=( \"${FILESDIR}/fix.patch\" )\n",
			pkg:     "hello",
			version: "1.0",
			want:    []string{"fix.patch"},
		},
		{
			// www-client/librewolf uses ${MOZ_PN} and sys-devel/binutils uses
			// ${PATCH_VER}. Neither is derivable from the ebuild's filename, so the
			// only honest answer is no name — NOT the literal "${MOZ_PN}-r3.desktop",
			// which would resolve to "::gentoo does not ship it" and be reported as
			// proof. No error either: an unresolvable reference is the ordinary
			// unproved case (R2.3).
			name: "an unknown variable yields no reference and no error",
			ebuild: "PATCHES=(\n" +
				"\t\"${FILESDIR}/${MOZ_PN}-r3.desktop\"\n" +
				"\t\"${FILESDIR}\"/patches-${PATCH_VER}.tar.xz\n" +
				")\n",
			pkg:     "librewolf",
			version: "153.0.3_p1",
			want:    nil,
		},
		{
			// An unknown variable poisons only its OWN reference. The resolvable
			// one beside it still resolves — otherwise one exotic line would
			// silently disarm the proof for the whole package.
			name: "an unknown variable does not suppress the resolvable references beside it",
			ebuild: "PATCHES=(\n" +
				"\t\"${FILESDIR}/${MOZ_PN}-r3.desktop\"\n" +
				"\t\"${FILESDIR}/${PN}-gentoo.patch\"\n" +
				")\n",
			pkg:     "librewolf",
			version: "153.0.3_p1",
			want:    []string{"librewolf-gentoo.patch"},
		},
		{
			name:    "an ebuild with no reference yields empty",
			ebuild:  "EAPI=8\nDESCRIPTION=\"nothing to see\"\nSLOT=\"0\"\ninherit ecm\n",
			pkg:     "spectacle",
			version: "6.7.4",
			want:    nil,
		},
		{
			// app-admin/rsyslog and friends: "${FILESDIR}"/rsyslog/* names a SET,
			// not a file. Stat'ing the literal "rsyslog/*" misses in every
			// repository on earth, so a resolver that returned it would prove
			// authorship for a package that copied its reference verbatim from
			// ::gentoo. A glob is as unresolvable as an unknown variable and is
			// dropped for the same reason.
			name: "a glob or a brace expansion names a set, not a file, so it yields nothing",
			ebuild: "src_install() {\n" +
				"\tinsinto /etc\n" +
				"\tdoins \"${FILESDIR}\"/rsyslog/*\n" +
				"\tdoins \"${FILESDIR}\"/*.desktop;\n" +
				"\tdoins \"${FILESDIR}\"/pref{,-extra}.js\n" +
				"}\n",
			pkg:     "rsyslog",
			version: "8.2508.0",
			want:    nil,
		},
		{
			// mail-client/thunderbird-153.0.2.ebuild:1056 really writes
			// "${FILESDIR}/icon/${PN}-r2.desktop". Flattening that to the bare
			// basename would send the caller to stat files/thunderbird-r2.desktop,
			// which NO repository has — manufacturing a proof out of a path we
			// resolved wrong. The subdirectory is part of the answer.
			name:    "a reference into a subdirectory keeps its path under files/",
			ebuild:  "\tlocal desktop_file=\"${FILESDIR}/icon/${PN}-r2.desktop\"\n",
			pkg:     "thunderbird",
			version: "153.0.2",
			want:    []string{"icon/thunderbird-r2.desktop"},
		},
		{
			// media-libs/opencv-5.0.0.ebuild:392 keeps two patches commented out,
			// and www-client/librewolf does the same with newins lines. A
			// commented-out line is not a reference the ebuild makes: counting it
			// would prove authorship from a patch the build never applies.
			name: "a commented-out reference is not a reference",
			ebuild: "PATCHES=(\n" +
				"\t# \"${FILESDIR}/${PN}_contrib-4.8.1-rgbd.patch\"\n" +
				"\t\"${FILESDIR}/${PN}-5.0.0-gentoo.patch\"\n" +
				")\n",
			pkg:     "opencv",
			version: "5.0.0",
			want:    []string{"opencv-5.0.0-gentoo.patch"},
		},
		{
			// Whatever these resolved to would be joined onto a real directory and
			// stat'ed by the caller. A reference that leaves ${FILESDIR} is not a
			// ${FILESDIR} reference, and nothing about it could prove authorship.
			name: "a reference that escapes ${FILESDIR} yields nothing",
			ebuild: "PATCHES=(\n" +
				"\t\"${FILESDIR}/../../../etc/passwd\"\n" +
				"\t\"${FILESDIR}/\"\n" +
				"\t\"${FILESDIR}\"\n" +
				")\n",
			pkg:     "hello",
			version: "1.0",
			want:    nil,
		},
		{
			// The same file referenced from two branches (a USE-conditional applied
			// in more than one place) is ONE file to look for upstream. Reporting
			// it twice would make one missing patch read as two.
			name: "the same reference twice yields one filename",
			ebuild: "src_prepare() {\n" +
				"\tuse abi_x86_32 && PATCHES+=( \"${FILESDIR}/${PN}-multilib.patch\" )\n" +
				"\tuse abi_x86_64 && PATCHES+=( \"${FILESDIR}\"/${PN}-multilib.patch )\n" +
				"}\n",
			pkg:     "hello",
			version: "1.0",
			want:    []string{"hello-multilib.patch"},
		},
		{
			// Two references on ONE line, which the array-on-a-single-line form
			// produces. A scanner that stopped at the first match per line would
			// lose the second.
			name:    "two references on one line",
			ebuild:  "PATCHES=( \"${FILESDIR}/a.patch\" \"${FILESDIR}/b.patch\" )\n",
			pkg:     "hello",
			version: "1.0",
			want:    []string{"a.patch", "b.patch"},
		},
		{
			// An empty ebuild is not a special case anywhere else; it must not be
			// one here either.
			name:    "an empty ebuild yields empty",
			ebuild:  "",
			pkg:     "hello",
			version: "1.0",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ebuildFilesdirRefs([]byte(tt.ebuild), tt.pkg, tt.version)
			// Compared element by element rather than with a slice equality that
			// distinguishes nil from empty: "no references" is one outcome, and
			// which of the two spellings it arrives in is not a contract.
			if len(got) != len(tt.want) {
				t.Fatalf("ebuildFilesdirRefs = %q (%d refs), want %q (%d refs)",
					got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					// ORDER is asserted, not just membership: the caller and its
					// tests must not depend on map iteration for what they report.
					t.Errorf("ebuildFilesdirRefs[%d] = %q, want %q (full result %q, want %q)",
						i, got[i], tt.want[i], got, tt.want)
				}
			}
		})
	}
}

// AnnotateAuthorship returns NOTHING, and that is the contract rather than an
// oversight. The design's error table gives one answer to every failure this
// pass can meet — an unreadable ebuild, an unresolvable upstream tree — and that
// answer is "unproved", never an error, because verifyAgainstLocalContent
// already reads an unreadable file as "nothing is known" and two functions
// disagreeing would make one unreadable file mean two things.
//
// A signature with no error value is how that is held: there is nothing for a
// caller to drop, and an edit that added one would stop compiling here.
var _ func(*CompareReport, provider.Provider, CompareOptions) = AnnotateAuthorship

// authorshipProvider is the upstream half of the two-tree fixture: a Provider
// whose compared repository is a directory on disk, like localRootedFakeProvider
// in compare_verification_test.go, plus the two behaviours only this pass needs.
//
// It is a SECOND double rather than a flag on the first because both extras are
// assertions rather than configuration. One refuses to resolve a package — the
// "upstream package dir unresolvable" row of the design's error table, the shape
// *GitCloneProvider produces for a package ::gentoo does not carry. The other
// FAILS THE TEST when asked about a package the pass must never examine: a zero
// Authorship proves nothing by itself, since a no-op AnnotateAuthorship would
// leave exactly the same zero behind, so "never examined" is asserted where the
// question would be asked rather than where its answer would land.
type authorshipProvider struct {
	t    *testing.T
	root string
	// refuse names the atoms whose upstream package directory cannot be
	// resolved.
	refuse map[string]bool
	// offLimits names the atoms this pass must never ask about.
	offLimits map[string]bool
}

// LocalPackagePath mirrors *GitCloneProvider's: the package directory under the
// repository root.
func (p *authorshipProvider) LocalPackagePath(category, pkg string) (string, error) {
	atom := category + "/" + pkg
	if p.offLimits[atom] {
		p.t.Errorf("the authorship pass resolved the package directory for %s, whose ebuilds were never compared; "+
			"only a VerifiedDiffers result has a difference whose authorship there is anything to prove", atom)
	}
	if p.refuse[atom] {
		return "", provider.ErrNotFound
	}
	return filepath.Join(p.root, category, pkg), nil
}

// GetPackageVersions must never run. The pass annotates a report the comparison
// has already produced; a version lookup here would be a second round of
// provider traffic for every differing package, on a question already answered.
func (p *authorshipProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	p.t.Errorf("the authorship pass asked %s/%s for its versions; it annotates a finished report and looks nothing up",
		category, pkg)
	return nil, provider.ErrNotFound
}

func (p *authorshipProvider) GetName() string   { return "fake-authorship" }
func (p *authorshipProvider) SupportsAPI() bool { return false }
func (p *authorshipProvider) Close() error      { return nil }

// The double must satisfy both the Provider contract and the optional capability
// the pass type-asserts for; if it stopped satisfying either, every "unproved"
// expectation below would pass for the wrong reason.
var (
	_ provider.Provider           = (*authorshipProvider)(nil)
	_ provider.PackageDirProvider = (*authorshipProvider)(nil)
)

// writeFilesdirFile creates <root>/<category>/<pkg>/files/<rel>, the tree
// ${FILESDIR} names. rel is slash-separated, exactly as ebuildFilesdirRefs
// returns it, and is converted here so the fixture cannot accidentally agree
// with an implementation that forgot to.
func writeFilesdirFile(t *testing.T, root, category, pkg, rel string) {
	t.Helper()
	full := filepath.Join(root, category, pkg, "files", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte("upstream file\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

// comparedResult is the shape the comparison leaves behind for a package whose
// two versions are equal — the only shape a content check runs on, and therefore
// the only one that can ever reach this pass carrying VerifiedDiffers.
//
// The Verdict is set too, and asserted unchanged below: a finding never changes
// a verdict (025 R4.5), and this pass is a finding.
func comparedResult(category, pkg, version string, verified Verification) CompareResult {
	return CompareResult{
		Category:      category,
		Package:       pkg,
		LocalVersion:  version,
		RemoteVersion: version,
		Status:        StatusUpToDate,
		Verdict:       VerdictRedundant,
		Verified:      verified,
	}
}

// annotateOne runs the pass over a single result and returns it annotated.
//
// The result is HAND-BUILT rather than produced by CompareWithProvider so that
// the provider double is touched only by the code under test — a real comparison
// asks it about every package, including the ones these cases prove are never
// examined. TestAnnotateAuthorshipThroughTheRealComparison keeps the hand-built
// shape honest by running the same assertion off the real pipeline's output.
func annotateOne(t *testing.T, overlayRoot string, prov provider.Provider, result CompareResult) CompareResult {
	t.Helper()
	report := &CompareReport{Results: []CompareResult{result}}
	AnnotateAuthorship(report, prov, CompareOptions{OverlayPath: overlayRoot})
	return report.Results[0]
}

// assertUnproved fails when a result claims a proof it does not have.
//
// BOTH fields are checked. A ProvedBy left behind beside an unproved verdict
// names a file that proves nothing, and the finding line renders whatever it
// finds there — so a stale name is the same false claim as a wrong Authorship,
// arriving by a different route.
func assertUnproved(t *testing.T, got CompareResult, why string) {
	t.Helper()
	if got.Authorship != AuthorshipUnproved {
		t.Errorf("Authorship = %d, want AuthorshipUnproved (%d): %s", got.Authorship, AuthorshipUnproved, why)
	}
	if got.ProvedBy != "" {
		t.Errorf("ProvedBy = %q, want empty: %s", got.ProvedBy, why)
	}
}

// TestAnnotateAuthorship covers the decision half of the proof: given the two
// package directories, does the overlay's ebuild prove the divergence is ours?
//
// The two failure directions are as asymmetric here as they are for the resolver
// above. Reporting as unproved a package that is really ours costs the operator
// one manual diff. CLAIMING A PROOF THAT IS NOT THERE costs the opposite: the
// report asserts, with a filename, that work of ours would be lost, and R6 makes
// `prune` refuse to remove the package on the strength of that name. So every
// case below that could manufacture a proof — a flattened subdirectory, an
// unreadable file, an upstream tree that could not be resolved — asserts the
// UNPROVED outcome, and none of them is an error.
//
// Unproved never means "the change is upstream's". It means the report cannot
// tell (R2.3), which is why there is no third state to assert.
//
// _Requirements: R2.1, R2.2, R2.3_
func TestAnnotateAuthorship(t *testing.T) {
	t.Run("a reference ::gentoo does not ship proves the divergence is ours (R2.1, R2.2)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// The live case, from kde-plasma/spectacle-6.7.4.ebuild:72 — one of the
		// three packages the story's Success Metrics require this to prove.
		writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4",
			"EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n")
		// ::gentoo carries the package and a files/ tree of its own, just not
		// this patch: the miss is a real absence rather than an absent directory.
		writeFilesdirFile(t, upstreamRoot, "kde-plasma", "spectacle", "spectacle-cmake.patch")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "spectacle", "6.7.4", VerifiedDiffers))

		if got.Authorship != AuthorshipOverlay {
			t.Errorf("Authorship = %d, want AuthorshipOverlay (%d); our ebuild applies a patch ::gentoo never had, "+
				"which cannot have been inherited from ::gentoo", got.Authorship, AuthorshipOverlay)
		}
		// R2.2 names the file PACKAGE-RELATIVE — "files/<name>", not the bare
		// basename — because the operator is meant to confirm the claim by
		// looking, and the finding line already carries the atom. Printed this
		// way, `ls /var/db/repos/gentoo/kde-plasma/spectacle/files/spectacle-opencv5.patch`
		// is a copy of what the report said rather than a path the operator had
		// to re-derive from knowing what ${FILESDIR} expands to.
		if got.ProvedBy != "files/spectacle-opencv5.patch" {
			t.Errorf("ProvedBy = %q, want %q; a claim the operator cannot check without re-deriving the path is one they will not check",
				got.ProvedBy, "files/spectacle-opencv5.patch")
		}
		// A finding describes; the declaration and the version comparison decide
		// (025 R4.5). This pass may add to a result and must not edit it.
		if got.Verdict != VerdictRedundant || got.Verified != VerifiedDiffers {
			t.Errorf("Verdict/Verified = %v/%v, want redundant/differs unchanged; authorship annotates, it does not decide",
				got.Verdict, got.Verified)
		}
	})

	t.Run("a reference ::gentoo does ship proves nothing (R2.3)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// The ebuild references a patch, so a check that stopped at "does it
		// reference anything?" would call this proved. It is the copied-ebuild
		// case: the reference came from ::gentoo along with the rest of the file,
		// and the difference is somewhere else entirely — possibly ::gentoo's own
		// in-place revision.
		writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4",
			"EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-gentoo.patch\" )\n")
		writeFilesdirFile(t, upstreamRoot, "kde-plasma", "spectacle", "spectacle-gentoo.patch")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "spectacle", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "::gentoo ships every file the ebuild references, so nothing in it originates here")
	})

	t.Run("an ebuild that references nothing is unproved (R2.3)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// The live shape of four of the eight undeclared divergences: a single
		// PYTHON_COMPAT line, revised upstream in place with no revbump, and not
		// a ${FILESDIR} reference anywhere in the file.
		writeVerifyEbuild(t, overlayRoot, "kde-plasma", "kwin", "6.7.4",
			"EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "kwin", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "the ebuild names no file at all, so its content proves nothing either way")
	})

	t.Run("an absent ebuild is unproved and never an error", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// Nothing written: the overlay tree has no such package. The pass must
		// answer "nothing is known", exactly as verifyAgainstLocalContent does
		// one function over.

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "drkonqi", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "an ebuild that could not be read yields no references, and no references is not a proof")
	})

	t.Run("an unreadable ebuild is unproved and never an error", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// A DIRECTORY where the ebuild belongs, rather than a chmod 000 file:
		// the read then fails for every user, including root, which is what the
		// CI runner is. A permission-based fixture would silently stop failing
		// there and the case would prove nothing.
		notAFile := filepath.Join(overlayRoot, "kde-plasma", "drkonqi", "drkonqi-6.7.4.ebuild")
		if err := os.MkdirAll(notAFile, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", notAFile, err)
		}

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "drkonqi", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "an unreadable file means nothing is known; making it mean something here "+
			"would give one failure two meanings across two functions")
	})

	t.Run("an unresolvable upstream package directory is unproved and never an error", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// Our side proves everything it can: a reference to a patch that exists
		// in no tree at all. Only the upstream lookup fails, so a pass that
		// treated an unresolvable directory as an empty one would report this as
		// proved — from a comparison it never made.
		writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4",
			"PATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n")
		prov := &authorshipProvider{
			t:      t,
			root:   upstreamRoot,
			refuse: map[string]bool{"kde-plasma/spectacle": true},
		}

		got := annotateOne(t, overlayRoot, prov, comparedResult("kde-plasma", "spectacle", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "the upstream tree could not be resolved, so nothing was compared against; "+
			"an absent answer is not a missing file")
	})

	t.Run("an unknown overlay root is unproved and never an error", func(t *testing.T) {
		upstreamRoot := t.TempDir()
		// PackageInfo carries no path, so an empty OverlayPath is the degraded
		// mode in which our side of the comparison simply cannot be opened. It is
		// already NotVerified in practice; the pass must not invent a second
		// meaning for it.

		got := annotateOne(t, "", &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("kde-plasma", "spectacle", "6.7.4", VerifiedDiffers))

		assertUnproved(t, got, "with no overlay root there is no ebuild of ours to read")
	})

	t.Run("only the reference ::gentoo lacks is named (R2.2)", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// Three references, and the missing one is in the MIDDLE: a pass that
		// stopped at the first reference, or reported the last one it looked at,
		// would name a file ::gentoo ships and send the operator to confirm a
		// claim that is not the one the report made.
		writeVerifyEbuild(t, overlayRoot, "net-libs", "nodejs", "26.7.0",
			"src_prepare() {\n"+
				"\tPATCHES+=( \"${FILESDIR}/${PN}-24.11.1-restore-ppc64be.patch\" )\n"+
				"\tPATCHES+=( \"${FILESDIR}\"/${PN}-26.5.1-paxmarking.patch )\n"+
				"\tPATCHES+=( \"${FILESDIR}/${PN}-shared-deps.patch\" )\n"+
				"}\n")
		writeFilesdirFile(t, upstreamRoot, "net-libs", "nodejs", "nodejs-24.11.1-restore-ppc64be.patch")
		writeFilesdirFile(t, upstreamRoot, "net-libs", "nodejs", "nodejs-shared-deps.patch")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("net-libs", "nodejs", "26.7.0", VerifiedDiffers))

		if got.Authorship != AuthorshipOverlay {
			t.Fatalf("Authorship = %d, want AuthorshipOverlay (%d); one of the three references exists in no ::gentoo tree",
				got.Authorship, AuthorshipOverlay)
		}
		if got.ProvedBy != "files/nodejs-26.5.1-paxmarking.patch" {
			t.Errorf("ProvedBy = %q, want %q; the name must be the reference that actually proves the point, "+
				"not whichever one the scan happened to end on", got.ProvedBy, "files/nodejs-26.5.1-paxmarking.patch")
		}
	})

	t.Run("a subdirectory reference ::gentoo ships proves nothing", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// mail-client/thunderbird-153.0.2.ebuild:1056 writes this verbatim, and
		// it is the case that makes flattening dangerous. An implementation that
		// dropped the "icon/" and stat'ed files/thunderbird-r2.desktop would find
		// nothing — that path exists in no repository — and report the miss as
		// PROOF, for an ebuild that took the line from ::gentoo unchanged.
		writeVerifyEbuild(t, overlayRoot, "mail-client", "thunderbird", "153.0.2",
			"\tlocal desktop_file=\"${FILESDIR}/icon/${PN}-r2.desktop\"\n")
		writeFilesdirFile(t, upstreamRoot, "mail-client", "thunderbird", "icon/thunderbird-r2.desktop")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("mail-client", "thunderbird", "153.0.2", VerifiedDiffers))

		assertUnproved(t, got, "::gentoo ships files/icon/thunderbird-r2.desktop; only a reference flattened to its "+
			"basename would look elsewhere and call the miss a proof")
	})

	t.Run("a reference that could not be looked up is not an absence", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "mail-client", "thunderbird", "153.0.2",
			"\tlocal desktop_file=\"${FILESDIR}/icon/${PN}-r2.desktop\"\n")
		// A regular FILE where files/icon/ would be a directory, so the stat
		// fails with ENOTDIR rather than ENOENT. It is a permission error or an
		// I/O error wearing a shape that survives running as root, which the CI
		// runner does — a chmod-based fixture would silently stop failing there.
		//
		// Strictly, ENOTDIR does prove the path cannot exist, and this case
		// therefore gives up a proof it could have drawn. That is the deliberate
		// trade: the check would otherwise have to reason about which errno means
		// "definitely absent" and which means "I could not look", and the class it
		// must never misread — EACCES, EIO — is indistinguishable from here. One
		// lost proof costs a manual diff; one invented proof tells the operator, by
		// name, that work of ours would be lost.
		blocked := filepath.Join(upstreamRoot, "mail-client", "thunderbird", "files", "icon")
		if err := os.MkdirAll(filepath.Dir(blocked), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(blocked), err)
		}
		if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", blocked, err)
		}

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("mail-client", "thunderbird", "153.0.2", VerifiedDiffers))

		assertUnproved(t, got, "the stat failed for a reason other than the file being absent, and a check that "+
			"could not look has found nothing")
	})

	t.Run("a subdirectory reference ::gentoo lacks is named where the file would live", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		writeVerifyEbuild(t, overlayRoot, "mail-client", "thunderbird", "153.0.2",
			"\tlocal desktop_file=\"${FILESDIR}/icon/${PN}-r2.desktop\"\n")
		// The FLAT name exists upstream and the subdirectory one does not, which
		// is the mirror of the case above: a flattening implementation finds this
		// file, concludes ::gentoo ships it, and loses a real proof.
		writeFilesdirFile(t, upstreamRoot, "mail-client", "thunderbird", "thunderbird-r2.desktop")

		got := annotateOne(t, overlayRoot, &authorshipProvider{t: t, root: upstreamRoot},
			comparedResult("mail-client", "thunderbird", "153.0.2", VerifiedDiffers))

		if got.Authorship != AuthorshipOverlay {
			t.Fatalf("Authorship = %d, want AuthorshipOverlay (%d); ::gentoo has no files/icon/ at all",
				got.Authorship, AuthorshipOverlay)
		}
		// The recorded name keeps the subdirectory, in slash form, for the same
		// reason it keeps the "files/" prefix: it is the path the operator pastes
		// after the package directory to see for themselves. "thunderbird-r2.desktop"
		// would name a file that exists — in the wrong place — and the check would
		// read as wrong when it was right.
		if got.ProvedBy != "files/icon/thunderbird-r2.desktop" {
			t.Errorf("ProvedBy = %q, want %q; the subdirectory is part of the answer, and dropping it names a "+
				"different file that happens to exist", got.ProvedBy, "files/icon/thunderbird-r2.desktop")
		}
	})

	t.Run("a package whose ebuilds were never compared is never examined", func(t *testing.T) {
		overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
		// ALL THREE packages have a proving ebuild on disk, so a pass that looked
		// at the wrong ones would find real proof rather than nothing. The zero
		// Authorship asserted below therefore cannot arrive by accident — and the
		// provider fails the test the moment either off-limits package is asked
		// about, so the assertion does not depend on the zero at all.
		for _, name := range []string{"spectacle", "kwin", "drkonqi"} {
			writeVerifyEbuild(t, overlayRoot, "kde-plasma", name, "6.7.4",
				"PATCHES=( \"${FILESDIR}/${PN}-ours.patch\" )\n")
		}
		prov := &authorshipProvider{t: t, root: upstreamRoot, offLimits: map[string]bool{
			"kde-plasma/kwin":    true, // the two ebuilds were byte-identical
			"kde-plasma/drkonqi": true, // no content check ran at all
		}}

		report := &CompareReport{Results: []CompareResult{
			comparedResult("kde-plasma", "spectacle", "6.7.4", VerifiedDiffers),
			comparedResult("kde-plasma", "kwin", "6.7.4", VerifiedIdentical),
			comparedResult("kde-plasma", "drkonqi", "6.7.4", NotVerified),
		}}
		AnnotateAuthorship(report, prov, CompareOptions{OverlayPath: overlayRoot})

		// The differing one IS annotated, which is what proves the pass ran at
		// all: without this the two zeroes below would also be satisfied by a
		// function that did nothing.
		if report.Results[0].Authorship != AuthorshipOverlay {
			t.Fatalf("Authorship for the differing package = %d, want AuthorshipOverlay (%d); "+
				"the pass did not run, so the untouched results below prove nothing",
				report.Results[0].Authorship, AuthorshipOverlay)
		}
		assertUnproved(t, report.Results[1], "byte-identical ebuilds hold no difference whose authorship there is anything to prove")
		assertUnproved(t, report.Results[2], "no content was compared, so there is no difference to attribute")
	})
}

// TestAnnotateAuthorshipThroughTheRealComparison runs the actual comparison and
// then the pass over its output.
//
// The cases above hand-build their CompareResults, which is what lets the
// provider double assert that some packages are never asked about — but a
// hand-built fixture can drift into a shape production never produces, and every
// assertion made on it would go on passing. This runs CompareWithProvider for
// real, over a temporary overlay and upstream tree, and annotates what it
// returns: the same proof, reached the way runCompare will reach it.
func TestAnnotateAuthorshipThroughTheRealComparison(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4",
		"EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n")
	writeVerifyEbuild(t, upstreamRoot, "kde-plasma", "spectacle", "6.7.4",
		"EAPI=8\ninherit ecm\n")
	writeFilesdirFile(t, upstreamRoot, "kde-plasma", "spectacle", "spectacle-cmake.patch")
	prov := &localRootedFakeProvider{
		root:     upstreamRoot,
		versions: map[string][]string{"kde-plasma/spectacle": {"6.7.4"}},
	}

	// ONE options value, passed to both calls, exactly as runCompare will do it:
	// a pass that needed options the comparison was not given would compile here
	// and fail in production.
	opts := CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence:    map[string]Divergence{"kde-plasma/spectacle": {}},
	}
	report, err := CompareWithProvider(
		[]PackageInfo{{Category: "kde-plasma", Package: "spectacle", LatestVersion: "6.7.4"}}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	if report.Results[0].Verified != VerifiedDiffers {
		t.Fatalf("Verified = %v, want VerifiedDiffers (fixture check)", report.Results[0].Verified)
	}

	AnnotateAuthorship(report, prov, opts)

	got := report.Results[0]
	if got.Authorship != AuthorshipOverlay || got.ProvedBy != "files/spectacle-opencv5.patch" {
		t.Errorf("Authorship/ProvedBy = %d/%q, want AuthorshipOverlay (%d)/%q",
			got.Authorship, got.ProvedBy, AuthorshipOverlay, "files/spectacle-opencv5.patch")
	}
	// The comparison's own findings survive the annotation untouched: this pass
	// adds a column to the report, it does not rewrite one (025 R4.5).
	if got.Verdict != VerdictRedundant {
		t.Errorf("Verdict = %v, want VerdictRedundant; authorship must not change a verdict", got.Verdict)
	}
}
