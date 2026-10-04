package autostart

import "strings"

// registryCommand is the Windows Run value: the quoted executable plus
// arguments, quoted the way CommandLineToArgvW reads them back. It has no
// build tag so the rendering is tested on every platform; only the registry
// calls around it are Windows-only (registry_windows.go).
func registryCommand(c Config) string {
	parts := []string{`"` + c.Exe + `"`}
	for _, a := range c.Args {
		if strings.ContainsAny(a, " \t\"") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}
