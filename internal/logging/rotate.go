// Implements spec 08§12 rotation (maxBytes=1MB, backupCount=3, lazy dir),
// with the file opened per record (DESIGN A53). Two writers that reach the
// limit at once may both roll over and lose a backup, possibly the newest.

package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type rotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
}

func newRotatingWriter(path string, maxBytes int64, backups int) *rotatingWriter {
	return &rotatingWriter{path: path, maxBytes: maxBytes, backups: backups}
}

// write appends p to the log, creating the parent dir lazily, after a
// rollover when p would push the file's own size to or past maxBytes.
func (w *rotatingWriter) write(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
		return err
	}
	if w.maxBytes > 0 {
		if fi, err := os.Stat(w.path); err == nil && fi.Size() > 0 && fi.Size()+int64(len(p)) >= w.maxBytes {
			w.rollover()
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(p)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// rollover shifts path.(n) → path.(n+1) down to path → path.1, discarding the
// oldest (mirroring RotatingFileHandler.doRollover); the next write creates a
// fresh log file.
func (w *rotatingWriter) rollover() {
	for i := w.backups - 1; i >= 1; i-- {
		sfn := fmt.Sprintf("%s.%d", w.path, i)
		dfn := fmt.Sprintf("%s.%d", w.path, i+1)
		if _, err := os.Stat(sfn); err == nil {
			_ = os.Remove(dfn)
			_ = os.Rename(sfn, dfn)
		}
	}
	dfn := w.path + ".1"
	_ = os.Remove(dfn)
	_ = os.Rename(w.path, dfn)
}
