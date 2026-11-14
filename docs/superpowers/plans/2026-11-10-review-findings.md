# Review findings, 2026-11-10

Open work only: a row is deleted by the change that closes it. Design:
`docs/superpowers/specs/2026-11-10-one-engine-design.md` (D-numbers below).

Verdict: FIX, CUT (goes with a removal in D10), DUP (same defect, other
lane), BY-DESIGN, PROVISIONER (fixed in
flakelab). Evidence at review time: T repro test, V re-checked at the cited
lines by the lead, L seen in a live log, P plausible (one fact unproven),
blank is a file:line trace. Series: E engine, C credentials, U screens,
R removals and rest.

Each FIX row is closed test-first: the regression test fails on the parent
commit for the stated reason, or the row is closed as not real with that
evidence.

## Evidence added after the review

- Central log store, 7 days, both boxes: 12 `Dashboard:` start lines, none
  with a token (HOST-6 is a local-journal exposure, not a remote one).
- The second box's timer journal: 2,649 ticks, 5 switches, 5 `cooldown`
  no-switch ticks (each means an engine switch less than a cooldown earlier
  that the timer did not make). One switch moved between two slots that carry
  the same email, so the same-email rows (STORE-7, STORE-16, WEB-9) describe a
  roster that exists.
- The tool's own log file and the output of the hook and status line ticks
  are not shipped anywhere; D5 (`--headless` prints every event) closes that.

## From the review of the off-switch change (v0.10.0)

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| OFF-2 | a tray click that only ends a dashboard dry run says "Auto-switch off" | cli/appshell.go, webfacades.go | FIX D5 (`SetEnabled`) |  | E |
| OFF-3 | an unreadable `settings.json` reads as off with no stored warning; `auto on` fails on it | settings/settings.go | FIX D1 D7 |  | E |
| OFF-4 | the hosted Codex loop reacts up to one interval after Start or `auto on`; it has no wake | cli/codexauto.go, webfacades.go | FIX D3 (one tick) |  | E |
| OFF-5 | saved bars other than the model reach the app's engine only at app start, since Stop no longer ends it | cli/webfacades.go | FIX D3 (settings read per tick) |  | E |

## From the review of the automatic reclaim (v0.11.6)

Planned follow-up: `docs/superpowers/plans/2026-11-14-start-login.md`
(spec S5, S6). S5 is a separate release after fleet acceptance; the first
startup-login release does not close RECLAIM-1 through RECLAIM-5.
Each T row has a private repro test that passes on v0.11.5 or fails only on
v0.11.6's new claim. The fixture
`tycswap-review-20261114/review_repro_test.go` is not tracked in this repository;
review and incorporate it with the later S5 implementation.

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| RECLAIM-1 | a tick below every bar logs a shell pinned with `env` out: every real tick releases every unused profile before polling, and a pinned shell without a Claude process is unused | autoswitch/tick.go tickInner, core/autoswitch_adapters.go | CUT S5 (`env` goes) | T | R |
| RECLAIM-2 | a tick between `run`'s lock release and Claude's `sessions/<pid>.json` (1.8 s after `exec` in one traced start) releases the starting profile and can make its account the default login too | session/bootstrap.go SetupSession, switching/legacyrelease.go | CUT S5 (`run` goes) | T | R |
| RECLAIM-3 | a profile login that rotated in its profile and then expired is never released: the release asks the profile endpoint, which an expired token cannot answer; the tick reports `no-comparison` | switching/legacyrelease.go ReleaseIdleLegacy | FIX S5 (verify by refresh) | T | R |
| RECLAIM-4 | the snapshot calls an account eligible when a release candidate exists, the engine refuses it when the release cannot verify: dashboard and tray show no reason | store/identity.go RotationOwnershipBlockReason | FIX S5 (reason stored on the claim) | T | R |
| RECLAIM-5 | a `reserved` or `model-missing` tick with a held account emits `accounts-in-use` and `…-no-target` | autoswitch/tick.go selectCandidates | FIX S5 | T | R |

