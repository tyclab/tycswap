package release

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dist(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func linuxWindowsDist(t *testing.T, tag, flavour string) string {
	files := map[string]string{}
	for _, n := range LinuxWindowsAssets(tag) {
		files[n] = n + flavour
	}
	return dist(t, files)
}

func darwinDist(t *testing.T, tag string) string {
	files := map[string]string{}
	for _, n := range DarwinAssets(tag) {
		files[n] = n + " bytes"
	}
	return dist(t, files)
}

func publisher(c *Client, tag, dir string, publish bool) Publisher {
	return Publisher{API: c, Tag: tag, Notes: "- notes for " + tag, HasNotes: true, Publish: publish, Dist: dir, Log: io.Discard}
}

func sumsLine(name, body string) string { return sum([]byte(body)) + "  " + name + "\n" }

// The release appears with its Linux and Windows builds, their sums and its
// notes all present; macOS follows into the published release, the sums
// grow by its lines and the Windows line stays byte for byte; hand edits to
// the notes made in between survive.
func TestPublishThenAttachDarwin(t *testing.T) {
	f, c := newFakeGitHub(t)
	ctx := context.Background()
	const tag = "v0.24.0"
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(ctx); err != nil {
		t.Fatal(err)
	}
	snap, ok := f.published[tag]
	if !ok {
		t.Fatal("the release was not published")
	}
	wantAssets := append(LinuxWindowsAssets(tag), SumsName)
	if strings.Join(snap.Assets, ",") != strings.Join(sortStrings(wantAssets), ",") {
		t.Errorf("visible with %v, want %v", snap.Assets, wantAssets)
	}
	win := "tycswap_v0.24.0_windows_amd64.exe"
	if snap.Body != "- notes for v0.24.0" || !strings.Contains(snap.Sums, sumsLine(win, win+" bytes")) || snap.Latest != "true" {
		t.Errorf("visible with body %q, sums %q, latest %s", snap.Body, snap.Sums, snap.Latest)
	}
	rel := f.release(tag)
	rel.Body = "edited by hand after publishing"
	before, _ := f.file(tag, SumsName)

	if err := publisher(c, tag, darwinDist(t, tag), false).Run(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := f.file(tag, SumsName)
	if len(ParseSums(after)) != 5 {
		t.Errorf("sums after macOS: %q", after)
	}
	for _, line := range strings.SplitAfter(before, "\n") {
		if !strings.Contains(after, line) {
			t.Errorf("line %q changed", line)
		}
	}
	if f.release(tag).Body != "edited by hand after publishing" {
		t.Errorf("notes overwritten: %q", f.release(tag).Body)
	}
}

// A finished release is left alone by a rerun, even one whose builds came
// out with other bytes: nothing is uploaded, deleted or edited.
func TestRerunOfAFinishedReleaseChangesNothing(t *testing.T) {
	f, c := newFakeGitHub(t)
	ctx := context.Background()
	const tag = "v0.24.0"
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := publisher(c, tag, darwinDist(t, tag), false).Run(ctx); err != nil {
		t.Fatal(err)
	}
	f.release(tag).Body = "edited by hand"
	n := len(f.writes())
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " rebuilt"), true).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := publisher(c, tag, darwinDist(t, tag), false).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if extra := f.writes()[n:]; len(extra) != 0 {
		t.Errorf("a rerun wrote %v", extra)
	}
	if win, _ := f.file(tag, "tycswap_v0.24.0_windows_amd64.exe"); win != "tycswap_v0.24.0_windows_amd64.exe bytes" {
		t.Errorf("windows build replaced: %q", win)
	}
	if f.release(tag).Body != "edited by hand" {
		t.Error("notes overwritten")
	}
}

// macOS built before Linux and Windows: its job creates the draft with the
// notes and stays invisible; the Linux and Windows job publishes.
func TestDarwinFirstLeavesADraft(t *testing.T) {
	f, c := newFakeGitHub(t)
	ctx := context.Background()
	const tag = "v0.24.0"
	if err := publisher(c, tag, darwinDist(t, tag), false).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if r := f.release(tag); r == nil || !r.Draft || r.Body != "- notes for v0.24.0" {
		t.Fatalf("after macOS: %+v", r)
	}
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if snap := f.published[tag]; len(snap.Assets) != 6 || len(ParseSums(snap.Sums)) != 5 {
		t.Errorf("published with %v and %q", snap.Assets, snap.Sums)
	}
}

