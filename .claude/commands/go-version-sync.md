---
description: Verify (and fix) the go.mod ↔ .tool-versions Go-version invariant.
allowed-tools: Bash(make:*), Bash(pre-commit run:*), Bash(go run:*), Read
---

Enforce the Go-version sync invariant that the `check-go-version-sync` pre-commit
hook guards (from blairham/pre-commit-hooks, pinned by `rev`): the `go X.Y.Z` line in `go.mod`
must equal the `golang X.Y.Z` line in `.tool-versions`.

`go.mod` is authoritative. It commonly drifts because the `go` directive gets
pulled up by a `tool` block dependency (golangci-lint, gofumpt) during
`go mod tidy`, leaving `.tool-versions` behind.

Steps:
1. Run `make check-versions`.
2. If it passes, report "in sync (go X.Y.Z)" and stop.
3. If it fails, run `make sync` (it rewrites `.tool-versions` from `go.mod`), then
   report the before/after and remind the caller to stage `.tool-versions`.

If `$ARGUMENTS` is `check`, only report drift — do not run `make sync`.

CI reads the Go version from `go.mod` (`go-version-file`), so moving the
`go` directive moves CI with it; there is no separate workflow pin to update.
