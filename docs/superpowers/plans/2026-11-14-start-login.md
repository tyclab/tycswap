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
- [ ] Accept the startup-login rollout across the fleet. A dedicated Windows
  Ctrl+C console check remains a follow-up; the native Windows suite covers
  child exit status, spawn failures and existing command-shim behavior.

## S5

Superseded: the operator ruled on 2026-11-16 that `run`, `env`, `map` and
`unmap` go now, without the acceptance gate above. The retirement and
RECLAIM-3 to -5 are `docs/superpowers/plans/2026-11-16-busy-model-rotation.md`,
Stream B.

Stronger serialization with manual switches or roster edits is deferred until
needed. Startup uses the current engine's state → store → Claude lock order;
E1 must migrate all participants together if it changes that order. Failures
continue to produce an actionable notice and launch the child once.
