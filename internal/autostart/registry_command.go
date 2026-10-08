package autostart

import "strings"

// registryCommand quotes for CommandLineToArgvW; no build tag so it is tested on every platform.
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
