package tray

import (
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
)

// netEvent is the network notification a netWatch decides is due.
type netEvent int

const (
	netNone netEvent = iota
	netDown
	netBack
)

// netWatch debounces NetworkManager verdicts into "network down" and
// "network back" decisions. It only decides: a down counts as sent once the
// App calls markSent after a successful, unpaused Send, and only a sent down
// is ever followed by a back. An event the App does not send is handed to
// skip, so it is not decided again.
type netWatch struct {
	enabled          bool
	downAfter        time.Duration
	downAfterStartup time.Duration
	backAfter        time.Duration
	startup          time.Time

	seen         bool // a verdict from a present NetworkManager has arrived
	absent       bool // the latest verdict said NetworkManager is absent
	online       bool
	since        time.Time // when the current online or offline spell began
	startupSpell bool      // the current offline spell is the first verdict
	downSkipped  bool      // skip(netDown) handled the current offline spell

	notifiedDown bool   // set only by markSent(netDown, id)
	notifID      uint32 // the sent "down" id, replaced by "back"
}

// newNetWatch returns a watch for a process started at start, using the
// windows of t.
func newNetWatch(start time.Time, t config.NetworkMonitor) *netWatch {
	return &netWatch{
		enabled:          t.Enabled,
		downAfter:        t.DownAfter,
		downAfterStartup: t.DownAfterStartup,
		backAfter:        t.BackAfter,
		startup:          start,
	}
}

// update records the verdict observed at now and returns the wait until the
// next decision falls due; ok is false when no timer is needed. A repeated
// verdict keeps the current spell, so it never pushes a deadline back. An
// NMAbsent verdict leaves the watch idle: its zero Online is no outage.
func (w *netWatch) update(now time.Time, v netmon.Verdict) (deadline time.Duration, ok bool) {
	if !w.enabled {
		return 0, false
	}
	if v.NMAbsent {
		w.absent = true
		return 0, false
	}
	w.absent = false
	if !w.seen || v.Online != w.online {
		w.begin(now, v.Online)
	}
	at, ev := w.pending()
	if ev == netNone {
		return 0, false
	}
	return max(at.Sub(now), 0), true
}

// begin starts a new online or offline spell.
func (w *netWatch) begin(now time.Time, online bool) {
	w.startupSpell = !w.seen && !online
	w.downSkipped = false
	w.since = now
	w.seen = true
	w.online = online
}

// pending returns the event the current spell leads to and the instant it
// falls due, or netNone when the spell leads to nothing.
func (w *netWatch) pending() (time.Time, netEvent) {
	switch {
	case !w.enabled || !w.seen || w.absent:
		return time.Time{}, netNone
	case !w.online && !w.notifiedDown && !w.downSkipped:
		return w.downAt(), netDown
	case w.online && w.notifiedDown:
		return w.since.Add(w.backAfter), netBack
	}
	return time.Time{}, netNone
}

// downAt returns when the current offline spell falls due. A first verdict
// that is offline waits until the startup window measured from the process
// start has passed, and never less than the plain window from the verdict,
// so NetworkManager coming up late does not fire a down at once.
func (w *netWatch) downAt() time.Time {
	at := w.since.Add(w.downAfter)
	if w.startupSpell {
		at = later(at, w.startup.Add(w.downAfterStartup))
	}
	return at
}

// later returns the later of a and b.
func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// due returns the event to send at now. A timer that fires early or after
// the state moved on gets netNone.
func (w *netWatch) due(now time.Time) netEvent {
	at, ev := w.pending()
	if ev == netNone || now.Before(at) {
		return netNone
	}
	return ev
}

// markSent records that ev was sent with notification id. A sent down
// remembers its id for the back to replace; a sent back clears both, so a
// later outage sends a new down.
func (w *netWatch) markSent(ev netEvent, id uint32) {
	switch ev {
	case netDown:
		w.notifiedDown, w.notifID = true, id
	case netBack:
		w.notifiedDown, w.notifID = false, 0
	case netNone:
	}
}

// skip records that the App did not send ev, because notifications are
// paused or the Send failed. A skipped down marks the current outage as
// handled, so due never offers it again and no back follows it. A skipped
// back forgets the sent down, so the next outage sends a new down, and
// returns that down's id for the App to close: the stale "network down"
// would otherwise stay on screen. Closing is not a notification, so a pause
// still holds.
func (w *netWatch) skip(ev netEvent) (closeID uint32) {
	switch ev {
	case netDown:
		w.downSkipped = true
	case netBack:
		closeID = w.notifID
		w.notifiedDown, w.notifID = false, 0
	case netNone:
	}
	return closeID
}
