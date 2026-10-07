//go:build linux

package tray

// StatusNotifierItem + com.canonical.dbusmenu over the client in dbus_linux.go.
// This is what GNOME (with the AppIndicator extension), KDE Plasma, XFCE and
// the other freedesktop panels display as a tray icon.

import (
	"os"
	"os/exec"
	"strconv"
	"sync"

	"github.com/tyclab/tycswap/internal/brand"
)

const (
	sniPath     = "/StatusNotifierItem"
	sniIface    = "org.kde.StatusNotifierItem"
	menuPath    = "/MenuBar"
	menuIface   = "com.canonical.dbusmenu"
	propsIface  = "org.freedesktop.DBus.Properties"
	introIface  = "org.freedesktop.DBus.Introspectable"
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = "/StatusNotifierWatcher"
)

type sniTray struct {
	opts Options
	conn *dbusConn

	mu       sync.Mutex
	icon     Icon // IconPixmap: New's icon until SetIcon
	menu     menuModel
	nodes    []dbusmenuNode // the dbusmenu tree of menu, rebuilt when it changes
	title    string
	tooltip  string
	revision uint32
	done     chan struct{}
	once     sync.Once
}

func newSNITray(icon Icon, opts Options) (Tray, error) {
	if len(icon.ARGB32) == 0 {
		return nil, ErrUnsupported
	}
	t := &sniTray{icon: icon, opts: opts, tooltip: opts.Tooltip, done: make(chan struct{})}
	conn, err := dialSession(t.handle)
	if err != nil {
		return nil, ErrUnsupported
	}
	t.conn = conn
	// A well-known name of the documented shape, then the watcher registration.
	name := "org.kde.StatusNotifierItem-" + strconv.Itoa(os.Getpid()) + "-1"
	e := &enc{}
	e.string_(name)
	e.uint32(0)
	if _, err := conn.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RequestName", "su", e.b); err != nil {
		conn.close()
		return nil, ErrUnsupported
	}
	e = &enc{}
	e.string_(name)
	if _, err := conn.call(watcherName, watcherPath, watcherName, "RegisterStatusNotifierItem", "s", e.b); err != nil {
		conn.close()
		return nil, ErrUnsupported
	}
	return t, nil
}

func (t *sniTray) Run() error {
	select {
	case <-t.done:
	case <-t.conn.done:
	}
	t.conn.close()
	return nil
}

func (t *sniTray) Quit() { t.once.Do(func() { close(t.done) }) }

func (t *sniTray) SetTitle(s string) {
	t.mu.Lock()
	t.title = s
	t.mu.Unlock()
	t.conn.emit(sniPath, sniIface, "NewTitle", "", nil)
	t.conn.emit(sniPath, sniIface, "NewToolTip", "", nil)
}

func (t *sniTray) SetTooltip(s string) {
	t.mu.Lock()
	t.tooltip = s
	t.mu.Unlock()
	t.conn.emit(sniPath, sniIface, "NewToolTip", "", nil)
}

// SetIcon swaps the pixmap and emits NewIcon, the StatusNotifierItem signal
// after which the host reads IconPixmap again. An icon whose ARGB32 is not
// Size²·4 bytes is ignored: a host would reject the malformed pixmap.
func (t *sniTray) SetIcon(icon Icon) {
	if icon.Size <= 0 || len(icon.ARGB32) != icon.Size*icon.Size*4 {
		return
	}
	t.mu.Lock()
	t.icon = icon
	t.mu.Unlock()
	t.conn.emit(sniPath, sniIface, "NewIcon", "", nil)
}

// SetMenu bumps the layout revision and emits LayoutUpdated only when the
// menu really changed: the caller re-sends the menu every poll tick, and a
// host relayouts (and may close an open submenu) on every LayoutUpdated.
func (t *sniTray) SetMenu(items []Item) {
	t.mu.Lock()
	if !t.menu.set(items) {
		t.mu.Unlock()
		return
	}
	t.nodes = dbusmenuTree(items)
	t.revision++
	rev := t.revision
	t.mu.Unlock()
	e := &enc{}
	e.uint32(rev)
	e.int32(0)
	t.conn.emit(menuPath, menuIface, "LayoutUpdated", "ui", e.b)
}

