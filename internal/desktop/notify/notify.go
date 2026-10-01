// Package notify sends desktop notifications over
// org.freedesktop.Notifications and reports the actions the user takes on
// them (S072-R6).
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// The notification server's well-known name, object path and interface.
const (
	serverName  = "org.freedesktop.Notifications"
	serverPath  = dbus.ObjectPath("/org/freedesktop/Notifications")
	serverIface = "org.freedesktop.Notifications"
)

// Signal members this package listens for on the notification server.
const (
	memberActionInvoked      = "ActionInvoked"
	memberActivationToken    = "ActivationToken"
	memberNotificationClosed = "NotificationClosed"
)

// The bus driver, which reports who owns the server's well-known name.
const (
	busName                = "org.freedesktop.DBus"
	busPath                = dbus.ObjectPath("/org/freedesktop/DBus")
	busIface               = "org.freedesktop.DBus"
	memberNameOwnerChanged = "NameOwnerChanged"
	errNameHasNoOwner      = "org.freedesktop.DBus.Error.NameHasNoOwner"
	nameOwnerChangedName   = busIface + "." + memberNameOwnerChanged
)

// What every notification carries on the wire.
const (
	appName      = "bentoo"
	appIcon      = "bentoo-tray"
	desktopEntry = "bentoo-tray" // R6.7
	// expireTimeout -1 leaves the expiry to the server's default.
	expireTimeout = int32(-1)
)

// defaultCallTimeout bounds a bus call whose context has no deadline. Neither
// dbus-daemon's session configuration nor dbus-broker times out a method call
// a peer never answers; 25 s is libdbus's default reply timeout.
const defaultCallTimeout = 25 * time.Second

// signalBuffer is the capacity of the channel handed to conn.Signal. godbus
// parks each signal that finds the channel full in a goroutine of its own, so
// the channel is buffered and its single reader never blocks on the consumer.
const signalBuffer = 64

// eventBuffer is the capacity of the Events channel. When the consumer falls
// this far behind, further events are dropped with a WARN.
const eventBuffer = 16

// maxParked bounds the signals held for IDs no in-flight Send has recorded
// yet. They are almost always another application's, so the oldest go first.
const maxParked = 64

// Action is one notification action: Key is what the server reports back in
// ActionInvoked, Label is what the user sees. The key "default" is the action
// taken when the notification itself is clicked.
type Action struct {
	Key, Label string
}

// Message is one notification. A summary message has an empty NoticeID.
// Summary and Body are notice-supplied text and are escaped by Send. Urgency
// is the spec's urgency hint: 0 low, 1 normal, 2 critical.
type Message struct {
	NoticeID, Summary, Body string
	Urgency                 byte
	Actions                 []Action
}

// Event is an action the user took on a notification this Notifier sent.
// ActivationToken is the token the server issued for that notification just
// before the action, or empty when it issued none.
type Event struct {
	NoticeID, Action, ActivationToken string
}

// sentNotification is one notification this Notifier sent.
type sentNotification struct {
	noticeID string // "" for a summary
	// seq orders the Notify reply against the connection's signals: an owner
	// change with a later sequence means the server that assigned this ID is
	// gone.
	seq dbus.Sequence
}

// serverSignal is a well-formed signal from the notification server.
type serverSignal struct {
	member string
	id     uint32
	sig    *dbus.Signal
}

// Notifier sends notifications on one connection and reports the actions
// taken on them. It is safe for concurrent use; concurrent Sends do not wait
// for one another.
//
// The ID a signal names is recorded by Send only after the Notify reply
// reaches it, and godbus hands that reply to Send and a signal right behind
// it to the reader concurrently. So while a Send is in flight, a signal from
// the server for an unknown ID is parked rather than dropped; the Send that
// records the ID replays the parked signals that name it, and once no
// Send is in flight the rest are known to be foreign and are discarded. No
// lock is ever held across a bus call.
type Notifier struct {
	conn   *dbus.Conn
	log    *slog.Logger
	events chan Event

	// mu guards the fields below. It is held only for map and slice work.
	mu sync.Mutex
	// owner is the unique name of the connection owning serverName ("" when
	// none does); only signals from it are believed. ownerSeq is the sequence
	// of the message that reported it, so an owner change delivered out of
	// order cannot overwrite a newer one.
	owner    string
	ownerSeq dbus.Sequence
	// sent maps a notification ID the server assigned to what was sent.
	// Signals are broadcast: an ID not in this map belongs to another
	// application.
	sent map[uint32]sentNotification
	// tokens holds an ActivationToken by notification ID until the
	// ActionInvoked it precedes.
	tokens map[uint32]string
	// inflight counts the Notify calls not yet answered and recorded; parked
	// holds, in arrival order, the server's signals for unknown IDs that
	// arrived while inflight was non-zero.
	inflight int
	parked   []serverSignal
	// dropped counts the events lost to a full Events channel.
	dropped uint64
	// lost holds the events dropped while n.mu was held; unlockAndReport
	// logs them after unlocking, so a blocked log writer never stalls the
	// reader or a Send.
	lost []Event
}

