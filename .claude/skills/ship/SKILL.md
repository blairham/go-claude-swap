---
name: ship
description: Run checks, commit staged changes, push, and open a pull request against main.
user-invocable: true
allowed-tools:
  - Bash(git status:*)
  - Bash(git diff:*)
  - Bash(git log:*)
  - Bash(git rev-parse:*)
  - Bash(git branch:*)
  - Bash(git add:*)
  - Bash(git commit:*)
  - Bash(git push -u origin *)
  - Bash(git push origin *)
  - Bash(make check)
  - Bash(make lint)
  - Bash(make fmt)
  - Bash(make test)
  - Bash(make check-versions)
  - Bash(gh pr create:*)
  - Bash(gh pr view:*)
  - Read
  - Grep
  - Glob
---

# ship

Commit changes on the current branch and open a pull request against main.

## Steps

1. Run `make check` **and** `make lint`. This repo's `check` target is
   `fmt vet test` and does not include lint, so both are required. If either
   fails, fix the issues and re-run before proceeding. Note that `fmt` rewrites
   files in place — re-stage anything it changed.
2. Review `git diff` and `git status` to understand what changed.
3. Check `git log --oneline main..HEAD` to see existing commits on this branch.
4. If you are on `main`, create a branch first — do not commit directly to it.
5. Stage and commit with a concise message explaining the **why**. Do not add
   `Co-Authored-By`, "Generated with Claude Code", or any other AI-attribution
   trailer (see AGENTS.md → Code Conventions).
6. Push the branch with `git push -u origin HEAD`.
7. Open a PR with `gh pr create --base main`:
   - Title: short, under 70 characters
   - Body:
     ```
     ## Summary
     <1-3 bullets covering all commits on the branch>

     ## Test plan
     - [ ] `make check` passes
     - [ ] `make lint` passes
     ```
8. Report the PR URL.

## Safety

- Cannot force-push or amend remote commits.
- Cannot merge, close, comment on, or edit existing PRs.
- Cannot bypass failures with `--no-verify`.
- If the change touches `internal/switcher`, `internal/credentials`,
  `internal/locks`, `internal/claudecfg`, or `internal/keychain`, say so in the
  PR summary — those carry the switch invariants documented in AGENTS.md.
