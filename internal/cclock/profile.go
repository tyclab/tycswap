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
