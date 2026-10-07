package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/store"
)

type migrationRecord struct {
	SourceSessionID      string    `json:"sourceSessionId"`
	SourceTranscript     string    `json:"sourceTranscript"`
	SourceProfile        string    `json:"sourceProfile"`
	DestinationSessionID string    `json:"destinationSessionId"`
	CWD                  string    `json:"cwd"`
	SourceHash           string    `json:"sourceHash"`
	CreatedAt            time.Time `json:"createdAt"`
}

type groupLaunchRecord struct {
	GroupLaunch
	SourceProfile   string    `json:"sourceProfile,omitempty"`
	SourceHash      string    `json:"sourceHash,omitempty"`
	ActualSessionID string    `json:"actualSessionId,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

func migrationsPath(root string, id groups.ID) string {
	return filepath.Join(groups.ScopeDir(root, id), "migrations.json")
}
func launchPath(root string, id groups.ID, launchID string) string {
	return filepath.Join(groups.ScopeDir(root, id), "launches", launchID+".json")
}

func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
		} else if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

func (m *Manager) prepareGroupResume(s *store.Store, launch *GroupLaunch, resume string) error {
	if resume == "" {
		return nil
	}
	links := map[string]migrationRecord{}
	if err := groups.ReadJSON(migrationsPath(s.SharedRoot(), s.GroupID()), &links); err != nil {
		return err
	}
	var linked []migrationRecord
	for _, link := range links {
		if resume == link.SourceSessionID || resume == link.SourceTranscript || resume == link.DestinationSessionID {
			linked = append(linked, link)
		}
	}
	if len(linked) > 1 {
		return fmt.Errorf("resume ID refers to several source conversations; provide its absolute transcript path")
	}
	if len(linked) == 1 {
		link := linked[0]
		if !validSessionID(link.DestinationSessionID) {
			return fmt.Errorf("recorded migrated session ID is invalid")
		}
		paths, err := transcriptPaths(launch.ProfileDir, link.DestinationSessionID)
		if err != nil {
			return err
		}
		if len(paths) != 1 {
			return fmt.Errorf("the migrated conversation %s is unavailable or ambiguous; inspect the group history before resuming", link.DestinationSessionID)
		}
		launch.SessionID = link.DestinationSessionID
		launch.SourceSessionID = link.SourceSessionID
		launch.SourceTranscript = link.SourceTranscript
		if launch.CWD == "" {
			launch.CWD = link.CWD
		}
		launch.Args = []string{"--resume", launch.SessionID}
		return groups.SafePath(paths[0])
	}
	profile, transcript, err := locateTranscript(s, resume)
	if err != nil {
		return err
	}
	sessionID := strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
	if !validSessionID(sessionID) {
		return fmt.Errorf("the selected transcript does not have a valid native session ID")
	}
	if samePath(profile, launch.ProfileDir) {
		if err := groups.SafePath(transcript); err != nil {
			return err
		}
		launch.SessionID = sessionID
		launch.Args = []string{"--resume", sessionID}
	} else {
		if err := store.VerifyProfileState(profile); err != nil {
			return err
		}
		sessions, err := procdetect.ListSessionsErr(profile)
		if err != nil {
			return err
		}
		for _, active := range sessions {
			if active.SessionID == "" {
				return fmt.Errorf("a live source-profile process has no readable session ID; resume is held until its state is known")
			}
			if active.SessionID == sessionID {
				return fmt.Errorf("the source conversation is still running (PID %d); exit it before opting into another group", active.PID)
			}
		}
		launch.Migrating = true
		launch.SourceSessionID = sessionID
		launch.SourceTranscript = transcript
		launch.Args = []string{"--resume", transcript, "--fork-session"}
	}
	savedCWD, err := transcriptCWD(transcript)
	if err != nil && launch.CWD == "" {
		return err
	}
	if launch.CWD == "" {
		launch.CWD = savedCWD
	}
	return nil
}

func profileRoots(s *store.Store) ([]string, error) {
	profiles := []string{s.DefaultProfileDir()}
	for _, id := range groups.All() {
		profiles = append(profiles, groups.ProfileDir(s.SharedRoot(), id))
	}
	entries, err := os.ReadDir(filepath.Join(s.SharedRoot(), "sessions"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			profiles = append(profiles, filepath.Join(s.SharedRoot(), "sessions", entry.Name()))
		}
	}
	return profiles, nil
}

func locateTranscript(s *store.Store, resume string) (string, string, error) {
	profiles, err := profileRoots(s)
	if err != nil {
		return "", "", err
	}
	if !validSessionID(resume) {
		path, err := filepath.Abs(resume)
		if err != nil {
			return "", "", err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", "", err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || filepath.Ext(path) != ".jsonl" {
			return "", "", fmt.Errorf("resume must name a regular Claude conversation transcript")
		}
		for _, profile := range profiles {
			if withinProfile(profile, path) {
				return profile, path, nil
			}
		}
		return "", "", fmt.Errorf("the selected transcript is outside the default, legacy and managed profiles")
	}
	type match struct{ profile, path string }
	matches := []match{}
	seen := map[string]bool{}
	for _, profile := range append([]string{s.ProfileDir()}, profiles...) {
		paths, err := transcriptPaths(profile, resume)
		if err != nil {
			return "", "", err
		}
		for _, path := range paths {
			if !seen[path] {
				matches = append(matches, match{profile, path})
				seen[path] = true
			}
		}
		if samePath(profile, s.ProfileDir()) && len(matches) == 1 {
			return matches[0].profile, matches[0].path, nil
		}
	}
	if len(matches) == 0 {
		return "", "", fmt.Errorf("Claude conversation %s was not found in the known profiles", resume)
	}
	if len(matches) > 1 {
		return "", "", fmt.Errorf("Claude conversation %s exists in several profiles; provide its absolute transcript path", resume)
	}
	return matches[0].profile, matches[0].path, nil
}

func transcriptPaths(profile, sessionID string) ([]string, error) {
	root := filepath.Join(profile, "projects")
	projects, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		path := filepath.Join(root, project.Name(), sessionID+".jsonl")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("session transcript is not a regular file: %s", path)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, resolved)
	}
	return paths, nil
}

func transcriptCWD(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scan.Scan() {
		var item struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(scan.Bytes(), &item) == nil && item.CWD != "" {
			return item.CWD, nil
		}
	}
	return "", fmt.Errorf("the transcript has no readable working directory; provide the intended workspace explicitly")
}

func samePath(a, b string) bool {
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func withinProfile(profile, path string) bool {
	root := filepath.Join(profile, "projects")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func transcriptHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func saveGroupLaunch(root string, launch *GroupLaunch) error {
	record := groupLaunchRecord{GroupLaunch: *launch, CreatedAt: time.Now().UTC()}
	if launch.Migrating {
		var err error
		record.SourceHash, err = transcriptHash(launch.SourceTranscript)
		if err != nil {
			return err
		}
		record.SourceProfile = filepath.Dir(filepath.Dir(filepath.Dir(launch.SourceTranscript)))
	}
	return groups.WriteJSON(launchPath(root, launch.Group, launch.LaunchID), record)
}

func RecordGroupSession(root string, group groups.ID, launchID, sessionID, cwd string) error {
	if launchID == "" {
		return nil
	}
	if !validSessionID(launchID) || !validSessionID(sessionID) {
		return fmt.Errorf("invalid group launch or native session ID")
	}
	if _, err := groups.Parse(string(group)); err != nil {
		return err
	}
	return filelock.New(filepath.Join(root, ".lock"), 0).With(func() error {
		path := launchPath(root, group, launchID)
		var record groupLaunchRecord
		if err := groups.ReadJSON(path, &record); err != nil {
			return err
		}
		if record.LaunchID != launchID || record.Group != group {
			return fmt.Errorf("group launch record is absent or inconsistent")
		}
		if cwd != "" && !samePath(cwd, record.CWD) {
			return fmt.Errorf("native session working directory disagrees with its managed launch")
		}
		if record.ActualSessionID != "" && record.ActualSessionID != sessionID {
			return nil
		}
		if record.Migrating {
			if sessionID == record.SourceSessionID {
				return fmt.Errorf("native migration reused its source ID; no migration is recorded")
			}
			currentHash, err := transcriptHash(record.SourceTranscript)
			if err != nil || currentHash != record.SourceHash {
				return fmt.Errorf("the source transcript changed during migration; no destination link is recorded")
			}
			links := map[string]migrationRecord{}
			if err := groups.ReadJSON(migrationsPath(root, group), &links); err != nil {
				return err
			}
			links[record.SourceTranscript] = migrationRecord{SourceSessionID: record.SourceSessionID, SourceTranscript: record.SourceTranscript, SourceProfile: record.SourceProfile, DestinationSessionID: sessionID, CWD: record.CWD, SourceHash: record.SourceHash, CreatedAt: time.Now().UTC()}
			if err := groups.WriteJSON(migrationsPath(root, group), links); err != nil {
				return err
			}
		}
		record.ActualSessionID = sessionID
		return groups.WriteJSON(path, record)
	})
}
