// Implements spec 08§12 rotation (maxBytes=1MB, backupCount=3, lazy dir) for
// a log the tray app and commands write at once (DESIGN A53). The reference's
// RotatingFileHandler keeps its file open and counts the bytes it wrote; here
// each record opens the file, rolls it over first when the record would push
// it to or past maxBytes, appends and closes it, so no handle outlives a
// record and the size is the file's own. The parent dir and the file are
// created on the first write. Two writers that reach the limit at once may
// both roll over and lose a backup, as the reference's handlers can.

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

// write appends p to the log, creating the parent dir lazily. The file is
// opened for each record and closed after it, and the size that decides a
// rollover is the file's own: the tray app and a command log to the same
// file at once, and a handle kept open would go on writing to the file the
// other process rotated away (on Windows, block its rename and any delete).
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
