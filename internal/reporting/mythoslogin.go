package reporting

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
	"github.com/tyclab/tycswap/internal/usage"
)

type MythosLogin struct {
	ChoiceNote string `json:"-"`
	Live       bool   `json:"live"`
	Status     string `json:"status"`
	Selectable bool   `json:"selectable"`
}

type mythosView struct {
	logins map[string]*MythosLogin
	object map[string]any
}

func readMythosView(s *store.Store, data *store.SequenceData) mythosView {
	v := mythosView{logins: map[string]*MythosLogin{}}
	if s.IsFable() {
		return v
	}
	digest, selectionErr := store.SelectedMythosDigest(s.BackupDir())
	obj := map[string]any{"number": nil, "linkedSessions": nil, "countedWindows": nil}
	if data == nil || data.Accounts == nil || data.Sequence == nil {
		if digest != "" || selectionErr != nil {
			obj["reason"] = "the Mythos account roster is unavailable"
			v.object = obj
		}
		return v
	}
	var active string
	readyCount := 0
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		login, err := s.MythosLogin(data, num)
		if login.Dir == "" {
			continue
		}
		l := &MythosLogin{Status: FableLoginOK}
		switch {
		case err != nil:
			l.Status = "unavailable"
			obj["reason"] = err.Error()
		case !login.SignedIn:
			l.Status = FableSignInAgain
		default:
			if login.Ready {
				readyCount++
			}
			l.Live = login.Ready && filepath.Base(login.Dir) == digest
			l.Selectable = login.Ready && !l.Live && selectionErr == nil
		}
		v.logins[num] = l
		if l.Live {
			active = num
			obj["number"] = n
		}
	}
	if len(v.logins) == 0 && digest == "" && selectionErr == nil {
		return v
	}
	v.object = obj
	if selectionErr != nil {
		obj["reason"] = selectionErr.Error()
		return v
	}
	if active == "" {
		return v
	}
	if readyCount == 1 && obj["reason"] == nil {
		v.logins[active].ChoiceNote = "The only ready Mythos login is already selected."
		obj["choiceNote"] = v.logins[active].ChoiceNote
	}
	_, email, org, err := s.ResolveAccountFrom(data, active)
	if err != nil {
		obj["reason"] = err.Error()
		return v
	}
	obj["email"] = email
	entry := s.Usage.Entries(map[string]usage.Identity{active: {Email: email, OrgUUID: org}})[active]
	value, _ := entry.DecisionValue().(map[string]any)
	u := oauth.NewUsage(oauth.AsOf(value, s.Clk.Now()))
	var names []string
	for _, w := range oauth.RelevantWindows(u, oauth.FamilyWindows(u, "mythos")) {
		names = append(names, w.Label)
	}
	obj["countedWindows"] = names
	n, err := countLinksOnLogin(s.BackupDir(), store.MythosLoginDir(s.BackupDir(), email, org))
	if err != nil {
		obj["reason"] = err.Error()
	} else {
		obj["linkedSessions"] = n
	}
	return v
}

// Count the live launch links themselves. A missing SessionStart binding or an
// older status-line writer cannot erase the login a process actually runs on.
func countLinksOnLogin(root, target string) (int, error) {
	dir, err := linksDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		pid, stamp, valid := linkOwner(entry.Name())
		if !valid || entry.Type()&os.ModeSymlink == 0 || !procdetect.RecordAlive(pid, stamp, "") {
			continue
		}
		dest, err := os.Readlink(filepath.Join(dir, entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if dest == target {
			count++
		}
	}
	return count, nil
}

// MythosLoginLine formats both the local snapshot and one received over JSON.
func MythosLoginLine(obj map[string]any) string {
	if obj == nil {
		return ""
	}
	var summary struct {
		Number         *int
		Email          string
		CountedWindows []string
		LinkedSessions *int
		Reason         string
		ChoiceNote     string
	}
	raw, err := json.Marshal(obj)
	if err != nil || json.Unmarshal(raw, &summary) != nil {
		return "Mythos login: unreadable summary"
	}
	head := "Mythos login: "
	if summary.Number == nil {
		if summary.Reason != "" {
			return head + termsafe.Strip(summary.Reason)
		}
		if _, present := obj["number"]; !present {
			return head + "selection unavailable"
		}
		return head + "none selected"
	}
	parts := []string{fmt.Sprintf("Account-%d (%s)", *summary.Number, termsafe.Strip(summary.Email))}
	if len(summary.CountedWindows) == 0 {
		parts = append(parts, "usage unavailable")
	} else {
		parts = append(parts, "counting "+termsafe.Strip(strings.Join(summary.CountedWindows, " + ")))
	}
	if summary.LinkedSessions != nil {
		word := "linked sessions"
		if *summary.LinkedSessions == 1 {
			word = "linked session"
		}
		parts = append(parts, fmt.Sprintf("%d %s", *summary.LinkedSessions, word))
	} else if summary.Reason == "" {
		parts = append(parts, "linked session count unavailable")
	}
	if summary.Reason != "" {
		parts = append(parts, termsafe.Strip(summary.Reason))
	}
	line := head + strings.Join(parts, ", ")
	if summary.ChoiceNote != "" {
		line += ". " + termsafe.Strip(summary.ChoiceNote)
	}
	return line
}

// Label names the account’s independent login on the human-facing surfaces.
func (l *MythosLogin) Label() string {
	if l == nil {
		return ""
	}
	if l.Status == FableLoginOK {
		if l.Live {
			return "Mythos login: in use"
		}
		return "Mythos login"
	}
	if l.Status == "" {
		return "Mythos login: unavailable"
	}
	return "Mythos login: " + l.Status
}
