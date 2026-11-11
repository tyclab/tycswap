// checker.go: any error means no notice; negative results are cached too, in the 24h cache/update_check.json (Amendment A6).

package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/tyclab/tycswap/internal/clock"
	"github.com/tyclab/tycswap/internal/platform"
	"github.com/tyclab/tycswap/internal/usage"
)

// Checker performs the passive update check. The zero value is usable in
// production: it reads the real ReleasesURL via http.DefaultClient against
// the real wall clock and OS environment. Tests override every seam (CacheDir is
// mandatory — there is no sane default).
type Checker struct {
	CacheDir string
	// HTTPClient asks the release page; nil -> http.DefaultClient.
	HTTPClient *http.Client
	// Clk supplies the cache timestamp and the time a limited network may
	// try again; nil -> clock.System{}.
	Clk    clock.Clock
	Getenv func(string) string
	// HomeDir is $HOME for install-shape detection; "" -> os.UserHomeDir().
	HomeDir string
}

func (c Checker) clock() clock.Clock {
	if c.Clk != nil {
		return c.Clk
	}
	return clock.System{}
}

func (c Checker) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c Checker) getenv() func(string) string {
	if c.Getenv != nil {
		return c.Getenv
	}
	return os.Getenv
}

func (c Checker) homeDir() string {
	if c.HomeDir != "" {
		return c.HomeDir
	}
	h, _ := os.UserHomeDir()
	return h
}

// CheckForUpdate returns a notice when a newer version exists, else ""; every failure folds into "" (spec 08§13.2).
// currentVersion keeps its "v" prefix (version.Version, not Display()): the semver comparator needs it.
func (c Checker) CheckForUpdate(exePath, currentVersion string, plat platform.Platform) string {
	cachePath := filepath.Join(c.CacheDir, "update_check.json")
	now := clock.Seconds(c.clock())

	var latest string
	if cached, ok := usage.ReadCache(cachePath, CacheTTL.Seconds(), now); ok {
		latest, _ = cached.(string)
	} else {
		latest = c.fetchLatestTag()
		var data any
		if latest != "" {
			data = latest
		}
		// Write regardless of success/failure, caching negative results too
		// (spec 08§13.2). Write errors are swallowed — never fail the check.
		_ = usage.WriteCache(cachePath, data, now)
	}

	if latest == "" || !semver.IsValid(latest) || !semver.IsValid(currentVersion) {
		return ""
	}
	if semver.Compare(latest, currentVersion) <= 0 {
		return ""
	}

	hint := UpgradeHint(UpgradePlan(DetectBuildSource(), exePath, c.getenv(), c.homeDir()), plat)
	return fmt.Sprintf(
		"A newer version of tycswap is available (%s). You are using %s. %s",
		strings.TrimPrefix(latest, "v"), strings.TrimPrefix(currentVersion, "v"), hint,
	)
}

func (c Checker) fetchLatestTag() string {
	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	tag, err := c.latestTag(ctx)
	if err != nil {
		return ""
	}
	return tag
}

// Latest asks the release page for the latest release's tag now and says why
// when it cannot (DESIGN A27: the dashboard's Updates card names what it could
// not check). The answer also refreshes cache/update_check.json, so the passive
// notice of the next CLI command agrees with the card; a failure leaves the
// cache alone. The tag is a v-prefixed semver string; one that is not
// semver is an error, never a "newer version".
func (c Checker) Latest(ctx context.Context) (string, error) {
	tag, err := c.latestTag(ctx)
	if err != nil {
		return "", err
	}
	if !semver.IsValid(tag) {
		return "", fmt.Errorf("release tag %q is not a version", tag)
	}
	_ = usage.WriteCache(filepath.Join(c.CacheDir, "update_check.json"), tag, clock.Seconds(c.clock()))
	return tag, nil
}

// latestTag is the one request both Latest and fetchLatestTag make.
func (c Checker) latestTag(ctx context.Context) (string, error) {
	return fetchLatestTag(ctx, c.client(), ReleasesURL, c.clock())
}

// limitedMsg is GitHub's 403 or 429: it limits the whole network (every
// program and device behind its address), not this program.
const limitedMsg = "GitHub is limiting requests from this network; try again later"

// fetchLatestTag reads the latest release's tag from where releases/latest
// redirects, the tag page, without following the redirect (DESIGN A86): the
// API's unauthenticated 60 requests an hour are shared by the whole network.
// The download shape of SelfUpgrade asks the same.
func fetchLatestTag(ctx context.Context, client *http.Client, releases string, clk clock.Clock) (string, error) {
	latest := strings.TrimRight(releases, "/") + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latest, nil)
	if err != nil {
		return "", err
	}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if loc, err := resp.Location(); err == nil {
			if tag, ok := tagFromLocation(loc); ok {
				return tag, nil
			}
		}
		// A repository without releases redirects to its releases list.
		return "", fmt.Errorf("no release found at %s", latest)
	case http.StatusForbidden, http.StatusTooManyRequests:
		if at, ok := retryAt(resp.Header, clk.Now()); ok {
			return "", fmt.Errorf("%s (after %s)", limitedMsg, at.Local().Format("15:04"))
		}
		return "", errors.New(limitedMsg)
	case http.StatusNotFound:
		return "", fmt.Errorf("no release found at %s", latest)
	}
	return "", fmt.Errorf("%s: HTTP %d", latest, resp.StatusCode)
}

// tagFromLocation is <tag> of a URL whose path ends in /releases/tag/<tag>.
// The escaped path is cut so an escaped "/" stays inside the tag.
func tagFromLocation(loc *url.URL) (string, bool) {
	const marker = "/releases/tag/"
	p := loc.EscapedPath()
	i := strings.LastIndex(p, marker)
	if i < 0 {
		return "", false
	}
	tag, err := url.PathUnescape(p[i+len(marker):])
	if err != nil || tag == "" {
		return "", false
	}
	return tag, true
}

// retryAt is when a limited network may ask again: Retry-After (seconds from
// now) or else GitHub's x-ratelimit-reset (Unix seconds).
func retryAt(h http.Header, now time.Time) (time.Time, bool) {
	if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 32); err == nil && s >= 0 {
		return now.Add(time.Duration(s) * time.Second), true
	}
	if u, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-Ratelimit-Reset")), 10, 64); err == nil && u > 0 {
		return time.Unix(u, 0), true
	}
	return time.Time{}, false
}
