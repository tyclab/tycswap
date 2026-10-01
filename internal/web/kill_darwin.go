//go:build darwin

package web

import (
	"time"

	"golang.org/x/sys/unix"
)

// processStart reads when pid started from the kernel's process table
// (sysctl kern.proc.pid, kinfo_proc.kp_proc.p_starttime).
func processStart(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	tv := kp.Proc.P_starttime
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*int64(time.Microsecond)), nil
}
