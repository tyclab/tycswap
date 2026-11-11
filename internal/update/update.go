// Package update is the update check and self-upgrade; ModulePath and ReleasesURL are vars for -ldflags -X.
package update

import "time"

var ModulePath = "github.com/tyclab/tycswap/cmd/tycswap"

// ReleasesURL is tycswap's own releases, never the forked project's (A24): its
// /latest redirect names the newest tag (A86), downloads come from
// download/<tag>/. Pre-releases do get a notice (Deviation #1).
var ReleasesURL = "https://github.com/tyclab/tycswap/releases"

const CheckoutHint = "built from a checkout: git pull && make install"

const CacheTTL = 24 * time.Hour

var FetchTimeout = 3 * time.Second
