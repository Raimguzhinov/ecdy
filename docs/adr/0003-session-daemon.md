# 0003. One daemon per shell session keeps the agent alive

- Status: accepted
- Date: 2026-09-28
- Milestone: M4

## Context

In M3 every prompt started the agent anew: no conversation across prompts, and `npx` start-up
(~10 s for claude-agent-acp) on every prompt. M4 must continue the conversation, start the daemon
lazily, stop it with the shell (`zshexit`) and after an idle timeout, and add `ecdy new` and
`ecdy use <agent>` (ROADMAP.md).

Open question 2 (AGENTS.md, section 9) asks whether a daemon is needed at all when the agent
supports `session/load`. Checked against the protocol
(https://agentclientprotocol.com/protocol/session-setup#loading-sessions): `session/load` is an
optional capability (`loadSession`); the agent replays the whole conversation as `session/update`
notifications before answering, and still needs a fresh process, i.e. the `npx` start-up on every
prompt. Not every agent declares it.

Open question 1 (a new cwd in the middle of a session) has to be answered for M4 as well: a
session's cwd is fixed by `session/new`.

## Decision

**A daemon per shell session.** The plugin exports `ECDY_SESSION` (a fresh id per interactive
shell: `<pid>-<time>`) and `ECDY_SHELL_PID`. `ecdy ask` with `ECDY_SESSION` set talks to the
session's daemon over `$XDG_RUNTIME_DIR/ecdy/<session>.sock`; without it (scripts, other shells),
it runs the same server in-process for one prompt, as in M3. `session/load` is not used: the
daemon keeps the agent process, so no start-up and no replay after the first prompt. It may be used
later to survive an idle timeout; that needs a way to hide the replay.

**Lazy start.** `ecdy ask` connects; if nothing listens, it starts `ecdy daemon` (new session id
of the OS: `setsid`, stdio on `/dev/null`, cwd `/`, the environment of that `ecdy ask`) and waits
up to 5 s for a readiness line on an inherited pipe. The daemon takes an exclusive `flock` on the
runtime directory while it checks for a live daemon (a successful `connect`) and binds, so two
daemons for one session cannot both listen.

**Runtime files.** `$XDG_RUNTIME_DIR/ecdy/` with mode 0700 (checked: owned by the user, no group
or other bits, not a symlink); without `XDG_RUNTIME_DIR` (macOS), `$TMPDIR/ecdy-<uid>/` with the
same checks (invariant 5). The socket is 0600. `<session>.state` (JSON) holds the session's chosen
agent (`ecdy use`) and a generation counter (`ecdy new`); both commands only rewrite that file
(atomically), so they work whether or not the daemon runs, and the daemon reads it at every
prompt. The daemon removes the state file when it stops because the shell exited.

**Protocol.** One connection per request, newline-delimited JSON messages (`internal/daemon`,
`proto.go`) with a protocol version; the payloads are the ACP SDK's types (`SessionUpdate`,
`RequestPermissionRequest`, …). It is not ACP itself: the daemon owns the ACP session, and
`use`/`new`/`stop` are not ACP methods. A version mismatch (ecdy upgraded while a daemon runs)
makes `ecdy ask` stop the old daemon and start a new one.

**Where things happen.** The daemon talks ACP to the agents; `ecdy ask` keeps everything that needs
the terminal: rendering, the permission dialogs (on `/dev/tty` of the `ecdy ask` process), Ctrl+C.
Updates and permission requests reach `ecdy ask` in the order the agent sent them.

**Agents.** The daemon keeps one agent process per agent name, started at its first prompt: each
agent has its own conversation in the shell session. `ecdy use codex` makes codex the session's
agent; `ecdy ask --agent codex` sends one prompt to codex without changing it. `ecdy new` starts a
new ACP session (`session/new` on the running agent, no restart) at the next prompt of each agent.
One turn per agent at a time: a second prompt to a busy agent fails at once ("busy"), it does not
queue.

**Cancel and disconnects.** The first Ctrl+C sends `cancel` (the daemon sends `session/cancel` and
waits for the stop reason); the second sends `kill`, which kills the agent's process group: its
conversation is lost, the next prompt starts it again. If `ecdy ask` disappears in the middle of a
turn, the daemon cancels the turn and kills the agent if it has not stopped within 5 s.

**Stopping.** The daemon exits, closing its agents (stdin EOF, SIGTERM, SIGKILL after 2 s):
- on `ecdy daemon stop`, which the plugin's `zshexit` hook runs (`--no-wait`, so exiting the shell
  never waits for the agents);
- when the shell process (`ECDY_SHELL_PID`) is gone: checked every 500 ms, for shells that die
  without `zshexit` (SIGKILL, a closed terminal);
- after `idle_timeout` (config, default `30m`) without a connected `ecdy ask`; `0s` stops it as soon
  as the last client disconnects;
- on SIGTERM, SIGINT or SIGHUP.

**The cwd of a session (open question 1).** The session keeps the cwd of the prompt that created
it. A prompt from a directory outside it continues the same conversation, and `ecdy ask` prints a
notice: `this conversation runs in <dir>; ecdy new starts one here`. No automatic new session: it
would silently drop the conversation.

## Consequences

- The second and later prompts skip the agent's start-up and continue the conversation.
- The agent's environment is the one of the `ecdy ask` that started the daemon: variables exported
  later in the shell do not reach it until the daemon restarts (`ecdy daemon stop`).
- The conversation ends with the daemon: shell exit, idle timeout, a killed agent.
- A shell replaced by `exec` keeps its pid and skips `zshexit`: only the idle timeout stops its
  daemon.
- Programs started from the shell inherit `ECDY_SESSION`, so `ecdy ask` in a script joins the
  shell's conversation. To run one-shot, unset it: `ECDY_SESSION= ecdy ask …`.
- A second daemon cannot bind the socket, but a `<session>.state` write may race between two
  concurrent `ecdy use`/`ecdy new` calls; the last write wins.
