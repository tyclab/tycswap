package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/paths"
)

type FableParking struct {
	WaitingForIdle bool   `json:"-"`
	ReadyAccount   string `json:"readyAccount,omitempty"`
	Identity       string `json:"identity,omitempty"`
}

func ReadFableParking(root string) (*FableParking, error) {
	path := filepath.Join(paths.FableScopeDir(root), "allocation-parked")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid Fable allocation marker at %s", path)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p FableParking
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func FableAllocationParked(root string) (bool, error) {
	p, err := ReadFableParking(root)
	return p != nil, err
}

func (s *Store) WriteFableParking(p FableParking) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(paths.FableScopeDir(s.BackupDir()), "allocation-parked"), raw, atomicfile.Opts{})
}

// ParkingStatus names the last prepared account only while the slot still
// binds to the same email and organization.
func (s *Store) ParkingStatus() (*FableParking, error) {
	p, err := ReadFableParking(s.BackupDir())
	if err != nil || p == nil {
		return p, err
	}
	if f, e := s.ForFable(); e == nil && f.HasLiveLogin() {
		p.WaitingForIdle = true
		p.ReadyAccount = ""
		return p, nil
	}
	if p.ReadyAccount == "" {
		return p, nil
	}
	data, err := s.SequenceForUpdate()
	if err != nil {
		return nil, err
	}
	num, email, org, err := s.ResolveAccountFrom(data, p.ReadyAccount)
	if err != nil || num != p.ReadyAccount || filepath.Base(MythosLoginDir(s.BackupDir(), email, org)) != p.Identity {
		return &FableParking{}, nil
	}
	return p, nil
}

func (p *FableParking) Description() string {
	if p == nil {
		return ""
	}
	if p.WaitingForIdle {
		return "waiting for the active Fable login to become idle"
	}
	if p.ReadyAccount != "" {
		return "Account-" + p.ReadyAccount + " ready for the next native Fable start"
	}
	return "parked, waiting for an eligible native Fable start"
}

// SetFableAllocationParked is called under the Fable state lock. Absence also
// represents a store explicitly turned off before this marker existed.
func (s *Store) SetFableAllocationParked(parked bool) error {
	path := filepath.Join(paths.FableScopeDir(s.BackupDir()), "allocation-parked")
	if parked {
		return s.WriteFableParking(FableParking{})
	}
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