// matchRules returns the match rules New adds: the three signals of the
// notification server's object, and the changes of owner of its well-known
// name. The server's sender is the well-known name, which the bus resolves to
// whichever connection owns it when a signal is sent, so a server that starts
// after New is still heard.
func matchRules() [][]dbus.MatchOption {
	members := []string{memberActionInvoked, memberActivationToken, memberNotificationClosed}
	rules := make([][]dbus.MatchOption, 0, len(members)+1)
	for _, m := range members {
		rules = append(rules, []dbus.MatchOption{
			dbus.WithMatchSender(serverName),
			dbus.WithMatchObjectPath(serverPath),
			dbus.WithMatchInterface(serverIface),
			dbus.WithMatchMember(m),
		})
	}
	return append(rules, []dbus.MatchOption{
		dbus.WithMatchSender(busName),
		dbus.WithMatchObjectPath(busPath),
		dbus.WithMatchInterface(busIface),
		dbus.WithMatchMember(memberNameOwnerChanged),
		dbus.WithMatchArg(0, serverName),
	})
}

// New subscribes to the notification server's signals on conn and starts
// the reader that turns them into Events. No server needs to be on the bus:
// one that appears later is heard. conn must stay open for as long as the
// Notifier is used (some servers withdraw an application's notifications when
// its connection goes away); New never closes it. A nil log discards.
func New(conn *dbus.Conn, log *slog.Logger) (*Notifier, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rules := matchRules()
	for i, rule := range rules {
		if err := conn.AddMatchSignal(rule...); err != nil {
			removeMatches(conn, rules[:i])
			return nil, fmt.Errorf("subscribing to %s signals: %w", serverIface, err)
		}
	}
	n := &Notifier{
		conn:   conn,
		log:    log,
		events: make(chan Event, eventBuffer),
		sent:   make(map[uint32]sentNotification),
		tokens: make(map[uint32]string),
	}
	// The channel is registered before the owner is asked for, so no change
	// of owner after the answer can be missed; one before it is older than
	// the answer and is discarded by its sequence.
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	if err := n.loadOwner(); err != nil {
		conn.RemoveSignal(signals)
		removeMatches(conn, rules)
		return nil, err
	}
	go n.read(signals)
	return n, nil
}

// removeMatches undoes AddMatchSignal for rules, best effort: it runs only on
// a path that already returns an error.
func removeMatches(conn *dbus.Conn, rules [][]dbus.MatchOption) {
	for _, rule := range rules {
		_ = conn.RemoveMatchSignal(rule...)
	}
}

// loadOwner records the current owner of serverName. No owner is not an
// error: the server may start later.
func (n *Notifier) loadOwner() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCallTimeout)
	defer cancel()
	call := n.conn.BusObject().CallWithContext(ctx, busIface+".GetNameOwner", 0, serverName)
	var owner string
	if err := call.Store(&owner); err != nil {
		var dbusErr dbus.Error
		if !errors.As(err, &dbusErr) || dbusErr.Name != errNameHasNoOwner {
			return fmt.Errorf("looking up the owner of %s: %w", serverName, err)
		}
		owner = ""
	}
	n.mu.Lock()
	n.owner, n.ownerSeq = owner, call.ResponseSequence
	n.mu.Unlock()
	return nil
}

// Events reports the actions taken on this Notifier's notifications. The
// channel is never closed; it simply goes quiet when the connection ends.
func (n *Notifier) Events() <-chan Event {
	return n.events
}

