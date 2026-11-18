// tycswap start picks the store by the session's model (DESIGN A103).
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tyclab/tycswap/internal/keychain"
	"github.com/tyclab/tycswap/internal/paths"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/reporting"
	"github.com/tyclab/tycswap/internal/store"
)

// startWindows is the platform seam of start and of cli.run's Fable store
// check: Windows matches environment names, and the Fable store's path,
// case-insensitively.
var startWindows = platform.IsWindows

// transcriptTail is how much of a transcript's end start reads for its model.
const transcriptTail = 1 << 20

var sessionUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// startEnvironment is the child environment for argv (program first) when no
// override holds, whether the session starts on the Fable store, which skips
// the default start-login selection, and the notice to print. Only a Fable
// session on a usable Fable store gets CLAUDE_SECURESTORAGE_CONFIG_DIR; every
// other session loses it when it names the store. Without Fable logins the
// session's model is not even read.
func startEnvironment(env, argv []string, fable store.FableState, hasFableLogins bool, storeDir string) (childEnv []string, skipSelection bool, notice string) {
	if !hasFableLogins || startSessionFamily(argv, env) != "fable" {
		return withoutFableStore(env, storeDir), false, ""
	}
	if fable.On && fable.Usable {
		return withFableStore(env, storeDir), true, ""
	}
	return withoutFableStore(env, storeDir), false, "the Fable store has no usable login (" + fable.Reason + "); starting on the default login."
}

func startSessionFamily(argv, env []string) string {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	family, _ := sessionFamily(argv[min(1, len(argv)):], env, cwd, home, paths.GetBackupRoot())
	return family
}

// startFableStore is the Fable store's state and whether any account has a
// Fable login. Without credentials-fable/, or with a store start must not
// read (startStoreProblem), it opens nothing. It makes no profile request and
// creates nothing.
func startFableStore(root string) (store.FableState, bool) {
	if info, err := os.Stat(paths.FableCredentialsDir(root)); err != nil || !info.IsDir() || startStoreProblem(root) != "" {
		return store.FableState{}, false
	}
	sw, err := newSwitcher(store.Options{Keychain: keychain.Security{}, Stderr: io.Discard, SkipMigrations: true})
	if err != nil {
		return store.FableState{}, false
	}
	fable := sw.Store.FableStore()
	return fable, fable.On || sw.Store.HasFableLogins()
}

func isFableStoreEntry(entry, storeDir string) bool {
	key, value, _ := strings.Cut(entry, "=")
	return envKeyIs(key, secureStorageEnv) && namesFableStore(value, storeDir, startWindows())
}

// withoutFableStore is env without a CLAUDE_SECURESTORAGE_CONFIG_DIR naming
// the Fable store; env itself when it has none.
func withoutFableStore(env []string, storeDir string) []string {
	for i, entry := range env {
		if !isFableStoreEntry(entry, storeDir) {
			continue
		}
		out := append([]string(nil), env[:i]...)
		for _, rest := range env[i+1:] {
			if !isFableStoreEntry(rest, storeDir) {
				out = append(out, rest)
			}
		}
		return out
	}
	return env
}

// withFableStore is env with CLAUDE_SECURESTORAGE_CONFIG_DIR set to storeDir,
// in place of an inherited entry or appended.
func withFableStore(env []string, storeDir string) []string {
	out := make([]string, 0, len(env)+1)
	set := false
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !envKeyIs(key, secureStorageEnv) {
			out = append(out, entry)
		} else if !set {
			out = append(out, secureStorageEnv+"="+storeDir)
			set = true
		}
	}
	if !set {
		out = append(out, secureStorageEnv+"="+storeDir)
	}
	return out
}

func accountSuffix(slot string) string {
	if slot == "" {
		return ""
	}
	return " (Account-" + slot + ")"
}

