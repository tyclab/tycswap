package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnreadableMythosCooldownKeepsTickAndStatusWorking(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(fmt.Sprintf("off=%t", off), func(t *testing.T) {
			sw, from, to := mythosTickFixture(t, 100, 20, off)
			link := bindTickSession(t, sw, tickSessionA, "mythos", from)
			statePath := mythosStatePath(sw.BackupDir())
			writeFile(t, statePath, "[")
			code, out, msg := runCodex(t, "", "auto", "--once", "--json")
			wantCode := 0
			if off {
				wantCode = 2
				if !strings.Contains(out, `"reason":"disabled"`) {
					t.Errorf("missing disabled result: %s", out)
				}
			}
			if code != wantCode || strings.Contains(out, `"event":"error"`) {
				t.Errorf("tick on unreadable cooldown failed: %d %s %s", code, out, msg)
			}
			line := sessionStatus(t, link, tickSessionA, "claude-mythos-5-2", false)
			if line != "" {
				t.Errorf("unreadable cooldown reached the status line: %q", line)
			}
			target := to
			if off {
				target = from
			}
			requireLinkTarget(t, link, target)
			if got := readOrAbsent(t, statePath); got != "[" {
				t.Errorf("automatic read rewrote cooldown: %q", got)
			}
		})
	}
}

func TestRepeatMythosChoiceRestampsCooldown(t *testing.T) {
	sw, from, _ := mythosTickFixture(t, 10, 20, false)
	bindTickSession(t, sw, tickSessionA, "mythos", from)
	path := mythosStatePath(sw.BackupDir())
	for _, raw := range []string{"[", `{"schemaVersion":1,"lastSwitchTo":"2","lastSwitchBy":"hand","lastSwitchAt":1}`} {
		writeFile(t, path, raw)
		code, out, msg := runSub(t, "switch", "--store", "mythos", "2", "--json")
		if code != 0 {
			t.Fatalf("reselecting account: %d %s %s", code, out, msg)
		}
		var stamp struct {
			LastSwitchTo, LastSwitchBy string
			LastSwitchAt               float64
		}
		b, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(b, &stamp) != nil || stamp.LastSwitchTo != "2" || stamp.LastSwitchBy != "hand" || stamp.LastSwitchAt <= 1 {
			t.Fatalf("reselecting account left no new hand stamp: %q (%v)", b, err)
		}
	}
}

func TestMythosCooldownWarnsOncePerEpisodeAcrossProcesses(t *testing.T) {
	const envName = "TYCSWAP_FIXTURE_COOLDOWN_OBSERVER"
	if link := os.Getenv(envName); link != "" {
		sessionStatus(t, link, tickSessionA, "claude-mythos-5-2", false)
		return
	}
	sw, from, _ := mythosTickFixture(t, 10, 20, true)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	path := mythosStatePath(sw.BackupDir())
	observe := func() {
		t.Helper()
		var cmds []*exec.Cmd
		var outputs []*bytes.Buffer
		for range 2 {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMythosCooldownWarnsOncePerEpisodeAcrossProcesses$")
			cmd.Env = append(os.Environ(), envName+"="+link)
			buf := new(bytes.Buffer)
			cmd.Stdout, cmd.Stderr = buf, buf
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			cmds = append(cmds, cmd)
			outputs = append(outputs, buf)
		}
		for i, cmd := range cmds {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("observer: %s %v", outputs[i], err)
			}
		}
	}
	warnings := func(want int) {
		t.Helper()
		n := 0
		for _, line := range strings.Split(readOrAbsent(t, filepath.Join(sw.BackupDir(), "tycswap.log")), "\n") {
			if strings.Contains(line, " - WARNING - ") && strings.Contains(strings.ToLower(line), "mythos") && strings.Contains(strings.ToLower(line), "cooldown") {
				n++
			}
		}
		if n != want {
			t.Fatalf("logged %d Mythos cooldown warnings; want %d", n, want)
		}
	}
	writeFile(t, path, "[")
	observe()
	warnings(1)
	writeFile(t, path, `{"lastSwitchBy":"hand","lastSwitchTo":"2"}`)
	observe()
	warnings(1)
	if code, out, msg := runSub(t, "switch", "--store", "mythos", "2", "--json"); code != 0 {
		t.Fatalf("manual repair: %d %s %s", code, out, msg)
	}
	writeFile(t, path, `{}`)
	observe()
	warnings(2)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	observe()
	warnings(2)
	writeFile(t, path, `null`)
	observe()
	warnings(3)
}
