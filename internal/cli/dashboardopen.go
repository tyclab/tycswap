package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/tyclab/tycswap/internal/atomicfile"
	"github.com/tyclab/tycswap/internal/paths"
)

type dashboardConnection struct {
	Base      string `json:"base"`
	TokenFile string `json:"tokenFile"`
}

func dashboardConnectionPath(remote bool) string {
	name := "app.connection.json"
	if remote {
		name = "remote.connection.json"
	}
	return filepath.Join(paths.GetBackupRoot(), name)
}

func publishDashboardConnection(rawURL, tokenFile string, remote bool) (func(), error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	base, err := remoteBaseURL(u.Scheme + "://" + u.Host)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(dashboardConnection{Base: base, TokenFile: tokenFile})
	if err != nil {
		return nil, err
	}
	path := dashboardConnectionPath(remote)
	if err := atomicfile.Write(path, data, atomicfile.Opts{FileMode: 0o600, DirMode: 0o700}); err != nil {
		return nil, err
	}
	return func() {
		if path != "" {
			_ = os.Remove(path)
			path = ""
		}
	}, nil
}

func runningDashboard() (*remoteClient, bool, error) {
	if _, err := paths.CheckPrivateRoot(paths.GetBackupRoot()); err != nil {
		return nil, false, err
	}
	for _, remote := range []bool{true, false} {
		lock := appLockPath()
		if remote {
			lock = remoteLockPath()
		}
		held, err := lockHeldAt(lock, 50*time.Millisecond)
		if err != nil {
			return nil, true, err
		}
		if !held {
			continue
		}
		raw, err := os.ReadFile(dashboardConnectionPath(remote))
		if err != nil {
			return nil, true, err
		}
		var connection dashboardConnection
		if err := json.Unmarshal(raw, &connection); err != nil {
			return nil, true, err
		}
		base, err := remoteBaseURL(connection.Base)
		if err != nil || connection.TokenFile == "" {
			return nil, true, fmt.Errorf("running app has invalid dashboard connection details")
		}
		client := newRemoteClient(base, connection.TokenFile)
		if err := client.loadToken(); err != nil {
			return nil, true, err
		}
		return client, true, nil
	}
	return nil, false, nil
}

var menuStartApp = startBackgroundApp

func menuOpenDashboard(prog string, s ioStreams) int {
	if code, blocked := guardRoot(s.err); blocked {
		return code
	}
	client, running, err := runningDashboard()
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(s.err, "Could not connect to the running dashboard: "+err.Error())
		return 1
	}
	if !running {
		if code := menuStartApp(prog, s); code != 0 {
			return code
		}
	}
	deadline := time.Now().Add(backgroundStartWait)
	for client == nil && time.Now().Before(deadline) {
		time.Sleep(backgroundPoll)
		client, _, err = runningDashboard()
		if err != nil && !os.IsNotExist(err) {
			break
		}
	}
	if client == nil {
		fmt.Fprintln(s.err, "The app has no ready dashboard connection. Open the dashboard from its tray, or restart an older app version.")
		return 1
	}
	u, err := client.Launch()
	if err == nil {
		err = openBrowser(u)
	}
	if err != nil {
		fmt.Fprintln(s.err, "Could not open the dashboard: "+err.Error())
		return 1
	}
	fmt.Fprintln(s.out, "Opened the running dashboard.")
	return 0
}
