---
name: ci-repro
description: Reproduce ecdy's CI locally before pushing or when a CI job fails. Use when a GitHub Actions job (go or nix) is red, when a test passes locally but may not on CI, before finishing a milestone, or after touching the zsh plugin, PTY tests or anything timing-sensitive.
---

# Reproduce CI locally

CI (`.github/workflows/ci.yml`) has two jobs, and they test different things. Reproduce the one
that failed; before a push that touches the shell integration, run both.

| CI job | What is special | Local command |
|---|---|---|
| `nix` | zsh 5.8.1, 5.9 and the latest release built by nix (`nix/zsh-versions.nix`) | `nix develop .#zsh-matrix -c go test ./...` |
| `go` | Ubuntu's zsh **build** from apt (distribution patches), non-root user, 2 CPUs | `.agents/skills/ci-repro/ubuntu.sh` |

The version matrix does not replace the Ubuntu run: the M2 blank-prompt bug reproduced only with
Ubuntu's zsh 5.9, not with upstream 5.9 built by nix.

## Steps

1. Read the failing log first: `gh run list --branch <branch> -L 3`, then
   `gh run view <id> --log-failed`. Note the exact test and the last output it saw.
2. Reproduce with the matching command above. Narrow it down, e.g.
   `.agents/skills/ci-repro/ubuntu.sh -race -count=10 -run 'TestFailOpen' ./shell/`.
   Timing-dependent failures: `CPUS=1` and `-count=10` or more. The M2 blank-prompt race did not
   show up in 50 runs with 2 idle CPUs and failed within 10 runs with `CPUS=1`.
3. Do not fix before it reproduces. If it does not reproduce, make the failing path cheaper to
   hit (for example `ECDY_CLASSIFY_TIMEOUT=0`) rather than guessing. PTY test timeouts print the
   raw output tail, the process tree with kernel wait channels and `$HOME`: use them.
4. After the fix, prove the new or changed test fails without it (revert the fix locally),
   then run both commands above.

## Notes

- `ubuntu.sh` needs Docker and network access (apt, go.dev, proxy.golang.org). It copies the
  repository, so uncommitted changes are included.
- `UBUNTU_IMAGE=ubuntu:26.04` checks the next `ubuntu-latest` before GitHub switches to it.
- `gh run rerun` and `gh pr create` may be denied to the token; a new push to a branch with an
  open PR triggers CI again.
