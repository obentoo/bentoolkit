package main

// Authored for story 092, sub-task 1.3: the run history is printed to the
// scrollback once, after the live region is released (R1.5), and a failure
// that left a repair bundle names --repair on its ✗ row (R2.2).

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

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

func s092CountRowsWithPrefix(screen []string, prefix string) int {
	n := 0
	for _, r := range screen {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
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
