//go:build windows

package netproxy

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readSystem reads the current user's WinINET proxy, which group policy and
// the Settings app both write.
func readSystem() settings {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return settings{}
	}
	defer k.Close()
	if on, _, err := k.GetIntegerValue("ProxyEnable"); err != nil || on == 0 {
		return settings{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	var s settings
	s.https, s.http = parseProxyServer(server)
	s.bypass = strings.Split(override, ";")
	return s
}
