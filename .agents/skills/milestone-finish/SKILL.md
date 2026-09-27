---
name: milestone-finish
description: Checklist for finishing an ecdy milestone or any branch before handing it over for review. Use when the work on a milestone (docs/ROADMAP.md) or a feature branch seems done, before the final push, or when asked to open a PR.
---

# Finish a milestone

Run every step; if one cannot be done, say so in the report instead of skipping it silently.

## 1. Tests you can trust

- Each new test must have been seen failing once: break the code or revert the fix locally,
  watch the test go red, restore. A test that passed on its first run proves nothing yet.
- Every timeout, deadline or retry has a test with the extreme value (`0`, a hung child).
- Classifier or redactor changes start as rows in the golden tables (AGENTS.md, section 8).

## 2. Local checks

```sh
nix develop .#zsh-matrix -c go test -race ./...           # CI go job: zsh 5.8.1, 5.9, latest
nix develop .#zsh-matrix -c go test -race -count=20 ./shell/   # PTY tests: flakiness
nix develop -c golangci-lint run
nix build && ./result/bin/ecdy version
nix flake check
```

Changed dependencies: `go mod tidy`, then refresh `vendorHash` in `flake.nix`
(set `pkgs.lib.fakeHash`, `nix build`, copy the `got:` hash).
Touched `shell/` or anything timing-sensitive: also run the PTY tests on one CPU (the
`ci-repro` skill, step 3).

## 3. Docs

- `docs/STATUS.md`: what is done, verified how (commands and numbers), known limitations,
  the next step and how to start it.
- `docs/ROADMAP.md`: mark the milestone ✅ only when its DoD is met.
- `README.md`: anything user-visible (install, settings, requirements).
- An ADR for any decision listed in AGENTS.md, section 8.

## 4. Hand over

- Small Conventional Commits, one logical step each, GPG-signed; never disable signing.
- `git push -u origin <branch>`, then `gh pr create`; if the token cannot create PRs, give
  the compare link `https://github.com/Raimguzhinov/ecdy/compare/main...<branch>`.
- CI runs on pull requests: once the PR exists, watch it (`gh run list --branch <branch>`,
  `gh run watch <id> --exit-status`). A red job → the `ci-repro` skill.
- The maintainer merges, with a merge commit. Do not start the next milestone in the same
  session.
