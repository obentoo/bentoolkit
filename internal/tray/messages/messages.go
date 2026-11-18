// Package messages is bentoo-tray's message catalog: every menu label,
// notification text and summary format the tray shows, in English, in one
// place. Each entry is marked for gettext extraction and translated when it is
// shown, through the bentoolkit catalog the command loads at startup. It
// imports only internal/common/i18n of this module, so the desktop wrappers,
// the tray policy and the command can all use it.
//
// Notice contents (titles and summaries from the feed or from Portage news)
// are data, not catalog text: they are never translated.
package messages

import (
	"fmt"
	"strconv"

	"github.com/obentoo/bentoolkit/internal/common/i18n"
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

	// SysEmergeSummary is the failed-build notification summary.
	SysEmergeSummary
	// SysEmergeBody is the failed-build notification body; %s names the
	// failed packages (an atom, or SysEmergeMore).
	SysEmergeBody
	// SysEmergeBodyLog is SysEmergeBody with the first failed package's build
	// log; the first %s names the failed packages, the second is the log path.
	SysEmergeBodyLog
	// SysEmergeMore names several failed packages; %s is the first failed
	// atom, %d how many others failed.
	SysEmergeMore
	// SysAlertEmerge is the tooltip line of the failed-build condition; %s
	// names the failed packages (an atom, or SysEmergeMore).
	SysAlertEmerge
	// SysUnitsSummary is the failed-unit notification summary.
	SysUnitsSummary
	// SysUnitsBody is the failed-unit notification body; the first %s is the
	// unit, the second its manager ("system" or "user"), %d how many units
	// are failed.
	SysUnitsBody
	// SysAlertUnit is the tooltip line of a failed unit; the first %s is the
	// unit, the second its manager.
	SysAlertUnit
	// SysReadonlySummary is the read-only filesystem notification summary;
	// %s is the mount point.
	SysReadonlySummary
	// SysReadonlyBody is the read-only filesystem notification body; the
	// three %s are the mount point, the device and the filesystem type.
	SysReadonlyBody
	// SysAlertReadonly is the tooltip line of a read-only filesystem; the
	// three %s are the mount point, the device and the filesystem type.
	SysAlertReadonly
	// SysSmartSummary is the failing-drive notification summary.
	SysSmartSummary
	// SysSmartBody is the failing-drive notification body; the three %s are
	// the drive model, its serial number and the reason.
	SysSmartBody
	// SysSmartFailing is the reason of a drive whose SMART self-assessment
	// reports failing.
	SysSmartFailing
	// SysAlertSmart is the tooltip line of a failing drive; the two %s are
	// the drive model and the reason.
	SysAlertSmart

	// SysUpdatesSummary is the pending-updates notification summary.
	SysUpdatesSummary
	// SysUpdatesBody is the pending-updates notification body; %d is the
	// number of packages to update, %s names them (the first two, or
	// SysUpdatesMore).
	SysUpdatesBody
	// SysUpdatesMore names more than two packages; %s is the first two, %d
	// how many others there are.
	SysUpdatesMore
	// SysInfoUpdates is the informational tooltip line of pending updates;
	// %d and %s are as in SysUpdatesBody.
	SysInfoUpdates
	// SysWorldBrokenSummary is the summary of the notification sent when
	// the @world update cannot be computed.
	SysWorldBrokenSummary
	// SysWorldBrokenBody is that notification's body, telling the user what
	// to run to see why.
	SysWorldBrokenBody
	// SysInfoWorldBroken is the informational tooltip line shown while the
	// @world update cannot be computed.
	SysInfoWorldBroken
	// SysGLSASummary is the security-advisory notification summary.
	SysGLSASummary
	// SysGLSABody is the security-advisory notification body; the three %s
	// are the GLSA id, its title and the affected package atoms, %d the
	// number of advisories that apply.
	SysGLSABody
	// SysAlertGLSA is the tooltip line of one applicable advisory; the two
	// %s are its id and title.
	SysAlertGLSA
	// SysSyncSummary is the stale-repository notification summary; %s is
	// the repository.
	SysSyncSummary
	// SysSyncBody is the stale-repository notification body; the first %s
	// is the repository, the second how long ago it was synced (SysDaysOne
	// or SysDaysMany).
	SysSyncBody
	// SysAlertSync is the tooltip line of a stale repository; the two %s
	// are as in SysSyncBody.
	SysAlertSync
	// SysDaysOne is a span of exactly one day; %d is the count.
	SysDaysOne
	// SysDaysMany is a span of any other number of days; %d is the count.
	SysDaysMany
)

