# AGENTS.md — ecdy

> **ecdy** (from *ecdysis* — molting: an arthropod sheds its old shell in order to grow).
> A smart layer on top of your real shell: you type in zsh as usual, ecdy figures out whether the line
> is a command or a prompt, and sends prompts to **any** agent over the Agent Client Protocol (ACP).

This file is the main source of truth for any coding agent (or human) working on the project.
Read it in full before every task. If something here contradicts the code, raise it with the maintainer
first, then fix either the code or this file.

The milestone plan lives in [docs/ROADMAP.md](docs/ROADMAP.md); current progress in [docs/STATUS.md](docs/STATUS.md).

---

## 1. Vision

Today the stack looks like this: terminal emulator → multiplexer (tmux) → shell (zsh) → agent (claude, codex, opencode, pi).
The shell and the agent live apart: the agent doesn't see what you did in the shell, and the agent doesn't have your zsh
(completion, highlighting, aliases, history).

ecdy glues the two layers together **without replacing zsh**:

- You stay in your own zsh with your config, plugins, completion and highlighting.
- On Enter, ecdy classifies the line. zsh runs commands as usual. Prompts go to the agent.
- The agent connects over ACP, so any ACP-compatible agent works: Claude Code, Codex, Gemini CLI, OpenCode, Pi and others.
- The agent receives session context: cwd, recent commands, exit codes, git branch.
- The agents' own logins and subscriptions are used. No cloud of our own, no token reselling.

### Non-goals (do not build these, even if "it would be cool")

- ❌ Not a terminal emulator.
- ❌ Not a new shell language and not a reimplementation of POSIX/zsh.
- ❌ Not an agent or an LLM client of its own: ecdy is only an ACP client.
- ❌ No cloud, no accounts, no telemetry by default.
- ❌ Does not replace zsh as the login shell: ecdy is a plugin + a binary.

---

## 2. Security invariants (must never be violated)

1. **A prompt is never executed as a command.** If the classifier is unsure, it does not run the line — it asks.
   The error "a command was sent to the agent" is cheap. The error "a prompt was executed as a command" is dangerous.
   Canonical example: `rm everything in tmp except configs` — the first word is a valid command, and `rm` will delete the file `configs` if it exists.
2. **An ecdy failure never breaks the shell.** If the binary is missing, crashes or exceeds its deadline, zsh behaves like vanilla zsh
   (plain `accept-line`). This behavior is documented in the README.
3. **ecdy never executes anything on the agent's behalf without the user's explicit permission.** Every `session/request_permission`
   is shown to the human. "Allow everything" mode is enabled only by an explicit per-session flag and is visible in the UI.
4. **Secrets are not sent to the agent by default.** Session context goes through a secret redactor
   (tokens, `*_KEY=`, `*_TOKEN=`, `Authorization:` etc.). Command output capture is off by default.
5. **Sockets and state** live in `$XDG_RUNTIME_DIR/ecdy/` (mode 0700) and `$XDG_STATE_HOME/ecdy/`. Never in `/tmp` with 0777.
6. **No network calls from ecdy itself.** Only the agent processes started by the user touch the network.

---

## 3. Architecture

```
┌──────────────── terminal / tmux ─────────────────┐
│  zsh (user config, completion, highlighting)     │
│   └─ ecdy.plugin.zsh                             │
│        • accept-line wrapper (ZLE widget)        │
│        • preexec/precmd hooks → session log      │
│        • zshaddhistory → the original line is    │
│          written to history                      │
│          │                                       │
│          ├─ `ecdy classify`  (fast, on every Enter)
│          └─ `ecdy ask -- "<prompt>"` (a regular  │
│              foreground process: TTY, Ctrl+C,    │
│              job control for free)               │
└──────────┼───────────────────────────────────────┘
           │ unix socket ($XDG_RUNTIME_DIR/ecdy/<session>.sock)
           ▼
     ecdy daemon (one per shell session, started lazily)
           │ keeps the agent process alive → the conversation continues across prompts
           │ JSON-RPC 2.0 over stdio (ACP)
           ▼
     ACP agent: claude-agent-acp | codex-acp | gemini --acp | ...
```

### Key decision: rewrite the buffer instead of writing our own line editor

When the classifier says `prompt`, the ZLE widget **rewrites `$BUFFER`** into `ecdy ask -- <quoted>` and calls
`zle .accept-line`. Thanks to this:

- `ecdy ask` becomes a regular foreground process with a TTY, signals and job control — nothing to reinvent;
- input before Enter stays entirely with zsh, so completion, highlighting, autosuggestions and vi-mode keep working;
- the `zshaddhistory` hook writes the **original** line to history, not `ecdy ask ...`.

