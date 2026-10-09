// Story 084, sub-task 1.1. MergeManifestDist turns the published Manifest and
// the staged (validated) one into the body promotion writes (R1.1-R1.6).
//
// Contract, from the sub-task objective:
//
//	MergeManifestDist(published, staged []byte) ([]byte, ManifestMerge)
//	type ManifestMerge struct{ Kept, Written, Replaced, Dropped int }
//
// Kept counts published records written, Written counts staged records
// written, Replaced counts published records a staged record of the same
// filename displaced, Dropped counts published records refused for having no
// usable filename.
//
// HOSTILE CASES FIRST, both directions. The merge decides that two records are
// "the same file" by filename, so it can fire wrongly two ways: a near-name of
// a staged filename collapsing into it (a published archive silently lost), and
// the same filename spelled with other whitespace NOT collapsing (two records
// for one archive, which Portage refuses). Each requirement about sameness is
// asserted on its wrong-collapse and wrong-split fixtures before its benign one.

package distfiles

import (
	"bytes"
	"strings"
	"testing"
)

// s084Body joins Manifest lines the way a file holds them: one per line, with
// a trailing newline.
func s084Body(lines ...string) []byte {
	if len(lines) == 0 {
		return nil
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// s084AssertMerge runs the merge and compares the body byte for byte.
func s084AssertMerge(t *testing.T, published, staged, want []byte) ManifestMerge {
	t.Helper()
	got, merge := MergeManifestDist(published, staged)
	if !bytes.Equal(got, want) {
		t.Errorf("merged Manifest differs from the expected body; Portage verifies these records against the archives, "+
			"so a lost, rewritten or reordered record is a package that no longer installs or a diff nobody asked for:\n"+
			"  published %q\n  staged    %q\n  got       %q\n  want      %q", published, staged, got, want)
	}
	return merge
}

func s084AssertCounts(t *testing.T, got, want ManifestMerge) {
	t.Helper()
	if got != want {
		t.Errorf("ManifestMerge = %+v, want %+v; promotion logs these counts, so a wrong one tells the operator "+
			"the published records were kept or replaced when they were not", got, want)
	}
}

// R1.2, wrong-collapse half: names that share a prefix, a suffix, a case-folded
// spelling or a digest field with the staged filename are DIFFERENT archives
// and must all survive.
func TestS084MergeNearNamesOfAStagedFilenameAreNotReplaced(t *testing.T) {
	staged := s084Body("DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd")
	published := s084Body(
		"DIST b-2.0.tar.gz.sig 10 BLAKE2B s1 SHA512 s2",
		"DIST b-2.00.tar.gz 20 BLAKE2B z1 SHA512 z2",
		"DIST B-2.0.tar.gz 30 BLAKE2B u1 SHA512 u2",
		"DIST b-2.0.tar 40 BLAKE2B t1 SHA512 t2",
		// The staged filename appears here only as a field after the name.
		"DIST x-1.tar.gz 50 BLAKE2B b-2.0.tar.gz SHA512 x2",
	)
	want := s084Body(
		"DIST B-2.0.tar.gz 30 BLAKE2B u1 SHA512 u2",
		"DIST b-2.0.tar 40 BLAKE2B t1 SHA512 t2",
		"DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd",
		"DIST b-2.0.tar.gz.sig 10 BLAKE2B s1 SHA512 s2",
		"DIST b-2.00.tar.gz 20 BLAKE2B z1 SHA512 z2",
		"DIST x-1.tar.gz 50 BLAKE2B b-2.0.tar.gz SHA512 x2",
	)
	merge := s084AssertMerge(t, published, staged, want)
	s084AssertCounts(t, merge, ManifestMerge{Kept: 5, Written: 1, Replaced: 0, Dropped: 0})
}

// R1.2, wrong-split half: the same filename in a record laid out with tabs,
// indentation or extra spaces is still the same archive, and the staged record
// is the only one written for it — including when the published side holds it
// twice (a third element reaching the same name).
func TestS084MergeSameFilenameSpelledDifferentlyIsReplaced(t *testing.T) {
	stagedLine := "DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd"

	t.Run("indented and tab-separated published record", func(t *testing.T) {
		published := s084Body(
			"\tDIST  b-2.0.tar.gz\t100 BLAKE2B old1 SHA512 old2",
			"DIST a-1.tar.gz 1 BLAKE2B a1 SHA512 a2",
		)
		want := s084Body("DIST a-1.tar.gz 1 BLAKE2B a1 SHA512 a2", stagedLine)
		merge := s084AssertMerge(t, published, s084Body(stagedLine), want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 1, Written: 1, Replaced: 1, Dropped: 0})
	})

	t.Run("published declares the staged filename twice", func(t *testing.T) {
		published := s084Body(
			"DIST b-2.0.tar.gz 100 BLAKE2B first SHA512 first",
			"  DIST b-2.0.tar.gz   150 BLAKE2B second SHA512 second",
		)
		got, merge := MergeManifestDist(published, s084Body(stagedLine))
		if want := s084Body(stagedLine); !bytes.Equal(got, want) {
			t.Errorf("a staged filename the published Manifest declares twice must come out as the staged record alone:\n  got  %q\n  want %q", got, want)
		}
		if merge.Kept != 0 || merge.Written != 1 {
			t.Errorf("ManifestMerge = %+v, want Kept 0 and Written 1: neither published duplicate was written", merge)
		}
	})

	t.Run("same filename, plainly", func(t *testing.T) {
		published := s084Body("DIST b-2.0.tar.gz 200 BLAKE2B old SHA512 old")
		merge := s084AssertMerge(t, published, s084Body(stagedLine), s084Body(stagedLine))
		s084AssertCounts(t, merge, ManifestMerge{Kept: 0, Written: 1, Replaced: 1, Dropped: 0})
	})
}

// R1.1, with the story's reproduction as the expected bytes. Non-DIST records
// (EBUILD, AUX, MISC) and lines that only resemble a DIST record are written
// from neither side.
func TestS084MergeKeepsThePublishedRecordsAndWritesTheStagedOnes(t *testing.T) {
	published := s084Body(
		"AUX b-1.0-fix.patch 12 BLAKE2B p1 SHA512 p2",
		"DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb",
		"EBUILD b-1.0.ebuild 33 BLAKE2B e1 SHA512 e2",
		"MISC metadata.xml 44 BLAKE2B m1 SHA512 m2",
		"DISTX b-0.9.tar.gz 1 BLAKE2B q1 SHA512 q2",
		"dist b-0.8.tar.gz 1 BLAKE2B r1 SHA512 r2",
	)
	staged := s084Body(
		"EBUILD b-2.0.ebuild 34 BLAKE2B f1 SHA512 f2",
		"DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd",
	)
	want := []byte("DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb\nDIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd\n")
	merge := s084AssertMerge(t, published, staged, want)
	s084AssertCounts(t, merge, ManifestMerge{Kept: 1, Written: 1, Replaced: 0, Dropped: 0})
}

// R1.1 with one side carrying no DIST record at all.
func TestS084MergeWithAnEmptySide(t *testing.T) {
	t.Run("published has no DIST record", func(t *testing.T) {
		published := s084Body("EBUILD b-1.0.ebuild 33 BLAKE2B e1 SHA512 e2")
		staged := s084Body("DIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd")
		merge := s084AssertMerge(t, published, staged, staged)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 0, Written: 1})
	})
	t.Run("staged has no DIST record", func(t *testing.T) {
		published := s084Body("DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb")
		staged := s084Body("EBUILD b-2.0.ebuild 34 BLAKE2B f1 SHA512 f2")
		merge := s084AssertMerge(t, published, staged, published)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 1, Written: 0})
	})
	t.Run("neither side has a DIST record", func(t *testing.T) {
		got, merge := MergeManifestDist(s084Body("MISC metadata.xml 1"), nil)
		if len(got) != 0 {
			t.Errorf("two Manifests declaring no archive merged into %q, want an empty body", got)
		}
		s084AssertCounts(t, merge, ManifestMerge{})
	})
}

