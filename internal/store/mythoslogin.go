package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
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

func (s *Store) MythosLoginReady(data *SequenceData, num string) (string, bool) {
	rec, ok := recordFor(data, num)
	if !ok || DisabledFrom(data, num) || kindFrom(data, num) != "oauth" || !MythosFrom(data, num) {
		return "", false
	}
	email, org := strField(rec, "email"), strField(rec, "organizationUuid")
	dir := MythosLoginDir(s.BackupDir(), email, org)
	raw, err := os.ReadFile(filepath.Join(dir, ccfile.CredentialsFileName))
	if err != nil || oauth.ExtractAccessToken(string(raw)) == "" {
		return "", false
	}
	return dir, true
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
