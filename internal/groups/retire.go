package groups

import (
	"os"
	"path/filepath"
)

const retiredMarker = "retired.json"

// Pending reports whether a group still holds something to retire (DESIGN
// A78): an account claim, or a scope directory not yet marked retired.
func Pending(root string, id ID) (bool, error) {
	registry, err := LoadRegistry(root)
	if err != nil {
		return false, err
	}
	for _, owner := range registry.Claims {
		if owner.Scope == string(id) {
			return true, nil
		}
	}
	entries, err := os.ReadDir(ScopeDir(root, id))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Name() == retiredMarker {
			return false, nil
		}
	}
	return len(entries) > 0, nil
}

// Retire removes a released group's control files, keeps its profile and
// marks the group retired.
func Retire(root string, id ID) error {
	scope := ScopeDir(root, id)
	if err := SafePath(scope); err != nil {
		return err
	}
	entries, err := os.ReadDir(scope)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "profile" || e.Name() == retiredMarker {
			continue
		}
		if err := os.RemoveAll(filepath.Join(scope, e.Name())); err != nil {
			return err
		}
	}
	return WriteJSON(filepath.Join(scope, retiredMarker), map[string]any{"version": 1})
}
