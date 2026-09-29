# ecdy

> From *ecdysis* — molting: an arthropod sheds its old shell in order to grow.

**Status: early development (M7 — UX). The conversation continues across the prompts of one shell, the agent sees your recent commands (secrets redacted, no output), the right prompt shows what Enter will do, and `ecdy doctor` checks the setup.**

ecdy is a smart layer on top of your real shell. You keep typing in zsh with your own config,
completion, highlighting and history. On Enter, ecdy decides whether the line is a shell command
or a prompt: commands run in zsh as usual, prompts go to any agent that speaks the
[Agent Client Protocol](https://agentclientprotocol.com) (Claude Code, Codex, Gemini CLI, OpenCode, …).

ecdy is not a terminal emulator, not a new shell language, and not an agent or LLM client of its own.
It uses your agents' own logins and subscriptions; there is no ecdy cloud and no telemetry.

## Safety

- A prompt is never executed as a command. When the classifier is unsure, it asks.
- If the `ecdy` binary is missing, crashes or is too slow, zsh behaves exactly like vanilla zsh.
- Every agent permission request is shown to you; nothing is allowed by default.
- Secrets are redacted before your commands are logged or sent to the agent; command output is
  never recorded.

## Usage

Requires zsh 5.8 or newer (on older versions the plugin does not load). Add this to the end of `~/.zshrc`, after plugins that wrap `accept-line`
(zsh-syntax-highlighting and zsh-autosuggestions work in either order):

```sh
eval "$(ecdy init zsh)"
```

Or source `shell/zsh/ecdy.plugin.zsh` from a plugin manager; it looks for `ecdy` in `$PATH`.

On Enter, ecdy classifies the line:

- a **command** runs as usual;
- a **prompt** runs as `ecdy ask -- '<prompt>'`; the screen and the history keep the line you typed;
- when it is **not sure**, a dialog appears under the line and nothing runs until you choose:
  `⏎ agent · r run · e edit · Esc cancel` (plus `f fix` when the first word looks like a typo,
  e.g. `gti status`). Enter sends the line to the agent.

Overrides: start the line with `?` to force a prompt; press **Alt+Enter** to run it as a command
without classification.

**Indicator.** While you type, the right prompt shows what Enter will do: `→ agent` for a prompt,
`? ask` when a dialog will ask, nothing for a command. The classifier runs in the background on every
change of the line, so typing never waits for it; Enter still classifies the line itself.

The plugin replaces the `accept-line` widget (calling the previous one, if another plugin wrapped
it), binds Alt+Enter (`^[^M`) in the `emacs` and `viins` keymaps, adds `zle-line-init`,
`zle-line-pre-redraw` and `zle-line-finish` hooks (with `add-zle-hook-widget`) and defines a
function `ecdy` in front of the binary, so that `ecdy doctor` can check the shell. Settings:

| Variable | Default | |
|---|---|---|
| `ECDY_BIN` | `ecdy` | the ecdy executable |
| `ECDY_CLASSIFY_TIMEOUT` | `0.5` | seconds to wait for the classifier |
| `ECDY_INDICATOR` | `rprompt` | `rprompt`: shown in front of `RPROMPT`; `var`: only set `$ECDY_VERDICT` (`cmd`, `prompt`, `ask` or empty) and redraw, for a theme that shows it itself; `off`: no indicator, no background classifier |
| `ECDY_INDICATOR_PROMPT` | `%F{magenta}→ agent%f` | indicator for a prompt (prompt escapes allowed) |
| `ECDY_INDICATOR_ASK` | `%F{yellow}? ask%f` | indicator when the dialog will ask |
| `ECDY_INDICATOR_CMD` | empty | indicator for a command |

If `ecdy` is missing, crashes, prints something unexpected or misses the deadline, Enter behaves
exactly like in vanilla zsh, and no indicator is shown.

**`ecdy doctor`** checks that everything is in place: the `ecdy` the plugin runs, the config, each
agent's command on `$PATH`, zsh, and, typed at the prompt, the shell itself: that Enter, Alt+Enter
and `?` still reach ecdy after the other plugins loaded. `ecdy doctor --agents` also starts every
agent (no prompt is sent) to see that it runs and that you are logged in; that may take a while the
first time, as npx downloads the agents.

**Other plugins.** Tested next to each of them, loaded before or after ecdy:

- zsh-syntax-highlighting, zsh-autosuggestions, zsh-vi-mode, fzf-tab: work as they are. Do not set
  fzf-tab's `accept-line` zstyle: that key accepts the line without ecdy's classifier.
- [atuin](https://atuin.sh): load it with `eval "$(atuin init zsh --disable-ai)"`, as atuin binds
  `?` to its AI mode otherwise, which takes ecdy's `?` prefix. atuin records a prompt as the
  `ecdy ask -- '…'` it ran; to keep those out, add to `~/.config/atuin/config.toml`:
  `history_filter = ["^ecdy ask -- "]`.

`ecdy doctor` reports each of these when it finds it.

## Agents

A prompt runs `ecdy ask -- '<prompt>'`, which sends it to the agent and streams the reply. On a
terminal the reply's markdown is rendered as it arrives (headings, emphasis, code, lists, quotes,
links; tables and HTML as they are), and escape sequences in it are removed; piped, the reply is the
agent's text as is. Tool calls appear as one status line each
(`⚙ Read main.go ✓`, `$ go test ./... ✗`); `ecdy ask -v` also shows thoughts, plans, tool output (the
last 20 lines of a command's output, with escape sequences and control characters removed; on a
terminal the last 5 lines while the command runs) and the agent's stderr.

The agent reads files, edits them and runs commands with its own tools and asks for permission its own
way; ecdy shows those requests but does not offer the agent its file system or terminal (ACP's `fs/*`
and `terminal/*`, see [ADR 0005](docs/adr/0005-client-capabilities.md)).

Built-in agents (the launch commands of the [ACP Registry](https://github.com/agentclientprotocol/registry)):

| Name | Command |
|---|---|
| `claude` (default) | `npx -y @agentclientprotocol/claude-agent-acp` |
| `codex` | `npx -y @agentclientprotocol/codex-acp` |
| `gemini` | `gemini --acp` |
| `opencode` | `opencode acp` |
| `pi` | `npx -y pi-acp` |

[pi](https://pi.dev) has no ACP mode of its own: `pi-acp`, the adapter listed in the registry, runs
`pi --mode rpc`, so `pi` (0.81 or newer) must be installed and on `$PATH`. pi runs its tools without
asking, so its commands and edits show up as tool calls but never as permission dialogs, unless a
pi extension asks (the adapter turns an extension's question into one). The adapter prints pi's start-up summary
before the first reply; `"quietStartup": true` in `~/.pi/agent/settings.json` turns it off.

Switch the agent of the current shell with `ecdy use codex` (`ecdy use` prints it), pick one for a
single prompt with `ecdy ask --agent codex -- ...`, or add and override agents in
`~/.config/ecdy/config.toml` (`$XDG_CONFIG_HOME/ecdy/config.toml`):

```toml
default_agent = "codex"
idle_timeout = "30m"             # stop the session's agents after this long without prompts

[context]
commands = 20                    # recent commands sent with a prompt; 0 sends none

[agents.claude]
command = ["claude-agent-acp"]   # installed globally instead of npx
```

ecdy uses the agent's own login: log in with the agent's CLI first (`claude`, `codex login`, …).
If the agent reports that it needs authentication, `ecdy ask` says so and exits.

**Permissions.** Every permission request of the agent is shown under the reply:

```
⚠ Allow? rm -rf build
  1 Allow once 2 Always allow 3 Reject  · Esc reject · ^C cancel
```

Press the option's number, `y` (allow once), `n` or Esc (reject). Enter does nothing: nothing is
allowed by default, and keys typed before the dialog appeared are discarded. Without a terminal
(e.g. in a script) every request is rejected. "Always allow" is remembered by the agent, not by ecdy.

**Ctrl+C** cancels the turn (`session/cancel`) and waits for the agent to stop; a second Ctrl+C
stops the agent at once. Exit status: 0 — the turn completed, 1 — agent or protocol error,
130 — cancelled.

**Conversations.** Every shell that loads the plugin is a session (`ECDY_SESSION`). Its first
prompt starts a small daemon, which keeps the agent running, so later prompts skip the agent's
start-up and continue the same conversation. Each agent keeps its own conversation while the daemon
runs.

- `ecdy new` — the next prompt starts a new conversation (in the directory it is typed in);
- `ecdy use <agent>` — switch the shell's agent;
- `ecdy daemon status` — the daemon, its agents, their directories;
- `ecdy daemon stop` — stop them now (the next prompt starts afresh, with the shell's current
  environment: the agent keeps the environment it was started with).

A conversation keeps the directory it started in; a prompt from outside it says so. The daemon
stops when the shell exits (even if it is killed), after `idle_timeout` without prompts, or with
`ecdy daemon stop`. Its socket lives in `$XDG_RUNTIME_DIR/ecdy/` (mode 0700; `$TMPDIR/ecdy-<uid>/`
without `XDG_RUNTIME_DIR`). `ecdy ask` outside such a shell (no `ECDY_SESSION`, e.g. in a script)
starts the agent for that one prompt.

**Session context.** After every command the plugin records, in the background, the line you
typed, its directory, exit status and duration to `$XDG_STATE_HOME/ecdy/sessions/<session>.jsonl`
(`~/.local/state/…`, mode 0600). A prompt goes to the agent with a short block before it: the
current directory, the git branch and the last `[context] commands` commands (default 20; at most
4 KiB, each command cut to 512 bytes). The next prompt of the same conversation gets only the
commands run since the previous one. `ecdy log` prints the session's log, `ecdy log --context` the
block a new conversation would get here.

- Secrets are replaced by `[REDACTED]` before the log is written and again when the block is
  built: `*_TOKEN=`/`*_KEY=`/`*_PASSWORD=`-style assignments and flags, `Authorization:` and other
  auth headers, passwords in URLs, `curl -u`, `mysql -p`, well-known token formats (GitHub, GitLab,
  Slack, AWS, OpenAI, Anthropic, …), private key blocks. It is a heuristic: keep secrets out of
  command lines when you can.
- Not recorded: prompts, lines starting with a space when `HIST_IGNORE_SPACE` is set, and command
  output (never).
- The log is removed when the shell exits; logs of shells that were killed without cleanup are
  removed after a week.

Set `ECDY_LOG=debug` (or `info`, `warn`, `error`) to log protocol diagnostics to
`$XDG_STATE_HOME/ecdy/ecdy.log` (`~/.local/state/ecdy/ecdy.log`).

## Development

```sh
nix develop               # go, gopls, golangci-lint, zsh, tmux, nodejs
go test -race ./...       # PTY tests need zsh; the devShell also provides the
                          # zsh plugins, atuin and fzf the compatibility tests
                          # load, and tmux for the tests that read the screen
nix develop .#zsh-matrix -c go test ./shell/   # PTY tests on zsh 5.8.1, 5.9 and the latest
golangci-lint run
go run ./cmd/ecdy version
go run ./cmd/ecdy classify --json --first-kind=command -- 'rm everything in tmp except configs'
nix build && ./result/bin/ecdy version
```

Design, invariants and contribution rules (for humans and coding agents alike) are in [AGENTS.md](AGENTS.md).
The plan is in [docs/ROADMAP.md](docs/ROADMAP.md), progress in [docs/STATUS.md](docs/STATUS.md).

## License

[Apache-2.0](LICENSE)
