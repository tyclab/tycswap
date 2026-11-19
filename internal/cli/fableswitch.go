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
		res, err = sw.EmptyFableBy(p.json, cliActor())
	} else {
		res, err = sw.SwitchFableBy(*p.switchTo, p.json, cliActor())
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
		if res["switched"] != true {
			return "The Fable store holds no login; Fable sessions start on the default login."
		}
		return "The Fable store is off now; Fable sessions start on the default login."
	}
	return "Switched the Fable store to Account-" + num + " (" + email + ")."
}

// quietFable is the dashboard's Fable store: its routes log each switch with
// the request's actor, so the switch itself writes no line.
type quietFable struct{ sw *core.Switcher }

func (q quietFable) SwitchFable(id string, jsonOut bool) (map[string]any, error) {
	return q.sw.SwitchFableBy(id, jsonOut, "")
}

func (q quietFable) EmptyFable(jsonOut bool) (map[string]any, error) {
	return q.sw.EmptyFableBy(jsonOut, "")
}
