---
description: Run the pre-PR gate (make check) and report failures concisely.
allowed-tools: Bash(make:*), Bash(go build:*), Bash(go test:*), Bash(go vet:*), Bash(go tool:*), Bash(gofmt:*)
---

Run the full pre-PR check gate for go-claude-swap and report the outcome.

Run `make check`, which is `fmt vet test`. `fmt` runs `go tool gofumpt -w .`,
`vet` runs `go vet ./...`, and `test` runs `go test -v -race ./...`. There is no
lint target: golangci-lint runs as a pre-commit hook and in CI, and is never
started by hand.

If the caller passes `$ARGUMENTS` to scope the run (e.g. `vet` or `test`), run
`make $ARGUMENTS` instead of the full gate.

Then:
- If everything passes, say so in one line.
- If `fmt` rewrote files, list which files changed (those need re-staging).
- If `vet` or `test` failed, report each failing linter/test by name with
  the `file:line` and the minimal fix — do not fix it yourself unless asked.
- For a `-race` failure, report both goroutine stacks, not just the summary line.

Do not commit, push, or open a PR.
