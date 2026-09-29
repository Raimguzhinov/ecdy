# Roadmap

Work strictly in order. Each milestone is a separate branch and PR. Do not start the next one until the current
milestone's DoD is met and [STATUS.md](STATUS.md) is updated. Design context: [AGENTS.md](../AGENTS.md).

**M0 — Skeleton.** ✅ go.mod, flake.nix devShell, cobra skeleton, `ecdy version`, CI (GitHub Actions: `go test -race ./...`,
`golangci-lint`, `go vet`), LICENSE (Apache-2.0), README stub.
*DoD:* `nix develop -c go test ./...` green, CI green.

**M1 — Classifier.** ✅ `internal/classify` + golden table ≥ 200 cases + fuzz + benchmark + CLI `ecdy classify`
(`--first-kind`, `--json` prints verdict and reason).
*DoD:* the table passes; p99 < 15 ms in the cold-start benchmark; every example from AGENTS.md sections 2 and 4 is in the table.

**M2 — zsh integration.** ✅ `shell/zsh/ecdy.plugin.zsh`, `ecdy init zsh`, accept-line wrapper, `?` prefix, Alt+Enter,
`zshaddhistory`, the `Ask` dialog, fail-open when the binary is missing. At this stage `ecdy ask` is a stub that prints the prompt.
*DoD:* PTY tests (`zsh -f` + plugin) for: a command, a prompt, Ask, fail-open, history recording, coexistence with
zsh-syntax-highlighting and zsh-autosuggestions (loaded in the test from vendored copies or from nix).

**M3 — ACP one-shot.** ✅ `ecdy ask` without a daemon: spawn agent → initialize → session/new → session/prompt → stream → exit.
Permission dialog, Ctrl+C → cancel.
*DoD:* tests against the fake agent from `internal/testutil` (text streaming, tool call, permission allow/reject, cancel, agent crash);
manual check with claude-agent-acp and codex-acp, results recorded in STATUS.md
(codex-acp could not be checked: no network access to its backend here; see STATUS.md).

**M4 — Daemon and continuity.** ✅ One daemon per session (`ECDY_SESSION` is exported by the plugin), lazy start, unix socket 0700,
shutdown on `zshexit` and idle timeout. The conversation continues across prompts. `ecdy new` — a new ACP session,
`ecdy use <agent>` — switch agents.
*DoD:* the second prompt sees the context of the first (test with the fake agent); no orphaned processes after the shell exits (test);
switching agents works.

**M5 — Session context.** ✅ preexec/precmd log, secret redactor (test table), context block in the prompt,
`ecdy log` to view it.
*DoD:* secret redactor tests; the block size is bounded; the agent sees the context (the fake agent checks it).

**M6 — Client capabilities.** ✅ ADR: do we implement `fs/*` and `terminal/*`, or leave the agent its own tools?
If we implement them, `terminal/*` runs commands in a PTY with the user's env and cwd and shows them in the terminal.
Decided in [ADR 0005](adr/0005-client-capabilities.md): not implemented, the agent keeps its own tools; command output
that agents send in `_meta` is shown with `-v`; `pi` preset (pi-acp adapter).
*DoD:* per the ADR: no capabilities declared (test); an agent calling `fs/*`/`terminal/*` anyway gets `method not found`,
nothing is read, written or run, and the turn goes on (test); `_meta` command output shown with `-v`, cleaned of escape
sequences (tests, both `ecdy ask` modes).

**M7 — UX.** ✅ Live classification indicator while typing (`zle-line-pre-redraw` + RPROMPT, only cheap checks on the zsh side),
markdown rendering, `ecdy doctor` (checks PATH, agents, login, zsh version, plugin conflicts).
Decided in [ADR 0006](adr/0006-ux.md): the indicator runs the real classifier in the background (typing never waits);
ecdy's own streaming markdown renderer; also live `-v` command output, the typed line in the scrollback, and
compatibility with zsh-vi-mode, fzf-tab and atuin.
*DoD:* screen tests (tmux) of the indicator and the scrollback on every zsh, incl. fail-open; markdown tests independent
of chunking, fuzzed; `ecdy doctor` tests (offline, the shell through the plugin, agent login with fake agents);
compatibility PTY tests.

**M8 — Other shells.** bash (via ble.sh or `bind -x`), then fish. The classifier is shared; each shell gets its own integration.

**Later (do not touch unless asked):** a small local model for `Ask` cases; learning from the user's corrections;
an aggregator of several agents in one session.
