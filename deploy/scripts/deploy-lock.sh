# shellcheck shell=bash
# 部署锁：目录是原子的互斥体，元数据说明「谁持有着」——.deploy.lock/pid 是纯 pid
# 文本（deploy.sh 与 upgrade.sh 共用同一份格式），owner 追加操作名、启动时间、进程
# 启动时刻。OPS2-04：pid 已死或已被复用 → 自动回收；元数据缺失则锁龄超过 1 小时回收。
#
# 调用方契约（source 之前设置）：
#   DEPLOY_LOCK_ROOT  安装根目录的绝对路径
#   lock_text KEY     返回当前语言的提示文本；未定义时退化为 key 本身
# 调用方还需提供 die()（拒绝时使用）。

: "${DEPLOY_LOCK_ROOT:?DEPLOY_LOCK_ROOT must be set before sourcing deploy-lock.sh}"

if ! declare -F lock_text >/dev/null 2>&1; then
  lock_text() { printf '%s' "$1"; }
fi

DEPLOY_LOCK_DIR="$DEPLOY_LOCK_ROOT/.deploy.lock"
DEPLOY_LOCK_PID="$DEPLOY_LOCK_DIR/pid"
DEPLOY_LOCK_OWNER="$DEPLOY_LOCK_DIR/owner"
DEPLOY_LOCK_LOG="$DEPLOY_LOCK_ROOT/.deployrc.lock.log"

lock_event() {
  printf '%s %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$1" >> "$DEPLOY_LOCK_LOG" 2>/dev/null || true
  printf '%s\n' "$1" >&2
}

# 进程启动时刻：pid 被复用后必然不同，用它区分原持有进程与复用该 pid 的新进程。
lock_proc_start() {
  LC_ALL=C ps -o lstart= -p "$1" 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' || true
}

lock_field() { sed -n "s/^$1=//p" "$DEPLOY_LOCK_OWNER" 2>/dev/null | head -n 1 || true; }

# kill -0 在跨用户时会返回 EPERM：活着的别人家进程也会「探测失败」。ps -p 兜底把
# 这种情况判回存活，否则一个 root 持有、普通用户发起的操作会把活锁当死锁清掉。
lock_pid_alive() {
  [ -n "${1:-}" ] || return 1
  kill -0 "$1" 2>/dev/null || ps -p "$1" >/dev/null 2>&1
}

# 读任意锁目录里的持有者 pid：认领副本不在 .deploy.lock 这个名字上，所以要能按目录读。
lock_dir_pid() {
  local dir="$1" pid
  pid="$(sed -n 's/^pid=//p' "$dir/owner" 2>/dev/null | head -n 1 || true)"
  if [ -z "$pid" ] && [ -f "$dir/pid" ]; then
    pid="$(head -n 1 "$dir/pid" | tr -d '[:space:]')"
  fi
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$pid"
}

lock_holder_pid() { lock_dir_pid "$DEPLOY_LOCK_DIR"; }

# 一行持有者摘要：die、warn、日志复用同一份格式，避免三处各写一套。
lock_holder_summary() {
  local pid action started_at state=unknown
  pid="$(lock_holder_pid || true)"
  action="$(lock_field action)"
  started_at="$(lock_field started_at)"
  if [ -n "$pid" ]; then
    if lock_pid_alive "$pid"; then state=alive; else state=dead; fi
  fi
  printf 'pid=%s action=%s started_at=%s state=%s' "${pid:-?}" "${action:-?}" "${started_at:-?}" "$state"
}

lock_is_stale() {
  local pid recorded current mtime
  if pid="$(lock_holder_pid)"; then
    if ! lock_pid_alive "$pid"; then LOCK_REASON=pid_gone; return 0; fi
    recorded="$(lock_field proc_start)"
    current="$(lock_proc_start "$pid")"
    if [ -n "$recorded" ] && [ -n "$current" ] && [ "$recorded" != "$current" ]; then
      LOCK_REASON=pid_reused
      return 0
    fi
    return 1
  fi
  LOCK_REASON=no_owner
  mtime="$(stat -c %Y "$DEPLOY_LOCK_DIR" 2>/dev/null || stat -f %m "$DEPLOY_LOCK_DIR" 2>/dev/null || echo 0)"
  [ "$mtime" -gt 0 ] && [ $(($(date +%s) - mtime)) -gt 3600 ]
}

