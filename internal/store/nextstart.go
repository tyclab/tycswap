package store

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/filelock"
	"github.com/tyclab/tycswap/internal/paths"
)

func SupportsMythosStart() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "windows"
}

var errMythosStartUnsupported = cerr.Validation("Independent Mythos starts require Linux, WSL, or Windows.")

// NextStartRequest identifies one setting, including when it is cleared and rearmed.
type NextStartRequest struct {
	Model string `json:"model"`
	ID    string `json:"id"`
}

const nextStartFile = "next-start.json"

var ErrUnreadableNextStart = errors.New("unreadable next start")

func ReadNextStart(root string) (NextStartRequest, error) {
	path := filepath.Join(root, nextStartFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NextStartRequest{}, nil
	}
	if err != nil {
		return NextStartRequest{}, fmt.Errorf("%w: %w", ErrUnreadableNextStart, err)
	}
	var value struct {
		Model *string
		ID    string
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return NextStartRequest{}, fmt.Errorf("%w at %s: %w", ErrUnreadableNextStart, path, err)
	}
	if value.ID == "" || value.Model == nil || (*value.Model != "" && *value.Model != "mythos") {
		return NextStartRequest{}, fmt.Errorf("%w at %s: invalid next start setting", ErrUnreadableNextStart, path)
	}
	return NextStartRequest{*value.Model, value.ID}, nil
}

func NextStartModel(root string) (string, error) {
	if !SupportsMythosStart() {
		return "", errMythosStartUnsupported
	}
	request, err := ReadNextStart(root)
	return request.Model, err
}

func SetNextStartModel(root, model string) error {
	if model == "mythos" && !SupportsMythosStart() {
		return errMythosStartUnsupported
	}
	if model != "" && model != "mythos" {
		return errors.New("choose Mythos or clear the next start")
	}
	_, err := changeNextStart(root, nil, NextStartRequest{model, rand.Text()})
	return err
}

// Claim writes the consumed state just before handing off to the native program.
// Restore on exec failure only if no other command changed that state meanwhile.
func (r NextStartRequest) Claim(root string) (bool, error) {
	return changeNextStart(root, &r, NextStartRequest{"", r.ID})
}

func (r NextStartRequest) Restore(root string) error {
	_, err := changeNextStart(root, &NextStartRequest{"", r.ID}, r)
	return err
}

func changeNextStart(root string, expected *NextStartRequest, next NextStartRequest) (bool, error) {
	if warn, err := paths.CheckPrivateRoot(root); err != nil {
		return false, err
	} else if warn != "" {
		return false, errors.New(warn)
	}
	changed := false
	err := filelock.New(filepath.Join(root, ".next-start.lock"), filelock.DefaultTimeout).With(func() error {
		if expected != nil {
			old, err := ReadNextStart(root)
			if err != nil {
				return err
			}
			if old != *expected {
				return nil
			}
		}
		raw, err := json.Marshal(next)
		if err == nil {
			err = atomicfile.Write(filepath.Join(root, nextStartFile), raw, atomicfile.Opts{})
		}
		changed = err == nil
		return err
	})
	return changed, err
}
