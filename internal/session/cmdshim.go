package session

import (
	"github.com/tyclab/tycswap/internal/cerr"
	"github.com/tyclab/tycswap/internal/launch"
)

func CheckCmdShimArgs(bin string, args []string) error {
	if err := launch.CheckCmdShimArgs(bin, args); err != nil {
		return cerr.Validation("%s", err.Error())
	}
	return nil
}