Sketch (implement, verify and cover with tests; this is not final code):

```zsh
_ecdy_accept_line() {
  emulate -L zsh
  local verdict
  verdict=$(command ecdy classify --shell=zsh --first-kind="$(_ecdy_first_kind)" -- "$BUFFER" 2>/dev/null) \
    || verdict=cmd            # invariant 2: failure → vanilla behavior
  case $verdict in
    prompt) _ECDY_ORIG=$BUFFER; BUFFER="ecdy ask -- ${(q)BUFFER}" ;;
    ask)    _ecdy_disambiguate; return ;;   # invariant 1: do not execute
  esac
  zle .accept-line
}
zle -N accept-line _ecdy_accept_line
bindkey '^[^M' _ecdy_force_command   # Alt+Enter — run as a command, skipping classification
```

`_ecdy_first_kind` determines the kind of the first word via `whence -w`, after skipping assignments (`FOO=1`)
and precommand modifiers (`sudo`, `noglob`, `command`, `builtin`, `exec`, `nocorrect`, `env`, `time`).
**Only the shell knows its aliases and functions**, so this fact is computed in zsh and passed to Go.

Compatibility to verify with tests: `zsh-syntax-highlighting`, `zsh-autosuggestions`
(both wrap widgets), `zsh-vi-mode`, `fzf-tab`, `atuin`. Document the source order and the `accept-line` override
in the README.

---

## 4. Classifier (the heart of the project)

A pure Go function with no I/O:
`Classify(input Input) Verdict`, where `Verdict ∈ {Cmd, Prompt, Ask}` plus a `Reason` for debugging.

`Input` contains: the line, `FirstKind` (alias/function/builtin/command/reserved/hashed/none — from zsh),
cwd (to check whether paths exist), config.

### Rule cascade (order matters, first match wins)

1. **Empty line / whitespace only** → `Cmd` (pass-through).
2. **Explicit override**: a `?` prefix at the start of the line → `Prompt` (the prefix is stripped). Alt+Enter → `Cmd`, bypassing the classifier.
   Prefixes are configurable.
3. **First word is unknown** (`FirstKind=none`, not a path like `./x` or `/x`, not an assignment):
   - looks like a typo of a known command (Damerau–Levenshtein ≤ 1, and the rest looks like arguments, e.g. `gti status`)
     → `Ask` with a "did you mean git?" suggestion;
   - otherwise → `Prompt`.
4. **First word is known.** Count natural-language signals in the rest of the line (outside quotes):
   - non-ASCII letters (Cyrillic etc.);
   - stop words (en: the, a, all, and, please, why, how, what, every, except…; ru: как, что, почему, все, кроме, пожалуйста…);
   - `?` at the end of the line;
   - ≥ 3 "word-like" arguments that are not flags, not paths, not existing files and not globs;
   - the line does not parse with `mvdan.cc/sh/v3/syntax` (bash dialect as a heuristic; zsh-specific syntax must not count as an error).

   No signals → `Cmd`. Strong signals → `Ask`, not `Prompt`: the word is known, so the human decides.
5. **Dangerous command + any NL signal** (`rm`, `dd`, `mkfs*`, `kill`, `pkill`, `chmod -R`, `chown -R`, `git reset --hard`,
   `git clean`, `git push --force`, `shutdown`, `reboot`, `truncate`, `find ... -delete`) → always `Ask`,
   and the dialog defaults to "send to agent".

### The `Ask` dialog

One line below the input, single-key choice:
`⏎ agent · r run · e edit · Esc cancel`. Enter = agent by default (the safe option).

### Performance budget

`ecdy classify` runs on every Enter. Target: **p99 < 15 ms** on a cold binary start. A benchmark is mandatory.
Heavy checks (file existence) are bounded in time and count. If the budget can't be met, add a
fast path through the daemon socket (`zmodload zsh/net/socket`), but only after measuring.

### Classifier tests

- Golden table `internal/classify/testdata/cases.tsv`: `input<TAB>first_kind<TAB>expected<TAB>comment`.
  **At least 200 cases** before the classifier is wired into zsh, including Russian,
  typos, pipes, heredocs, `sudo`, assignments, dangerous commands and the cases from section 2.
- Every classifier bug first becomes a row in the table, then gets fixed.
- Fuzz test: `Classify` does not panic on arbitrary input.

---

## 5. ACP client

Specification: https://agentclientprotocol.com (read `protocol/overview`, `initialization`, `session-setup`,
`prompt-turn`, `tool-calls`, `file-system`, `terminals`). JSON-RPC 2.0, stdio, all paths absolute.

