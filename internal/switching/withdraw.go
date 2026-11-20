package switching

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tyclab/tycswap/internal/ccfile"
	"github.com/tyclab/tycswap/internal/ccsettings"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/oauth"
	"github.com/tyclab/tycswap/internal/store"
)

// WithdrawLogin runs under the caller's scope state lock. check revalidates
// the selected identities and idleness under the native store's write locks.
func WithdrawLogin(s *store.Store, from groups.Account, to string, check func(*store.SequenceData) error) (bool, error) {
	guard := func(data *store.SequenceData) error {
		if err := check(data); err != nil {
			return err
		}
		if !s.IsFable() {
			if err := s.EnsureAccountAvailable(from.Number); err != nil {
				return err
			}
		}
		live := s.ResolveOwnerSlot(data)
		if s.AccountKindFor(from.Number) == "api_key" {
			backup, err := s.ReadAccountCredentials(from.Number, from.Email)
			if err != nil {
				return err
			}
			value, unavailable, err := s.Creds.ReadActive()
			if err != nil {
				return err
			}
			match := backup != "" && !unavailable && strings.TrimSpace(backup) == strings.TrimSpace(value)
			if value == "" {
				endpoint := ccsettings.Live(claudeSettingsPath())
				match = backup != "" && !unavailable && endpoint.Token == backup && endpoint.BaseURL != "" && endpoint.BaseURL == store.BaseURLFrom(data, from.Number)
			}
			if !match {
				return fmt.Errorf("cannot verify Account-%s's live API key", from.Number)
			}
		} else if live.Source == store.SourceConfig || live.Source == store.SourceLastInstall {
			return cerr.Session("Account-%s is disabled, but its live credential ownership cannot be verified", from.Number)
		}
		if s.IsFable() && to == "" {
			return s.SetFableAllocationParked(true)
		}
		return nil
	}
	if s.IsFable() {
		var result map[string]any
		var err error
		if to == "" {
			result, err = emptyFableWhen(s, guard)
		} else {
			result, err = SwitchFable(s, to, FableSwitchOptions{
				BeforeChange: func(_ *store.Store, current *store.SequenceData) error { return guard(current) },
			})
		}
		return result["switched"] == true, err
	}
	if to != "" {
		_, err := performSwitchWhen(s, to, false, false, nil, guard)
		return err == nil, err
	}
	err := withTripleLock(s, func() error {
		data, err := s.ReadSequence()
		if err != nil {
			return err
		}
		if err := guard(data); err != nil {
			return err
		}
		return parkDefaultLogin(s, data, from)
	})
	return err == nil, err
}

func parkDefaultLogin(s *store.Store, data *store.SequenceData, from groups.Account) (err error) {
	config, _, err := readConfigText()
	if err != nil {
		return err
	}
	if _, err := readConfigForUpdate(); err != nil {
		return err
	}
	value, unavailable, err := s.Creds.ReadActive()
	if err != nil {
		return err
	}
	if unavailable {
		return cerr.Session("Account-%s's credential store is unavailable", from.Number)
	}
	if s.AccountKindFor(from.Number) == "oauth" {
		if oauth.CredentialFingerprint(value) == nil {
			return fmt.Errorf("cannot verify Account-%s's live OAuth login", from.Number)
		}
		if err := s.WriteAccountCredentials(from.Number, from.Email, oauth.AccountOnly(value)); err != nil {
			return err
		}
	}
	configSlot := ""
	if owner := s.ResolveOwnerSlot(data); owner.ByLineage {
		configSlot = owner.ConfigSlot
	}
	current, ok := outgoingConfig(s, from.Number, from.Email, config, configSlot)
	if ok {
		if err := s.WriteAccountConfig(from.Number, from.Email, current); err != nil {
			return err
		}
	}
	plan, err := planProfile(s, data, "", "")
	if err != nil {
		return err
	}
	tx := switchTransaction{originalCredentials: value, originalConfig: config, restoreClear: value == "", plan: plan}
	defer func() {
		if err != nil && !tx.rollback(s) {
			err = errors.Join(err, errors.New("could not restore all live login files; the saved account is available"))
		}
	}()
	tx.recordStep("config_written")
	tx.recordStep("credentials_written")
	if err := s.Creds.ClearActive(); err != nil {
		return err
	}
	if plan.touches() {
		tx.recordStep("profile_written")
		if err := plan.commit(s); err != nil {
			return err
		}
	}
	if err := ccfile.UpdateGlobalConfig(func(cfg map[string]any) { delete(cfg, "oauthAccount") }); err != nil {
		return err
	}
	data.ActiveAccountNumber = nil
	data.LastUpdated = storeTimestamp(s)
	return s.WriteSequence(data)
}
