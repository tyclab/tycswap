# AGENTS.md — tycswap

Go CLI for Claude and Codex account slots. Entry: `cmd/tycswap`; commands:
`internal/cli`; providers and credential stores: `internal/core`, `internal/store`,
and `internal/codex`; session groups: `internal/groups`, `internal/session`;
recovery: `internal/recovery`; interfaces: `internal/web`, `internal/tray`, and dashboard.
The Windows overview panel lives in `internal/tray/panel*` and
`internal/cli/traypanel.go`; shared web settings mutations live in
`internal/web/settingsmutation.go`. The contracts live in `docs/reference.md`
and `docs/DESIGN.md`.

Use the Go version required by `go.mod`. Validate with `make fmt`, `go test ./...`,
`go vet ./...` and, as CI does, `GOOS=windows go vet ./...` and
`GOOS=darwin CGO_ENABLED=0 go vet ./...`; use `make build` for a versioned local binary.
`testdata/**` is byte-exact golden fixtures (`-text` in `.gitattributes`): never let
line endings or formatting tools rewrite them. Preserve file-lock,
atomic-write, credential ownership, JSON, exit-code, and migration contracts.
Keep `//go:build`, `//go:embed`, cgo preambles, and licensing intact. Tests must use
temporary stores and fake providers rather than the operator's active logins.

## Maintenance policy

- README files and agent instruction files are exempt from the comment limit
  by design. The limit applies to code and configuration files.
- Outside the limit (operator, 2026-10-08): vendored third-party code and
  third-party build output (kept byte-identical to upstream), Hugo site
  functional files, translation files, example/template configs whose comments
  document the schema, files under ten code lines, and approved runtime text
  such as MCP tool docstrings. Output of our own generators is inside it: fix
  the generator.
- Keep code-file comments at or below 20% of nonblank lines. Only the operator
  may grant a documented, file-specific exception; do not silently exempt files.
- Preserve licenses, tool directives, runtime strings, and behavior. If the limit
  conflicts with these, report the file for an operator decision.
- Keep this file as the repository's single agent instruction source. Keep the
  README and its process/logic diagram consistent with the implementation.
- Remove unused files or features only with evidence that nothing depends on them.
  Keep changes focused; do not add speculative features or abstractions.
