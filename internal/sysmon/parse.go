package sysmon

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// kibibyte converts the meminfo figures, labelled "kB" but counted in KiB.
const kibibyte = 1024

// ParseMeminfo reads MemTotal, MemAvailable, SwapTotal and SwapFree from the
// text of /proc/meminfo. Keys are matched whole, so SwapCached or MemFree is
// never read in place of a wanted key. MemTotal and MemAvailable are
// required: a missing MemAvailable is an error, never a fallback to MemFree.
// The swap keys read as zero when absent.
func ParseMeminfo(b []byte) (Memory, error) {
	var m Memory
	wanted := map[string]*uint64{
		"MemTotal":     &m.Total,
		"MemAvailable": &m.Available,
		"SwapTotal":    &m.SwapTotal,
		"SwapFree":     &m.SwapFree,
	}
	seen := make(map[string]bool, len(wanted))
	for line := range bytes.Lines(b) {
		key, rest, ok := strings.Cut(string(line), ":")
		dst, want := wanted[key]
		if !ok || !want {
			continue
		}
		v, err := parseKib(key, rest)
		if err != nil {
			return Memory{}, err
		}
		*dst = v
		seen[key] = true
	}
	for _, key := range []string{"MemTotal", "MemAvailable"} {
		if !seen[key] {
			return Memory{}, fmt.Errorf("meminfo: %s missing", key)
		}
	}
	return m, nil
}

// parseKib turns the value part of a meminfo line, "<n> kB", into bytes.
func parseKib(key, rest string) (uint64, error) {
	fields := strings.Fields(rest)
	if len(fields) != 2 || fields[1] != "kB" {
		return 0, fmt.Errorf("meminfo %s: value %q is not \"<n> kB\"", key, strings.TrimSpace(rest))
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("meminfo %s: %w", key, err)
	}
	if n > math.MaxUint64/kibibyte {
		return 0, fmt.Errorf("meminfo %s: %d kB overflows a byte count", key, n)
	}
	return n * kibibyte, nil
}

// ParsePressure reads the avg60 figures of a /proc/pressure file. The some
// and full lines are told apart by their first word and avg60 is found by
// its key, never by position. The some line is required; the full line is
// optional (older kernels expose none for cpu) and sets HasFull when present.
func ParsePressure(b []byte) (Pressure, error) {
	var p Pressure
	hasSome := false
	for line := range bytes.Lines(b) {
		text := strings.TrimRight(string(line), "\n")
		fields := strings.Fields(text)
		if len(fields) == 0 || (fields[0] != "some" && fields[0] != "full") {
			continue
		}
		avg, err := pressureAvg60(fields[1:])
		if err != nil {
			return Pressure{}, fmt.Errorf("pressure line %q: %w", text, err)
		}
		if fields[0] == "some" {
			p.SomeAvg60, hasSome = avg, true
		} else {
			p.FullAvg60, p.HasFull = avg, true
		}
	}
	if !hasSome {
		return Pressure{}, errors.New("pressure: no some line")
	}
	return p, nil
}

// pressureAvg60 finds the avg60= field and parses it as a finite,
// non-negative percentage.
func pressureAvg60(fields []string) (float64, error) {
	for _, f := range fields {
		v, ok := strings.CutPrefix(f, "avg60=")
		if !ok {
			continue
		}
		avg, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("avg60: %w", err)
		}
		if math.IsNaN(avg) || math.IsInf(avg, 0) || avg < 0 {
			return 0, fmt.Errorf("avg60: %q is not a percentage", v)
		}
		return avg, nil
	}
	return 0, errors.New("avg60 missing")
}
