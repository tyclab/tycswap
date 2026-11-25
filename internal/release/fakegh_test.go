package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub keeps releases, assets and workflow runs in memory and answers
// the REST calls Client makes, the way GitHub documents them: drafts only in
// the listing, 422 for a second asset of one name, downloads by redirect.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	nextID   int64
	releases map[int64]*fakeRelease
	calls    []string

	runs    map[int64]Run
	jobs    map[string][]Job // "<run>/<attempt>"
	newest  map[string]int64 // "<workflow>/<branch>/<event>"
	reruns  []int64
	rerunOK bool

	// onCreate runs after a release is created, before the answer.
	onCreate func()
	// published is each release's state at the moment it stopped being a draft.
	published map[string]fakeSnapshot
}

type fakeRelease struct {
	Release
	assets []*fakeAsset
}

type fakeAsset struct {
	Asset
	data []byte
}

type fakeSnapshot struct {
	Body   string
	Assets []string
	Sums   string
	Latest string
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *Client) {
	f := &fakeGitHub{t: t, nextID: 100, releases: map[int64]*fakeRelease{}, runs: map[int64]Run{}, jobs: map[string][]Job{},
		newest: map[string]int64{}, published: map[string]fakeSnapshot{}, rerunOK: true}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f, &Client{API: f.srv.URL, Repo: "o/r", Token: "t", HTTP: f.srv.Client()}
}

func (f *fakeGitHub) id() int64 { f.nextID++; return f.nextID }

func (f *fakeGitHub) addRelease(tag string, draft bool, body string) *fakeRelease {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addReleaseLocked(tag, draft, body)
}

func (f *fakeGitHub) addReleaseLocked(tag string, draft bool, body string) *fakeRelease {
	return f.putRelease(f.id(), tag, draft, body)
}

func (f *fakeGitHub) putRelease(id int64, tag string, draft bool, body string) *fakeRelease {
	r := &fakeRelease{Release: Release{ID: id, TagName: tag, Name: "tycswap " + tag, Body: body, Draft: draft,
		UploadURL: fmt.Sprintf("%s/uploads/repos/o/r/releases/%d/assets{?name,label}", f.srv.URL, id)}}
	f.releases[id] = r
	return r
}

func (f *fakeGitHub) attach(r *fakeRelease, name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.assets = append(r.assets, &fakeAsset{Asset{ID: f.id(), Name: name, State: "uploaded"}, data})
}

func (f *fakeGitHub) release(tag string) *fakeRelease {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found *fakeRelease
	for _, r := range f.releases {
		if r.TagName == tag && (found == nil || r.ID < found.ID) {
			found = r
		}
	}
	return found
}

