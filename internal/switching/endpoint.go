package switching

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/termsafe"
)

// getenv reads the process environment for the second-key warning; tests
// replace it.
var getenv = os.Getenv

func claudeSettingsPath() string { return paths.GetClaudeSettingsPath() }

// endpointIsLive reports whether Claude Code's settings.json carries
// tycswap's endpoint profile: the record exists and names the live settings
// file. With nothing in the credential store, that is the state a switch onto
// an account with a base URL leaves, whatever the account's record says now
// (its URL may have been removed by a refresh since).
func endpointIsLive(s *store.Store) bool {
	return ccsettings.RecordsFile(ProfileSidecarPath(s), claudeSettingsPath())
}

func ProfileSidecarPath(s *store.Store) string {
	return filepath.Join(s.BackupDir(), ccsettings.SidecarName)
}

func endpointFor(data *store.SequenceData, num string) (string, error) {
	raw := store.BaseURLFrom(data, num)
	if raw == "" {
		return "", nil
	}
	v, err := ccsettings.ValidateBaseURL(raw)
	if err != nil {
		return "", cerr.Switch("Account-%s carries an invalid base URL (%v). Re-add it with: tycswap add-token --base-url URL --slot %s", num, err, num)
	}
	return v, nil
}

type profilePlan struct {
	settingsPath string
	sidecarPath  string
	apply        *ccsettings.Profile
	revert       bool
	known        []ccsettings.Profile
	// snap is both files as they were, for the rollback; nil when the plan
	// touches nothing.
	snap              *ccsettings.Snapshot
	applied, reverted bool
	// warnings name what would still send a second key to the endpoint
	// (ccsettings.Competing); shown once the profile is applied.
	warnings []string
}

func (p *profilePlan) touches() bool {
	return p != nil && (p.apply != nil || p.revert || len(p.known) > 0)
}

func (p *profilePlan) changed() bool { return p != nil && (p.applied || p.reverted) }

// planProfile decides what a switch onto target does to settings.json, and
// refuses before anything is written when it cannot be done: an invalid URL,
// a stored key that cannot be sent as a header, an unparseable settings file,
// a corrupt record, or an API-key account with no URL whose key is not an
// Anthropic key (writing that into the credential store would make an OAuth
// credential of it).
func planProfile(s *store.Store, data *store.SequenceData, target, targetCreds string) (*profilePlan, error) {
	p := &profilePlan{settingsPath: claudeSettingsPath(), sidecarPath: ProfileSidecarPath(s)}
	if targetCreds == "" {
		// Nothing to switch to: the switch stops at its own check for a
		// stored credential, with that error, before it writes anything.
		return p, nil
	}
	endpoint, err := endpointFor(data, target)
	if err != nil {
		return nil, err
	}
	if endpoint != "" {
		token, err := ccsettings.ValidateToken(targetCreds)
		if err != nil {
			return nil, cerr.Switch("Account-%s's stored key cannot be written for its endpoint (%v). Re-add it with: tycswap add-token --base-url URL --slot %s", target, err, target)
		}
		p.apply = &ccsettings.Profile{BaseURL: endpoint, Token: token}
		host := termsafe.Strip(ccsettings.Host(endpoint))
		for _, name := range ccsettings.Competing(p.settingsPath, getenv) {
			switch name {
			case ccsettings.CompetingHelper:
				p.warnings = append(p.warnings, "Claude Code's settings.json sets apiKeyHelper: Claude Code sends its key as X-Api-Key to "+
					host+" beside this account's key. Remove it while this account is active if that endpoint must not see it.")
			case ccsettings.CompetingEnvKey:
				p.warnings = append(p.warnings, "ANTHROPIC_API_KEY is set in this environment: a Claude Code started from it sends that key as X-Api-Key to "+
					host+" beside this account's key. Unset it there if that endpoint must not see it.")
			}
		}
	} else if s.AccountKindFor(target) == "api_key" && !credstore.LooksLikeAPIKey(targetCreds) {
		return nil, cerr.Switch("Account-%s's key is not an Anthropic API key and the account has no base URL. Re-add it with: tycswap add-token --base-url URL --slot %s", target, target)
	}

	if ccsettings.SidecarExists(p.sidecarPath) {
		p.revert = p.apply == nil
	} else if live := ccsettings.Live(p.settingsPath); live.BaseURL != "" && live.Token != "" {
		// No record. A profile left without one (its sidecar removed by
		// hand) is still taken out when settings.json holds exactly an
		// endpoint tycswap knows and its key, and is not recorded as the
		// user's when another endpoint is written over it. A file that
		// cannot be read carries nothing here: it never stops a switch that
		// would not otherwise write it.
		p.known = knownProfiles(s, data)
	}
	if !p.touches() {
		return p, nil
	}
	if p.apply != nil || p.revert {
		if err := ccsettings.Check(p.settingsPath, p.sidecarPath); err != nil {
			return nil, cerr.Config("Cannot update Claude Code's settings: %v", err).Wrap(err)
		}
	}
	snap, err := ccsettings.Take(p.sidecarPath, p.settingsPath, ccsettings.RecordedSettingsPath(p.sidecarPath))
	if err != nil {
		if p.apply == nil && !p.revert {
			p.known = nil // the by-value cleanup is best-effort
			return p, nil
		}
		return nil, cerr.Config("Cannot snapshot Claude Code's settings before the switch: %v", err).Wrap(err)
	}
	p.snap = snap
	return p, nil
}

