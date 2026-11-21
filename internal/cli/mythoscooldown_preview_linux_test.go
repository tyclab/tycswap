package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewMythosTickLeavesCooldownWarningForRealTick(t *testing.T) {
	sw, from, to := mythosTickFixture(t, 100, 20, false)
	link := bindTickSession(t, sw, tickSessionA, "mythos", from)
	root := sw.BackupDir()
	writeFile(t, mythosStatePath(root), "[")
	logPath := filepath.Join(root, "tycswap.log")
	cooldownWarnings := func() int {
		count := 0
		for _, line := range strings.Split(strings.ToLower(readOrAbsent(t, logPath)), "\n") {
			if strings.Contains(line, " - warning - ") && strings.Contains(line, "mythos") && strings.Contains(line, "cooldown") {
				count++
			}
		}
		return count
	}
	code, out, msg := runCodex(t, "", "auto", "--once", "--dry-run", "--json")
	if code != 0 || strings.Contains(out, `"event":"error"`) || !strings.Contains(out, `"dryRun":true`) {
		t.Fatalf("dry run could not preview a switch: %d %s %s", code, out, msg)
	}
	requireLinkTarget(t, link, from)
	if n := cooldownWarnings(); n != 0 {
		t.Fatalf("dry run logged %d cooldown warnings", n)
	}
	code, out, msg = runCodex(t, "", "auto", "--once", "--json")
	if code != 0 || cooldownWarnings() != 1 {
		t.Fatalf("first real tick: %d %s %s; log: %s", code, out, msg, readOrAbsent(t, logPath))
	}
	requireLinkTarget(t, link, to)
}