Methods we need:

- **Client → Agent**: `initialize`, `authenticate` (if the agent requires it), `session/new`, `session/load`
  (if the `loadSession` capability is present), `session/prompt`, `session/cancel` (notification), `session/set_mode` (optional).
- **Agent → Client**: `session/update` (message chunks, tool calls, plans, mode changes),
  `session/request_permission`; with the declared capabilities also `fs/read_text_file`, `fs/write_text_file`,
  `terminal/create|output|wait_for_exit|kill|release`.

### Go SDK

Use an existing SDK, do not hand-write JSON-RPC. Candidates: `github.com/coder/acp-go-sdk`
(has an example bridge to Claude Code), `github.com/kdlbs/acp-go-sdk`, `github.com/keepmind9/acp-sdk-go`.
**Before choosing**, check which one is listed on the libraries page at agentclientprotocol.com, which schema version
it is generated from, and whether the repository is alive. Record the choice in an ADR (section 8).
**Do not invent SDK APIs**: read the source or godoc first, then write code.

### Agent presets (`~/.config/ecdy/config.toml`)

```toml
default_agent = "claude"

[agents.claude]
command = ["npx", "-y", "@agentclientprotocol/claude-agent-acp"]

[agents.codex]
command = ["npx", "-y", "@agentclientprotocol/codex-acp"]

[agents.gemini]
command = ["gemini", "--acp"]

[agents.opencode]
command = ["opencode", "acp"]

[agents.pi]
command = ["npx", "-y", "pi-acp"]   # adapter: runs `pi --mode rpc`, pi must be on $PATH

# others — check launch commands against the ACP Registry before adding a preset
```

These presets are built in (`internal/config`); the file only adds agents or overrides them.

The agent owns authentication: if the user is logged in to the agent's CLI, that login is used.
ecdy does not store keys.

### Rendering in `ecdy ask`

- Agent text chunks stream to stdout immediately. Markdown is rendered as it arrives, without waiting for the end of the reply.
- Tool calls are shown as a single status line (`⚙ Read src/main.go`, `$ go test ./...` → ✓/✗), details with `-v`.
- The permission prompt reads from `/dev/tty` in raw mode (`golang.org/x/term`); the options come from the agent's request.
- `Ctrl+C` → `session/cancel`, wait for the stop reason, exit with code 130. A second `Ctrl+C` kills hard.
- `ecdy ask` exit codes: 0 — turn completed, 1 — agent or protocol error, 130 — cancelled.

### Session context

The `preexec`/`precmd` hooks write to `$XDG_STATE_HOME/ecdy/sessions/<ECDY_SESSION>.jsonl`:
the command, cwd, exit code, duration and time. On `session/prompt`, a bounded block is appended to the prompt
(the last N=20 commands, ≤ 4 KB, after the secret redactor) plus cwd and git branch.
Command output is **not** captured by default. Optionally via `tmux capture-pane` when `$TMUX` is set
(a separate ADR decides this).

---

## 6. Repository layout

```
cmd/ecdy/            main: subcommands init, classify, ask, daemon, use, new, agents, doctor, log, version
internal/classify/   classifier (pure, no I/O except an injected FS) + testdata/
internal/acpclient/  wrapper over the chosen ACP SDK, agent launch, event mapping
internal/daemon/     per-session daemon, unix socket, agent lifecycle, idle timeout
internal/render/     terminal output: streaming, tool calls, statuses
internal/tty/        raw-mode dialogs (permission, Ask)
internal/sessionlog/ command log, secret redactor, context assembly
internal/config/     TOML loading, defaults, validation
internal/testutil/   fake ACP agent for tests (scriptable), PTY helpers
shell/zsh/           ecdy.plugin.zsh (embedded into the binary, served by `ecdy init zsh`)
docs/adr/            architecture decisions (NNNN-title.md)
docs/ROADMAP.md      milestones and their definitions of done
docs/STATUS.md       current milestone, what's done, what's next
docs/demo/           the README's demo GIF: VHS tapes, a scripted agent, render.sh
flake.nix            devShell (go, gopls, golangci-lint, zsh, tmux, nodejs for npx agents) + package
nix/zsh-versions.nix zsh releases for the zsh-matrix devShell (PTY tests on every supported zsh)
.agents/skills/      skills for coding agents (.claude/skills is a symlink to it)
```

User installation (like atuin/zoxide): `eval "$(ecdy init zsh)"` in `.zshrc`.

---

## 7. Stack and dependencies

