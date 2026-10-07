package recovery

import (
	"os"
	"path/filepath"
)

func WorkspaceKey(cwd string) (string, error) {
	path, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	} else if !os.IsNotExist(err) {
		return "", err
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
