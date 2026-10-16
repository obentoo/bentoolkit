package sysmon_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// hostMeminfo is the /proc/meminfo shape of a Linux 6.x host, trimmed. The
// numbers are distinct on purpose so a value read from the wrong line shows.
const hostMeminfo = `MemTotal:       16303016 kB
MemFree:          812344 kB
MemAvailable:    9876543 kB
Buffers:          123456 kB
Cached:          7654321 kB
SwapCached:        11111 kB
Active:          5555555 kB
SwapTotal:       8388604 kB
SwapFree:        7340028 kB
HugePages_Total:       0
Hugepagesize:       2048 kB
`

const kib = 1024

// TestParseMeminfo_KibToBytes is R3.1/R3.2: the values the kernel labels "kB"
// are KiB and come back as bytes.
func TestParseMeminfo_KibToBytes(t *testing.T) {
	m, err := sysmon.ParseMeminfo([]byte(hostMeminfo))
	if err != nil {
		t.Fatalf("ParseMeminfo(host format) error = %v", err)
	}
	want := sysmon.Memory{
		Total:     16303016 * kib,
		Available: 9876543 * kib,
		SwapTotal: 8388604 * kib,
		SwapFree:  7340028 * kib,
	}
	if m != want {
		t.Errorf("ParseMeminfo = %+v, want %+v", m, want)
	}
}

// TestParseMeminfo_LookAlikeKeysNeverRead is the hostile half of reading by
// key: SwapCached shares a prefix with SwapTotal/SwapFree, MemFree is the
// figure MemAvailable must never collapse into, and a key that merely starts
// with a wanted key is a different key. Each decoy carries a value the real
// line does not, and sits both before and after the real lines so neither a
// first-wins nor a last-wins scan hides a prefix match.
func TestParseMeminfo_LookAlikeKeysNeverRead(t *testing.T) {
	in := `MemTotalHuge:     999999 kB
MemAvailableX:      888 kB
SwapCachedTotal:    555 kB
SwapFreeish:        666 kB
MemTotal:          1000 kB
MemFree:            900 kB
MemAvailable:       100 kB
SwapCached:         777 kB
SwapTotal:           50 kB
SwapFree:            20 kB
MemTotalHuge:     999998 kB
MemAvailableX:      887 kB
SwapTotalX:         444 kB
SwapFreeish:        665 kB
`
	m, err := sysmon.ParseMeminfo([]byte(in))
	if err != nil {
		t.Fatalf("ParseMeminfo error = %v", err)
	}
	want := sysmon.Memory{Total: 1000 * kib, Available: 100 * kib, SwapTotal: 50 * kib, SwapFree: 20 * kib}
	if m != want {
		t.Errorf("ParseMeminfo = %+v, want %+v (a look-alike key was read)", m, want)
	}
}

// TestParseMeminfo_KeyOrderIrrelevant is the converse: the same figures in a
// different line order are the same reading.
func TestParseMeminfo_KeyOrderIrrelevant(t *testing.T) {
	in := "SwapFree: 20 kB\nMemAvailable: 100 kB\nSwapTotal: 50 kB\nMemTotal: 1000 kB\n"
	m, err := sysmon.ParseMeminfo([]byte(in))
	if err != nil {
		t.Fatalf("ParseMeminfo error = %v", err)
	}
	want := sysmon.Memory{Total: 1000 * kib, Available: 100 * kib, SwapTotal: 50 * kib, SwapFree: 20 * kib}
	if m != want {
		t.Errorf("ParseMeminfo = %+v, want %+v", m, want)
	}
}

// TestParseMeminfo_NoSwapLinesMeansZero is R3.2's "swap absent": a host
// without swap lines parses, with zero swap.
func TestParseMeminfo_NoSwapLinesMeansZero(t *testing.T) {
	m, err := sysmon.ParseMeminfo([]byte("MemTotal: 2048 kB\nMemFree: 10 kB\nMemAvailable: 1024 kB\n"))
	if err != nil {
		t.Fatalf("ParseMeminfo without swap lines error = %v", err)
	}
	if m.SwapTotal != 0 || m.SwapFree != 0 {
		t.Errorf("swap = %d/%d, want 0/0 when the lines are absent", m.SwapFree, m.SwapTotal)
	}
	if m.Total != 2048*kib || m.Available != 1024*kib {
		t.Errorf("ParseMeminfo = %+v, want Total 2048 KiB, Available 1024 KiB", m)
	}
}

