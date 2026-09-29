# ecdy.plugin.zsh — zsh integration for ecdy (https://github.com/Raimguzhinov/ecdy).
#
# Load with `eval "$(ecdy init zsh)"` in ~/.zshrc, or source this file directly.
#
# On Enter the line is classified by `ecdy classify`:
#   cmd    → the line runs as usual;
#   prompt → the buffer is rewritten to `ecdy ask -- '<prompt>'` and run, so the
#            agent gets a regular foreground process (TTY, Ctrl+C, job control);
#   ask    → a one-key dialog below the line; Enter sends the line to the agent.
# Alt+Enter runs the line as a command without classification.
#
# Fail-open (AGENTS.md, invariant 2): if the binary is missing, crashes, prints
# garbage or misses the deadline, the line is accepted exactly like vanilla zsh.
# Requires zsh 5.8 or newer; on older versions the plugin does not load.
#
# Settings (set before loading):
#   ECDY_BIN               ecdy executable (default: `ecdy` from $PATH)
#   ECDY_CLASSIFY_TIMEOUT  classification deadline in seconds (default: 0.5)
#   ECDY_INDICATOR         rprompt (default): what Enter will do is shown in
#                          front of RPROMPT while typing; var: only set
#                          $ECDY_VERDICT (cmd, prompt, ask or empty) and redraw,
#                          for a theme that draws it; off: nothing, no forks
#   ECDY_INDICATOR_PROMPT, ECDY_INDICATOR_ASK, ECDY_INDICATOR_CMD
#                          the indicator per verdict, prompt escapes allowed
#                          (defaults: '%F{magenta}→ agent%f', '%F{yellow}? ask%f', '')
#
# The indicator runs `ecdy classify` in the background on every change of the
# line, the way Enter does, and never delays typing; Enter classifies again
# and decides (docs/adr/0006-ux.md).
#
# Every shell that loads the plugin is a session: it exports ECDY_SESSION and
# ECDY_SHELL_PID, so that `ecdy ask` talks to this session's daemon and the
# conversation continues across prompts. The daemon is started by the first
# prompt and stopped by the zshexit hook (docs/adr/0003-session-daemon.md).
#
# The plugin defines a function `ecdy` in front of the binary (unless one
# exists): `ecdy doctor` also checks this shell, which only the shell can
# see (_ecdy_doctor_probe).
#
# After every command, precmd runs `ecdy log record` in the background: the
# line, its directory, exit status and duration go to the session's log,
# secrets redacted, and the agent gets the recent ones with a prompt
# (docs/adr/0004-session-context.md; `ecdy log` shows them). Lines starting
# with a space are not recorded when HIST_IGNORE_SPACE is set.

[[ -o interactive ]] || return 0
# Oldest supported zsh; on older ones Enter stays vanilla (fail-open).
autoload -Uz is-at-least
is-at-least 5.8 || return 0
(( ${+functions[_ecdy_accept_line]} )) && return 0 # already loaded

# $sysparams[pid] (the pid of a subshell) lets us kill a classifier that
# missed its deadline; zsh/parameter provides $aliases, $functions, etc.
# https://zsh.sourceforge.io/Doc/Release/Zsh-Modules.html
zmodload zsh/system 2>/dev/null
zmodload zsh/parameter 2>/dev/null
zmodload zsh/zselect 2>/dev/null
zmodload zsh/datetime 2>/dev/null

# A new session for every shell, also one started from a shell with ecdy:
# the pid and the start time make the id unique on this machine.
export ECDY_SESSION="$$-${${EPOCHREALTIME:-$SECONDS}//[^0-9]/}"
export ECDY_SHELL_PID=$$

typeset -g _ECDY_ORIG=''      # the line the user typed, for zshaddhistory
typeset -g _ECDY_REWRITTEN='' # what accept-line actually ran instead
typeset -g _ECDY_ASK_LINE=''  # the same, for preexec: a prompt is not a command to record
typeset -g _ECDY_SHOW=''      # the typed line to draw over the rewritten one
typeset -g ECDY_VERDICT=''     # the indicator's verdict for the line being typed
typeset -g _ecdy_ind_fd='' _ecdy_ind_pid='' # the background classifier, if any
typeset -g _ecdy_ind_buf=''    # the line it classifies (or classified last)
typeset -g _ecdy_ind_text=''   # the indicator shown
typeset -g _ecdy_rps1_user='' _ecdy_rps1_set='' # RPS1 without and with it
typeset -g _ECDY_CMD='' _ECDY_CMD_CWD='' _ECDY_CMD_START='' # the command running now
typeset -ga _ecdy_recorders=() # pids of `ecdy log record` that may still be running

