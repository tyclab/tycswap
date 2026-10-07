package update

import "time"

var Endpoint = "https://api.github.com/repos/tyclab/tycswap/releases/latest"

var ModulePath = "github.com/tyclab/tycswap/cmd/tycswap"

var ReleasesURL = "https://github.com/tyclab/tycswap/releases"

const CheckoutHint = "built from a checkout: git pull && make install"

const CacheTTL = 24 * time.Hour

var FetchTimeout = 3 * time.Second
