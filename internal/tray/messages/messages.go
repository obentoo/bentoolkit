// Package messages is bentoo-tray's message catalog: every menu label,
// notification text and summary format the tray shows, in English, in one
// place. It is a leaf package: it imports nothing of this module,
// so the desktop wrappers, the tray policy and the command can all use it.
package messages

import (
	"fmt"
	"strconv"
)

// Key names one catalog entry.
type Key int

// Catalog keys. A key whose entry is a format says so, with its verbs.
const (
	// TooltipOne is the tray tooltip for exactly one unread notice; %d is the
	// count.
	TooltipOne Key = iota
	// TooltipMany is the tray tooltip for any other unread count, zero
	// included; %d is the count.
	TooltipMany
	// MenuMore counts the unread notices the menu does not list; %d is the
	// count.
	MenuMore
	// MenuCheckNow is the menu entry that checks the feed at once.
	MenuCheckNow
	// MenuMarkAllRead is the menu entry that marks every notice read.
	MenuMarkAllRead
	// MenuPauseHour is the menu entry that pauses notifications for an hour.
	MenuPauseHour
	// MenuPauseTomorrow is the menu entry that pauses notifications until
	// the next day.
	MenuPauseTomorrow
	// MenuResume is the menu entry that ends a pause.
	MenuResume
	// MenuQuit is the menu entry that exits the tray.
	MenuQuit
	// ActionOpen is the notification action that opens a notice.
	ActionOpen
	// ActionMarkRead is the notification action that marks a notice read.
	ActionMarkRead
	// SummaryNewNotices is the burst summary notification; %d is the count,
	// always more than three.
	SummaryNewNotices

	// SysDiskSummary is the low-disk notification summary; %s is the watched
	// path, shown verbatim.
	SysDiskSummary
	// SysDiskBody is the low-disk notification body; %s is the free space in
	// human units, %.1f the free share of the filesystem in percent.
	SysDiskBody
	// SysMemorySummary is the low-memory notification summary.
	SysMemorySummary
	// SysMemoryBody is the low-memory notification body; the three %s are the
	// available memory, the total memory and the free swap, in human units.
	SysMemoryBody
	// SysCPUSummary is the CPU-pressure notification summary.
	SysCPUSummary
	// SysCPUBody is the CPU-pressure notification body; %.0f is the pressure
	// in percent, %d how many minutes it has lasted.
	SysCPUBody
	// SysNetDownSummary is the summary of the notification sent when the
	// network has been down long enough.
	SysNetDownSummary
	// SysNetDownBody is the body of the network-down notification.
	SysNetDownBody
	// SysNetBackSummary is the summary of the notification that replaces the
	// network-down one once the network is back.
	SysNetBackSummary
	// SysAlertDisk is the tooltip line of a disk condition; %s is the watched
	// path, %s the free space in human units, %.1f the free share in percent.
	SysAlertDisk
	// SysAlertMemory is the tooltip line of the memory condition; the two %s
	// are the available and the total memory, in human units.
	SysAlertMemory
	// SysAlertCPU is the tooltip line of the CPU condition; %.0f is the
	// pressure in percent, %d how many minutes it has lasted.
	SysAlertCPU
	// SysAlertNetwork is the tooltip line shown while the network is down.
	SysAlertNetwork
	// SysAlertsOne is the tooltip title suffix for exactly one active system
	// alert; %d is the count.
	SysAlertsOne
	// SysAlertsMany is the tooltip title suffix for any other count of active
	// system alerts; %d is the count.
	SysAlertsMany
)

// catalog is the English text of every Key. The ellipsis is written as an
// escape so no literal non-ASCII rune enters the source.
var catalog = map[Key]string{
	TooltipOne:        "%d unread notice",
	TooltipMany:       "%d unread notices",
	MenuMore:          "%d more\u2026",
	MenuCheckNow:      "Check now",
	MenuMarkAllRead:   "Mark all as read",
	MenuPauseHour:     "Pause for 1 hour",
	MenuPauseTomorrow: "Pause until tomorrow",
	MenuResume:        "Resume",
	MenuQuit:          "Quit",
	ActionOpen:        "Open",
	ActionMarkRead:    "Mark as read",
	SummaryNewNotices: "%d new notices",

	SysDiskSummary:    "Low disk space on %s",
	SysDiskBody:       "%s free (%.1f%%)",
	SysMemorySummary:  "Memory almost exhausted",
	SysMemoryBody:     "%s of %s available; swap %s free",
	SysCPUSummary:     "CPU saturated",
	SysCPUBody:        "Pressure %.0f%% for %d min",
	SysNetDownSummary: "Network down",
	SysNetDownBody:    "No network connection is available.",
	SysNetBackSummary: "Network back",
	SysAlertDisk:      "Disk %s: %s free (%.1f%%)",
	SysAlertMemory:    "Memory: %s of %s available",
	SysAlertCPU:       "CPU pressure: %.0f%% for %d min",
	SysAlertNetwork:   "Network: down",
	SysAlertsOne:      "%d system alert",
	SysAlertsMany:     "%d system alerts",
}

// Text returns k's catalog text. A key with no entry, which the catalog test
// rules out, yields its own name ("messages.Key(N)") rather than an empty
// label, so the gap shows on screen instead of hiding.
func Text(k Key) string {
	if s, ok := catalog[k]; ok {
		return s
	}
	return "messages.Key(" + strconv.Itoa(int(k)) + ")"
}

// Format returns k's catalog text formatted with args, as fmt.Sprintf does.
func Format(k Key, args ...any) string {
	return fmt.Sprintf(Text(k), args...)
}

// Count formats the entry for n: one when n is 1, other for any other count,
// with n as the format's only argument.
func Count(one, other Key, n int) string {
	if n == 1 {
		return Format(one, n)
	}
	return Format(other, n)
}
