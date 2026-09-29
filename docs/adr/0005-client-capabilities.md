# 0005. Client capabilities: no `fs/*`, no `terminal/*`; the agent keeps its own tools

- Status: accepted
- Date: 2026-09-29
- Milestone: M6

## Context

ACP lets a client declare `clientCapabilities.fs.readTextFile`, `fs.writeTextFile` and
`terminal` in `initialize`. An agent that sees them may route its file access through
`fs/read_text_file` / `fs/write_text_file` and run commands through `terminal/create` (plus
`output`, `wait_for_exit`, `kill`, `release`) instead of doing it itself. Both default to false.
Since M3 ecdy declares none and answers every such request with `method not found`
(`internal/acpclient`, ADR 0002). ROADMAP.md asks M6 to decide whether to implement them; if so,
`terminal/*` runs commands in a PTY with the user's env and cwd and shows them in the terminal.

The capabilities were designed for editors: `fs/*` lets the agent see unsaved buffers and have
its edits land in them, `terminal/*` lets the editor host the agent's commands in its own
terminal panel. ecdy has no buffers, and the agent runs on the same machine, as the same user,
in the same cwd.

What the built-in presets actually do with the capabilities (read from the code on 2026-09-29):

| Agent | `fs/*` | `terminal/*` |
|---|---|---|
| `claude` — `@agentclientprotocol/claude-agent-acp` 0.84.0 (npm, `dist/`) | never called: `readTextFile`/`writeTextFile` exist only as pass-through methods of the agent class; Read/Write/Edit use Claude Code's own tools | never called; Bash output can be streamed in `tool_call_update._meta.terminal_output` if the client sets `_meta.terminal_output` in its capabilities |
| `codex` — `@agentclientprotocol/codex-acp` 1.13.1 (npm, `dist/index.js`) | never called (only the SDK's schema) | never called; streams command output in `_meta.terminal_output_delta` (the default when the client declares nothing) |
| `gemini` — gemini-cli, `packages/cli/src/acp/acpFileSystemService.ts` | used when declared: reads and writes go through the client, with a local fallback for paths outside the workspace | never called |
| `opencode` — anomalyco/opencode, `packages/opencode/src/acp/permission.ts` | calls `fs/write_text_file` after an edit permission is granted, **without checking the capability**, fire-and-forget (`void`, errors ignored), to push the new content to an editor; the edit tool writes the file itself | never called |
| `pi` — `pi-acp` 0.0.34 (npm, adapter over `pi --mode rpc`), README "Limitations" and `dist/index.js` | never called ("pi reads/writes and executes locally") | never called; puts a `terminal` content block with its own id (no `terminal/create`) and `_meta.terminal_output` in the bash tool call |

So no preset uses `terminal/*` at all, and the only `fs/*` user that depends on the capability
(gemini) works the same without it.

Implementing them would cost:

- `fs/write_text_file` makes ecdy itself write files on the agent's request. Invariant 3 needs a
  permission for that; the agents already ask for their edits through
  `session/request_permission`, so it would be a second prompt or a policy of trusting the
  agent's earlier request, which is open question 4.
- `terminal/*` makes ecdy execute commands on the agent's behalf: a PTY per terminal, output
  buffers with `outputByteLimit` truncated at a character boundary, process groups, kill and
  release, the daemon owning processes that outlive a turn, and drawing their output while
  `ecdy ask` renders the reply. That is the most dangerous code ecdy could have (invariant 3),
  for no agent that would use it today.
- One real benefit of `terminal/*` (commands in the user's current env instead of the env of the
  `ecdy ask` that started the daemon, a known M4 limitation) exists only for agents that call it.

## Decision

**ecdy does not implement `fs/*` or `terminal/*`.** It keeps declaring no `fs` and no
`terminal` capability (set explicitly in `initialize`, not left to the default), and keeps
answering those methods with `method not found`. The agent reads, writes and runs commands with
its own tools, under its own permission requests, which ecdy shows to the user (invariant 3).

**An undeclared call must not break a turn.** opencode sends `fs/write_text_file` regardless:
the error goes back to the agent, nothing is written by ecdy, and the turn continues. The fake
agent gains a step that calls `fs/*` and `terminal/*` unasked, and tests check exactly that.

**Command output in `_meta` is shown with `-v`.** codex and pi put what their commands print in
`tool_call_update._meta.terminal_output_delta` / `terminal_output` (`{"terminal_id", "data"}`,
`data` being the next piece to append in both), and a `terminal` content block in place of text:
without reading `_meta`, `ecdy ask -v` shows nothing of that output. This is a convention of Zed
and the adapters, not the ACP schema, and it needs no capability: codex sends deltas by default,
pi always. ecdy does not declare `_meta.terminal_output` either, so claude-agent-acp keeps
sending the output as text content, which ecdy already shows. The renderer appends the `data` of
either key per tool call, keeps a bounded tail, removes escape sequences and control characters
(the bytes come from a program's terminal output and must not drive the user's terminal), and
prints the last lines under the finished tool call instead of the `terminal <id>` placeholder.
The output is only rendered: it is not stored and never reaches the session log (invariant 4).

**Revisit when** a preset agent (or one users ask for) needs `terminal/*` or `fs/*` to work,
not just to mirror edits into an editor. A new ADR then decides the permission model before any
code executes on the agent's behalf.

## Consequences

- Nothing new in ecdy executes commands or writes files; invariant 3 stays as it is.
- The agent's own permission policy is what the user gets. pi has none: checked through ecdy on
  2026-09-29, `touch` in pi's bash tool ran with no `session/request_permission` (only pi
  extensions' questions become permission requests). The README says so next to the preset.
- The agent's commands run in the agent's environment: the env of the `ecdy ask` that started the
  daemon (STATUS.md, M4 limitations), not the shell's current one.
- `-v` shows command output for every preset; showing it live while the command runs is left to
  M7's rendering work.
- An agent that requires the capabilities to function will fail its tool calls under ecdy; the
  `method not found` error names the method, and how it surfaces is up to the agent.
