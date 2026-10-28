# Model routing probe

This experiment runs the existing account switcher against temporary stores and
synthetic accounts. Its local coordinator reserves an account before dispatch and
releases it after the response. Requests for the same account may overlap;
requests needing a different account are refused while a reservation is active.

Run the coordinator checks:

```sh
go test -race ./experiments/model-routing -v
```

Run the native integration check on Linux with Claude Code available on PATH:

```sh
TYCSWAP_ROUTING_NATIVE_TEST=1 go test -race ./experiments/model-routing -v
```

The native checks load `plugin/` for temporary Claude processes. A local fake API
records model IDs and synthetic account numbers while the process changes models
through its control interface. No real model service or operator login is used.
The test verifies that the session ID stays the same and that an unavailable
account stops dispatch. A second check holds two responses open simultaneously
and verifies that isolated Fable and Opus profiles use different accounts. The
checks do not test the interactive model picker.

`TestNativeWorkerCrashBypassesModGuard` characterizes Claude Code 2.1.312's
worker failure behavior. A hook that stops responding is unloaded after the
worker heartbeat fails. Claude then sends a model request without acquiring the
fixture's account reservation, despite the hook's error handler. This matches
the [native runtime documentation](https://code.claude.com/docs/en/plugins/mods/troubleshoot#it-crashed-the-hooks-worker).
A hook error handler covers callback failures; it does not keep an unloaded
module in the request path.

The coordinator tests cover overlapping requests, model restrictions for child
requests, unavailable quota, and the selection-to-dispatch race without a
reservation. Account preferences and quota are fixtures; account changes use the
production `switching.SwitchTo` implementation.

The production implementation has a separate native integration test:

```sh
TYCSWAP_ROUTING_NATIVE_TEST=1 go test -race ./internal/session -run TestNativeManagedRoutingUsesProductionCoordinator -v
```

It uses the embedded routing mod and production coordinator with synthetic
accounts, fresh cached quota and a local fake API. It checks model changes,
quota exhaustion, request release and switch protection during a streamed reply.

This directory retains the original fixture with in-memory reservations and
fixed candidate preferences. The production implementation uses durable session
and request records, shared mutation guards, fresh usage and runtime readiness
checks. Its command and runtime limits are described in
[the command reference](../../docs/reference.md#tycswap-claude).

[Codex applicability](codex.md) records a separate investigation and an isolated
native probe of Codex 0.165.0's request hooks and credential reload behavior.
