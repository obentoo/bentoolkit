package sni

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

// The dbusmenu interface served at menuPath.
const (
	menuIface = "com.canonical.dbusmenu"
	// menuVersion is the dbusmenu protocol version hosts check.
	menuVersion       uint32 = 3
	menuStatus               = "normal"
	menuTextDirection        = "ltr"
)

// Menu ids (design.md). Notice entries take 1..maxNoticeEntries in the order
// of View.Entries.
const (
	menuRootID          int32 = 0
	menuIDMore          int32 = 100
	menuIDCheckNow      int32 = 200
	menuIDMarkAllRead   int32 = 201
	menuIDPauseHour     int32 = 202
	menuIDPauseTomorrow int32 = 203
	menuIDResume        int32 = 204
	menuIDQuit          int32 = 300
	// menuIDSeparator is the first separator's id; the others follow it.
	menuIDSeparator int32 = 900
)

// maxNoticeEntries is how many notices the menu lists (R9.1).
const maxNoticeEntries = 10

// maxTitleRunes bounds a notice label, ellipsis included: titles come from
// the network.
const maxTitleRunes = 80

// eventClicked is the only dbusmenu event that is an action; "hovered",
// "opened" and "closed" are not.
const eventClicked = "clicked"

// dbusmenu property names.
const (
	propLabel           = "label"
	propType            = "type"
	propChildrenDisplay = "children-display"
	typeSeparator       = "separator"
	childrenSubmenu     = "submenu"
)

// errInvalidArgs is the error name for a call naming an unknown menu item.
const errInvalidArgs = "org.freedesktop.DBus.Error.InvalidArgs"

// menuItem is one entry of the menu below the root. A separator has no label.
type menuItem struct {
	id        int32
	label     string
	separator bool
}

// props returns the item's dbusmenu properties, restricted to names unless
// names is empty.
func (m menuItem) props(names []string) map[string]dbus.Variant {
	all := map[string]dbus.Variant{propLabel: dbus.MakeVariant(m.label)}
	if m.separator {
		all = map[string]dbus.Variant{propType: dbus.MakeVariant(typeSeparator)}
	}
	return filterProps(all, names)
}

// rootProps returns the root's dbusmenu properties, restricted to names unless
// names is empty.
func rootProps(names []string) map[string]dbus.Variant {
	return filterProps(map[string]dbus.Variant{propChildrenDisplay: dbus.MakeVariant(childrenSubmenu)}, names)
}

// filterProps keeps the properties named in names; an empty names keeps all.
func filterProps(all map[string]dbus.Variant, names []string) map[string]dbus.Variant {
	if len(names) == 0 {
		return all
	}
	out := make(map[string]dbus.Variant, len(names))
	for _, n := range names {
		if v, ok := all[n]; ok {
			out[n] = v
		}
	}
	return out
}

// layoutNode is a dbusmenu layout node, (ia{sv}av). Every child is a variant
// wrapping another layoutNode: a child that is not wrapped renders an empty
// menu on GNOME.
type layoutNode struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// itemProperties is one element of GetGroupProperties' reply, (ia{sv}).
type itemProperties struct {
	ID    int32
	Props map[string]dbus.Variant
}

// menuEvent is one element of EventGroup's argument, (isvu).
type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// buildMenu maps a View to the menu (R9.1, R9.3, R9.5): the notices, the
// count of those not listed, then Check now and Mark all as read, the pause
// entries or Resume while paused, and Quit last, in groups split by
// separators. Entries beyond maxNoticeEntries are counted with More.
func buildMenu(v View) []menuItem {
	var items []menuItem
	listed := v.Entries[:min(len(v.Entries), maxNoticeEntries)]
	id := int32(1)
	for _, e := range listed {
		items = append(items, menuItem{id: id, label: noticeLabel(e)})
		id++
	}
	if more := max(v.More, 0) + len(v.Entries) - len(listed); more > 0 {
		items = append(items, menuItem{id: menuIDMore, label: fmt.Sprintf(labelMoreFormat, more)})
	}

	sep := menuIDSeparator
	separator := func() {
		items = append(items, menuItem{id: sep, separator: true})
		sep++
	}
	if len(items) > 0 {
		separator()
	}
	items = append(items,
		menuItem{id: menuIDCheckNow, label: labelCheckNow},
		menuItem{id: menuIDMarkAllRead, label: labelMarkAllRead})
	separator()
	if v.Paused {
		items = append(items, menuItem{id: menuIDResume, label: labelResume})
	} else {
		items = append(items,
			menuItem{id: menuIDPauseHour, label: labelPauseHour},
			menuItem{id: menuIDPauseTomorrow, label: labelPauseTomorrow})
	}
	separator()
	return append(items, menuItem{id: menuIDQuit, label: labelQuit})
}

