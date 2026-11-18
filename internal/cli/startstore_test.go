// Tests for the session's model family that tycswap start reads (DESIGN A103).
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	resumeID      = "0f4c2a1e-8b3d-4e5f-9a6b-7c8d9e0f1a2b"
	otherResumeID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
)

type familyFiles struct {
	cwd, home, root string
}

func newFamilyFiles(t *testing.T) familyFiles {
	t.Helper()
	base := t.TempDir()
	f := familyFiles{cwd: filepath.Join(base, "work"), home: filepath.Join(base, "home"), root: filepath.Join(base, "root")}
	for _, dir := range []string{f.cwd, f.home, f.root} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func assistantLine(model string) string {
	return `{"type":"assistant","message":{"model":"` + model + `","role":"assistant","content":[]}}` + "\n"
}

const userLine = `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n"

func (f familyFiles) record(t *testing.T, id, family string) {
	writeFile(t, filepath.Join(f.root, "default_models.json"),
		`{"schemaVersion":1,"sessions":{"`+id+`":{"family":"`+family+`","seenAt":1}}}`)
}

func (f familyFiles) transcript(t *testing.T, id, content string) {
	writeFile(t, filepath.Join(f.home, ".claude", "projects", "-work", id+".jsonl"), content)
}

func (f familyFiles) localSettings(t *testing.T, content string) {
	writeFile(t, filepath.Join(f.cwd, ".claude", "settings.local.json"), content)
}

func (f familyFiles) projectSettings(t *testing.T, content string) {
	writeFile(t, filepath.Join(f.cwd, ".claude", "settings.json"), content)
}

func (f familyFiles) userSettings(t *testing.T, content string) {
	writeFile(t, filepath.Join(f.home, ".claude", "settings.json"), content)
}

func TestSessionFamilySources(t *testing.T) {
	model := func(m string) string { return `{"model":"` + m + `"}` }
	for _, tc := range []struct {
		name         string
		args, env    []string
		setup        func(*testing.T, familyFiles)
		family, from string
	}{
		{name: "nothing", family: "", from: ""},
		{name: "--model alias", args: []string{"--model", "fable"}, family: "fable", from: "--model"},
		{name: "--model=", args: []string{"--model=opus"}, family: "opus", from: "--model"},
		{name: "--model full id with [1m]", args: []string{"--model", "claude-fable-5-1[1m]"}, family: "fable", from: "--model"},
		{name: "--model full id", args: []string{"--model=claude-opus-5-5"}, family: "opus", from: "--model"},
		{name: "--model twice, last wins", args: []string{"--model", "opus", "-p", "x", "--model=fable"}, family: "fable", from: "--model"},
		{name: "--model twice, last wins (other order)", args: []string{"--model=fable", "--model", "opus"}, family: "opus", from: "--model"},
		{name: "--model after -- is a prompt", args: []string{"--", "--model", "fable"}, family: "", from: ""},
		{name: "ANTHROPIC_MODEL", env: []string{"ANTHROPIC_MODEL=claude-fable-5-1"}, family: "fable", from: "ANTHROPIC_MODEL"},
		{name: "--model beats ANTHROPIC_MODEL", args: []string{"--model", "opus"}, env: []string{"ANTHROPIC_MODEL=fable"}, family: "opus", from: "--model"},
		{name: "recorded session", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "fable")
		}, family: "fable", from: "recorded session"},
		{name: "-r ID", args: []string{"-r", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "fable")
		}, family: "fable", from: "recorded session"},
		{name: "--resume=ID", args: []string{"--resume=" + resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "fable")
		}, family: "fable", from: "recorded session"},
		{name: "ANTHROPIC_MODEL beats --resume", args: []string{"--resume", resumeID}, env: []string{"ANTHROPIC_MODEL=opus"}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "fable")
		}, family: "opus", from: "ANTHROPIC_MODEL"},
		{name: "another session's record is no answer", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, otherResumeID, "fable")
		}, family: "", from: ""},
		{name: "transcript", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, userLine+assistantLine("claude-opus-5-5")+userLine+assistantLine("claude-fable-5-1")+userLine)
		}, family: "fable", from: "transcript"},
		{name: "record beats transcript", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "opus")
			f.transcript(t, resumeID, assistantLine("claude-fable-5-1"))
		}, family: "opus", from: "recorded session"},
		{name: "transcript skips synthetic and sidechain lines", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, assistantLine("claude-fable-5-1")+
				`{"type":"assistant","isSidechain":true,"message":{"model":"claude-opus-5-5"}}`+"\n"+
				assistantLine("<synthetic>")+userLine)
		}, family: "fable", from: "transcript"},
		{name: "last assistant line beyond 1 MiB", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, assistantLine("claude-fable-5-1")+strings.Repeat(userLine, (1<<20)/len(userLine)+1))
		}, family: "", from: ""},
		{name: "last assistant line just inside 1 MiB", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, strings.Repeat(userLine, 40000)+assistantLine("claude-fable-5-1")+strings.Repeat(userLine, (1<<20)/len(userLine)-10))
		}, family: "fable", from: "transcript"},
		{name: "malformed transcript", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, "not json\n{\"type\":\"assistant\",\"message\":\n\x00\x01\n")
		}, family: "", from: ""},
		{name: "malformed transcript falls through to settings", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, "{{{\n")
			f.userSettings(t, model("opus"))
		}, family: "opus", from: "user"},
		{name: "non-UUID id", args: []string{"--resume", "my-session"}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, "my-session", "fable")
			f.transcript(t, "my-session", assistantLine("claude-fable-5-1"))
		}, family: "", from: ""},
		{name: "--resume without an id", args: []string{"--resume"}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, assistantLine("claude-fable-5-1"))
		}, family: "", from: ""},
		{name: "--resume followed by an option", args: []string{"--resume", "--verbose"}, setup: func(t *testing.T, f familyFiles) {
			f.userSettings(t, model("opus"))
		}, family: "opus", from: "user"},
		{name: "--continue", args: []string{"--continue"}, setup: func(t *testing.T, f familyFiles) {
			f.record(t, resumeID, "fable")
			f.userSettings(t, model("opus"))
		}, family: "opus", from: "user"},
		{name: "-c", args: []string{"-c"}, family: "", from: ""},
		{name: "--resume beats settings", args: []string{"--resume", resumeID}, setup: func(t *testing.T, f familyFiles) {
			f.transcript(t, resumeID, assistantLine("claude-fable-5-1"))
			f.localSettings(t, model("opus"))
		}, family: "fable", from: "transcript"},
		{name: "local settings", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, model("fable"))
		}, family: "fable", from: "local"},
		{name: "local beats project", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, model("fable"))
			f.projectSettings(t, model("opus"))
		}, family: "fable", from: "local"},
		{name: "project beats user", setup: func(t *testing.T, f familyFiles) {
			f.projectSettings(t, model("claude-fable-5-1[1m]"))
			f.userSettings(t, model("opus"))
		}, family: "fable", from: "project"},
		{name: "user", setup: func(t *testing.T, f familyFiles) {
			f.userSettings(t, model("opus"))
		}, family: "opus", from: "user"},
		{name: "a file without the key is skipped", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, `{"permissions":{}}`)
			f.projectSettings(t, model("fable"))
		}, family: "fable", from: "project"},
		{name: "malformed and unreadable files are skipped", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, `{"model":`)
			if err := os.MkdirAll(filepath.Join(f.cwd, ".claude", "settings.json"), 0o700); err != nil {
				t.Fatal(err)
			}
			f.userSettings(t, model("fable"))
		}, family: "fable", from: "user"},
		{name: "a non-string model is skipped", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, `{"model":7}`)
			f.userSettings(t, model("opus"))
		}, family: "opus", from: "user"},
		{name: "an unknown model answers with no family", setup: func(t *testing.T, f familyFiles) {
			f.localSettings(t, model("gpt-9"))
			f.userSettings(t, model("fable"))
		}, family: "", from: "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFamilyFiles(t)
			if tc.setup != nil {
				tc.setup(t, f)
			}
			family, from := sessionFamily(tc.args, tc.env, f.cwd, f.home, f.root)
			want := map[string]string{
				"local":   filepath.Join(f.cwd, ".claude", "settings.local.json"),
				"project": filepath.Join(f.cwd, ".claude", "settings.json"),
				"user":    filepath.Join(f.home, ".claude", "settings.json"),
			}[tc.from]
			if want == "" {
				want = tc.from
			}
			if family != tc.family || from != want {
				t.Fatalf("sessionFamily = %q from %q, want %q from %q", family, from, tc.family, want)
			}
		})
	}
}

func TestSessionFamilyTranscriptStaysInProjects(t *testing.T) {
	for name, link := range map[string]func(t *testing.T, f familyFiles, outside string){
		"file symlink": func(t *testing.T, f familyFiles, outside string) {
			dir := filepath.Join(f.home, ".claude", "projects", "-work")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, resumeID+".jsonl"), filepath.Join(dir, resumeID+".jsonl")); err != nil {
				t.Fatal(err)
			}
		},
		"directory symlink": func(t *testing.T, f familyFiles, outside string) {
			projects := filepath.Join(f.home, ".claude", "projects")
			if err := os.MkdirAll(projects, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(projects, "-work")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFamilyFiles(t)
			outside := t.TempDir()
			writeFile(t, filepath.Join(outside, resumeID+".jsonl"), assistantLine("claude-fable-5-1"))
			link(t, f, outside)
			if family, from := sessionFamily([]string{"--resume", resumeID}, nil, f.cwd, f.home, f.root); family != "" || from != "" {
				t.Fatalf("sessionFamily followed a symlink out of projects/: %q from %q", family, from)
			}
		})
	}
}

func TestSessionFamilyTranscriptInsideAProjectsSymlink(t *testing.T) {
	f := newFamilyFiles(t)
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "-work", resumeID+".jsonl"), assistantLine("claude-fable-5-1"))
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(f.home, ".claude", "projects")); err != nil {
		t.Fatal(err)
	}
	if family, from := sessionFamily([]string{"--resume", resumeID}, nil, f.cwd, f.home, f.root); family != "fable" || from != "transcript" {
		t.Fatalf("sessionFamily = %q from %q, want fable from transcript", family, from)
	}
}