// TestParseMeminfo_MissingRequiredKeyIsError: MemAvailable has existed since
// Linux 3.14, so its absence is a parse error, never a silent fallback to
// MemFree; the error names the missing key.
func TestParseMeminfo_MissingRequiredKeyIsError(t *testing.T) {
	cases := []struct {
		name, in, key string
	}{
		{"no MemAvailable, MemFree present", "MemTotal: 1000 kB\nMemFree: 900 kB\nSwapTotal: 0 kB\n", "MemAvailable"},
		{"no MemTotal", "MemFree: 900 kB\nMemAvailable: 100 kB\n", "MemTotal"},
		{"empty", "", "MemTotal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := sysmon.ParseMeminfo([]byte(tc.in))
			if err == nil {
				t.Fatalf("ParseMeminfo(%q) = %+v, nil; want an error", tc.in, m)
			}
			if tc.name != "empty" && !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name the missing key %s", err, tc.key)
			}
		})
	}
}

// TestParseMeminfo_MalformedValueIsError: a wanted key whose value is not an
// unsigned integer is an error, not a zero reading (a zero MemAvailable would
// read as "memory exhausted").
func TestParseMeminfo_MalformedValueIsError(t *testing.T) {
	cases := map[string]string{
		"word":     "MemTotal: 1000 kB\nMemAvailable: lots kB\n",
		"negative": "MemTotal: 1000 kB\nMemAvailable: -5 kB\n",
		"overflow": "MemTotal: 99999999999999999999999 kB\nMemAvailable: 5 kB\n",
		"no value": "MemTotal: 1000 kB\nMemAvailable:\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if m, err := sysmon.ParseMeminfo([]byte(in)); err == nil {
				t.Errorf("ParseMeminfo(%q) = %+v, nil; want an error", in, m)
			}
		})
	}
}

const (
	hostPSIMemory = "some avg10=1.50 avg60=12.25 avg300=3.00 total=123456\nfull avg10=0.50 avg60=10.75 avg300=1.00 total=65432\n"
	hostPSICPU    = "some avg10=90.00 avg60=81.50 avg300=40.00 total=999999\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"
)

// TestParsePressure_HostFormats is R3.3/R4.1: some avg60 and full avg60 come
// from their own lines.
func TestParsePressure_HostFormats(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want sysmon.Pressure
	}{
		{"memory", hostPSIMemory, sysmon.Pressure{SomeAvg60: 12.25, FullAvg60: 10.75, HasFull: true}},
		{"cpu", hostPSICPU, sysmon.Pressure{SomeAvg60: 81.50, FullAvg60: 0, HasFull: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := sysmon.ParsePressure([]byte(tc.in))
			if err != nil {
				t.Fatalf("ParsePressure error = %v", err)
			}
			if p != tc.want {
				t.Errorf("ParsePressure = %+v, want %+v", p, tc.want)
			}
		})
	}
}

// TestParsePressure_FullLineFirstDoesNotCollapseIntoSome is the hostile half:
// the some and full lines are told apart by their first word, not by order.
func TestParsePressure_FullLineFirstDoesNotCollapseIntoSome(t *testing.T) {
	in := "full avg10=0.50 avg60=10.75 avg300=1.00 total=65432\nsome avg10=1.50 avg60=12.25 avg300=3.00 total=123456\n"
	p, err := sysmon.ParsePressure([]byte(in))
	if err != nil {
		t.Fatalf("ParsePressure error = %v", err)
	}
	want := sysmon.Pressure{SomeAvg60: 12.25, FullAvg60: 10.75, HasFull: true}
	if p != want {
		t.Errorf("ParsePressure = %+v, want %+v (some and full crossed)", p, want)
	}
}

