package targets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExpandBraces(t *testing.T) {
	cases := map[string][]string{
		"python3_{12..15}": {"python3_12", "python3_13", "python3_14", "python3_15"},
		"python3_{13,14}":  {"python3_13", "python3_14"},
		"lua5-{1..2}":      {"lua5-1", "lua5-2"},
		"pypy3_11":         {"pypy3_11"},
		"python3_{14..13}": {"python3_14", "python3_13"},
		"a{x}b":            {"a{x}b"},
		"{1,2}_{a,b}":      {"1_a", "1_b", "2_a", "2_b"},
	}
	for in, want := range cases {
		if got := ExpandBraces(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ExpandBraces(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFromIUSE(t *testing.T) {
	got := FromIUSE("X +python python_single_target_python3_12 python_single_target_python3_15 lua_single_target_luajit -ruby_targets_ruby34 perl_features_ithreads")
	want := Offered{
		"python_single_target": {"python3_12", "python3_15"},
		"lua_single_target":    {"luajit"},
		"ruby_targets":         {"ruby34"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FromIUSE = %v, want %v", got, want)
	}
}

func TestFromEbuild(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		want    Offered
		wantErr string
	}{
		{
			name: "python-single-r1 and lua-single",
			text: "PYTHON_COMPAT=( python3_{12..14} )\nLUA_COMPAT=( lua5-{3..4} luajit )\ninherit lua-single python-single-r1 xdg\n",
			want: Offered{"python_single_target": {"python3_12", "python3_13", "python3_14"}, "lua_single_target": {"lua5-3", "lua5-4", "luajit"}},
		},
		{
			name: "distutils-r1 defaults to python_targets",
			text: "PYTHON_COMPAT=(\n\tpython3_{13..15}\n)\ninherit distutils-r1\n",
			want: Offered{"python_targets": {"python3_13", "python3_14", "python3_15"}},
		},
		{
			name: "DISTUTILS_SINGLE_IMPL selects python_single_target",
			text: "DISTUTILS_SINGLE_IMPL=1\nPYTHON_COMPAT=( python3_15 )\ninherit distutils-r1\n",
			want: Offered{"python_single_target": {"python3_15"}},
		},
		{
			name: "ruby-fakegem reads USE_RUBY",
			text: "USE_RUBY=\"ruby33 ruby34\"\ninherit ruby-fakegem\n",
			want: Offered{"ruby_targets": {"ruby33", "ruby34"}},
		},
		{
			name: "python-any-r1 generates no flags",
			text: "PYTHON_COMPAT=( python3_12 )\ninherit python-any-r1\n",
			want: Offered{},
		},
		{
			name:    "inherited but not assigned",
			text:    "inherit python-r1\n",
			wantErr: "not assigned literally",
		},
		{
			name:    "built from another variable",
			text:    "PYTHON_COMPAT=( ${MY_COMPAT} )\ninherit python-r1\n",
			wantErr: "built from another variable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromEbuild([]byte(tc.text))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecodePolicyRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":          "[required]\npython_targets = [\"python3_15\"]\n[other]\nx = 1\n",
		"unknown group":        "[required]\nperl_features = [\"ithreads\"]\n",
		"empty group":          "[required]\npython_targets = []\n",
		"exempt without why":   "[[exempt]]\natom = \"a/b\"\ngroups = [\"python_targets\"]\n",
		"exempt bad atom":      "[[exempt]]\natom = \"b\"\ngroups = [\"python_targets\"]\nreason = \"x\"\n",
		"exempt unknown group": "[[exempt]]\natom = \"a/b\"\ngroups = [\"nope\"]\nreason = \"x\"\n",
		"exempt no groups":     "[[exempt]]\natom = \"a/b\"\nreason = \"x\"\n",
		"not toml":             "[required\n",
	}
	for name, text := range cases {
		if _, err := DecodePolicy([]byte(text)); err == nil {
			t.Errorf("%s: DecodePolicy accepted %q", name, text)
		}
	}
}

func TestLoadPolicyMissingIsNoPolicy(t *testing.T) {
	if _, err := LoadPolicy(t.TempDir()); !errors.Is(err, ErrNoPolicy) {
		t.Fatalf("err = %v, want ErrNoPolicy", err)
	}
}

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	p, err := DecodePolicy([]byte(`
[required]
python_targets = ["python3_15"]
python_single_target = ["python3_15"]

[[exempt]]
atom = "dev-python/old"
groups = ["python_targets"]
reason = "upstream is stuck on 3.14"
`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheck(t *testing.T) {
	p := testPolicy(t)
	gaps := p.Check("app-editors/vim", Offered{"python_single_target": {"python3_14", "python3_12"}, "lua_single_target": {"luajit"}})
	want := []Gap{{Group: "python_single_target", Missing: []string{"python3_15"}, Offered: []string{"python3_12", "python3_14"}}}
	if !reflect.DeepEqual(gaps, want) {
		t.Errorf("gaps = %v, want %v", gaps, want)
	}
	if got := p.Check("dev-python/old", Offered{"python_targets": {"python3_14"}}); got != nil {
		t.Errorf("an exempted group reported %v", got)
	}
	if got := p.Check("x/y", Offered{}); got != nil {
		t.Errorf("a package using no group reported %v", got)
	}
	if got := p.Check("x/y", Offered{"python_targets": {"python3_15"}}); got != nil {
		t.Errorf("a satisfied package reported %v", got)
	}
}

func TestMentionsRequiredGroup(t *testing.T) {
	p := testPolicy(t)
	if !p.MentionsRequiredGroup("python? ( exactly-one-of ( python_single_target_python3_12 ) )") {
		t.Error("a python_single_target constraint was not recognised")
	}
	if p.MentionsRequiredGroup("images? ( !minimal )") {
		t.Error("an unrelated constraint was attributed to the policy")
	}
}

func TestSplitPF(t *testing.T) {
	cases := map[string][2]string{
		"vim-9.2.1302":            {"vim", "9.2.1302"},
		"vim-9.2.1302-r1":         {"vim", "9.2.1302-r1"},
		"font-adobe-100dpi-1.0.4": {"font-adobe-100dpi", "1.0.4"},
		"gst-plugins-bad-1.29.2":  {"gst-plugins-bad", "1.29.2"},
		"python-3.15.0_beta1":     {"python", "3.15.0_beta1"},
	}
	for pf, want := range cases {
		pn, pvr, ok := SplitPF(pf)
		if !ok || pn != want[0] || pvr != want[1] {
			t.Errorf("SplitPF(%q) = %q %q %v, want %q %q", pf, pn, pvr, ok, want[0], want[1])
		}
	}
	if _, _, ok := SplitPF("noversion"); ok {
		t.Error("SplitPF accepted a name without a version")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureTrees builds an overlay and a master reproducing the vim case:
// ::gentoo carries a higher vim without python3_15, the overlay a lower one with it.
func fixtureTrees(t *testing.T) (overlay, gentoo string) {
	t.Helper()
	overlay, gentoo = t.TempDir(), t.TempDir()
	write(t, filepath.Join(overlay, "profiles", "repo_name"), "bentoo\n")

	// vim: overtaken by the master.
	write(t, filepath.Join(overlay, "app-editors/vim/vim-9.2.1275.ebuild"), "")
	write(t, filepath.Join(overlay, "metadata/md5-cache/app-editors/vim-9.2.1275"),
		"IUSE=python python_single_target_python3_14 python_single_target_python3_15\nKEYWORDS=~amd64\n")
	write(t, filepath.Join(gentoo, "metadata/md5-cache/app-editors/vim-9.2.1302"),
		"IUSE=python python_single_target_python3_12 python_single_target_python3_14\nKEYWORDS=amd64 ~arm64\n")
	write(t, filepath.Join(gentoo, "metadata/md5-cache/app-editors/vim-9999"),
		"IUSE=python python_single_target_python3_12\nKEYWORDS=\n")

	// fire: the overlay's own gap, with no cache entry (read from the text).
	write(t, filepath.Join(overlay, "dev-python/fire/fire-0.7.1.ebuild"),
		"PYTHON_COMPAT=( python3_{12..14} )\ninherit distutils-r1\nKEYWORDS=\"~amd64\"\n")

	// ok: equal versions in both repos; the overlay's wins and is complete.
	write(t, filepath.Join(overlay, "dev-libs/ok/ok-1.0.ebuild"), "")
	write(t, filepath.Join(overlay, "metadata/md5-cache/dev-libs/ok-1.0"),
		"IUSE=python_targets_python3_15\nKEYWORDS=~amd64\n")
	write(t, filepath.Join(gentoo, "metadata/md5-cache/dev-libs/ok-1.0"),
		"IUSE=python_targets_python3_14\nKEYWORDS=amd64\n")

	// hidden: an overlay version not keyworded for amd64 is not a candidate.
	write(t, filepath.Join(overlay, "dev-libs/hidden/hidden-2.0.ebuild"), "")
	write(t, filepath.Join(overlay, "metadata/md5-cache/dev-libs/hidden-2.0"),
		"IUSE=python_targets_python3_14\nKEYWORDS=~arm64\n")

	// master-only: reported only with All.
	write(t, filepath.Join(gentoo, "metadata/md5-cache/dev-libs/elsewhere-3.0"),
		"IUSE=python_targets_python3_14\nKEYWORDS=amd64\n")
	return overlay, gentoo
}

func TestSweep(t *testing.T) {
	overlay, gentoo := fixtureTrees(t)
	p := testPolicy(t)

	findings, err := Sweep(context.Background(), p, SweepOptions{Overlay: overlay, OverlayRepo: "bentoo", Gentoo: gentoo})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Finding{}
	for _, f := range findings {
		got[f.Atom] = f
	}
	if len(got) != 2 {
		t.Fatalf("findings = %+v, want vim and fire only", findings)
	}
	vim := got["app-editors/vim"]
	if vim.Version != "9.2.1302" || vim.Repo != "gentoo" || vim.OverlayVersion != "9.2.1275" {
		t.Errorf("vim = %+v, want ::gentoo 9.2.1302 overtaking the overlay's 9.2.1275", vim)
	}
	fire := got["dev-python/fire"]
	if fire.Repo != "bentoo" || len(fire.Gaps) != 1 || fire.Gaps[0].Group != "python_targets" || fire.OverlayVersion != "" {
		t.Errorf("fire = %+v, want one python_targets gap from the uncached overlay ebuild", fire)
	}

	all, err := Sweep(context.Background(), p, SweepOptions{Overlay: overlay, OverlayRepo: "bentoo", Gentoo: gentoo, All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("All sweep = %+v, want the master-only package as well", all)
	}
}

func TestSweepReportsUnreadableOffer(t *testing.T) {
	overlay := t.TempDir()
	write(t, filepath.Join(overlay, "dev-libs/odd/odd-1.0.ebuild"), "inherit python-r1\n")
	findings, err := Sweep(context.Background(), testPolicy(t), SweepOptions{Overlay: overlay, OverlayRepo: "bentoo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].Unreadable == "" {
		t.Fatalf("findings = %+v, want one unreadable offer", findings)
	}
}

func TestSweepCancelled(t *testing.T) {
	overlay, gentoo := fixtureTrees(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Sweep(ctx, testPolicy(t), SweepOptions{Overlay: overlay, OverlayRepo: "bentoo", Gentoo: gentoo}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSweepMissingMaster(t *testing.T) {
	overlay, _ := fixtureTrees(t)
	if _, err := Sweep(context.Background(), testPolicy(t), SweepOptions{Overlay: overlay, OverlayRepo: "bentoo", Gentoo: filepath.Join(t.TempDir(), "none")}); err == nil {
		t.Fatal("a missing master md5-cache was not reported")
	}
}

func TestRepoName(t *testing.T) {
	dir := t.TempDir()
	if _, err := RepoName(dir); err == nil {
		t.Error("a missing repo_name was accepted")
	}
	write(t, filepath.Join(dir, "profiles", "repo_name"), "bad name\n")
	if _, err := RepoName(dir); err == nil {
		t.Error("a repo_name with a space was accepted")
	}
	write(t, filepath.Join(dir, "profiles", "repo_name"), "bentoo\n")
	if name, err := RepoName(dir); err != nil || name != "bentoo" {
		t.Errorf("RepoName = %q, %v", name, err)
	}
}