// R1.5. Wrong-collapse first (d.tar.gz and d.tar.gz.asc are two archives),
// then wrong-split (a duplicate laid out differently is still a duplicate),
// then the plain duplicate.
func TestS084MergeFirstOfTwoPublishedDuplicatesIsKept(t *testing.T) {
	staged := s084Body("DIST z-9.tar.gz 9 BLAKE2B z1 SHA512 z2")

	t.Run("a near-name is not a duplicate", func(t *testing.T) {
		published := s084Body(
			"DIST d.tar.gz 1 BLAKE2B d1 SHA512 d2",
			"DIST d.tar.gz.asc 2 BLAKE2B s1 SHA512 s2",
		)
		want := s084Body(
			"DIST d.tar.gz 1 BLAKE2B d1 SHA512 d2",
			"DIST d.tar.gz.asc 2 BLAKE2B s1 SHA512 s2",
			"DIST z-9.tar.gz 9 BLAKE2B z1 SHA512 z2",
		)
		merge := s084AssertMerge(t, published, staged, want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 2, Written: 1})
	})

	t.Run("a duplicate laid out differently is still a duplicate", func(t *testing.T) {
		published := s084Body(
			"DIST d.tar.gz 1 BLAKE2B first SHA512 first",
			"DIST e.tar.gz 5 BLAKE2B e1 SHA512 e2",
			"  DIST\td.tar.gz  9 BLAKE2B second SHA512 second",
		)
		want := s084Body(
			"DIST d.tar.gz 1 BLAKE2B first SHA512 first",
			"DIST e.tar.gz 5 BLAKE2B e1 SHA512 e2",
			"DIST z-9.tar.gz 9 BLAKE2B z1 SHA512 z2",
		)
		merge := s084AssertMerge(t, published, staged, want)
		if merge.Kept != 2 || merge.Written != 1 {
			t.Errorf("ManifestMerge = %+v, want Kept 2 (the first d.tar.gz and e.tar.gz) and Written 1", merge)
		}
	})

	t.Run("plain duplicate", func(t *testing.T) {
		published := s084Body(
			"DIST d.tar.gz 1 BLAKE2B first SHA512 first",
			"DIST d.tar.gz 2 BLAKE2B second SHA512 second",
		)
		want := s084Body(
			"DIST d.tar.gz 1 BLAKE2B first SHA512 first",
			"DIST z-9.tar.gz 9 BLAKE2B z1 SHA512 z2",
		)
		merge := s084AssertMerge(t, published, staged, want)
		if merge.Kept != 1 || merge.Written != 1 {
			t.Errorf("ManifestMerge = %+v, want Kept 1 and Written 1", merge)
		}
	})
}

