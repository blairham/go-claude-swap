# CLAUDE.md

@AGENTS.md

<!--
AGENTS.md (imported above) is the cross-tool single source of truth for working in
this repo — project overview, build/test commands, structure, conventions, and the
switching invariants. Claude Code does not read AGENTS.md natively, so this file
imports it and holds only Claude Code-specific extras. Put repo guidance in
AGENTS.md, not here.
-->

## Claude Code-specific notes

- **Subagents** (`.claude/agents/`) — delegate proactively:
  - `switch-flow-auditor` — read-only; before changing `switcher`, `credentials`, `locks`, or `claudecfg`, traces the lock ordering, rollback path, and credential-store contract a change would have to preserve.
  - `go-checks` — runs `make check` + `make lint` and triages failures across gofumpt, golangci-lint v2, and `go test -race`.
- **Slash commands** (`.claude/commands/`):
  - `/check` — run the pre-PR gate (`make check` + `make lint`) and report failures. Pass a target (e.g. `test`) to scope it.
  - `/go-version-sync` — verify/fix the `go.mod` ↔ `.tool-versions` invariant via `make sync`. Pass `check` to report only.
- **Skills** (`.claude/skills/`):
  - `ship` — run the gate, commit, push, and open a PR against `main` (`/ship`).
- The permission allowlist is in `.claude/settings.json`.
- Commits and PRs carry no AI-attribution trailers (see AGENTS.md → Code Conventions).
