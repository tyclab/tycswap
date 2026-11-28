package lifecycle

import (
	"os"
	"strconv"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

// EmptyFableStoreWithin runs fn under the Fable store's locks, the store lock
// among them, and fn's empty empties the store in that hold
// (switching.EmptyFableWithin). core sets it to the switch by hand's, which
// also holds the Fable state lock; a test replaces it.
var EmptyFableStoreWithin = func(s *store.Store, fn func(empty func() error) error) error {
	_, err := switching.EmptyFableWithin(s, fn)
	return err
}

// AddFableLogin stores a `claude auth login` in a scratch profile as the Fable
// login of the roster account it signed in as (DESIGN A101). It writes the
// Fable login file only, under the store lock, keeping .prev, and creates the
// Fable directories only once the roster re-check there passed. When the account
// owns the Fable store's login, its last install included, the new login is
// installed there too, under the Fable store's locks: the store's older login
// would otherwise be harvested or backed up over the new file. A login
// replaced on the last install's assumption is stashed first.
func AddFableLogin(s *store.Store, src AddSource, slot *int) (string, error) {
	f, err := s.ForFable()
	if err != nil {
		return "", err
	}
	if !src.isLoginDir() {
		return "", errLoginIncomplete()
	}
	creds, _, err := src.material(s)
	if err != nil {
		return "", err
	}
	email, orgUUID, ok := src.identity(s)
	if !ok {
		return "", errLoginIncomplete()
	}
	data, err := s.ReadSequence()
	if err != nil {
		return "", err
	}
	num, err := fableLoginSlot(s, data, email, orgUUID, slot)
	if err != nil {
		return "", err
	}
	if err := refuseDefaultGrant(s, num, email, creds); err != nil {
		return "", err
	}
	live, _, _ := f.Creds.ReadActive()
	renew := live != "" && f.ResolveOwnerSlot(data).Slot == num
	write := func() error {
		current, err := s.ReadSequence()
		if err != nil {
			return err
		}
		if s.FindAccountSlot(current, email, orgUUID) != num {
			return cerr.Config("Account-%s changed while the sign-in was stored; nothing stored.", num)
		}
		if err := f.SetupFableDirectories(); err != nil {
			return err
		}
		return f.WriteAccountCredentials(num, email, creds)
	}
	if renew {
		err = f.WithFableLocks(func() error {
			now, _, _ := f.Creds.ReadActive()
			owner := fableOwnerOf(f)
			readers, err := store.ReadFableReaders(s.BackupDir())
			if err != nil {
				return err
			}
			if readers.Identity != "" && readers.Identity != store.AccountIdentityDigest(email, orgUUID) || now != live && owner.Slot != num {
				renew = false
				return write()
			}
			if owner.Slot == num && owner.Source == store.SourceLastInstall {
				if _, err := f.Creds.WriteUnclaimed(now, map[string]any{"lineage": "fable", "reason": store.SourceLastInstall, "slot": num}); err != nil {
					return err
				}
			}
			if err := write(); err != nil {
				return err
			}
			if err := f.Creds.WriteActiveAccount(creds); err != nil {
				return err
			}
			return f.RecordFableLive(num, creds)
		})
	} else {
		err = s.Lock.With(write)
	}
	if err != nil {
		return "", err
	}
	logInfof(s, "Stored the Fable login for account %s: %s", num, email)
	emitLine("Stored the Fable login for Account-" + num + " (" + email + ").")
	if renew {
		emitLine(FableStoreUses(num))
	}
	return num, nil
}

// FableStoreUses is the line printed when the Fable store takes an account.
func FableStoreUses(num string) string { return "The Fable store now uses Account-" + num + "." }

// fableLoginSlot is the OAuth roster account the sign-in names, refused when
// it is not stored, is not the --slot account, or has no Fable on its plan.
func fableLoginSlot(s *store.Store, data *store.SequenceData, email, orgUUID string, slot *int) (string, error) {
	num := s.FindAccountSlot(data, email, orgUUID)
	if num == "" || s.AccountKindFor(num) == "api_key" {
		return "", ErrFableNotStored(email)
	}
	if slot != nil {
		if want := strconv.Itoa(*slot); want != num {
			other := ""
			if rec, ok := recordAt(data, want); ok {
				other = " (" + rec.str("email") + ")"
			}
			return "", cerr.Validation("This sign-in is %s, not Account-%s%s; nothing stored.", email, want, other)
		}
	}
	if err := FablePlanRefusal(s, data, num); err != nil {
		return "", err
	}
	return num, nil
}

// refuseDefaultGrant refuses a credential whose refresh token is the
// account's default login or the live default credential: a Fable login is a
// second, independent sign-in.
func refuseDefaultGrant(s *store.Store, num, email, creds string) error {
	fp := oauth.CredentialFingerprint(creds)
	backup, _ := s.ReadAccountCredentials(num, email)
	live, _, _ := s.Creds.ReadActive()
	for _, other := range []string{backup, live} {
		if theirs := oauth.CredentialFingerprint(other); fp != nil && theirs != nil && *fp == *theirs {
			return cerr.Validation("This is Account-%s's default login, not a second sign-in; sign in again in the browser.", num)
		}
	}
	return nil
}

// refuseFableSessionConfig refuses to store the live login under the identity
// ~/.claude.json names when that is the Fable store's account and the default
// credential resolves to another: a Fable session wrote it there (DESIGN A101).
func refuseFableSessionConfig(s *store.Store, email, orgUUID string) error {
	fableSlot := fableLiveSlot(fableLogins(s))
	if fableSlot == "" {
		return nil
	}
	data, _ := s.ReadSequence()
	if s.FindAccountSlot(data, email, orgUUID) != fableSlot {
		return nil
	}
	if live := s.ResolveLiveSlot(data).Slot; live != "" && live != fableSlot {
		return cerr.Config("~/.claude.json names Account-%s, the Fable store's account, but the default login is Account-%s's; nothing stored.", fableSlot, live)
	}
	return nil
}

// fableLogins is s's Fable scope while credentials-fable/ exists, else nil:
// a store without Fable logins is left exactly as it was.
func fableLogins(s *store.Store) *store.Store {
	f, err := s.ForFable()
	if err != nil {
		return nil
	}
	if _, err := os.Stat(f.CredentialsDir); err != nil {
		return nil
	}
	return f
}

// fableLiveSlot is the Fable store's live account by backup or chain, "" when
// the store holds no login or its account is unknown.
func fableLiveSlot(f *store.Store) string {
	if f == nil {
		return ""
	}
	if st := f.FableStore(); st.On {
		return st.Live.Slot
	}
	return ""
}

// fableOwner is the account the Fable store's login belongs to, its last
// install when nothing else names it (store.ResolveOwnerSlot); "" when the
// store holds none or no account is known.
func fableOwner(f *store.Store) string { return fableOwnerOf(f).Slot }

func fableOwnerOf(f *store.Store) store.LiveSlot {
	if f == nil {
		return store.LiveSlot{}
	}
	data, _ := f.ReadSequence()
	return f.ResolveOwnerSlot(data)
}

// ownsFableLogin reports whether num owns the Fable store's login, its last
// install included.
func ownsFableLogin(s *store.Store, num string) bool {
	f, err := s.ForFable()
	return err == nil && f.HasLiveLogin() && fableOwner(f) == num
}

// deleteFableLogin removes num's Fable login and its .prev; the caller holds
// the store lock. A store the Fable store took back meanwhile refuses.
func deleteFableLogin(s *store.Store, num, email string) error {
	f := fableLogins(s)
	if f == nil {
		return nil
	}
	if fableOwner(f) == num {
		return cerr.Config("The Fable store took Account-%s again; nothing was removed. Retry the command.", num)
	}
	return f.Creds.DeleteBackupStrict(num, email)
}

// withRosterForDisplacing is withRosterEmptyingFable for add --slot: the
// Fable store's locks are held when the add is to put another identity into
// the slot of the Fable store's account. Otherwise reslotFableLogins refuses
// a slot the store took meanwhile.
func withRosterForDisplacing(s *store.Store, slot *int, email, orgUUID string, commit func(data *store.SequenceData, emptyFable func(num string) error) error) error {
	return withRosterEmptyingFable(s, displacesFableOwner(s, slot, email, orgUUID), commit)
}

// withRosterEmptyingFable runs commit under the store lock on the locked
// roster. With hold the Fable store's locks are held as well and commit's
// emptyFable(num) empties the store in that hold if num still owns its
// login: commit calls it once its last refusal is behind it, so a refused
// add or remove leaves the store as it was (DESIGN A130). Without hold
// emptyFable does nothing.
func withRosterEmptyingFable(s *store.Store, hold bool, commit func(data *store.SequenceData, emptyFable func(num string) error) error) error {
	if !hold {
		return s.WithRosterLocked(func(data *store.SequenceData) error {
			return commit(data, func(string) error { return nil })
		})
	}
	return EmptyFableStoreWithin(s, func(empty func() error) error {
		data, err := s.MigratedSequenceForUpdateLocked()
		if err != nil {
			return err
		}
		return commit(data, func(num string) error {
			if !ownsFableLogin(s, num) {
				return nil
			}
			return empty()
		})
	})
}

// refuseLiveSessions gives the session refusal DeleteAccountFiles gives for
// the slots an add deletes, the displaced one and the one the account moves
// from, before the add changes anything (DESIGN A130).
func refuseLiveSessions(s *store.Store, data *store.SequenceData, displaced *displaceInfo, migrateFrom string) error {
	if displaced != nil {
		if err := s.EnsureNoLiveSession(displaced.num, displaced.email, "the operation"); err != nil {
			return err
		}
	}
	if migrateFrom == "" {
		return nil
	}
	email := ""
	if rec, _ := recordAt(data, migrateFrom); rec != nil {
		email = rec.str("email")
	}
	return s.EnsureNoLiveSession(migrateFrom, email, "the operation")
}

func displacesFableOwner(s *store.Store, slot *int, email, orgUUID string) bool {
	f := fableLogins(s)
	if slot == nil || f == nil || !f.HasLiveLogin() {
		return false
	}
	data, _ := s.ReadSequence()
	num := strconv.Itoa(*slot)
	return slotOccupant(data, num, email, orgUUID) != nil && fableOwner(f) == num
}

// reslotFableLogins gives accountNum the Fable login of the identity add puts
// there (DESIGN A101): a displaced identity's goes, one moving in from
// migrateFrom brings its own, a new one has none, and one the locked roster
// data already holds there keeps its own. The caller holds the store lock and
// has deleted nothing yet. Each step that deletes or moves a Fable login runs
// beside the default side's: displace right after the displaced account's
// files are deleted, follow (the moved login, its old key, the chain record)
// once the roster is written, so a failure before either leaves the Fable
// logins where the roster still has their accounts.
func reslotFableLogins(s *store.Store, data *store.SequenceData, displaced *displaceInfo, migrateFrom, accountNum, email string) (displace func() error, follow func(), err error) {
	displace, follow = func() error { return nil }, func() {}
	fab := fableLogins(s)
	if fab == nil {
		return displace, follow, nil
	}
	if _, held := recordAt(data, accountNum); held && displaced == nil && migrateFrom == "" {
		return displace, follow, nil
	}
	if displaced != nil && fableOwner(fab) == displaced.num {
		return displace, follow, cerr.Config("The Fable store took Account-%s again; nothing was removed. Retry the command.", displaced.num)
	}
	creds := ""
	if migrateFrom != "" {
		creds = fableMaterial(fab, migrateFrom, email)
	}
	clearLeftover := func() error {
		if creds != "" {
			return nil
		}
		return fab.Creds.DeleteBackupStrict(accountNum, email)
	}
	if displaced != nil {
		displace = func() error {
			if err := fab.Creds.DeleteBackupStrict(displaced.num, displaced.email); err != nil {
				return err
			}
			return clearLeftover()
		}
	} else if err := clearLeftover(); err != nil {
		return displace, follow, err
	}
	if migrateFrom == "" {
		return displace, follow, nil
	}
	owner := fableOwnerOf(fab)
	follow = func() {
		if creds != "" {
			if err := fab.WriteAccountCredentials(accountNum, email, creds); err != nil {
				logWarningf(fab, "Could not move Account-%s's Fable login from slot %s; it stays there: %v", accountNum, migrateFrom, err)
				return
			}
			_ = fab.Creds.DeletePrev(accountNum, email)
		}
		_ = fab.Creds.DeleteBackup(migrateFrom, email)
		followFableChain(fab, owner, map[string]string{migrateFrom: accountNum})
	}
	return displace, follow, nil
}
