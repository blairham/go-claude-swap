<!--
Explain the why. The diff already says what.
A change to pkg/swapapi/swapapi.proto needs `make proto` committed with it.
No credential may reach a log, the history, the usage cache or a --json
output, and on-disk formats stay compatible with claude-swap
(CONTRIBUTING.md).
-->

## What this changes



## Why



## How it was verified

<!--
Tests are the usual answer. A bug fix comes with the test that would have
caught it; parsing of a file or response cswap reads (settings, roster,
mappings, usage) has a fuzz target.
-->



---

- [ ] I have signed the CLA (see CLA.md)
- [ ] CHANGELOG.md has an entry under [Unreleased]

Closes #
