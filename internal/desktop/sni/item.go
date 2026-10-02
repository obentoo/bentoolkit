package sni

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"

	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// The item's own object and the StatusNotifierWatcher it registers with.
const (
	itemPath     = dbus.ObjectPath("/StatusNotifierItem")
	itemIface    = "org.kde.StatusNotifierItem"
	menuPath     = dbus.ObjectPath("/MenuBar")
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = dbus.ObjectPath("/StatusNotifierWatcher")
	watcherIface = "org.kde.StatusNotifierWatcher"
)

// The bus driver, which reports who owns the watcher's well-known name.
const (
	busName              = "org.freedesktop.DBus"
	busPath              = dbus.ObjectPath("/org/freedesktop/DBus")
	busIface             = "org.freedesktop.DBus"
	nameOwnerChanged     = "NameOwnerChanged"
	nameOwnerChangedName = busIface + "." + nameOwnerChanged
)

// The item's fixed properties (R8.1).
const (
	itemID       = "bentoo-tray"
	itemTitle    = "bentoo"
	itemCategory = "ApplicationStatus"
)

// Item status values from the StatusNotifierItem specification.
const (
	statusActive         = "Active"
	statusNeedsAttention = "NeedsAttention"
)

// appIndicatorPackage is what a GNOME user installs to get a watcher (R8.2).
const appIndicatorPackage = "gnome-shell-extension-appindicator"

// defaultCallTimeout bounds a bus call whose context has no deadline. Neither
// dbus-daemon nor dbus-broker times out a call a peer never answers; 25 s is
// libdbus's default reply timeout.
const defaultCallTimeout = 25 * time.Second

// registerBudget is how long one registration may take, retries included: a
// watcher that has just taken its name may not have exported its object yet
// (R8.3 allows 5 s).
const registerBudget = 5 * time.Second

// registerRetry is the pause between two registration attempts.
const registerRetry = 200 * time.Millisecond

// cleanupTimeout bounds the best-effort removal of the match rule once
// tracking stops.
const cleanupTimeout = 2 * time.Second

// signalBuffer is the capacity of the channel handed to conn.Signal. godbus
// parks each signal that finds the channel full in a goroutine of its own, so
// the channel is buffered and its single reader never blocks.
const signalBuffer = 64

// eventBuffer is the capacity of the Events channel.
const eventBuffer = 16

// IconSet holds the three icon variants, each at several sizes.
type IconSet struct {
	Plain, Unread, Critical []Pixmap
}

// MenuEntry is one notice in the menu.
type MenuEntry struct {
	NoticeID, Title string
}

// View is what the tray shows. Critical means at least one unread notice is
// critical; it has no effect when Unread is zero. Paused, Entries and More
// feed the menu.
type View struct {
	Unread   int
	Critical bool
	Paused   bool
	Entries  []MenuEntry
	More     int
}

// Event is a click on the menu item ItemID. NoticeID is the notice that item
// lists at click time; it is empty for the fixed entries and for an id whose
// notice is no longer listed (R9.7).
type Event struct {
	ItemID   int32
	NoticeID string
}

// toolTip is the SNI ToolTip property, (sa(iiay)ss): icon name, icon pixmap,
// title, description.
type toolTip struct {
	IconName    string
	IconPixmap  []Pixmap
	Title       string
	Description string
}

// variant names one of the three icon variants.
type variant int

const (
	variantPlain variant = iota
	variantUnread
	variantCritical
)

// shown is what the item's mutable properties hold.
type shown struct {
	variant variant
	status  string
	tooltip string
}

// render maps a View to what the item shows (R8.4, R8.5, R8.7, R8.1).
func render(v View) shown {
	unread := max(v.Unread, 0)
	s := shown{variant: variantPlain, status: statusActive}
	switch {
	case unread == 0:
	case v.Critical:
		s.variant, s.status = variantCritical, statusNeedsAttention
	default:
		s.variant, s.status = variantUnread, statusNeedsAttention
	}
	s.tooltip = messages.Count(messages.TooltipOne, messages.TooltipMany, unread)
	return s
}

