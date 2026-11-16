package switching

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/slotkey"
	"github.com/tyclab/tycswap/internal/store"
)

// historyCopiedMarker records that a former profile's conversations reached
// the default profile, so the copy runs once and a transcript Claude Code
// later cleans up there is not brought back.
const historyCopiedMarker = ".tycswap-history-copied"

// RetireLegacyProfiles retires what `tycswap run` and `env` left behind
// (DESIGN A92). For every stored account, disabled ones included, whose
// profile no Claude process uses, its conversations are copied into the
// default profile where missing, then its login and claim are released: the
// live account's by dropping a stale or duplicate profile login, never
// writing its backup or the live credential; any other through the verified
// release (A88). A live or unverifiable claim stays; held is told why.
func RetireLegacyProfiles(s *store.Store, held func(number string, err error)) {
	data, err := s.ReadSequence()
	if err != nil || data == nil {
		if err != nil {
			held("", err)
		}
		return
	}
	keys := make([]string, 0, len(data.Accounts))
	for number := range data.Accounts {
		keys = append(keys, number)
	}
	live := s.CurrentAccountNumber()
	for _, number := range slotkey.Sorted(keys) {
		if _, err := strconv.Atoi(number); err != nil || s.AccountKindFor(number) == "api_key" {
			continue
		}
		if err := retireLegacyProfile(s, number, live != nil && *live == number); err != nil {
			held(number, err)
		}
	}
}

func retireLegacyProfile(s *store.Store, number string, live bool) error {
	dir := s.SessionDir(number, s.AccountEmail(number))
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return nil
	}
	if pids, err := store.LiveProfilePIDs(dir); err != nil || len(pids) > 0 {
		return err
	}
	copyErr := copyProfileHistory(s, dir)
	var profile store.LegacyProfile
	if err := s.Lock.With(func() error {
		var err error
		profile, err = s.LegacyProfileOf(number)
		return err
	}); err != nil {
		return err
	}
	var err error
	switch {
	case !profile.Pending():
	case live:
		err = withLegacyLocks(s, profile.Dir, func() error { return s.DiscardLegacyLogin(number, profile.Credential) })
	default:
		err = ReleaseIdleLegacy(s, number)
	}
	if err == nil {
		err = copyErr
	}
	return err
}

// copyProfileHistory copies the profile's conversations into the default
// profile's projects where missing (A78's copy), once. A shared history
// (`run --share-history` linked projects to the default) is already there.
func copyProfileHistory(s *store.Store, dir string) error {
	marker := filepath.Join(dir, historyCopiedMarker)
	if _, err := os.Lstat(marker); err == nil || !os.IsNotExist(err) {
		return err
	}
	from := filepath.Join(dir, "projects")
	info, err := os.Lstat(from)
	switch {
	case os.IsNotExist(err):
	case err != nil:
		return err
	case info.IsDir():
		if err := copyHistory(from, filepath.Join(s.DefaultProfileDir(), "projects")); err != nil {
			return err
		}
	case info.Mode()&os.ModeSymlink == 0:
		return &os.PathError{Op: "copy history", Path: from, Err: os.ErrInvalid}
	}
	return atomicfile.Write(marker, nil, atomicfile.Opts{})
}