func (f *fakeGitHub) file(tag, name string) (string, bool) {
	r := f.release(tag)
	if r == nil {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range r.assets {
		if a.Name == name {
			return string(a.data), true
		}
	}
	return "", false
}

func (f *fakeGitHub) names(r *fakeRelease) []string {
	var out []string
	for _, a := range r.assets {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}

func (f *fakeGitHub) writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeGitHub) reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	f.calls = append(f.calls, r.Method+" "+p)
	if r.Header.Get("Authorization") != "Bearer t" && !strings.HasPrefix(p, "/storage/") {
		f.reply(w, 401, map[string]string{"message": "no token"})
		return
	}
	seg := strings.Split(strings.Trim(p, "/"), "/")
	num := func(i int) int64 { n, _ := strconv.ParseInt(seg[i], 10, 64); return n }
	switch {
	case strings.HasPrefix(p, "/storage/"):
		for _, rel := range f.releases {
			for _, a := range rel.assets {
				if a.ID == num(1) {
					_, _ = w.Write(a.data)
					return
				}
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(p, "/uploads/repos/o/r/releases/") && r.Method == http.MethodPost:
		rel := f.releases[num(5)]
		name := r.URL.Query().Get("name")
		for _, a := range rel.assets {
			if a.Name == name {
				f.reply(w, 422, map[string]string{"message": "Validation Failed"})
				return
			}
		}
		data, _ := io.ReadAll(r.Body)
		a := &fakeAsset{Asset{ID: f.id(), Name: name, State: "uploaded"}, data}
		rel.assets = append(rel.assets, a)
		f.reply(w, 201, a.Asset)
	case strings.HasPrefix(p, "/repos/o/r/releases"):
		f.serveReleases(w, r, seg, num)
	case strings.HasPrefix(p, "/repos/o/r/actions"):
		f.serveActions(w, r, seg, num)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) serveReleases(w http.ResponseWriter, r *http.Request, seg []string, num func(int) int64) {
	switch {
	case len(seg) == 4 && r.Method == http.MethodGet:
		var out []Release
		if r.URL.Query().Get("page") == "1" {
			for _, id := range sortedIDs(f.releases) {
				out = append(out, f.releases[id].Release)
			}
		}
		f.reply(w, 200, out)
	case len(seg) == 4 && r.Method == http.MethodPost:
		var in struct {
			TagName string `json:"tag_name"`
			Name    string
			Body    string
			Draft   bool
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		rel := f.addReleaseLocked(in.TagName, in.Draft, in.Body)
		if f.onCreate != nil {
			f.onCreate()
		}
		f.reply(w, 201, rel.Release)
	case len(seg) == 6 && seg[4] == "tags":
		for _, id := range sortedIDs(f.releases) {
			if rel := f.releases[id]; rel.TagName == seg[5] && !rel.Draft {
				f.reply(w, 200, rel.Release)
				return
			}
		}
		f.reply(w, 404, map[string]string{"message": "Not Found"})
	case len(seg) == 6 && seg[4] == "assets" && r.Method == http.MethodGet:
		http.Redirect(w, r, "/storage/"+seg[5], http.StatusFound)
	case len(seg) == 6 && seg[4] == "assets" && r.Method == http.MethodDelete:
		for _, rel := range f.releases {
			for i, a := range rel.assets {
				if a.ID == num(5) {
					rel.assets = append(rel.assets[:i], rel.assets[i+1:]...)
					f.reply(w, 204, nil)
					return
				}
			}
		}
		f.reply(w, 404, map[string]string{"message": "Not Found"})
	case len(seg) == 6 && seg[5] == "assets":
		var out []Asset
		if rel := f.releases[num(4)]; rel != nil && r.URL.Query().Get("page") == "1" {
			for _, a := range rel.assets {
				out = append(out, a.Asset)
			}
		}
		f.reply(w, 200, out)
	case len(seg) == 5 && r.Method == http.MethodGet:
		if rel := f.releases[num(4)]; rel != nil {
			f.reply(w, 200, rel.Release)
			return
		}
		f.reply(w, 404, map[string]string{"message": "Not Found"})
	case len(seg) == 5 && r.Method == http.MethodPatch:
		rel := f.releases[num(4)]
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if b, ok := in["body"].(string); ok {
			rel.Body = b
		}
		if d, ok := in["draft"].(bool); ok && rel.Draft && !d {
			snap := fakeSnapshot{Body: rel.Body, Assets: f.names(rel), Latest: fmt.Sprint(in["make_latest"])}
			for _, a := range rel.assets {
				if a.Name == SumsName {
					snap.Sums = string(a.data)
				}
			}
			f.published[rel.TagName] = snap
			rel.Draft = false
		}
		f.reply(w, 200, rel.Release)
	case len(seg) == 5 && r.Method == http.MethodDelete:
		delete(f.releases, num(4))
		f.reply(w, 204, nil)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) serveActions(w http.ResponseWriter, r *http.Request, seg []string, num func(int) int64) {
	switch {
	case len(seg) == 6 && seg[4] == "runs" && r.Method == http.MethodGet:
		run, ok := f.runs[num(5)]
		if !ok {
			f.reply(w, 404, map[string]string{"message": "Not Found"})
			return
		}
		f.reply(w, 200, run)
	case len(seg) == 9 && seg[6] == "attempts" && seg[8] == "jobs":
		var jobs []Job
		if r.URL.Query().Get("page") == "1" {
			jobs = f.jobs[seg[5]+"/"+seg[7]]
		}
		f.reply(w, 200, map[string]any{"total_count": len(jobs), "jobs": jobs})
	case len(seg) == 7 && seg[4] == "workflows" && seg[6] == "runs":
		q := r.URL.Query()
		id := f.newest[seg[5]+"/"+q.Get("branch")+"/"+q.Get("event")]
		var runs []Run
		if id != 0 {
			runs = []Run{{ID: id - 5}, {ID: id}, {ID: id - 9}}
		}
		f.reply(w, 200, map[string]any{"workflow_runs": runs})
	case len(seg) == 7 && seg[6] == "rerun-failed-jobs" && r.Method == http.MethodPost:
		if !f.rerunOK {
			f.reply(w, 403, map[string]string{"message": "This workflow run cannot be retried"})
			return
		}
		f.reruns = append(f.reruns, num(5))
		f.reply(w, 201, map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

func sortedIDs(m map[int64]*fakeRelease) []int64 {
	var ids []int64
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids
}
