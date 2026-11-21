package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/store"
)

type allocationBroker struct {
	dir string
}

func installAllocationBroker(t *testing.T) allocationBroker {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "broker")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env bash
set -eu
IFS= read -r request
printf '%s\n' "$request" >"$ASK_DIR/$PPID.tmp"
mv "$ASK_DIR/$PPID.tmp" "$ASK_DIR/$PPID.question"
while [[ ! -f "$ASK_DIR/reply" ]]; do
  if IFS= read -r -t 0.02 extra; then exit 1; else rc=$?; fi
  if [[ $rc -eq 1 ]]; then exit 1; fi
done
cat "$ASK_DIR/reply"
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(childRoleEnv, "session-placement-cli")
	t.Setenv("ASK_DIR", dir)
	t.Setenv("TYCSWAP_PLACEMENT_BROKER", script)
	return allocationBroker{dir}
}

func (b allocationBroker) requests() []string {
	matches, _ := filepath.Glob(filepath.Join(b.dir, "*.question"))
	return matches
}

func (b allocationBroker) wait(t *testing.T, ready func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b allocationBroker) reply(t *testing.T, allow bool) {
	t.Helper()
	answer := "Wait"
	if allow {
		answer = placementUse
	}
	writeFile(t, filepath.Join(b.dir, "reply.tmp"), fmt.Sprintf(`{"status":"answered","question_id":"fixture","answer":%q}`, answer))
	if err := os.Rename(filepath.Join(b.dir, "reply.tmp"), filepath.Join(b.dir, "reply")); err != nil {
		t.Fatal(err)
	}
}

// Keep the answer in the test's hands until the original hook/observer has returned.
func (b allocationBroker) decide(t *testing.T, call func() string, answer func(string, string) bool) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		defer close(done)
		done <- call()
	}()
	b.wait(t, func() bool { return len(b.requests()) != 0 }, "the fixture broker received no allocation question")
	var line string
	select {
	case line = <-done:
	case <-time.After(time.Second):
		b.reply(t, false)
		<-done
		t.Fatal("the caller waited for the human allocation answer")
	}
	raw, err := os.ReadFile(b.requests()[0])
	if err != nil {
		t.Fatal(err)
	}
	var q struct{ Question, Context string }
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	b.reply(t, answer(q.Question, q.Context))
	b.wait(t, func() bool {
		_, err := os.Stat(filepath.Join(b.dir, "finished"))
		return err == nil
	}, "detached asker did not finish recording the answer")
	return line
}

func TestSessionRetriesAfterAllocationAskerDies(t *testing.T) {
	sw, rec := fableStartLogin(t, fableTickFixture{def: "1", off: true})
	link := linkedTestStart(t, rec, placementSession, "opus")
	setRecord(t, sw, "2", "mythos", true, store.ModelsKey, []string{"opus"})
	dest := store.MythosLoginDir(sw.BackupDir(), fableEmail("2"), "")
	writeFile(t, filepath.Join(dest, ".credentials.json"), dryCreds("independent", "fixture-refresh"))
	b := installAllocationBroker(t)
	t.Setenv(secureStorageEnv, link)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func() *exec.Cmd {
		cmd := exec.Command(exe, "auto", "--observe")
		cmd.Stdin = strings.NewReader(`{"session_id":"` + placementSession + `","model":{"id":"mythos"}}`)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := run()
	b.wait(t, func() bool { return len(b.requests()) == 1 }, "first observer never asked")
	pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(b.requests()[0]), ".question"))
	if err != nil {
		t.Fatal(err)
	}
	asker, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err != nil || got != exe || pid == os.Getpid() {
		t.Fatalf("unexpected fixture asker %d %q: %v", pid, got, err)
	}
	if err := asker.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	second := run()
	finished := make(chan struct{})
	go func() { _ = second.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		b.reply(t, false)
		t.Fatal("retrying observer waited on a person")
	}
	b.wait(t, func() bool { return len(b.requests()) == 2 }, "the saved claim outlived its asker and was never asked again")
	for i := 0; i < 3; i++ {
		_ = run().Wait()
	}
	if len(b.requests()) != 2 {
		t.Fatal("concurrent observers opened another broker for the live claim")
	}
	b.reply(t, true)
	b.wait(t, func() bool {
		_, err := os.Stat(filepath.Join(b.dir, "finished"))
		return err == nil
	}, "replacement asker did not finish recording its answer")
	requireLinkTarget(t, link, dest)
}
