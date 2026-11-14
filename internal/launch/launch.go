package launch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const LauncherMarker = "tycswap-start-launcher"

func Resolve(command string) (string, error) { return exec.LookPath(command) }

// ResolveDefault skips the provisioner's launcher so a plain start cannot recurse.
func ResolveDefault() (string, error) {
	self, _ := os.Executable()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		path, err := filepath.Abs(filepath.Join(dir, "claude"))
		if err != nil {
			continue
		}
		bin, err := exec.LookPath(path)
		if err != nil || sameFile(bin, self) || isLauncher(bin) {
			continue
		}
		return bin, nil
	}
	return "", fmt.Errorf("real Claude executable not found; pass its verified path after 'tycswap start --': %w", exec.ErrNotFound)
}

func sameFile(a, b string) bool {
	first, err := os.Stat(a)
	if err != nil {
		return false
	}
	second, err := os.Stat(b)
	return err == nil && os.SameFile(first, second)
}

func isLauncher(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() > 1024*1024 {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(LauncherMarker))
}

func ExitCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	return 1
}

func CheckCmdShimArgs(bin string, args []string) error {
	if !isCmdShim(bin) {
		return nil
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "&|<>^%!\"()\r\n") {
			return fmt.Errorf("refusing to pass shell metacharacters to %s through cmd.exe; use a native executable", filepath.Base(bin))
		}
	}
	return nil
}

func isCmdShim(bin string) bool {
	ext := strings.ToLower(filepath.Ext(bin))
	return ext == ".cmd" || ext == ".bat"
}
