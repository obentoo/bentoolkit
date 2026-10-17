// Package unitmon lists the units a systemd manager reports failed and tells
// when any unit's ActiveState may have changed, over D-Bus. One Monitor
// watches one manager: the system manager on the system bus or the user
// manager on the session bus. Policy (notify, clear, log) is the caller's:
// this package only reports.
package unitmon
