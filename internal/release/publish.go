package release

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// SumsName is the checksum file every release carries; clients take a build
// as published once it has a line there (DESIGN A119).
const SumsName = "SHA256SUMS"

// PlaceholderNotes is a draft's body while no CHANGELOG section names its
// version: a tag pushed by hand, as before.
const PlaceholderNotes = "Release notes to follow; see CHANGELOG.md."

// binaryPrefix starts the name of every build of tag.
func binaryPrefix(tag string) string { return "tycswap_" + tag + "_" }

// LinuxWindowsAssets are the builds a release must carry before it is
// published; DarwinAssets follow when a Mac has built them.
func LinuxWindowsAssets(tag string) []string {
	p := binaryPrefix(tag)
	return []string{p + "linux_amd64", p + "linux_arm64", p + "windows_amd64.exe"}
}

func DarwinAssets(tag string) []string {
	p := binaryPrefix(tag)
	return []string{p + "darwin_amd64", p + "darwin_arm64"}
}

// Publisher adds a directory of builds to tag's release and keeps its
// SHA256SUMS equal to the release's builds. It never replaces a build, and
// changes the notes of a draft only; Publish makes the draft visible once
// the Linux and Windows builds and their sums are there.
type Publisher struct {
	API      *Client
	Tag      string
	Notes    string
	HasNotes bool
	Publish  bool
	Dist     string
	Log      io.Writer
}

func (p Publisher) logf(format string, args ...any) {
	fmt.Fprintf(p.Log, format+"\n", args...)
}

// Run is idempotent: a second run, or one beside another run on the same
// release, uploads nothing twice and leaves the sums complete.
func (p Publisher) Run(ctx context.Context) error {
	rel, err := p.ensure(ctx)
	if err != nil {
		return err
	}
	local, err := p.localBuilds()
	if err != nil {
		return err
	}
	assets, err := p.API.Assets(ctx, rel.ID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range assets {
		if a.State != "uploaded" {
			p.logf("removing the unfinished upload %s", a.Name)
			if err := p.API.DeleteAsset(ctx, a.ID); err != nil && !isStatus(err, 404) {
				return err
			}
			continue
		}
		have[a.Name] = true
	}
	known := map[string]string{}
	for _, name := range sortedKeys(local) {
		if have[name] {
			p.logf("%s is already attached; it stays as it is", name)
			continue
		}
		data, err := os.ReadFile(local[name])
		if err != nil {
			return err
		}
		switch _, err := p.API.UploadAsset(ctx, rel, name, data); {
		case errors.Is(err, errAssetExists):
			p.logf("%s was attached by another run meanwhile", name)
		case err != nil:
			return err
		default:
			known[name] = sum(data)
			p.logf("attached %s", name)
		}
	}
	sums, err := p.syncSums(ctx, rel, known)
	if err != nil {
		return err
	}
	return p.finish(ctx, rel, sums)
}

// ensure finds tag's one release or creates it as a draft. Two runs that
// both create one keep the older, and the other deletes its own.
func (p Publisher) ensure(ctx context.Context) (Release, error) {
	mine, err := p.releases(ctx)
	if err != nil || len(mine) == 1 {
		return first(mine), err
	}
	if len(mine) > 1 {
		return Release{}, fmt.Errorf("%d releases carry tag %s; delete all but one", len(mine), p.Tag)
	}
	body := PlaceholderNotes
	if p.HasNotes {
		body = p.Notes
	}
	created, err := p.API.CreateDraft(ctx, p.Tag, "tycswap "+p.Tag, body)
	if err != nil {
		return Release{}, err
	}
	p.logf("created the draft release %s", p.Tag)
	if mine, err = p.releases(ctx); err != nil || len(mine) == 0 {
		return created, err
	}
	if mine[0].ID != created.ID {
		p.logf("another run created release %s first; deleting this run's draft", p.Tag)
		return mine[0], p.API.DeleteRelease(ctx, created.ID)
	}
	return mine[0], nil
}

func (p Publisher) releases(ctx context.Context) ([]Release, error) {
	all, err := p.API.Releases(ctx)
	var mine []Release
	for _, r := range all {
		if r.TagName == p.Tag {
			mine = append(mine, r)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].ID < mine[j].ID })
	return mine, err
}

