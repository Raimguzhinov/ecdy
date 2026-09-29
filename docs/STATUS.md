# Status

## Current milestone: M7 — UX

Decisions: [ADR 0006](adr/0006-ux.md).

### Done

- **Indicator.** While typing, `zle-line-pre-redraw` starts `ecdy classify` in the background
  (process substitution watched with `zle -F -w`, the same arguments as Enter); a classifier for an
  older line is killed. The verdict goes in front of RPS1 (`→ agent`, `? ask`, nothing for a
  command) and the prompt is redrawn only when it changes. `ECDY_INDICATOR=rprompt|var|off`,
  `ECDY_INDICATOR_{PROMPT,ASK,CMD}`, `$ECDY_VERDICT`. Enter still classifies synchronously.
  Found on the way:
  - ZLE shows no right prompt for a line that started without one, even after `reset-prompt`
    (5.8.1–5.9.2): precmd sets an invisible `RPS1='%{%}'` when it is empty.
  - An answer that arrived during the Ask dialog's `read -k` redrew the prompt over it: Enter
    drops the pending classifier.
  - Ctrl+C and Esc (`send-break`) skip `zle-line-finish`: the indicator is also reset in precmd
    and `zle-line-init`, or it stuck to the next prompts.
  - A classifier that writes part of an answer and hangs was left running: it is killed.
  - The indicator's redraw sometimes stopped after the prompt, the line blank until the next key
    (CI, zsh 5.9.2 with fzf-tab; twice locally in `-count=20`). Cause, read in zsh's source and
    reproduced: zsh's signal handlers do not restart system calls (`sa_flags = 0`), so a SIGCHLD
    arriving as ZLE writes makes the write fail, and the rest stays in its stdio buffer. The
    classifier exits right after its answer, i.e. right as the redraw starts. The redraw now waits
    until the classifier is a zombie (its state read from `/proc`, no fork: zsh defers reaping
    inside a widget, so `kill -0` cannot tell; without `/proc` a 10 ms nap); a cancelled one too.
    `TestIndicatorSignals` (a classifier sending SIGCHLD until it exits, a 40 KB invisible prompt
    to make each redraw long): 6 of 20 blank before, 0 after.
- **Markdown** (`internal/render/markdown.go`), own streaming renderer, no dependency (glamour
  renders whole documents only). Headings, emphasis (CommonMark flanking rules), code spans,
  fenced code, bullet and task lists, quotes, rules, links, escapes; the rest as it is. Holds back
  at most the rest of a line; an opener counts only if its closer follows on the line (so
  `$((6*7))` keeps its `*`: the PTY tests caught the first version eating it). Escape sequences and
  controls are removed from the agent's text on a terminal; a pipe gets the raw text.
- **Live command output** with `-v` on a terminal: the last 5 lines of a running command's
  `_meta` output under its status line, redrawn in place, each cut to the terminal width (`fit`:
  tabs, East Asian wide characters); erased before anything else is printed. The open status line
  is cut to the width too.
- **Scrollback**: `zle-line-finish` writes the typed line over the rewritten `ecdy ask -- '…'`
  (`CURSOR=0; zle -R`, then `ESC 7` line `ESC [J` `ESC 8`); the history already had it (M2).
