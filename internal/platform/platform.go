// Package platform detects the host OS family and container status.
package platform

import (
	"os"
	"runtime"
	"strings"
)

// Platform is the detected host OS family.
type Platform int

const (
	// MacOS is Apple macOS (GOOS=darwin).
	MacOS Platform = iota
	// Linux is a non-WSL Linux host.
	Linux
	// WSL is Linux running under Windows Subsystem for Linux.
	WSL
	// Windows is Microsoft Windows (GOOS=windows).
	Windows
	// Unknown is any other platform.
	Unknown
)

// Detect returns WSL on linux when WSL_DISTRO_NAME is non-empty; it never shells out to uname.
func Detect() Platform {
	switch {
	case runtime.GOOS == "darwin":
		return MacOS
	case runtime.GOOS == "windows":
		return Windows
	case runtime.GOOS == "linux":
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			return WSL
		}
		return Linux
	default:
		return Unknown
	}
}

// String returns the export/JSON tag for the platform.
func (p Platform) String() string {
	switch p {
	case MacOS:
		return "macos"
	case Linux:
		return "linux"
	case WSL:
		return "wsl"
	case Windows:
		return "windows"
	default:
		return "unknown"
	}
}

// Every chmod in the codebase is gated on !IsWindows().
func IsWindows() bool {
	return runtime.GOOS == "windows"
}

// Probe paths (package vars so tests can redirect them; production values match
// the Python source exactly).
var (
	containerDockerEnvPath = "/.dockerenv"
	containerCgroupPath    = "/proc/1/cgroup"
	containerMountinfoPath = "/proc/self/mountinfo"
)

// The two substring sets are deliberately different (audit 10 Gap 3): cgroup
// matches container runtimes, mountinfo matches overlay/docker mounts.
var (
	cgroupSubstrings    = []string{"docker", "lxc", "containerd", "kubepods"}
	mountinfoSubstrings = []string{"docker", "overlay"}
)

// RunningInContainer probes like switcher._is_running_in_container: CONTAINER/container env (all platforms), then, off Windows,
// /.dockerenv, /proc/1/cgroup (docker|lxc|containerd|kubepods) and /proc/self/mountinfo (docker|overlay).
// Read errors count as no match, mirroring the Python PermissionError swallow.
func RunningInContainer() bool {
	if os.Getenv("CONTAINER") != "" || os.Getenv("container") != "" {
		return true
	}
	if Detect() == Windows {
		return false
	}
	if fileExists(containerDockerEnvPath) {
		return true
	}
	if fileContainsAny(containerCgroupPath, cgroupSubstrings) {
		return true
	}
	if fileContainsAny(containerMountinfoPath, mountinfoSubstrings) {
		return true
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileContainsAny(path string, subs []string) bool {
	if !fileExists(path) {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	for _, s := range subs {
		if strings.Contains(content, s) {
			return true
		}
	}
	return false
}