// TestParsePressure_FieldsReadByKeyNotPosition: avg60 is found by its key, so
// a reordered line still yields avg60, never avg10 or avg300.
func TestParsePressure_FieldsReadByKeyNotPosition(t *testing.T) {
	in := "some total=5 avg300=3.00 avg10=1.00 avg60=2.00\nfull avg60=4.00 avg10=7.00 avg300=8.00 total=9\n"
	p, err := sysmon.ParsePressure([]byte(in))
	if err != nil {
		t.Fatalf("ParsePressure error = %v", err)
	}
	want := sysmon.Pressure{SomeAvg60: 2.00, FullAvg60: 4.00, HasFull: true}
	if p != want {
		t.Errorf("ParsePressure = %+v, want %+v (a field was read by position)", p, want)
	}
}

// TestParsePressure_MissingFullLine: older kernels expose no cpu full line;
// that parses, with HasFull false.
func TestParsePressure_MissingFullLine(t *testing.T) {
	p, err := sysmon.ParsePressure([]byte("some avg10=0.00 avg60=42.00 avg300=0.00 total=0\n"))
	if err != nil {
		t.Fatalf("ParsePressure without a full line error = %v", err)
	}
	want := sysmon.Pressure{SomeAvg60: 42.00}
	if p != want {
		t.Errorf("ParsePressure = %+v, want %+v", p, want)
	}
}

// TestParsePressure_ExplicitZeroFullIsStillPresent is the converse of the
// missing-full case: a full line of zeros is a present line (HasFull true),
// not an absent one.
func TestParsePressure_ExplicitZeroFullIsStillPresent(t *testing.T) {
	p, err := sysmon.ParsePressure([]byte(hostPSICPU))
	if err != nil {
		t.Fatalf("ParsePressure error = %v", err)
	}
	if !p.HasFull {
		t.Error("HasFull = false for a present all-zero full line; zero collapsed into absent")
	}
}

// TestParsePressure_MalformedIsError: no some line, or an unparsable avg60, is
// an error rather than a zero reading.
func TestParsePressure_MalformedIsError(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"only full":          "full avg10=0.00 avg60=1.00 avg300=0.00 total=0\n",
		"some without avg60": "some avg10=1.00 avg300=1.00 total=1\n",
		"some avg60 word":    "some avg10=1.00 avg60=high avg300=1.00 total=1\n",
		"full avg60 word":    "some avg10=1.00 avg60=1.00 avg300=1.00 total=1\nfull avg10=0 avg60=x avg300=0 total=0\n",
		"garbage":            "not a pressure file\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if p, err := sysmon.ParsePressure([]byte(in)); err == nil {
				t.Errorf("ParsePressure(%q) = %+v, nil; want an error", in, p)
			}
		})
	}
}

// FuzzParseMeminfo is Q15: no input panics, and parsing is deterministic.
func FuzzParseMeminfo(f *testing.F) {
	f.Add([]byte(hostMeminfo))
	f.Add([]byte("MemTotal: 1 kB\nMemAvailable: 1 kB\n"))
	f.Add([]byte("MemTotal: 18446744073709551615 kB\nMemAvailable: 1 kB\n"))
	f.Add([]byte(""))
	f.Add([]byte("MemTotal:\x00\n:::\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		m1, err1 := sysmon.ParseMeminfo(b)
		m2, err2 := sysmon.ParseMeminfo(b)
		if (err1 == nil) != (err2 == nil) || m1 != m2 {
			t.Fatalf("ParseMeminfo is not deterministic: %+v/%v then %+v/%v", m1, err1, m2, err2)
		}
	})
}

// FuzzParsePressure is Q15: no input panics, and parsing is deterministic.
func FuzzParsePressure(f *testing.F) {
	f.Add([]byte(hostPSIMemory))
	f.Add([]byte(hostPSICPU))
	f.Add([]byte("some avg60=\n"))
	f.Add([]byte("some avg60=NaN avg10=Inf\n"))
	f.Add([]byte("=\n= =\nsome\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p1, err1 := sysmon.ParsePressure(b)
		p2, err2 := sysmon.ParsePressure(b)
		// fmt renders NaN the same way twice, where == would not.
		if (err1 == nil) != (err2 == nil) || fmt.Sprint(p1) != fmt.Sprint(p2) {
			t.Fatalf("ParsePressure is not deterministic: %+v/%v then %+v/%v", p1, err1, p2, err2)
		}
	})
}