// Item is a StatusNotifierItem on one connection. It is safe for concurrent
// use. No lock is ever held across a method call on the bus: a hung watcher
// stalls only the registration in flight, never SetState or the signal
// reader.
type Item struct {
	conn   *dbus.Conn
	log    *slog.Logger
	icons  IconSet
	events chan Event
	// kick asks the registrar for a registration; it holds at most one
	// pending request.
	kick chan struct{}

	// stateMu serializes SetState and guards cur and props. It is held across
	// conn.Emit, which sends a signal and waits for no reply.
	stateMu sync.Mutex
	cur     shown
	// props is nil until Start exports the item.
	props *prop.Properties

	// menuMu guards menuItems, menuRev, noticeIDs and menuServed. It is held
	// only for field access and building the menu, so the menu's D-Bus
	// handlers never wait on SetState. menuItems is replaced, never modified
	// in place. menuServed is what each id last served, kept for the process
	// lifetime like noticeIDs.
	menuMu     sync.Mutex
	menuItems  []menuItem
	menuRev    uint32
	noticeIDs  *noticeIDs
	menuServed servedItems

	// mu guards the fields below. It is held only for field access.
	mu      sync.Mutex
	started bool
	// watcherSeq is the sequence of the message that last reported whether
	// the watcher is on the bus, so an older report cannot overwrite it.
	watcherSeq     dbus.Sequence
	watcherPresent bool
	registered     bool
	warned         bool
}

// New prepares an item showing the plain icon. Nothing is exported until
// Start. conn must stay open while the item is used; New never closes it. A
// nil log discards. The pixmaps' Data is shared, not copied, and must not be
// modified afterwards.
func New(conn *dbus.Conn, log *slog.Logger, icons IconSet) *Item {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ids := newNoticeIDs()
	initial := buildMenu(View{}, ids)
	served := servedItems{}
	served.serve(initial)
	return &Item{
		conn: conn,
		log:  log,
		icons: IconSet{
			Plain:    slices.Clone(icons.Plain),
			Unread:   slices.Clone(icons.Unread),
			Critical: slices.Clone(icons.Critical),
		},
		events: make(chan Event, eventBuffer),
		kick:   make(chan struct{}, 1),
		cur:    render(View{}),
		// Revision 1 is the initial menu; hosts read it with GetLayout.
		menuItems:  initial,
		menuRev:    1,
		noticeIDs:  ids,
		menuServed: served,
	}
}

// pixmaps returns the icon of variant v.
func (i *Item) pixmaps(v variant) []Pixmap {
	switch v {
	case variantUnread:
		return i.icons.Unread
	case variantCritical:
		return i.icons.Critical
	default:
		return i.icons.Plain
	}
}

// Start exports /StatusNotifierItem, registers it with the
// StatusNotifierWatcher when one is on the bus (R8.1) or logs the R8.2 WARN
// once when none is, and returns. Tracking the watcher continues in the
// background until ctx ends or the connection closes: whenever a watcher
// takes the name, the item registers with it again within 5 s (R8.3).
//
// Bus calls made by Start are bounded by ctx's deadline, or by 25 s when it
// has none. Start may be called once.
func (i *Item) Start(ctx context.Context) error {
	names := i.conn.Names()
	if len(names) == 0 {
		return errors.New("starting the tray item: the connection has no unique name")
	}
	i.mu.Lock()
	already := i.started
	i.started = true
	i.mu.Unlock()
	if already {
		return errors.New("starting the tray item: already started")
	}

	var undo []func()
	rollback := func() {
		for _, f := range slices.Backward(undo) {
			f()
		}
	}
	if err := i.export(); err != nil {
		return err
	}
	undo = append(undo, i.unexport)
	// The menu goes up next to the item that names it in its Menu property.
	if err := i.exportMenu(); err != nil {
		rollback()
		return err
	}
	undo = append(undo, i.unexportMenu)

	signals := make(chan *dbus.Signal, signalBuffer)
	i.conn.Signal(signals)
	undo = append(undo, func() { i.conn.RemoveSignal(signals) })

	callCtx, cancel := withCallTimeout(ctx)
	defer cancel()
	// The rule is added and the channel registered before the owner is asked
	// for, so a watcher that appears after the answer is never missed; a
	// change older than the answer is discarded by its sequence.
	if err := i.conn.AddMatchSignalContext(callCtx, watcherRule()...); err != nil {
		rollback()
		return fmt.Errorf("subscribing to owner changes of %s: %w", watcherName, err)
	}
	undo = append(undo, func() { i.removeMatch(ctx) })

	present, seq, err := i.watcherHasOwner(callCtx)
	if err != nil {
		rollback()
		return err
	}
	i.mu.Lock()
	i.watcherSeq, i.watcherPresent = seq, present
	warn := !present && !i.warned
	if warn {
		i.warned = true
	}
	i.mu.Unlock()

	if present {
		i.requestRegistration()
	} else if warn {
		i.warnNoWatcher()
	}
	go i.track(ctx, signals)
	go i.registrar(ctx)
	return nil
}

