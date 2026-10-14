// Package sysmon reads host resource figures for bentoo-tray: memory from
// /proc/meminfo, pressure stall information from /proc/pressure, free disk
// space and running emerge processes. It returns data and wrapped errors
// only; it never logs and applies no thresholds, which are the caller's
// policy.
package sysmon

// Memory is a /proc/meminfo reading, every field in bytes.
type Memory struct {
	Total, Available, SwapTotal, SwapFree uint64
}

// Pressure is the avg60 figures of a /proc/pressure file. HasFull reports
// whether the file carried a full line at all, so an all-zero full line is
// told apart from an absent one.
type Pressure struct {
	SomeAvg60, FullAvg60 float64
	HasFull              bool
}
