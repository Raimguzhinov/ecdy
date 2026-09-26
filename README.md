# ecdy

> From *ecdysis* — molting: an arthropod sheds its old shell in order to grow.

**Status: early development (M0 — skeleton). Nothing useful to install yet.**

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

## Planned usage

```sh
eval "$(ecdy init zsh)"   # in ~/.zshrc (not implemented yet)
```

## Development

```sh
nix develop               # go, gopls, golangci-lint, zsh, tmux, nodejs
go test -race ./...
golangci-lint run
go run ./cmd/ecdy version
nix build && ./result/bin/ecdy version
```

Progress is tracked in [docs/STATUS.md](docs/STATUS.md).

## License

[Apache-2.0](LICENSE)
