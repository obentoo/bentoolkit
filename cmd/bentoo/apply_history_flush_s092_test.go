package main

// Authored for story 092, sub-task 1.3: the run history is printed to the
// scrollback once, after the live region is released (R1.5), and a failure
// that left a repair bundle names --repair on its ✗ row (R2.2).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// s092ApplyAllFixture sets up an overlay whose n pending bumps all fail at the
// manifest step (every tool is a stub exiting 1), each keeping a staged tree
// and so a repair bundle. It returns the packages in pending order.
func s092ApplyAllFixture(t *testing.T, auOpts *autoupdateOptions, n int) []string {
	t.Helper()
	overlay, configDir := s056AutoupdateEnv(t, auOpts)
	oApply, oDistdir := auOpts.apply, auOpts.distdir
	t.Cleanup(func() { auOpts.apply, auOpts.distdir = oApply, oDistdir })
	auOpts.check = false
	auOpts.apply = "all"
	auOpts.distdir = t.TempDir()

	if err := os.WriteFile(filepath.Join(overlay, "profiles", "repo_name"), []byte("bentoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	pkgs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("flush%02d", i)
		pkg := "dev-go/" + name
		dir := filepath.Join(overlay, "dev-go", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+"-1.0.ebuild"), []byte("EAPI=8\nSLOT=\"0\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := pending.Add(autoupdate.PendingUpdate{Package: pkg, CurrentVersion: "1.0", NewVersion: "1.1", Status: autoupdate.StatusPending}); err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, pkg)
	}

	toolDir := t.TempDir()
	for _, tool := range []string{"claude", "pkgdev", "pkgcheck", "emerge", "ebuild", "egencache", "portageq"} {
		writeStub(t, toolDir, tool, "exit 1\n")
	}
	t.Setenv("PATH", toolDir+":/usr/bin:/bin")
	// The live region is drawn only when nothing opts out of it.
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI", "BENTOO_UI"} {
		t.Setenv(env, "")
	}
	return pkgs
}

// s092Screen replays a captured terminal stream on a minimal screen and returns
// the rows left visible, so what bubbletea erased or redrew counts as a
// terminal shows it rather than as bytes in the capture. It interprets the
// sequences the inline renderer emits — cursor up, carriage return, line feed,
// erase line and erase below — and drops every other escape sequence.
func s092Screen(stream string) []string {
	var rows [][]rune
	row, col := 0, 0
	grow := func() {
		for len(rows) <= row {
			rows = append(rows, nil)
		}
	}
	grow()
	rs := []rune(stream)
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\r':
			col = 0
		case '\n': // a terminal's output processing turns it into CR LF
			row, col = row+1, 0
			grow()
		case 0x1b:
			i = s092Escape(rs, i, func(final rune, param string) {
				n, err := strconv.Atoi(param)
				switch final {
				case 'A':
					if err != nil || n < 1 {
						n = 1
					}
					row = max(row-n, 0)
				case 'K':
					if param == "2" {
						rows[row] = nil
					} else if col < len(rows[row]) {
						rows[row] = rows[row][:col]
					}
				case 'J':
					if col < len(rows[row]) {
						rows[row] = rows[row][:col]
					}
					rows = rows[:row+1]
				}
			})
		default:
			if r < 0x20 {
				continue
			}
			for len(rows[row]) < col {
				rows[row] = append(rows[row], ' ')
			}
			if col < len(rows[row]) {
				rows[row][col] = r
			} else {
				rows[row] = append(rows[row], r)
			}
			col++
		}
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = strings.TrimRight(string(r), " ")
	}
	return out
}

// s092Escape consumes the escape sequence starting at rs[i] and returns the
// index of its last rune. A CSI sequence is handed to csi with its final rune
// and its parameter string; OSC strings and two-rune escapes are skipped.
func s092Escape(rs []rune, i int, csi func(final rune, param string)) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[':
		j := i + 2
		for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
			j++
		}
		if j < len(rs) {
			csi(rs[j], string(rs[i+2:j]))
		}
		return j
	case ']':
		j := i + 2
		for j < len(rs) && rs[j] != 0x07 && rs[j] != 0x1b {
			j++
		}
		if j+1 < len(rs) && rs[j] == 0x1b && rs[j+1] == '\\' {
			j++
		}
		return j
	default:
		return i + 1
	}
}

