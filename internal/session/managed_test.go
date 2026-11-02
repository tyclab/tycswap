package session_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tyclab/tycswap/internal/groups"
	"github.com/tyclab/tycswap/internal/routingstate"
	"github.com/tyclab/tycswap/internal/session"
)

type managedRunner struct {
	groupRunner
	version    string
	failPlugin bool
	probes     [][]string
	probeEnv   []string
	probeCWD   string
}

func (r *managedRunner) ProbeInDir(argv, env []string, timeout time.Duration, cwd string) (string, int, error) {
	r.probeEnv, r.probeCWD = env, cwd
	return r.Probe(argv, env, timeout)
}

func (r *managedRunner) Probe(argv, env []string, _ time.Duration) (string, int, error) {
	r.probes = append(r.probes, argv)
	if len(argv) == 2 && argv[1] == "--version" {
		return r.version, 0, nil
	}
	if r.failPlugin {
		return "", 1, nil
	}
	return "all passed", 0, nil
}

func TestAutomaticLaunchReservesStartupAndInstallsMod(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	launch, err := manager.PrepareManaged("1", "sonnet[1m]", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Group != groups.Opus || !launch.Routing || launch.Model != "sonnet[1m]" {
		t.Fatalf("unexpected launch: %+v", launch)
	}
	if envValue(launch.Env, "TYCSWAP_ROUTING") != "1" || envValue(launch.Env, "TYCSWAP_ROUTE_LAUNCH") != launch.LaunchID {
		t.Fatal("missing routing environment")
	}
	state, err := routingstate.Read(sw.Store.SharedRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Sessions) != 1 || len(state.Leases) != 1 {
		t.Fatalf("startup is not reserved: %+v", state)
	}
	if err := state.CheckSwitch("opus", "2"); err == nil {
		t.Fatal("different account can replace startup reservation")
	}
	plugin := launch.Args[len(launch.Args)-1]
	if _, err := os.Stat(filepath.Join(plugin, "hooks", "register.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plugin, "check.test.ts")); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticLaunchSkipsCurrentAccountWithoutStartCapability(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	if _, err := manager.PrepareGroup("fable", "1", "fable", "", cwd); err != nil {
		t.Fatal(err)
	}
	scoped, err := sw.Store.ForGroup(groups.Fable)
	if err != nil {
		t.Fatal(err)
	}
	if err := scoped.SetGroupCapability("1", groups.Start, false); err != nil {
		t.Fatal(err)
	}
	launch, err := manager.PrepareManaged("", "fable", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	if launch.AccountNumber != "2" {
		t.Fatalf("picked startup-ineligible account: %s", launch.AccountNumber)
	}
}

func TestAutomaticResumePreservesProfileAndLastModel(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	launch, err := manager.PrepareManaged("1", "fable", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	transcript(t, launch.ProfileDir, launch.SessionID, cwd)
	if err := routingstate.With(sw.Store.SharedRoot(), func(s *routingstate.State) error {
		s.Sessions = map[string]routingstate.Session{}
		s.Leases = map[string]routingstate.Lease{}
		s.History[routingstate.Key("fable", launch.SessionID)] = routingstate.Session{Group: "fable", ID: launch.SessionID, PID: os.Getpid(), Model: "opus", Launch: launch.LaunchID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := &managedRunner{version: "2.1.312 (Claude Code)"}
	manager = session.NewManager(sw, session.Options{Runner: runner, Stdout: io.Discard, Keychain: sw.Keychain(), HookExecutable: "/tmp/fixture tycswap"})
	if err := manager.RunGroup("fable", "", "", launch.SessionID, cwd, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(runner.argv, " "), "--model opus") || envValue(runner.env, "CLAUDE_CONFIG_DIR") != launch.ProfileDir || envValue(runner.env, "TYCSWAP_ROUTING") != "1" {
		t.Fatalf("resume lost routing/profile/model: %v", runner.argv)
	}
	if strings.Contains(strings.Join(runner.argv, " "), "--fork-session") {
		t.Fatal("existing routed session was forked")
	}
	if envValue(runner.probeEnv, "CLAUDE_CONFIG_DIR") != launch.ProfileDir || runner.probeCWD != cwd {
		t.Fatal("readiness was checked outside the resumed profile and working directory")
	}
}

func TestAutomaticResumeUsesLaunchWhenRegistrationNeverCompleted(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	launch, err := manager.PrepareManaged("1", "fable", "", cwd)
	if err != nil {
		t.Fatal(err)
	}
	transcript(t, launch.ProfileDir, launch.SessionID, cwd)
	if err := routingstate.With(sw.Store.SharedRoot(), func(s *routingstate.State) error {
		s.Sessions = map[string]routingstate.Session{}
		s.Leases = map[string]routingstate.Lease{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := manager.PrepareManaged("", "opus", launch.SessionID, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ProfileDir != launch.ProfileDir || resumed.Migrating || !resumed.Routing {
		t.Fatal("resume lost managed launch identity")
	}
}

func TestAutomaticReadinessFailureDoesNotClaimAccount(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		failPlugin    bool
	}{
		{"old native", "2.1.308", false}, {"missing mods", "2.1.312", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sw, _, _, cwd := sessionFixture(t)
			runner := &managedRunner{version: tc.version, failPlugin: tc.failPlugin}
			manager := session.NewManager(sw, session.Options{Runner: runner, Stdout: io.Discard, Keychain: sw.Keychain(), HookExecutable: "/tmp/fixture tycswap"})
			if err := manager.RunManaged("1", "fable", "", cwd, nil); err == nil {
				t.Fatal("unsupported native accepted")
			}
			if _, err := os.Stat(groups.ProfileDir(sw.Store.SharedRoot(), groups.Fable)); !os.IsNotExist(err) {
				t.Fatalf("readiness failure mutated profile: %v", err)
			}
			if len(runner.argv) != 0 {
				t.Fatal("failed readiness executed Claude")
			}
		})
	}
}

func TestAutomaticLaunchRejectsRoutingBypassesAndUnknownModels(t *testing.T) {
	for _, arg := range []string{"--bare", "--safe-mode", "--continue", "--settings=x", "--plugin-dir=x", "--fork-session", "--session-id=x", "--fallback-model=opus"} {
		t.Run(arg, func(t *testing.T) {
			_, manager, _, cwd := sessionFixture(t)
			if err := manager.RunManaged("", "fable", "", cwd, []string{arg}); err == nil {
				t.Fatal("routing bypass accepted")
			}
		})
	}
	for _, model := range []string{"default", "custom-alias", "fable/opus", "claude-fable-5$"} {
		t.Run(model, func(t *testing.T) {
			_, manager, _, cwd := sessionFixture(t)
			if _, err := manager.PrepareManaged("", model, "", cwd); err == nil {
				t.Fatal("unknown model accepted")
			}
		})
	}
}

func TestAutomaticLaunchRejectsUnmanagedNativeAndDuplicateResume(t *testing.T) {
	t.Run("unmanaged process", func(t *testing.T) {
		sw, manager, _, cwd := sessionFixture(t)
		profile := groups.ProfileDir(sw.Store.SharedRoot(), groups.Fable)
		if err := groups.WriteJSON(filepath.Join(profile, "sessions", "fixture.json"), map[string]any{"pid": os.Getpid(), "sessionId": "legacy-session"}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.PrepareManaged("1", "fable", "", cwd); err == nil || !strings.Contains(err.Error(), "unmanaged") {
			t.Fatalf("unmanaged process accepted: %v", err)
		}
	})
	t.Run("reserved resume", func(t *testing.T) {
		_, manager, _, cwd := sessionFixture(t)
		launch, err := manager.PrepareManaged("1", "fable", "", cwd)
		if err != nil {
			t.Fatal(err)
		}
		transcript(t, launch.ProfileDir, launch.SessionID, cwd)
		if _, err := manager.PrepareManaged("1", "fable", launch.SessionID, cwd); err == nil || !strings.Contains(err.Error(), "already running") {
			t.Fatalf("reserved resume accepted: %v", err)
		}
	})
}

func TestAutomaticLaunchRejectsFallbackAndRoutingEnvironmentOverrides(t *testing.T) {
	for _, config := range []map[string]any{
		{"fallbackModel": "opus"},
		{"env": map[string]any{"TYCSWAP_ROUTE_BINARY": "/other/tycswap"}},
		{"env": map[string]any{"TYCSWAP_GROUP": "opus"}},
		{"allowManagedModsOnly": true},
	} {
		sw, manager, _, cwd := sessionFixture(t)
		if err := groups.WriteJSON(filepath.Join(sw.DefaultProfileDir(), "settings.json"), config); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.PrepareManaged("1", "fable", "", cwd); err == nil {
			t.Fatalf("unsafe settings accepted: %v", config)
		}
	}
}

type waitingLegacyRunner struct {
	groupRunner
	entered chan struct{}
	release chan struct{}
}

func (r *waitingLegacyRunner) Exec(_ string, _, _ []string) error {
	close(r.entered)
	<-r.release
	return nil
}
func (r *waitingLegacyRunner) ExecInDir(bin string, argv, env []string, _ string) error {
	return r.Exec(bin, argv, env)
}

func TestLegacyAndAutomaticLaunchesCannotOverlapBootstrap(t *testing.T) {
	t.Run("managed blocks legacy", func(t *testing.T) {
		_, manager, _, cwd := sessionFixture(t)
		if _, err := manager.PrepareManaged("1", "fable", "", cwd); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.PrepareGroup("Fable", "1", "fable", "", cwd); err == nil {
			t.Fatal("legacy preparation entered managed profile")
		}
	})
	t.Run("legacy reservation blocks managed before native registration", func(t *testing.T) {
		sw, manager, _, cwd := sessionFixture(t)
		runner := &waitingLegacyRunner{entered: make(chan struct{}), release: make(chan struct{})}
		legacy := session.NewManager(sw, session.Options{Runner: runner, Stdout: io.Discard, Keychain: sw.Keychain(), HookExecutable: "/tmp/fixture tycswap"})
		done := make(chan error, 1)
		go func() { done <- legacy.RunGroup("fable", "1", "fable", "", cwd, nil) }()
		released := false
		defer func() {
			if !released {
				close(runner.release)
			}
		}()
		select {
		case <-runner.entered:
		case err := <-done:
			t.Fatalf("legacy launch failed: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("legacy launcher did not reach exec")
		}
		if _, err := manager.PrepareManaged("1", "fable", "", cwd); err == nil || !strings.Contains(err.Error(), "legacy launch") {
			t.Fatalf("managed bootstrap bypassed pending legacy launch: %v", err)
		}
		close(runner.release)
		released = true
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if _, err := manager.PrepareManaged("1", "fable", "", cwd); err != nil {
			t.Fatalf("finished legacy launch held reservation: %v", err)
		}
	})
}

func TestAutomaticLaunchRejectsUnreadableNativeRegistry(t *testing.T) {
	sw, manager, _, cwd := sessionFixture(t)
	path := filepath.Join(groups.ProfileDir(sw.Store.SharedRoot(), groups.Fable), "sessions", "fixture.json")
	if err := groups.WriteJSON(path, map[string]any{"sessionId": "missing-pid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareManaged("1", "fable", "", cwd); err == nil || !strings.Contains(err.Error(), "no valid PID") {
		t.Fatalf("unknown native owner accepted: %v", err)
	}
}
