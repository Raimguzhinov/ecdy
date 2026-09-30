---
name: demo-gif
description: Re-render or change the README's demo GIF (docs/demo/demo.gif) — VHS tapes, the scripted demo agent, captions and transitions. Use when a user-visible change (rendering, dialogs, indicator, messages) makes the GIF outdated, when adding or changing a scene, or when asked for a demo, screencast or GIF of ecdy.
---

# The demo GIF

The GIF is rendered from text, not recorded by hand: [VHS](https://github.com/charmbracelet/vhs)
plays each `docs/demo/N-name.tape` in a real zsh with the real plugin and binary, and ffmpeg
joins the scenes with a title card, captions and transitions.

| File | Role |
|---|---|
| `docs/demo/render.sh` | builds `ecdy` and the demo agent, sets up a throwaway `$HOME`, records, assembles |
| `docs/demo/settings.tape` | shared VHS settings (theme, font, size) and the hidden shell setup |
| `docs/demo/N-name.tape` | one scene each; captions are in the `scenes` list of `render.sh` |
| `docs/demo/agent/main.go` | scripted ACP agent: a canned turn per prompt, picked by a substring |
| `docs/demo/calc/` | the project the scenes work in (a failing test that the agent really fixes) |
| `docs/demo/zshrc` | the demo's clean zsh: not the maintainer's config |

## Commands

```sh
nix develop .#demo -c docs/demo/render.sh 2-prompt 3-context   # record some scenes to out/
nix develop .#demo -c docs/demo/render.sh                       # record all, write demo.gif
```

The GIF is assembled whenever `out/` holds every scene, so record in batches of 3–4 scenes
(each takes ~15–20 s; a full run ~2.5 min — ask the maintainer before one) and check frames
between them:

```sh
ffmpeg -v error -y -sseof -0.3 -i docs/demo/out/2-prompt.mp4 -frames:v 1 last.png   # last frame
ffmpeg -v error -y -ss 3 -i docs/demo/out/2-prompt.mp4 -frames:v 1 at3s.png          # at 3 s
```

Look at the frames (Read the PNG) before assembling: wrapped lines, pagers, locale and timing
problems are only visible there.

## Changing a scene

- A new prompt needs a reply in `agent/main.go` (`replies`; the first matching substring wins),
  otherwise the agent answers "I am a demo agent…".
- Timing is `Sleep` after each step; the agent's own delays (`took`, streaming) are fixed, so a
  scene is deterministic. Leave ~1 s on the final frame.
- Keep a line under the terminal width (~100 columns at font size 18), or it wraps mid-word.
- New scene: add `N-name.tape` (Output `out/N-name.mp4`, `Source settings.tape`) and a
  `"N-name|Caption"` row in `render.sh`.

## Known traps

- **VHS 0.12.0 writes nothing**: it prints `Creating out/x.mp4...`, exits 0 and leaves no file
  (ffmpeg is never run). The `demo` devShell pins 0.11.0; keep it pinned until a release is
  checked with a one-line tape. VHS drops ffmpeg errors too: a missing file after
  "Creating" means the render failed, not that it went elsewhere.
- VHS starts zsh with `--no-rcs`: the rc is sourced in the hidden part of `settings.tape`.
- The environment leaks into the recording: `render.sh` sets `LC_ALL=C.UTF-8` (git spoke the
  maintainer's language), `PAGER=cat` (`git log` opened `less`), its own `HOME`, XDG dirs and
  git config.
- The font comes from `FONTCONFIG_FILE` in the devShell (JetBrainsMono Nerd Font Mono), so the
  GIF does not depend on the machine's fonts; the branch glyph is written as `$''` in
  `zshrc`, because a literal private-use character can be lost by editors.
- ffmpeg's `drawtext` gets captions through `textfile=`: `text=` needs escaping of `:` even
  inside quotes.
- Size: ~3.5 MB for ~50 s at 1000 px, 15 fps. More scenes or a larger scale grow it quickly.