// sessionFamily is the model family of the Claude session that args (the
// child's arguments after its program) start, and the source that answered:
// --model, ANTHROPIC_MODEL, the resumed session's record or transcript, then
// the settings files. A source answers when it names a model; an unknown
// model answers with no family. It reads files only, never the network.
func sessionFamily(args, env []string, cwd, home, root string) (family, source string) {
	if model, ok := modelFlag(args); ok {
		return reporting.ModelFamily(model), "--model"
	}
	if model := envValue(env, "ANTHROPIC_MODEL"); model != "" {
		return reporting.ModelFamily(model), "ANTHROPIC_MODEL"
	}
	if id := resumedSession(args); sessionUUID.MatchString(id) {
		if family, _, ok := reporting.RecordedSession(root, id); ok && family != "" {
			return family, "recorded session"
		}
		if home != "" {
			if model := transcriptModel(filepath.Join(home, ".claude", "projects"), id); model != "" {
				return reporting.ModelFamily(model), "transcript"
			}
		}
	}
	var files []string
	if cwd != "" {
		files = append(files, filepath.Join(cwd, ".claude", "settings.local.json"), filepath.Join(cwd, ".claude", "settings.json"))
	}
	if home != "" {
		files = append(files, filepath.Join(home, ".claude", "settings.json"))
	}
	for _, path := range files {
		if model := settingsModel(path); model != "" {
			return reporting.ModelFamily(model), path
		}
	}
	return "", ""
}

// modelFlag is the last --model value before a "--".
func modelFlag(args []string) (model string, ok bool) {
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		if value, found := strings.CutPrefix(args[i], "--model="); found {
			model, ok = value, true
		} else if args[i] == "--model" && i+1 < len(args) {
			i++
			model, ok = args[i], true
		}
	}
	return model, ok && model != ""
}

// resumedSession is the id --resume / -r names, "" for a bare --resume (the
// picker).
func resumedSession(args []string) string {
	id := ""
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		if value, found := strings.CutPrefix(args[i], "--resume="); found {
			id = value
		} else if args[i] == "--resume" || args[i] == "-r" {
			id = ""
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				id = args[i]
			}
		}
	}
	return id
}

// envValue is key's first value in env, its name matched case-insensitively
// on Windows.
func envValue(env []string, key string) string {
	for _, entry := range env {
		if k, v, _ := strings.Cut(entry, "="); envKeyIs(k, key) {
			return v
		}
	}
	return ""
}

func envKeyIs(got, want string) bool {
	return got == want || startWindows() && strings.EqualFold(got, want)
}

// transcriptModel is the model of the last assistant line of
// <projects>/*/<id>.jsonl. A path that resolves out of projects is skipped.
func transcriptModel(projects, id string) string {
	base, err := filepath.EvalSymlinks(projects)
	if err != nil {
		return ""
	}
	dirs, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, dir := range dirs {
		path, err := filepath.EvalSymlinks(filepath.Join(base, dir.Name(), id+".jsonl"))
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(base, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if model := lastAssistantModel(path); model != "" {
			return model
		}
	}
	return ""
}

// lastAssistantModel reads at most the last transcriptTail bytes of path and
// returns the model of its last assistant line. Claude Code's own messages
// (<synthetic>) and subagent lines name no session model.
func lastAssistantModel(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	off := max(0, info.Size()-transcriptTail)
	buf := make([]byte, info.Size()-off)
	n, _ := f.ReadAt(buf, off)
	buf = buf[:n]
	if off > 0 {
		_, rest, found := bytes.Cut(buf, []byte("\n"))
		if !found {
			return ""
		}
		buf = rest
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte(`"assistant"`)) {
			continue
		}
		var line struct {
			Type        string `json:"type"`
			IsSidechain bool   `json:"isSidechain"`
			Message     struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &line) != nil || line.Type != "assistant" || line.IsSidechain {
			continue
		}
		if line.Message.Model != "" && line.Message.Model != "<synthetic>" {
			return line.Message.Model
		}
	}
	return ""
}

// settingsModel is the "model" a Claude Code settings file sets, "" when the
// file is missing, unreadable, malformed or does not set it.
func settingsModel(path string) string {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Model any `json:"model"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	model, _ := doc.Model.(string)
	return strings.TrimSpace(model)
}
