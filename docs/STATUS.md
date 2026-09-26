# Status

## Current milestone: M1 — Classifier

### Done

- `internal/classify`: pure `Classify(Input) Result` implementing the rule cascade from AGENTS.md
  section 4 (empty line, `?` prefix, unknown first word → typo `Ask` or `Prompt`, known first word →
  weighted natural-language signals, dangerous commands). Scoring and thresholds are in
  [ADR 0001](adr/0001-classifier-signal-scoring.md).
  - Own lightweight lexer for command positions / arguments / quoting; `mvdan.cc/sh/v3/syntax`
    (`LangZsh`, experimental upstream) only for the "does it parse" signal.
  - File-existence checks go through an injected `fs.StatFS`, at most 16 `Stat` calls per line.
  - `Result` carries `reason`, `score`, `signals`, `prompt`, typo `suggestion`/`correction` and a
    `dangerous` flag (the Ask dialog in M2 must default to the agent).
  - `classify.FirstWord` skips assignments and precommand modifiers (`sudo -u x`, `env -i`, `time`, …);
    the zsh plugin must skip the same words (`precommands` in `internal/classify/words.go`).
- Golden table `internal/classify/testdata/cases.tsv`: 324 cases, including every example from
  AGENTS.md sections 2 and 4, Russian, typos, pipes, heredocs, `sudo`, assignments, zsh syntax,
  dangerous commands with and without signals.
- Tests: golden, result details, "known first word is never `Prompt`" property, dangerous-command
  table, lexer, OSA distance; `FuzzClassify`; `BenchmarkClassify` (~5 µs/op in-process).
- `ecdy classify [--shell=zsh] [--first-kind=KIND|auto] [--cwd DIR] [--json] -- LINE`.
  `auto` (the default, for manual use) guesses the kind from zsh builtin/reserved lists and `$PATH`.
- Cold-start measurement: `ECDY_COLDSTART=1 go test ./cmd/ecdy -run ColdStart -v` (opt-in test
  with a hard p99 < 15 ms check, also run in CI) and `BenchmarkClassifyColdStart`.
- CI: cold-start step and a 30 s fuzz run added to the `go` job. `vendorHash` updated.

### Verified locally (2026-09-27)

- `nix develop -c go test -race ./...` — green; `golangci-lint run` — 0 issues.
- Cold start, 300 execs of the built binary: p50 3.5 ms, p99 5.0 ms (budget 15 ms).
- `go test ./internal/classify -fuzz FuzzClassify -fuzztime 60s` — no failures.
- `nix build`, `nix flake check` — ok.

### Known limitations

- Typo candidates are a built-in list (~150 commands) plus `Config.ExtraCommands`; the shell's
  own command table is not used yet (see ADR 0001, consequences).
- Unquoted English after a harmless known command (`echo this is a test`) is `Ask`, by design.
- `Config` is not loaded from `config.toml` yet (no config package until it is needed).

## Next: M2 — zsh integration

See [ROADMAP.md](ROADMAP.md). Start a new session, read this file, branch `m2-zsh`.
