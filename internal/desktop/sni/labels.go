package sni

// The package's user-visible strings, kept in this one place: task 8.2 moves
// them into the messages catalog.
const (
	// tooltipFormat is the tooltip text, formatted with the unread count
	// (R8.1).
	tooltipFormat = "%d unread notices"
	// labelMoreFormat states how many unread notices the menu does not list
	// (R9.1).
	labelMoreFormat = "%d more\u2026"

	labelCheckNow      = "Check now"
	labelMarkAllRead   = "Mark all as read"
	labelPauseHour     = "Pause for 1 hour"
	labelPauseTomorrow = "Pause until tomorrow"
	labelResume        = "Resume"
	labelQuit          = "Quit"
)