// catalog is the English text of every Key, each marked with i18n.N so
// misc/i18n-extract collects it; Text translates it. The ellipsis is written
// as an escape so no literal non-ASCII rune enters the source.
var catalog = map[Key]string{
	TooltipOne:        i18n.NPlural("%d unread notice", "%d unread notices"),
	TooltipMany:       "%d unread notices", // translated as TooltipOne's plural
	MenuMore:          i18n.N("%d more\u2026"),
	MenuCheckNow:      i18n.N("Check now"),
	MenuMarkAllRead:   i18n.N("Mark all as read"),
	MenuPauseHour:     i18n.N("Pause for 1 hour"),
	MenuPauseTomorrow: i18n.N("Pause until tomorrow"),
	MenuResume:        i18n.N("Resume"),
	MenuQuit:          i18n.N("Quit"),
	ActionOpen:        i18n.N("Open"),
	ActionMarkRead:    i18n.N("Mark as read"),
	SummaryNewNotices: i18n.NPlural("%d new notices", "%d new notices"), // English reads the same for every count

	SysDiskSummary:    i18n.N("Low disk space on %s"),
	SysDiskBody:       i18n.N("%s free (%.1f%%)"),
	SysMemorySummary:  i18n.N("Memory almost exhausted"),
	SysMemoryBody:     i18n.N("%s of %s available; swap %s free"),
	SysCPUSummary:     i18n.N("CPU saturated"),
	SysCPUBody:        i18n.N("Pressure %.0f%% for %d min"),
	SysNetDownSummary: i18n.N("Network down"),
	SysNetDownBody:    i18n.N("No network connection is available."),
	SysNetBackSummary: i18n.N("Network back"),
	SysAlertDisk:      i18n.N("Disk %s: %s free (%.1f%%)"),
	SysAlertMemory:    i18n.N("Memory: %s of %s available"),
	SysAlertCPU:       i18n.N("CPU pressure: %.0f%% for %d min"),
	SysAlertNetwork:   i18n.N("Network: down"),
	SysAlertsOne:      i18n.NPlural("%d system alert", "%d system alerts"),
	SysAlertsMany:     "%d system alerts", // translated as SysAlertsOne's plural

	SysEmergeSummary:   i18n.N("Package build failed"),
	SysEmergeBody:      i18n.N("%s failed to build."),
	SysEmergeBodyLog:   i18n.N("%s failed to build. Log: %s"),
	SysEmergeMore:      i18n.N("%s and %d more"),
	SysAlertEmerge:     i18n.N("Build failed: %s"),
	SysUnitsSummary:    i18n.N("systemd unit failed"),
	SysUnitsBody:       i18n.N("%s failed in the %s manager. Failed units: %d"),
	SysAlertUnit:       i18n.N("Unit %s failed (%s)"),
	SysReadonlySummary: i18n.N("Filesystem read-only: %s"),
	SysReadonlyBody:    i18n.N("%s (%s, %s) is now read-only."),
	SysAlertReadonly:   i18n.N("Read-only: %s (%s, %s)"),
	SysSmartSummary:    i18n.N("Drive failing"),
	SysSmartBody:       i18n.N("%s (serial %s): %s"),
	SysSmartFailing:    i18n.N("SMART reports failing"),
	SysAlertSmart:      i18n.N("Drive %s: %s"),

	SysUpdatesSummary:     i18n.N("Updates available"),
	SysUpdatesBody:        i18n.N("Packages to update in @world: %d (%s)"),
	SysUpdatesMore:        i18n.N("%s and %d more"),
	SysInfoUpdates:        i18n.N("Updates: %d pending (%s)"),
	SysWorldBrokenSummary: i18n.N("@world does not resolve"),
	SysWorldBrokenBody:    i18n.N("Portage cannot compute the @world update. Run emerge -puDN @world to see why."),
	SysInfoWorldBroken:    i18n.N("Updates: @world does not resolve"),
	SysGLSASummary:        i18n.N("Security advisory applies"),
	SysGLSABody:           i18n.N("GLSA %s: %s - affects %s. Applicable advisories: %d"),
	SysAlertGLSA:          i18n.N("GLSA %s: %s"),
	SysSyncSummary:        i18n.N("Repository not synced: %s"),
	SysSyncBody:           i18n.N("%s was last synced %s ago."),
	SysAlertSync:          i18n.N("Repository %s: synced %s ago"),
	SysDaysOne:            i18n.NPlural("%d day", "%d days"),
	SysDaysMany:           "%d days", // translated as SysDaysOne's plural
}

// Text returns k's catalog text, translated by the loaded gettext catalog
// (English when none is loaded). A key with no entry, which the catalog test
// rules out, yields its own name ("messages.Key(N)") rather than an empty
// label, so the gap shows on screen instead of hiding.
func Text(k Key) string {
	if s, ok := catalog[k]; ok {
		return i18n.T(s)
	}
	return "messages.Key(" + strconv.Itoa(int(k)) + ")"
}

// Format returns k's catalog text formatted with args, as fmt.Sprintf does.
func Format(k Key, args ...any) string {
	return fmt.Sprintf(Text(k), args...)
}

// Count formats the entry for n: one when n is 1, other for any other count
// in English, with n as the format's only argument. The pair is one gettext
// plural entry (marked with i18n.NPlural on one), so a translation picks the
// form its language needs for n: Russian has three.
func Count(one, other Key, n int) string {
	s1, ok1 := catalog[one]
	s2, ok2 := catalog[other]
	if !ok1 || !ok2 {
		if n == 1 {
			return Format(one, n)
		}
		return Format(other, n)
	}
	return fmt.Sprintf(i18n.TPlural(s1, s2, n), n)
}
