package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTranscript(t *testing.T, claudeDir, cwd, id string, lines ...string) string {
	t.Helper()
	p := TranscriptPath(claudeDir, cwd, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectDirNameMatchesClaudeCode(t *testing.T) {
	cases := map[string]string{
		"/Users/jane.doe/work/tycswap": "-Users-jane-doe-work-tycswap",
		`C:\Users\me\proj_1`:           "C--Users-me-proj-1",
		"/tmp/äö":                      "-tmp---",
	}
	for in, want := range cases {
		if got := projectDirName(in); got != want {
			t.Errorf("projectDirName(%q) = %q, want %q", in, got, want)
		}
	}
	if TranscriptPath("/c", "/w", "../x") != "" || TranscriptPath("", "/w", "id") != "" {
		t.Error("unsafe or incomplete inputs must yield no path")
	}
}

func TestSessionTitlePrefersSummaryThenFirstPrompt(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "/w/a", "s1",
		`{"type":"queue-operation","sessionId":"s1"}`,
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"sidechain noise"}}`,
		`{"type":"user","message":{"role":"user","content":"<command-name>/login</command-name>"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"  Fix the   flaky\n test in ci  "}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"ok"}}`,
	)
	if got := SessionTitle(dir, "/w/a", "s1"); got != "Fix the flaky test in ci" {
		t.Errorf("title = %q", got)
	}
	writeTranscript(t, dir, "/w/b", "s2",
		`{"type":"user","message":{"role":"user","content":"first prompt"}}`,
		`{"type":"summary","summary":"Renaming the project","leafUuid":"x"}`,
	)
	if got := SessionTitle(dir, "/w/b", "s2"); got != "Renaming the project" {
		t.Errorf("summary should win, got %q", got)
	}
}

func TestSessionTitleTruncatesAndSkipsSynthetic(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("word ", 40)
	writeTranscript(t, dir, "/w/c", "s3",
		`{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"}}`,
		`{"type":"user","message":{"role":"user","content":"Caveat: the messages below were generated"}}`,
		`{"type":"user","message":{"role":"user","content":"`+long+`"}}`,
	)
	got := SessionTitle(dir, "/w/c", "s3")
	if r := []rune(got); len(r) != titleMaxRunes || !strings.HasSuffix(got, "…") || !strings.HasPrefix(got, "word word") {
		t.Errorf("title = %q (%d runes)", got, len(r))
	}
}

func TestSessionTitleMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := SessionTitle(dir, "/nope", "zz"); got != "" {
		t.Errorf("missing transcript → %q", got)
	}
	writeTranscript(t, dir, "/w/d", "s4", `{"type":"bridge-session"}`, `not json at all`)
	if got := SessionTitle(dir, "/w/d", "s4"); got != "" {
		t.Errorf("no prompt yet → %q", got)
	}
}

func TestSessionTitleCacheRefreshesOnlyWhileEmpty(t *testing.T) {
	dir := t.TempDir()
	p := writeTranscript(t, dir, "/w/e", "s5", `{"type":"bridge-session"}`)
	if SessionTitle(dir, "/w/e", "s5") != "" {
		t.Fatal("expected no title yet")
	}
	// the transcript grows and gains a prompt → re-read because nothing was cached
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"type":"user","message":{"role":"user","content":"now there is a prompt"}}` + "\n")
	_ = f.Close()
	if got := SessionTitle(dir, "/w/e", "s5"); got != "now there is a prompt" {
		t.Fatalf("after growth: %q", got)
	}
	// once found, the title sticks even if the file changes underneath
	if err := os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"something else"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SessionTitle(dir, "/w/e", "s5"); got != "now there is a prompt" {
		t.Errorf("cached title should stick, got %q", got)
	}
}
