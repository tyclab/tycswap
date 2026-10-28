# Codex applicability

Investigated on 2026-10-28 with installed `codex-cli 0.165.0`. Automatic account
selection could benefit Codex, but the Claude implementation cannot be reused
directly. No Codex routing feature was added.

## Installed-runtime evidence

`codex app-server generate-json-schema --out DIR` exposes `PreModelRequest` and
`PostModelRequest` hook configuration in this version. A temporary, isolated
`codex --no-daemon exec` process was pointed at a localhost Responses API fixture
with synthetic API-key credentials:

1. Its temporary `auth.json` initially contained key A.
2. A synchronous `PreModelRequest` command wrote a marker, replaced that file
   with key B, and exited with code 2.
3. The fixture still received `/v1/responses`, authenticated with key A, after
   the hook had run. Codex completed with exit code 0.

The probe used an empty temporary home, an explicit local provider with
`requires_openai_auth=true` and `supports_websockets=false`, and no inherited
operator credentials. Hook trust was bypassed only for the fixture's own command.
The fixture returned a minimal SSE `response.created` / `response.completed`
sequence. This establishes that a failed request hook does not block dispatch
and that replacing the auth file at that boundary does not change that request.
It does **not** establish live ChatGPT OAuth account-switch behavior.

The separately inspected local 0.164.0 source agrees: the model-request hooks in
`hooks/src/events/model_request.rs` are observation-only. Managed ChatGPT auth
in `login/src/auth/manager.rs` is cached, and its guarded reload rejects a changed
account ID. The model-provider auth adapter also binds credentials to an expected
account. These source findings are from the older version, not a claim that its
entire implementation matches 0.165.0.

## Feasible approaches and limits

The documented [app-server authentication interface](https://learn.chatgpt.com/docs/app-server#auth-endpoints)
offers experimental `chatgptAuthTokens` login and host-managed token refresh.
It is a plausible integration point for explicit account changes, but switching
existing threads and active requests still needs its own proof. In the inspected
source, internal child sessions share the parent's `AuthManager`; the generated
thread and turn request schemas expose no per-request account selector.
Independent simultaneous accounts therefore require isolated app-server or CLI
processes, or an additional integration that owns request dispatch. Restarting an
isolated process and resuming its conversation is the simpler baseline. The
[documented token-based app-server setup](https://developers.openai.com/siwc/token-sharing-open-source/codex-app-server)
also uses restart and resume when replacing its environment token.

tycswap already rotates Codex accounts using shared quota thresholds and
hysteresis. Model-aware selection would add value where accounts have different
model access or independently identified quota pools. The app-server reports
limits by limit ID, but those IDs do not by themselves establish a model-to-pool
mapping. Its model catalog is also not an entitlement check. These would need
verified inputs before automatic selection could improve the existing engine.

This is a separate, more complex integration than the Claude hook path. Retain
Codex's current restart-required behavior; the installed hooks cannot enforce
the routing coordinator's refusal or account choice.
