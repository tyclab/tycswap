// sessiontitle.go — a human title for a running Claude Code session.
//
// Claude Code keeps each session's transcript at
// <claude dir>/projects/<cwd with every non-alphanumeric byte → "-">/<session id>.jsonl.
// The best title on disk is a "summary" entry (written when Claude names or
// compacts a session); failing that, the first real user prompt. Nothing here
// touches credentials; the file is read once and re-read only when it grows
// and no title had been found.
package web

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

const (
	titleMaxRunes = 80
	titleScanMax  = 512 << 10 // the title is at the top; never read a huge transcript whole
)

// projectDirName mirrors Claude Code's transcript directory encoding.
func projectDirName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TranscriptPath is where Claude Code writes the session's transcript.
func TranscriptPath(claudeDir, cwd, sessionID string) string {
	if claudeDir == "" || cwd == "" || sessionID == "" || strings.ContainsAny(sessionID, `/\`) {
		return ""
	}
	return filepath.Join(claudeDir, "projects", projectDirName(cwd), sessionID+".jsonl")
}

type titleEntry struct {
	size  int64
	title string
}

// titleCache remembers per transcript what was found and at which size, so
// the 5-second poll does not re-read files whose title is already known.
type titleCache struct {
	mu sync.Mutex
	m  map[string]titleEntry
}

var titles = &titleCache{m: map[string]titleEntry{}}

// SessionTitle returns the title for the session's transcript, or "".
func SessionTitle(claudeDir, cwd, sessionID string) string {
	return titles.lookup(TranscriptPath(claudeDir, cwd, sessionID))
}

func (c *titleCache) lookup(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	e, ok := c.m[path]
	c.mu.Unlock()
	if ok && (e.title != "" || e.size == fi.Size()) {
		return e.title
	}
	title := readTranscriptTitle(path)
	c.mu.Lock()
	c.m[path] = titleEntry{size: fi.Size(), title: title}
	c.mu.Unlock()
	return title
}

// readTranscriptTitle scans the head of a transcript: a summary wins, else
// the first non-sidechain user prompt that is real text.
func readTranscriptTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var firstPrompt string
	read := 0
	for sc.Scan() && read < titleScanMax {
		line := sc.Bytes()
		read += len(line) + 1
		var entry struct {
			Type        string `json:"type"`
			Summary     string `json:"summary"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		switch entry.Type {
		case "summary":
			if t := cleanTitle(entry.Summary); t != "" {
				return t
			}
		case "user":
			if firstPrompt != "" || entry.IsSidechain || entry.Message.Role != "user" {
				continue
			}
			if t := cleanTitle(promptText(entry.Message.Content)); t != "" && !looksSynthetic(t) {
				firstPrompt = t
			}
		}
	}
	return firstPrompt
}

// promptText extracts the text of a user message: a plain string, or the
// first text block of a content array.
func promptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return b.Text
			}
		}
	}
	return ""
}

// looksSynthetic filters the prompts Claude Code injects itself (slash
// command wrappers, tool output echoes, interruption markers).
func looksSynthetic(t string) bool {
	return strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[Request interrupted") || strings.HasPrefix(t, "Caveat:")
}

// cleanTitle collapses whitespace and cuts to titleMaxRunes with an ellipsis.
func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > titleMaxRunes {
		return strings.TrimRight(string(r[:titleMaxRunes-1]), " ") + "…"
	}
	return s
}
