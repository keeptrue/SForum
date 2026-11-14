#!/usr/bin/env bash
# 夹具正样本重放：一条命令把 extensions/fixtures/** 的夹具包挂进 dev 来源根，由
# **已在运行**的 dev API（air 即可）完成 构建 → digest → stage → 精确 grant → enable。
#
# 一行命令：
#   ./scripts/dev-fixture-install.sh sforum.badge-facts
#
# 前置条件：
#   - .env 存在（脚本通过 scripts/sforum.sh 取 DATABASE_URL 做只读回读）
#   - :8080 的 API 以 APP_ENV=development 运行，且装配了 dev watcher
#     （来源根集合 = EXTERNAL_EXTENSION_ROOTS ∪ extensions/dev；本机 .env 第一项
#     就是仓库内 extensions/dev，所以无需额外配置）
#   - 不重启 API；不写 trust.levels / user_trust_levels（本脚本全程只读回读）
#
# 幂等（为什么重复跑安全）：
#   - 包内容按规范化签名比较：忽略 backend/plugin* 构建产物与 manifest 里由构建
#     重新生成的 digest 字段；签名一致时**不复制**，因此不产生文件事件。
#   - host 侧 BuildBackend 在产物字节不变时不替换 backend/plugin、WriteDigest 在
#     manifest 字节不变时不重写 —— 见 app/Support/ExtensionPackage/dev_build.go。
#   - 因此第二遍只做一次只读回读，输出同一行 RESULT，不产生第二份 artifact。
#
# 退出码：0 = 已启用且可回读；1 = 前置缺失 / 包校验失败 / 超时（附可读诊断）。
#
# 回滚：本脚本只写 dev 来源根（extensions/dev/**，gitignored），不写库。
#   - 只停用：POST /api/v1/admin/extensions/<id>/disable（dev auto 尊重显式停用，
#     不会自己再启用回来）。
#   - 彻底撤销：先 disable，再 DELETE /api/v1/admin/extensions/<id>；若卸载后留下
#     「制品缺失」行，用 POST /api/v1/admin/extensions/missing-artifacts/uninstall 收敛。
#   - 注意 host **不会**因为 dev 根里的副本消失就卸载扩展（来源同步只处理在场包），
#     所以 rm -rf 副本不等于回滚。
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ID="sforum.badge-facts"
WAIT_SECONDS=300
STATUS_ONLY=0
API_LOG="${SFORUM_API_LOG:-/tmp/api-dev-air.log}"

usage() {
  cat <<'EOF'
用法: ./scripts/dev-fixture-install.sh [--wait 秒] [--status] [extension-id]

  extension-id   夹具包目录名（默认 sforum.badge-facts），须存在于
                 extensions/fixtures/plugins|themes/<id>/
  --wait N       等待 dev API 完成启用的最长秒数（默认 300）
  --status       只回读当前状态，不复制、不等待

环境：
  SFORUM_API_LOG  dev API 日志路径，仅用于超时诊断提示（默认 /tmp/api-dev-air.log）
EOF
}

die() { printf 'FAIL %s\n' "$*" >&2; exit 1; }
note() { printf '  %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --wait) [[ $# -ge 2 ]] || die "--wait 需要秒数"; WAIT_SECONDS="$2"; shift 2 ;;
    --status) STATUS_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) die "未知参数: $1（--help 查看用法）" ;;
    *) ID="$1"; shift ;;
  esac
done

# ---- 1. 定位夹具包（plugins 优先，其次 themes） ----
# 目录名不必等于扩展 id（如 sforum-badge-facts 承载 sforum.badge-facts），所以
# 先按目录名直配，再退化为「扫描 fixtures 树、读 manifest 的 id」。
SRC=""
DEST_ROOT=""
for kind in plugins themes; do
  candidate="$REPO/extensions/fixtures/$kind/$ID"
  if [[ -f "$candidate/sforum.extension.json" ]]; then
    SRC="$candidate"
    DEST_ROOT="$REPO/extensions/dev/$kind"
    break
  fi
done
if [[ -z "$SRC" ]]; then
  for kind in plugins themes; do
    shopt -s nullglob
    for candidate in "$REPO/extensions/fixtures/$kind"/*/sforum.extension.json; do
      candidate="$(dirname "$candidate")"
      if [[ "$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['id'])" "$candidate/sforum.extension.json" 2>/dev/null)" == "$ID" ]]; then
        SRC="$candidate"
        DEST_ROOT="$REPO/extensions/dev/$kind"
        break 2
      fi
    done
    shopt -u nullglob
  done
fi
[[ -n "$SRC" ]] || die "找不到夹具包: 在 extensions/fixtures/{plugins,themes}/*/sforum.extension.json 里没有 id=$ID（先 ls 确认目录存在）"

# dev 来源根里的目录名沿用夹具目录名（与 host 的包目录扫描一致）。
DEST="$DEST_ROOT/$(basename "$SRC")"
MANIFEST_ID="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['id'])" "$SRC/sforum.extension.json" 2>/dev/null)" \
  || die "夹具 manifest 无法解析: $SRC/sforum.extension.json"
[[ "$MANIFEST_ID" == "$ID" ]] || die "目录名与 manifest id 不一致: dir=$ID manifest.id=$MANIFEST_ID"