// R1.3. Every written record is the line its source held: tabs, runs of
// spaces, indentation, trailing blanks, digest case and a CR from a CRLF file
// all survive.
func TestS084MergeCopiesEachRecordLineByteForByte(t *testing.T) {
	t.Run("whitespace and digest case", func(t *testing.T) {
		published := s084Body(
			"DIST\ta-1.tar.gz\t100\tBLAKE2B\tAbC\tSHA512\tdEf",
			"   DIST   c-1.tar.gz   300   BLAKE2B  x  SHA512  y   ",
		)
		staged := s084Body(
			"DIST  b-1.tar.gz  200 BLAKE2B B SHA512 b ",
			"\tDIST d-1.tar.gz 400 BLAKE2B d SHA512 d",
		)
		want := s084Body(
			"DIST\ta-1.tar.gz\t100\tBLAKE2B\tAbC\tSHA512\tdEf",
			"DIST  b-1.tar.gz  200 BLAKE2B B SHA512 b ",
			"   DIST   c-1.tar.gz   300   BLAKE2B  x  SHA512  y   ",
			"\tDIST d-1.tar.gz 400 BLAKE2B d SHA512 d",
		)
		merge := s084AssertMerge(t, published, staged, want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 2, Written: 2})
	})

	t.Run("CRLF published Manifest", func(t *testing.T) {
		published := []byte("DIST e-1.tar.gz 500 BLAKE2B e SHA512 e\r\n")
		staged := s084Body("DIST f-1.tar.gz 600 BLAKE2B f SHA512 f")
		want := []byte("DIST e-1.tar.gz 500 BLAKE2B e SHA512 e\r\nDIST f-1.tar.gz 600 BLAKE2B f SHA512 f\n")
		s084AssertMerge(t, published, staged, want)
	})
}

// R1.4. Ordered by FILENAME in byte order: not by line (an indented or
// tab-separated record would sort first by its line), not by source, and not
// by version (b-10 sorts before b-9, B before a).
func TestS084MergeOrdersRecordsByFilenameInByteOrder(t *testing.T) {
	t.Run("byte order of filenames", func(t *testing.T) {
		published := s084Body(
			"   DIST b-9.tar.gz 9 BLAKE2B n9 SHA512 n9",
			"DIST\tz-1.tar.gz 1 BLAKE2B z1 SHA512 z1",
			"DIST b-1.0.1.tar.gz 3 BLAKE2B n3 SHA512 n3",
		)
		staged := s084Body(
			"DIST b-10.tar.gz 10 BLAKE2B n10 SHA512 n10",
			"DIST B-1.tar.gz 1 BLAKE2B up SHA512 up",
			"DIST b-1.0_p1.tar.gz 2 BLAKE2B n2 SHA512 n2",
			"DIST a.tar.gz 0 BLAKE2B a0 SHA512 a0",
		)
		want := s084Body(
			"DIST B-1.tar.gz 1 BLAKE2B up SHA512 up",
			"DIST a.tar.gz 0 BLAKE2B a0 SHA512 a0",
			"DIST b-1.0.1.tar.gz 3 BLAKE2B n3 SHA512 n3",
			"DIST b-1.0_p1.tar.gz 2 BLAKE2B n2 SHA512 n2",
			"DIST b-10.tar.gz 10 BLAKE2B n10 SHA512 n10",
			"   DIST b-9.tar.gz 9 BLAKE2B n9 SHA512 n9",
			"DIST\tz-1.tar.gz 1 BLAKE2B z1 SHA512 z1",
		)
		merge := s084AssertMerge(t, published, staged, want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 3, Written: 4})
	})

	t.Run("exactly one trailing newline and no blank line", func(t *testing.T) {
		published := []byte("DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb")
		staged := []byte("\n\nDIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd\n\n\nMISC metadata.xml 1\n\n")
		want := []byte("DIST b-1.0.tar.gz 100 BLAKE2B aa SHA512 bb\nDIST b-2.0.tar.gz 200 BLAKE2B cc SHA512 dd\n")
		s084AssertMerge(t, published, staged, want)
	})
}

