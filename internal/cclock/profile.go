package cclock

import (
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/clock"
)

func AcquireProfileCredentials(profile string, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(profile+".lock", CredentialsStalenessS, timeout, clk)
}

func AcquireProfileStorageWrite(profile string, timeout time.Duration, clk clock.Clock) (*Handle, error) {
	return acquire(filepath.Join(profile, ".storage-write.lock"), StorageWriteStalenessS, timeout, clk)
}

// AcquireOAuthRefresh waits for the client's outer refresh lock without taking
// over its owner file. A busy native refresh must finish writing its grant.
func AcquireOAuthRefresh(dir string, timeout time.Duration) (*Handle, error) {
	return acquire(filepath.Join(dir, ".oauth_refresh.lock"), 0, timeout, clock.System{})
}
