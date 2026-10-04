//go:build linux

package tray

// A deliberately small D-Bus client: enough of the wire protocol
// (https://dbus.freedesktop.org/doc/dbus-specification.html) to speak
// StatusNotifierItem, com.canonical.dbusmenu and org.freedesktop.Notifications
// from a stdlib-only binary. Little-endian only (we are the sender; peers
// answer in our endianness), no Unix-FD passing.

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	msgMethodCall   = 1
	msgMethodReturn = 2
	msgError        = 3
	msgSignal       = 4

	fieldPath        = 1
	fieldInterface   = 2
	fieldMember      = 3
	fieldErrorName   = 4
	fieldReplySerial = 5
	fieldDestination = 6
	fieldSender      = 7
	fieldSignature   = 8

	flagNoReplyExpected = 1
)

type message struct {
	Type        byte
	Flags       byte
	Serial      uint32
	Path        string
	Interface   string
	Member      string
	ErrorName   string
	ReplySerial uint32
	Destination string
	Sender      string
	Signature   string
	Body        []byte
}

// ── encoder ────────────────────────────────────────────────────────────────

type enc struct{ b []byte }

func (e *enc) align(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}
func (e *enc) byte_(v byte) { e.b = append(e.b, v) }
func (e *enc) bool_(v bool) {
	if v {
		e.uint32(1)
	} else {
		e.uint32(0)
	}
}
func (e *enc) int32(v int32)   { e.uint32(uint32(v)) }
func (e *enc) uint32(v uint32) { e.align(4); e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *enc) string_(s string) {
	e.uint32(uint32(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}
func (e *enc) objectPath(s string) { e.string_(s) }
func (e *enc) signature(s string) {
	e.b = append(e.b, byte(len(s)))
	e.b = append(e.b, s...)
	e.b = append(e.b, 0)
}

// array writes the length placeholder, aligns to the element boundary and
// returns a closure that patches the length once the elements are written.
func (e *enc) array(elemAlign int) func() {
	e.uint32(0)
	lenAt := len(e.b) - 4
	e.align(elemAlign)
	start := len(e.b)
	return func() {
		binary.LittleEndian.PutUint32(e.b[lenAt:], uint32(len(e.b)-start))
	}
}
func (e *enc) structStart() { e.align(8) }

// variant writes the signature then the value via fn.
func (e *enc) variant(sig string, fn func()) {
	e.signature(sig)
	fn()
}
func (e *enc) variantString(s string) { e.variant("s", func() { e.string_(s) }) }
func (e *enc) variantBool(b bool)     { e.variant("b", func() { e.bool_(b) }) }
func (e *enc) variantInt32(i int32)   { e.variant("i", func() { e.int32(i) }) }
func (e *enc) variantUint32(u uint32) { e.variant("u", func() { e.uint32(u) }) }
func (e *enc) variantObjectPath(p string) {
	e.variant("o", func() { e.objectPath(p) })
}
func (e *enc) variantStringArray(ss []string) {
	e.variant("as", func() {
		done := e.array(4)
		for _, s := range ss {
			e.string_(s)
		}
		done()
	})
}

// pixmaps writes a(iiay): one entry per icon.
func (e *enc) pixmaps(size int, argb []byte) {
	done := e.array(8)
	if len(argb) > 0 {
		e.structStart()
		e.int32(int32(size))
		e.int32(int32(size))
		d := e.array(1)
		e.b = append(e.b, argb...)
		d()
	}
	done()
}

// dictEntryStart aligns for a{sv}-style entries.
func (e *enc) dictEntry(key string, value func()) {
	e.align(8)
	e.string_(key)
	value()
}

// ── decoder ────────────────────────────────────────────────────────────────

type dec struct {
	b   []byte
	off int
	err error
}

func (d *dec) align(n int) {
	for d.off%n != 0 {
		d.off++
	}
	if d.off > len(d.b) {
		d.err = errors.New("dbus: truncated message")
	}
}
func (d *dec) need(n int) bool {
	if d.err != nil {
		return false
	}
	if d.off+n > len(d.b) {
		d.err = errors.New("dbus: truncated message")
		return false
	}
	return true
}
func (d *dec) byte_() byte {
	if !d.need(1) {
		return 0
	}
	v := d.b[d.off]
	d.off++
	return v
}
func (d *dec) uint32() uint32 {
	d.align(4)
	if !d.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(d.b[d.off:])
	d.off += 4
	return v
}
func (d *dec) int32() int32 { return int32(d.uint32()) }
func (d *dec) bool_() bool  { return d.uint32() != 0 }
func (d *dec) uint64() uint64 {
	d.align(8)
	if !d.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(d.b[d.off:])
	d.off += 8
	return v
}
func (d *dec) string_() string {
	n := int(d.uint32())
	if !d.need(n + 1) {
		return ""
	}
	s := string(d.b[d.off : d.off+n])
	d.off += n + 1
	return s
}
func (d *dec) signature() string {
	n := int(d.byte_())
	if !d.need(n + 1) {
		return ""
	}
	s := string(d.b[d.off : d.off+n])
	d.off += n + 1
	return s
}

// value decodes one complete type from sig (returns the rest of sig).
// Result types: byte, bool, int32, uint32, int64/uint64 (as uint64), float64
// (bits), string, []any (arrays/structs), map[string]any (a{s?}), variant → inner.
func (d *dec) value(sig string) (any, string) {
	if sig == "" || d.err != nil {
		return nil, ""
	}
	switch sig[0] {
	case 'y':
		return d.byte_(), sig[1:]
	case 'b':
		return d.bool_(), sig[1:]
	case 'n', 'q':
		d.align(2)
		if !d.need(2) {
			return nil, ""
		}
		v := binary.LittleEndian.Uint16(d.b[d.off:])
		d.off += 2
		return uint32(v), sig[1:]
	case 'i':
		return d.int32(), sig[1:]
	case 'u', 'h':
		return d.uint32(), sig[1:]
	case 'x', 't', 'd':
		return d.uint64(), sig[1:]
	case 's', 'o':
		return d.string_(), sig[1:]
	case 'g':
		return d.signature(), sig[1:]
	case 'v':
		inner := d.signature()
		v, _ := d.value(inner)
		return v, sig[1:]
	case 'a':
		n := int(d.uint32())
		elem, rest := splitType(sig[1:])
		d.align(alignOf(elem))
		end := d.off + n
		if elem[0] == '{' {
			m := map[string]any{}
			for d.off < end && d.err == nil {
				d.align(8)
				k, _ := d.value(elem[1:2])
				v, _ := d.value(elem[2 : len(elem)-1])
				m[fmt.Sprint(k)] = v
			}
			d.off = end
			return m, rest
		}
		var out []any
		for d.off < end && d.err == nil {
			v, _ := d.value(elem)
			out = append(out, v)
		}
		d.off = end
		return out, rest
	case '(':
		d.align(8)
		body, rest := splitType(sig)
		inner := body[1 : len(body)-1]
		var out []any
		for inner != "" && d.err == nil {
			var v any
			v, inner = d.value(inner)
			out = append(out, v)
		}
		return out, rest
	}
	d.err = fmt.Errorf("dbus: unsupported type %q", sig)
	return nil, ""
}

// splitType returns the first complete type in sig and the remainder.
func splitType(sig string) (string, string) {
	if sig == "" {
		return "", ""
	}
	switch sig[0] {
	case 'a':
		inner, rest := splitType(sig[1:])
		return "a" + inner, rest
	case '(', '{':
		depth := 0
		for i := 0; i < len(sig); i++ {
			switch sig[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					return sig[:i+1], sig[i+1:]
				}
			}
		}
		return sig, ""
	}
	return sig[:1], sig[1:]
}

func alignOf(sig string) int {
	switch sig[0] {
	case 'y', 'g', 'v':
		return 1
	case 'n', 'q':
		return 2
	case 'b', 'i', 'u', 'h', 's', 'o', 'a':
		return 4
	default: // x t d ( {
		return 8
	}
}

// ── connection ─────────────────────────────────────────────────────────────

type dbusConn struct {
	c      net.Conn
	r      *bufio.Reader
	wmu    sync.Mutex
	serial atomic.Uint32
	name   string

	rmu     sync.Mutex
	waiting map[uint32]chan *message
	handler func(*message)
	done    chan struct{}
	closeMu sync.Once
}

// sessionBusAddress resolves the session bus socket path.
func sessionBusAddress() (string, bool, error) {
	addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if addr == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			return filepath.Join(rt, "bus"), false, nil
		}
		return "", false, errors.New("dbus: no session bus address")
	}
	for _, part := range strings.Split(addr, ";") {
		if !strings.HasPrefix(part, "unix:") {
			continue
		}
		for _, kv := range strings.Split(part[len("unix:"):], ",") {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "path":
				return v, false, nil
			case "abstract":
				return v, true, nil
			}
		}
	}
	return "", false, errors.New("dbus: no unix transport in " + addr)
}

