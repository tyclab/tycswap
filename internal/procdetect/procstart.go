package procdetect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is where /proc is mounted; tests point it at a fake one.
var procRoot = "/proc"

// RecordAlive reports whether the process a Claude Code session record names
// still runs. Claude Code writes procStart, field 22 of /proc/<pid>/stat: in
// this PID namespace a record is alive only while its PID still has that start
// time, so a reused PID does not revive a dead session (SES-6). Without
// procStart, from another namespace or where /proc cannot tell, IsPIDAlive decides.
func RecordAlive(pid int, procStart any, pidDomain string) bool {
	if pid <= 1 {
		return false
	}
	if want := procStartText(procStart); want != "" && sameNamespace(pidDomain) {
		if got, ok := procStartTime(pid); ok {
			return got == want
		}
	}
	return IsPIDAlive(pid)
}

// procStartTime is field 22 of <procRoot>/<pid>/stat. The command name, field
// 2, may hold spaces and parentheses, so fields count from its last ")".
func procStartTime(pid int) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	s := string(raw)
	end := strings.LastIndexByte(s, ')')
	if end < 0 {
		return "", false
	}
	fields := strings.Fields(s[end+1:])
	if len(fields) < 20 || !digits(fields[19]) {
		return "", false
	}
	return fields[19], true
}

// sameNamespace reports whether a record's pidDomain ("linux:<id>:pid:[N]")
// names this process's PID namespace; a record without one is taken as local.
func sameNamespace(pidDomain string) bool {
	if pidDomain == "" {
		return true
	}
	ns, err := os.Readlink(filepath.Join(procRoot, "self", "ns", "pid"))
	return err == nil && strings.HasSuffix(pidDomain, ":"+ns)
}

func procStartText(v any) string {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	case float64:
		s = strconv.FormatFloat(t, 'f', -1, 64)
	}
	if !digits(s) {
		return ""
	}
	return s
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
