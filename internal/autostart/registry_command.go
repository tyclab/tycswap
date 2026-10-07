package autostart

import "strings"

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