## Engine lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| ENG-1 | observe forces a usage request every 15 s, also for another account's status line | cli/observe.go, reporting/markdue.go | FIX D6 | TVL | E |
| ENG-2 | proactive move judged on one window: two accounts trade places every cooldown | autoswitch/tick.go selectCandidates | FIX D4 | TVL | E |
| ENG-3 | a manual switch is undone by the next tick | autoswitch/tick.go, state.go | FIX D2 | VL | E |
| ENG-4 | the switch step never re-checks the live account | autoswitch/tick.go perform | FIX D3 | TV | E |
| ENG-5 | failover strikes count ticks, from every source | autoswitch/tick.go countUnknownTick | FIX D4 | T | E |
| ENG-6 | candidate without a 5h window judged on the 5h line | autoswitch/tick.go | FIX D4 | T | E |
| ENG-7 | window at 100 % stays full after its reset passed | usage/store.go, entry.go | FIX D4 | T | E |
| ENG-8 | loop hosts never apply the session policy | cli/auto.go, tuiwire.go, webfacades.go | FIX D3 | V | E |
| ENG-9 | all-exhausted sleeps up to 6 h, nothing wakes it | autoswitch/loop.go | FIX D5 | V | E |
| ENG-10 | switch warnings dropped (`[]string` vs `[]any`) | autoswitch/tick.go sliceOf | FIX D4 | T | E |
| ENG-11 | idle login with an expired token is failed over | autoswitch/tick.go | FIX D4 D8 | L | E |
| ENG-12 | `auto` usage error exits 2, the no-action status | cli/config.go subError, auto.go | FIX D4 | V | E |
| ENG-13 | failed state write reports an error for a switch that happened | autoswitch/tick.go perform | FIX D4 |  | E |
| ENG-14 | Codex 429 spelled `http 429`, never stamps `last429At` | codex/api/usage.go | FIX D4 | T | E |
| ENG-15 | one-time Codex import reruns whenever the store is empty | cli/codex.go, registryimport | FIX D4 |  | E |
| ENG-16 | backward clock step freezes polling and trust | usage/store.go, autoswitch/state.go | FIX D4 | P | E |
| ENG-17 | `nan` accepted for numeric flags; loop spins | cli/auto.go, settings clampValue | FIX D4 | T | E |

## Hosting lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| HOST-2 | hosted engine lacks A75 to A77 | cli/tuiwire.go | DUP ENG-8 |  |  |
| HOST-3 | Stop during a tick reports failure; the retry turns auto on | cli/webfacades.go Stop, appshell.go | FIX D5 | TV | E |
| HOST-5 | exit gives a tick in flight 2 s | cli/webfacades.go stopKeepingChoice | FIX D5 | T | E |
| HOST-6 | launch URL with its token printed to the service journal (local journal only: the central log store holds the line with the token redacted) | cli/appcmd.go, web/web.go | FIX D5 | VL | E |
| HOST-7 | nil Codex engine logs an error every tick | cli/webfacades.go codexTick | FIX D5 | TV | E |
| HOST-8 | handover rotation switches inside every state build | cli/recovery_runtime.go | CUT D10 |  | R |
| HOST-9 | hosted loop asleep after all-exhausted | autoswitch/loop.go | DUP ENG-9 |  |  |
| HOST-10 | Claude incident recorded without an account | cli/recovery.go | CUT D10 | T | R |
| HOST-11 | default-login session cannot be handed over | cli/recovery_runtime.go | CUT D10 | T | R |
| HOST-12 | unconfirmed handover blocks the workspace for good | cli/recovery_web.go | CUT D10 | T | R |
| HOST-13 | reclaim always fails for a Claude destination | cli/recovery_reclaim.go | CUT D10 | T | R |
| HOST-14 | start-at-login entry drops `--no-update-check` | cli/appcmd.go autostartConfig | FIX D9 | V | U |
| HOST-15 | Updates card says installed on any exit 0 | cli/updateshost.go applyApp | FIX |  | R |

