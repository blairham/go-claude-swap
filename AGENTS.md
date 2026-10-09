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
make check      # fmt + vet + test  (no lint target: golangci-lint runs as a pre-commit hook and in CI)
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
                         # every other key is machine state and must survive a switch.
                         # Also reads ~/.claude/settings.json (read-only) to detect the
                         # model Claude Code is using, for autoswitch.model=auto
  cmd/                   # One struct per CLI command + shared helpers (flag parsing, JSON envelopes)
  credentials/           # Live Claude Code credential (Keychain on macOS, file elsewhere) and
                         # cswap's per-account backups (Keychain-primary, base64 .enc fallback)
  history/               # switch-history.jsonl — append-only, size-bounded record of every switch
  keychain/              # macOS `security` CLI wrapper for generic-password items
  locks/                 # cswap's flock file lock + Claude Code's proper-lockfile directory locks
  logfile/               # Size-rotated log file `cswap auto --log-file` writes (and dups onto stdout/stderr)
  mappings/              # mappings.json: directory → account identity, for `cswap run` with no account
  notify/                # Desktop notifications: pinned /usr/bin/osascript (macOS), notify-send (Linux), 5s timeout
  oauth/                 # Token refresh (oauth.go) and the PKCE authorization-code login (authorize.go)
  paths/                 # Claude config locations and cswap's backup root, per OS
  session/               # `cswap run` profiles: seed, validate (claude auth status), share, stale/live tracking
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
- **Session profiles hold the newest token**: Claude Code rotates the refresh token inside a `cswap run` profile and nothing syncs it back. `credentials.WriteBackup` is the invalidation chokepoint (a backup write invalidates the slot's profile, or flags it stale while it runs); switch, run, and the collector adopt a quiescent profile's newer credential first (`WriteBackupKeepSession`), and never refresh a live one.
- **Nothing is discarded**: displaced credentials are stashed and surfaced by `cswap unclaimed`.
- **Keychain access**: `internal/keychain` pins `/usr/bin/security` rather than resolving it from PATH, so the Keychain ACL entry survives interpreter changes. Every spawn has a 5s timeout.
- **Usage request budget**: the usage endpoint budgets roughly 28–30 requests per trailing hour per identity. `internal/usage` exists to schedule within that (adaptive interval, hard backoff after a 429, reset-aligned wakeups). Don't add ad-hoc fetches — go through the scheduler and the cache.
- **One poller**: when the auto-switch service is running, it owns the request budget. The TUI detects it over the unix socket and goes store-only, reading the shared cache instead of polling.

## Code Conventions

- **Go version**: `go.mod`'s `go` directive and `.tool-versions`' `golang` pin must match **exactly** — enforced by the `check-go-version-sync` pre-commit hook from [blairham/pre-commit-hooks](https://github.com/blairham/pre-commit-hooks) (pinned by `rev` in `.pre-commit-config.yaml` — there is no local copy). `go.mod` is authoritative (its directive gets pulled up by the `tool` block during `go mod tidy`); run `make sync` to bring `.tool-versions` back in line. CI and the release take the toolchain from `go.mod` (`go-version-file`), and blairham/.github's drift check requires the latest published 1.26.x patch
- **Formatter**: gofumpt via `go tool` (pinned in go.mod's `tool` block alongside golangci-lint). Formatting is applied at commit time by the `golangci-lint-fmt` hook, driven by `.golangci.yml` (gofmt, gofumpt, goimports, gci, golines) — keep the hook `rev` in lockstep with the `tool` pin
- **Linter**: golangci-lint v2, config in `.golangci.yml`
- **Synced files are not edited here.** `.golangci.yml`, `.pre-commit-config.yaml`, `.editorconfig`, `.yamllint.yml`, `.gitleaks.toml`, `.github/dependabot.yml`, `.github/CODEOWNERS`, `.github/workflows/scorecard.yml` and `.github/workflows/codeql.yml` are rendered from [blairham/.github](https://github.com/blairham/.github)'s baseline by its `make sync REPO=go-claude-swap DIR=<checkout>`, and its weekly drift check reports any difference. Change the baseline there; a departure for this repo is an `overrides/go-claude-swap.yml` entry with a reason, approved first
- **License**: Apache-2.0. Every hand-written `.go` file starts with `// SPDX-FileCopyrightText: 2026 Blair Hamilton` and `// SPDX-License-Identifier: Apache-2.0` (the `check-license-headers` hook). cswap is a port of the MIT-licensed claude-swap; `NOTICE` carries its copyright and permission notice and must keep doing so
- **Imports**: grouped by goimports with local prefix `github.com/blairham/go-claude-swap` — local imports get their own trailing group
- **Commands**: implement `hashicorp/cli.Command` (`Run(args []string) int`, `Help()`, `Synopsis()`), registered in `cmd.CommandFactory`. Aliases (`ls`, `rm`, `watch`, `relogin`) reuse the same struct rather than duplicating it
- **Flags**: `jessevdk/go-flags` struct tags, parsed through the shared `parseFlags` helper (handles `-h` and error printing)
- **Exit codes**: errors go to stderr via `ui.Error`; return `1` for failure, `0` for success
- **JSON output**: every `--json` path goes through `printJSON`/`jsonError` so the envelope carries `schemaVersion`
- **Commits/PRs**: no AI-attribution trailers — do not add `Co-Authored-By: Claude`, "Generated with Claude Code", or similar to commit messages or PR bodies

## CI/CD

- **ci.yml** calls blairham/.github's `go-ci.yml` (pinned by the latest tag's commit SHA with a `# vX.Y.Z` comment): `CI / Pre-commit` (the hooks, plus `golangci-lint-new` over the change's diff — never `--all-files`), `CI / Detect changed files`, `CI / Build and test (ubuntu-latest)` and `(macos-latest)`, and `CI / Fuzz` (every `Fuzz*` target, on main and weekly, never on a pull request) — on pull requests and pushes to main
- **release.yml** calls `go-release.yml` on a `v*` tag: tests, GoReleaser (archives + the Homebrew formula with its `service` block, via `HOMEBREW_TAP_TOKEN`), keyless cosign over `checksums.txt`, SLSA build provenance for every archive (also attached as `go-claude-swap-<tag>.intoto.jsonl`). The release notes are the tag's `## [X.Y.Z]` section of `CHANGELOG.md` (no `v`), which must exist. `workflow_dispatch` with `dry-run` builds a snapshot and publishes nothing
- **codeql.yml** and **scorecard.yml** are synced from the baseline; Dependabot covers gomod and GitHub Actions
- **Pre-commit hooks** (`pre-commit install`): trailing-whitespace, end-of-file-fixer, check-yaml, check-added-large-files, detect-private-key, `golangci-lint-fmt` + `golangci-lint` (what changed since HEAD), go-mod-tidy-repo, `check-license-headers`, `check-go-version-sync`, `check-conflict-markers` and `go-vulncheck` (blairham/pre-commit-hooks), misspell over prose, gitleaks, yamllint

## Key Dependencies

- `hashicorp/cli` — command dispatch
- `jessevdk/go-flags` — per-command flag parsing
- `charmbracelet/bubbletea` + `lipgloss` — TUI
- `google.golang.org/grpc` + `protobuf` — the service ⟷ TUI control API
- `golang.org/x/sys` — platform syscalls (flock)
- `golang.org/x/text` — NFC normalization, to derive the Keychain service Claude Code uses for a session profile

## Testing

- `go test -race ./...`; fuzz targets cover the files and responses cswap parses (`settings`: `FuzzParseStrict`, `FuzzLoad`; `account`: `FuzzRoster`; `mappings`: `FuzzCovers`, `FuzzMappingsFile`; `usage`: `FuzzParseUsage`), and a finding is committed under `testdata/fuzz/` as a regression case; unit tests cover `account`, `autoswitch`, `cmd` (history, run, add-token), `credentials`, `history`, `logfile`, `mappings`, `notify`, `oauth`, `paths`, `service`, `session`, `settings`, `switcher`, `usage`, and `pkg/swapapi`
- The TUI and the live Keychain/credential paths are not unit-tested — they need an interactive terminal and a real login
- Tests must never touch the real backup root, `~/.claude.json`, or the Keychain. The established pattern is `t.TempDir()` plus `t.Setenv` on `HOME` / `XDG_DATA_HOME` / `CLAUDE_CONFIG_DIR`, with every path resolved through `internal/paths` so the redirect takes effect
