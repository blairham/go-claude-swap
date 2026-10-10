# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and the project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-stable releases (`v0.x.y`) make no API-stability promise -- breaking changes can land in any `v0.x` bump.

Entries before 0.5.0 were reconstructed from the commit history when this
file was added.

## [Unreleased]

## [0.5.0] - 2026-10-10

### Changed

- **License: go-claude-swap is now Apache-2.0** (it was MIT). Releases up
  to and including 0.4.0 remain available under MIT. `NOTICE` records that
  cswap is a port of [realiti4/claude-swap](https://github.com/realiti4/claude-swap)
  and carries that project's MIT copyright and permission notice; every Go
  file carries an SPDX header.
- Releases are built, signed and attested by the shared workflows in
  [blairham/.github](https://github.com/blairham/.github) (`go-release.yml`):
  `checksums.txt` is signed with keyless cosign and every archive carries
  SLSA build provenance. `SECURITY.md` shows how to verify them. Release
  notes are this file's section for the tag. The Homebrew formula and its
  `brew services` block are unchanged.
- CI is the shared `go-ci.yml` (Ubuntu and macOS), with CodeQL, OpenSSF
  Scorecard and Dependabot alongside.

### Fixed

- `cswap config set` refuses `NaN` for a numeric setting (it passed both
  bound checks) and a string that is not UTF-8 (settings.json would have
  stored U+FFFD instead of what was typed).
- A `null` account in a hand-edited `sequence.json` no longer crashes cswap,
  and an account under a key that is not a slot number is no longer
  resolved to slot 0.
- `cswap map` refuses a directory whose path is not UTF-8, which
  `mappings.json` would have stored under a key no directory matches.

### Security

- Built with Go 1.26.9.
- Fuzz targets for the files and responses cswap parses: settings.json and
  `config set` values, sequence.json, mappings.json and the usage endpoint's
  response.
- `osv-scanner.toml` records why GO-2026-5932 (x/crypto/openpgp) does not
  apply: no openpgp package is in cswap's build or tool graph.

## [0.4.0] - 2026-10-09

### Added

- `cswap run` session mode, ported from claude-swap: run Claude Code as one
  account in one terminal, in its own profile, without touching the default
  login (#37). It mirrors user-scope MCP servers into the profile and takes
  `--share-history` (#42), checks for PID reuse, refuses to nest inside
  another session's shell, and `cswap list --token-status` reports OAuth
  token state for the login, the profile and the backup (#44).
- `cswap map` / `unmap`: directory → account mappings, shared with
  claude-swap through `mappings.json` (#28).
- `cswap add-token`: register an account from a `claude setup-token` token
  or an API key (#32).
- `cswap history`: every switch is recorded in `switch-history.jsonl` (#30).
- Desktop notifications from `cswap auto` on at-limit, failover, exhaustion
  and recovery (#35).
- The `balance` auto-switch strategy, which paces weekly usage across
  accounts (#38).

### Changed

- Auto-switch switches to the least-bad account instead of riding to 100%
  (#26), switches ahead of a fast burn (#34), sleeps until the known
  recovery when every account is exhausted (#31), keeps its poll interval on
  a blocked tick with headroom (#41), and says why usage is unknown (#33).
- The service log is dated, change-only and size-bounded (#29).
- `cswap service` sees a Homebrew-managed service, not only its own (#27).
- A roster change wakes a running auto loop (#43).

## [0.3.0] - 2026-10-03

### Added

- Remove an account from the TUI's switch screen (#11).

### Changed

- CI is one workflow with pre-commit first and a single required check (#9).

## [0.2.0] - 2026-08-17

### Changed

- Auto-switch watches the active model's weekly limit by default
  (`autoswitch.model auto`) (#7), and matches model windows by selector
  containment rather than a family table (#8).
- Built with Go 1.26.6.

## [0.1.1] - 2026-08-13

### Changed

- Homebrew ships a formula instead of a cask, restoring the `brew services`
  block (#3).
- macOS binaries can be signed and notarized (#4).

## [0.1.0] - 2026-08-13

### Added

- First release: the Go rewrite of claude-swap — capture, switch, list,
  auto-switch, roster management, export/import, the TUI, and
  `cswap service` with a gRPC control API the TUI attaches to.

[Unreleased]: https://github.com/blairham/go-claude-swap/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/blairham/go-claude-swap/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/blairham/go-claude-swap/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/blairham/go-claude-swap/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/blairham/go-claude-swap/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/blairham/go-claude-swap/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/blairham/go-claude-swap/releases/tag/v0.1.0
