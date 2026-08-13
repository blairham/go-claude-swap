---
description: Run the pre-PR gate (make check + make lint) and report failures concisely.
allowed-tools: Bash(make:*), Bash(go build:*), Bash(go test:*), Bash(go vet:*), Bash(go tool:*), Bash(gofmt:*), Bash(golangci-lint:*)
---

Run the full pre-PR check gate for go-claude-swap and report the outcome.

Run `make check` followed by `make lint`. Note that `check` is `fmt vet test` —
it does **not** include lint, which is why both commands are needed. `fmt` runs
`go tool gofumpt -w .`, `vet` runs `go vet ./...`, `test` runs
`go test -v -race ./...`, and `lint` runs `go tool golangci-lint run ./...`.

If the caller passes `$ARGUMENTS` to scope the run (e.g. `lint` or `test`), run
`make $ARGUMENTS` instead of the full gate.

Then:
- If everything passes, say so in one line.
- If `fmt` rewrote files, list which files changed (those need re-staging).
- If `vet`, `lint`, or `test` failed, report each failing linter/test by name with
  the `file:line` and the minimal fix — do not fix it yourself unless asked.
- For a `-race` failure, report both goroutine stacks, not just the summary line.

Do not commit, push, or open a PR.
