package reporting

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/cclock"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/procdetect"
	"github.com/tyclab/tycswap/internal/store"
)

type SessionLink struct {
	ID             string `json:"-"`
	Family         string `json:"-"`
	Token          string `json:"token"`
	PID            int    `json:"pid"`
	ProcStart      string `json:"procStart"`
	Target         string `json:"target"`
	Wanted         string `json:"wanted,omitempty"`
	ResumeMythos   string `json:"resumeMythos,omitempty"`
	Question       string `json:"question,omitempty"`
	SharedMythos   bool   `json:"sharedMythos,omitempty"`
	ObservePending bool   `json:"observePending,omitempty"`
}

func linkOwner(token string) (int, string, bool) {
	parts := strings.Split(token, "-")
	if len(parts) != 3 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(parts[0])
	start, e := strconv.ParseUint(parts[1], 10, 64)
	_, hexErr := hex.DecodeString(parts[2])
	return pid, parts[1], err == nil && pid > 1 && e == nil && start > 0 && len(parts[2]) == 32 && hexErr == nil
}

func IsSessionLink(root, path string) bool {
	_, _, ok := linkOwner(filepath.Base(path))
	return runtime.GOOS == "linux" && ok && filepath.Dir(path) == filepath.Join(root, "links") && filepath.Clean(path) == path
}

func targetScope(root, target string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch target {
	case filepath.Join(home, ".claude"):
		return "", nil
	case paths.FableStoreDir(root):
		return "fable", nil
	}
	digest := filepath.Base(target)
	if filepath.Dir(target) == store.MythosLoginRoot(root) && len(digest) == 64 {
		if _, err := hex.DecodeString(digest); err == nil {
			return "mythos/" + digest, nil
		}
	}
	return "", errors.New("invalid session login folder")
}

func checkTarget(root, target string) error {
	scope, err := targetScope(root, target)
	if err != nil {
		return err
	}
	dir, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if scope != "" {
		base, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, target)
		if err != nil || dir != filepath.Join(base, rel) {
			return errors.New("login folder contains a symlink")
		}
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("session login is not a directory")
	}
	return nil
}

func linksDir(root string) (string, error) {
	dir := filepath.Join(root, "links")
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0700 {
		return "", errors.New("session links require a private links directory")
	}
	return dir, nil
}

func ResolveSessionLink(root, path string) (string, error) {
	if !IsSessionLink(root, path) {
		return "", errors.New("invalid session link path")
	}
	if _, err := linksDir(root); err != nil {
		return "", err
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if err := checkTarget(root, target); err != nil {
		return "", err
	}
	return target, nil
}

func CreateSessionLink(root, target string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("session links require Linux")
	}
	if warn, err := paths.CheckPrivateRoot(root); err != nil {
		return "", err
	} else if warn != "" {
		return "", errors.New(warn)
	}
	if err := checkTarget(root, target); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return "", err
	}
	dir := filepath.Join(root, "links")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if _, err := linksDir(root); err != nil {
		return "", err
	}
	start, ok := procdetect.ProcStartTime(os.Getpid())
	if !ok {
		return "", errors.New("cannot identify the session process")
	}
	token := fmt.Sprintf("%d-%s-%x", os.Getpid(), start, randBytes())
	path := filepath.Join(dir, token)
	if err := os.Symlink(target, path); err != nil {
		return "", err
	}
	_, err := ResolveSessionLink(root, path)
	if err == nil && filepath.Dir(target) == store.MythosLoginRoot(root) {
		err = filelock.New(filepath.Join(root, ".lock"), filelock.DefaultTimeout).With(func() error {
			return store.SeedMythosSelection(root, target)
		})
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func randBytes() []byte {
	var b [16]byte
	rand.Read(b[:])
	return b[:]
}

func savedLink(root, id string, rec defaultSessionModel) (SessionLink, bool, error) {
	if rec.bad != nil {
		return SessionLink{}, false, rec.bad.err
	}
	if rec.Link == nil {
		return SessionLink{}, false, nil
	}
	link := *rec.Link
	link.ID, link.Family = id, rec.Family
	pid, start, ok := linkOwner(link.Token)
	scope, err := targetScope(root, link.Target)
	if !ok || link.PID != pid || link.ProcStart != start || err != nil || scope != rec.Store {
		return SessionLink{}, false, errors.New("invalid saved session link binding")
	}
	return link, true, nil
}

func LookupSessionLink(root, id string) (SessionLink, bool, error) {
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		return SessionLink{}, false, err
	}
	return savedLink(root, id, records[id])
}

