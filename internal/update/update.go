// Package update is the update check and self-upgrade; Endpoint, ModulePath and ReleasesURL are vars for -ldflags -X.
package update

import "time"

// tycswap's own releases, never the forked project's (A24). Pre-releases do get a notice (Deviation #1).
var Endpoint = "https://api.github.com/repos/tyclab/tycswap/releases/latest"

var ModulePath = "github.com/tyclab/tycswap/cmd/tycswap"

var ReleasesURL = "https://github.com/tyclab/tycswap/releases"

const CheckoutHint = "built from a checkout: git pull && make install"

const CacheTTL = 24 * time.Hour

var FetchTimeout = 3 * time.Second
