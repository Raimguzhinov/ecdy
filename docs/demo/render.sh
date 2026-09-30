#!/usr/bin/env bash
# Renders docs/demo/demo.gif, the README's demo, from the *.tape scenes:
#
#   nix develop .#demo -c docs/demo/render.sh
#
# With scene names (e.g. 2-prompt 3-context), it records only those; the
# GIF is made once out/ has every scene.
#
# Every scene runs in a clean zsh (./zshrc) with a throwaway $HOME, a fresh
# copy of the ./calc project and ./agent, a scripted ACP agent, as both
# `claude` and `codex`: no real agent, login or network is involved. VHS
# (https://github.com/charmbracelet/vhs) records each tape; ffmpeg adds the
# captions and the transitions and makes the GIF.
set -euo pipefail

demo=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$demo/../.." && pwd)
font=${ECDY_DEMO_FONT:?run in nix develop .#demo}

# scene tape | caption
scenes=(
	"1-commands|Commands run in zsh, as always"
	"2-prompt|Plain English goes to your agent"
	"3-context|The agent sees what you just ran"
	"4-permission|Nothing runs without your OK"
	"5-ask|Not sure? ecdy asks first"
	"6-typo|A typo? It offers the fix"
	"7-agents|Any ACP agent: Claude, Codex, Gemini, …"
)
fade=0.6   # seconds of each transition
title=2.4  # seconds of the title card
width=1200 # of every frame (the tapes' Width)
height=640 # of a recording (the tapes' Height)
caption=64 # height of the caption band under it
bg=0x11111b

work=$(mktemp -d)
cleanup() {
	# The shells' daemons stop with their shells; this catches a stray one.
	pkill -f "^$work/bin/" 2>/dev/null || true
	rm -rf "$work"
}
trap cleanup EXIT

mkdir -p "$work/bin" "$work/home/.config/ecdy" "$demo/out"
(cd "$repo" && CGO_ENABLED=0 go build -o "$work/bin/ecdy" ./cmd/ecdy && go build -o "$work/bin/demo-agent" ./docs/demo/agent)
cp "$demo/zshrc" "$work/home/.zshrc"
cat >"$work/home/.config/ecdy/config.toml" <<EOF
default_agent = "claude"

[agents.claude]
command = ["$work/bin/demo-agent", "-name", "Claude Code"]

[agents.codex]
command = ["$work/bin/demo-agent", "-name", "Codex"]
EOF

export HOME=$work/home
export XDG_CONFIG_HOME=$HOME/.config XDG_STATE_HOME=$HOME/.local/state XDG_CACHE_HOME=$HOME/.cache
export XDG_RUNTIME_DIR=$work/run ZDOTDIR=$HOME PATH=$work/bin:$PATH
export PAGER=cat GIT_PAGER=cat LC_ALL=C.UTF-8 LANG=C.UTF-8
export GIT_CONFIG_GLOBAL=$HOME/.gitconfig GIT_CONFIG_NOSYSTEM=1
mkdir -m 0700 "$XDG_RUNTIME_DIR"
git config --global user.name "Demo" && git config --global user.email demo@example.com
git config --global init.defaultBranch main && git config --global advice.detachedHead false

# fixture makes a fresh ~/calc: a git repository with a failing test.
fixture() {
	rm -rf "$HOME/calc"
	cp -r "$demo/calc" "$HOME/calc"
	cd "$HOME/calc"
	git init -q
	echo tmp/ >.gitignore
	local msg
	for msg in "Add Parse and Eval" "Add table-driven tests" "Support division"; do
		git commit -q --allow-empty -m "$msg"
	done
	git add -A && git commit -q -m "Test division"
	mkdir tmp && touch tmp/{app.conf,cache.bin,build.log,settings.toml}
	go test ./... >/dev/null 2>&1 || true # warm the build cache
	cd "$demo"
}

cd "$demo"
if ((!$#)); then
	for s in "${scenes[@]}"; do set -- "$@" "${s%%|*}"; done
fi
for name; do
	fixture
	vhs "$name.tape" >/dev/null
	echo "recorded out/$name.mp4"
done
for s in "${scenes[@]}"; do
	[[ -f out/${s%%|*}.mp4 ]] || exit 0
done

# The title card, then every scene with its caption, joined by transitions.
inputs=(-f lavfi -i "color=c=$bg:s=${width}x$((height + caption)):d=$title:r=30")
filter="[0]drawtext=fontfile=$font/JetBrainsMonoNerdFontMono-Bold.ttf:text=ecdy:fontsize=96:fontcolor=0xcba6f7:x=(w-tw)/2:y=(h-th)/2-40,"
# Texts go in files: drawtext's text= would need its own escaping.
printf %s "type in zsh · prompts go to your agent" >"$work/caption0"
filter+="drawtext=fontfile=$font/JetBrainsMonoNerdFontMono-Regular.ttf:textfile=$work/caption0:fontsize=30:fontcolor=0xcdd6f4:x=(w-tw)/2:y=(h/2)+50,settb=1/30,format=yuv420p[v0];"
offset=$(awk "BEGIN{print $title - $fade}")
prev=v0
i=1
for s in "${scenes[@]}"; do
	name=${s%%|*} text=${s#*|}
	inputs+=(-i "out/$name.mp4")
	printf %s "$text" >"$work/caption$i"
	filter+="[$i]fps=30,pad=$width:$((height + caption)):0:0:color=$bg,"
	filter+="drawtext=fontfile=$font/JetBrainsMonoNerdFontMono-Bold.ttf:textfile=$work/caption$i:fontsize=28:fontcolor=0xf5e0dc:x=(w-tw)/2:y=$height+($caption-th)/2-8,settb=1/30,format=yuv420p[s$i];"
	filter+="[$prev][s$i]xfade=transition=smoothleft:duration=$fade:offset=$offset[v$i];"
	len=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "out/$name.mp4")
	offset=$(awk "BEGIN{print $offset + $len - $fade}")
	prev=v$i
	i=$((i + 1))
done
filter+="[$prev]fps=15,scale=1000:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle"
ffmpeg -v error -y "${inputs[@]}" -filter_complex "$filter" demo.gif
echo "wrote $demo/demo.gif ($(du -h demo.gif | cut -f1))"