func dialSession(handler func(*message)) (*dbusConn, error) {
	path, abstract, err := sessionBusAddress()
	if err != nil {
		return nil, err
	}
	if abstract {
		path = "@" + path
	}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	conn := &dbusConn{c: c, r: bufio.NewReader(c), waiting: map[uint32]chan *message{}, handler: handler, done: make(chan struct{})}
	if err := conn.auth(); err != nil {
		c.Close()
		return nil, err
	}
	go conn.readLoop()
	reply, err := conn.call("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "Hello", "", nil)
	if err != nil {
		conn.close()
		return nil, err
	}
	d := &dec{b: reply.Body}
	conn.name = d.string_()
	return conn, nil
}

func (c *dbusConn) auth() error {
	_ = c.c.SetDeadline(time.Now().Add(5 * time.Second))
	defer c.c.SetDeadline(time.Time{})
	uid := hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))
	if _, err := c.c.Write([]byte("\x00AUTH EXTERNAL " + uid + "\r\n")); err != nil {
		return err
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(line, "OK ") {
		return errors.New("dbus: auth refused: " + strings.TrimSpace(line))
	}
	_, err = c.c.Write([]byte("BEGIN\r\n"))
	return err
}

func (c *dbusConn) close() {
	c.closeMu.Do(func() {
		close(c.done)
		c.c.Close()
	})
}

