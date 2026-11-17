#!/usr/bin/env bash
# 终端配色层：一键链路的唯一实现。sforum-install.sh / sforum-bootstrap.sh /
# sforum-latest.sh 内联同一份标记块（它们运行在 deploy/scripts 还不存在的空目录里）。
# 改动必须四处同改，tests/validate-term-ui.sh 会逐字节比对。
# >>> term-ui
ui_init_color() {
  if [ "${SFORUM_COLOR:-auto}" = never ] || [ -n "${NO_COLOR:-}" ] || [ "${TERM:-}" = dumb ]; then
    UI_COLOR=false
  elif [ "${SFORUM_COLOR:-auto}" = always ] || [ -t 1 ]; then
    UI_COLOR=true
  else
    UI_COLOR=false
  fi
  if [ "$UI_COLOR" = true ]; then
    UI_RESET=$'\033[0m';  UI_BANNER=$'\033[1;36m'; UI_STEP=$'\033[1m'
    UI_OK=$'\033[1;32m';  UI_WARN=$'\033[1;33m';   UI_ERR=$'\033[1;31m'
    UI_KEY=$'\033[36m';   UI_PROMPT=$'\033[1;36m'
  else
    UI_RESET=''; UI_BANNER=''; UI_STEP=''; UI_OK=''
    UI_WARN=''; UI_ERR=''; UI_KEY=''; UI_PROMPT=''
  fi
}
ui_init_color
ui_banner() { printf '%s== %s ==%s\n' "$UI_BANNER" "$1" "$UI_RESET"; }
ui_step()   { printf '%s==> %s%s\n' "$UI_STEP" "$1" "$UI_RESET"; }
ui_ok()     { printf '%s[ok] %s%s\n' "$UI_OK" "$1" "$UI_RESET"; }
ui_warn()   { printf '%s[!] %s%s\n' "$UI_WARN" "$1" "$UI_RESET" >&2; }
ui_err()    { printf '%s[x] %s%s\n' "$UI_ERR" "$1" "$UI_RESET" >&2; }
ui_key()    { printf '%s%s%s\n' "$UI_KEY" "$1" "$UI_RESET"; }
ui_prompt() { printf '%s%s%s' "$UI_PROMPT" "$1" "$UI_RESET"; }
# <<< term-ui
