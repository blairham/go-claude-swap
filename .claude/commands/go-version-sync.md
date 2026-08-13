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

`.github/workflows/ci.yml` pins `GO_VERSION` to the minor line (`"1.26"`), which
setup-go resolves to the latest patch — that tracks automatically and is not part
of the exact-match invariant. If the minor version itself changes in `go.mod`,
update the workflow too.
