package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Client is the few GitHub REST calls the release needs, on the workflow's
// own GITHUB_TOKEN.
type Client struct {
	API   string // https://api.github.com
	Repo  string // owner/name
	Token string
	HTTP  *http.Client
}

// ClientFromEnv reads GITHUB_API_URL, GITHUB_REPOSITORY and GH_TOKEN (or
// GITHUB_TOKEN), as a workflow step has them.
func ClientFromEnv() (*Client, error) {
	c := &Client{API: os.Getenv("GITHUB_API_URL"), Repo: os.Getenv("GITHUB_REPOSITORY"), Token: os.Getenv("GH_TOKEN"), HTTP: http.DefaultClient}
	if c.API == "" {
		c.API = "https://api.github.com"
	}
	if c.Token == "" {
		c.Token = os.Getenv("GITHUB_TOKEN")
	}
	if c.Repo == "" || c.Token == "" {
		return nil, errors.New("GITHUB_REPOSITORY and GH_TOKEN must be set")
	}
	return c, nil
}

// APIError is a response outside 2xx.
type APIError struct {
	Status  int
	Request string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.Request, e.Status, e.Message)
}

func isStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}

// maxBody bounds what one response may carry: a binary is about 10 MB.
const maxBody = 256 << 20

func (c *Client) do(ctx context.Context, method, target, contentType string, body []byte, accept string, out any) ([]byte, error) {
	if !strings.HasPrefix(target, "http") {
		target = strings.TrimRight(c.API, "/") + target
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Accept", "application/vnd.github+json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBody {
		return nil, fmt.Errorf("%s %s: response larger than %d bytes", method, target, maxBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var msg struct{ Message string }
		_ = json.Unmarshal(data, &msg)
		return nil, &APIError{Status: resp.StatusCode, Request: method + " " + target, Message: msg.Message}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, target, err)
		}
	}
	return data, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	_, err := c.do(ctx, http.MethodGet, path, "", nil, "", out)
	return err
}

func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, method, path, "application/json", body, "", out)
	return err
}

// pages calls fetch for page 1, 2, … until a page holds fewer than 100.
func pages(fetch func(page int) (int, error)) error {
	for page := 1; ; page++ {
		n, err := fetch(page)
		if err != nil || n < 100 {
			return err
		}
	}
}

func (c *Client) repoPath(format string, args ...any) string {
	return "/repos/" + c.Repo + fmt.Sprintf(format, args...)
}

// Release is a GitHub release; drafts are listed only to a token that can
// push.
type Release struct {
	ID         int64  `json:"id"`
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	UploadURL  string `json:"upload_url"`
}

// Asset is a release's file. State "uploaded" is a complete upload.
type Asset struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// Releases lists every release, drafts included for a pushing token.
func (c *Client) Releases(ctx context.Context) ([]Release, error) {
	var all []Release
	err := pages(func(page int) (int, error) {
		var batch []Release
		err := c.getJSON(ctx, c.repoPath("/releases?per_page=100&page=%d", page), &batch)
		all = append(all, batch...)
		return len(batch), err
	})
	return all, err
}