// Notify calls org.freedesktop.Notifications.Notify.
func (t *sniTray) Notify(title, body string) error {
	e := &enc{}
	e.string_(brand.Sanitized().Name) // app_name
	e.uint32(0)                       // replaces_id
	e.string_("")                     // app_icon
	e.string_(title)
	e.string_(body)
	done := e.array(4) // actions: as
	done()
	done = e.array(8) // hints: a{sv}
	done()
	e.int32(-1) // expire_timeout
	_, err := t.conn.call("org.freedesktop.Notifications", "/org/freedesktop/Notifications", "org.freedesktop.Notifications", "Notify", "susssasa{sv}i", e.b)
	return err
}

// Ask uses zenity or kdialog when one is installed; otherwise ErrUnsupported
// and the caller falls back to a notification plus a menu item.
func (t *sniTray) Ask(title, body, okLabel, cancelLabel string) (bool, error) {
	if p, err := exec.LookPath("zenity"); err == nil {
		err := exec.Command(p, "--question", "--title="+title, "--text="+body, "--ok-label="+okLabel, "--cancel-label="+cancelLabel).Run()
		if err == nil {
			return true, nil
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	if p, err := exec.LookPath("kdialog"); err == nil {
		err := exec.Command(p, "--title", title, "--yes-label", okLabel, "--no-label", cancelLabel, "--yesno", body).Run()
		if err == nil {
			return true, nil
		}
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return false, ErrUnsupported
}

// ── incoming method calls ──────────────────────────────────────────────────

func (t *sniTray) handle(m *message) {
	switch {
	case m.Interface == introIface && m.Member == "Introspect":
		e := &enc{}
		if m.Path == menuPath {
			e.string_(menuIntrospect)
		} else {
			e.string_(sniIntrospect)
		}
		t.conn.reply(m, "s", e.b)
	case m.Interface == propsIface:
		t.handleProperties(m)
	case m.Path == sniPath && m.Interface == sniIface:
		t.handleItem(m)
	case m.Path == menuPath && m.Interface == menuIface:
		t.handleMenu(m)
	default:
		t.conn.replyError(m, "org.freedesktop.DBus.Error.UnknownMethod", "no such method: "+m.Interface+"."+m.Member)
	}
}

func (t *sniTray) handleItem(m *message) {
	switch m.Member {
	case "Activate":
		if t.opts.OnActivate != nil {
			go t.opts.OnActivate()
		}
		t.conn.reply(m, "", nil)
	case "SecondaryActivate", "ContextMenu", "Scroll", "ProvideXdgActivationToken":
		t.conn.reply(m, "", nil)
	default:
		t.conn.replyError(m, "org.freedesktop.DBus.Error.UnknownMethod", m.Member)
	}
}

// property writers: each appends a variant for the named property, or
// reports false when the name is unknown.
func (t *sniTray) itemProperty(e *enc, name string) bool {
	t.mu.Lock()
	title, tooltip, icon := t.title, t.tooltip, t.icon
	t.mu.Unlock()
	if title == "" {
		title = brand.Sanitized().Name
	}
	switch name {
	case "Category":
		e.variantString("ApplicationStatus")
	case "Id":
		e.variantString(brand.Sanitized().Name)
	case "Title":
		e.variantString(title)
	case "Status":
		e.variantString("Active")
	case "WindowId":
		e.variantInt32(0)
	case "IconName", "OverlayIconName", "AttentionIconName", "AttentionMovieName", "IconThemePath":
		e.variantString("")
	case "IconPixmap":
		e.variant("a(iiay)", func() { e.pixmaps(icon.Size, icon.ARGB32) })
	case "OverlayIconPixmap", "AttentionIconPixmap":
		e.variant("a(iiay)", func() { e.pixmaps(0, nil) })
	case "ToolTip":
		e.variant("(sa(iiay)ss)", func() {
			e.structStart()
			e.string_("")
			e.pixmaps(0, nil)
			e.string_(title)
			e.string_(tooltip)
		})
	case "ItemIsMenu":
		e.variantBool(true)
	case "Menu":
		e.variantObjectPath(menuPath)
	default:
		return false
	}
	return true
}

var itemPropertyNames = []string{"Category", "Id", "Title", "Status", "WindowId", "IconName", "IconPixmap", "OverlayIconName", "OverlayIconPixmap", "AttentionIconName", "AttentionIconPixmap", "AttentionMovieName", "IconThemePath", "ToolTip", "ItemIsMenu", "Menu"}

func (t *sniTray) menuProperty(e *enc, name string) bool {
	switch name {
	case "Version":
		e.variantUint32(3)
	case "Status":
		e.variantString("normal")
	case "TextDirection":
		e.variantString("ltr")
	case "IconThemePath":
		e.variantStringArray(nil)
	default:
		return false
	}
	return true
}

var menuPropertyNames = []string{"Version", "Status", "TextDirection", "IconThemePath"}

func (t *sniTray) handleProperties(m *message) {
	d := &dec{b: m.Body}
	iface := d.string_()
	write := t.itemProperty
	names := itemPropertyNames
	if m.Path == menuPath {
		write, names = t.menuProperty, menuPropertyNames
	}
	switch m.Member {
	case "Get":
		name := d.string_()
		e := &enc{}
		if !write(e, name) {
			t.conn.replyError(m, "org.freedesktop.DBus.Error.InvalidArgs", "no property "+iface+"."+name)
			return
		}
		t.conn.reply(m, "v", e.b)
	case "GetAll":
		e := &enc{}
		done := e.array(8)
		for _, n := range names {
			e.dictEntry(n, func() { write(e, n) })
		}
		done()
		t.conn.reply(m, "a{sv}", e.b)
	case "Set":
		t.conn.replyError(m, "org.freedesktop.DBus.Error.PropertyReadOnly", "read-only")
	default:
		t.conn.replyError(m, "org.freedesktop.DBus.Error.UnknownMethod", m.Member)
	}
}

// ── com.canonical.dbusmenu ─────────────────────────────────────────────────

// dbusmenuNode is one item of the dbusmenu tree. Its index in the tree is
// its dbusmenu id: 0 is the root, every other id is the menuModel tag + 1,
// numbered by walkMenu, so an Event's id minus one goes straight to idAt.
type dbusmenuNode struct {
	item     Item
	children []int32
}

// dbusmenuTree lays items out as the tree GetLayout serves: a submenu row
// holds its children's ids, everything else hangs off the root.
func dbusmenuTree(items []Item) []dbusmenuNode {
	nodes := []dbusmenuNode{{}}
	parents := []int32{0}
	add := func(tag int, it Item) int32 {
		id := int32(tag + 1) // == len(nodes): walkMenu numbers in visiting order
		nodes = append(nodes, dbusmenuNode{item: it})
		p := parents[len(parents)-1]
		nodes[p].children = append(nodes[p].children, id)
		return id
	}
	walkMenu(items,
		func(tag int, it Item) { add(tag, it) },
		func(tag int, it Item) { parents = append(parents, add(tag, it)) },
		func() { parents = parents[:len(parents)-1] })
	return nodes
}

// isSubmenu mirrors walkMenu's test for a row that opens children.
func isSubmenu(it Item) bool { return len(it.Children) > 0 && !it.Separator }

// dbusmenuEnabled is the "enabled" property. A submenu row is not clickable
// (Clickable is false) but must stay enabled, or the host will not open it.
func dbusmenuEnabled(it Item) bool {
	if isSubmenu(it) {
		return !it.Disabled
	}
	return it.Clickable()
}

// itemProps writes the a{sv} for one menu item.
func (t *sniTray) itemProps(e *enc, it Item) {
	done := e.array(8)
	if it.Separator {
		e.dictEntry("type", func() { e.variantString("separator") })
		done()
		return
	}
	e.dictEntry("label", func() { e.variantString(it.FallbackTitle()) })
	e.dictEntry("enabled", func() { e.variantBool(dbusmenuEnabled(it)) })
	e.dictEntry("visible", func() { e.variantBool(true) })
	if isSubmenu(it) {
		e.dictEntry("children-display", func() { e.variantString("submenu") })
	}
	if it.Checked || it.Active {
		e.dictEntry("toggle-type", func() { e.variantString("checkmark") })
		e.dictEntry("toggle-state", func() { e.variantInt32(1) })
	}
	done()
}

// writeLayout writes the (ia{sv}av) for id and, depth levels down, its
// children: depth 0 is the node alone, a negative depth the whole subtree.
// An unknown id comes back as a bare node without properties.
func (t *sniTray) writeLayout(e *enc, nodes []dbusmenuNode, id, depth int32) {
	e.structStart()
	e.int32(id)
	known := id >= 0 && int(id) < len(nodes)
	switch {
	case id == 0:
		props := e.array(8)
		e.dictEntry("children-display", func() { e.variantString("submenu") })
		props()
	case known:
		t.itemProps(e, nodes[id].item)
	default:
		none := e.array(8)
		none()
	}
	children := e.array(1)
	if known && depth != 0 {
		for _, c := range nodes[id].children {
			e.variant("(ia{sv}av)", func() { t.writeLayout(e, nodes, c, depth-1) })
		}
	}
	children()
}

// writeGroupProperties writes the a(ia{sv}) for the requested ids, nested
// ones included; ids that name no item (the root among them) are skipped.
func (t *sniTray) writeGroupProperties(e *enc, nodes []dbusmenuNode, ids []any) {
	done := e.array(8)
	for _, idAny := range ids {
		id, _ := idAny.(int32)
		if id <= 0 || int(id) >= len(nodes) {
			continue
		}
		e.structStart()
		e.int32(id)
		t.itemProps(e, nodes[id].item)
	}
	done()
}

func (t *sniTray) handleMenu(m *message) {
	t.mu.Lock()
	nodes := t.nodes // replaced, never mutated, by SetMenu: safe to read unlocked
	rev := t.revision
	t.mu.Unlock()
	if nodes == nil {
		nodes = []dbusmenuNode{{}} // no menu yet: an empty root
	}
	d := &dec{b: m.Body}
	switch m.Member {
	case "GetLayout":
		parent := d.int32()
		depth := d.int32()
		e := &enc{}
		e.uint32(rev)
		t.writeLayout(e, nodes, parent, depth)
		t.conn.reply(m, "u(ia{sv}av)", e.b)
	case "GetGroupProperties":
		idsAny, _ := d.value("ai")
		ids, _ := idsAny.([]any)
		e := &enc{}
		t.writeGroupProperties(e, nodes, ids)
		t.conn.reply(m, "a(ia{sv})", e.b)
	case "GetProperty":
		id := d.int32()
		name := d.string_()
		e := &enc{}
		if id > 0 && int(id) < len(nodes) {
			it := nodes[id].item
			switch name {
			case "label":
				e.variantString(it.FallbackTitle())
			case "enabled":
				e.variantBool(dbusmenuEnabled(it))
			case "visible":
				e.variantBool(true)
			case "type":
				if it.Separator {
					e.variantString("separator")
				} else {
					e.variantString("standard")
				}
			case "children-display":
				if isSubmenu(it) {
					e.variantString("submenu")
				} else {
					e.variantString("")
				}
			case "toggle-state":
				if it.Checked {
					e.variantInt32(1)
				} else {
					e.variantInt32(0)
				}
			default:
				t.conn.replyError(m, "org.freedesktop.DBus.Error.InvalidArgs", "no property "+name)
				return
			}
			t.conn.reply(m, "v", e.b)
			return
		}
		t.conn.replyError(m, "org.freedesktop.DBus.Error.InvalidArgs", "no item "+strconv.Itoa(int(id)))
	case "Event":
		id := d.int32()
		event := d.string_()
		t.conn.reply(m, "", nil)
		if event == "clicked" {
			t.clicked(int(id) - 1)
		}
	case "EventGroup":
		evs, _ := d.value("a(isvu)")
		list, _ := evs.([]any)
		for _, ev := range list {
			f, _ := ev.([]any)
			if len(f) >= 2 {
				if s, _ := f[1].(string); s == "clicked" {
					if id, ok := f[0].(int32); ok {
						t.clicked(int(id) - 1)
					}
				}
			}
		}
		e := &enc{}
		none := e.array(4)
		none()
		t.conn.reply(m, "ai", e.b)
	case "AboutToShow":
		e := &enc{}
		e.bool_(false)
		t.conn.reply(m, "b", e.b)
	case "AboutToShowGroup":
		e := &enc{}
		a := e.array(4)
		a()
		b := e.array(4)
		b()
		t.conn.reply(m, "aiai", e.b)
	default:
		t.conn.replyError(m, "org.freedesktop.DBus.Error.UnknownMethod", m.Member)
	}
}

// clicked resolves a dbusmenu id minus one, i.e. the menuModel tag.
func (t *sniTray) clicked(idx int) {
	t.mu.Lock()
	id, ok := t.menu.idAt(idx)
	t.mu.Unlock()
	if ok && t.opts.OnClick != nil {
		go t.opts.OnClick(id)
	}
}

const sniIntrospect = `<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd">
<node>
  <interface name="org.kde.StatusNotifierItem">
    <property name="Category" type="s" access="read"/>
    <property name="Id" type="s" access="read"/>
    <property name="Title" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="WindowId" type="i" access="read"/>
    <property name="IconName" type="s" access="read"/>
    <property name="IconPixmap" type="a(iiay)" access="read"/>
    <property name="OverlayIconName" type="s" access="read"/>
    <property name="OverlayIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionIconName" type="s" access="read"/>
    <property name="AttentionIconPixmap" type="a(iiay)" access="read"/>
    <property name="AttentionMovieName" type="s" access="read"/>
    <property name="IconThemePath" type="s" access="read"/>
    <property name="ToolTip" type="(sa(iiay)ss)" access="read"/>
    <property name="ItemIsMenu" type="b" access="read"/>
    <property name="Menu" type="o" access="read"/>
    <method name="Activate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
    <method name="SecondaryActivate"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
    <method name="ContextMenu"><arg name="x" type="i" direction="in"/><arg name="y" type="i" direction="in"/></method>
    <method name="Scroll"><arg name="delta" type="i" direction="in"/><arg name="orientation" type="s" direction="in"/></method>
    <signal name="NewTitle"/><signal name="NewIcon"/><signal name="NewToolTip"/>
    <signal name="NewStatus"><arg name="status" type="s"/></signal>
  </interface>
  <interface name="org.freedesktop.DBus.Properties">
    <method name="Get"><arg type="s" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="out"/></method>
    <method name="GetAll"><arg type="s" direction="in"/><arg type="a{sv}" direction="out"/></method>
  </interface>
</node>`

const menuIntrospect = `<!DOCTYPE node PUBLIC "-//freedesktop//DTD D-BUS Object Introspection 1.0//EN" "http://www.freedesktop.org/standards/dbus/1.0/introspect.dtd">
<node>
  <interface name="com.canonical.dbusmenu">
    <property name="Version" type="u" access="read"/>
    <property name="TextDirection" type="s" access="read"/>
    <property name="Status" type="s" access="read"/>
    <property name="IconThemePath" type="as" access="read"/>
    <method name="GetLayout"><arg type="i" direction="in"/><arg type="i" direction="in"/><arg type="as" direction="in"/><arg type="u" direction="out"/><arg type="(ia{sv}av)" direction="out"/></method>
    <method name="GetGroupProperties"><arg type="ai" direction="in"/><arg type="as" direction="in"/><arg type="a(ia{sv})" direction="out"/></method>
    <method name="GetProperty"><arg type="i" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="out"/></method>
    <method name="Event"><arg type="i" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="in"/><arg type="u" direction="in"/></method>
    <method name="EventGroup"><arg type="a(isvu)" direction="in"/><arg type="ai" direction="out"/></method>
    <method name="AboutToShow"><arg type="i" direction="in"/><arg type="b" direction="out"/></method>
    <method name="AboutToShowGroup"><arg type="ai" direction="in"/><arg type="ai" direction="out"/><arg type="ai" direction="out"/></method>
    <signal name="ItemsPropertiesUpdated"><arg type="a(ia{sv})"/><arg type="a(ias)"/></signal>
    <signal name="LayoutUpdated"><arg type="u"/><arg type="i"/></signal>
    <signal name="ItemActivationRequested"><arg type="i"/><arg type="u"/></signal>
  </interface>
  <interface name="org.freedesktop.DBus.Properties">
    <method name="Get"><arg type="s" direction="in"/><arg type="s" direction="in"/><arg type="v" direction="out"/></method>
    <method name="GetAll"><arg type="s" direction="in"/><arg type="a{sv}" direction="out"/></method>
  </interface>
</node>`