// noticeLabel is a notice's menu label: its title, or its ID when the title
// is blank, cleaned and with every "_" doubled, since dbusmenu reads a single
// "_" as a mnemonic marker.
func noticeLabel(e MenuEntry) string {
	label := cleanTitle(e.Title)
	if label == "" {
		label = cleanTitle(e.NoticeID)
	}
	return strings.ReplaceAll(label, "_", "__")
}

// cleanTitle makes a title from the network safe to send and fit for one menu
// line: invalid UTF-8 and control characters would make godbus refuse the
// whole reply, and a line break or a very long title would break the menu.
func cleanTitle(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxTitleRunes {
		s = string([]rune(s)[:maxTitleRunes-1]) + "\u2026"
	}
	return s
}

// menuSnapshot returns the current menu and its revision.
func (i *Item) menuSnapshot() ([]menuItem, uint32) {
	i.menuMu.Lock()
	defer i.menuMu.Unlock()
	return i.menuItems, i.menuRev
}

// menuChange is what one SetState did to the menu.
type menuChange struct {
	old, cur []menuItem
	rev      uint32
}

// updateMenu replaces the menu with the one v shows. When it differs, the
// revision goes up and changed is true. i.stateMu must be held, so that
// revisions are announced in the order they are made.
func (i *Item) updateMenu(v View) (c menuChange, changed bool) {
	items := buildMenu(v)
	i.menuMu.Lock()
	defer i.menuMu.Unlock()
	c.old = i.menuItems
	if !slices.Equal(items, i.menuItems) {
		i.menuItems = items
		i.menuRev++
		changed = true
	}
	c.cur, c.rev = i.menuItems, i.menuRev
	return c, changed
}

// removedProperties is one element of ItemsPropertiesUpdated's second
// argument, (ias): an item and the properties it no longer has.
type removedProperties struct {
	ID    int32
	Props []string
}

// propertyChanges lists, for every id in both old and cur, the properties
// whose value changed or appeared, and those that went away. Ids in only one
// of the two menus are LayoutUpdated's business.
func propertyChanges(old, cur []menuItem) ([]itemProperties, []removedProperties) {
	updated, removed := []itemProperties{}, []removedProperties{}
	for _, m := range cur {
		prev, ok := findItem(old, m.id)
		if !ok || prev == m {
			continue
		}
		before, after := prev.props(nil), m.props(nil)
		changed := map[string]dbus.Variant{}
		for name, v := range after {
			if b, ok := before[name]; !ok || !reflect.DeepEqual(b.Value(), v.Value()) || b.Signature() != v.Signature() {
				changed[name] = v
			}
		}
		var gone []string
		for name := range before {
			if _, ok := after[name]; !ok {
				gone = append(gone, name)
			}
		}
		if len(changed) > 0 {
			updated = append(updated, itemProperties{ID: m.id, Props: changed})
		}
		if len(gone) > 0 {
			slices.Sort(gone)
			removed = append(removed, removedProperties{ID: m.id, Props: gone})
		}
	}
	return updated, removed
}

// deliver hands a click to Events without blocking: a reader that has
// stopped reading costs the click, not the D-Bus handler.
func (i *Item) deliver(ev Event) {
	select {
	case i.events <- ev:
	default:
		i.log.Warn("dropping a tray menu click: the events channel is full", "item_id", ev.ItemID)
	}
}

// exportMenu exports the dbusmenu object the item's Menu property names.
func (i *Item) exportMenu() error {
	if err := i.conn.Export(menuObject{item: i}, menuPath, menuIface); err != nil {
		return fmt.Errorf("exporting %s at %s: %w", menuIface, menuPath, err)
	}
	props, err := prop.Export(i.conn, menuPath, prop.Map{menuIface: {
		"Version":       {Value: menuVersion, Emit: prop.EmitConst},
		"Status":        {Value: menuStatus, Emit: prop.EmitConst},
		"TextDirection": {Value: menuTextDirection, Emit: prop.EmitConst},
	}})
	if err != nil {
		_ = i.conn.Export(nil, menuPath, menuIface)
		return fmt.Errorf("exporting the properties of %s at %s: %w", menuIface, menuPath, err)
	}

	node := &introspect.Node{
		Name: string(menuPath),
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			menuIntrospection(props),
		},
	}
	if err := i.conn.Export(introspect.NewIntrospectable(node), menuPath, introspect.IntrospectData.Name); err != nil {
		i.unexportMenu()
		return fmt.Errorf("exporting the introspection data of %s: %w", menuPath, err)
	}
	return nil
}