// warnNoWatcher logs the R8.2 WARN. Callers make sure it runs once.
func (i *Item) warnNoWatcher() {
	i.log.Warn("no StatusNotifierWatcher on the session bus: running with notifications only; on GNOME, install "+
		appIndicatorPackage+" to show the tray icon",
		"watcher", watcherName)
}

// watcherRule is the match rule for owner changes of the watcher's name.
func watcherRule() []dbus.MatchOption {
	return []dbus.MatchOption{
		dbus.WithMatchSender(busName),
		dbus.WithMatchObjectPath(busPath),
		dbus.WithMatchInterface(busIface),
		dbus.WithMatchMember(nameOwnerChanged),
		dbus.WithMatchArg(0, watcherName),
	}
}

// removeMatch drops the match rule, best effort: tracking has stopped and a
// failure leaves only an unused rule behind.
func (i *Item) removeMatch(ctx context.Context) {
	if i.conn.Context().Err() != nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := i.conn.RemoveMatchSignalContext(cleanupCtx, watcherRule()...); err != nil {
		i.log.Debug("removing the watcher match rule failed", "watcher", watcherName, "error", err)
	}
}

// watcherHasOwner asks the bus whether a watcher is present, and returns the
// sequence of the answer.
func (i *Item) watcherHasOwner(ctx context.Context) (bool, dbus.Sequence, error) {
	call := i.conn.BusObject().CallWithContext(ctx, busIface+".NameHasOwner", 0, watcherName)
	var present bool
	if err := call.Store(&present); err != nil {
		return false, 0, fmt.Errorf("asking whether %s is on the bus: %w", watcherName, err)
	}
	return present, call.ResponseSequence, nil
}

// withCallTimeout returns ctx bounded by defaultCallTimeout when it has no
// deadline of its own.
func withCallTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, defaultCallTimeout)
}

// export exports the item's methods, properties and introspection data.
func (i *Item) export() error {
	if err := i.conn.Export(itemObject{}, itemPath, itemIface); err != nil {
		return fmt.Errorf("exporting %s at %s: %w", itemIface, itemPath, err)
	}

	i.stateMu.Lock()
	props, err := prop.Export(i.conn, itemPath, i.propMap())
	if err == nil {
		i.props = props
	}
	i.stateMu.Unlock()
	if err != nil {
		_ = i.conn.Export(nil, itemPath, itemIface)
		return fmt.Errorf("exporting the properties of %s at %s: %w", itemIface, itemPath, err)
	}

	node := &introspect.Node{
		Name: string(itemPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			itemIntrospection(props),
		},
	}
	if err := i.conn.Export(introspect.NewIntrospectable(node), itemPath, introspect.IntrospectData.Name); err != nil {
		i.unexport()
		return fmt.Errorf("exporting the introspection data of %s: %w", itemPath, err)
	}
	return nil
}

// unexport withdraws everything export exported.
func (i *Item) unexport() {
	_ = i.conn.Export(nil, itemPath, introspect.IntrospectData.Name)
	_ = i.conn.Export(nil, itemPath, prop.IntrospectData.Name)
	_ = i.conn.Export(nil, itemPath, itemIface)
	i.stateMu.Lock()
	i.props = nil
	i.stateMu.Unlock()
}

// propMap is the item's property table, built from i.cur. i.stateMu must be
// held. Properties with a dedicated SNI change signal never emit
// PropertiesChanged: hosts follow NewIcon, NewStatus and the rest instead.
// The others never change.
func (i *Item) propMap() prop.Map {
	px := slices.Clone(i.pixmaps(i.cur.variant))
	return prop.Map{itemIface: {
		"Category":            {Value: itemCategory, Emit: prop.EmitConst},
		"Id":                  {Value: itemID, Emit: prop.EmitConst},
		"Title":               {Value: itemTitle, Emit: prop.EmitFalse},
		"Status":              {Value: i.cur.status, Emit: prop.EmitFalse},
		"IconName":            {Value: "", Emit: prop.EmitConst},
		"IconPixmap":          {Value: px, Emit: prop.EmitFalse},
		"AttentionIconPixmap": {Value: slices.Clone(px), Emit: prop.EmitFalse},
		"ToolTip":             {Value: toolTip{Title: i.cur.tooltip}, Emit: prop.EmitFalse},
		"ItemIsMenu":          {Value: true, Emit: prop.EmitConst},
		"Menu":                {Value: menuPath, Emit: prop.EmitConst},
	}}
}

