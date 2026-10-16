package tray

// Pinned contract (story 097, design.md "internal/tray/health.go"):
//
//	type level int // levelOK, levelWarning, levelCritical
//	func classifyDisk(d sysmon.Disk, t config.DiskMonitor, cur level) level
//	func classifyMemory(m sysmon.Memory, p *sysmon.Pressure, t config.MemoryMonitor, cur level) level
//	func classifyCPU(p sysmon.Pressure, emerge bool, t config.CPUMonitor, cur level) level
//
// Thresholds come from config.TrayConfig{}.GetMonitors(), the defaults of the
// design's Data Models. Every boundary below is exact in float64 under every
// reasonable formulation (free/size*100, free*100/size, size*thr/100), and
// "just under" / "just over" step by 1 MiB so float rounding cannot decide.

import (
	"fmt"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/sysmon"
)

const (
	clsMiB uint64 = 1 << 20
	clsGiB uint64 = 1 << 30
	clsTiB uint64 = 1 << 40
)

func clsName(l level) string {
	switch l {
	case levelOK:
		return "ok"
	case levelWarning:
		return "warning"
	case levelCritical:
		return "critical"
	}
	return fmt.Sprintf("level(%d)", int(l))
}

func clsDefaults(t *testing.T) config.Monitors {
	t.Helper()
	m, warns := config.TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Fatalf("GetMonitors() on an empty tray config warned: %v", warns)
	}
	return m
}