# Words after which the next word is still in command position, and the
# options of those words that take a separate value. Must match
# precommands/precommandArgFlags in internal/classify/words.go.
typeset -ga _ecdy_precommands=(sudo doas noglob command builtin exec nocorrect env time nohup -)
typeset -gA _ecdy_precommand_arg_flags=(
  sudo '-u -g -h -p -C -D -r -t -U -T --user --group --host --prompt --chdir'
  doas '-u -C'
  env  '-u -C -S --unset --chdir --split-string'
  exec '-a'
)

# _ecdy_first_kind LINE — set REPLY to the kind of the first command word the way
# `whence -w` names it: alias, reserved, function, builtin, command or none.
# Assignments, precommand modifiers and leading redirections are skipped, like
# classify.FirstWord does. Uses parameter lookups instead of $(whence -w) to
# avoid a fork on every Enter (hence REPLY rather than stdout, too).
_ecdy_first_kind() {
  emulate -L zsh
  setopt extendedglob
  local -a words
  words=(${(z)1}) # split like the shell parser would (zshexpn, "(z)")
  local w pre='' word=''
  integer skip=0 redir=0
  for w in $words; do
    if (( redir )); then redir=0; continue; fi
    if (( skip )); then skip=0; continue; fi
    case $w in
      ([0-9]#(\<|\>)*) redir=1; continue ;;                      # >file, 2>, <<
      (\;*|\&*|\|*|\(|\)) break ;;                               # nothing before an operator
    esac
    [[ $w == [[:alpha:]_][[:alnum:]_]#(\[*\]|)(+|)=* ]] && continue # FOO=1
    if [[ -n $pre && $w == -?* ]]; then
      (( ${${=_ecdy_precommand_arg_flags[$pre]}[(Ie)$w]} )) && skip=1
      continue
    fi
    if (( ${_ecdy_precommands[(Ie)$w]} )); then pre=$w; continue; fi
    word=${(Q)w}
    break
  done
  if [[ -z $word ]]; then
    REPLY=none
  elif (( ${+aliases[$word]} || ${+galiases[$word]} )); then
    REPLY=alias
  elif (( ${reswords[(Ie)$word]} )); then
    REPLY=reserved
  elif (( ${+functions[$word]} )); then
    REPLY=function
  elif (( ${+builtins[$word]} )); then
    REPLY=builtin
  elif whence -p -- $word >/dev/null 2>&1; then
    REPLY=command
  else
    REPLY=none
  fi
}

# _ecdy_classify LINE — set reply=(verdict prompt suggestion correction dangerous)
# from `ecdy classify --format=nul`. Returns non-zero on any failure, in which
# case the caller must fall back to vanilla accept-line.
_ecdy_classify() {
  emulate -L zsh
  reply=()
  local bin=${ECDY_BIN:-ecdy} timeout=${ECDY_CLASSIFY_TIMEOUT:-0.5}
  whence -p -- $bin >/dev/null 2>&1 || return 1
  local REPLY kind fd pid field
  _ecdy_first_kind $1
  kind=$REPLY
  local -a fields
  # The subshell first reports its pid, then execs ecdy in place, so the pid
  # is ecdy's and a hung classifier can be killed after the deadline.
  exec {fd}< <(
    print -rn -- "${sysparams[pid]:-}"$'\0'
    exec command $bin classify --shell=zsh --format=nul --first-kind=$kind -- $1 2>/dev/null
  ) || return 1
  # The pid comes before ecdy starts, so it gets a fixed deadline of its own:
  # without it a timed-out classifier could not be stopped.
  IFS= read -r -d '' -t 1 -u $fd pid
  while IFS= read -r -d '' -t $timeout -u $fd field; do
    fields+=("$field")
  done
  exec {fd}<&-
  if (( $#fields != 5 )); then
    _ecdy_stop $pid
    return 1
  fi
  reply=("${fields[@]}")
}

# _ecdy_nap — sleep 10 ms without forking. Returns 1 without zsh/zselect,
# so that callers stop waiting instead of spinning. zselect's own status is
# 1 on a timeout, so it cannot tell that apart.
_ecdy_nap() {
  zmodload -e zsh/zselect || return 1
  zselect -t 1 2>/dev/null
  return 0
}

# _ecdy_stop PID — kill a classifier that missed its deadline and wait (up to
# 0.2 s) until it is gone. `wait` does not work for process substitutions, and
# a child that exits later, while ZLE draws the next prompt, can leave that
# prompt blank until a key is pressed (seen with zsh 5.9).
_ecdy_stop() {
  [[ $1 == <-> ]] || return 0
  kill $1 2>/dev/null || return 0
  integer i
  for (( i = 0; i < 20; i++ )); do
    kill -0 $1 2>/dev/null || return 0
    _ecdy_nap || return 0
  done
}

# Replace the buffer with a call to `ecdy ask` and remember the original line
# for zshaddhistory. (qq) single-quotes the prompt, so history expansion (`!`)
# and globbing do not touch it.
_ecdy_to_agent() {
  _ECDY_ORIG=$BUFFER
  BUFFER="${(q)${ECDY_BIN:-ecdy}} ask -- ${(qq)1}"
  _ECDY_REWRITTEN=$BUFFER
  _ECDY_ASK_LINE=$BUFFER
  # Printable text only: zle shows control characters and newlines its own way.
  [[ $_ECDY_ORIG == *[[:cntrl:]]* ]] || _ECDY_SHOW=$_ECDY_ORIG
  # The agent should see the command typed just before: let its recorder
  # finish (it takes milliseconds). A hung one is forgotten, so that it
  # delays one prompt, not every prompt.
  _ecdy_wait_recorders 50 || _ecdy_recorders=()
}

# _ecdy_dialog — the Ask dialog: one line under the input, one key.
# reply is the classification result. Enter (the default) sends the line to
# the agent, as AGENTS.md section 4 requires.
_ecdy_dialog() {
  local prompt=$reply[2] correction=$reply[4] dangerous=$reply[5] msg key
  if [[ -n $correction ]]; then
    msg="ecdy: did you mean \`$correction\`?  ⏎ agent · f fix · r run · e edit · Esc cancel"
  elif [[ $dangerous == 1 ]]; then
    msg="ecdy: destructive command that reads like a prompt.  ⏎ agent · r run · e edit · Esc cancel"
  else
    msg="ecdy: command or prompt?  ⏎ agent · r run · e edit · Esc cancel"
  fi
  zle -M -- $msg
  zle -R
  read -k 1 key || key=$'\e'
  zle -M ''
  case $key in
    ($'\r'|$'\n')
      _ecdy_to_agent $prompt
      _ecdy_accept ;;
    (r|R) _ecdy_accept ;;
    (f|F)
      if [[ -n $correction ]]; then
        BUFFER=$correction
        _ecdy_accept
      fi ;;
    (e|E) ;; # keep the line for editing
    (*) # Esc and anything else: drop the line, run nothing
      BUFFER=''
      zle .send-break ;;
  esac
}

# Accept the line through whatever accept-line was before ecdy loaded
# (another plugin's wrapper, or the builtin).
_ecdy_accept() {
  if (( ${+widgets[_ecdy_orig_accept_line]} )); then
    zle _ecdy_orig_accept_line
  else
    zle .accept-line
  fi
}

_ecdy_accept_line() {
  emulate -L zsh
  _ECDY_ORIG='' _ECDY_REWRITTEN=''
  # An indicator answer must not redraw the prompt under the Ask dialog.
  _ecdy_ind_cancel
  # Classify only a fresh top-level line: not continuation lines ($PREBUFFER,
  # CONTEXT=cont), vared or select prompts.
  if [[ $CONTEXT != start || -n $PREBUFFER || -z ${BUFFER//[[:space:]]/} ]] ||
     ! _ecdy_classify $BUFFER; then
    _ecdy_accept
    return
  fi
  case $reply[1] in
    (prompt)
      _ecdy_to_agent $reply[2]
      _ecdy_accept ;;
    (ask) _ecdy_dialog ;;
    (*) _ecdy_accept ;; # cmd, or anything unexpected
  esac
}

# Show the line as typed in the scrollback instead of the `ecdy ask -- ...`
# it was rewritten to (docs/adr/0006-ux.md). ZLE has drawn the rewritten
# line and redraws it once more after this hook, so it cannot be shown as
# one text and run as another: the typed line is written over it, from the
# start of the buffer, with the cursor saved and restored around it. It is
# shorter than the rewritten line already on screen, so it cannot scroll,
# and ZLE's last redraw finds nothing to change.
_ecdy_line_finish() {
  # First take the indicator away, as that redraws the line: the accepted
  # line keeps the user's own right prompt.
  local verdict=$ECDY_VERDICT
  _ecdy_ind_reset
  [[ -z $verdict ]] || zle reset-prompt
  [[ -n $_ECDY_SHOW ]] || return 0
  local show=$_ECDY_SHOW
  _ECDY_SHOW=''
  [[ $BUFFER == $_ECDY_REWRITTEN && $TERM != dumb ]] || return 0
  CURSOR=0
  zle -R
  print -rn -- $'\e7'"$show"$'\e[J\e8' 2>/dev/null >/dev/tty
}

# The indicator. zle-line-pre-redraw runs before every redraw; when the line
# has changed, the classifier for the old line is dropped and a new one is
# started in the background, its output watched with `zle -F` (zshzle,
# "zle -F"; zsh-autosuggestions' async mode does the same). The answer
# comes to _ecdy_ind_ready, a widget, which may redraw the prompt.
_ecdy_ind_on() { [[ ${ECDY_INDICATOR:-rprompt} == (rprompt|var) && $TERM != dumb ]] }

_ecdy_ind_cancel() {
  if [[ -n $_ecdy_ind_fd ]]; then
    zle -F $_ecdy_ind_fd 2>/dev/null
    exec {_ecdy_ind_fd}<&-
    _ecdy_ind_fd=''
  fi
  if [[ -n $_ecdy_ind_pid ]]; then
    kill $_ecdy_ind_pid 2>/dev/null
    _ecdy_ind_pid=''
  fi
}

_ecdy_pre_redraw() {
  _ecdy_ind_on || return 0
  [[ $BUFFER == $_ecdy_ind_buf ]] && return 0
  emulate -L zsh
  _ecdy_ind_buf=$BUFFER
  _ecdy_ind_cancel
  local bin=${ECDY_BIN:-ecdy} REPLY
  if [[ $CONTEXT != start || -n $PREBUFFER || -z ${BUFFER//[[:space:]]/} ]] ||
     ! whence -p -- $bin >/dev/null 2>&1; then
    _ecdy_ind_show ''
    return 0
  fi
  _ecdy_first_kind $BUFFER
  # As in _ecdy_classify: the subshell reports its pid and execs ecdy.
  exec {_ecdy_ind_fd}< <(
    print -rn -- "${sysparams[pid]:-}"$'\0'
    exec command $bin classify --shell=zsh --format=nul --first-kind=$REPLY -- $BUFFER 2>/dev/null
  ) || { _ecdy_ind_fd=''; return 0; }
  IFS= read -r -d '' -t 1 -u $_ecdy_ind_fd _ecdy_ind_pid
  zle -F -w $_ecdy_ind_fd _ecdy_ind_ready
}

# The classifier's answer (or its end: $2 is set on an error or a hangup
# without data). A half-written answer is read with a deadline, so a hung
# classifier cannot freeze the line.
_ecdy_ind_ready() {
  emulate -L zsh
  local fd=$1 field
  local -a fields
  zle -F $fd 2>/dev/null
  if [[ $fd == $_ecdy_ind_fd ]]; then
    while IFS= read -r -d '' -t ${ECDY_CLASSIFY_TIMEOUT:-0.5} -u $fd field; do
      fields+=("$field")
    done
    # An answer cut short: the classifier may hang after it.
    (( $#fields == 5 )) || kill $_ecdy_ind_pid 2>/dev/null
    _ecdy_ind_pid=''
    _ecdy_ind_fd=''
  fi
  exec {fd}<&-
  (( $#fields == 5 )) || fields=('')
  _ecdy_ind_show $fields[1]
}

# _ecdy_ind_show VERDICT — show the indicator for VERDICT (empty: none),
# redrawing the prompt only when it changes.
_ecdy_ind_show() {
  local text
  case $1 in
    (prompt) text=${ECDY_INDICATOR_PROMPT-'%F{magenta}→ agent%f'} ;;
    (ask)    text=${ECDY_INDICATOR_ASK-'%F{yellow}? ask%f'} ;;
    (cmd)    text=${ECDY_INDICATOR_CMD-} ;;
  esac
  [[ $1 == $ECDY_VERDICT && $text == $_ecdy_ind_text ]] && return 0
  ECDY_VERDICT=$1 _ecdy_ind_text=$text
  if [[ ${ECDY_INDICATOR:-rprompt} == rprompt ]]; then
    # A theme may have changed RPS1 since (a vi-mode marker): that is the
    # user's part now.
    [[ $RPS1 == $_ecdy_rps1_set ]] || _ecdy_rps1_user=$RPS1
    RPS1="$text${text:+${_ecdy_rps1_user:+ }}$_ecdy_rps1_user"
    _ecdy_rps1_set=$RPS1
  fi
  zle && zle reset-prompt
}

# _ecdy_ind_reset — take the indicator away. Returns 1 if RPS1 changed.
# A line dropped by Ctrl+C or send-break skips zle-line-finish, so this also
# runs before the next prompt.
_ecdy_ind_reset() {
  _ecdy_ind_cancel
  _ecdy_ind_buf='' ECDY_VERDICT='' _ecdy_ind_text=''
  if [[ -n $_ecdy_rps1_set && $RPS1 == $_ecdy_rps1_set && $RPS1 != $_ecdy_rps1_user ]]; then
    RPS1=$_ecdy_rps1_user
    return 1
  fi
  return 0
}

_ecdy_line_init() {
  _ecdy_ind_reset || zle reset-prompt
  _ecdy_rps1_user=$RPS1 _ecdy_rps1_set=$RPS1
}

# _ecdy_doctor_probe — set REPLY to what `ecdy doctor` checks in this shell,
# as key=value lines (internal/doctor): the widgets and keys ecdy relies on,
# which plugins loaded later may have taken.
_ecdy_doctor_probe() {
  emulate -L zsh
  local accept=''
  # fzf-tab's accept-line key accepts through .accept-line, skipping ecdy.
  zstyle -s ':fzf-tab:complete:' accept-line accept 2>/dev/null
  local -a p=(
    "zsh=$ZSH_VERSION"
    "bin=${ECDY_BIN:-}"
    "keymap=${${(z)$(bindkey -lL main)}[3]}"
    "accept-line=${widgets[accept-line]:-none}"
    "enter=${${(z)$(bindkey -M main '^M')}[2]}"
    "alt-enter=${${(z)$(bindkey -M main '^[^M')}[2]}"
    "question=${${(z)$(bindkey -M main '?')}[2]}"
    "pre-redraw=${widgets[zle-line-pre-redraw]:-}"
    "indicator=${ECDY_INDICATOR:-rprompt}"
    "atuin=${+functions[_atuin_preexec]}"
    "fzf-tab-accept-line=$accept"
  )
  REPLY=${(F)p}
}

if (( ! ${+functions[ecdy]} && ! ${+aliases[ecdy]} )); then
  ecdy() {
    if [[ $1 == doctor ]]; then
      local REPLY
      _ecdy_doctor_probe
      ECDY_DOCTOR_ZSH=$REPLY command ${ECDY_BIN:-ecdy} "$@"
    else
      command ${ECDY_BIN:-ecdy} "$@"
    fi
  }
fi

# Alt+Enter: run the line as a command, skipping classification.
_ecdy_force_command() {
  _ECDY_ORIG='' _ECDY_REWRITTEN=''
  _ecdy_accept
}

# Record the line the user typed instead of the `ecdy ask -- ...` it became.
# Returning 1 from zshaddhistory keeps the rewritten line out of the history
# (zshmisc, "Hook Functions"), but it "lingers in the history until the next
# line is executed", so Up would still recall it. Adding the original line
# with `print -s` from precmd, once the command has run, replaces it.
typeset -g _ECDY_HISTORY_PENDING=''

_ecdy_zshaddhistory() {
  emulate -L zsh
  [[ -n $_ECDY_REWRITTEN ]] || return 0
  local orig=$_ECDY_ORIG rewritten=$_ECDY_REWRITTEN
  _ECDY_ORIG='' _ECDY_REWRITTEN=''
  [[ ${1%$'\n'} == $rewritten ]] || return 0
  if ! [[ -o histignorespace && $orig == ' '* ]]; then
    _ECDY_HISTORY_PENDING=$orig
  fi
  return 1
}

# Stop the session's daemon and its agents when the shell exits. --no-wait:
# exiting never waits for the agents. The daemon also stops by itself once
# this shell's pid is gone (a shell killed without zshexit).
_ecdy_zshexit() {
  # zshexit also runs when a subshell calls exit: `(cd x; exit 1)` must not
  # end the session. ZSH_SUBSHELL counts the forks (zshparam).
  (( ${ZSH_SUBSHELL:-0} == 0 )) || return 0
  local bin=${ECDY_BIN:-ecdy}
  whence -p -- $bin >/dev/null 2>&1 || return 0
  # A recorder still running would write the log again after it is removed.
  if ! _ecdy_wait_recorders 50; then
    kill $_ecdy_recorders 2>/dev/null
    _ecdy_wait_recorders 20
  fi
  command $bin daemon stop --no-wait --end-session >/dev/null 2>&1
  return 0
}

# Remember the command about to run for the session log. $1 is the line as
# typed, continuation lines included (zshmisc, "Hook Functions").
_ecdy_preexec() {
  emulate -L zsh
  _ECDY_CMD=''
  if [[ -n $_ECDY_ASK_LINE && $1 == $_ECDY_ASK_LINE ]]; then
    _ECDY_ASK_LINE=''
    return 0
  fi
  _ECDY_ASK_LINE=''
  # emulate -L keeps HIST_IGNORE_SPACE: only options that affect
  # portability are reset (zshbuiltins, "emulate").
  [[ -o histignorespace && $1 == ' '* ]] && return 0
  [[ -n $EPOCHREALTIME ]] || return 0 # no zsh/datetime
  _ECDY_CMD=$1 _ECDY_CMD_CWD=$PWD _ECDY_CMD_START=$EPOCHREALTIME
}

# _ecdy_record STATUS — add the command that just ended to the session log.
# In the background and disowned: the prompt never waits for ecdy, and a
# missing or hung binary cannot hold the shell (AGENTS.md, invariant 2).
_ecdy_record() {
  emulate -L zsh
  [[ -n $_ECDY_CMD ]] || return 0
  local bin=${ECDY_BIN:-ecdy} cmd=$_ECDY_CMD
  _ECDY_CMD=''
  whence -p -- $bin >/dev/null 2>&1 || return 0
  command $bin log record --exit=$1 --start=$_ECDY_CMD_START --end=$EPOCHREALTIME \
    --cwd=$_ECDY_CMD_CWD -- $cmd </dev/null >/dev/null 2>&1 &!
  _ecdy_recorders+=($!)
  _ecdy_reap_recorders
}

# Forget the recorders that have finished.
_ecdy_reap_recorders() {
  local pid
  local -a alive
  for pid in $_ecdy_recorders; do
    kill -0 $pid 2>/dev/null && alive+=($pid)
  done
  _ecdy_recorders=($alive)
}

# _ecdy_wait_recorders N — wait until the recorders started so far have
# finished, at most N hundredths of a second. Returns 1 if some are still
# running (they stay in _ecdy_recorders).
_ecdy_wait_recorders() {
  integer i
  for (( i = 0; i < $1; i++ )); do
    _ecdy_reap_recorders
    (( $#_ecdy_recorders )) || return 0
    _ecdy_nap || break
  done
  _ecdy_reap_recorders
  (( $#_ecdy_recorders == 0 ))
}

_ecdy_precmd() {
  # First: $? is the status of the command. zsh gives every precmd function
  # that status, whatever the functions before it did (checked on 5.8.1-5.9.2).
  local st=$?
  _ecdy_record $st
  _ecdy_ind_reset
  # ZLE shows no right prompt for a line that started without one, even
  # after reset-prompt (seen on 5.8.1-5.9.2): give it an invisible one.
  [[ -n $RPS1 || ${ECDY_INDICATOR:-rprompt} != rprompt ]] || RPS1='%{%}'
  [[ -n $_ECDY_HISTORY_PENDING ]] || return 0
  print -sr -- $_ECDY_HISTORY_PENDING
  _ECDY_HISTORY_PENDING=''
}

if [[ $widgets[accept-line] == user:* ]]; then
  zle -A accept-line _ecdy_orig_accept_line
fi
zle -N accept-line _ecdy_accept_line
zle -N ecdy-force-command _ecdy_force_command
bindkey -M emacs '^[^M' ecdy-force-command
bindkey -M viins '^[^M' ecdy-force-command

autoload -Uz add-zsh-hook add-zle-hook-widget
add-zle-hook-widget line-finish _ecdy_line_finish
add-zle-hook-widget line-init _ecdy_line_init
add-zle-hook-widget line-pre-redraw _ecdy_pre_redraw
zle -N _ecdy_ind_ready
add-zsh-hook zshaddhistory _ecdy_zshaddhistory
add-zsh-hook preexec _ecdy_preexec
add-zsh-hook precmd _ecdy_precmd
add-zsh-hook zshexit _ecdy_zshexit
