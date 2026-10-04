// endpoint.go — what a switch does to Claude Code's settings.json (DESIGN
// A46).
//
// An API-key account may carry a base URL. A switch onto such an account
// stores no key in Claude Code's credential store (no primaryApiKey, no
// managed Keychain item): it takes every login off that store and writes the
// endpoint and the key into settings.json as env.ANTHROPIC_BASE_URL and
// env.ANTHROPIC_AUTH_TOKEN, through ccsettings, which records what those two
// keys held first. A switch onto any other account puts them back from that
// record. Every path that writes the live login does this — the normal
// switch, --force and the other direct activations, the fresh machine — and a
// switch that fails after either file was written puts both back byte for
// byte from a snapshot taken before.
package switching

import (
	"path/filepath"

	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/credstore"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/store"
)

// claudeSettingsPath is Claude Code's own settings.json under the live config
// home, the one a switch writes the endpoint into.
func claudeSettingsPath() string { return paths.GetClaudeSettingsPath() }

// ProfileSidecarPath is where the record of what settings.json held before an
// endpoint was written lives: <backup root>/claude-settings.prev.json.
func ProfileSidecarPath(s *store.Store) string {
	return filepath.Join(s.BackupDir(), ccsettings.SidecarName)
}

// endpointFor returns the validated base URL slot num carries in data, "" for
// none. A stored URL that no longer validates (a hand-edited roster) stops
// the switch: writing it would point Claude Code somewhere nobody checked,
// and ignoring it would send the endpoint's key to Anthropic.
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

// profilePlan is what a switch will do to settings.json, decided before
// anything is written.
type profilePlan struct {
	settingsPath string
	sidecarPath  string
	// apply is the endpoint to write, nil for a target without one.
	apply *ccsettings.Profile
	// revert: a record exists, so the priors go back.
	revert bool
	// known are the endpoint accounts' URLs and keys, for the revert of a
	// profile whose record was lost; nil when settings.json carries none.
	known []ccsettings.Profile
	// snap is both files as they were, for the rollback; nil when the plan
	// touches nothing.
	snap *ccsettings.Snapshot
	// applied / reverted say what commit did.
	applied, reverted bool
}

// touches reports whether the plan writes settings.json at all.
func (p *profilePlan) touches() bool {
	return p != nil && (p.apply != nil || p.revert || len(p.known) > 0)
}

// changed reports whether commit rewrote settings.json, i.e. whether Claude
// Code sessions that are already running authenticate differently from new
// ones now.
func (p *profilePlan) changed() bool { return p != nil && (p.applied || p.reverted) }

// planProfile decides what a switch onto target does to settings.json, and
// refuses before anything is written when it cannot be done: an invalid URL,
// a stored key that cannot be sent as a header, an unparseable settings file,
// a corrupt record, or an API-key account with no URL whose key is not an
// Anthropic key (writing that into the credential store would make an OAuth
// credential of it).
func planProfile(s *store.Store, data *store.SequenceData, target, targetCreds string) (*profilePlan, error) {
	p := &profilePlan{settingsPath: claudeSettingsPath(), sidecarPath: ProfileSidecarPath(s)}
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
	} else if s.AccountKindFor(target) == "api_key" && !credstore.LooksLikeAPIKey(targetCreds) {
		return nil, cerr.Switch("Account-%s's key is not an Anthropic API key and the account has no base URL. Re-add it with: tycswap add-token --base-url URL --slot %s", target, target)
	}

	switch {
	case p.apply != nil:
	case ccsettings.SidecarExists(p.sidecarPath):
		p.revert = true
	default:
		// No record. A profile left without one (its sidecar removed by
		// hand) is still taken out when settings.json holds exactly an
		// endpoint tycswap knows and its key. A file that cannot be read
		// carries nothing here: it never stops a switch that would not
		// otherwise write it.
		if live := ccsettings.Live(p.settingsPath); live.BaseURL != "" && live.Token != "" {
			p.known = knownProfiles(s, data)
		}
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

// knownProfiles lists every endpoint account's URL and stored key.
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

// writeActiveFor writes the target's credential into Claude Code's credential
// store: the stored blob or key as always, or, for an endpoint account,
// nothing at all (ClearActive), since its key goes into settings.json.
func writeActiveFor(s *store.Store, p *profilePlan, targetCreds string) error {
	if p != nil && p.apply != nil {
		return s.Creds.ClearActive()
	}
	return s.Creds.WriteActiveAccount(targetCreds)
}

// commit writes settings.json as planned. A failed revert of a record is an
// error (the switch rolls back); the by-value cleanup of a profile without a
// record is best-effort and only logged.
func (p *profilePlan) commit(s *store.Store) error {
	if !p.touches() {
		return nil
	}
	if p.apply != nil {
		if err := ccsettings.Apply(p.settingsPath, p.sidecarPath, *p.apply); err != nil {
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

// restore puts settings.json and the record back as they were before the
// switch.
func (p *profilePlan) restore() error {
	if p == nil || p.snap == nil {
		return nil
	}
	return p.snap.Restore()
}