// Half a release (one build uploaded, then the job died) is finished by the
// rerun, and an upload GitHub never completed is replaced.
func TestARerunFinishesAHalfRelease(t *testing.T) {
	f, c := newFakeGitHub(t)
	const tag = "v0.24.0"
	rel := f.addRelease(tag, true, PlaceholderNotes)
	f.attach(rel, "tycswap_v0.24.0_linux_amd64", []byte("tycswap_v0.24.0_linux_amd64 bytes"))
	f.attach(rel, "tycswap_v0.24.0_linux_arm64", []byte("partial"))
	rel.assets[1].State = "starter"
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	arm, _ := f.file(tag, "tycswap_v0.24.0_linux_arm64")
	snap := f.published[tag]
	if arm != "tycswap_v0.24.0_linux_arm64 bytes" || len(ParseSums(snap.Sums)) != 3 || snap.Body != "- notes for v0.24.0" {
		t.Errorf("arm64 %q, published %+v", arm, snap)
	}
}

// A hand-pushed tag whose commit names no section stays a draft with the
// placeholder, as before; a person writes the notes and publishes.
func TestAHandTagWithoutNotesStaysADraft(t *testing.T) {
	f, c := newFakeGitHub(t)
	p := publisher(c, "v0.24.0", linuxWindowsDist(t, "v0.24.0", " bytes"), false)
	p.Notes, p.HasNotes = "", false
	if err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := f.release("v0.24.0"); !r.Draft || r.Body != PlaceholderNotes {
		t.Errorf("release %+v", r.Release)
	}
}

func TestPublishRefuses(t *testing.T) {
	const tag = "v0.24.0"
	t.Run("a sums line that disagrees with its build", func(t *testing.T) {
		f, c := newFakeGitHub(t)
		rel := f.addRelease(tag, false, "notes")
		f.attach(rel, "tycswap_v0.24.0_windows_amd64.exe", []byte("replaced"))
		f.attach(rel, SumsName, []byte(sumsLine("tycswap_v0.24.0_windows_amd64.exe", "original")))
		err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "a published hash never changes") {
			t.Errorf("error %v", err)
		}
	})
	t.Run("publishing without a Windows build", func(t *testing.T) {
		f, c := newFakeGitHub(t)
		d := linuxWindowsDist(t, tag, " bytes")
		_ = os.Remove(filepath.Join(d, "tycswap_v0.24.0_windows_amd64.exe"))
		err := publisher(c, tag, d, true).Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "stays a draft") || !f.release(tag).Draft {
			t.Errorf("error %v", err)
		}
	})
	t.Run("two releases already carry the tag", func(t *testing.T) {
		f, c := newFakeGitHub(t)
		f.addRelease(tag, true, "a")
		f.addRelease(tag, true, "b")
		if err := publisher(c, tag, linuxWindowsDist(t, tag, ""), true).Run(context.Background()); err == nil {
			t.Error("no error")
		}
	})
}

// Another run created the release between this run's look and its own
// creation: the older one is kept and this run's draft deleted.
func TestTwoRunsCreatingTheReleaseKeepOne(t *testing.T) {
	f, c := newFakeGitHub(t)
	const tag = "v0.24.0"
	f.onCreate = func() {
		f.onCreate = nil
		f.putRelease(1, tag, true, "other run")
	}
	if err := publisher(c, tag, linuxWindowsDist(t, tag, " bytes"), true).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var mine int
	for _, r := range f.releases {
		if r.TagName == tag {
			mine++
		}
	}
	if mine != 1 || f.published[tag].Body != "- notes for v0.24.0" {
		t.Errorf("%d releases for the tag, published %+v", mine, f.published[tag])
	}
}

// A version published after a greater one is not marked latest, so clients
// keep being offered the greater one.
func TestLatestOnlyForTheGreatestVersion(t *testing.T) {
	f, c := newFakeGitHub(t)
	f.addRelease("v0.24.1", false, "newer")
	if err := publisher(c, "v0.24.0", linuxWindowsDist(t, "v0.24.0", " bytes"), true).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.published["v0.24.0"].Latest; got != "false" {
		t.Errorf("make_latest %s", got)
	}
}

func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
