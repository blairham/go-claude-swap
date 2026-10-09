# go-claude-swap

[![CI](https://github.com/blairham/go-claude-swap/actions/workflows/ci.yml/badge.svg?branch=main&event=push)](https://github.com/blairham/go-claude-swap/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/blairham/go-claude-swap?sort=semver)](https://github.com/blairham/go-claude-swap/releases/latest)
[![CodeQL](https://github.com/blairham/go-claude-swap/actions/workflows/codeql.yml/badge.svg)](https://github.com/blairham/go-claude-swap/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/blairham/go-claude-swap/badge)](https://scorecard.dev/viewer/?uri=github.com/blairham/go-claude-swap)
[![Go version](https://img.shields.io/github/go-mod/go-version/blairham/go-claude-swap)](go.mod)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

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
| `cswap add-token [--slot N] [--email E] [TOKEN\|-]` | Register an account from a `claude setup-token` token or an `sk-ant-api…` key, without a login on this machine |
| `cswap login [N\|alias\|email]` / `relogin` | Re-authenticate an account (or add one) via the OAuth flow, without touching the live session |
| `cswap list` / `ls` `[--token-status]` | All accounts with 5h/7d/per-model usage and reset times; `--token-status` adds OAuth token diagnostics (active login, `cswap run` profile, stored backup) |
| `cswap status` | Current account |
| `cswap switch [N\|alias\|email] [--force]` | Switch accounts (bare = rotate) |
| `cswap auto [--once] [--dry-run] [--json]` | Auto-switch when the binding window hits the threshold |
| `cswap service install\|uninstall\|status` | Run `cswap auto` as an always-on login service |
| `cswap tui` / `cswap watch` | Interactive dashboard / live watch view (switch screen: `d` removes an account, after a y/N confirm) |
| `cswap remove` / `disable` / `enable` / `alias` / `move` | Roster management |
| `cswap export` / `import` | Back up or migrate accounts between machines |
| `cswap config [list\|get\|set\|unset\|path]` | Settings (threshold, strategy, cooldown, theme, …) |
| `cswap unclaimed` | Credentials preserved from displaced logins |
| `cswap run [N\|alias\|email] [--no-share] [--share-history] [--require-session] [-- ARGS]` | Run Claude Code as an account in this terminal only, in its own profile; bare `run` uses the directory's mapping |
| `cswap map [N\|alias\|email [PATH]]` / `unmap [PATH]` | Map a directory (and everything below it) to an account; bare `map` lists mappings. Shared with the Python claude-swap via `mappings.json` |
| `cswap history [--since 7d] [--limit N] [--json]` | Recorded switches — manual and automatic — with trigger, utilization, and reason |

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
- **Model-aware**: the binding window is the worst of 5h, 7d, and the watched
  models' weekly limits. By default (`autoswitch.model auto`) cswap watches
  the model Claude Code is currently using — `$ANTHROPIC_MODEL`, then the
  `model` key in `~/.claude/settings.json` — re-detected on every poll. So
  when you're on Fable and an account's Fable weekly window fills up, the
  rotation lands on the account with the most Fable headroom left, even if
  the exhausted account's overall 7d window still looks healthy.
- **Session mode**: `cswap run N` launches Claude Code with
  `CLAUDE_CONFIG_DIR` pointing at a persistent per-account profile under
  `<backup root>/sessions/`, so one terminal runs as account N while the
  default login is untouched. Profiles are seeded from the account's backup,
  reused while `claude auth status` vouches for them, and kept coherent with
  the backup in both directions: a re-login or refresh invalidates a
  profile (or flags it while it is running), and a token Claude Code rotated
  inside a quiescent profile is adopted back into the backup before cswap
  would otherwise switch to, refresh, or poll with the older generation.
  Customizations from `~/.claude` are linked in and the default login's
  user-scope MCP servers are mirrored into the profile (unless
  `--no-share`); `--share-history` also links `~/.claude`'s conversation
  history, merging the profile's own history into it first.
- **Service ⟷ TUI over gRPC**: a looping `cswap auto` serves a control API
  (`pkg/swapapi`) on a unix socket in the backup root. The TUI connects to
  it for status, streams switch events live, and goes store-only while the
  service is running — one process owns the usage-request budget, and the
  dashboard reads its results from the shared cache.

- **Notifications**: `cswap auto` posts a desktop notification when it
  switches at the limit or fails over, when every account is exhausted
  (once per episode, with the earliest recovery time), and when an account
  is usable again. macOS uses Notification Center via `/usr/bin/osascript`;
  Linux uses `notify-send` when installed; elsewhere it stays silent. Both
  work from the login service. `autoswitch.notify` picks the level:
  `important` (default), `all` (every switch and quarantine too), or `off`.
- **Switch history**: every completed switch (CLI, TUI, or auto) is appended
  to `switch-history.jsonl` in the backup root — time, from, to, trigger,
  the outgoing account's utilization, and reason — trimmed to the newest
  1000 entries once it passes 512 KiB. `cswap history` reads it.

Data lives in `~/.claude-swap-backup` (macOS) or
`${XDG_DATA_HOME:-~/.local/share}/claude-swap` (Linux/WSL) — the same layout
as the original claude-swap, so both tools can coexist.

## Settings

```sh
cswap config set autoswitch.threshold 85
cswap config set autoswitch.strategy consume-first
cswap config set autoswitch.model auto         # follow Claude Code's model (default)
cswap config set autoswitch.model Fable,Opus   # or pin the watched weekly limits
cswap config set autoswitch.model none         # 5h/7d windows only
cswap config set autoswitch.notify all         # important (default) | all | off
cswap config set ui.theme light
```

`autoswitch.strategy` picks the target when a switch is due:

- `best` (default) — the account with the most headroom, once the active
  account reaches the threshold.
- `consume-first` — rotate early onto accounts whose weekly window resets
  sooner, so their remaining budget is not wasted.
- `balance` — pace the weekly budget across the roster. Each account's
  *pace* is its weekly headroom per hour left until that weekly window
  resets; rotation goes to the highest pace, and even below the threshold
  the engine moves once a healthy account's pace is 1.5× the active
  account's. Accounts are drawn down together rather than each drained to
  the threshold in turn, so late in the week something still qualifies.

## Not (yet) ported

The macOS menubar extra, and the deepest edge-case machinery of the original
(consume-gate CAS persistence, provenance oracle probing). Because the
consume gate is not ported, `cswap run` freshens a backup before seeding a
profile through the switch path's refresh-near-expiry rather than the
original's gated one-refresh-per-bootstrap (#36).

## Development

```sh
pre-commit install
make test     # go test -race ./...
```

Formatting and golangci-lint run as pre-commit hooks; CI runs the shared
workflows from [blairham/.github](https://github.com/blairham/.github). See
[CONTRIBUTING.md](CONTRIBUTING.md). Report security issues privately, as
[SECURITY.md](SECURITY.md) describes; it also shows how to verify a
release's signature and provenance.

## Credits & license

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE). Releases up to
and including v0.4.0 were published under MIT.

A Go rewrite of [realiti4/claude-swap](https://github.com/realiti4/claude-swap)
(MIT, Copyright (c) 2026 Onur Cetinkol) — the storage layout, lock
protocols, and switching semantics follow the original so the two stay
compatible on disk. `NOTICE` carries claude-swap's copyright and permission
notice.