// Send shows m and returns the ID the server assigned to it. Summary and body
// are escaped (R6.6); m.Actions are sent in order as key/label pairs (R6.5).
// Errors name the notice.
//
// ctx should carry a deadline: a server that accepts the call and never
// answers is otherwise waited for up to 25 s. A slow or hung server delays
// only this Send, never another Send or the delivery of Events.
func (n *Notifier) Send(ctx context.Context, m Message) (uint32, error) {
	actions := make([]string, 0, 2*len(m.Actions))
	for _, a := range m.Actions {
		actions = append(actions, a.Key, a.Label)
	}
	hints := map[string]dbus.Variant{
		"urgency":       dbus.MakeVariant(m.Urgency),
		"desktop-entry": dbus.MakeVariant(desktopEntry),
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}

	n.mu.Lock()
	n.inflight++
	n.mu.Unlock()

	call := n.conn.Object(serverName, serverPath).CallWithContext(ctx, serverIface+".Notify", 0,
		appName, uint32(0), appIcon, Escape(m.Summary), Escape(m.Body), actions, hints, expireTimeout,
	)
	var id uint32
	err := call.Store(&id)

	n.mu.Lock()
	defer n.unlockAndReport()
	n.inflight--
	if err == nil {
		n.recordLocked(id, sentNotification{noticeID: m.NoticeID, seq: call.ResponseSequence})
	}
	if n.inflight == 0 {
		// Every reply that preceded a parked signal has been recorded and
		// has replayed its own: what is left is another application's.
		n.parked = nil
	}
	if err != nil {
		if m.NoticeID == "" {
			return 0, fmt.Errorf("sending the summary notification: %w", err)
		}
		return 0, fmt.Errorf("sending the notification for notice %q: %w", m.NoticeID, err)
	}
	return id, nil
}

// recordLocked records a notification the server just accepted and replays
// the parked signals that name it. n.mu must be held.
func (n *Notifier) recordLocked(id uint32, s sentNotification) {
	// A restarted server hands out IDs again: a token buffered for an older
	// notification must not reach this one.
	if s.seq <= n.ownerSeq {
		// The server that assigned id lost its name after replying, and the
		// change has already been applied: the notification died with it.
		// handleOwnerChanged already forgot every older entry, so whatever
		// n.sent or n.tokens now holds for id belongs to the new server and
		// must be left alone.
		return
	}
	delete(n.tokens, id)
	n.sent[id] = s

	kept := n.parked[:0]
	for _, p := range n.parked {
		if p.id != id {
			kept = append(kept, p)
			continue
		}
		// A server may signal before its reply leaves (a notification closed
		// at once, say), so a signal older than the reply still counts, as
		// long as it came from the current owner: one older than the last
		// owner change belonged to the server that left.
		if p.sig.Sequence > n.ownerSeq {
			n.applyLocked(p)
		}
	}
	n.parked = kept
}

// read is the single reader of the connection's signal channel. It exits when
// godbus closes the channel or the connection's context ends (on a connection
// already closed, conn.Signal registers nothing and the channel never closes).
func (n *Notifier) read(signals <-chan *dbus.Signal) {
	done := n.conn.Context().Done()
	for {
		select {
		case sig, ok := <-signals:
			if !ok {
				return
			}
			n.handle(sig)
		case <-done:
			return
		}
	}
}

// handle applies one signal to the Notifier's state. godbus delivers every
// signal the connection receives to every channel, so anything that is not
// one of the signals this package subscribed to, carries a malformed body,
// comes from a connection that does not own serverName, or names a
// notification this Notifier did not send is ignored.
func (n *Notifier) handle(sig *dbus.Signal) {
	if sig == nil {
		return
	}
	if sig.Name == nameOwnerChangedName {
		n.handleOwnerChanged(sig)
		return
	}
	p, ok := n.parseServerSignal(sig)
	if !ok {
		return
	}

	n.mu.Lock()
	defer n.unlockAndReport()
	if n.owner == "" || sig.Sender != n.owner {
		return
	}
	if _, ours := n.sent[p.id]; ours {
		n.applyLocked(p)
		return
	}
	if n.inflight > 0 {
		// Possibly the ID of a Send whose reply arrived just before this
		// signal and is not recorded yet.
		if len(n.parked) == maxParked {
			n.parked = n.parked[1:]
		}
		n.parked = append(n.parked, p)
	}
}

