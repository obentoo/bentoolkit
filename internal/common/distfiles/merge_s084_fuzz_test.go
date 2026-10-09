// Story 084, sub-task 1.2. FuzzMergeManifestDist states what MergeManifestDist
// guarantees for ANY pair of Manifest bodies (R1.3, R1.4), with the counts
// promotion logs held to the body they describe.
//
// Both inputs come from an overlay checkout: the published Manifest is whatever
// the repository holds, malformed or hostile, and the staged one is whatever
// pkgdev wrote. For every input:
//   - every output line is, byte for byte, a DIST line of one of the inputs
//     (R1.3) — Portage verifies digests against the archive, so a record that
//     was rebuilt, trimmed or invented is a build failure;
//   - every staged DIST line is written (the staged Manifest is the one the
//     gates validated);
//   - lines that carry a filename are in ascending byte order of filename (R1.4);
//   - the body is empty or ends in exactly one newline, with no empty line (R1.4);
//   - Written equals the staged DIST record count and Kept+Written equals the
//     output line count, so the logged counts describe the written file.
//
// The DIST-line grammar is restated here on purpose (split on "\n", fields by
// whitespace, first field "DIST") so the fuzz target is an outside check of the
// package's grammar rather than a call back into it.

package distfiles

import (
	"strings"
	"testing"
)

func s084FuzzDistLines(body []byte) []string {
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == "DIST" {
			out = append(out, line)
		}
	}
	return out
}

func FuzzMergeManifestDist(f *testing.F) {
	seeds := [][2]string{
		// The story's reproduction.
		{"DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb\n", "DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd\n"},
		// Shared filename, near-names, a filename inside a digest field.
		{"DIST b-2.0.tar.gz.sig 1\nDIST b-2.00.tar.gz 2\nDIST B-2.0.tar.gz 3\nDIST x 4 BLAKE2B b-2.0.tar.gz\nDIST b-2.0.tar.gz 5\n",
			"DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd\n"},
		// Layout variants, CRLF, indentation, trailing blanks.
		{"\tDIST  b-2.0.tar.gz\t100 BLAKE2B o SHA512 o\r\n   DIST c 3   \n", "DIST  b-2.0.tar.gz  200 BLAKE2B n SHA512 n \n"},
		// Duplicates on each side.
		{"DIST d 1 first\nDIST d 2 second\n  DIST\td 3 third\n", "DIST e 1\nDIST e 2\n"},
		// Ordering against version order and line order.
		{"   DIST b-9.tar.gz 9\nDIST\tz-1.tar.gz 1\nDIST b-1.0.1.tar.gz 3\n", "DIST b-10.tar.gz 10\nDIST B-1.tar.gz 1\nDIST b-1.0_p1.tar.gz 2\nDIST a.tar.gz 0\n"},
		// Refused and accepted dotted names, missing filename fields.
		{"DIST\nDIST   \nDIST . 1\nDIST .. 1\nDIST ../x 1\nDIST a/b 1\nDIST a\\b 1\nDIST ... 1\nDIST .foo 1\nDIST ..foo 1\n", "DIST\nDIST s 1\n"},
		// Non-DIST records and look-alikes, blank lines, no trailing newline.
		{"EBUILD b-1.0.ebuild 1\nAUX p 1\nMISC metadata.xml 1\nDISTX q 1\ndist r 1\n\n\nDIST k 1", "\n\nEBUILD b-2.0.ebuild 1\n\n"},
		{"", ""},
	}
	for _, s := range seeds {
		f.Add([]byte(s[0]), []byte(s[1]))
	}

	f.Fuzz(func(t *testing.T, published, staged []byte) {
		got, merge := MergeManifestDist(published, staged)

		if len(got) == 0 {
			if merge.Kept+merge.Written != 0 {
				t.Fatalf("empty body but ManifestMerge %+v reports records written", merge)
			}
			if n := len(s084FuzzDistLines(staged)); n != 0 {
				t.Fatalf("empty body although the staged Manifest declares %d DIST records", n)
			}
			return
		}
		body := string(got)
		if !strings.HasSuffix(body, "\n") || strings.HasSuffix(body, "\n\n") {
			t.Fatalf("merged body must end in exactly one newline: %q", body)
		}
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")

		inputs := map[string]bool{}
		for _, l := range s084FuzzDistLines(published) {
			inputs[l] = true
		}
		stagedLines := s084FuzzDistLines(staged)
		for _, l := range stagedLines {
			inputs[l] = true
		}

		written := map[string]int{}
		prev, havePrev := "", false
		for i, l := range lines {
			if l == "" {
				t.Fatalf("merged body has an empty line at %d: %q", i, body)
			}
			if !inputs[l] {
				t.Fatalf("output line %d %q is not a DIST line of either input (R1.3)\n  published %q\n  staged    %q", i, l, published, staged)
			}
			written[l]++
			fields := strings.Fields(l)
			if len(fields) < 2 {
				continue
			}
			if havePrev && fields[1] < prev {
				t.Fatalf("records are not in ascending byte order of filename: %q after %q (R1.4)\n  body %q", fields[1], prev, body)
			}
			prev, havePrev = fields[1], true
		}

		need := map[string]int{}
		for _, l := range stagedLines {
			need[l]++
		}
		for l, n := range need {
			if written[l] < n {
				t.Fatalf("staged DIST line %q occurs %d times in the staged Manifest but %d times in the output", l, n, written[l])
			}
		}
		if merge.Written != len(stagedLines) {
			t.Fatalf("Written = %d, but the staged Manifest declares %d DIST records", merge.Written, len(stagedLines))
		}
		if merge.Kept+merge.Written != len(lines) {
			t.Fatalf("Kept+Written = %d+%d, but the body holds %d records", merge.Kept, merge.Written, len(lines))
		}
	})
}
