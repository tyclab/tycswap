package reporting

import (
	"crypto/rand"
	"encoding/hex"
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

func withLinkRecords(root string, fn func(map[string]defaultSessionModel) error) error {
	if _, err := linksDir(root); err != nil {
		return err
	}
	return filelock.New(filepath.Join(root, ".lock"), filelock.DefaultTimeout).With(func() error {
		return filelock.New(filepath.Join(root, ".default_models.lock"), filelock.DefaultTimeout).With(func() error {
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
	return withLinkRecords(root, func(records map[string]defaultSessionModel) error {
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
		if !procdetect.RecordAlive(pid, start, "") {
			return errors.New("session process ended")
		}
		for other, rec := range records {
			if rec.Link != nil && rec.Link.Token == token && id != other {
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
			SharedMythos: family == "mythos" && targetStore == "" && (!ok || old.SharedMythos)}
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
		return saveSessionModels(root, records)
	})
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

func UpdateSessionLink(root, id, path string, now time.Time, decide func(*SessionLink) error) error {
	return withLinkRecords(root, func(records map[string]defaultSessionModel) error {
		if id == "" && IsSessionLink(root, path) {
			for candidate, rec := range records {
				if rec.Link != nil && rec.Link.Token == filepath.Base(path) {
					id = candidate
					break
				}
			}
		}
		old := records[id]
		link, ok, err := savedLink(root, id, old)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("session has no linked login binding")
		}
		bound := filepath.Join(root, "links", link.Token)
		if path != "" && path != bound {
			return errors.New("session does not own this link")
		}
		target, err := ResolveSessionLink(root, bound)
		if err != nil {
			return err
		}
		if target != link.Target || !procdetect.RecordAlive(link.PID, link.ProcStart, "") {
			return errors.New("session link binding is no longer current")
		}
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
		return withSessionRefreshLock(target, cclock.DefaultTimeoutS, func() error {
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
		return nil
	}
	return withLinkRecords(root, func(records map[string]defaultSessionModel) error {
		dir := filepath.Join(root, "links")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var ended []string
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			token, staged := strings.CutPrefix(entry.Name(), ".")
			pid, start, owned := linkOwner(token)
			if owned && (staged || !procdetect.RecordAlive(pid, start, "")) {
				ended = append(ended, filepath.Join(dir, entry.Name()))
			}
		}
		if len(ended) == 0 {
			return nil
		}
		// Save recovered bindings before deleting the links that resolved a pending move.
		if len(records) > 0 {
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
