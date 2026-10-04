package tray

import (
	"bufio"
	"bytes"
	"net"
	"reflect"
	"testing"
	"time"
)

// SetIcon swaps what IconPixmap answers and emits the StatusNotifierItem
// NewIcon signal, after which the host reads it again (DESIGN A44).
func TestSNISetIconSwapsThePixmapAndSignalsNewIcon(t *testing.T) {
	ours, host := net.Pipe()
	defer ours.Close()
	defer host.Close()
	plain := Icon{ARGB32: bytes.Repeat([]byte{0xff, 0x5a, 0xa2, 0xff}, 4), Size: 2}
	tr := &sniTray{icon: plain, conn: &dbusConn{c: ours, done: make(chan struct{}), waiting: map[uint32]chan *message{}}}
	peer := &dbusConn{c: host, r: bufio.NewReader(host)}
	signals := make(chan *message, 4)
	go func() {
		for {
			m, err := peer.readMessage()
			if err != nil {
				return
			}
			signals <- m
		}
	}()

	pixmap := func() []any {
		t.Helper()
		e := &enc{}
		if !tr.itemProperty(e, "IconPixmap") {
			t.Fatal("IconPixmap is not a property")
		}
		d := &dec{b: e.b}
		v, _ := d.value(d.signature())
		if d.err != nil {
			t.Fatal(d.err)
		}
		list, _ := v.([]any)
		if len(list) != 1 {
			t.Fatalf("pixmaps = %v, want one", v)
		}
		return list[0].([]any)
	}
	if got := pixmap(); got[0] != int32(2) || len(got[2].([]any)) != 16 {
		t.Fatalf("plain pixmap = %v", got)
	}

	badge := Icon{ARGB32: bytes.Repeat([]byte{0xff, 0x24, 0x41, 0x66}, 9), Size: 3}
	tr.SetIcon(badge)
	select {
	case m := <-signals:
		if m.Type != msgSignal || m.Path != sniPath || m.Interface != sniIface || m.Member != "NewIcon" || len(m.Body) != 0 {
			t.Errorf("signal = %+v, want %s.NewIcon on %s without arguments", m, sniIface, sniPath)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no NewIcon signal")
	}
	got := pixmap()
	if got[0] != int32(3) || got[1] != int32(3) {
		t.Errorf("badge pixmap size = %v×%v, want 3×3", got[0], got[1])
	}
	if px := got[2].([]any); len(px) != 36 || px[1] != byte(0x1a) {
		t.Errorf("badge pixmap bytes = %v", px)
	}

	// A pixmap whose length does not match its size is ignored: no signal,
	// and IconPixmap keeps the badge.
	tr.SetIcon(Icon{ARGB32: []byte{1, 2, 3}, Size: 2})
	tr.SetIcon(Icon{PNG: []byte("png only")})
	select {
	case m := <-signals:
		t.Errorf("unexpected signal %s", m.Member)
	case <-time.After(100 * time.Millisecond):
	}
	if got := pixmap(); got[0] != int32(3) {
		t.Errorf("an unusable icon replaced the pixmap: %v", got[0])
	}
}

// A submenu row is a dbusmenu item with children-display "submenu" and its
// children nested under it; GetLayout serves any subtree to any depth,
// GetGroupProperties finds nested ids, and a click on a nested id reaches
// OnClick with the child's ID. An unchanged SetMenu emits no LayoutUpdated.
func TestSNISubmenuLayout(t *testing.T) {
	ours, host := net.Pipe()
	defer ours.Close()
	defer host.Close()
	clicks := make(chan string, 4)
	tr := &sniTray{
		opts: Options{OnClick: func(id string) { clicks <- id }},
		conn: &dbusConn{c: ours, done: make(chan struct{}), waiting: map[uint32]chan *message{}},
	}
	peer := &dbusConn{c: host, r: bufio.NewReader(host)}
	signals := make(chan *message, 4)
	go func() {
		for {
			m, err := peer.readMessage()
			if err != nil {
				return
			}
			signals <- m
		}
	}()

	items := []Item{
		{ID: "open", Title: "Open"},
		{ID: "more", Title: "More", Children: []Item{
			{ID: "a", Title: "A"},
			{ID: "deep", Title: "Deep", Children: []Item{{ID: "x", Title: "X"}}},
		}},
		{ID: "quit", Title: "Quit"},
	}
	// ids: 1 open, 2 more{3 a, 4 deep{5 x}}, 6 quit
	tr.SetMenu(items)
	select {
	case m := <-signals:
		if m.Member != "LayoutUpdated" {
			t.Fatalf("signal = %s, want LayoutUpdated", m.Member)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no LayoutUpdated")
	}
	tr.SetMenu(append([]Item(nil), items...))
	select {
	case m := <-signals:
		t.Errorf("an unchanged menu emitted %s", m.Member)
	case <-time.After(100 * time.Millisecond):
	}
	if tr.revision != 1 {
		t.Errorf("revision = %d, want 1", tr.revision)
	}

	type node struct {
		id       int32
		props    map[string]any
		children []node
	}
	var conv func(v any) node
	conv = func(v any) node {
		f := v.([]any)
		n := node{id: f[0].(int32), props: f[1].(map[string]any)}
		kids, _ := f[2].([]any)
		for _, k := range kids {
			n.children = append(n.children, conv(k))
		}
		return n
	}
	layout := func(parent, depth int32) node {
		t.Helper()
		e := &enc{}
		tr.writeLayout(e, tr.nodes, parent, depth)
		d := &dec{b: e.b}
		v, _ := d.value("(ia{sv}av)")
		if d.err != nil {
			t.Fatal(d.err)
		}
		return conv(v)
	}
	ids := func(ns []node) []int32 {
		var out []int32
		for _, n := range ns {
			out = append(out, n.id)
		}
		return out
	}

	root := layout(0, -1)
	if got := ids(root.children); !reflect.DeepEqual(got, []int32{1, 2, 6}) {
		t.Fatalf("root children = %v", got)
	}
	more := root.children[1]
	if more.props["children-display"] != "submenu" || more.props["label"] != "More" || more.props["enabled"] != true {
		t.Errorf("submenu props = %v", more.props)
	}
	if got := ids(more.children); !reflect.DeepEqual(got, []int32{3, 4}) {
		t.Errorf("submenu children = %v", got)
	}
	if got := ids(more.children[1].children); !reflect.DeepEqual(got, []int32{5}) {
		t.Errorf("nested submenu children = %v", got)
	}
	if _, ok := root.children[0].props["children-display"]; ok {
		t.Error("a leaf must not claim children")
	}

	if n := layout(2, 0); n.id != 2 || len(n.children) != 0 || n.props["children-display"] != "submenu" {
		t.Errorf("layout(2, 0) = %+v", n)
	}
	if n := layout(2, 1); !reflect.DeepEqual(ids(n.children), []int32{3, 4}) || len(n.children[1].children) != 0 {
		t.Errorf("layout(2, 1) = %+v", n)
	}
	if n := layout(4, -1); !reflect.DeepEqual(ids(n.children), []int32{5}) || n.children[0].props["label"] != "X" {
		t.Errorf("layout(4, -1) = %+v", n)
	}
	if n := layout(0, 1); len(n.children) != 3 || len(n.children[1].children) != 0 {
		t.Errorf("layout(0, 1) = %+v", n)
	}
	if n := layout(99, -1); n.id != 99 || len(n.props) != 0 || len(n.children) != 0 {
		t.Errorf("layout(99) = %+v", n)
	}

	e := &enc{}
	tr.writeGroupProperties(e, tr.nodes, []any{int32(5), int32(0), int32(42), int32(2)})
	d := &dec{b: e.b}
	v, _ := d.value("a(ia{sv})")
	if d.err != nil {
		t.Fatal(d.err)
	}
	group := v.([]any)
	if len(group) != 2 {
		t.Fatalf("group = %v, want ids 5 and 2", group)
	}
	if f := group[0].([]any); f[0] != int32(5) || f[1].(map[string]any)["label"] != "X" {
		t.Errorf("group[0] = %v", f)
	}

	tr.clicked(5 - 1)
	tr.clicked(2 - 1) // the submenu row: no click
	tr.clicked(3 - 1)
	got := map[string]bool{}
	for range 2 { // OnClick runs on its own goroutine: no order
		select {
		case id := <-clicks:
			got[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("clicks so far %v, want x and a", got)
		}
	}
	if !got["x"] || !got["a"] {
		t.Errorf("clicks = %v, want x and a", got)
	}
	select {
	case id := <-clicks:
		t.Errorf("unexpected click %q", id)
	case <-time.After(100 * time.Millisecond):
	}
}
