package secrets

// Authored for story 062, sub-task 1.1 (R4.1, R4.3, R4.4).
//
// Contract, from the sub-task objective: `func Resolved() []string` returns a
// snapshot of every non-empty value Lookup has returned in this process — each
// once, longest first, as a copy — and is safe to call while Lookup runs on
// other goroutines.
//
// The registry is process-wide and this package offers no way to reset it, so
// every test here uses values made unique to the run (a random suffix) and
// asserts membership and relative order, never the whole slice. All values are
// fake; none is a real credential.
//
// Every test isolates the file chain with withPaths (HOME to a t.TempDir(), the
// user and system files to paths the test controls), so no test reads the
// developer's real secrets files.

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// s062Unique returns a fresh fake value with the given prefix. The random part
// keeps two tests — or two runs with -count>1 — from observing each other's
// entries in the process-wide registry.
func s062Unique(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("reading random bytes: %v", err)
	}
	return prefix + hex.EncodeToString(b)
}

// s062Isolate points the file chain at two files that do not exist under a
// fresh temp dir, and isolates XDG_CONFIG_HOME, so a Lookup can only hit the
// environment unless the test writes one of the returned files.
func s062Isolate(t *testing.T) (userPath, sysPath string) {
	t.Helper()
	dir := t.TempDir()
	userPath = filepath.Join(dir, "user", "secrets")
	sysPath = filepath.Join(dir, "system", "secrets")
	withPaths(t, userPath, sysPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	return userPath, sysPath
}

// s062MustLookup resolves name and fails the test unless it was found with want.
func s062MustLookup(t *testing.T, name, want string) {
	t.Helper()
	got, found, err := Lookup(name)
	if err != nil || !found || got != want {
		t.Fatalf("Lookup(%q) = (%q, %v, %v), want (%q, true, nil) — the fixture did not resolve", name, got, found, err, want)
	}
}

func s062Count(values []string, v string) int {
	n := 0
	for _, x := range values {
		if x == v {
			n++
		}
	}
	return n
}

func s062Index(values []string, v string) int {
	for i, x := range values {
		if x == v {
			return i
		}
	}
	return -1
}

// TestResolvedRecordsEveryValueLookupReturned: a value resolved from each link
// of the chain — environment, user file, system file — is in the snapshot, and
// it is the value Lookup RETURNED (trimmed), not the raw text it was read from.
func TestResolvedRecordsEveryValueLookupReturned(t *testing.T) {
	userPath, sysPath := s062Isolate(t)

	envName := s062Unique(t, "S062_FAKE_ENV_")
	envValue := s062Unique(t, "s062-fake-env-")
	t.Setenv(envName, "  "+envValue+"\t")
	s062MustLookup(t, envName, envValue)

	userName := s062Unique(t, "S062_FAKE_USER_")
	userValue := s062Unique(t, "s062-fake-user-")
	t.Setenv(userName, "")
	writeFile(t, userPath, userName+"="+userValue+"\n", 0o600)
	s062MustLookup(t, userName, userValue)

	sysName := s062Unique(t, "S062_FAKE_SYS_")
	sysValue := s062Unique(t, "s062-fake-sys-")
	t.Setenv(sysName, "")
	writeFile(t, sysPath, sysName+"=\""+sysValue+"\"\n", 0o600)
	s062MustLookup(t, sysName, sysValue)

	got := Resolved()
	for source, v := range map[string]string{"environment": envValue, "user file": userValue, "system file": sysValue} {
		if s062Count(got, v) != 1 {
			t.Errorf("the value Lookup returned from the %s (%q) is in Resolved() %d times, want 1", source, v, s062Count(got, v))
		}
	}
	for _, x := range got {
		if strings.Contains(x, envValue) && x != envValue {
			t.Errorf("Resolved() holds %q, the untrimmed text the environment carried, rather than the %q Lookup returned", x, envValue)
		}
		if strings.Contains(x, sysValue) && x != sysValue {
			t.Errorf("Resolved() holds %q, the quoted text the file carried, rather than the %q Lookup returned", x, sysValue)
		}
	}
}

// TestResolvedRecordsNothingForAMissingSecret: every Lookup that returns no
// value — unset, whitespace-only, blank in a file, or an unreadable user file —
// leaves the registry exactly as it was. In particular no empty string is ever
// recorded: an empty entry would make a redactor that replaces it put `***`
// between every character of every line.
func TestResolvedRecordsNothingForAMissingSecret(t *testing.T) {
	userPath, _ := s062Isolate(t)
	before := Resolved()

	unset := s062Unique(t, "S062_FAKE_UNSET_")
	t.Setenv(unset, "")
	if _, found, err := Lookup(unset); found || err != nil {
		t.Fatalf("Lookup of an unset name = (found %v, err %v), want (false, nil)", found, err)
	}

	blankEnv := s062Unique(t, "S062_FAKE_BLANK_")
	t.Setenv(blankEnv, "   \t ")
	if _, found, _ := Lookup(blankEnv); found {
		t.Fatalf("Lookup of a whitespace-only environment value reported found")
	}

	blankFile := s062Unique(t, "S062_FAKE_BLANKFILE_")
	t.Setenv(blankFile, "")
	writeFile(t, userPath, blankFile+"=\n", 0o600)
	if _, found, _ := Lookup(blankFile); found {
		t.Fatalf("Lookup of a blank file value reported found")
	}

	// An unreadable user file: a directory where the file should be.
	if err := os.Remove(userPath); err != nil {
		t.Fatalf("removing %s: %v", userPath, err)
	}
	if err := os.MkdirAll(userPath, 0o700); err != nil {
		t.Fatalf("creating %s: %v", userPath, err)
	}
	unreadable := s062Unique(t, "S062_FAKE_UNREADABLE_")
	t.Setenv(unreadable, "")
	if _, found, err := Lookup(unreadable); found || err == nil {
		t.Fatalf("Lookup against an unreadable user file = (found %v, err %v), want (false, an error)", found, err)
	}

	after := Resolved()
	if len(after) != len(before) {
		t.Errorf("Resolved() grew from %d to %d entries across lookups that returned nothing: %q", len(before), len(after), after)
	}
	for _, v := range after {
		if v == "" || strings.TrimSpace(v) == "" {
			t.Errorf("Resolved() holds a blank entry %q", v)
		}
	}
}

// TestResolvedHoldsEachValueOnce: identity is by value. The two hostile halves
// come first — values that are nearly the same must stay two entries, and one
// value reached through two names must stay one — then the benign repeat.
func TestResolvedHoldsEachValueOnce(t *testing.T) {
	s062Isolate(t)

	// 1. Must NOT collapse: a case variant and an extension of a value are
	// different secrets, and each must be recorded (and later redacted) itself.
	base := s062Unique(t, "s062-fake-case-")
	upper := strings.ToUpper(base)
	longer := base + "-x"
	for _, v := range []string{base, upper, longer} {
		name := s062Unique(t, "S062_FAKE_NEAR_")
		t.Setenv(name, v)
		s062MustLookup(t, name, v)
	}

	// 2. Must NOT split: the same value resolved under two different names is
	// one secret.
	shared := s062Unique(t, "s062-fake-shared-")
	nameA := s062Unique(t, "S062_FAKE_SHARED_A_")
	nameB := s062Unique(t, "S062_FAKE_SHARED_B_")
	t.Setenv(nameA, shared)
	t.Setenv(nameB, " "+shared+" ")
	s062MustLookup(t, nameA, shared)
	s062MustLookup(t, nameB, shared)

	// 3. Benign: the same name looked up three times.
	repeated := s062Unique(t, "s062-fake-repeat-")
	nameR := s062Unique(t, "S062_FAKE_REPEAT_")
	t.Setenv(nameR, repeated)
	for range 3 {
		s062MustLookup(t, nameR, repeated)
	}

	got := Resolved()
	for _, v := range []string{base, upper, longer} {
		if n := s062Count(got, v); n != 1 {
			t.Errorf("near-identical value %q is in Resolved() %d times, want exactly 1 — distinct values must stay distinct entries", v, n)
		}
	}
	if n := s062Count(got, shared); n != 1 {
		t.Errorf("a value resolved under two names is in Resolved() %d times, want 1", n)
	}
	if n := s062Count(got, repeated); n != 1 {
		t.Errorf("a value resolved three times is in Resolved() %d times, want 1", n)
	}
}

// TestResolvedOrdersALongerSecretBeforeTheShorterOneItContains: the shorter
// value is resolved FIRST, so an append-only registry would list it first; a
// redactor walking that order would replace the shorter value inside the longer
// one and leave the rest of the longer one in the line (R4.3).
func TestResolvedOrdersALongerSecretBeforeTheShorterOneItContains(t *testing.T) {
	s062Isolate(t)

	short := s062Unique(t, "s062-fake-")
	long := "prefix-" + short + "-and-a-longer-tail"
	unrelated := s062Unique(t, "s062-fake-unrelated-value-that-is-longest-")

	for _, v := range []string{short, long, unrelated} {
		name := s062Unique(t, "S062_FAKE_ORDER_")
		t.Setenv(name, v)
		s062MustLookup(t, name, v)
	}

	got := Resolved()
	iShort, iLong := s062Index(got, short), s062Index(got, long)
	if iShort < 0 || iLong < 0 {
		t.Fatalf("Resolved() lacks the fixture values (short at %d, long at %d): %q", iShort, iLong, got)
	}
	if iLong > iShort {
		t.Errorf("Resolved() lists %q (index %d) before %q (index %d), which contains it — the longer value must come first", short, iShort, long, iLong)
	}
	for i := 1; i < len(got); i++ {
		if len(got[i]) > len(got[i-1]) {
			t.Errorf("Resolved() is not longest first: %q (len %d) at index %d follows %q (len %d)", got[i], len(got[i]), i, got[i-1], len(got[i-1]))
			break
		}
	}
}

// TestResolvedReturnsACopy: a caller that edits, truncates or appends to the
// slice it got must not change what the next caller sees.
func TestResolvedReturnsACopy(t *testing.T) {
	s062Isolate(t)

	v := s062Unique(t, "s062-fake-copy-")
	name := s062Unique(t, "S062_FAKE_COPY_")
	t.Setenv(name, v)
	s062MustLookup(t, name, v)

	first := Resolved()
	i := s062Index(first, v)
	if i < 0 {
		t.Fatalf("Resolved() lacks %q", v)
	}
	first[i] = "s062-mutated-by-caller"
	_ = append(first[:i], "s062-appended-by-caller")

	second := Resolved()
	if s062Count(second, v) != 1 {
		t.Errorf("after a caller edited its slice, Resolved() holds %q %d times, want 1: the caller wrote into the registry", v, s062Count(second, v))
	}
	for _, x := range second {
		if strings.HasPrefix(x, "s062-mutated-by-caller") || strings.HasPrefix(x, "s062-appended-by-caller") {
			t.Errorf("Resolved() returned %q, a value only a caller's slice ever held", x)
		}
	}
}

// TestResolvedIsSafeAlongsideConcurrentLookups: Lookup on many goroutines —
// several of them resolving the SAME name — while others read the snapshot.
// Run under -race; afterwards every value is present exactly once.
func TestResolvedIsSafeAlongsideConcurrentLookups(t *testing.T) {
	s062Isolate(t)

	const distinct = 16
	names := make([]string, distinct)
	values := make([]string, distinct)
	for i := range distinct {
		names[i] = s062Unique(t, "S062_FAKE_CONC_")
		values[i] = s062Unique(t, "s062-fake-conc-")
		t.Setenv(names[i], values[i])
	}
	sameName := s062Unique(t, "S062_FAKE_SAME_")
	sameValue := s062Unique(t, "s062-fake-same-")
	t.Setenv(sameName, sameValue)

	var wg sync.WaitGroup
	for i := range distinct {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = Lookup(names[i])
		}(i)
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = Lookup(sameName)
		}()
	}
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				snapshot := Resolved()
				for j := range snapshot {
					_ = len(snapshot[j])
				}
			}
		}()
	}
	wg.Wait()

	got := Resolved()
	for _, v := range values {
		if n := s062Count(got, v); n != 1 {
			t.Errorf("value %q resolved concurrently is in Resolved() %d times, want 1", v, n)
		}
	}
	if n := s062Count(got, sameValue); n != 1 {
		t.Errorf("one name resolved on 8 goroutines at once is in Resolved() %d times, want 1", n)
	}
}
