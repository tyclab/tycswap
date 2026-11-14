# Start login: remaining rollout and follow-up

Design: `docs/superpowers/specs/2026-11-14-start-login-design.md`.
The first-release implementation covers S1–S4 and S6. The provisioner launcher
ships alongside its release pin. Account marks remain an operator choice.

## Release and fleet acceptance

- [ ] Complete the repository gates and native Windows/macOS CI on the final
  change, then use the normal release and provisioner-pin workflow. Tests use
  temporary stores and fake providers; Go builds run on the dev VM.
- [ ] Deploy the launcher and verify routing and fallback on each participating
  box. The operator selects marks and authorizes any real-login acceptance
  test. Verify one child invocation, shared-login selection and cooldown with
  authorized accounts. Record sanitized results, not private transcripts.
- [ ] Accept the replacement across the fleet before starting S5. A dedicated
  Windows Ctrl+C console check remains a follow-up; the native Windows suite
  covers child exit status, spawn failures and existing command-shim behavior.

## Later release: S5

`run`, `env`, `map` and `unmap` remain available until the replacement is
accepted. RECLAIM-1 through RECLAIM-5 stay open. The first release documents
their current limitations; it does not fix or conceal them.

- [ ] Review a separate retirement change against the then-current code. Audit
  legacy claims/releases, mappings, session helpers, group retirement and all
  callers before deleting anything. Preserve conversation history and login
  ownership, and leave live or unverifiable claims intact.
- [ ] Review and incorporate the private reproduction fixture
  `tycswap-review-20261114/review_repro_test.go`, which is not tracked here.
  Test idle, live, rotated/expired, invalid-grant and offline claims, scheduling
  on real versus observe/dry-run ticks, and the removed command/help paths.
- [ ] Remove each review-finding row only in the change that closes it.

Stronger serialization with manual switches or roster edits is deferred until
needed. Startup uses the current engine's state → store → Claude lock order;
E1 must migrate all participants together if it changes that order. Failures
continue to produce an actionable notice and launch the child once.
