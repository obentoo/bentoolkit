// Copyright 2026 The Bentoolkit Authors
// SPDX-License-Identifier: GPL-3.0-or-later

package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubstituteCommitHash_LeavesVendoredRevisionsAlone is the regression test for
// the missing left-hand anchor. Before it, the quoted alternation matched the
// COMMIT at the END of any identifier, so every `<ANYTHING>_COMMIT="<sha>"` in the
// file was rewritten with the package's own commit.
//
// The two bodies are the shapes the overlay actually ships, reduced to what the
// substitution sees:
//
//   - zed pins the rev= of its git dependencies in seven `local *_COMMIT` inside
//     src_prepare, which feed a sed that rewrites [patch.crates-io] from `git =`
//     to `path =`. Clobbering them makes the sed a no-op, the git reference
//     survives, and cargo needs a network the sandbox does not have.
//   - mesa pins the venus-protocol subproject in VENUS_PROTOCOL_COMMIT, next to
//     its own tab-indented GIT_COMMIT — which also pins the anchor to leading
//     whitespace, since a `^`-only anchor would stop matching mesa entirely.
func TestSubstituteCommitHash_LeavesVendoredRevisionsAlone(t *testing.T) {
	const newSHA = "1111111111111111111111111111111111111111"

	tests := []struct {
		name      string
		body      string
		rewritten []string // assignments expected to carry newSHA afterwards
		preserved []string // assignments expected to survive verbatim
	}{
		{
			name: "zed: EGIT_COMMIT at the margin, git-dep revs under local",
			body: `EAPI=8
EGIT_COMMIT="6bf539cd52126974eb0dbff667de02a696a737ec"
S="${WORKDIR}/${PN}-${EGIT_COMMIT}"

src_prepare() {
	local ASYNC_PROCESS_COMMIT="0b6d6713570af61806e1e5cb40e0f757cb93fd9d"
	local TREE_SITTER_COMMIT="dff1fd868c750dbbae179fcd5c43ce987e4e0528"
}
`,
			rewritten: []string{`EGIT_COMMIT="` + newSHA + `"`},
			preserved: []string{
				`local ASYNC_PROCESS_COMMIT="0b6d6713570af61806e1e5cb40e0f757cb93fd9d"`,
				`local TREE_SITTER_COMMIT="dff1fd868c750dbbae179fcd5c43ce987e4e0528"`,
			},
		},
		{
			name: "mesa: tab-indented GIT_COMMIT, subproject rev at the margin",
			body: `EAPI=8
if [[ ${PV} == *_pre* ]]; then
	GIT_COMMIT="3f1b217baffffa00cb8f53e158713a33e1bd4632"
fi
VENUS_PROTOCOL_COMMIT="e94b12f301b9eb27ebead757128a18420b4f7994"
`,
			rewritten: []string{"\tGIT_COMMIT=\"" + newSHA + "\""},
			preserved: []string{`VENUS_PROTOCOL_COMMIT="e94b12f301b9eb27ebead757128a18420b4f7994"`},
		},
		{
			name: "asus-ec-sensors: bare COMMIT is still the package's own",
			body: `EAPI=8
COMMIT="503d0d3d3858f463973f2cfce4a3aa0173567500"
KERNEL_COMMIT="abcdef0123456789abcdef0123456789abcdef01"
`,
			rewritten: []string{`COMMIT="` + newSHA + `"`},
			preserved: []string{`KERNEL_COMMIT="abcdef0123456789abcdef0123456789abcdef01"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "demo-1.0.ebuild")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if err := substituteCommitHash(path, newSHA); err != nil {
				t.Fatalf("substituteCommitHash: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			for _, want := range tc.rewritten {
				if !strings.Contains(string(got), want) {
					t.Errorf("the package's own commit variable was not rewritten\nwant line: %s\ngot:\n%s", want, got)
				}
			}
			for _, want := range tc.preserved {
				if !strings.Contains(string(got), want) {
					t.Errorf("a vendored revision was clobbered with the package's commit\nwant line: %s\ngot:\n%s", want, got)
				}
			}
			if n := strings.Count(string(got), newSHA); n != len(tc.rewritten) {
				t.Errorf("the new SHA appears %d times, want %d — it leaked into an assignment that is not the package's own:\n%s", n, len(tc.rewritten), got)
			}
		})
	}
}

// TestSubstituteCommitHash_UnquotedCOMMITStillAnchored keeps the unquoted spelling
// (sqlitebrowser) under the same rule as the quoted one.
func TestSubstituteCommitHash_UnquotedCOMMITStillAnchored(t *testing.T) {
	const newSHA = "1111111111111111111111111111111111111111"
	body := "EAPI=8\nCOMMIT=503d0d3d3858f463973f2cfce4a3aa0173567500\n" +
		"\tlocal VENDOR_COMMIT=abcdef0123456789abcdef0123456789abcdef01\n"
	path := filepath.Join(t.TempDir(), "demo-1.0.ebuild")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := substituteCommitHash(path, newSHA); err != nil {
		t.Fatalf("substituteCommitHash: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(got), "COMMIT="+newSHA+"\n") {
		t.Errorf("unquoted COMMIT= was not rewritten:\n%s", got)
	}
	if !strings.Contains(string(got), "VENDOR_COMMIT=abcdef0123456789abcdef0123456789abcdef01") {
		t.Errorf("a vendored revision was clobbered:\n%s", got)
	}
}
