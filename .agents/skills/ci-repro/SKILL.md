---
name: ci-repro
description: Reproduce ecdy's CI locally before pushing or when a CI job fails. Use when a GitHub Actions job (go or nix) is red, when a test passes locally but may not on CI, before finishing a milestone, or after touching the zsh plugin, PTY tests or anything timing-sensitive.
---

# Reproduce CI locally

CI (`.github/workflows/ci.yml`) runs in the same nix environments as local work, so reproducing
a job means running its commands in the same devShell.

| CI job | Checks | Local command |
|---|---|---|
| `go` | vet, `-race` tests with the PTY tests on zsh 5.8.1, 5.9 and the latest (`nix/zsh-versions.nix`), cold start, fuzz, lint | `nix develop .#zsh-matrix -c go test -race ./...` (other steps: see ci.yml) |
| `nix` | packaging | `nix flake check && nix build && ./result/bin/ecdy version` |

## Time budget

Every command below runs in the foreground with a budget: `timeout <seconds>` outside and
`-timeout=` (or `-test.timeout=`) for the test binary. Never start a long run in the background
and wait on it, and never pipe it through `tail`: progress must stay visible. A run expected to
take more than a couple of minutes (the full zsh matrix with `-race`, `-count` in the hundreds)
needs the maintainer's go-ahead and an estimate first.

## Steps

1. Read the failing log first: `gh run list --branch <branch> -L 3`, then
   `gh run view <id> --log-failed`. Note the exact test and the last output it saw. PTY test
   timeouts print the raw output tail, the process tree with kernel wait channels and `$HOME`.
2. Reproduce it: run only that test, many times, on the zsh it failed on:
   ```sh
   timeout 300 nix develop .#zsh-matrix -c go test -race -count=20 -timeout=280s -run 'TestFailOpen/zsh-5.9/deadline' ./shell/
   ```
3. CI runners are slower and busier than a workstation. For timing-dependent failures, build
   the test binary once and run it on one CPU:
   ```sh
   nix develop .#zsh-matrix -c go test -c -o /tmp/shell.test ./shell/
   timeout 600 nix develop .#zsh-matrix -c systemd-run --user --scope -q -p CPUQuota=100% \
     /tmp/shell.test -test.count=100 -test.timeout=550s -test.run 'TestFailOpen/.*/deadline'
   ```
   (`taskset -c 0 …` pins it to one core instead.) Note that the PTY tests build `ecdy` from the
   working tree when they start, so the code under test is whatever is on disk at that moment.
4. Races can be rare: the M2 blank-prompt race showed up a handful of times in hundreds of runs,
   mostly in the first shells of a test process. If it does not reproduce, make the failing path
   cheaper to hit (for example `ECDY_CLASSIFY_TIMEOUT=0`), and fix the cause you can explain
   rather than the symptom. Do not claim a cause (a zsh version, a distribution) without a
   sample large enough to compare.
5. After the fix, prove the test fails without it (revert the fix locally), then run the
   commands of both jobs.

## Notes

- `gh run rerun` and `gh pr create` may be denied to the token; a new push to a branch with an
  open PR triggers CI again.
