# go-claude-swap

A Go rewrite of [claude-swap](https://github.com/realiti4/claude-swap) — a
multi-account manager for [Claude Code](https://claude.com/claude-code).
Switch between Claude accounts without logging out, watch usage across all of
them, and rotate automatically when the active account approaches its rate
limits.

Single static binary (`cswap`), no Python runtime required.

## Install

```sh
brew install blairham/tap/cswap
```

Or with Go:

```sh
go install github.com/blairham/go-claude-swap/cmd/cswap@latest
```

Or from a clone:

```sh
make build   # → build/cswap
make install
```

## Quick start

```sh
# While logged in to Claude Code as account A:
cswap add

# Log in as account B (claude /login), then:
cswap add

# See everyone's usage:
cswap list

# Switch:
cswap switch 2        # by slot
cswap switch work     # by alias
cswap switch          # rotate to the next account

# Live dashboard:
cswap tui

# Hands-off rotation at 90% utilization:
cswap auto

# ...or run it always-on (starts at login, restarts on crash):
brew services start cswap     # Homebrew installs
cswap service install         # any install: launchd (macOS) / systemd --user (Linux)
```

## Commands

| Command | Description |
|---|---|
| `cswap add [--slot N] [--alias NAME]` | Back up the current Claude Code login as a managed account |
| `cswap login [N\|alias\|email]` / `relogin` | Re-authenticate an account (or add one) via the OAuth flow, without touching the live session |
| `cswap list` / `ls` | All accounts with 5h/7d/per-model usage and reset times |
| `cswap status` | Current account |
| `cswap switch [N\|alias\|email] [--force]` | Switch accounts (bare = rotate) |
| `cswap auto [--once] [--dry-run] [--json]` | Auto-switch when the binding window hits the threshold |
| `cswap service install\|uninstall\|status` | Run `cswap auto` as an always-on login service |
| `cswap tui` / `cswap watch` | Interactive dashboard / live watch view |
| `cswap remove` / `disable` / `enable` / `alias` / `move` | Roster management |
| `cswap export` / `import` | Back up or migrate accounts between machines |
| `cswap config [list\|get\|set\|unset\|path]` | Settings (threshold, strategy, cooldown, theme, …) |
| `cswap unclaimed` | Credentials preserved from displaced logins |

Most read commands take `--json` for scripting; `cswap auto --json` emits
JSONL events.

## How switching works

- **Credentials**: on macOS the Keychain item Claude Code itself uses
  (`Claude Code-credentials`) is swapped; on Linux/WSL it's
  `~/.claude/.credentials.json`. Per-account backups live in the Keychain
  (macOS) or as files under the backup root.
- **Config**: only the `oauthAccount` object inside `~/.claude.json` is
  spliced per account — projects, MCP servers, and all other machine state
  are preserved.
- **MCP logins survive**: machine-shared credential fields (`mcpOAuth`,
  `pluginSecrets`, …) always follow the live machine state, not the slot.
- **Safety**: switches hold Claude Code's own credential locks (the
  proper-lockfile protocol) so a concurrent token refresh can't collide; all
  writes are atomic; a failed switch rolls back; displaced credentials are
  stashed (`cswap unclaimed`), never discarded.
- **Auto-switch**: `cswap auto` polls usage adaptively (respecting the
  endpoint's request budget with backoff and reset-aligned scheduling) and
  switches proactively — before the limit — so a running Claude Code picks up
  the new credential while the old one still works.
- **Service ⟷ TUI over gRPC**: a looping `cswap auto` serves a control API
  (`pkg/swapapi`) on a unix socket in the backup root. The TUI connects to
  it for status, streams switch events live, and goes store-only while the
  service is running — one process owns the usage-request budget, and the
  dashboard reads its results from the shared cache.

Data lives in `~/.claude-swap-backup` (macOS) or
`${XDG_DATA_HOME:-~/.local/share}/claude-swap` (Linux/WSL) — the same layout
as the original claude-swap, so both tools can coexist.

## Settings

```sh
cswap config set autoswitch.threshold 85
cswap config set autoswitch.strategy consume-first
cswap config set autoswitch.model Fable        # also watch per-model weekly limits
cswap config set ui.theme light
```

## Not (yet) ported

Session mode (`cswap run`), directory mappings, setup-token accounts
(`add-token`), the macOS menubar extra, and the deepest edge-case machinery
of the original (consume-gate CAS persistence, provenance oracle probing).

## Development

```sh
make check    # fmt + vet + test
make lint     # golangci-lint
pre-commit install
```

## Credits & license

MIT. A from-scratch Go rewrite of
[realiti4/claude-swap](https://github.com/realiti4/claude-swap) (MIT) — the
storage layout, lock protocols, and switching semantics follow the original
so the two stay compatible on disk.
