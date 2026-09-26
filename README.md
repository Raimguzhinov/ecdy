# ecdy

> From *ecdysis* — molting: an arthropod sheds its old shell in order to grow.

**Status: early development (M2 — zsh integration). Prompts are not sent to an agent yet: `ecdy ask` only prints them.**

ecdy is a smart layer on top of your real shell. You keep typing in zsh with your own config,
completion, highlighting and history. On Enter, ecdy decides whether the line is a shell command
or a prompt: commands run in zsh as usual, prompts go to any agent that speaks the
[Agent Client Protocol](https://agentclientprotocol.com) (Claude Code, Codex, Gemini CLI, OpenCode, …).

ecdy is not a terminal emulator, not a new shell language, and not an agent or LLM client of its own.
It uses your agents' own logins and subscriptions; there is no ecdy cloud and no telemetry.

## Safety

- A prompt is never executed as a command. When the classifier is unsure, it asks.
- If the `ecdy` binary is missing, crashes or is too slow, zsh behaves exactly like vanilla zsh.
- Every agent permission request is shown to you.

## Usage

Add this to the end of `~/.zshrc`, after plugins that wrap `accept-line`
(zsh-syntax-highlighting and zsh-autosuggestions work in either order):

```sh
eval "$(ecdy init zsh)"
```

Or source `shell/zsh/ecdy.plugin.zsh` from a plugin manager; it looks for `ecdy` in `$PATH`.

On Enter, ecdy classifies the line:

- a **command** runs as usual;
- a **prompt** runs as `ecdy ask -- '<prompt>'`; the history keeps the line you typed;
- when it is **not sure**, a dialog appears under the line and nothing runs until you choose:
  `⏎ agent · r run · e edit · Esc cancel` (plus `f fix` when the first word looks like a typo,
  e.g. `gti status`). Enter sends the line to the agent.

Overrides: start the line with `?` to force a prompt; press **Alt+Enter** to run it as a command
without classification.

The plugin replaces the `accept-line` widget (calling the previous one, if another plugin wrapped
it) and binds Alt+Enter (`^[^M`) in the `emacs` and `viins` keymaps. Settings, read on every Enter:

| Variable | Default | |
|---|---|---|
| `ECDY_BIN` | `ecdy` | the ecdy executable |
| `ECDY_CLASSIFY_TIMEOUT` | `0.5` | seconds to wait for the classifier |

If `ecdy` is missing, crashes, prints something unexpected or misses the deadline, Enter behaves
exactly like in vanilla zsh.

## Development

```sh
nix develop               # go, gopls, golangci-lint, zsh, tmux, nodejs
go test -race ./...       # PTY tests need zsh; the devShell also provides the
                          # zsh plugins the coexistence tests load
golangci-lint run
go run ./cmd/ecdy version
go run ./cmd/ecdy classify --json --first-kind=command -- 'rm everything in tmp except configs'
nix build && ./result/bin/ecdy version
```

Design, invariants and contribution rules (for humans and coding agents alike) are in [AGENTS.md](AGENTS.md).
The plan is in [docs/ROADMAP.md](docs/ROADMAP.md), progress in [docs/STATUS.md](docs/STATUS.md).

## License

[Apache-2.0](LICENSE)