// unexportMenu withdraws everything exportMenu exported.
func (i *Item) unexportMenu() {
	_ = i.conn.Export(nil, menuPath, introspect.IntrospectData.Name)
	_ = i.conn.Export(nil, menuPath, prop.IntrospectData.Name)
	_ = i.conn.Export(nil, menuPath, menuIface)
}

// emitMenuChange announces a menu change: ItemsPropertiesUpdated first, for
// the items that kept their id but not their properties, then LayoutUpdated.
// Hosts such as GNOME's AppIndicator extension fetch the properties of an
// item only when its id is new or ItemsPropertiesUpdated names it: without
// the signal, entry 1 would keep its old title after the notices shift, and
// a click on it would open the notice now in that position.
func (i *Item) emitMenuChange(c menuChange) {
	if updated, removed := propertyChanges(c.old, c.cur); len(updated) > 0 || len(removed) > 0 {
		if err := i.conn.Emit(menuPath, menuIface+".ItemsPropertiesUpdated", updated, removed); err != nil {
			i.log.Warn("emitting a tray menu signal failed", "signal", "ItemsPropertiesUpdated", "revision", c.rev, "error", err)
		}
	}
	if err := i.conn.Emit(menuPath, menuIface+".LayoutUpdated", c.rev, menuRootID); err != nil {
		i.log.Warn("emitting a tray menu signal failed", "signal", "LayoutUpdated", "revision", c.rev, "error", err)
	}
}

// menuIntrospection describes com.canonical.dbusmenu as exported here.
func menuIntrospection(props *prop.Properties) introspect.Interface {
	in := func(name, typ string) introspect.Arg { return introspect.Arg{Name: name, Type: typ, Direction: "in"} }
	out := func(name, typ string) introspect.Arg { return introspect.Arg{Name: name, Type: typ, Direction: "out"} }
	properties := props.Introspection(menuIface)
	slices.SortFunc(properties, func(a, b introspect.Property) int { return strings.Compare(a.Name, b.Name) })
	return introspect.Interface{
		Name: menuIface,
		Methods: []introspect.Method{
			{Name: "AboutToShow", Args: []introspect.Arg{in("id", "i"), out("needUpdate", "b")}},
			{Name: "AboutToShowGroup", Args: []introspect.Arg{
				in("ids", "ai"), out("updatesNeeded", "ai"), out("idErrors", "ai"),
			}},
			{Name: "Event", Args: []introspect.Arg{
				in("id", "i"), in("eventId", "s"), in("data", "v"), in("timestamp", "u"),
			}},
			{Name: "EventGroup", Args: []introspect.Arg{in("events", "a(isvu)"), out("idErrors", "ai")}},
			{Name: "GetGroupProperties", Args: []introspect.Arg{
				in("ids", "ai"), in("propertyNames", "as"), out("properties", "a(ia{sv})"),
			}},
			{Name: "GetLayout", Args: []introspect.Arg{
				in("parentId", "i"), in("recursionDepth", "i"), in("propertyNames", "as"),
				out("revision", "u"), out("layout", "(ia{sv}av)"),
			}},
			{Name: "GetProperty", Args: []introspect.Arg{in("id", "i"), in("name", "s"), out("value", "v")}},
		},
		Signals: []introspect.Signal{
			{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{
				{Name: "updatedProps", Type: "a(ia{sv})"}, {Name: "removedProps", Type: "a(ias)"},
			}},
			{Name: "LayoutUpdated", Args: []introspect.Arg{
				{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"},
			}},
		},
		Properties: properties,
	}
}

// unknownItem is the error for a call naming an id the menu does not have.
func unknownItem(id int32) *dbus.Error {
	return dbus.NewError(errInvalidArgs, []any{fmt.Sprintf("no menu item with id %d", id)})
}

// findItem returns the item with id in items.
func findItem(items []menuItem, id int32) (menuItem, bool) {
	k := slices.IndexFunc(items, func(m menuItem) bool { return m.id == id })
	if k < 0 {
		return menuItem{}, false
	}
	return items[k], true
}

// knownID reports whether id is the root or an item of items.
func knownID(items []menuItem, id int32) bool {
	_, ok := findItem(items, id)
	return ok || id == menuRootID
}

// menuObject carries the dbusmenu D-Bus methods. It is a type of its own so
// that conn.Export does not publish Item's Go API on the bus. godbus runs
// each call in a goroutine of its own; the methods only read a snapshot of
// the menu and never wait on SetState.
type menuObject struct {
	item *Item
}

// GetLayout returns the menu below parentID, down to recursionDepth levels
// (-1 for all, 0 for the node alone), with the current revision.
func (o menuObject) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, layoutNode, *dbus.Error) {
	items, rev := o.item.menuSnapshot()
	if parentID != menuRootID {
		// Entries have no children.
		m, ok := findItem(items, parentID)
		if !ok {
			return 0, layoutNode{}, unknownItem(parentID)
		}
		return rev, layoutNode{ID: m.id, Props: m.props(propertyNames), Children: []dbus.Variant{}}, nil
	}
	root := layoutNode{ID: menuRootID, Props: rootProps(propertyNames), Children: []dbus.Variant{}}
	if recursionDepth != 0 {
		for _, m := range items {
			child := layoutNode{ID: m.id, Props: m.props(propertyNames), Children: []dbus.Variant{}}
			root.Children = append(root.Children, dbus.MakeVariant(child))
		}
	}
	return rev, root, nil
}

