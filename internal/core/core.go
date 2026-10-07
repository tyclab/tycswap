package core

import (
	"strconv"

	"github.com/tyclab/tycswap/internal/lifecycle"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
	"github.com/tyclab/tycswap/internal/switching"
)

type Switcher struct {
	*store.Store
}

func New(opts store.Options) (*Switcher, error) {
	s, err := store.New(opts)
	if err != nil {
		return nil, err
	}
	return &Switcher{Store: s}, nil
}

func init() {
	switching.UsageProvider = reporting.UsageByAccount
	switching.PostSwitchList = postSwitchList
	switching.AutoAddCurrent = autoAddCurrent
	switching.Prompt = func(prompt string) (string, bool) {
		return lifecycle.ActivePrompter.Prompt(prompt)
	}
	reporting.FirstRunSetup = firstRunSetup
}

// postSwitchList wires switching.PostSwitchList to the nested list_accounts()
// call _perform_switch makes after releasing its locks (spec 02§8, the
// "Switched to Account-X" follow-up display): human mode, no token-status
// column, every stale account eligible for a refetch — Python's
// self.list_accounts() called with every default.
func postSwitchList(s *store.Store) error {
	_, err := reporting.ListAccounts(s, false, false, nil)
	return err
}

func autoAddCurrent(s *store.Store) (string, error) {
	if err := lifecycle.AddAccount(s, nil, false, nil); err != nil {
		return "", err
	}
	data, err := s.ReadSequence()
	if err != nil {
		return "", err
	}
	if data == nil || data.ActiveAccountNumber == nil {
		return "", nil
	}
	return strconv.Itoa(*data.ActiveAccountNumber), nil
}