// TestClassifyDefaultsMatchDesign pins the defaults every boundary table below
// is computed from, so a wrong default fails here and not as a confusing
// classification mismatch.
func TestClassifyDefaultsMatchDesign(t *testing.T) {
	m := clsDefaults(t)
	checks := []struct {
		name      string
		got, want float64
	}{
		{"disk.warn_percent", float64(m.Disk.WarnPercent), 10},
		{"disk.warn_free", float64(m.Disk.WarnFree), float64(20 * clsGiB)},
		{"disk.crit_percent", float64(m.Disk.CritPercent), 2},
		{"disk.crit_free", float64(m.Disk.CritFree), float64(2 * clsGiB)},
		{"memory.warn_percent", float64(m.Memory.WarnPercent), 10},
		{"memory.crit_percent", float64(m.Memory.CritPercent), 5},
		{"memory.crit_swap_percent", float64(m.Memory.CritSwapPercent), 10},
		{"memory.psi_full_crit", float64(m.Memory.PSIFullCrit), 10},
		{"cpu.warn_psi", float64(m.CPU.WarnPSI), 80},
		{"cpu.clear_psi", float64(m.CPU.ClearPSI), 70},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("default %s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func clsDisk(size, free uint64) sysmon.Disk {
	return sysmon.Disk{Path: "/var/tmp/portage", Dev: 42, Size: size, Free: free}
}

// TestClassifyDiskR2_6R2_7 covers raising: warning needs free below 10% AND
// below 20 GiB; critical needs free below 2% OR below 2 GiB. Hostile rows
// first: readings that satisfy only one half of the AND must not warn, and
// readings that satisfy only one half of the OR must be critical.
func TestClassifyDiskR2_6R2_7(t *testing.T) {
	d := clsDefaults(t).Disk
	cases := []struct {
		name       string
		size, free uint64
		want       level
	}{
		// Hostile: the warning rule is an AND.
		{"R2.6 hostile: 5% free but 50 GiB free is not low", 1000 * clsGiB, 50 * clsGiB, levelOK},
		{"R2.6 hostile: 15 GiB free but 15% free is not low", 100 * clsGiB, 15 * clsGiB, levelOK},
		{"R2.6 hostile: 9% free of 1 TiB is 92 GiB, not low", clsTiB, clsTiB * 9 / 100, levelOK},
		// Hostile: the critical rule is an OR.
		{"R2.7 hostile: 1.5 GiB free at 3% is critical by size", 50 * clsGiB, 1536 * clsMiB, levelCritical},
		{"R2.7 hostile: 150 GiB free at 1.46% is critical by percent", 10 * clsTiB, 150 * clsGiB, levelCritical},
		// Warning percent boundary on 100 GiB (10% = 10 GiB < 20 GiB).
		{"R2.6 percent at 10% is not below", 100 * clsGiB, 10 * clsGiB, levelOK},
		{"R2.6 percent just under 10%", 100 * clsGiB, 10*clsGiB - clsMiB, levelWarning},
		{"R2.6 percent just over 10%", 100 * clsGiB, 10*clsGiB + clsMiB, levelOK},
		// Warning size boundary on 500 GiB (20 GiB = 4%).
		{"R2.6 size at 20 GiB is not below", 500 * clsGiB, 20 * clsGiB, levelOK},
		{"R2.6 size just under 20 GiB", 500 * clsGiB, 20*clsGiB - clsMiB, levelWarning},
		{"R2.6 size just over 20 GiB", 500 * clsGiB, 20*clsGiB + clsMiB, levelOK},
		// Critical percent boundary on 500 GiB (2% = 10 GiB > 2 GiB).
		{"R2.7 percent at 2% is warning", 500 * clsGiB, 10 * clsGiB, levelWarning},
		{"R2.7 percent just under 2%", 500 * clsGiB, 10*clsGiB - clsMiB, levelCritical},
		{"R2.7 percent just over 2%", 500 * clsGiB, 10*clsGiB + clsMiB, levelWarning},
		// Critical size boundary on 50 GiB (2 GiB = 4%).
		{"R2.7 size at 2 GiB is warning", 50 * clsGiB, 2 * clsGiB, levelWarning},
		{"R2.7 size just under 2 GiB", 50 * clsGiB, 2*clsGiB - clsMiB, levelCritical},
		{"R2.7 size just over 2 GiB", 50 * clsGiB, 2*clsGiB + clsMiB, levelWarning},
		{"R2.7 nothing free", 50 * clsGiB, 0, levelCritical},
		{"plenty free", 100 * clsGiB, 60 * clsGiB, levelOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyDisk(clsDisk(c.size, c.free), d, levelOK); got != c.want {
				t.Errorf("classifyDisk(size=%d, free=%d, cur=ok) = %s, want %s", c.size, c.free, clsName(got), clsName(c.want))
			}
		})
	}
}

// TestClassifyDiskHysteresisR6_9 covers leaving a level: an active warning
// clears only at free >= 12% OR >= 24 GiB; a critical falls to warning only at
// free >= 2.4% AND >= 2.4 GiB. The hostile rows come first: the same reading
// inside a band must NOT hold a condition that is not active, and must NOT
// release one that is.
func TestClassifyDiskHysteresisR6_9(t *testing.T) {
	d := clsDefaults(t).Disk
	// ceil(2.4 GiB): the smallest byte count at or over the critical clear size.
	critClearFree := (24*clsGiB + 9) / 10
	cases := []struct {
		name       string
		cur        level
		size, free uint64
		want       level
	}{
		// Hostile: a band reading must not raise a condition that is not active.
		{"hostile: 11% free with no active condition stays ok", levelOK, 100 * clsGiB, 11 * clsGiB, levelOK},
		{"hostile: 22 GiB free with no active condition stays ok", levelOK, 500 * clsGiB, 22 * clsGiB, levelOK},
		{"hostile: 2.2% free with an active warning is not critical", levelWarning, 500 * clsGiB, 11 * clsGiB, levelWarning},
		{"hostile: 2.2 GiB free with an active warning is not critical", levelWarning, 50 * clsGiB, 2253 * clsMiB, levelWarning},
		// Warning clear, percent dimension on 100 GiB (12% = 12 GiB < 24 GiB).
		{"warning held at 11%", levelWarning, 100 * clsGiB, 11 * clsGiB, levelWarning},
		{"warning held just under 12%", levelWarning, 100 * clsGiB, 12*clsGiB - clsMiB, levelWarning},
		{"warning clears at 12%", levelWarning, 100 * clsGiB, 12 * clsGiB, levelOK},
		{"warning clears just over 12%", levelWarning, 100 * clsGiB, 12*clsGiB + clsMiB, levelOK},
		// Warning clear, size dimension on 500 GiB (24 GiB = 4.8% < 12%).
		{"warning held at 22 GiB", levelWarning, 500 * clsGiB, 22 * clsGiB, levelWarning},
		{"warning held just under 24 GiB", levelWarning, 500 * clsGiB, 24*clsGiB - clsMiB, levelWarning},
		{"warning clears at 24 GiB", levelWarning, 500 * clsGiB, 24 * clsGiB, levelOK},
		{"warning clears just over 24 GiB", levelWarning, 500 * clsGiB, 24*clsGiB + clsMiB, levelOK},
		// Critical clear needs BOTH 2.4% and 2.4 GiB.
		{"hostile: critical held at 4% free but only 2 GiB", levelCritical, 50 * clsGiB, 2 * clsGiB, levelCritical},
		{"hostile: critical held at 225 GiB free but only 2.2%", levelCritical, 10 * clsTiB, 225 * clsGiB, levelCritical},
		{"critical held at 2.2% (500 GiB)", levelCritical, 500 * clsGiB, 11 * clsGiB, levelCritical},
		{"critical held just under 2.4% (500 GiB)", levelCritical, 500 * clsGiB, 12*clsGiB - clsMiB, levelCritical},
		{"critical falls to warning at 2.4% and 12 GiB", levelCritical, 500 * clsGiB, 12 * clsGiB, levelWarning},
		{"critical falls to warning just over 2.4%", levelCritical, 500 * clsGiB, 12*clsGiB + clsMiB, levelWarning},
		{"critical held just under 2.4 GiB (50 GiB disk)", levelCritical, 50 * clsGiB, critClearFree - clsMiB, levelCritical},
		{"critical falls to warning at 2.4 GiB (50 GiB disk)", levelCritical, 50 * clsGiB, critClearFree, levelWarning},
		// A critical reading past every clear level drops straight to ok.
		{"critical clears to ok when plenty is free", levelCritical, 100 * clsGiB, 50 * clsGiB, levelOK},
		// A critical reading during an active warning escalates at once.
		{"warning escalates on a critical reading", levelWarning, 100 * clsGiB, clsGiB, levelCritical},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyDisk(clsDisk(c.size, c.free), d, c.cur); got != c.want {
				t.Errorf("classifyDisk(size=%d, free=%d, cur=%s) = %s, want %s", c.size, c.free, clsName(c.cur), clsName(got), clsName(c.want))
			}
		})
	}
}

