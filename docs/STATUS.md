# Status

## Current milestone: M3 — ACP one-shot

### Done

- ADR 0002: the ACP SDK is `github.com/coder/acp-go-sdk` v0.13.5 (first Go library on the ACP
  libraries page, generated from the official schema, protocol version 1).
- `internal/config`: built-in agent presets from the ACP Registry (`claude`, `codex`, `gemini`,
  `opencode`), `$XDG_CONFIG_HOME/ecdy/config.toml` adds or overrides agents (strict decoding:
  unknown keys are errors).
- `internal/acpclient`: `Start` spawns the agent in its own process group with three `os.Pipe`s
  (so `Wait` never blocks on a grandchild holding stderr), `initialize` (no client capabilities),
  checks the protocol version, `session/new` (absolute cwd, `mcpServers: []`); `auth_required` →
  `AuthError` (ecdy does not call `authenticate`: the user logs in with the agent's CLI).
  `Prompt` sends `session/cancel` on cancellation and keeps waiting for the stop reason; pending
  permission requests are answered `cancelled`, also when the agent never sends `$/cancel_request`,
  and requests arriving after the cancel are answered without asking. `Kill` (SIGKILL to the group)
  and `Close` (stdin EOF, SIGTERM, SIGKILL after `KillGrace`). An agent that exits or closes its
  stdout → `ExitError` with the exit status and the last 4 KiB of stderr. Before reporting it,
  ecdy waits (up to 2 s) until every `session/update` already read has been handled and stderr is
  read to EOF: the SDK handles notifications on its own goroutine and reported the disconnect
  first, so the last text before a crash was lost in ~half of the runs (found by `-count=20`). A
  child that left the process group (setsid) and holds stdout no longer hangs `ecdy ask`: after
  the same timeout ecdy stops reading. Agent errors are printed as `method: message: details`,
  not the SDK's JSON.
  The same wait (bounded the same way) runs before a permission dialog: the SDK handles the
  request ahead of the notifications before it, so on one CPU the agent's explanation was printed
  after the dialog. Ctrl+C in the dialog sends `session/cancel` before answering the request
  (once per turn); otherwise the agent's reply could end the turn first and the cancel was lost. The SDK logs to
  `ECDY_LOG`'s file or nowhere, never to the terminal.
- `internal/render`: text chunks as is; one status line per tool call (`⚙`/`$` + title, `✓`/`✗`),
  redrawn in place on a terminal; `-v` adds thoughts, plans, tool output. Markdown rendering is M7.
- `internal/tty`: permission dialog on `/dev/tty` in raw mode: digits, `y`, `n`, Esc (reject),
  Ctrl+C (cancel the turn); Enter allows nothing; pending input is flushed when the dialog opens
  (`tcflush`, Linux and macOS); several keys in one read are processed in order, escape
  sequences skipped. No terminal → the request is rejected with a message on stderr.
- `ecdy ask [--agent NAME] [-v] -- PROMPT`: exit 0 / 1 / 130. First Ctrl+C → `session/cancel`,
  second (or SIGTERM/SIGHUP) → kill the agent's group.
- `internal/testutil/fakeagent`: scriptable ACP agent (text, echo, thoughts, tool calls,
  permission, wait/await cancel, hang, crash, stderr, auth required, protocol version, ignore
  SIGTERM, no `$/cancel_request`), recording every request. Test binaries re-exec themselves as
  the agent. `testutil.Term.ExitCode`.
- Tests: acpclient against the fake agent (streaming, tool call, permission allow/reject,
  handler error, cancel, cancel during permission, Ctrl+C in the dialog, hung turn + Kill,
  crash, exit at start, command not found, auth, version mismatch, cancelled start, kill grace
  0 and 300 ms with a SIGTERM-ignoring group, orphan-free Kill); `ecdy ask` end to end under a PTY
  (streaming, dialog keys, typeahead flush, Ctrl+C in the dialog, cancel, second Ctrl+C with a
  hung agent, Ctrl+C at start, crash, auth, unknown agent, missing binary, no terminal); shell
  PTY tests now go through the fake agent instead of the stub. Every new test was seen failing:
  21 mutations of the code (no flush, no process group, no cancel, no kill, no drain, …), all
  caught; the drain timeout is tested with 0 and 300 ms (an undecodable update, a setsid child
  holding the pipes).

### Verified locally (2026-09-27)

- `nix develop .#zsh-matrix -c go test -race ./...`; `-race -count=20` and on one CPU
  (`taskset -c 0`, `-count=5`) for `./shell/ ./cmd/ecdy/ ./internal/acpclient/`;
  `golangci-lint run` (0 issues); `nix build`, `nix flake check`. No processes left behind.
- Manual check with claude-agent-acp (npx, 0.81.2) in a real terminal: a reply streams (exit 0);
  the permission dialog for `rm note.txt` (options `Yes`/`No`) — `n` rejects, the file stays;
  Ctrl+C in the dialog and during a long reply → `cancelled`, exit 130; a prompt through the zsh
  plugin reaches the agent (`⚙ Read note.txt ✓`). `ls` ran without a dialog: claude-agent-acp
  allows read-only commands itself.
- codex-acp: not verified. `session/new` fails with "workspace routing discovery timed out", and
  `codex exec` alone cannot reach chatgpt.com from this machine either; to be retried.

### Known limitations

- One prompt = one agent process and one session: no conversation continuity, npx start-up on
  every prompt (~10 s for claude). M4.
- No session context in the prompt (M5); no `fs/*`, `terminal/*` capabilities (M6).
- Markdown is printed raw (M7).
- "Always allow" is stored by the agent; ecdy has no policy of its own (open question 4) and no
  "allow everything" mode yet.
- `authenticate` is never called: an agent that needs a login makes `ecdy ask` exit 1 with a hint.
- `tcflush` is implemented for Linux (common architectures) and macOS only.

## Done: M2 — zsh integration

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
  Both jobs use magic-nix-cache (GitHub Actions cache; FlakeHub and diagnostics off), so the zsh
  releases built from source are compiled once, not on every run.
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

## Next: M4 — daemon and continuity

See [ROADMAP.md](ROADMAP.md). Start a new session, read this file, branch `m4-daemon`.
First task: the daemon model ADR (AGENTS.md section 8, open question 2).
