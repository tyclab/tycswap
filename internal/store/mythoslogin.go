package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tyclab/tycswap/internal/atomicfile"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/oauth"
)

func MythosLoginRoot(root string) string {
	return filepath.Join(root, "mythos", "accounts")
}

// The identity, including the organization, is stable across slot moves.
// A running Claude keeps this directory for its entire session.
func MythosLoginDir(root, email, org string) string {
	return filepath.Join(MythosLoginRoot(root), AccountIdentityDigest(email, org))
}

func AccountIdentityDigest(email, org string) string {
	hash := sha256.Sum256([]byte(email + "\x00" + org))
	return hex.EncodeToString(hash[:])
}

type MythosLoginInfo struct {
	Dir      string
	SignedIn bool
	Ready    bool // Marked, enabled OAuth account with its own signed-in credential.
}

// MythosLogin reads the independent login. Marked OAuth accounts without a
// credential still need a sign-in. A filesystem error is reported separately.
func (s *Store) MythosLogin(data *SequenceData, num string) (MythosLoginInfo, error) {
	rec, ok := recordFor(data, num)
	if !ok || kindFrom(data, num) != "oauth" {
		return MythosLoginInfo{}, nil
	}
	email, org := strField(rec, "email"), strField(rec, "organizationUuid")
	login := MythosLoginInfo{Dir: MythosLoginDir(s.BackupDir(), email, org)}
	marked := MythosFrom(data, num)
	raw, err := os.ReadFile(filepath.Join(login.Dir, ccfile.CredentialsFileName))
	if os.IsNotExist(err) {
		if !marked {
			return MythosLoginInfo{}, nil
		}
		return login, nil
	}
	if err != nil {
		return login, err
	}
	login.SignedIn = oauth.ExtractAccessToken(string(raw)) != ""
	login.Ready = marked && !DisabledFrom(data, num) && login.SignedIn
	return login, nil
}

func (s *Store) MythosLoginReady(data *SequenceData, num string) (string, bool) {
	// The ranker never needs to inspect an unmarked or disabled account's login.
	if DisabledFrom(data, num) || !MythosFrom(data, num) {
		return "", false
	}
	login, _ := s.MythosLogin(data, num)
	if !login.Ready {
		return "", false
	}
	return login.Dir, true
}

// OwnStartLogin is a start login's ready Mythos login: a login of its own a
// linked start can come up on without moving a shared login (DESIGN A125).
type OwnStartLogin struct{ Num, Dir string }

// OwnStartLogins are, in sequence order, the start logins with a ready
// Mythos login, and those enrolled for one: carrying the Mythos mark or
// holding a Mythos login directory (DESIGN A125, A126).
func (s *Store) OwnStartLogins(data *SequenceData) (ready []OwnStartLogin, enrolled []string) {
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		if !StartLoginFrom(data, num) {
			continue
		}
		if dir, ok := s.MythosLoginReady(data, num); ok {
			ready = append(ready, OwnStartLogin{num, dir})
		}
		if MythosFrom(data, num) || s.holdsMythosLoginDir(data, num) {
			enrolled = append(enrolled, num)
		}
	}
	return ready, enrolled
}

func (s *Store) holdsMythosLoginDir(data *SequenceData, num string) bool {
	rec, ok := recordFor(data, num)
	if !ok {
		return false
	}
	info, err := os.Stat(MythosLoginDir(s.BackupDir(), strField(rec, "email"), strField(rec, "organizationUuid")))
	return err == nil && info.IsDir()
}

// SelectedMythosAccount resolves the identity without guessing through an
// unreadable credential. Empty means it is no longer a ready, eligible login.
func (s *Store) SelectedMythosAccount(data *SequenceData, digest string) (string, error) {
	if data == nil || data.Accounts == nil || data.Sequence == nil {
		return "", fmt.Errorf("the account roster is unavailable or incomplete")
	}
	if digest == "" {
		return "", nil
	}
	var unreadable error
	for _, n := range data.Sequence {
		num := strconv.Itoa(n)
		rec, ok := recordFor(data, num)
		email, validEmail := rec["email"].(string)
		org, validOrg := rec["organizationUuid"].(string)
		if !ok || !validEmail || email == "" {
			unreadable = fmt.Errorf("cannot verify the identity of Account-%s", num)
			continue
		}
		if AccountIdentityDigest(email, org) != digest {
			if !validOrg {
				unreadable = fmt.Errorf("cannot verify the organization of Account-%s", num)
			}
			continue
		}
		// An absent mark or disabled flag is the normal false representation.
		// A value of another type cannot authorize deleting a saved selection.
		for _, field := range []string{"mythos", "disabled"} {
			if value, present := rec[field]; present {
				if _, ok := value.(bool); !ok {
					return "", fmt.Errorf("cannot verify %s for Account-%s", field, num)
				}
			}
		}
		if DisabledFrom(data, num) || !MythosFrom(data, num) {
			return "", nil
		}
		login, err := s.MythosLogin(data, num)
		if err != nil {
			return "", err
		}
		if login.Ready {
			return num, nil
		}
	}
	return "", unreadable
}

// ClearInvalidMythosSelection runs under the root lock with a verified roster.
func (s *Store) ClearInvalidMythosSelection(data *SequenceData) error {
	digest, err := SelectedMythosDigest(s.BackupDir())
	if err != nil || digest == "" {
		return err
	}
	num, err := s.SelectedMythosAccount(data, digest)
	if err != nil || num != "" {
		return err
	}
	return os.Remove(filepath.Join(s.BackupDir(), "mythos", "selected"))
}

func SelectedMythosDigest(root string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, "mythos", "selected"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(raw))
	if bytes, err := hex.DecodeString(digest); err != nil || len(bytes) != sha256.Size {
		return "", fmt.Errorf("the saved Mythos login identity is unreadable")
	}
	return digest, nil
}

// SeedMythosSelection records a box's first linked Mythos login under the caller's root lock.
func SeedMythosSelection(root, target string) error {
	current, err := SelectedMythosDigest(root)
	if err != nil || current != "" {
		return err
	}
	return SelectMythosLogin(root, target)
}

func SelectMythosLogin(root, target string) error {
	digest := filepath.Base(target)
	if bytes, err := hex.DecodeString(digest); err != nil || len(bytes) != sha256.Size || filepath.Dir(target) != MythosLoginRoot(root) {
		return fmt.Errorf("invalid Mythos login directory")
	}
	return atomicfile.Write(filepath.Join(root, "mythos", "selected"), []byte(digest+"\n"), atomicfile.Opts{})
}
