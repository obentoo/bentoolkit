package snapshot

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestStripEmergeHookBlock pins R6.4 with direct cases.
func TestStripEmergeHookBlock(t *testing.T) {
	block := emergeHookBashrcBlock
	cases := []struct{ name, in, want string }{
		{"absent", "export A=1\n", "export A=1\n"},
		{"absent, no trailing newline", "export A=1", "export A=1"},
		{"empty", "", ""},
		{"first", block + "export B=2\n", "export B=2\n"},
		{"first, user content without trailing newline", block + "export B=2", "export B=2"},
		{"middle", "export A=1\n" + block + "export B=2\n", "export A=1\nexport B=2\n"},
		{"last", "export A=1\n" + block, "export A=1\n"},
		{"only the block", block, ""},
		{"marker text inside a longer line is user content",
			"echo '" + emergeHookBlockBegin + "'\nexport A=1\n", "echo '" + emergeHookBlockBegin + "'\nexport A=1\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := stripEmergeHookBlock([]byte(tc.in))
			if err != nil {
				t.Fatalf("stripEmergeHookBlock: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("last, no trailing newline", func(t *testing.T) {
		got, err := stripEmergeHookBlock([]byte("export A=1\n" + strings.TrimSuffix(block, "\n")))
		if err != nil {
			t.Fatalf("stripEmergeHookBlock: %v", err)
		}
		if strings.TrimSuffix(string(got), "\n") != "export A=1" {
			t.Errorf("got %q, want the user line only", got)
		}
	})
}

// TestStripEmergeHookBlock_UnterminatedIsError pins the broken-block refusal:
// the error names the line and text of the UNTERMINATED begin marker.
func TestStripEmergeHookBlock_UnterminatedIsError(t *testing.T) {
	cases := []struct {
		name, in string
		line     int
	}{
		{"begin mid-file", "export A=1\n" + emergeHookBlockBegin + "\nexport B=2\n", 2},
		{"begin on the first line", emergeHookBlockBegin + "\nexport B=2\n", 1},
		{"begin on the last line, no newline", "export A=1\n" + emergeHookBlockBegin, 2},
		{"end marker only before the begin", emergeHookBlockEnd + "\nexport A=1\n" + emergeHookBlockBegin + "\nexport B=2\n", 3},
		{"good block, then a second unterminated begin", emergeHookBashrcBlock + "export A=1\n" + emergeHookBlockBegin + "\nexport B=2\n", 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stripEmergeHookBlock([]byte(tc.in))
			if !errors.Is(err, ErrBrokenHookBlock) {
				t.Fatalf("err = %v, want ErrBrokenHookBlock", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, fmt.Sprintf("line %d", tc.line)) && !strings.Contains(msg, fmt.Sprintf(":%d", tc.line)) {
				t.Errorf("err %q must name line %d", msg, tc.line)
			}
			if !strings.Contains(msg, emergeHookBlockBegin) {
				t.Errorf("err %q must quote the begin-marker text", msg)
			}
		})
	}
}
