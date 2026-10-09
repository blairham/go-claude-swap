# Contributing

Thank you for considering a contribution. Please read the two rules below
before opening a pull request -- both are non-negotiable.

## 1. Credentials stay where they belong, and are never discarded

cswap holds a credential for every Claude account it manages. A change that
writes a token to a log line, `switch-history.jsonl`, the usage cache, a
`--json` output or a file readable by another user will not be merged.
Neither will one that lets a switch, refresh or import destroy a credential
instead of stashing it (`cswap unclaimed`), or that treats an unreadable
credential store as an empty one. The on-disk layout and lock protocols are
shared with the Python claude-swap, so a change to them needs a design
discussion in an issue first. See [`SECURITY.md`](SECURITY.md) for the
trust model and [`AGENTS.md`](AGENTS.md) for the switching invariants.

## 2. The Contributor License Agreement

Contributions require a signed CLA; the text is in [`CLA.md`](CLA.md).

**Why.** The project may need to offer different licensing terms in future.
That is only possible if one party can license the whole work, and copyright
in a contribution stays with its author unless licensed onward.

The CLA does **not** take your copyright. You keep it; you grant a license
broad enough to include sublicensing, and you affirm the work is your own --
including that no employer holds rights to it.

## Practical

- Open an issue before a large change, so the design can be agreed first.
- Work on a branch and open a pull request against `main`.
- Commit messages explain the **why**, not a restatement of the diff.
- Commits must be signed.
- Every hand-written `.go` file carries the two-line SPDX header; the
  pre-commit hook fails without it.
- Never hand-edit `pkg/swapapi/*.pb.go`. Change `pkg/swapapi/swapapi.proto`,
  run `make proto` and commit both.
- Every user-visible change gets a line under `[Unreleased]` in
  [`CHANGELOG.md`](CHANGELOG.md); a release's notes are that section.
- `pre-commit install` once per checkout. The hooks format, lint and scan for
  secrets on every commit; never bypass them with `--no-verify`.
- `go test -race ./...` must pass, and must never touch your real backup
  root, `~/.claude.json` or Keychain: redirect `HOME`, `XDG_DATA_HOME` and
  `CLAUDE_CONFIG_DIR` to `t.TempDir()`. **New functionality comes with tests
  in the same pull request**, and a pull request that adds behavior without
  them will not be merged. A bug fix comes with the test that would have
  caught it. A file or response cswap parses has a fuzz target.

Please also read the [Code of Conduct](CODE_OF_CONDUCT.md). Security issues go
through [SECURITY.md](SECURITY.md), never a public issue.