// R1.6. Wrong-fire first: names that merely start or end with dots are
// ordinary files and must be kept. Then each refused shape, alone and together.
func TestS084MergeDropsPublishedRecordsWithNoUsableFilename(t *testing.T) {
	staged := s084Body("DIST s-1.tar.gz 1 BLAKE2B s1 SHA512 s2")

	t.Run("dotted names that are ordinary files are kept", func(t *testing.T) {
		published := s084Body(
			"DIST ... 1 BLAKE2B a SHA512 a",
			"DIST .foo 2 BLAKE2B b SHA512 b",
			"DIST ..foo 3 BLAKE2B c SHA512 c",
			"DIST foo..tar.gz 4 BLAKE2B d SHA512 d",
			"DIST foo. 5 BLAKE2B e SHA512 e",
		)
		want := s084Body(
			"DIST ... 1 BLAKE2B a SHA512 a",
			"DIST ..foo 3 BLAKE2B c SHA512 c",
			"DIST .foo 2 BLAKE2B b SHA512 b",
			"DIST foo. 5 BLAKE2B e SHA512 e",
			"DIST foo..tar.gz 4 BLAKE2B d SHA512 d",
			"DIST s-1.tar.gz 1 BLAKE2B s1 SHA512 s2",
		)
		merge := s084AssertMerge(t, published, staged, want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 5, Written: 1, Dropped: 0})
	})

	valid := "DIST k-1.tar.gz 7 BLAKE2B k1 SHA512 k2"
	refused := []string{
		"DIST",
		"DIST   \t ",
		"DIST . 1 BLAKE2B x SHA512 x",
		"DIST .. 1 BLAKE2B x SHA512 x",
		"DIST ../etc/passwd 1 BLAKE2B x SHA512 x",
		"DIST a/b.tar.gz 1 BLAKE2B x SHA512 x",
		"DIST a\\b.tar.gz 1 BLAKE2B x SHA512 x",
		"DIST /abs.tar.gz 1 BLAKE2B x SHA512 x",
	}
	want := s084Body(valid, "DIST s-1.tar.gz 1 BLAKE2B s1 SHA512 s2")
	for _, bad := range refused {
		t.Run("refused "+bad, func(t *testing.T) {
			merge := s084AssertMerge(t, s084Body(bad, valid), staged, want)
			s084AssertCounts(t, merge, ManifestMerge{Kept: 1, Written: 1, Dropped: 1})
		})
	}
	t.Run("all refused shapes together", func(t *testing.T) {
		published := s084Body(append(append([]string{}, refused...), valid)...)
		merge := s084AssertMerge(t, published, staged, want)
		s084AssertCounts(t, merge, ManifestMerge{Kept: 1, Written: 1, Dropped: len(refused)})
	})
}

// The merge performs no I/O and returns a new body: neither input changes,
// not even in spare capacity an append could reach.
func TestS084MergeLeavesItsInputsUntouched(t *testing.T) {
	withSpare := func(body string) []byte {
		b := make([]byte, len(body), len(body)+64)
		copy(b, body)
		full := b[:cap(b)]
		for i := len(body); i < len(full); i++ {
			full[i] = '#'
		}
		return b
	}
	published := withSpare("DIST z-1.tar.gz 1 BLAKE2B z SHA512 z\nDIST a-1.tar.gz 1 BLAKE2B a SHA512 a\n")
	staged := withSpare("DIST m-1.tar.gz 1 BLAKE2B m SHA512 m\n")
	pubBefore := append([]byte(nil), published[:cap(published)]...)
	stgBefore := append([]byte(nil), staged[:cap(staged)]...)

	got, _ := MergeManifestDist(published, staged)

	if !bytes.Equal(published[:cap(published)], pubBefore) {
		t.Errorf("MergeManifestDist wrote into the published body: promotion keeps those bytes to restore the Manifest on rollback")
	}
	if !bytes.Equal(staged[:cap(staged)], stgBefore) {
		t.Errorf("MergeManifestDist wrote into the staged body")
	}
	want := "DIST a-1.tar.gz 1 BLAKE2B a SHA512 a\nDIST m-1.tar.gz 1 BLAKE2B m SHA512 m\nDIST z-1.tar.gz 1 BLAKE2B z SHA512 z\n"
	if string(got) != want {
		t.Errorf("merged body = %q, want %q", got, want)
	}
}