// TestClassifyDiskUsesConfiguredThresholds: the defaults are not hard-coded.
func TestClassifyDiskUsesConfiguredThresholds(t *testing.T) {
	d := clsDefaults(t).Disk
	d.WarnPercent = 50
	d.WarnFree = 500 * clsGiB
	if got := classifyDisk(clsDisk(1000*clsGiB, 400*clsGiB), d, levelOK); got != levelWarning {
		t.Errorf("with warn 50%%/500 GiB, 400 GiB free of 1000 GiB = %s, want warning", clsName(got))
	}
	if got := classifyDisk(clsDisk(1000*clsGiB, 600*clsGiB), d, levelOK); got != levelOK {
		t.Errorf("with warn 50%%/500 GiB, 600 GiB free of 1000 GiB = %s, want ok", clsName(got))
	}
}

const clsMemTotal = 100 * clsGiB

func clsMem(avail, swapTotal, swapFree uint64) sysmon.Memory {
	return sysmon.Memory{Total: clsMemTotal, Available: avail, SwapTotal: swapTotal, SwapFree: swapFree}
}

func clsPSI(full float64) *sysmon.Pressure {
	return &sysmon.Pressure{SomeAvg60: full, FullAvg60: full, HasFull: true}
}

// TestClassifyMemoryR3 covers raising on a 100 GiB machine: warning below 10%
// available; critical below 5% while swap is absent or under 10% free; critical
// at PSI full avg60 >= 10.00. Hostile rows first: low memory with plenty of
// swap free, a high PSI some (not full) value, a PSI full value without a full
// line, and no PSI at all must not raise critical.
func TestClassifyMemoryR3(t *testing.T) {
	m := clsDefaults(t).Memory
	const swap = 50 * clsGiB // 10% = 5 GiB, 12% = 6 GiB
	cases := []struct {
		name string
		mem  sysmon.Memory
		psi  *sysmon.Pressure
		want level
	}{
		{"R3.2 hostile: 1% available with 50% swap free is warning, not critical", clsMem(clsGiB, swap, 25*clsGiB), nil, levelWarning},
		{"R3.2 hostile: 6% available with no swap is warning, not critical", clsMem(6*clsGiB, 0, 0), nil, levelWarning},
		{"R3.3 hostile: PSI some 95 with full 0 is not memory pressure", clsMem(50*clsGiB, 0, 0), &sysmon.Pressure{SomeAvg60: 95, FullAvg60: 0, HasFull: true}, levelOK},
		{"R3.4 hostile: no full line, FullAvg60 is not read", clsMem(50*clsGiB, 0, 0), &sysmon.Pressure{SomeAvg60: 1, FullAvg60: 50, HasFull: false}, levelOK},
		{"R3.4 no PSI file at all", clsMem(50*clsGiB, 0, 0), nil, levelOK},
		// R3.1 warning boundary.
		{"R3.1 at 10% is not below", clsMem(10*clsGiB, 0, 0), nil, levelOK},
		{"R3.1 just under 10%", clsMem(10*clsGiB-clsMiB, 0, 0), nil, levelWarning},
		{"R3.1 just over 10%", clsMem(10*clsGiB+clsMiB, 0, 0), nil, levelOK},
		// R3.2 with swap absent.
		{"R3.2 no swap, at 5% is warning", clsMem(5*clsGiB, 0, 0), nil, levelWarning},
		{"R3.2 no swap, just under 5%", clsMem(5*clsGiB-clsMiB, 0, 0), nil, levelCritical},
		{"R3.2 no swap, just over 5%", clsMem(5*clsGiB+clsMiB, 0, 0), nil, levelWarning},
		// R3.2 with swap present: the swap free boundary at 10% of 50 GiB.
		{"R3.2 swap at 10% free is warning", clsMem(4*clsGiB, swap, 5*clsGiB), nil, levelWarning},
		{"R3.2 swap just under 10% free", clsMem(4*clsGiB, swap, 5*clsGiB-clsMiB), nil, levelCritical},
		{"R3.2 swap just over 10% free", clsMem(4*clsGiB, swap, 5*clsGiB+clsMiB), nil, levelWarning},
		{"R3.2 swap exhausted", clsMem(4*clsGiB, swap, 0), nil, levelCritical},
		// R3.3 PSI full avg60 boundary with plenty of memory available.
		{"R3.3 PSI full at 10.00", clsMem(50*clsGiB, 0, 0), clsPSI(10.00), levelCritical},
		{"R3.3 PSI full just under 10.00", clsMem(50*clsGiB, 0, 0), clsPSI(9.99), levelOK},
		{"R3.3 PSI full just over 10.00", clsMem(50*clsGiB, 0, 0), clsPSI(10.01), levelCritical},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyMemory(c.mem, c.psi, m, levelOK); got != c.want {
				t.Errorf("classifyMemory(%+v, %+v, cur=ok) = %s, want %s", c.mem, c.psi, clsName(got), clsName(c.want))
			}
		})
	}
}

