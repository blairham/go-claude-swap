# AGENTS.md — go-claude-swap

Guidance for AI coding agents (Claude Code, Cursor, Copilot, Codex, OpenCode, …) working in this repository. This is the **cross-tool single source of truth** — `CLAUDE.md` imports it.

## Project Overview

**go-claude-swap** is a Go rewrite of [claude-swap](https://github.com/realiti4/claude-swap): a multi-account manager for Claude Code, shipped as a single static binary named `cswap`. It captures the current Claude Code login as a named account, switches the live credential between accounts without logging out, tracks per-account rate-limit usage, and can rotate automatically as the active account approaches its limits — optionally as an always-on login service with a TUI dashboard attached over gRPC.

The on-disk layout, lock protocols, and switching semantics deliberately mirror the original Python claude-swap so both tools can coexist on one machine. When changing storage or lock behavior, compatibility with the original is a constraint, not a preference.

## Quick Reference

```bash
make build      # Build binary to build/cswap (ldflags stamp Version/commit/date)
make install    # go install ./cmd/cswap
make test       # Run tests with race detector: go test -v -race ./...
make test-cover # Tests + coverage.html
make fmt        # Format: go tool gofumpt -w .
make vet        # go vet ./...
make lint       # go tool golangci-lint run ./...
make check      # fmt + vet + test  (note: does NOT run lint — run `make lint` separately before a PR)
make tidy       # go mod tidy
make sync       # Rewrite .tool-versions' golang pin from go.mod
make check-versions  # Assert go.mod ↔ .tool-versions agree (runs the pre-commit hook)
make proto      # Regenerate pkg/swapapi from swapapi.proto (installs protoc-gen-go, needs protoc)
```

## Project Structure

```
cmd/cswap/main.go        # Entry point: hashicorp/cli dispatch → internal/cmd.CommandFactory()
internal/
  account/               # sequence.json roster — slots, aliases, which account is active
  autoswitch/            # The `cswap auto` loop: usage polling, rotation decisions, and the
                         # cooldown/hysteresis/quarantine state in autoswitch_state.json
  claudecfg/             # Reads/edits ~/.claude.json. ONLY oauthAccount is account-specific;
                         # every other key is machine state and must survive a switch
  cmd/                   # One struct per CLI command + shared helpers (flag parsing, JSON envelopes)
  credentials/           # Live Claude Code credential (Keychain on macOS, file elsewhere) and
                         # cswap's per-account backups (Keychain-primary, base64 .enc fallback)
  keychain/              # macOS `security` CLI wrapper for generic-password items
  locks/                 # cswap's flock file lock + Claude Code's proper-lockfile directory locks
  oauth/                 # Token refresh (oauth.go) and the PKCE authorization-code login (authorize.go)
  paths/                 # Claude config locations and cswap's backup root, per OS
  service/               # launchd LaunchAgent (macOS) / systemd user unit (Linux) for the auto loop
  settings/              # settings.json — typed, bounded keys; forgiving load, strict `config set`
  switcher/              # Account lifecycle: capture, switch the live credential, roster maintenance
  tui/                   # Bubble Tea dashboard, switch picker, live watch view
  usage/                 # Fetch, normalize, cache usage windows; adaptive poll scheduling
pkg/swapapi/             # gRPC control API served by a running `cswap auto` over a unix socket
```

## How switching works

The invariants below are the reason most of this code is shaped the way it is. Read this before touching `switcher`, `credentials`, `locks`, or `claudecfg`.

- **Lock ordering**: a switch takes cswap's own file lock → Claude Code's credential locks → the config lock, always in that order. Only local I/O happens while locks are held, and completed steps roll back in reverse on failure. Holding Claude Code's own locks is what keeps a concurrent token refresh from colliding.
- **Absent vs unreadable**: an unreadable credential store must never be treated as empty. That distinction gates backup overwrites and re-add advice — collapsing it silently destroys credentials.
- **Auth axes are exclusive**: OAuth and managed API key cannot both be live; writing one clears the other.
- **Config splicing**: only `oauthAccount` in `~/.claude.json` is per-account. Projects, MCP servers, `mcpOAuth`, `pluginSecrets`, and the rest are machine-shared and follow the live machine, not the slot.
- **Nothing is discarded**: displaced credentials are stashed and surfaced by `cswap unclaimed`.
- **Keychain access**: `internal/keychain` pins `/usr/bin/security` rather than resolving it from PATH, so the Keychain ACL entry survives interpreter changes. Every spawn has a 5s timeout.
- **Usage request budget**: the usage endpoint budgets roughly 28–30 requests per trailing hour per identity. `internal/usage` exists to schedule within that (adaptive interval, hard backoff after a 429, reset-aligned wakeups). Don't add ad-hoc fetches — go through the scheduler and the cache.
- **One poller**: when the auto-switch service is running, it owns the request budget. The TUI detects it over the unix socket and goes store-only, reading the shared cache instead of polling.

## Code Conventions

- **Go version**: `go.mod`'s `go` directive and `.tool-versions`' `golang` pin must match **exactly** — enforced by the `check-go-version-sync` pre-commit hook from [blairham/pre-commit-hooks](https://github.com/blairham/pre-commit-hooks) (pinned by `rev` in `.pre-commit-config.yaml` — there is no local copy). `go.mod` is authoritative (its directive gets pulled up by the `tool` block during `go mod tidy`); run `make sync` to bring `.tool-versions` back in line. CI pins the minor line (`GO_VERSION: "1.26"`), which setup-go resolves to the latest patch — update it only when the minor version changes
- **Formatter**: gofumpt via `go tool` (pinned in go.mod's `tool` block alongside golangci-lint). Formatting is applied at commit time by the `golangci-lint-fmt` hook, driven by `.golangci.yml` — keep the hook `rev` in lockstep with the `tool` pin and the CI action version
- **Linter**: golangci-lint v2, config in `.golangci.yml`
- **Imports**: grouped by goimports with local prefix `github.com/blairham/go-claude-swap` — local imports get their own trailing group
- **Commands**: implement `hashicorp/cli.Command` (`Run(args []string) int`, `Help()`, `Synopsis()`), registered in `cmd.CommandFactory`. Aliases (`ls`, `rm`, `watch`, `relogin`) reuse the same struct rather than duplicating it
- **Flags**: `jessevdk/go-flags` struct tags, parsed through the shared `parseFlags` helper (handles `-h` and error printing)
- **Exit codes**: errors go to stderr via `ui.Error`; return `1` for failure, `0` for success
- **JSON output**: every `--json` path goes through `printJSON`/`jsonError` so the envelope carries `schemaVersion`
- **Commits/PRs**: no AI-attribution trailers — do not add `Co-Authored-By: Claude`, "Generated with Claude Code", or similar to commit messages or PR bodies

## CI/CD

- **ci.yml**: `test` (ubuntu + macos matrix, `make test`), `lint` (golangci-lint-action), then `build` — on push to main and PRs
- **goreleaser.yml**: GoReleaser on version tags (`v*`) or manual dispatch
- **Pre-commit hooks** (`pre-commit install`): trailing-whitespace, end-of-file-fixer, check-yaml, check-added-large-files, check-merge-conflict, detect-private-key, go-mod-tidy-repo, go-fumpt-repo, `check-go-version-sync` (blairham/pre-commit-hooks), `golangci-lint-fmt` + `golangci-lint`, gitleaks. `golangci-lint-fmt` applies every formatter in `.golangci.yml` (gofumpt + goimports); `golangci-lint` lints only what changed since HEAD. The whole-repo run stays in CI — `--new-from-rev` can't see whole-module linters like `unused`

## Key Dependencies

- `hashicorp/cli` — command dispatch
- `jessevdk/go-flags` — per-command flag parsing
- `charmbracelet/bubbletea` + `lipgloss` — TUI
- `google.golang.org/grpc` + `protobuf` — the service ⟷ TUI control API
- `golang.org/x/sys` — platform syscalls (flock)

## Testing

- `go test -race ./...`; unit tests cover `account`, `autoswitch`, `credentials`, `oauth`, `paths`, `service`, `settings`, `switcher`, `usage`, and `pkg/swapapi`
- The TUI and the live Keychain/credential paths are not unit-tested — they need an interactive terminal and a real login
- Tests must never touch the real backup root, `~/.claude.json`, or the Keychain. The established pattern is `t.TempDir()` plus `t.Setenv` on `HOME` / `XDG_DATA_HOME` / `CLAUDE_CONFIG_DIR`, with every path resolved through `internal/paths` so the redirect takes effect