## CLI and TUI lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| CLI-1 | TUI Auto screen runs its own engine beside the scheduler | tui/autoview.go, cli/tuiwire.go | CUT D5 | V | E |
| CLI-2 | leaving the Auto screen does not stop a tick in flight | tui/autoview.go | CUT D5 |  | E |
| CLI-3 | `add --login` scratch profiles never reaped unless Ctrl-C | cli/logincleanup.go, sigint.go | FIX | L | R |
| CLI-4 | `--slot` overwrite prompt while the login owns the terminal | cli/loginwait.go | FIX | P | R |
| CLI-5 | login stored before its organization name is written | cli/loginwait.go, lifecycle/addsource.go | FIX | P | R |
| CLI-6 | `auto` usage error exits 2 | cli/config.go | DUP ENG-12 |  |  |
| CLI-7 | pinned shell: other commands fall back to `~/.claude` | cli/cli.go neutralizePinnedSessionProfile | CUT D10 | V | R |
| CLI-8 | observe from a pinned profile booked to the default login | cli/observe.go | FIX D6 | T | E |
| CLI-9 | `run` without an account in a pinned shell launches the pinned one | cli/run.go | CUT D10 |  | R |
| CLI-10 | `config set autoswitch.model ","` accepted, disables the session policy | settings/settings.go ParseSettingValue | FIX | V | E |
| CLI-11 | TUI add-token "slot occupied" matches a Codex number | tui/app.go slotOccupant | FIX | V | R |
| CLI-12 | menu prefills "(none)", Enter fails | cli/menu.go, interactive.go | FIX |  | R |

## Web lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| WEB-1 | Add token with an occupied slot deletes that account unasked | web/handlers.go handleAddToken | FIX D9 | TV | U |
| WEB-2 | page text about counted models is wrong for either engine | web/static/index.html, app.js | FIX D9 |  | U |
| WEB-3 | six open tabs stall every request | web/static/app.js EventSource | FIX D9 | T | U |
| WEB-4 | Stop refused after the host slept (start rebuilt from boot time) | web/kill_linux.go, kill_unix.go | FIX D9 | TL | U |
| WEB-5 | launch token in the journal | cli/appcmd.go | DUP HOST-6 |  |  |
| WEB-6 | Start/Stop button left clickable in the wrong state | web/static/app.js run() | FIX D9 | TV | E |
| WEB-7 | "Switch (best) done." when nothing switched | web/static/app.js | FIX D9 | T | U |
| WEB-8 | model list saved elsewhere shown as the running engine's | web/static/app.js engineSetting | FIX D3 | T | E |
| WEB-9 | add-token alias lands on an older account with the same email | web/handlers.go findNumberByEmail | FIX D9 | T | U |
| WEB-10 | lost session tells the user to run `tycswap web` | web/static/app.js, handlers.go | FIX D9 |  | U |
| WEB-11 | Event log empty and misleading under an external scheduler | web/static/index.html | FIX D7 |  | E |
| WEB-12 | focus in the filter box freezes the session list | web/static/app.js editingInside | FIX |  | U |
| WEB-13 | action response waits for a state rebuild that fetches usage | web/handlers.go runMutation | FIX | T | U |
| WEB-14 | a burst of engine events is shown twice | web/static/app.js appendAutoEvent | FIX D7 | T | E |
| WEB-15 | state observers can get an older state after a newer one | web/web.go broadcast | FIX | T | U |
| WEB-16 | token stays in the page after the dialog closes | web/static/app.js closeModal | FIX D9 | TV | U |
| WEB-17 | stale session file listed as a running session | web/web.go probeSessions | DUP SES-6 | P |  |