func LinkedSessions(root string) ([]SessionLink, error) {
	records, err := loadDefaultSessionModels(root)
	if err != nil {
		return nil, err
	}
	var links []SessionLink
	for id, rec := range records {
		if rec.bad != nil {
			continue
		}
		link, ok, err := savedLink(root, id, rec)
		if err != nil {
			return nil, err
		}
		if ok && procdetect.RecordAlive(link.PID, link.ProcStart, "") {
			links = append(links, link)
		}
	}
	return links, nil
}

func withLinkRecords(root string, deadline time.Time, fn func(map[string]defaultSessionModel) error) error {
	if _, err := linksDir(root); err != nil {
		return err
	}
	return withSessionRecords(root, deadline, fn)
}

func withSessionRecords(root string, deadline time.Time, fn func(map[string]defaultSessionModel) error) error {
	withLock := func(name string, body func() error) error {
		timeout := filelock.DefaultTimeout
		if !deadline.IsZero() {
			timeout = time.Until(deadline)
			if timeout <= 0 {
				return cerr.Lock("Timed out waiting for %s", name)
			}
		}
		return filelock.New(filepath.Join(root, name), timeout).With(body)
	}
	return withLock(".lock", func() error {
		return withLock(".default_models.lock", func() error {
			// flock can succeed on its final poll, just past the acquisition deadline.
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return cerr.Lock("Timed out waiting for the session store locks")
			}
			records, err := loadDefaultSessionModels(root)
			if err != nil {
				return err
			}
			return fn(records)
		})
	})
}

func BindSessionLink(root, id, path, model, scope string, now time.Time) error {
	if id == "" || !IsSessionLink(root, path) {
		return errors.New("missing session id or invalid session link")
	}
	return withLinkRecords(root, time.Time{}, func(records map[string]defaultSessionModel) error {
		return bindSessionLink(root, id, path, model, scope, true, now, records)
	})
}

// bindSessionLink runs with both tycswap locks held and saves the record before returning.
func bindSessionLink(root, id, path, model, scope string, sharedMythos bool, now time.Time, records map[string]defaultSessionModel) error {
	target, err := ResolveSessionLink(root, path)
	if err != nil {
		return err
	}
	targetStore, _ := targetScope(root, target)
	binding := ""
	if targetStore == "fable" && strings.HasPrefix(scope, "fable/") {
		binding, scope = scope, "fable"
	}
	if targetStore != scope {
		return errors.New("session store changed before binding")
	}
	token := filepath.Base(path)
	pid, start, _ := linkOwner(token)
	if actual, alive := procdetect.ProcStartTime(pid); !alive || actual != start {
		return errors.New("session process ended")
	}
	for other, rec := range records {
		if id != other && recordNamesToken(rec, token) {
			return errors.New("link is already bound to another session")
		}
	}
	old, ok, err := savedLink(root, id, records[id])
	if err != nil {
		return err
	}
	if ok && old.Token != token && procdetect.RecordAlive(old.PID, old.ProcStart, "") {
		return errors.New("session already has a live link")
	}
	if ok && old.Token == token {
		return nil
	}
	family, wanted := ClaudeFamily(model), old.Wanted
	if family == "" {
		family = old.Family
	} else if family != old.Family && wanted != "" && SessionLoginForFamily(old.Family) == wanted && SessionLoginForFamily(family) != wanted {
		wanted = ""
	}
	link := SessionLink{Token: token, PID: pid, ProcStart: start, Target: target, Wanted: wanted, Family: family, ObservePending: true,
		SharedMythos: family == "mythos" && targetStore == "" && sharedMythos && (!ok || old.SharedMythos)}
	if ok && (wanted == "mythos" || wanted == "" && family == "mythos") {
		digest := old.ResumeMythos
		if digest == "" {
			if recorded, dedicated := strings.CutPrefix(records[id].Store, "mythos/"); dedicated {
				digest = recorded
			}
		}
		if digest != "" && targetStore != "mythos/"+digest {
			link.ResumeMythos = digest
		}
	}
	rec := defaultSessionModel{Family: link.Family, Store: targetStore, Binding: binding, Link: &link, SeenAt: float64(now.UnixNano()) / 1e9}
	records[id] = rec
	expireSessionModels(records, now)
	return saveSessionModels(root, records)
}