// GetGroupProperties returns the properties of the items ids names, or of
// every item when ids is empty. Unknown ids are skipped.
func (o menuObject) GetGroupProperties(ids []int32, propertyNames []string) ([]itemProperties, *dbus.Error) {
	items, _ := o.item.menuSnapshot()
	out := []itemProperties{}
	if len(ids) == 0 {
		out = append(out, itemProperties{ID: menuRootID, Props: rootProps(propertyNames)})
		for _, m := range items {
			out = append(out, itemProperties{ID: m.id, Props: m.props(propertyNames)})
		}
		return out, nil
	}
	for _, id := range ids {
		if id == menuRootID {
			out = append(out, itemProperties{ID: id, Props: rootProps(propertyNames)})
		} else if m, ok := findItem(items, id); ok {
			out = append(out, itemProperties{ID: id, Props: m.props(propertyNames)})
		}
	}
	return out, nil
}

// GetProperty returns one property of one item.
func (o menuObject) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	items, _ := o.item.menuSnapshot()
	var props map[string]dbus.Variant
	if id == menuRootID {
		props = rootProps(nil)
	} else if m, ok := findItem(items, id); ok {
		props = m.props(nil)
	} else {
		return dbus.Variant{}, unknownItem(id)
	}
	v, ok := props[name]
	if !ok {
		return dbus.Variant{}, dbus.NewError(errInvalidArgs, []any{fmt.Sprintf("menu item %d has no property %q", id, name)})
	}
	return v, nil
}

// Event reports an event on item id. Only a click on an entry reaches
// Events.
func (o menuObject) Event(id int32, eventID string, _ dbus.Variant, _ uint32) *dbus.Error {
	if !o.item.handleEvent(id, eventID) {
		return unknownItem(id)
	}
	return nil
}

// EventGroup reports several events and returns the ids it did not know.
func (o menuObject) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	idErrors := []int32{}
	for _, ev := range events {
		if !o.item.handleEvent(ev.ID, ev.EventID) {
			idErrors = append(idErrors, ev.ID)
		}
	}
	return idErrors, nil
}

// AboutToShow tells the host the menu needs no refresh before showing: every
// change is announced with LayoutUpdated.
func (o menuObject) AboutToShow(id int32) (bool, *dbus.Error) {
	items, _ := o.item.menuSnapshot()
	if !knownID(items, id) {
		return false, unknownItem(id)
	}
	return false, nil
}

// AboutToShowGroup is AboutToShow for several ids.
func (o menuObject) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	items, _ := o.item.menuSnapshot()
	idErrors := []int32{}
	for _, id := range ids {
		if !knownID(items, id) {
			idErrors = append(idErrors, id)
		}
	}
	return []int32{}, idErrors, nil
}

// handleEvent delivers a click on an entry of the current menu. It reports
// false when the menu has no item id; events on the root or a separator, and
// events other than a click, are accepted and dropped.
func (i *Item) handleEvent(id int32, eventID string) bool {
	if id == menuRootID {
		return true
	}
	items, _ := i.menuSnapshot()
	m, ok := findItem(items, id)
	if !ok {
		return false
	}
	if eventID == eventClicked && !m.separator {
		i.deliver(Event{ItemID: id})
	}
	return true
}
