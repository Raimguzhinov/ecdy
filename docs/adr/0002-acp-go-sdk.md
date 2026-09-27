# 0002. ACP Go SDK: github.com/coder/acp-go-sdk

- Status: accepted
- Date: 2026-09-27
- Milestone: M3

## Context

AGENTS.md section 5 requires exactly one existing ACP SDK instead of hand-written JSON-RPC, chosen
by three criteria: listed on the libraries page of agentclientprotocol.com, the schema version it is
generated from, and whether the repository is alive. Candidates named there:
`github.com/coder/acp-go-sdk`, `github.com/kdlbs/acp-go-sdk`, `github.com/keepmind9/acp-sdk-go`.

State on 2026-09-27 (GitHub API; the page is `docs/libraries/community.mdx` in
`agentclientprotocol/agent-client-protocol`):

| Library | On the libraries page | Schema | Activity |
|---|---|---|---|
| `coder/acp-go-sdk` | yes, first Go entry | generated from the official `schema.json` (+ `schema.unstable.json`), `schema/version` 0.13.5, `ProtocolVersionNumber = 1` | v0.13.5 on 2026-06-02, 238 stars, releases follow the schema |
| `kdlbs/acp-go-sdk` | no | a fork of `coder/acp-go-sdk` | 0 stars, no releases |
| `keepmind9/acp-sdk-go` | no | hand-written | last push 2026-04, 0 stars |

The page also lists other Go libraries (`ironpark/acp-go`, `eino-contrib/acp`, …) that were not
among the candidates; none is more established than `coder/acp-go-sdk`.

## Decision

Use `github.com/coder/acp-go-sdk` v0.13.5. Its API, read from the source of that tag:

- `acp.NewClientSideConnection(client, agentStdin, agentStdout)`; the connection exposes
  `Initialize`, `Authenticate`, `NewSession`, `LoadSession`, `Prompt`, `Cancel`, `SetSessionMode`.
- The `acp.Client` interface has every client method, including `fs/*` and `terminal/*`: ecdy
  implements those as `method not found` and does not declare the capabilities (M6 decides).
- Notifications are processed in order on one goroutine; a response is delivered only after the
  notifications that arrived before it, so a turn's last message chunks are rendered before
  `Prompt` returns. Incoming requests (`session/request_permission`) run on their own goroutines.
- Cancelling the context passed to `Prompt` sends `session/cancel` and returns at once, without the
  stop reason. ecdy therefore sends `Cancel` itself on Ctrl+C and keeps waiting for the
  `cancelled` stop reason, as the protocol asks (`prompt-turn#cancellation`).
- The connection logs through `slog.Default()` unless given a logger. ecdy always sets one: nothing
  may be written to the user's terminal behind its back.

## Consequences

- Types follow the ACP schema, including unstable parts (`Unstable*`), which ecdy does not use.
- Protocol upgrades come as SDK releases: bumping the SDK means reading its changelog and
  re-running the fake-agent tests; the fake agent (`internal/testutil/fakeagent`) is written with
  the same SDK, so wire-level regressions against real agents are caught only by the manual checks
  listed in STATUS.md.
- Pinned by `go.mod`; a change of SDK needs a new ADR.