- **`ecdy doctor`** (`internal/doctor`, pure checks): the ecdy the plugin runs, the config, each
  agent's command on `$PATH` (pi-acp also needs `pi`), zsh ≥ 5.8, and through the plugin's `ecdy`
  function (`ECDY_DOCTOR_ZSH`) the shell: accept-line and Enter, Alt+Enter, `?` (atuin's AI), the
  pre-redraw hook, atuin's history filter, fzf-tab's `accept-line`. `--agents` / `--agent NAME`
  start agents for `initialize` + `session/new` in parallel with `--timeout` (default 1m).
  Exit 1 on a failed check.
- **Compatibility** (PTY, every zsh, each plugin loaded before and after ecdy): zsh-vi-mode 0.12.0,
  fzf-tab 1.3.0, atuin 18.x (`--disable-ai`, `history_filter`): commands, prompts, the indicator,
  the Ask dialog, Alt+Enter, history. Found: atuin binds `?` to its AI mode (takes ecdy's prefix)
  and records prompts as `ecdy ask -- '…'`; fzf-tab's optional `accept-line` key accepts through
  `.accept-line`, skipping the classifier. doctor reports all three; README says what to do.
- Fixed on the way:
  - `clean` let an escape sequence swallow the newline after it (found by `FuzzMarkdown`).
  - `acpclient`: an agent that exited before `initialize` was written could be reported as
    `initialize: Internal error: write |1: broken pipe` instead of its exit (20 of 50 runs on one
    CPU): a failed request now waits up to 200 ms for the agent to be gone.
  - The second and later permission dialogs of a turn lost their first key (found in use: `1`
    had to be pressed twice). `tty` took the descriptor with `File.Fd()`, which switches it to
    blocking mode, so `Close` no longer ended the reader goroutine's pending `read`, and that
    goroutine swallowed the next dialog's first key. The descriptor is now taken with
    `SyscallConn().Control`. `TestAskPermissionTwice` (two dialogs in one turn) hung before.
- Tests: markdown table (68 cases) split at every byte and in random pieces, flush, `FuzzMarkdown`
  (chunk independence, UTF-8, no controls, line count); live output and `fit` tables; `ecdy ask`
  markdown and live output end to end in both modes; tmux screen tests (`shell/screen_test.go`,
  what the terminal shows): scrollback incl. a wrapped rewritten line, indicator verdicts, custom
  strings, `var`, `off` (no background classifier), Esc/Ctrl+C, fail-open (missing, hung, partial
  answer; hung classifiers killed); compatibility; doctor unit table, `ecdy doctor` with fake agents
  (ok, auth, silent, crash), `ecdy doctor` in zsh (healthy, accept-line reset, Alt+Enter taken,
  atuin with AI, fzf-tab accept-line). devShell: zsh-vi-mode, fzf-tab, atuin, fzf.
- Mutation check: about 60 deliberate breakages of the new code, each alone with `-timeout=60s`.
  Survivors got tests (tail of the live view, a cut character at flush, an escaped delimiter, the
  indicator on an accepted line, `off`, a partial answer); one removal that survived — dropping the
  pending classifier on Enter — was a real bug that a later test caught. Left: one equivalent
  mutant (redrawing a command's line over itself) and one of speed only (a zombie not recognized:
  every redraw waits the full 0.1 s).

- **CI flakes** (every red run in the history has a found cause): `TestAgentCrashStderrHeld`'s
  holder ran in the agent's process group until setsid and was killed with it when the prompt
  made the agent exit (a vacuous pass, a 1.7 ms "drain", or a pid written into TempDir during its
  removal); the test now waits for the holder before the prompt and checks that it is alive
  after. The daemon freed the agent after telling the client the turn ended, so a prompt right
  after an answer could be refused as busy (`TestBackToBack`). acp-go-sdk starts reading in its
  constructor, before it stores its own fields and before `SetLogger`: the fake agent (and, in
  theory, `acpclient`) raced with it, reported only in the fake agent's stderr; reading is gated
  until the connection is set up (a full `-race` run with `GORACE=log_path`, children included: 0
  reports). A terminal's close killed the shell only: what it left in the background (atuin's
  `(atuin history end ... &)`) wrote into `$HOME` while it was removed and kept the PTY open; the
  PTY's whole session is killed now (`TestCloseKillsSession`).

### Verified locally (2026-09-29)

- `nix develop .#zsh-matrix -c go test -race ./...` — green (2:02); `golangci-lint run` — 0 issues;
  `nix build` (`ecdy c80713a`), `nix flake check` — ok. No `ecdy daemon`, agent or classifier left
  after any run.
- `-race -count=20 ./shell/` on zsh 5.8.1, 5.9, 5.9.2, as two runs of `-count=10` (7 min each): one
  failure in each of two runs out of three attempts (the first one's log was lost to a `| tail`,
  the second was `TestCompat/zsh-5.8.1/atuin/before`: the indicator's redraw printed the prompt but
  not the line, see limitations), one attempt green. Not reproduced since: `TestCompat` `-race
  -count=40` on the matrix (720 subtests) and on one CPU `-count=8` with `TestIndicator` — green.
- One CPU (`taskset -c 0`, `-count=3`): `./shell/` (2:08), `./cmd/ecdy/ ./internal/acpclient/
  ./internal/daemon/` — green after the acpclient fix above.
- `FuzzMarkdown` 45 s + 40 s (~700 k inputs each), `FuzzClean` 30 s — green.
- Manual check with claude-agent-acp (npx) in tmux: a markdown reply rendered as it streamed
  (heading, bullets, bold, inline code, a go block); the scrollback showed the typed line;
  `ecdy doctor --agent claude`: every shell check ✓, `login claude: started, session created`.

### Known limitations

- Any other signal arriving as ZLE redraws (a job of the user's ending, say) can still leave the
  rest of the line blank until the next key: that is zsh's behaviour, not ecdy's; the plugin only
  keeps its own children out of its redraws.
- zsh hides RPROMPT when the line reaches it: no indicator on long lines.
- `zle reset-prompt` re-expands the user's prompt at each verdict change (costly with slow prompts;
  `ECDY_INDICATOR=off` or `var`).
- A line dropped by Ctrl+C keeps its indicator on the screen (zsh runs no hook for it).
- Markdown: a subset; emphasis and code spans do not cross lines; after an opener the rest of
  the line waits for its closer; no syntax highlighting, no reflow, tables as they are.
- Live output: a terminal resize during a command may leave a stale row.
- The typed line in the scrollback has no syntax highlighting; a rewritten line that wrapped may
  leave a blank row.
- doctor cannot tell whether a widget that wraps accept-line calls ecdy's; it says so.
- In a shell with the plugin `ecdy` is a function; `command ecdy doctor` skips the shell checks.

## Done: M6 — client capabilities

### Done

- ADR 0005: ecdy does not implement `fs/*` or `terminal/*`; the agent keeps its own tools. Read
  from the code of every preset (2026-09-29): claude-agent-acp 0.84.0 and codex-acp 1.13.1 never
  call them, gemini-cli uses `fs/*` only when declared and works the same without it, opencode
  sends `fs/write_text_file` after an edit without checking the capability (fire-and-forget, to
  mirror the edit into an editor), pi-acp calls neither. None uses `terminal/*`.
- `acpclient`: `initialize` sets `fs` and `terminal` to false explicitly; the methods still
  answer `method not found`.
- `pi` preset: `npx -y pi-acp`, the ACP Registry's adapter for pi (pi.dev), which runs
  `pi --mode rpc` (pi 0.81+ on `$PATH`). README: pi runs its tools without permission requests
  (only pi extensions' questions become dialogs); `quietStartup` hides the adapter's start-up
  summary. `TestPresets` pins every built-in launch command.
- `render`: with `-v`, command output that codex-acp and pi-acp send in
  `tool_call_update._meta.terminal_output_delta` / `terminal_output` is appended per tool call
  (a 64 KiB tail) and its last 20 lines are printed under the finished tool call
  (`… N earlier lines` above them) instead of the `terminal <id>` placeholder. `clean` removes
  escape sequences (CSI, OSC/DCS/SOS/PM/APC up to BEL or ST, two-byte ones, unfinished ones),
  C0/DEL/C1 controls, keeps what follows the last `\r` of a line, replaces invalid UTF-8; tool
  output text (claude's command output) is cleaned the same way.
- Fake agent: `Call` (calls `fs/read_text_file`, `fs/write_text_file` or `terminal/create`
  unasked and reports the error code), `TerminalOutput` (`_meta` output), `Tool.Terminal`
  (terminal content block).
- Tests: no capabilities in `initialize`; undeclared calls get `-32601`, nothing written or run,
  the turn ends normally; render table (delta and full keys, placeholder gone, no output without
  `-v`, malformed `_meta`, escapes incl. one split across pieces, `\r`, invalid UTF-8, text
  content), last lines shown, bounded tail with the line count right after 100 000 lines, a cut
  inside a character; `FuzzClean` (valid UTF-8, no controls, idempotent; 30 s, 1.17 M inputs);
  `ecdy ask -v` end to end in both modes (the daemon passes `_meta` through).
- Mutation check: 10 breakages, each alone with `-timeout=60s`, all caught: capability declared,
  write carried out, only one `_meta` key read, no cleaning, head instead of tail, unbounded
  buffer, partial first line kept, OSC ended only by BEL, no `\r` handling, `_meta` ignored.

### Verified locally (2026-09-29)

- `nix develop .#zsh-matrix -c go test -race ./...` — green (1:50); `golangci-lint run` — 0
  issues; `nix build` (`ecdy ffdd355`), `nix flake check` — ok. No `ecdy daemon` or agent left.
  `shell/` and timing-sensitive code were not touched: no `-count=20` or one-CPU runs.
- Manual check with pi 0.87.1 through `pi-acp` 0.0.34 (`npx`): a reply (exit 0); a `touch` in pi's
  bash tool ran with no permission request; `ecdy ask -v` showed a `printf` with colours as
  `│ red` / `│ second line`, no escape sequences left (`cat -v`).
- Not verified live: codex (no network route from this machine, see M3), gemini, opencode.

### Known limitations

- Command output is shown after the command finishes, not while it runs (M7).
- The agent's own text is printed as is; only tool output is cleaned (markdown rendering, M7).
- pi has no permission requests: with the `pi` preset, the agent's commands and edits run
  without a dialog.
- `FuzzClean` is not in CI (as `FuzzRedact`); its seed corpus runs as a unit test.

## Done: M5 — session context

### Done

- ADR 0004: how commands are recorded, where secrets are removed, where the context block is
  built and how it is bounded.
- `internal/sessionlog`:
  - `Redact`: secrets → `[REDACTED]`, the text around them kept: private key blocks, known token
    formats (GitHub, GitLab, Slack, AWS, Anthropic/OpenAI, Google, Stripe, npm, HF, Tailscale,
    JWT), auth headers (`Authorization`, `Cookie`, `X-*-Key/Token`), URL passwords,
    `NAME=value` / `--name[= ]value` / `"name": "value"` / `?name=value` when a word of the name
    is secret (`key`, `token`, `secret`, `password`, …) and the last word does not say it is a
    path, URL, id… (`_FILE`, `_URL`, `-stdin`), `curl -u`, `mysql -p`, `sshpass -p`,
    `docker login -p`. Values that start with `$` are kept. Golden table
    `testdata/redact.tsv` (152 cases, written first), multi-line cases, `FuzzRedact`
    (idempotent, UTF-8, line count), benchmarks with pathological inputs (linear).
  - `Log`: `<session>.jsonl` in `$XDG_STATE_HOME/ecdy/sessions` (0700/0600), records redacted on
    write, `flock` on `<session>.lock` with a 2 s timeout, trimmed past 256 KiB (last 200
    records), logs idle for 7 days pruned when a new one is created, read in end-time order.
  - `BuildContext`: cwd, git branch (`.git/HEAD`, worktrees, no `git` process), the last N
    commands after a given time, each redacted and then cut to 512 bytes, oldest dropped to fit
    4 KiB.
- Daemon: `Options.Context`; every turn sends the block as a text block before the prompt; each
  conversation remembers the end of the last command it was sent (reset by a new ACP session).
  `acpclient.Prompt` takes several text blocks. `RunOptions.EndSession` removes the log when the
  shell is gone.
- `ecdy log` (`--json`, `--context`), hidden `ecdy log record`; `daemon stop --end-session` also
  removes the log. Config: `[context] commands` (default 20, 0 = none, at most 1000).
- Plugin: `preexec` keeps the line, `$PWD`, `$EPOCHREALTIME`; `precmd` takes `$?` first and runs
  `ecdy log record` in the background (`&!`). Not recorded: prompts, lines starting with a space
  under `HIST_IGNORE_SPACE`. Before a prompt and in `zshexit` the plugin waits (up to 0.5 s) for
  the recorders still running; `zshexit` kills the ones left; a hung one is forgotten after one wait.
- Fixed on the way:
  - `zshexit` ran in subshells: `(cd x; exit 1)` stopped the daemon and forgot the agent (M4).
  - `zselect -t` returns 1 on a timeout, so `_ecdy_stop` waited 10 ms instead of up to 0.2 s for a
    killed classifier (M2). `_ecdy_nap` replaces the pattern.
  - GitHub push protection rejected a fake Slack token in the table; the fake tokens are shaped
    so that it does not take them for real ones.
- Fake agent: `Echo`/`History` use the last text block (the prompt); `fakeagent.Prompts` returns
  every prompt's text blocks.
- Tests: redactor table, fuzz and bench; log (modes, redaction on disk, order, torn lines, trim,
  60 concurrent writers, lock timeout 0 and 200 ms, remove, prune); context (format, since, limit,
  0 commands, 4 KiB bound with Cyrillic, redaction before cutting, git branch incl. worktrees);
  config; `epoch` (zsh prints `$EPOCHREALTIME` with 10 fraction digits); `ecdy log`; daemon
  `TestContext` (block before the prompt, since per conversation, reset by `ecdy new`, empty
  block not sent); PTY on every zsh: records with `$?` behind another precmd hook, the agent's
  context on the first and second prompt (no wait in the test: the plugin waits), no job notices,
  `HIST_IGNORE_SPACE`, a hung recorder (prompt waits once, exit kills it), the log removed on
  `exit` and on SIGKILL, a subshell `exit` mid-conversation.
- Mutation check: 46 deliberate breakages (12 of the redactor, 34 of the log, context, daemon
  and plugin), each run alone with `-timeout=60s`; the recorder waits on one CPU, where their
  races show. 4 survived at first: a redundant check (removed), a curl rule and an anchor without
  a table row (rows added; the anchor was hiding `helm --set db.password=…`, now redacted), and a
  final cut the bounded head made unreachable (removed). Mutations that did not compile were
  redone. All caught now.

### Verified locally (2026-09-29)

- `nix develop .#zsh-matrix -c go test -race ./...` — green (1:49);
  `go test -race -count=20 ./shell/` on zsh 5.8.1, 5.9, 5.9.2 — green (5:38);
  on one CPU (`taskset -c 0`, `-count=3`) `./shell/ ./internal/daemon/ ./internal/sessionlog/
  ./cmd/ecdy/` — green (1:35) after fixing the two races it found (see above);
  `golangci-lint run` — 0 issues; `nix build`, `nix flake check` — ok. No `ecdy daemon`, agent or
  recorder left after any run.
- Manual check with claude-agent-acp (npx) in zsh through the plugin:
  `export DEMO_API_KEY=hunter2secret`, `(exit 7)`, then a prompt asking for both: "Your last
  command exited with status 7, and the value of `DEMO_API_KEY` was redacted, so I can't see it."
  `ecdy log --context` showed the same block; after `exit` no daemon, agent or log was left.

### Known limitations

- The redactor is a heuristic: a secret in an unrecognised form (a bare password argument, a
  custom flag with an unusual name) reaches the log and the agent.
- Command output is never captured (a separate ADR if it is ever wanted).
- A prompt or `exit` right after a command may wait for its recorder (milliseconds; 0.5 s once
  with a hung one).
- A shell killed before its first prompt has no daemon to remove its log: the log stays until
  the next session starts a log and prunes the ones idle for a week.
- The prompt's `ecdy ask -- ...` line is not recorded, but a manually typed `ecdy ask` is.
- Without `ECDY_SESSION` (scripts) the block has only cwd and git branch.

## Done: M4 — daemon and continuity

### Done

- ADR 0003: one daemon per shell session keeps the agents alive (answers open questions 1 and 2).
- `internal/daemon`:
  - `Server`: one agent process per agent name, its ACP session is the conversation. The agent is
    started at its first prompt (`acpclient.Start` in the prompt's cwd); `ecdy new` bumps a
    generation in the session state and the next prompt runs `session/new` on the same process;
    an agent that died between prompts is restarted with a notice. One turn per agent at a time
    (`busy`). A client that disconnects mid-turn cancels it; if the turn has not ended after
    `CancelGrace` (5 s) the agent is killed. The turn is marked ended before the last message is
    sent, so a client closing right after it is not mistaken for one that left.
  - Protocol (`proto.go`): one request per connection, newline-delimited JSON, ACP SDK types as
    payloads, protocol version (a mismatch makes `ecdy ask` stop the old daemon and start a new
    one). Updates and permission requests reach the client in the agent's order; a withdrawn
    request closes the dialog at once, not at the end of the turn.
  - `Run`: `$XDG_RUNTIME_DIR/ecdy` (or `$TMPDIR/ecdy-<uid>`), checked 0700, owned, not a symlink;
    `flock` on the directory while checking for a live daemon and binding; socket 0600, removed
    only if it is still ours. Stops on `stop`, SIGTERM/SIGINT/SIGHUP, idle timeout (`idle_timeout`,
    default 30m, `0s` = right after the last client, at least 10 s for the first client), and
    when `ECDY_SHELL_PID` is gone (checked every 500 ms; then the state file is removed too).
  - Client: `Connect` spawns `ecdy daemon` detached (setsid, stdio /dev/null, cwd /) and waits up
    to 5 s for a readiness line on fd 3; `Stop`, `GetStatus`, `Turn`.
- `ecdy ask` is a client of that server: the session's daemon with `ECDY_SESSION`, an in-process
  server over `net.Pipe` without it (M3 behaviour, same code path). Second Ctrl+C → `kill`.
- `ecdy daemon` (hidden `--ready-fd`), `ecdy daemon stop [--no-wait]`, `ecdy daemon status`,
  `ecdy use [AGENT]`, `ecdy new`. `use`/`new` only rewrite `<session>.state` (atomically).
- `acpclient`: `NewSession`, `Session`, `Done`. `config`: `idle_timeout`.
- Plugin: exports `ECDY_SESSION` (`<pid>-<time>`, fresh in every shell, nested ones too) and
  `ECDY_SHELL_PID`; `zshexit` runs `ecdy daemon stop --no-wait --end-session`.
- Fake agent: history per session, unique session ids, pid in the record, per-turn cancel,
  `WithdrawMS`, `ExitDelayMS`. `testutil.Alive`, `Term.Output`, `Term.Pid`.
- Tests:
  - every M3 `ecdy ask` test runs in both modes (one-shot and daemon); after each, no agent
    process is left;
  - `cmd/ecdy/daemon_test.go`: continuity (agent started once), one-shot forgets, `new`, `use` and
    `--agent` (each agent keeps its conversation), busy, client killed mid-turn, agent died
    between prompts, cwd notice, `daemon stop` (waits for a slow agent), shell gone (daemon,
    agents and state file), idle timeout `0s` and `5s`, protocol version mismatch, unsafe runtime
    directory;
  - `internal/daemon`: turns continue, stderr of verbose turns, client gone with `CancelGrace`
    0 / 300 ms / an agent that stops, permission answer / interrupt / withdraw by cancel /
    withdraw by the agent, busy, protocol version, silent client, oversized message, idle timer
    0 and 50 ms, runtime directory and state files;
  - `shell/session_test.go` (PTY, zsh): a conversation across prompts with `ecdy new` and
    `ecdy use`; no daemon or agent left after `exit` (zshexit only: pid watching off) or SIGKILL
    of the shell; session variables in children and nested shells. Every test shell's daemons
    are stopped in its cleanup.
  - 19 mutations of the code, each caught by the tests above (one survived at first — `stop`
    returning without waiting; the fake agent now exits slowly in that test).

### Verified locally (2026-09-28)

- `nix develop -c go test -race ./cmd/ecdy/ ./internal/...` — green (1:46);
  `nix develop .#zsh-matrix -c go test -race -count=20 ./shell/` (zsh 5.8.1, 5.9, 5.9.2) — green (3:00);
  on one CPU (`taskset -c 0`): `./shell/ ./internal/daemon/` ×3 and `./cmd/ecdy/` — green after
  fixing `TestPermissionWithdrawnByAgent`, which assumed an order that one CPU does not keep;
  `golangci-lint run` — 0 issues; `nix build` (`ecdy e9713a9`), `nix flake check` — ok.
  No `ecdy daemon` or agent process left after any run.
- Manual check with claude-agent-acp (npx) through the daemon: "remember 7481" (4 s, agent
  start-up included), then "what number?" → `7481` (1 s); `ecdy daemon status` showed the agent
  and its ACP session; `ecdy daemon stop` left no process.
- Mutation check: 19 deliberate breakages of the new code, each run alone with
  `go test -timeout=60s`; all caught.

### Known limitations

- The agent keeps the environment of the `ecdy ask` that started the daemon.
- A shell replaced by `exec` keeps its pid and skips zshexit: only the idle timeout stops its daemon.
- `session/load` is not used: the conversation ends with the daemon (idle timeout, shell exit).
- Two concurrent `ecdy use`/`ecdy new` may race on the state file (last write wins).

## Done: M3 — ACP one-shot

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

- One prompt = one agent process and one session (fixed in M4).
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

## After M7: fixes

- **The rewritten line flashed after Enter** (seen in a screen recording at 60 fps: two frames of
  a highlighted `ecdy ask -- '...'` before the typed line was written over it). The plugin now
  holds the screen with synchronized output (DEC mode 2026) from the rewrite to the end of
  `zle-line-finish` ([ADR 0006](adr/0006-ux.md)); terminals without the mode are unchanged.
  `TestSyncOutput`: red before the fix, green on zsh 5.8.1, 5.9 and 5.9.2.

## Next: M8 — other shells

See [ROADMAP.md](ROADMAP.md). Start a new session, read this file, branch `m8-shells`: bash
(ble.sh or `bind -x`), then fish; the classifier is shared, each shell gets its own integration.