# 认领并销毁残留锁：rename 是 shell 里唯一的原子「取走」，谁先 mv 走谁负责这一段，
# 所以只删自己认领到的副本，永远不对 .deploy.lock 直接 rm -rf（那会删掉别人刚建的活锁）。
reclaim_stale_deploy_lock() {
  local summary="$1" stale_pid="$2" claim
  claim="$DEPLOY_LOCK_DIR.stale.$$"
  # 同 pid 的上一次残留副本（pid 被回收过）先清掉，否则下面的 mv 会嵌套进去。
  rm -rf "$claim" 2>/dev/null || true
  mv "$DEPLOY_LOCK_DIR" "$claim" 2>/dev/null || die "$(lock_text deploy_locked) $(lock_holder_summary)"
  # 认领到的可能是别人在判定之后重建的活锁：比对副本里的 pid 与判定时那把是否同一把。
  if [ "$(lock_dir_pid "$claim" || true)" = "$stale_pid" ]; then
    rm -rf "$claim"
    lock_event "$(lock_text lock_recovered) ${summary} reason=${LOCK_REASON:-manual}"
    return 0
  fi
  # 认领错了：还原，但必须先确认 .deploy.lock 没被再占 —— mv 到已存在的目录会把副本
  # 塞进它里面（.deploy.lock/.deploy.lock.stale.N），锁就彻底乱了。还不了就保留副本
  # 并 die：副本名不占 .deploy.lock，不阻塞任何人，运维手动删即可。
  if [ -e "$DEPLOY_LOCK_DIR" ]; then
    die "$(lock_text deploy_locked) $(lock_holder_summary) (recovered copy left at $claim)"
  fi
  mv "$claim" "$DEPLOY_LOCK_DIR" 2>/dev/null || \
    die "$(lock_text deploy_locked) $(lock_holder_summary) (recovered copy left at $claim)"
  die "$(lock_text deploy_locked) $(lock_holder_summary)"
}

acquire_deploy_lock() {
  local summary stale_pid
  LOCK_ACTION="${1:-console}"
  if ! mkdir "$DEPLOY_LOCK_DIR" 2>/dev/null; then
    summary="$(lock_holder_summary)"
    stale_pid="$(lock_holder_pid || true)"
    lock_is_stale || die "$(lock_text deploy_locked) ${summary}"
    reclaim_stale_deploy_lock "$summary" "$stale_pid"
    mkdir "$DEPLOY_LOCK_DIR" 2>/dev/null || die "$(lock_text deploy_locked) $(lock_holder_summary)"
  fi
  printf '%s\n' "$$" > "$DEPLOY_LOCK_PID"
  printf 'pid=%s\naction=%s\nstarted_at=%s\nproc_start=%s\n' "$$" "$LOCK_ACTION" \
    "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$(lock_proc_start "$$")" > "$DEPLOY_LOCK_OWNER"
  trap 'release_deploy_lock' EXIT HUP INT TERM
}

release_deploy_lock() {
  rm -rf "$DEPLOY_LOCK_DIR" 2>/dev/null || true
  trap - EXIT HUP INT TERM
}

# 人工兜底：不猜 pid、不判活，直接清掉并留下「清了谁」的日志。
force_unlock_deploy_lock() {
  local summary
  [ -e "$DEPLOY_LOCK_DIR" ] || { printf '%s\n' "$(lock_text unlock_none)"; return 0; }
  summary="$(lock_holder_summary)"
  rm -rf "$DEPLOY_LOCK_DIR" 2>/dev/null || true
  lock_event "$(lock_text unlock_done) ${summary}"
}