// itemIntrospection describes org.kde.StatusNotifierItem as exported here.
func itemIntrospection(props *prop.Properties) introspect.Interface {
	xy := []introspect.Arg{{Name: "x", Type: "i", Direction: "in"}, {Name: "y", Type: "i", Direction: "in"}}
	properties := props.Introspection(itemIface)
	slices.SortFunc(properties, func(a, b introspect.Property) int { return strings.Compare(a.Name, b.Name) })
	return introspect.Interface{
		Name: itemIface,
		Methods: []introspect.Method{
			{Name: "Activate", Args: xy},
			{Name: "ContextMenu", Args: xy},
			{Name: "Scroll", Args: []introspect.Arg{
				{Name: "delta", Type: "i", Direction: "in"},
				{Name: "orientation", Type: "s", Direction: "in"},
			}},
			{Name: "SecondaryActivate", Args: xy},
		},
		Signals: []introspect.Signal{
			{Name: "NewAttentionIcon"},
			{Name: "NewIcon"},
			{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}},
			{Name: "NewTitle"},
			{Name: "NewToolTip"},
		},
		Properties: properties,
	}
}

// itemObject carries the item's D-Bus methods. It is a type of its own so
// that conn.Export, which exports every exported method of the value, does
// not publish Item's Go API on the bus. With ItemIsMenu set, hosts show the
// menu on a click, so the methods do nothing.
type itemObject struct{}

// Activate is a primary click on the icon.
func (itemObject) Activate(_, _ int32) *dbus.Error { return nil }

// SecondaryActivate is a middle click on the icon.
func (itemObject) SecondaryActivate(_, _ int32) *dbus.Error { return nil }

// ContextMenu asks the item to show its menu itself.
func (itemObject) ContextMenu(_, _ int32) *dbus.Error { return nil }

// Scroll is a scroll over the icon.
func (itemObject) Scroll(_ int32, _ string) *dbus.Error { return nil }

// SetState shows v: the icon variant and status (R8.4, R8.5, R8.7), the
// unread count in the tooltip (R8.1) and the menu (R9.1, R9.3, R9.5). Before
// Start it only records v. It emits NewIcon and NewAttentionIcon when the
// variant changes, NewStatus when the status does, NewToolTip when the
// tooltip does, and ItemsPropertiesUpdated for the entries whose label
// differs from what their id last served followed by LayoutUpdated, with a
// higher revision, when the menu does.
func (i *Item) SetState(v View) {
	next := render(v)

	i.stateMu.Lock()
	defer i.stateMu.Unlock()
	prev := i.cur
	i.cur = next
	change, menuChanged := i.updateMenu(v)
	if i.props == nil {
		return
	}

	var signals []string
	if next.variant != prev.variant {
		px := i.pixmaps(next.variant)
		// Both pixmaps carry the variant: hosts show AttentionIconPixmap
		// while the status is NeedsAttention, and those that ignore it still
		// see the dot on IconPixmap.
		setPixmaps(i.props, "IconPixmap", px)
		setPixmaps(i.props, "AttentionIconPixmap", px)
		signals = append(signals, "NewIcon", "NewAttentionIcon")
	}
	if next.status != prev.status {
		i.props.SetMust(itemIface, "Status", next.status)
	}
	if next.tooltip != prev.tooltip {
		i.props.SetMust(itemIface, "ToolTip", toolTip{Title: next.tooltip})
		signals = append(signals, "NewToolTip")
	}

	for _, name := range signals {
		i.emit(name)
	}
	if next.status != prev.status {
		i.emit("NewStatus", next.status)
	}
	if menuChanged {
		i.emitMenuChange(change)
	}
}

// setPixmaps replaces a pixmap property. dbus.Store, behind SetMust, writes a
// slice into the destination's backing array whenever that array is long
// enough, and prop's Get hands the same array to the reply encoder after
// releasing its lock: storing in place would race with a host reading the
// property. Emptying the property first makes the second store allocate a
// new array.
func setPixmaps(props *prop.Properties, name string, px []Pixmap) {
	props.SetMust(itemIface, name, []Pixmap{})
	props.SetMust(itemIface, name, px)
}

