// cache.go: {"timestamp", "data"}; (value, ok) tells a cached null from a miss. The writer is deliberately non-atomic and
// not chmod'ed (Amendment A8): this is the one low-value cache.

package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ReadCache returns the cached data if the file exists, parses, carries a
// numeric timestamp, and is within ttl of now (epoch seconds). ok is false on
// any miss (missing/corrupt/expired/malformed), matching Python's MISSING
// return, so a genuine cached null is distinguishable from a miss.
func ReadCache(path string, ttl, now float64) (data any, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, false
	}
	m, isMap := raw.(map[string]any)
	if !isMap {
		return nil, false
	}
	ts := numPtr(m["timestamp"])
	if ts == nil {
		return nil, false
	}
	if now-*ts < ttl {
		d, present := m["data"]
		if !present {
			return nil, false
		}
		return d, true
	}
	return nil, false
}

// WriteCache writes {"timestamp": now, "data": data} to path (04§4). DELIBERATE
// (Amendment A8): a plain, non-atomic, non-chmod'd write (created 0600 under a
// 0700 directory, like the rest of the store) — do NOT route this
// through atomicfile. The parent directory is created if absent.
func WriteCache(path string, data any, now float64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(map[string]any{"timestamp": now, "data": data})
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