// TestClassifyMemoryHysteresisR6_9 covers leaving a level: a warning clears at
// >= 12% available; a critical leaves critical when (available >= 6% OR swap
// free >= 12%) AND PSI full avg60 < 8.00.
func TestClassifyMemoryHysteresisR6_9(t *testing.T) {
	m := clsDefaults(t).Memory
	const swap = 50 * clsGiB
	cases := []struct {
		name string
		cur  level
		mem  sysmon.Memory
		psi  *sysmon.Pressure
		want level
	}{
		{"hostile: 11% with no active condition stays ok", levelOK, clsMem(11*clsGiB, 0, 0), nil, levelOK},
		{"hostile: PSI full 9.00 with no active condition stays ok", levelOK, clsMem(50*clsGiB, 0, 0), clsPSI(9), levelOK},
		{"hostile: 5.5% no swap with an active warning is not critical", levelWarning, clsMem(5632*clsMiB, 0, 0), nil, levelWarning},
		{"warning held at 11%", levelWarning, clsMem(11*clsGiB, 0, 0), nil, levelWarning},
		{"warning held just under 12%", levelWarning, clsMem(12*clsGiB-clsMiB, 0, 0), nil, levelWarning},
		{"warning clears at 12%", levelWarning, clsMem(12*clsGiB, 0, 0), nil, levelOK},
		{"warning clears just over 12%", levelWarning, clsMem(12*clsGiB+clsMiB, 0, 0), nil, levelOK},
		{"critical held at 5.5% no swap", levelCritical, clsMem(5632*clsMiB, 0, 0), nil, levelCritical},
		{"critical held just under 6% no swap", levelCritical, clsMem(6*clsGiB-clsMiB, 0, 0), nil, levelCritical},
		{"critical falls to warning at 6%", levelCritical, clsMem(6*clsGiB, 0, 0), nil, levelWarning},
		{"critical falls to warning just over 6%", levelCritical, clsMem(6*clsGiB+clsMiB, 0, 0), nil, levelWarning},
		{"critical held at 3% with swap 11% free", levelCritical, clsMem(3*clsGiB, swap, 5632*clsMiB), nil, levelCritical},
		{"critical held at 3% with swap just under 12% free", levelCritical, clsMem(3*clsGiB, swap, 6*clsGiB-clsMiB), nil, levelCritical},
		{"critical falls to warning at 3% with swap 12% free", levelCritical, clsMem(3*clsGiB, swap, 6*clsGiB), nil, levelWarning},
		{"hostile: critical held at 7% available while PSI full is 8.50", levelCritical, clsMem(7*clsGiB, 0, 0), clsPSI(8.5), levelCritical},
		{"critical held at PSI full 8.00", levelCritical, clsMem(50*clsGiB, 0, 0), clsPSI(8.00), levelCritical},
		{"critical clears to ok at PSI full 7.99", levelCritical, clsMem(50*clsGiB, 0, 0), clsPSI(7.99), levelOK},
		{"critical clears straight to ok at 13% and no PSI", levelCritical, clsMem(13*clsGiB, 0, 0), nil, levelOK},
		{"warning escalates on a critical reading", levelWarning, clsMem(clsGiB, 0, 0), nil, levelCritical},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyMemory(c.mem, c.psi, m, c.cur); got != c.want {
				t.Errorf("classifyMemory(%+v, %+v, cur=%s) = %s, want %s", c.mem, c.psi, clsName(c.cur), clsName(got), clsName(c.want))
			}
		})
	}
}