// parseServerSignal returns sig as a notification server signal when it is
// one of the three this package handles and its body is well-formed.
func (n *Notifier) parseServerSignal(sig *dbus.Signal) (serverSignal, bool) {
	if sig.Path != serverPath {
		return serverSignal{}, false
	}
	member, ok := strings.CutPrefix(sig.Name, serverIface+".")
	if !ok {
		return serverSignal{}, false
	}
	switch member {
	case memberActionInvoked, memberActivationToken, memberNotificationClosed:
	default:
		return serverSignal{}, false
	}
	if len(sig.Body) < 2 {
		n.log.Debug("malformed notification signal ignored", "member", member, "args", len(sig.Body))
		return serverSignal{}, false
	}
	id, ok := sig.Body[0].(uint32)
	if !ok {
		n.log.Debug("malformed notification signal ignored", "member", member, "arg0_type", fmt.Sprintf("%T", sig.Body[0]))
		return serverSignal{}, false
	}
	return serverSignal{member: member, id: id, sig: sig}, true
}

// applyLocked applies a signal for a notification in n.sent. n.mu must be
// held.
func (n *Notifier) applyLocked(p serverSignal) {
	s, ours := n.sent[p.id]
	if !ours {
		return
	}
	switch p.member {
	case memberActivationToken:
		if token, ok := p.sig.Body[1].(string); ok {
			n.tokens[p.id] = token
		}
	case memberActionInvoked:
		key, ok := p.sig.Body[1].(string)
		if !ok {
			return
		}
		token := n.tokens[p.id]
		delete(n.tokens, p.id)
		n.emitLocked(Event{NoticeID: s.noticeID, Action: key, ActivationToken: token})
	case memberNotificationClosed:
		delete(n.sent, p.id)
		delete(n.tokens, p.id)
	}
}

// emitLocked hands ev to the consumer without ever blocking: when the Events
// channel is full the event is dropped and logged at WARN once n.mu is
// released (see unlockAndReport). n.mu must be held.
func (n *Notifier) emitLocked(ev Event) {
	select {
	case n.events <- ev:
	default:
		n.dropped++
		n.lost = append(n.lost, ev)
	}
}

// unlockAndReport releases n.mu, then logs the events emitLocked dropped
// while it was held.
func (n *Notifier) unlockAndReport() {
	lost, total := n.lost, n.dropped
	n.lost = nil
	n.mu.Unlock()
	for _, ev := range lost {
		n.log.Warn("notification event dropped: events channel full",
			"notice", ev.NoticeID, "action", ev.Action, "dropped_total", total)
	}
}

// handleOwnerChanged follows serverName to its new owner. The notifications
// the old owner assigned die with it, without a NotificationClosed, and a new
// server numbers its own from the start again: every ID recorded from a reply
// older than the change is forgotten, so the new server's IDs are never taken
// for ours. A change older than the owner already known is stale and ignored.
func (n *Notifier) handleOwnerChanged(sig *dbus.Signal) {
	if sig.Sender != busName || sig.Path != busPath || len(sig.Body) < 3 {
		return
	}
	name, ok := sig.Body[0].(string)
	if !ok || name != serverName {
		return // another component's subscription on the same connection
	}
	newOwner, ok := sig.Body[2].(string)
	if !ok {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if sig.Sequence <= n.ownerSeq {
		return
	}
	n.owner, n.ownerSeq = newOwner, sig.Sequence
	for id, s := range n.sent {
		if s.seq < sig.Sequence {
			delete(n.sent, id)
			delete(n.tokens, id)
		}
	}
}

// markupEscaper replaces the three characters the notification markup treats
// specially. Quotes are left alone: they are only special inside attribute
// values, which notice text never produces.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Escape makes notice-supplied text safe for a notification body or summary
// (R6.6): & becomes &amp;, < becomes &lt;, > becomes &gt;. Text that already
// looks like an entity is escaped again, because it is text, not markup.
func Escape(s string) string {
	return markupEscaper.Replace(s)
}
