package cli

import (
	"fmt"
	"os"

	"github.com/tyclab/tycswap/internal/core"
)

// dispatchFableSwitch runs `switch --store fable <id>` and `switch --store
// fable --empty` (DESIGN A100): the Fable store's switch by hand.
func dispatchFableSwitch(p *parsed, sw *core.Switcher, payload *any) error {
	var res map[string]any
	var err error
	if p.empty {
		res, err = sw.EmptyFable(p.json)
	} else {
		res, err = sw.SwitchFable(*p.switchTo, p.json)
	}
	if err != nil {
		return err
	}
	if p.json {
		*payload = res
		return nil
	}
	fmt.Fprintln(os.Stdout, fableSwitchLine(res))
	return nil
}

func fableSwitchLine(res map[string]any) string {
	to, _ := res["to"].(map[string]any)
	num := ""
	if n, ok := to["number"].(*int); ok && n != nil {
		num = fmt.Sprint(*n)
	}
	email, _ := to["email"].(string)
	switch res["reason"] {
	case "already-active":
		return "The Fable store is already on Account-" + num + "."
	case "emptied":
		return "The Fable store is off now; Fable sessions start on the default login."
	}
	return "Switched the Fable store to Account-" + num + " (" + email + ")."
}