// TestClassifyMemoryUsesConfiguredThresholds: the defaults are not hard-coded.
func TestClassifyMemoryUsesConfiguredThresholds(t *testing.T) {
	m := clsDefaults(t).Memory
	m.WarnPercent = 50
	if got := classifyMemory(clsMem(40*clsGiB, 0, 0), nil, m, levelOK); got != levelWarning {
		t.Errorf("with warn 50%%, 40%% available = %s, want warning", clsName(got))
	}
}

func clsCPU(some float64) sysmon.Pressure {
	return sysmon.Pressure{SomeAvg60: some, FullAvg60: 0, HasFull: true}
}

// TestClassifyCPUR4 covers raising and clearing: warning at some avg60 >=
// 80.00, held until below 70.00, suppressed while emerge runs. Hostile rows
// first: a build at full pressure, a high full value with a low some value,
// and a band reading with no active condition must all stay ok.
func TestClassifyCPUR4(t *testing.T) {
	c := clsDefaults(t).CPU
	cases := []struct {
		name   string
		cur    level
		p      sysmon.Pressure
		emerge bool
		want   level
	}{
		{"R4.3 hostile: 99.00 while emerge runs is ok", levelOK, clsCPU(99), true, levelOK},
		{"R4.3 hostile: 80.00 while emerge runs is ok", levelOK, clsCPU(80), true, levelOK},
		{"R4.1 hostile: full 95 with some 10 is not CPU pressure", levelOK, sysmon.Pressure{SomeAvg60: 10, FullAvg60: 95, HasFull: true}, false, levelOK},
		{"R6.10 hostile: 75.00 with no active condition stays ok", levelOK, clsCPU(75), false, levelOK},
		{"R4.1 just under 80.00", levelOK, clsCPU(79.99), false, levelOK},
		{"R4.1 at 80.00", levelOK, clsCPU(80.00), false, levelWarning},
		{"R4.1 just over 80.00", levelOK, clsCPU(80.01), false, levelWarning},
		{"R4.1 at 99.00 without emerge", levelOK, clsCPU(99), false, levelWarning},
		{"R6.10 held at 75.00", levelWarning, clsCPU(75), false, levelWarning},
		{"R6.10 held just over 70.00", levelWarning, clsCPU(70.01), false, levelWarning},
		{"R6.10 held at 70.00", levelWarning, clsCPU(70.00), false, levelWarning},
		{"R6.10 clears just under 70.00", levelWarning, clsCPU(69.99), false, levelOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCPU(tc.p, tc.emerge, c, tc.cur); got != tc.want {
				t.Errorf("classifyCPU(%+v, emerge=%v, cur=%s) = %s, want %s", tc.p, tc.emerge, clsName(tc.cur), clsName(got), clsName(tc.want))
			}
		})
	}
}