func knownProfiles(s *store.Store, data *store.SequenceData) []ccsettings.Profile {
	if data == nil {
		return nil
	}
	var out []ccsettings.Profile
	for num, raw := range data.Accounts {
		endpoint, err := endpointFor(data, num)
		if err != nil || endpoint == "" {
			continue
		}
		key, _ := s.ReadAccountCredentials(num, recStr(decodeRec(raw), "email"))
		if key == "" {
			continue
		}
		out = append(out, ccsettings.Profile{BaseURL: endpoint, Token: key})
	}
	return out
}

// ownStoredKey reports whether live, the credential found in Claude Code's
// store, is the account's own stored one.
func ownStoredKey(s *store.Store, num, email, live string) bool {
	stored, _ := s.ReadAccountCredentials(num, email)
	return stored != "" && strings.TrimSpace(stored) == strings.TrimSpace(live)
}

// writeActiveFor writes the target's credential into Claude Code's credential
// store: the stored blob or key as always, or, for an endpoint account,
// nothing at all (ClearActive), since its key goes into settings.json.
func writeActiveFor(s *store.Store, p *profilePlan, targetCreds string) error {
	if p != nil && p.apply != nil {
		return s.Creds.ClearActive()
	}
	return s.Creds.WriteActiveAccount(targetCreds)
}

func (p *profilePlan) commit(s *store.Store) error {
	if !p.touches() {
		return nil
	}
	if p.apply != nil {
		if err := ccsettings.Apply(p.settingsPath, p.sidecarPath, *p.apply, p.known); err != nil {
			return cerr.Config("Cannot write the endpoint into Claude Code's settings: %v", err).Wrap(err)
		}
		p.applied = true
		if s.Log != nil {
			s.Log.Infof("Wrote the account's endpoint into %s", p.settingsPath)
		}
		return nil
	}
	out, err := ccsettings.Revert(p.settingsPath, p.sidecarPath, p.known)
	if err != nil {
		if p.revert {
			return cerr.Config("Cannot restore Claude Code's settings: %v", err).Wrap(err)
		}
		if s.Log != nil {
			s.Log.Warningf("Could not take an endpoint without a record out of Claude Code's settings: %v", err)
		}
		return nil
	}
	p.reverted = out != ccsettings.RevertedNothing
	if s.Log != nil {
		switch out {
		case ccsettings.RevertedFromRecord:
			s.Log.Infof("Restored Claude Code's settings from the record of what they held before the endpoint")
		case ccsettings.RevertedByValue:
			s.Log.Infof("Took an endpoint tycswap wrote without a record out of Claude Code's settings")
		}
	}
	return nil
}

// warn shows the plan's warnings: printed, or carried in the switch's JSON
// warnings.
func (p *profilePlan) warn(emitOutput bool, warningsOut *[]string) {
	if p == nil || !p.applied {
		return
	}
	for _, w := range p.warnings {
		if emitOutput {
			printWarning(w)
		} else {
			*warningsOut = append(*warningsOut, w)
		}
	}
}

func (p *profilePlan) restore() error {
	if p == nil || p.snap == nil {
		return nil
	}
	return p.snap.Restore()
}