- **Go**: latest stable from nixpkgs, module `github.com/Raimguzhinov/ecdy`.
- **Allowed dependencies** (anything else only after asking the maintainer):
  - ACP SDK (exactly one, per ADR);
  - `mvdan.cc/sh/v3` — shell syntax parser for heuristics;
  - `github.com/creack/pty` — PTY in integration tests;
  - `golang.org/x/term` — raw mode;
  - `github.com/pelletier/go-toml/v2` — config;
  - `github.com/spf13/cobra` — CLI;
  - `github.com/charmbracelet/lipgloss` (+ `glamour` if needed) — rendering.
- Logging: `log/slog` to the file `$XDG_STATE_HOME/ecdy/ecdy.log`, level via `ECDY_LOG`. **Never** write logs to stdout or stderr while the user is typing.

---

## 8. How to work in this repository (rules for agents)

- **Do not invent APIs.** For ACP, the SDK and non-trivial zsh features (ZLE, hooks, `whence`, `(q)`/`(z)` flags),
  find the documentation or source first and leave a link in a comment next to non-obvious code.
  Not sure — say so plainly, don't guess.
- **Tests first** for the classifier and the secret redactor. For everything else — tests in the same PR.
- **Small diffs.** One logical step per commit, Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`).
- **ADRs** (`docs/adr/NNNN-title.md`: context → decision → consequences) for the SDK choice, the daemon model,
  client capabilities, output capture and any change to the invariants.
- **Ask the maintainer** before: adding a dependency outside the list; changing the classifier's default behavior;
  doing anything that executes commands without confirmation; changing section 2.
- **Work milestone by milestone** as described in [docs/ROADMAP.md](docs/ROADMAP.md): one branch and one PR per milestone.
- **Update `docs/STATUS.md`** at the end of every session: what's done, what's broken, the next step.
- **Trust a test only after seeing it fail.** Revert the fix or break the code locally, watch the new
  test go red, restore. Every timeout or deadline gets a test with the extreme value (`0`, a hung child).
- **Every wait in a test is bounded.** No bare `<-ch` on something the code under test must close:
  `select` with a deadline, so a regression fails the test instead of hanging it. Run `go test`
  with `-timeout`, and long local runs with an outer `timeout`; a mutation check (break the code,
  watch the test go red) runs one mutation at a time with `-timeout=60s`, because broken code
  often hangs rather than fails.
- **Tests clean up the processes they start**, daemons included, even when they fail midway.
- **Shell integration is tested on every supported zsh** (`nix develop .#zsh-matrix`: 5.8.1 — the
  oldest supported, 5.9, the latest); CI runs the same devShell. PTY tests also run with
  `-race -count=20` before a PR, and on one CPU after timing-sensitive changes.
- **Reproduce a CI failure before fixing it**, and do not guess from the log alone.
- **Skills for coding agents** live in `.agents/skills/` (`.claude/skills` is a symlink to it):
  `ci-repro` reproduces the CI jobs locally, `milestone-finish` is the checklist before a PR,
  `demo-gif` re-renders the README's demo.
- Everything in the repository is in English: code, comments, user docs and this file.
- Go: no global state, `context.Context` as the first argument, wrap errors with `%w`,
  no `panic` outside `main`. No `golangci-lint` exclusions without a "why" comment.

### Commands

```sh
nix develop                                  # environment
go test ./...                                # all tests
go test -race ./...                          # before a PR
go test ./internal/classify -run TestGolden  # classifier table
go test ./internal/classify -bench . -benchmem
go test ./internal/classify -fuzz FuzzClassify -fuzztime 60s
golangci-lint run
nix develop .#zsh-matrix -c go test ./shell/ # PTY tests on zsh 5.8.1, 5.9 and the latest
go run ./cmd/ecdy classify --json --first-kind=command -- 'rm everything in tmp except configs'
zsh -f -c 'eval "$(go run ./cmd/ecdy init zsh)"; ...'   # manual plugin check
```

---

## 9. Open questions (decide via ADR, never silently)

1. ~~Changing cwd in the middle of an ACP session.~~ Decided in [ADR 0003](docs/adr/0003-session-daemon.md):
   the session keeps its cwd, a prompt from outside it prints a notice suggesting `ecdy new`.
2. ~~Do we need the daemon if the agent supports `session/load`?~~ Yes, [ADR 0003](docs/adr/0003-session-daemon.md):
   `session/load` still pays the agent's start-up and replays the whole conversation; not every agent has it.
3. How to show long agent replies without flooding the scrollback (folding, pager, `ecdy last`)?
4. Agents' "allow always" permissions vs ecdy's policy: whose wins and where is it stored?
5. Name: check `ecdy` for availability on GitHub, crates/npm/nixpkgs and domains before the first public release.
