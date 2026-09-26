# 0001. Classifier: scoring natural-language signals

- Status: accepted
- Date: 2026-09-27
- Milestone: M1

## Context

AGENTS.md section 4 defines the rule cascade but leaves two things open:

- Rule 4 says "no signals → `Cmd`, strong signals → `Ask`" without saying what makes signals strong.
  Treating every signal equally is too eager: `make all`, `docker compose up`, `for f in *.go` and
  `nix develop -c go test ./...` all contain a stop word or three word-like arguments, and asking
  on them would make ecdy unusable.
- Rule 3 needs a list of "known commands" for typo suggestions, but the classifier has no access to
  the shell's command table.

Invariant 1 (a prompt is never executed) pushes towards `Ask`; usability pushes towards `Cmd`.

## Decision

**Weighted signals with a threshold.** Arguments of every simple command in the line (not command
words after `|`, `;`, `&&`, not redirection targets, not assignments, not quoted or expanded words)
are scored:

| Signal | Weight |
|---|---|
| strong stop word (`the`, `this`, `it`, `why`, `please`, `except`, …, most Russian function words) | 2 per distinct word |
| weak stop word (`a`, `all`, `and`, `in`, `to`, `for`, `with`, …) | 1 per distinct word |
| word containing non-ASCII letters (Cyrillic etc.) | 2, once |
| contraction or possessive with an unmatched `'` (`isn't`, `repo's`) | 2 per word |
| `?` at the end of the line after a word | 2 |
| ≥ 3 word-like arguments | 1 (another 1 at ≥ 6) |
| the line fails to parse as zsh (not merely incomplete) | 1 |

A word-like argument is a plain word made of letters (plus inner `-`/`'`) that is not a flag, path,
glob, number or an existing file in the current directory (≤ 16 `Stat` calls per line; past the
limit words are assumed not to be files, which can only move the verdict towards `Ask`).

- Known first word: score ≥ 2 → `Ask`, otherwise `Cmd`.
- Dangerous command (rule 5): any signal other than the word count → `Ask` with `dangerous: true`.
  The word count is excluded because subcommands and refs look like words
  (`git push --force origin main`).
- A reserved word first (`for`, `if`, `while`, …) with a line that parses as valid zsh → `Cmd`:
  compound commands are unambiguous shell syntax.
- Unknown first word (rule 3): a typo suggestion is offered only when the rest scores < 2;
  a first word that is exactly a known command name (just not installed) → `Cmd`, so zsh reports
  "command not found".

**Typo candidates** are a built-in list of ~150 common commands plus `Config.ExtraCommands`.
Words of two letters match only by transposition (`sl` → `ls`); longer words match within an
optimal-string-alignment Damerau–Levenshtein distance of 1. Stop words and prompt starters
(`explain`, `fix`, `help`, `lets`, …) are never treated as typos.

**Syntax check** uses `mvdan.cc/sh/v3/syntax` with `LangZsh` (experimental upstream). Because of
that, and because incomplete input is legitimate (zsh shows a continuation prompt), a parse error
is only a weak signal and incomplete input is no signal at all.

## Consequences

- Single weak signals never cause `Ask`; a single strong one does. `ls the` asks, `make all` does not.
- Unquoted English in arguments of a known command asks even for harmless commands
  (`echo this is a test`); the user presses `r`. This is the cheap error in invariant 1.
- Typo suggestions are limited to the built-in list until the zsh plugin can pass the shell's own
  command names cheaply (revisit in M2/M7 after measuring).
- The weights live in `internal/classify/classify.go`; every change must come with golden-table rows
  (`internal/classify/testdata/cases.tsv`) that motivate it.