// TestClassifyCPUNeverCriticalR4_2 sweeps every current level and a range of
// readings, the saturated ones included.
func TestClassifyCPUNeverCriticalR4_2(t *testing.T) {
	c := clsDefaults(t).CPU
	for _, cur := range []level{levelOK, levelWarning, levelCritical} {
		for _, v := range []float64{0, 69.99, 70, 79.99, 80, 95, 100} {
			if got := classifyCPU(clsCPU(v), false, c, cur); got == levelCritical {
				t.Errorf("classifyCPU(some=%.2f, cur=%s) = critical; CPU is never critical", v, clsName(cur))
			}
		}
	}
}

// TestClassifyCPUUsesConfiguredThresholds: warn and clear come from config.
func TestClassifyCPUUsesConfiguredThresholds(t *testing.T) {
	c := clsDefaults(t).CPU
	c.WarnPSI = 50
	c.ClearPSI = 40
	if got := classifyCPU(clsCPU(50), false, c, levelOK); got != levelWarning {
		t.Errorf("with warn 50, some 50 = %s, want warning", clsName(got))
	}
	if got := classifyCPU(clsCPU(41), false, c, levelWarning); got != levelWarning {
		t.Errorf("with clear 40, some 41 while active = %s, want warning", clsName(got))
	}
	if got := classifyCPU(clsCPU(39.99), false, c, levelWarning); got != levelOK {
		t.Errorf("with clear 40, some 39.99 while active = %s, want ok", clsName(got))
	}
}

// TestClassifyDiskPseudoFilesystem: a filesystem whose Size is 0 (a pseudo
// filesystem) is always ok, whatever Free says and whatever level is active,
// and computing it must not divide by zero. Hostile half: 0 free on 0 bytes
// reads as "below 2 GiB" under a literal reading of the critical rule.
func TestClassifyDiskPseudoFilesystem(t *testing.T) {
	d := clsDefaults(t).Disk
	for _, cur := range []level{levelOK, levelWarning, levelCritical} {
		for _, free := range []uint64{0, clsMiB, 50 * clsGiB} {
			got := func() (l level) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("classifyDisk(size=0, free=%d, cur=%s) panicked: %v", free, clsName(cur), r)
					}
				}()
				return classifyDisk(clsDisk(0, free), d, cur)
			}()
			if got != levelOK {
				t.Errorf("classifyDisk(size=0, free=%d, cur=%s) = %s, want ok", free, clsName(cur), clsName(got))
			}
		}
	}
}