// s092RunApply runs runAutoupdate on a stdout the run takes for a terminal,
// with fds 1 and 2 captured in order. Stdin is a pipe: bubbletea registers its
// input with epoll, which refuses a regular file, and a program that cannot
// start draws nothing.
func s092RunApply(t *testing.T, auOpts *autoupdateOptions) (code int, out string) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	origStdin := os.Stdin
	os.Stdin = inR
	defer func() { os.Stdin = origStdin }()
	saved1, err1 := syscall.Dup(1)
	saved2, err2 := syscall.Dup(2)
	if err1 != nil || err2 != nil {
		t.Fatalf("dup: %v %v", err1, err2)
	}
	if err := syscall.Dup3(int(f.Fd()), 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Dup3(int(f.Fd()), 2, 0); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			_ = syscall.Dup3(saved1, 1, 0)
			_ = syscall.Dup3(saved2, 2, 0)
			_ = syscall.Close(saved1)
			_ = syscall.Close(saved2)
		}()
		auCmd := testAutoupdateCmd()
		auCmd.SetContext(logging.NewContext(context.Background(), slog.New(slog.NewTextHandler(os.Stderr, nil))))
		code = exitCodeFor(runAutoupdate(auCmd, nil, auOpts, ttyDeps(false, true)))
	}()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

func s092CountRowsWithPrefix(screen []string, prefix string) int {
	n := 0
	for _, r := range screen {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// R1.5: more failures than the default 24-row region holds, so the live frame
// folds the history. After the run every ✗ row is on screen exactly once —
// none lost to the fold or to bubbletea's exit erase, none left twice by a
// final frame that still carried it — below the header and above the summary.
func TestApplyHistoryFlushPrintsEveryRowOnceAfterRelease(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs := s092ApplyAllFixture(t, auOpts, 26)
	code, out := s092RunApply(t, auOpts)
	if code != 1 {
		t.Fatalf("a batch of failing bumps exited %d, want 1:\n%s", code, out)
	}
	screen := s092Screen(out)
	shown := strings.Join(screen, "\n")
	if s092CountRowsWithPrefix(screen, "Applying updates") != 1 {
		t.Fatalf("want the live region's header once on screen:\n%s", shown)
	}
	if strings.Contains(shown, "earlier package(s) finished") {
		t.Errorf("the fold line of the live frame survived the release:\n%s", shown)
	}
	header, summary := -1, -1
	for i, r := range screen {
		if strings.HasPrefix(r, "Applying updates") {
			header = i
		}
		if strings.Contains(r, "Repair:  bentoo overlay autoupdate --repair") && summary < 0 {
			summary = i
		}
	}
	for _, pkg := range pkgs {
		prefix := "✗ " + pkg + " — "
		if n := s092CountRowsWithPrefix(screen, prefix); n != 1 {
			t.Errorf("%s: %d ✗ rows on screen, want exactly 1:\n%s", pkg, n, shown)
			continue
		}
		for i, r := range screen {
			if !strings.HasPrefix(r, prefix) {
				continue
			}
			if i < header || (summary >= 0 && i > summary) {
				t.Errorf("%s: the ✗ row (row %d) is not between the header (row %d) and the summary (row %d)", pkg, i, header, summary)
			}
			if !strings.HasSuffix(r, applyRepairHint(&autoupdate.ApplyResult{Package: pkg, RepairBundle: "x"})) {
				t.Errorf("%s: the flushed ✗ row does not end with its repair hint: %q", pkg, r)
			}
		}
	}
}

// R2.2 wiring: through the live region, a failure that left a repair bundle
// carries the --repair hint on its ✗ row, once per package.
func TestApplyRepairHintReachesTheLiveFailureRow(t *testing.T) {
	auOpts := testAutoupdateOptions()
	pkgs := s092ApplyAllFixture(t, auOpts, 2)
	code, out := s092RunApply(t, auOpts)
	if code != 1 {
		t.Fatalf("a batch of failing bumps exited %d, want 1:\n%s", code, out)
	}
	shown := strings.Join(s092Screen(out), "\n")
	for _, pkg := range pkgs {
		hint := "(repair: bentoo overlay autoupdate --repair " + pkg + ")"
		if n := strings.Count(shown, hint); n != 1 {
			t.Errorf("%s: the repair hint is on screen %d times, want 1:\n%s", pkg, n, shown)
		}
	}
}

// R2.2: the hint names --repair only for a failure that left a bundle.
func TestApplyRepairHintOnlyForAFailureWithABundle(t *testing.T) {
	failed := errors.New("manifest failed")
	cases := []struct {
		name   string
		result *autoupdate.ApplyResult
		want   string
	}{
		{"failure with bundle", &autoupdate.ApplyResult{Package: "dev-go/alpha", Error: failed, StagedPath: "/s/a", RepairBundle: "/s/a/.bentoo-repair.json"},
			"(repair: bentoo overlay autoupdate --repair dev-go/alpha)"},
		{"failure without bundle", &autoupdate.ApplyResult{Package: "dev-go/beta", Error: failed, StagedPath: "/s/b"}, ""},
		{"success", &autoupdate.ApplyResult{Package: "dev-go/gamma", Success: true, RepairBundle: "/stale"}, ""},
		{"no result", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyRepairHint(tc.result); got != tc.want {
				t.Fatalf("applyRepairHint = %q, want %q", got, tc.want)
			}
		})
	}
}
