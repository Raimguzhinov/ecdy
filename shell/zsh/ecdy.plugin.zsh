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
#
# Every shell that loads the plugin is a session: it exports ECDY_SESSION and
# ECDY_SHELL_PID, so that `ecdy ask` talks to this session's daemon and the
# conversation continues across prompts. The daemon is started by the first
# prompt and stopped by the zshexit hook (docs/adr/0003-session-daemon.md).
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
typeset -g _ECDY_CMD='' _ECDY_CMD_CWD='' _ECDY_CMD_START='' # the command running now

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
}

_ecdy_precmd() {
  # First: $? is the status of the command. zsh gives every precmd function
  # that status, whatever the functions before it did (checked on 5.8.1-5.9.2).
  local st=$?
  _ecdy_record $st
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

autoload -Uz add-zsh-hook
add-zsh-hook zshaddhistory _ecdy_zshaddhistory
add-zsh-hook preexec _ecdy_preexec
add-zsh-hook precmd _ecdy_precmd
add-zsh-hook zshexit _ecdy_zshexit
