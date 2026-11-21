//go:build !linux

package cli

import "errors"

func startSessionAsker(sessionQuestion) error { return errors.New("session links require Linux") }
func sessionAskerCommand([]string) int        { return 2 }
