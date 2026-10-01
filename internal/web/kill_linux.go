//go:build linux

package web

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// procRoot is the procfs mount; tests point it at a directory of their own.
var procRoot = "/proc"

// clockTicksPerSecond is USER_HZ: /proc reports a process's start time in
// these ticks since boot, 100 per second on every Linux architecture Go
// supports, whatever the kernel's own HZ.
const clockTicksPerSecond = 100

// processStart reads when pid started: the boot time from /proc/stat plus
// field 22 of /proc/<pid>/stat (starttime, in clock ticks since boot).
func processStart(pid int) (time.Time, error) {
	boot, err := bootTime()
	if err != nil {
		return time.Time{}, err
	}
	stat, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, err
	}
	// The command name (field 2) is in parentheses and may hold spaces or
	// parentheses of its own, so split after the LAST ')'.
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return time.Time{}, errors.New("malformed stat line for pid " + strconv.Itoa(pid))
	}
	fields := strings.Fields(string(stat[i+1:]))
	// fields[0] is field 3 (state); starttime is field 22.
	if len(fields) < 20 {
		return time.Time{}, errors.New("short stat line for pid " + strconv.Itoa(pid))
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("starttime of pid %d: %w", pid, err)
	}
	return boot.Add(time.Duration(ticks) * time.Second / clockTicksPerSecond), nil
}

// bootTime reads the btime line of /proc/stat.
func bootTime() (time.Time, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			secs, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("btime: %w", err)
			}
			return time.Unix(secs, 0), nil
		}
	}
	return time.Time{}, errors.New("no btime in " + filepath.Join(procRoot, "stat"))
}
