package validate

import (
	"context"
	"os/exec"
	"slices"
	"testing"
)

// emergeBlockedByInstalledZeo is `emerge -p --quiet --color=n
// =app-editors/zed-1.30.2::bentoo` on the 2026-11-28 host: the installed
// app-editors/zeo carries `!app-editors/zed` in RDEPEND, so Portage names the
// atom (zed, the candidate) first and zeo as the package that blocks it.
const emergeBlockedByInstalledZeo = "[ebuild  N    ] app-editors/zed-1.30.2::bentoo\n" +
	"[blocks B      ] app-editors/zed (\"app-editors/zed\" is hard blocking app-editors/zeo-0.32.0_p20261127)\n" +
	"\n * Error: The above package list contains packages which cannot be\n" +
	" * installed at the same time on the same system.\n" +
	"\n  (app-editors/zeo-0.32.0_p20261127:0/0::bentoo, installed) pulled in by\n" +
	"    app-editors/zeo required by @selected\n"

// TestDependenciesSatisfied_NamesTheInstalledBlockerNotTheCandidate: the hint
// is the operator's next command, so it must name what to remove. Read from the
// atom alone, the 2026-11-28 run said "remove app-editors/zed, which blocks
// it" for the zed bump itself.
func TestDependenciesSatisfied_NamesTheInstalledBlockerNotTheCandidate(t *testing.T) {
	deps := BuildDeps{
		ExecCommand: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", `printf %s "$1"; exit 1`, "sh", emergeBlockedByInstalledZeo)
		},
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
	}

	ok, missing, err := DependenciesSatisfied(context.Background(), t.TempDir(), "app-editors/zed", "1.30.2", deps)
	if err != nil {
		t.Fatalf("a blocked resolve was reported as undetermined: %v", err)
	}
	if ok {
		t.Fatal("a blocked resolve was reported as satisfied")
	}
	if want := []string{"app-editors/zeo" + blockerNote}; !slices.Equal(missing, want) {
		t.Fatalf("unsatisfied = %q, want %q", missing, want)
	}
	if got, want := DependencyAction(missing), "remove app-editors/zeo, which blocks it,"; got != want {
		t.Errorf("DependencyAction = %q, want %q", got, want)
	}
}

func TestBlockersFrom_ReadsEveryPortageSpelling(t *testing.T) {
	cases := []struct {
		name, out string
		want      []string
	}{
		{
			name: "the candidate carries the blocker: remove the installed atom",
			out:  `[blocks B      ] app-editors/zeo ("app-editors/zeo" is hard blocking app-editors/zed-1.30.2)`,
			want: []string{"app-editors/zeo"},
		},
		{
			name: "unquoted form, several installed parents, deduplicated",
			out: "[blocks B      ] app-editors/zed (is soft blocking app-editors/zeo-0.32.0_p20261127, app-editors/zeo-bin-1.0-r1)\n" +
				"[blocks b      ] app-editors/zed (is soft blocking app-editors/zeo-0.32.0_p20261127)\n",
			want: []string{"app-editors/zeo", "app-editors/zeo-bin"},
		},
		{
			name: "an unrecognised trailer falls back to the atom",
			out:  `[blocks B      ] <sys-apps/foo-2`,
			want: []string{"<sys-apps/foo-2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, name := range tc.want {
				want = append(want, name+blockerNote)
			}
			if got := blockersFrom(tc.out, "app-editors/zed"); !slices.Equal(got, want) {
				t.Errorf("blockersFrom = %q, want %q", got, want)
			}
		})
	}
}
