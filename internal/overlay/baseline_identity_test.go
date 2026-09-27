package overlay

import "testing"

// TestPickBaselineIdentifiesOursByVersionText guards the identity check in
// pickBaseline: two ebuild files are identified by their version text, and PMS
// equality (1.0 and 1.0-r0, 1.010 and 1.01) is not file identity.
//
// The hostile fixtures are the pairs that STILL compare equal once the
// comparison follows PMS. The pair that used to exercise this check, 1.0 and
// 1.0.0, no longer compares equal, so it can no longer tell a text check from a
// comparison check; these pairs can. Each puts the PMS-equal impostor first, so
// an identity check rewritten as `CompareVersions(...) == 0` returns the
// impostor and fails here. That each impostor compares equal to ours is pinned
// by TestCompareVersionsPMS in internal/common/ebuild, not re-asserted here, so
// this guard holds before and after the comparison changes.
func TestPickBaselineIdentifiesOursByVersionText(t *testing.T) {
	cases := []struct {
		name    string
		ours    string
		carried []string
		want    string
		why     string
	}{
		{
			name: "the -r0 spelling is not our ebuild", ours: "1.0",
			carried: []string{"1.0-r0", "1.0"}, want: "1.0",
			why: "1.0-r0 orders equal to 1.0 but is another file; the exact version text is the answer",
		},
		{
			name: "a trailing-zero spelling is not our ebuild", ours: "1.01",
			carried: []string{"1.010", "1.01"}, want: "1.01",
			why: "1.010 orders equal to 1.01 but is another file",
		},
		{
			name: "our -r0 spelling is not the bare version", ours: "1.0-r0",
			carried: []string{"1.0", "1.0-r0"}, want: "1.0-r0",
			why: "the converse: the bare 1.0 orders equal to our 1.0-r0 and is still another file",
		},
		{
			name: "a third spelling does not join the pair", ours: "1.0",
			carried: []string{"1.00", "1.0-r0", "1.0.0", "1.0"}, want: "1.0",
			why: "1.00, 1.0-r0 and 1.0 all order equal; only the exact text is ours",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickBaseline(pickBaselineCandidates("foo", tc.carried), tc.ours)

			if got.version != tc.want {
				t.Errorf("with ours at %s and ::gentoo carrying %v, the baseline chosen is %s, want %s — %s",
					tc.ours, tc.carried, got.version, tc.want, tc.why)
			}
			if want := "foo-" + tc.want + ".ebuild"; got.filename != want {
				t.Errorf("the chosen candidate's filename is %q, want %q", got.filename, want)
			}
		})
	}
}
