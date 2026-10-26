# AGENTS.md — tycswap

Go CLI for agent account slots. docs/reference.md and docs/DESIGN.md own the
command, credential, session, recovery and UI contracts.

- Preserve file locks, atomic writes, credential ownership, JSON/exit-code and
  migration contracts. Tests use temporary stores and fake providers, never the
  operator's active logins. Never log credentials or inline session tokens.
- Never use /logout to switch accounts. Use tycswap switch; a new machine gets
  its own login. Read [login ownership](docs/agents/login-ownership.md) before
  export/import, account sharing, refresh or multi-machine recovery work.
- testdata/** contains byte-exact golden fixtures (-text in .gitattributes):
  formatters and line-ending conversion must not rewrite them. Preserve
  go:build/go:embed directives, cgo preambles and licensing.
- Follow go.mod's Go version. Validate with `make fmt`, `go test ./...`,
  `go vet ./...`, `GOOS=windows go vet ./...`, and
  `GOOS=darwin CGO_ENABLED=0 go vet ./...`. `make build` creates a versioned binary.
- Inspect incoming branches and preserve concurrent work. Respect repository
  hooks and merge defaults. Keep private addresses, personal/employer details and
  credentials out of this public repo; use the repo-local identity or noreply email.

Keep code/config comments at or below 20% of nonblank lines; preserve licenses,
tool directives and runtime descriptions. Only the operator may grant a documented,
file-specific exception. See the shared [maintenance policy](https://github.com/tyclab/flakelab/blob/main/docs/agents/maintenance.md)
for exclusions and counting rules. Remove code only with evidence that behavior and
callers are preserved; keep README commands and diagrams aligned with code.

Keep repository-wide instructions here; put task-specific guidance in `docs/agents/`
and link it from this file. README owns human setup and architecture; plans hold open work only.
