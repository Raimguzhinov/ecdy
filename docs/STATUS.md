# Status

## Current milestone: M0 — Skeleton

### Done

- `go.mod` (`github.com/Raimguzhinov/ecdy`), cobra CLI skeleton, `ecdy version`
  (ldflags `-X main.version=…`, falls back to module version / VCS revision / `dev`) with tests.
- `flake.nix`: devShell (go, gopls, golangci-lint, zsh, tmux, nodejs), `packages.default`
  (`buildGoModule`, version from the flake's git revision), `formatter` (nixfmt).
- `.golangci.yml` (v2 config), GitHub Actions CI: `go vet`, `go test -race`, `golangci-lint`,
  plus a Nix job (`nix flake check`, `nix build`, `nix develop -c go test ./...`).
- LICENSE (Apache-2.0), README stub.

### Verified locally (2026-09-27)

- `nix develop -c go test -race ./...` — green.
- `nix develop -c golangci-lint run` — 0 issues.
- `nix flake check`, `nix build && ./result/bin/ecdy version` — ok.

### CI

- PR #1: `go` and `nix` jobs green (run 36271433540). M0 DoD met.

### Notes

- The devShell unsets `GOROOT`: an inherited `GOROOT` pointing at another Go
  breaks builds with `compile: version "goX" does not match go tool version "goY"`.
- When `go.mod` dependencies change, update `vendorHash` in `flake.nix`
  (set it to `pkgs.lib.fakeHash`, run `nix build`, copy the `got:` hash).

## Next: M1 — Classifier

See [ROADMAP.md](ROADMAP.md).

`internal/classify` + golden table (≥ 200 cases) + fuzz + benchmark + `ecdy classify`.