// PublishedRelease is tag's published release, nil when there is none.
func (c *Client) PublishedRelease(ctx context.Context, tag string) (*Release, error) {
	var r Release
	err := c.getJSON(ctx, c.repoPath("/releases/tags/%s", url.PathEscape(tag)), &r)
	if isStatus(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetRelease reads a release by id, a draft too for a pushing token.
func (c *Client) GetRelease(ctx context.Context, id int64) (Release, error) {
	var r Release
	err := c.getJSON(ctx, c.repoPath("/releases/%d", id), &r)
	return r, err
}

// CreateDraft creates tag's release as a draft.
func (c *Client) CreateDraft(ctx context.Context, tag, name, body string) (Release, error) {
	var r Release
	err := c.sendJSON(ctx, http.MethodPost, c.repoPath("/releases"),
		map[string]any{"tag_name": tag, "name": name, "body": body, "draft": true}, &r)
	return r, err
}

// UpdateRelease changes the given fields of a release.
func (c *Client) UpdateRelease(ctx context.Context, id int64, fields map[string]any) (Release, error) {
	var r Release
	err := c.sendJSON(ctx, http.MethodPatch, c.repoPath("/releases/%d", id), fields, &r)
	return r, err
}

// DeleteRelease deletes a release (not its tag).
func (c *Client) DeleteRelease(ctx context.Context, id int64) error {
	_, err := c.do(ctx, http.MethodDelete, c.repoPath("/releases/%d", id), "", nil, "", nil)
	return err
}

// Assets lists a release's files.
func (c *Client) Assets(ctx context.Context, releaseID int64) ([]Asset, error) {
	var all []Asset
	err := pages(func(page int) (int, error) {
		var batch []Asset
		err := c.getJSON(ctx, c.repoPath("/releases/%d/assets?per_page=100&page=%d", releaseID, page), &batch)
		all = append(all, batch...)
		return len(batch), err
	})
	return all, err
}

// errAssetExists is GitHub's 422 for an upload whose name a release holds.
var errAssetExists = errors.New("the release already has an asset of that name")

// UploadAsset adds a file to a release; GitHub never replaces one in place.
func (c *Client) UploadAsset(ctx context.Context, r Release, name string, data []byte) (Asset, error) {
	target, _, _ := strings.Cut(r.UploadURL, "{")
	var a Asset
	_, err := c.do(ctx, http.MethodPost, target+"?name="+url.QueryEscape(name), "application/octet-stream", data, "", &a)
	if isStatus(err, http.StatusUnprocessableEntity) {
		return a, errAssetExists
	}
	return a, err
}

// DownloadAsset reads a release file; GitHub redirects to its storage, and
// Go does not carry the token to another host.
func (c *Client) DownloadAsset(ctx context.Context, id int64) ([]byte, error) {
	return c.do(ctx, http.MethodGet, c.repoPath("/releases/assets/%d", id), "", nil, "application/octet-stream", nil)
}

// DeleteAsset removes a release file.
func (c *Client) DeleteAsset(ctx context.Context, id int64) error {
	_, err := c.do(ctx, http.MethodDelete, c.repoPath("/releases/assets/%d", id), "", nil, "", nil)
	return err
}

// GetRun reads a workflow run.
func (c *Client) GetRun(ctx context.Context, id int64) (Run, error) {
	var r Run
	err := c.getJSON(ctx, c.repoPath("/actions/runs/%d", id), &r)
	return r, err
}

// AttemptJobs lists the jobs of one attempt of a run, which a later attempt
// does not change.
func (c *Client) AttemptJobs(ctx context.Context, id int64, attempt int) ([]Job, error) {
	var all []Job
	err := pages(func(page int) (int, error) {
		var batch struct{ Jobs []Job }
		err := c.getJSON(ctx, c.repoPath("/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", id, attempt, page), &batch)
		all = append(all, batch.Jobs...)
		return len(batch.Jobs), err
	})
	return all, err
}

// NewestRun is the highest run id of a workflow on branch for event, among
// the newest hundred; 0 when there is none.
func (c *Client) NewestRun(ctx context.Context, workflowID int64, branch, event string) (int64, error) {
	var page struct {
		Runs []Run `json:"workflow_runs"`
	}
	q := url.Values{"branch": {branch}, "event": {event}, "per_page": {"100"}}
	err := c.getJSON(ctx, c.repoPath("/actions/workflows/%s/runs?%s", strconv.FormatInt(workflowID, 10), q.Encode()), &page)
	var newest int64
	for _, r := range page.Runs {
		newest = max(newest, r.ID)
	}
	return newest, err
}

// RunOn is the newest run of a workflow (by file name) for a push of sha;
// its ID is 0 when there is none, or no such workflow on the default branch.
func (c *Client) RunOn(ctx context.Context, workflow, sha string) (Run, error) {
	var page struct {
		Runs []Run `json:"workflow_runs"`
	}
	q := url.Values{"head_sha": {sha}, "event": {"push"}, "per_page": {"100"}}
	err := c.getJSON(ctx, c.repoPath("/actions/workflows/%s/runs?%s", url.PathEscape(workflow), q.Encode()), &page)
	if isStatus(err, http.StatusNotFound) {
		return Run{}, nil
	}
	var newest Run
	for _, r := range page.Runs {
		if r.ID > newest.ID {
			newest = r
		}
	}
	return newest, err
}

// RerunFailedJobs reruns a run's failed and cancelled jobs and the jobs that
// need them.
func (c *Client) RerunFailedJobs(ctx context.Context, id int64) error {
	_, err := c.do(ctx, http.MethodPost, c.repoPath("/actions/runs/%d/rerun-failed-jobs", id), "", nil, "", nil)
	return err
}
