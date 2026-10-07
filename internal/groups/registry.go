package groups

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/sessprofile"
)

type Registry struct {
	Version int              `json:"version"`
	Claims  map[string]Owner `json:"claims"`
}

func ClaimsPath(root string) string { return filepath.Join(root, "groups", "claims.json") }

func LoadRegistry(root string) (*Registry, error) {
	r := &Registry{Version: 1, Claims: map[string]Owner{}}
	if err := ReadJSON(ClaimsPath(root), r); err != nil {
		return nil, err
	}
	if r.Version != 1 || r.Claims == nil {
		return nil, fmt.Errorf("invalid credential ownership registry; repair %s before switching", ClaimsPath(root))
	}
	for number, owner := range r.Claims {
		id, err := Parse(owner.Scope)
		validScope := err == nil && string(id) == owner.Scope && owner.ProfileDir == ProfileDir(root, id)
		if strings.HasPrefix(owner.Scope, "legacy:") {
			validScope = owner.Scope == "legacy:"+number && owner.ProfileDir == sessprofile.SessionDirFor(root, number, owner.Account.Email)
		}
		if !validScope || number != owner.Account.Number || owner.Account.Email == "" {
			return nil, fmt.Errorf("invalid account claim in %s", ClaimsPath(root))
		}
		if n, err := strconv.Atoi(number); err != nil || n < 1 {
			return nil, fmt.Errorf("invalid account number in %s", ClaimsPath(root))
		}
	}
	return r, nil
}

func (r *Registry) Save(root string) error {
	return WriteJSON(ClaimsPath(root), r)
}

func ReadJSON(path string, target any) error {
	if err := SafePath(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) == 0 || string(data) == "null" {
		return fmt.Errorf("unreadable session-group state at %s", path)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unreadable session-group state at %s: %w", path, err)
	}
	return nil
}

func WriteJSON(path string, value any) error {
	if err := SafePath(path); err != nil {
		return err
	}
	return atomicfile.WriteJSON(path, value, atomicfile.Opts{})
}

func SafePath(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing session-group I/O through symlink %s", p)
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

type Snapshot struct {
	Exists bool   `json:"exists"`
	Text   string `json:"text"`
}

type Journal struct {
	Version           int      `json:"version"`
	Group             ID       `json:"group"`
	Previous          *Account `json:"previous"`
	Target            Account  `json:"target"`
	Credential        Snapshot `json:"credential"`
	Config            Snapshot `json:"config"`
	TargetFingerprint string   `json:"targetFingerprint"`
	DefaultHandoff    bool     `json:"defaultHandoff,omitempty"`
	DefaultCredential Snapshot `json:"defaultCredential"`
	DefaultConfig     Snapshot `json:"defaultConfig"`
	SourceOwner       *Owner   `json:"sourceOwner,omitempty"`
	SourceCredential  Snapshot `json:"sourceCredential"`
}

func LoadJournal(root string, id ID) (*Journal, error) {
	path := JournalPath(root, id)
	if err := SafePath(path); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var j Journal
	if err := ReadJSON(path, &j); err != nil {
		return nil, err
	}
	if j.Version != 1 || j.Group != id || j.Target.Number == "" || j.Target.Email == "" || j.TargetFingerprint == "" {
		return nil, fmt.Errorf("invalid recovery journal at %s; credential claims remain held", path)
	}
	if j.SourceOwner != nil {
		owner := j.SourceOwner
		if owner.Scope != "legacy:"+j.Target.Number || owner.Account.Number != j.Target.Number || !owner.Account.SameIdentity(j.Target) || owner.ProfileDir != sessprofile.SessionDirFor(root, owner.Account.Number, owner.Account.Email) || !j.SourceCredential.Exists || j.SourceCredential.Text == "" {
			return nil, fmt.Errorf("invalid legacy source in recovery journal %s; account claims remain held", path)
		}
	}
	if j.Credential.Exists && j.Credential.Text == "" || j.DefaultHandoff && (!j.DefaultCredential.Exists || j.DefaultCredential.Text == "" || !j.DefaultConfig.Exists) {
		return nil, fmt.Errorf("incomplete snapshots in recovery journal %s; account claims remain held", path)
	}
	return &j, nil
}

func LoadActive(root string, id ID) (*Account, error) {
	path := ActivePath(root, id)
	if err := SafePath(path); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var a Account
	if err := ReadJSON(path, &a); err != nil {
		return nil, err
	}
	if a.Number == "" || a.Email == "" {
		return nil, fmt.Errorf("invalid active group account at %s", path)
	}
	return &a, nil
}