## Tray lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| TRAY-1 | Linux/macOS menu click resolved by row position | tray/sni_linux.go, tray_darwin.go | FIX D9 | T | U |
| TRAY-2 | Windows keeps one pending notification | tray/tray_windows.go Notify | FIX D9 | V | U |
| TRAY-3 | dry-run engine notifies "Auto-switched"; titles lack the slot | cli/webfacades.go onEvent, appshell.go | FIX D5 D9 |  | E |
| TRAY-4 | start-at-login status is "an entry with this label exists" | autostart/autostart.go Enabled | FIX D9 | T | U |
| TRAY-6 | Linux registers with the watcher once | tray/sni_linux.go | FIX D9 |  | U |
| TRAY-7 | text menus drop a switch row's second line | tray/tray.go FallbackTitle | FIX D9 | TV | U |
| TRAY-9 | macOS: turning start at login off boots out the caller | autostart/launchagent.go | FIX | P | U |
| TRAY-11 | Windows: a refused first NIM_ADD ends the app | tray/tray_windows.go | FIX | P | U |
| TRAY-12 | Linux: underscore in a label is swallowed | tray/sni_linux.go | FIX | P | U |

## Switching lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| SW-1 | live login refreshed outside every lock; rotation discarded | reporting/fetch.go fetchActiveUsage | FIX D8 | TVL | C |
| SW-2 | unattributed live credential written into the config's slot | switching/perform.go unresolved | FIX D8 | TVL | C |
| SW-3 | dead mark outlives the harvest that stores a live credential | store/refresh.go, usage/store.go | FIX D8 | T | C |
| SW-4 | abandoned guarded refresh retried with the same token | store/refresh.go, oauth/fetch.go | FIX D8 | PL | C |
| SW-5 | slot write failing after the grant drops the rotation | store/refresh.go | FIX D8 | T | C |
| SW-6 | four writers rename without retry on Windows | ccfile, credstore/backup.go, switching/configio.go, ccsettings | FIX D8 |  | C |
| SW-7 | interrupted switch never repaired; only Ctrl-C releases locks | switching/perform.go, cli/sigint.go | FIX D5 D8 | VL | C |
| SW-8 | switch without an account block overwrites the live credential unstashed | switching/perform.go | FIX D8 | T | C |
| SW-9 | macOS: failed Keychain delete ignored after the file fallback | credstore/active.go | FIX | P | C |

## Store lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| STORE-1 | `add` files the live credential under the account the config names | lifecycle/add.go, addsource.go | FIX D8 | TVL | C |
| STORE-2 | `move`/`swap` deletes a stopped profile's newest token | lifecycle/moveswap.go, store/credproxy.go | CUT D10 | TV | R |
| STORE-3 | `add` reads identity before the lock, material after | lifecycle/add.go | FIX D8 | T | C |
| STORE-4 | `export` writes the live credential into the config's entry | transfer/export.go | FIX D8 |  | C |
| STORE-5 | `disable` does not hold the active account | autoswitch/tick.go | BY-DESIGN (upstream; need met by D1 D2) | V |  |
| STORE-6 | symlink above the store root fails every ownership check | groups/registry.go SafePath | FIX | T | R |
| STORE-7 | same-email swap strands a profile claim | lifecycle/moveswap.go | CUT D10 | T | R |
| STORE-8 | `add` refreshes an API-key slot in place with an OAuth login | lifecycle/add.go | FIX | T | C |
| STORE-9 | `add-token` placeholder email collision deletes an account | lifecycle/addtoken.go | FIX | T | C |
| STORE-10 | re-adding in place drops `disabled` | lifecycle/add.go, addtoken.go, transfer/import.go | FIX | T | C |
| STORE-11 | `migrate` refuses once tycswap's own state files exist | storemigrate/storemigrate.go | FIX | T | R |
| STORE-12 | active pointer left on a removed slot | lifecycle/remove.go, add.go | FIX D8 | T | C |
| STORE-13 | macOS purge reports Keychain items removed unchecked | lifecycle/purge.go | FIX |  | R |
| STORE-14 | scratch login directories never reclaimed | cli/logincleanup.go | DUP CLI-3 | L |  |
| STORE-15 | snapshot `RotationEligible` omits ownership | reporting/snapshot.go | CUT D10 |  | R |
| STORE-16 | `RecordPlan` matches by email only | store/plan.go | FIX |  | R |
| STORE-17 | macOS pinned config dir: two readers, two Keychain items | credstore/active.go, store/defaulthandoff.go | FIX | P | R |
| STORE-18 | `add-token` secret echoed under mintty without a warning | lifecycle/lifecycle.go Secret | FIX | P | R |
| STORE-19 | changed email case gets a second slot | store/identity.go FindAccountSlot | FIX | P | R |

