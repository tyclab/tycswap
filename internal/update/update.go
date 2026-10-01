// Package update implements the Go-redesigned update check and self-upgrade
// subsystem.
//
// Implements spec 08§13 (update_check.py, redesigned) and 10-audit.md
// §Corrections.2, per DESIGN.md §6 Deviations 1–3 and Amendments A5/A6/A24.
// Unlike Python's PyPI/uv/pipx-based checker, this package queries a GitHub
// releases endpoint and compares versions with a real semver comparator
// (golang.org/x/mod/semver) on v-prefixed strings — so, deliberately unlike
// Python, a pre-release build DOES receive an update notice (Deviation #1).
// "Any error → no notification" and "cache failures too" are preserved
// (spec 08§13.2); the on-disk cache format is byte-compatible with Python's
// {"timestamp","data"} 24h-TTL shape via internal/usage's cache helpers
// (Amendment A6, A8).
package update

import "time"

// Endpoint is the releases API the update notice reads the latest tag from
// (Amendment A24): tycswap's own releases, never the project it was forked
// from. A package-level var (not a const) so a packager can point it elsewhere
// at build time and tests can point it at an httptest server:
//
//	-ldflags "-X github.com/tyclab/tycswap/internal/update.Endpoint=https://..."
var Endpoint = "https://api.github.com/repos/tyclab/tycswap/releases/latest"

// ModulePath is the package `tycswap upgrade` installs for a go-installed
// binary: `go install <ModulePath>@latest`. Overridable the same way:
//
//	-ldflags "-X github.com/tyclab/tycswap/internal/update.ModulePath=example.com/fork/cmd/tycswap"
var ModulePath = "github.com/tyclab/tycswap/cmd/tycswap"

// ReleasesURL is shown in manual-upgrade guidance when the install shape
// cannot be determined. Overridable with -X like the two above.
var ReleasesURL = "https://github.com/tyclab/tycswap/releases"

// CheckoutHint is what a binary built from a checkout is told instead of being
// re-installed from a remote: its source is the checkout, not the module proxy.
const CheckoutHint = "built from a checkout: git pull && make install"

// CacheTTL is the update-check cache freshness window: 24h, unchanged from
// Python (spec 08§13.1: CACHE_TTL = 24 * 3600).
const CacheTTL = 24 * time.Hour

// FetchTimeout bounds the releases-endpoint HTTP request (Amendment A6: 3s,
// vs. Python's 2s PyPI timeout). A var so tests can shrink it to exercise the
// timeout path without a slow test.
var FetchTimeout = 3 * time.Second
