package update

import "path/filepath"

type InstallShape int

const (
	ShapeUnknown InstallShape = iota
	ShapeGoInstall
)

func (s InstallShape) String() string {
	if s == ShapeGoInstall {
		return "go-install"
	}
	return "unknown"
}

// DetectInstallShape classifies exePath (typically the symlink-resolved
// result of os.Executable()) by checking whether its containing directory is
// one of the three Go-managed bin dirs named in Amendment A6: $GOBIN (if
// set), $GOPATH/bin (GOPATH defaulting to $HOME/go, mirroring `go env
// GOPATH`), and — always, even when GOPATH is set to something else —
// $HOME/go/bin explicitly.
//
// getenv and homeDir are seams: production callers pass os.Getenv and the
// result of os.UserHomeDir(); tests pass fakes to exercise every branch
// without touching the real environment.
func DetectInstallShape(exePath string, getenv func(string) string, homeDir string) InstallShape {
	if exePath == "" {
		return ShapeUnknown
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	dir := filepath.Clean(filepath.Dir(exePath))
	for _, cand := range goBinDirs(getenv, homeDir) {
		if cand != "" && filepath.Clean(cand) == dir {
			return ShapeGoInstall
		}
	}
	return ShapeUnknown
}

func goBinDirs(getenv func(string) string, homeDir string) []string {
	var dirs []string
	if gobin := getenv("GOBIN"); gobin != "" {
		dirs = append(dirs, gobin)
	}
	gopath := getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(homeDir, "go")
	}
	dirs = append(dirs, filepath.Join(gopath, "bin"))
	if homeDir != "" {
		dirs = append(dirs, filepath.Join(homeDir, "go", "bin"))
	}
	return dirs
}
