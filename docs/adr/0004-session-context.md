# 0004. Session context: a redacted command log, sent at every turn

- Status: accepted
- Date: 2026-09-29
- Milestone: M5

## Context

The agent should know what the user just did in the shell: the working directory, the git branch,
the last commands with their exit status and duration (AGENTS.md, section 5, "Session context").
Invariant 4 says secrets are not sent by default and command output is not captured. M5 has to
decide how commands are recorded without slowing down or breaking the shell (invariant 2), where
secrets are removed, where the context block is built, and how it stays bounded.

## Decision

**Recording.** The plugin's `preexec` hook keeps the line as typed (`$1`: continuation lines
included), `$PWD` and `$EPOCHREALTIME`; `precmd` takes `$?` (zsh restores it for every precmd
function, checked on 5.8.1, 5.9 and 5.9.2) and runs `ecdy log record` **in the background**
(`&!`, stdio on `/dev/null`). The prompt never waits for it, so a slow, hung or missing binary
cannot delay the next command; the cost on the shell's side is one fork. The plugin keeps the
recorders' pids and waits for them (up to 0.5 s) in two places only: before it sends a prompt, so
that the agent sees the command typed just before, and in `zshexit`, so that a late record does
not create the log again after it is removed (it kills a recorder still running then). A hung
recorder is forgotten after one such wait. Not recorded: the
`ecdy ask -- ...` lines the plugin runs for prompts (the prompt is already in the conversation),
lines starting with a space when `HIST_IGNORE_SPACE` is set (the user asked to keep them out of the
history), and anything without `ECDY_SESSION`.

**Redaction on write, and again on read.** `ecdy log record` runs the command line through
`sessionlog.Redact` before it touches the disk, so secrets never land in
`$XDG_STATE_HOME/ecdy/sessions/`. The context is redacted again when it is built (cheap, idempotent,
covers a log written by an older ecdy). Redaction happens before truncation: a cut-off secret would
no longer match its pattern.

**The log.** `$XDG_STATE_HOME/ecdy/sessions/<session>.jsonl` (directory 0700, file 0600), one JSON
object per command: line, cwd, exit status, start time, duration. Writers take an `flock` on
`<session>.lock` next to it: background records of fast commands may run concurrently, and the
file is trimmed to its last 200 records when it grows past 256 KiB (atomic rename). Records carry
zsh's timestamps and are read in end-time order, so a late writer does not reorder them. The log is
removed when the session ends (`zshexit`, or the daemon noticing the shell is gone); a log not
written for 7 days is removed when a new session starts its log (shells that were killed with no
daemon to notice).

**The context block is built by the server at every turn**, not by `ecdy ask`: the server knows
the conversation. Each agent's conversation remembers the end time of the last command it was
sent; a turn sends only the commands after it (the first turn of a conversation, the last N). The
block always carries the prompt's cwd and git branch (read from `.git/HEAD`, worktrees included,
without running `git`). It is sent as a separate text content block before the prompt. A prompt
without a session (no `ECDY_SESSION`) has no log and gets no command list.

**Bounds.** N = `[context] commands` in the config (default 20, `0` sends no commands); each
command is cut to 512 bytes; the oldest commands are dropped until the block fits in 4 KiB.

**`ecdy log`** prints the session's records; `ecdy log --context` prints the block the next
prompt of a new conversation would get, so the user can see exactly what the agent sees.

## Consequences

- One background fork per command. A prompt or `exit` right after a command can take a few
  milliseconds longer (up to 0.5 s with a hung recorder, once). Both races (a prompt missing the
  last command, a log written after the session ended) were found by the PTY tests on one CPU.
- Secrets that the redactor does not recognise reach the log and the agent. The redactor's table
  (`internal/sessionlog/testdata/redact.tsv`) is where such cases go.
- Output capture (`tmux capture-pane`) stays off and needs its own ADR.
- The daemon protocol does not change: the context is built on the server side from the session
  it already knows.
