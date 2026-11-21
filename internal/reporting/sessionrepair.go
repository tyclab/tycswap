package reporting

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/procdetect"
)

var errNoSessionBinding = errors.New("session has no linked login binding")

// SessionLinkRecovery enables repair by an observer or an explicit use command.
// Only the caller's own link supplies launch metadata.
type SessionLinkRecovery struct {
	LaunchFamily   string
	ObservedFamily string
	FableBinding   string
}

// sessionLinkForUpdate runs under both tycswap locks. Repair only a genuinely absent
// binding: a damaged row or a recorded link with a different owner remains a refusal.
func sessionLinkForUpdate(root, id, path string, records map[string]defaultSessionModel, now time.Time, recovery []SessionLinkRecovery) (SessionLink, error) {
	link, err := currentSessionLink(root, id, path, records)
	if !errors.Is(err, errNoSessionBinding) || len(recovery) == 0 {
		return link, err
	}
	id, path, err = locateSessionLink(root, id, path)
	if err != nil {
		return SessionLink{}, err
	}
	if !procdetect.SessionUUID.MatchString(id) || !IsSessionLink(root, path) {
		return SessionLink{}, errNoSessionBinding
	}
	target, err := ResolveSessionLink(root, path)
	if err != nil {
		return SessionLink{}, err
	}
	scope, _ := targetScope(root, target)
	source := recovery[0]
	rec := records[id]
	if scope == "fable" {
		// A v0.19.0 writer retained the recovered Fable identity even when
		// it identified this linked session's current store as the default.
		binding := rec.Binding
		if binding == "" {
			binding = source.FableBinding
		}
		if binding != "" {
			digest, ok := strings.CutPrefix(binding, "fable/")
			if _, err := hex.DecodeString(digest); !ok || len(digest) != 64 || err != nil {
				return SessionLink{}, errors.New("invalid recovered Fable session binding")
			}
			scope = binding
		}
	}
	family := source.ObservedFamily
	if family == "" {
		family = rec.Family
	}
	if family == "" {
		family = source.LaunchFamily
	}
	if err := bindSessionLink(root, id, path, family, scope, source.LaunchFamily == "mythos", now, records); err != nil {
		return SessionLink{}, err
	}
	return currentSessionLink(root, id, path, records)
}

// locateSessionLink joins the native session registry to the launch token's
// PID and /proc start time. A process with multiple possible links cannot
// identify one for an outside caller.
func locateSessionLink(root, id, path string) (string, string, error) {
	var owner int
	var stamp string
	if path != "" {
		if !IsSessionLink(root, path) {
			return "", "", errNoSessionBinding
		}
		owner, stamp, _ = linkOwner(filepath.Base(path))
	}
	sessions, err := procdetect.LocalSessions(procdetect.GetClaudeDir())
	if err != nil {
		return "", "", err
	}
	var match *procdetect.ClaudeSession
	for i := range sessions {
		s := &sessions[i]
		sameProcess := s.PID == owner && s.ProcStart == stamp
		if path != "" && (sameProcess || id == s.SessionID) && !registryCoversLink(s, path) {
			continue
		}
		if owner != 0 && id != "" && (sameProcess != (id == s.SessionID)) {
			return "", "", errors.New("native session and link identify different processes")
		}
		if owner != 0 && !sameProcess || id != "" && s.SessionID != id {
			continue
		}
		if match != nil {
			return "", "", errors.New("multiple live processes match the session")
		}
		match = s
	}
	// The first status line can precede the native registry entry. Its
	// inherited launch path and reported session ID supply both halves.
	if path != "" && id != "" {
		return id, path, nil
	}
	if match == nil {
		return "", "", errNoSessionBinding
	}
	if path != "" {
		return match.SessionID, path, nil
	}
	dir := filepath.Join(root, "links")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}
	for _, entry := range entries {
		pid, stamp, valid := linkOwner(entry.Name())
		if entry.Type()&os.ModeSymlink == 0 || !valid || pid != match.PID || stamp != match.ProcStart {
			continue
		}
		if path != "" {
			return "", "", errors.New("multiple session links match the process")
		}
		path = filepath.Join(dir, entry.Name())
	}
	if path == "" || !registryCoversLink(match, path) {
		return "", "", errNoSessionBinding
	}
	return match.SessionID, path, nil
}

// A PID without a native start stamp could have been reused since an old
// registry file was written. Such a file must be at least as new as this link.
func registryCoversLink(session *procdetect.ClaudeSession, path string) bool {
	if session.PIDOnlyAt.IsZero() {
		return true
	}
	info, err := os.Lstat(path)
	return err == nil && !session.PIDOnlyAt.Before(info.ModTime())
}
