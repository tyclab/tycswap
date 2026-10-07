package recovery

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func WorkspaceKey(cwd string) (string, error) {
	path, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	path, err = canonicalWorkspacePath(path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	original := path
	for {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return original, nil
		}
		path = parent
	}
}

func canonicalWorkspacePath(path string) (string, error) {
	probe := path
	var missing []string
	for {
		canonical, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				canonical = filepath.Join(canonical, missing[i])
			}
			return canonical, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(probe) == probe {
			return "", err
		}
		missing = append(missing, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
}