func (c *dbusConn) readLoop() {
	defer c.close()
	for {
		m, err := c.readMessage()
		if err != nil {
			return
		}
		switch m.Type {
		case msgMethodReturn, msgError:
			c.rmu.Lock()
			ch := c.waiting[m.ReplySerial]
			delete(c.waiting, m.ReplySerial)
			c.rmu.Unlock()
			if ch != nil {
				ch <- m
			}
		case msgMethodCall:
			if c.handler != nil {
				c.handler(m)
			}
		}
	}
}

func (c *dbusConn) readMessage() (*message, error) {
	head := make([]byte, 16)
	if _, err := ioReadFull(c.r, head); err != nil {
		return nil, err
	}
	if head[0] != 'l' {
		return nil, errors.New("dbus: big-endian peer not supported")
	}
	bodyLen := binary.LittleEndian.Uint32(head[4:])
	fieldsLen := binary.LittleEndian.Uint32(head[12:])
	padded := (16 + int(fieldsLen) + 7) &^ 7
	rest := make([]byte, padded-16+int(bodyLen))
	if _, err := ioReadFull(c.r, rest); err != nil {
		return nil, err
	}
	m := &message{Type: head[1], Flags: head[2], Serial: binary.LittleEndian.Uint32(head[8:])}
	// header fields: a(yv) starting at offset 12 of the full message
	full := append(head, rest...)
	d := &dec{b: full, off: 12}
	fields, _ := d.value("a(yv)")
	for _, f := range fields.([]any) {
		pair := f.([]any)
		code, _ := pair[0].(byte)
		switch code {
		case fieldPath:
			m.Path, _ = pair[1].(string)
		case fieldInterface:
			m.Interface, _ = pair[1].(string)
		case fieldMember:
			m.Member, _ = pair[1].(string)
		case fieldErrorName:
			m.ErrorName, _ = pair[1].(string)
		case fieldReplySerial:
			m.ReplySerial, _ = pair[1].(uint32)
		case fieldDestination:
			m.Destination, _ = pair[1].(string)
		case fieldSender:
			m.Sender, _ = pair[1].(string)
		case fieldSignature:
			m.Signature, _ = pair[1].(string)
		}
	}
	m.Body = full[padded:]
	return m, nil
}

