package atomicfile

import (
	"errors"
	"os"
	"syscall"

	"github.com/tyclab/tycswap/internal/platform"
)

// SyncFile flushes f to stable storage before it is renamed into place, so a
// crash cannot leave an empty or torn credential file under the final name. A
// filesystem that does not support fsync (EINVAL, ENOTSUP) is not an error.
func SyncFile(f *os.File) error {
	if err := f.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}

// SyncDir flushes dir after a rename so the new directory entry survives a
// crash. Best effort: an error is ignored, and Windows (which cannot open a
// directory for fsync) is skipped.
func SyncDir(dir string) {
	if platform.IsWindows() {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
