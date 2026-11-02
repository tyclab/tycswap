// Package tray exposes the native desktop shell without account or credential APIs.
package tray

import native "github.com/tyclab/tycswap/internal/tray"

type Item = native.Item
type Icon = native.Icon
type Tray = native.Tray
type Asker = native.Asker

// Options mirrors the native tray's options; see the README's "Native tray adapter".
type Options struct {
	Name          string
	Identifier    string
	Tooltip       string
	OnClick       func(string)
	OnSystemEvent func(string)
	// OnActivate runs on a primary click that does not open the menu (Linux StatusNotifierItem hosts).
	OnActivate func()
}

var ErrUnsupported = native.ErrUnsupported

// OnSystemEvent's names; see the README's "Native tray adapter".
const (
	EventSuspend           = native.EventSuspend
	EventResumeAutomatic   = native.EventResumeAutomatic
	EventResume            = native.EventResume
	EventDisplayChange     = native.EventDisplayChange
	EventConsoleConnect    = native.EventConsoleConnect
	EventConsoleDisconnect = native.EventConsoleDisconnect
	EventRemoteConnect     = native.EventRemoteConnect
	EventRemoteDisconnect  = native.EventRemoteDisconnect
	EventLogon             = native.EventLogon
	EventLogoff            = native.EventLogoff
	EventLock              = native.EventLock
	EventUnlock            = native.EventUnlock
	EventRemoteControl     = native.EventRemoteControl

	EventSessionEventsUnavailable = native.EventSessionEventsUnavailable
)

func New(icon Icon, opts Options) (Tray, error) { return native.New(icon, opts.native()) }

func (o Options) native() native.Options {
	return native.Options{Name: o.Name, Identifier: o.Identifier, Tooltip: o.Tooltip, OnClick: o.OnClick,
		OnSystemEvent: o.OnSystemEvent, OnActivate: o.OnActivate}
}
