// Command printvars prints the update package's link-time variables, so a test
// can prove `-ldflags -X` reaches them.
package main

import (
	"fmt"

	"github.com/tyclab/tycswap/internal/update"
)

func main() {
	fmt.Println(update.ModulePath)
	fmt.Println(update.Endpoint)
	fmt.Println(update.ReleasesURL)
}
