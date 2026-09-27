# Status

## Current milestone: M2 — zsh integration

### Done

- `shell/zsh/ecdy.plugin.zsh`, embedded by package `shell` and printed by `ecdy init zsh`
  (`eval "$(ecdy init zsh)"`); can also be sourced directly.
  - `accept-line` wrapper. Classifies only fresh top-level lines (`CONTEXT=start`, empty
    `PREBUFFER`): continuation lines, `vared` and `select` are accepted untouched.
  - `_ecdy_first_kind` computes the kind of the first word without forking: skips assignments,
    precommand modifiers (with the same option table as `internal/classify/words.go`) and
    redirections, then checks `$aliases`/`$galiases`, `$reswords`, `$functions`, `$builtins`,
    `whence -p`. `TestFirstKind` checks it against `classify.FirstWord`.
  - `prompt` → the buffer becomes `ecdy ask -- '<prompt>'` (`(qq)` quoting: no history expansion,
    no globbing) and is accepted through the previous `accept-line`.
  - `ask` → one-key dialog under the line (`zle -M` + `read -k 1`): Enter → agent (default),
    `r` run, `e` edit (keep the buffer), Esc/other → drop the line (`send-break`), and `f` runs
    the typo correction (not in AGENTS.md's sketch; shown only when the classifier has one).
  - Alt+Enter (`^[^M`, emacs + viins) → run as a command, no classification.
  - History: `zshaddhistory` returns 1 for the rewritten line; the original line is added with
    `print -s` from `precmd`, because a line rejected by `zshaddhistory` "lingers" and Up would
    recall `ecdy ask -- ...` otherwise.
  - Fail-open: `ecdy` not in `$PATH` (checked with `whence -p`, no fork), non-zero exit, wrong
    number of fields, or no answer within `ECDY_CLASSIFY_TIMEOUT` (default 0.5 s; the classifier
    runs in a process substitution that reports its pid, and is killed on timeout) → vanilla
    `accept-line`. The pid is read with its own 1 s deadline and the killed classifier is waited
    for (up to 0.2 s, `kill -0` + `zselect`): on zsh 5.9 (Ubuntu) a classifier child exiting while
    ZLE drew the next prompt left the prompt blank until a key was pressed. This was the first
    CI failure of M2; `TestFailOpen/deadline` covers it.
  - If `accept-line` was already a user widget (zsh-syntax-highlighting, zsh-autosuggestions, …)
    it is kept as `_ecdy_orig_accept_line` and called instead of `.accept-line`.
- `ecdy classify --format=nul`: five NUL-terminated fields (verdict, prompt, suggestion,
  correction, dangerous) for the plugin; `--format=text|json`, `--json` kept.
- `ecdy ask -- PROMPT`: stub that prints `ecdy ask (no agent yet): PROMPT`.
- `internal/testutil`: `Term`, a PTY driver (`github.com/creack/pty`) with `Send`/`Expect`.
- PTY tests in `shell/zsh_test.go` (`zsh -f -i`, plugin loaded via `eval "$(ecdy init zsh)"`):
  command, prompt (incl. `$`, quotes, globs, `?` prefix), Alt+Enter, Ask dialog (Enter / e / Esc /
  r on `rm everything in tmp except configs` with a real `configs` file), typo dialog (f / Enter),
  fail-open (binary missing, crashing, hanging, printing garbage), history (in memory, `$HISTFILE`,
  Up right after a prompt), coexistence with zsh-syntax-highlighting 0.8.0 and
  zsh-autosuggestions 0.7.1 loaded before and after ecdy.
- devShell exports `ECDY_TEST_ZSH_SYNTAX_HIGHLIGHTING` / `ECDY_TEST_ZSH_AUTOSUGGESTIONS`; the CI
  `go` job installs zsh and both plugins from apt. `vendorHash` updated.

### Follow-up: supported zsh versions and agent tooling (branch `chore/dev-tooling`)

- Minimum supported zsh is 5.8 (Ubuntu 22.04, Debian 11, RHEL 9); the plugin checks it with
  `is-at-least` and does not load on older versions (`TestMinVersion`).
- `nix/zsh-versions.nix` builds 5.8.1 and 5.9 from the upstream tarballs (gcc 13: with gcc >= 14
  their configure misdetects termcap and signal handling, and 5.9 hung in `pause()`) plus nixpkgs'
  latest zsh. `nix develop .#zsh-matrix` sets `ECDY_TEST_ZSH`, and every PTY test runs as a subtest
  per zsh.
- CI: both jobs run in the flake's environments. `go` runs vet, `-race` tests on the zsh matrix,
  cold start, fuzz and lint in the `zsh-matrix` devShell; `nix` checks packaging
  (`flake check`, `build`). The apt-installed zsh and `setup-go` are gone, so nothing runs twice.
- The blank-prompt race fixed above is rare: it reproduced a handful of times in hundreds of runs
  (on a CI runner and in Docker with 1–2 CPUs), and 800 runs of the pre-fix plugin in the same
  container, on Ubuntu's zsh 5.9 and on nix-built 5.8.1/5.9/5.9.2, all passed. So there is no
  evidence that it depends on the zsh version or its distribution build; an earlier note here
  claiming it did was wrong. `TestFailOpen/deadline` runs 20 lines to give it more chances.
- Agent skills in `.agents/skills/` (`.claude/skills` → symlink): `ci-repro` (reproduce CI jobs
  locally, incl. running PTY tests on one CPU) and `milestone-finish` (the checklist before a
  PR). AGENTS.md section 8 gained the testing rules learned in M2.

### Verified locally (2026-09-27)

- `nix develop -c go test -race ./...` — green; `go test -race -count=20 ./shell/` — green;
  also `go test -race -count=10 ./shell/` in Docker `ubuntu:24.04` (zsh 5.9, 2 CPUs, as a
  non-root user, apt zsh plugins) — green;
  `golangci-lint run` — 0 issues; `nix build`, `nix flake check` — ok.
- Plugin overhead per Enter (`_ecdy_first_kind` + fork/exec of `ecdy classify`), 200 runs:
  p50 4.5 ms, p99 5.2 ms.

### Known limitations

- After a prompt, the scrollback shows the rewritten `ecdy ask -- '...'` line instead of what was
  typed (the buffer is redrawn before it runs). UX, M7.
- Other tools that record commands in `preexec` (e.g. atuin) see `ecdy ask -- ...`; not tested
  with zsh-vi-mode, fzf-tab and atuin yet (AGENTS.md section 3 lists them).
- `ECDY_SESSION` is not exported yet (M4).
- `TestFirstKind` uses `/usr/bin:/bin` as `$PATH`, so it expects `ls` and `/bin/sh` there.

## Next: M3 — ACP one-shot

See [ROADMAP.md](ROADMAP.md). Start a new session, read this file, branch `m3-acp`.
First task: choose the ACP Go SDK and record it in ADR 0002 (AGENTS.md section 5).
