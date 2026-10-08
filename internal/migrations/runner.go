package migrations

import "fmt"

type migrationFunc func(host Host) (completed bool, notices []string, err error)

type migrationEntry struct {
	id string
	fn migrationFunc
}

// registry is MIGRATIONS from migrations.py, preserved in full per spec
// 07§9: both migrations exist to rescue data from storage backends a
// from-scratch Go binary never wrote to itself, but that a user upgrading
// from an old Python claude-swap release may still be sitting on.
var registry = []migrationEntry{
	{"windows_keyring_to_files", migrateWindowsKeyringToFiles},
	{"macos_keyring_to_security", migrateMacOSKeyringToSecurity},
}

// Run never errors or panics out: a failed migration stays unmarked and retries next launch. store.New calls it last; it must never abort construction.
func Run(host Host) []string {
	if !dirExists(host.BackupDir()) {
		return nil
	}

	statePath := host.StateFilePath()
	applied := loadApplied(statePath)

	var notices []string
	for _, m := range registry {
		if _, ok := applied[m.id]; ok {
			continue
		}
		completed, ns, err := runOne(host, m)
		notices = append(notices, ns...)
		if err != nil {
			host.Logger().Warningf("Migration %s did not complete (will retry): %v", m.id, err)
			continue
		}
		if !completed {
			continue // skip / not applicable — record nothing (silent)
		}
		if err := markApplied(statePath, host.Clock(), m.id); err != nil {
			host.Logger().Warningf("Migration %s ran but recording it failed (will re-run next time): %v", m.id, err)
		}
	}
	return notices
}

// runOne calls m.fn, converting a panic into an error so a single broken
// migration can never bring down construction (Run's "never raises"
// contract, hardened beyond Python's blanket `except Exception` — which only
// needs to catch exceptions, not the panic-shaped failures a Go port can
// also produce).
func runOne(host Host, m migrationEntry) (completed bool, notices []string, err error) {
	defer func() {
		if r := recover(); r != nil {
			completed, notices, err = false, nil, fmt.Errorf("panic: %v", r)
		}
	}()
	return m.fn(host)
}