func ioReadFull(r *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		k, err := r.Read(b[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func (c *dbusConn) marshal(m *message) []byte {
	e := &enc{}
	e.byte_('l')
	e.byte_(m.Type)
	e.byte_(m.Flags)
	e.byte_(1)
	e.uint32(uint32(len(m.Body)))
	e.uint32(m.Serial)
	done := e.array(8)
	field := func(code byte, write func()) {
		e.structStart()
		e.byte_(code)
		write()
	}
	if m.Path != "" {
		field(fieldPath, func() { e.variantObjectPath(m.Path) })
	}
	if m.Interface != "" {
		field(fieldInterface, func() { e.variantString(m.Interface) })
	}
	if m.Member != "" {
		field(fieldMember, func() { e.variantString(m.Member) })
	}
	if m.ErrorName != "" {
		field(fieldErrorName, func() { e.variantString(m.ErrorName) })
	}
	if m.ReplySerial != 0 {
		field(fieldReplySerial, func() { e.variantUint32(m.ReplySerial) })
	}
	if m.Destination != "" {
		field(fieldDestination, func() { e.variantString(m.Destination) })
	}
	if m.Signature != "" {
		field(fieldSignature, func() { e.variant("g", func() { e.signature(m.Signature) }) })
	}
	done()
	e.align(8)
	return append(e.b, m.Body...)
}

func (c *dbusConn) send(m *message) error {
	if m.Serial == 0 {
		m.Serial = c.serial.Add(1)
	}
	raw := c.marshal(m)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.c.Write(raw)
	return err
}

// call sends a method call and waits for its reply (5 s).
func (c *dbusConn) call(dest, path, iface, member, sig string, body []byte) (*message, error) {
	m := &message{Type: msgMethodCall, Serial: c.serial.Add(1), Path: path, Interface: iface, Member: member, Destination: dest, Signature: sig, Body: body}
	ch := make(chan *message, 1)
	c.rmu.Lock()
	c.waiting[m.Serial] = ch
	c.rmu.Unlock()
	if err := c.send(m); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Type == msgError {
			return r, errors.New("dbus: " + r.ErrorName)
		}
		return r, nil
	case <-time.After(5 * time.Second):
		c.rmu.Lock()
		delete(c.waiting, m.Serial)
		c.rmu.Unlock()
		return nil, errors.New("dbus: call timed out: " + member)
	case <-c.done:
		return nil, errors.New("dbus: connection closed")
	}
}

func (c *dbusConn) reply(to *message, sig string, body []byte) {
	_ = c.send(&message{Type: msgMethodReturn, Flags: flagNoReplyExpected, ReplySerial: to.Serial, Destination: to.Sender, Signature: sig, Body: body})
}

func (c *dbusConn) replyError(to *message, name, text string) {
	e := &enc{}
	e.string_(text)
	_ = c.send(&message{Type: msgError, Flags: flagNoReplyExpected, ReplySerial: to.Serial, Destination: to.Sender, ErrorName: name, Signature: "s", Body: e.b})
}

func (c *dbusConn) emit(path, iface, member, sig string, body []byte) {
	_ = c.send(&message{Type: msgSignal, Flags: flagNoReplyExpected, Path: path, Interface: iface, Member: member, Signature: sig, Body: body})
}