// emit sends one of the item's change signals.
func (i *Item) emit(member string, args ...any) {
	if err := i.conn.Emit(itemPath, itemIface+"."+member, args...); err != nil {
		i.log.Warn("emitting a tray item signal failed", "signal", member, "error", err)
	}
}

// Events reports clicks on the item's menu. The channel is never closed.
func (i *Item) Events() <-chan Event {
	return i.events
}

// Available reports whether the item is registered with a watcher that is
// still on the bus.
func (i *Item) Available() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.registered && i.watcherPresent
}

// track is the single reader of the connection's signal channel. It exits
// when ctx ends, when godbus closes the channel, or when the connection's
// context ends (on a connection already closed, conn.Signal registers
// nothing and the channel never closes).
func (i *Item) track(ctx context.Context, signals chan *dbus.Signal) {
	done := i.conn.Context().Done()
	for {
		select {
		case sig, ok := <-signals:
			if !ok {
				return
			}
			i.handle(sig)
		case <-done:
			return
		case <-ctx.Done():
			i.conn.RemoveSignal(signals)
			i.removeMatch(ctx)
			return
		}
	}
}

// handle follows the watcher's name from one NameOwnerChanged. godbus hands
// every signal the connection receives to every channel, so anything else
// (NameAcquired, NameLost, another component's signals, a malformed body) is
// ignored. A change older than the last one applied is stale.
func (i *Item) handle(sig *dbus.Signal) {
	if sig == nil || sig.Sender != busName || sig.Path != busPath || sig.Name != nameOwnerChangedName || len(sig.Body) < 3 {
		return
	}
	name, ok := sig.Body[0].(string)
	if !ok || name != watcherName {
		return
	}
	newOwner, ok := sig.Body[2].(string)
	if !ok {
		return
	}

	i.mu.Lock()
	if sig.Sequence <= i.watcherSeq {
		i.mu.Unlock()
		return
	}
	i.watcherSeq = sig.Sequence
	i.watcherPresent = newOwner != ""
	if newOwner == "" {
		i.registered = false
	}
	i.mu.Unlock()

	if newOwner == "" {
		i.log.Info("the StatusNotifierWatcher left the session bus; the tray icon is hidden until it returns",
			"watcher", watcherName)
		return
	}
	i.log.Info("a StatusNotifierWatcher appeared on the session bus; registering the tray item",
		"watcher", watcherName, "owner", newOwner)
	i.requestRegistration()
}

// requestRegistration asks the registrar for a registration; a request
// already pending covers this one.
func (i *Item) requestRegistration() {
	select {
	case i.kick <- struct{}{}:
	default:
	}
}

// registrar runs the registrations handle and Start request, one at a time,
// off the signal reader: a slow watcher never delays the reading of signals.
func (i *Item) registrar(ctx context.Context) {
	done := i.conn.Context().Done()
	for {
		select {
		case <-i.kick:
			i.register(ctx)
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// register registers the item with the watcher, retrying for up to
// registerBudget (R8.3). It gives up early when the watcher leaves.
func (i *Item) register(ctx context.Context) {
	names := i.conn.Names()
	if len(names) == 0 {
		return
	}
	service := names[0]
	regCtx, cancel := context.WithTimeout(ctx, registerBudget)
	defer cancel()

	var err error
	for attempt := 1; ; attempt++ {
		i.mu.Lock()
		present := i.watcherPresent
		i.mu.Unlock()
		if !present {
			return
		}

		err = i.conn.Object(watcherName, watcherPath).CallWithContext(regCtx,
			watcherIface+".RegisterStatusNotifierItem", 0, service).Err
		if err == nil {
			i.mu.Lock()
			i.registered = i.watcherPresent
			i.mu.Unlock()
			i.log.Info("registered the tray item with the StatusNotifierWatcher",
				"watcher", watcherName, "service", service, "attempts", attempt)
			return
		}

		timer := time.NewTimer(registerRetry)
		select {
		case <-timer.C:
		case <-regCtx.Done():
			timer.Stop()
			if ctx.Err() == nil {
				i.log.Warn("registering the tray item with the StatusNotifierWatcher failed; retrying when a watcher next appears",
					"watcher", watcherName, "service", service, "attempts", attempt, "error", err)
			}
			return
		}
	}
}