// A damaged sibling still owns a token that is recognizable in its saved link.
func recordNamesToken(rec defaultSessionModel, token string) bool {
	if rec.bad == nil {
		return rec.Link != nil && rec.Link.Token == token
	}
	var saved struct {
		Link struct{ Token string }
	}
	return json.Unmarshal(rec.bad.raw, &saved) == nil && saved.Link.Token == token
}

func SessionLoginForFamily(family string) string {
	if family == "mythos" || family == "fable" {
		return family
	}
	return "default"
}

func withSessionRefreshLock(source string, timeout time.Duration, fn func() error) error {
	lock, err := cclock.AcquireOAuthRefresh(source, timeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	return fn()
}

func committedLinkRecord(root string, rec defaultSessionModel) (defaultSessionModel, error) {
	old := rec.Before
	rec.Before = nil
	if old.Link == nil || rec.Link == nil || old.Link.Token != rec.Link.Token || old.Before != nil {
		return rec, errors.New("invalid pending session move")
	}
	path := filepath.Join(root, "links", rec.Link.Token)
	if !IsSessionLink(root, path) {
		return rec, errors.New("invalid pending session link")
	}
	actual, err := os.Readlink(path)
	switch {
	case os.IsNotExist(err):
		// With no commit point the last confirmed identity is the only one safe to resume.
		if old.Link.ResumeMythos != "" && old.binding() == "" {
			old.Binding = "mythos/" + old.Link.ResumeMythos
		}
		old.Link = nil
		return *old, nil
	case err != nil:
		return rec, err
	case actual == rec.Link.Target:
		return rec, nil
	case actual == old.Link.Target:
		return *old, nil
	default:
		return rec, errors.New("pending session move points at an unexpected folder")
	}
}

func currentSessionLink(root, id, path string, records map[string]defaultSessionModel) (SessionLink, error) {
	if id == "" && IsSessionLink(root, path) {
		for candidate, rec := range records {
			if rec.Link != nil && rec.Link.Token == filepath.Base(path) {
				id = candidate
				break
			}
		}
	}
	link, ok, err := savedLink(root, id, records[id])
	if err != nil {
		return SessionLink{}, err
	}
	if !ok {
		return SessionLink{}, errNoSessionBinding
	}
	bound := filepath.Join(root, "links", link.Token)
	if path != "" && path != bound {
		return SessionLink{}, errors.New("session does not own this link")
	}
	target, err := ResolveSessionLink(root, bound)
	if err != nil {
		return SessionLink{}, err
	}
	if target != link.Target || !procdetect.RecordAlive(link.PID, link.ProcStart, "") {
		return SessionLink{}, errors.New("session link binding is no longer current")
	}
	return link, nil
}

// UseSessionLink retries client contention. Each attempt reads the binding and
// recomputes the proposed move. The decision must have no external side effects.
func UseSessionLink(root, id, path string, now time.Time, decide func(*SessionLink) error, recovery ...SessionLinkRecovery) error {
	var source SessionLink
	return cclock.RetryOAuthRefresh(func(deadline time.Time) error {
		return updateSessionLink(root, id, path, now, deadline, &source, decide, recovery...)
	})
}

// UpdateSessionLink defers automatic moves when the client's refresh lock is busy.
func UpdateSessionLink(root, id, path string, now time.Time, decide func(*SessionLink) error, recovery ...SessionLinkRecovery) error {
	return updateSessionLink(root, id, path, now, time.Time{}, nil, decide, recovery...)
}

func updateSessionLink(root, id, path string, now, deadline time.Time, original *SessionLink, decide func(*SessionLink) error, recovery ...SessionLinkRecovery) error {
	return withLinkRecords(root, deadline, func(records map[string]defaultSessionModel) error {
		if original != nil && original.Token != "" {
			// A legacy writer can strip this binding between attempts. Give
			// repair the verified ID and link, and check its source before it writes.
			id, path = original.ID, filepath.Join(root, "links", original.Token)
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if target != original.Target {
				return errors.New("the session binding changed while waiting for the refresh lock. Retry the move.")
			}
		}
		link, err := sessionLinkForUpdate(root, id, path, records, now, recovery)
		if err != nil {
			return err
		}
		if original != nil && original.Token == "" {
			*original = link
		}
		id = link.ID
		old := records[id]
		before := link
		if err := decide(&link); err != nil {
			return err
		}
		if link.Family != "mythos" || link.Target != before.Target || link.Wanted != before.Wanted {
			link.SharedMythos = false
		}
		if link == before {
			return nil
		}
		if link.ID != before.ID || link.Token != before.Token || link.PID != before.PID || link.ProcStart != before.ProcStart {
			return errors.New("cannot change the session process binding")
		}
		scope, err := targetScope(root, link.Target)
		if err != nil {
			return err
		}
		next := defaultSessionModel{Link: &link, Family: link.Family, Store: scope, SeenAt: float64(now.UnixNano()) / 1e9}
		if link.Target == before.Target {
			next.Binding = old.Binding
			records[id] = next
			return saveSessionModels(root, records)
		}
		if err := checkTarget(root, link.Target); err != nil {
			return err
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return errors.New("timed out preparing the session move")
		}
		return withSessionRefreshLock(before.Target, 0, func() error {
			bound := filepath.Join(root, "links", link.Token)
			tmp := filepath.Join(root, "links", "."+link.Token)
			// An interrupted mover may have left its staging link before rename.
			if err := removeStagedLink(tmp); err != nil {
				return err
			}
			if err := os.Symlink(link.Target, tmp); err != nil {
				return err
			}
			defer os.Remove(tmp)
			// The symlink is the commit point. Readers recover either complete record after a crash.
			next.Before = &old
			records[id] = next
			if err := saveSessionModels(root, records); err != nil {
				return err
			}
			if strings.HasPrefix(scope, "mythos/") {
				if err := store.SeedMythosSelection(root, link.Target); err != nil {
					return err
				}
			}
			if err := os.Rename(tmp, bound); err != nil {
				return err
			}
			atomicfile.SyncDir(filepath.Dir(bound))
			next.Before = nil
			records[id] = next
			// If compaction fails, the durable pending record already describes the committed move.
			_ = saveSessionModels(root, records)
			return nil
		})
	})
}

func removeStagedLink(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return errors.New("unexpected file at staged session link")
	}
	return os.Remove(path)
}