# ---- 2. 规范化内容签名（忽略构建产物与生成型 digest） ----
signature() {
  python3 - "$1" <<'PY'
import hashlib, json, os, sys

root = sys.argv[1]
skip_dirs = {'node_modules', '.git', 'dist', 'tmp', 'temp', '.cache'}
skip_names = {'.DS_Store'}
digest = hashlib.sha256()
for dirpath, dirnames, filenames in os.walk(root):
    dirnames[:] = sorted(d for d in dirnames if d not in skip_dirs)
    for name in sorted(filenames):
        if name in skip_names or name.endswith('.zip'):
            continue
        rel = os.path.relpath(os.path.join(dirpath, name), root).replace(os.sep, '/')
        # 构建产物：字节随工具链变化，不是作者内容。
        if rel.startswith('backend/') and name in {'plugin', 'plugin.tmp'}:
            continue
        data = open(os.path.join(dirpath, name), 'rb').read()
        if rel == 'sforum.extension.json':
            try:
                manifest = json.loads(data)
                (manifest.get('backend') or {}).pop('digest', None)
                for entry in manifest.get('packageFiles') or []:
                    if isinstance(entry, dict):
                        entry.pop('digest', None)
                data = json.dumps(manifest, sort_keys=True, ensure_ascii=False).encode()
            except Exception:
                pass
        digest.update(rel.encode())
        digest.update(b'\0')
        digest.update(hashlib.sha256(data).hexdigest().encode())
        digest.update(b'\n')
print(digest.hexdigest())
PY
}

SRC_SIG="$(signature "$SRC")" || die "计算源签名失败: $SRC"

# ---- 3. 只读回读：从 CLI 的 recovery 视图读库（不启动插件代码） ----
read_status() {
  "$REPO/scripts/sforum.sh" extension list --json 2>/dev/null | python3 -c "
import json, sys
want = sys.argv[1]
try:
    items = json.load(sys.stdin)
except Exception:
    print('parse-error')
    sys.exit(0)
for item in items:
    if item.get('id') == want:
        print('%s\t%s\t%s' % (item.get('status'), item.get('version'), item.get('packageDigest')))
        break
else:
    print('absent')
" "$ID"
}

report_and_exit() {
  local observed="$1"
  local status version digest
  status="$(printf '%s' "$observed" | cut -f1)"
  version="$(printf '%s' "$observed" | cut -f2)"
  digest="$(printf '%s' "$observed" | cut -f3)"
  note "source: $SRC"
  note "target: $DEST"
  note "signature: $SRC_SIG"
  printf 'RESULT id=%s status=%s version=%s digest=%s signature=%s\n' \
    "$ID" "$status" "$version" "$digest" "$SRC_SIG"
  [[ "$status" == "enabled" ]] || die "扩展未启用（status=$status）；诊断: tail -n 60 $API_LOG"
  exit 0
}

if [[ "$STATUS_ONLY" == "1" ]]; then
  report_and_exit "$(read_status)"
fi

# ---- 4. 幂等同步到 dev 来源根 ----
mkdir -p "$DEST_ROOT"
if [[ -d "$DEST" ]] && [[ "$(signature "$DEST")" == "$SRC_SIG" ]]; then
  note "up-to-date: 目标内容签名与源一致，不复制（不产生文件事件）"
else
  # 故意不复制 backend/plugin：夹具里那份是作者构建产物（不带 -s -w），而 dev
  # watcher 用 `-trimpath -buildvcs=false -ldflags=-s -w` 重建，字节必然不同。
  # 复制旧二进制会让「首次快照」记下一个随后会被重建替换的摘要，于是 active 与
  # staged 摘要不一致，dev auto 的 exact grant 与 Enable 的 trust 校验对不上，
  # 表现为日志里的 `dev auto: promote failed ... trust challenge required`。
  # 交给 buildRoot 先构建，快照就只可能看到与后续重建一致的二进制。
  if command -v rsync >/dev/null 2>&1; then
    rsync -a --delete --exclude 'backend/plugin' --exclude 'backend/plugin.tmp' \
      "$SRC/" "$DEST/" || die "rsync 失败: $SRC -> $DEST"
  else
    rm -rf "$DEST" && mkdir -p "$DEST" && cp -R "$SRC/." "$DEST/" || die "cp 失败: $SRC -> $DEST"
    rm -f "$DEST/backend/plugin" "$DEST/backend/plugin.tmp"
  fi
  note "synced: $SRC -> $DEST（等待 dev watcher 构建/启用）"
fi

# 库里没有这行、但 dev 根里已有同内容副本时，不会再有文件事件；轻推一次 mtime
# （内容不变 ⇒ 摘要不变）让 watcher 重扫，避免「脚本说 up-to-date、库里却 absent」。
if [[ "$(read_status | cut -f1)" == "absent" ]]; then
  touch "$DEST/sforum.extension.json"
  note "nudged: 库内无此扩展，touch manifest 触发一次来源重扫"
fi

# ---- 5. 等待 dev watcher 收敛（不重启 API） ----
deadline=$((SECONDS + WAIT_SECONDS))
last=""
while (( SECONDS < deadline )); do
  observed="$(read_status)"
  if [[ "$observed" != "$last" ]]; then
    note "observe: $observed"
    last="$observed"
  fi
  status="${observed%%$'\t'*}"
  [[ "$status" == "enabled" ]] && report_and_exit "$observed"
  if [[ "$status" == "disabled" ]]; then
    # dev auto 尊重显式停用（promoteOne 直接返回），继续等只会白等到超时。
    note "detected: 显式停用状态"
    die "扩展被显式停用，dev auto 不会自动重启用：先在管理端启用（POST /api/v1/admin/extensions/$ID/enable），再跑本脚本"
  fi
  sleep 3
done

note "超时 ${WAIT_SECONDS}s 仍未 enabled，最后一次观测: ${last:-none}"
die "dev watcher 未收敛；诊断: tail -n 60 $API_LOG（关注 'package built' / 'dev auto: extension promoted' / 'promote failed'）"
