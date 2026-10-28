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

The coordinator tests cover overlapping requests, model restrictions for child
requests, unavailable quota, and the selection-to-dispatch race without a
reservation. Account preferences and quota are fixtures; account changes use the
production `switching.SwitchTo` implementation.

This is a test fixture, not an installable account-routing feature. Reservations
are in memory and only coordinate participating requests. A production version
still needs cancellation and crash recovery, integration with every account
mutation path, runtime guard availability checks, and real quota selection.