func PruneSessionLinks(root string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := linksDir(root); os.IsNotExist(err) {
		if _, err := os.Stat(filepath.Join(root, defaultModelsFile)); os.IsNotExist(err) {
			return nil
		}
	} else if err != nil {
		return err
	}
	return withSessionRecords(root, time.Time{}, func(records map[string]defaultSessionModel) error {
		var entries []os.DirEntry
		dir, err := linksDir(root)
		if err == nil {
			entries, err = os.ReadDir(dir)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var ended []string
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".ask") {
				token := strings.TrimSuffix(entry.Name()[1:], ".ask")
				pid, start, valid := linkOwner(token)
				if valid && !procdetect.RecordAlive(pid, start, "") {
					ended = append(ended, filepath.Join(dir, entry.Name()))
				}
				continue
			}
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			token, staged := strings.CutPrefix(entry.Name(), ".")
			pid, start, owned := linkOwner(token)
			if owned && (staged || !procdetect.RecordAlive(pid, start, "")) {
				ended = append(ended, filepath.Join(dir, entry.Name()))
			}
		}
		dirty := expireSessionModels(records, time.Now())
		for _, rec := range records {
			if rec.recovered {
				dirty = true
				break
			}
		}
		if len(ended) == 0 && !dirty {
			return nil
		}
		// Save recovered bindings before deleting the links that resolved a pending move.
		if len(records) > 0 || dirty {
			if err := saveSessionModels(root, records); err != nil {
				return err
			}
		}
		for _, path := range ended {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}