// localBuilds are Dist's files named as tag's builds.
func (p Publisher) localBuilds() (map[string]string, error) {
	entries, err := os.ReadDir(p.Dist)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), binaryPrefix(p.Tag)) {
			out[e.Name()] = filepath.Join(p.Dist, e.Name())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no build of %s", p.Dist, p.Tag)
	}
	return out, nil
}

// syncSums makes the release's SHA256SUMS list each of its builds by the
// hash of the bytes attached, until a pass finds it so: another run adding
// builds meanwhile only adds lines. A line that disagrees with its build
// stops the run, since a published hash never changes.
func (p Publisher) syncSums(ctx context.Context, rel Release, known map[string]string) (map[string]string, error) {
	for {
		assets, err := p.API.Assets(ctx, rel.ID)
		if err != nil {
			return nil, err
		}
		var sumsAsset *Asset
		builds := map[string]string{}
		for _, a := range assets {
			switch {
			case a.State != "uploaded":
			case a.Name == SumsName:
				sumsAsset = &a
			case strings.HasPrefix(a.Name, binaryPrefix(p.Tag)):
				if known[a.Name] == "" {
					data, err := p.API.DownloadAsset(ctx, a.ID)
					if err != nil {
						return nil, err
					}
					known[a.Name] = sum(data)
				}
				builds[a.Name] = known[a.Name]
			}
		}
		want := RenderSums(builds)
		if sumsAsset != nil {
			data, err := p.API.DownloadAsset(ctx, sumsAsset.ID)
			if err != nil {
				return nil, err
			}
			if string(data) == want {
				return builds, nil
			}
			for name, h := range ParseSums(string(data)) {
				if b, ok := builds[name]; ok && b != h {
					return nil, fmt.Errorf("%s gives %s for %s, but the attached file hashes to %s: a published hash never changes; find out who replaced it", SumsName, h, name, b)
				}
			}
			if err := p.API.DeleteAsset(ctx, sumsAsset.ID); err != nil && !isStatus(err, 404) {
				return nil, err
			}
		}
		if _, err := p.API.UploadAsset(ctx, rel, SumsName, []byte(want)); err != nil && !errors.Is(err, errAssetExists) {
			return nil, err
		}
		p.logf("wrote %s for %d builds", SumsName, len(builds))
	}
}

// finish writes the notes into a draft and publishes it when asked and
// complete; a published release is left as it is, hand edits included.
func (p Publisher) finish(ctx context.Context, rel Release, sums map[string]string) error {
	rel, err := p.API.GetRelease(ctx, rel.ID)
	if err != nil {
		return err
	}
	if !rel.Draft {
		p.logf("release %s is published; its notes stay as they are", p.Tag)
		return nil
	}
	fields := map[string]any{}
	if p.HasNotes && rel.Body != p.Notes {
		fields["body"] = p.Notes
	}
	if p.Publish {
		for _, name := range LinuxWindowsAssets(p.Tag) {
			if sums[name] == "" {
				return fmt.Errorf("release %s lacks %s; it stays a draft", p.Tag, name)
			}
		}
		latest, err := p.isLatest(ctx)
		if err != nil {
			return err
		}
		fields["draft"], fields["make_latest"] = false, fmt.Sprint(latest)
	}
	if len(fields) == 0 {
		p.logf("release %s stays a draft until a person publishes it", p.Tag)
		return nil
	}
	if _, err := p.API.UpdateRelease(ctx, rel.ID, fields); err != nil {
		return err
	}
	if p.Publish {
		p.logf("published release %s", p.Tag)
	}
	return nil
}

// isLatest: no published release carries a greater version.
func (p Publisher) isLatest(ctx context.Context) (bool, error) {
	all, err := p.API.Releases(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range all {
		if !r.Draft && !r.Prerelease && semver.IsValid(r.TagName) && semver.Compare(r.TagName, p.Tag) > 0 {
			p.logf("%s is published already; %s is not marked latest", r.TagName, p.Tag)
			return false, nil
		}
	}
	return true, nil
}

// RenderSums is sha256sum's output for builds, sorted by name.
func RenderSums(builds map[string]string) string {
	var b strings.Builder
	for _, name := range sortedKeys(builds) {
		b.WriteString(builds[name] + "  " + name + "\n")
	}
	return b.String()
}

// ParseSums reads sha256sum's lines ("<hash>  <name>" or " *<name>").
func ParseSums(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 {
			out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return out
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func first(rs []Release) Release {
	if len(rs) == 0 {
		return Release{}
	}
	return rs[0]
}