## Session lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| SES-1 | failed probe reseeds a profile from the stale backup, then deletes it | session/bootstrap.go | CUT D10 | T | R |
| SES-2 | every slot backup write deletes the profile's credential | store/credproxy.go postBackupWrite | CUT D10 | TV | R |
| SES-3 | one unparsable session file vetoes every ownership check | store/ownership.go profilePIDs | FIX | T | R |
| SES-4 | `env N` on the active account pins nothing, exits 0 | session/env.go | CUT D10 | V | R |
| SES-5 | pinned session's status line handled as default login | cli/observe.go | DUP CLI-8 | T |  |
| SES-6 | stale session file counts as running once its PID is reused | procdetect/procdetect.go, store/ownership.go | FIX | TV | R |
| SES-7 | an account once run in a profile is refused as a target for good | store/ownership.go | CUT D10 | T | R |
| SES-8 | symlink above the store root | groups/registry.go | DUP STORE-6 | T |  |
| SES-9 | `--share-history` deletes same-named profile memory files | session/history.go | CUT D10 | T | R |
| SES-10 | at-limit marker kept after the reset time passed | reporting/atlimit.go, render.go | FIX D4 | T | E |
| SES-11 | unknown 5h utilization printed as 0 % | reporting/render.go | FIX | T | R |
| SES-12 | at-limit marker ignores the session-model policy | reporting/atlimit.go | FIX D9 |  | E |
| SES-13 | model window name printed without stripping control characters | reporting/render.go, atlimit.go | FIX | T | R |
| SES-14 | `CLAUDE_CONFIG_DIR` spelled uncleaned reads as another profile | reporting/fetch.go, paths | FIX |  | R |
| SES-15 | ccsettings rewrites settings.json without a lock | ccsettings/ccsettings.go | FIX | P | R |
| SES-16 | API key written into settings.json with its existing mode | ccsettings/ccsettings.go | FIX | P | R |

## Provisioner and live lane

| ID | Defect | Where | Verdict | Ev | Series |
| --- | --- | --- | --- | --- | --- |
| FL-1 | manual switch undone; no off switch | see ENG-3, HOST-1 | DUP | L |  |
| FL-2 | ping-pong across windows | see ENG-2 | DUP | L |  |
| FL-3 | observe drives 429s | see ENG-1 | DUP | L |  |
| FL-4 | hook and observe switches leave no record of trigger or origin | flakelab claude.nix; tycswap log line | FIX D7 | VL | E |
| FL-5 | ticks count model limits every screen shows as not counted | see SES-12 | DUP | L |  |
| FL-6 | quota hook fires on resume, not on the limit | flakelab claude.nix | PROVISIONER D6 | V |  |
| FL-7 | launch token in the journal | see HOST-6 | DUP | L |  |
| FL-8 | `flakelab web` keeps the previous binary after a pin bump | flakelab scripts.nix, web.nix | PROVISIONER | L |  |
| FL-9 | "2min" timer ticks every 3 minutes | flakelab tycswap.nix | PROVISIONER D5 (timer deleted) | V |  |
| FL-10 | doctor reports a stopped timer as scheduled | flakelab nix-doctor | PROVISIONER D5 | V |  |
