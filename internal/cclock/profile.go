package cclock

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/clock"
)

func AcquireProfileCredentials(profile string, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(profile+".lock", CredentialsStalenessS, timeout, clk)
}

func AcquireProfileStorageWrite(profile string, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(filepath.Join(profile, ".storage-write.lock"), StorageWriteStalenessS, timeout, clk)
}

var errOAuthRefreshBusy = errors.New("client refresh lock is busy")

// AcquireOAuthRefresh waits for the client's outer refresh lock without taking
// over its owner file. A zero timeout makes one attempt. A busy native
// refresh must finish writing its grant.
func AcquireOAuthRefresh(dir string, timeout time.Duration) (*Handle, error) {
	lock, err := acquire(filepath.Join(dir, ".oauth_refresh.lock"), 0, timeout, clock.System{})
	if timeoutErr, ok := err.(*cerr.Error); ok && timeoutErr.Kind == cerr.KindClaudeCodeLockTimeout {
		return nil, timeoutErr.Wrap(errOAuthRefreshBusy)
	}
	return lock, err
}

// RetryOAuthRefresh retries the entire move only when the client owns the outer
// refresh lock. The attempt must release every lock before returning.
func RetryOAuthRefresh(attempt func(deadline time.Time) error) error {
	deadline := time.Now().Add(DefaultTimeoutS)
	for {
		err := attempt(deadline)
		if !errors.Is(err, errOAuthRefreshBusy) {
			return err
		}
		time.Sleep(min(jitterBackoff(), time.Until(deadline)))
		if !time.Now().Before(deadline) {
			return err
		}
	}
}
