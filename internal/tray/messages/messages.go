// Package messages is bentoo-tray's message catalog: every menu label,
// notification text and summary format the tray shows, in English, in one
// place (S072-R9.6). It is a leaf package: it imports nothing of this module,
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
	// count (R8.1).
	TooltipOne Key = iota
	// TooltipMany is the tray tooltip for any other unread count, zero
	// included; %d is the count (R8.1).
	TooltipMany
	// MenuMore counts the unread notices the menu does not list; %d is the
	// count (R9.1).
	MenuMore
	// MenuCheckNow is the menu entry that checks the feed at once (R9.2).
	MenuCheckNow
	// MenuMarkAllRead is the menu entry that marks every notice read (R9.2).
	MenuMarkAllRead
	// MenuPauseHour is the menu entry that pauses notifications for an hour
	// (R9.3).
	MenuPauseHour
	// MenuPauseTomorrow is the menu entry that pauses notifications until
	// the next day (R9.3).
	MenuPauseTomorrow
	// MenuResume is the menu entry that ends a pause (R9.3).
	MenuResume
	// MenuQuit is the menu entry that exits the tray (R9.5).
	MenuQuit
	// ActionOpen is the notification action that opens a notice (R6.5).
	ActionOpen
	// ActionMarkRead is the notification action that marks a notice read
	// (R6.5).
	ActionMarkRead
	// SummaryNewNotices is the burst summary notification; %d is the count,
	// always more than three (R6.8).
	SummaryNewNotices
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
