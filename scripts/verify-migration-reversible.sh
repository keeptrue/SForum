#!/usr/bin/env bash
# 验证「徽章迁移」在真实 PostgreSQL 上物理可逆：
#   完整迁移链 up → 单文件 down → 断言对象全部消失 → 再 up → 断言对象全部重建。
#
# 全程跑在一个一次性 PostgreSQL 集群里，绝不碰开发库（开发库有
# public.sforum_dev_guard 哨兵，命中即中止）。脚本幂等、可反复执行，
# 退出时销毁临时集群（SFORUM_VERIFY_KEEP_CLUSTER=1 可保留现场排错）。
#
# 退出码：
#   0  全部通过
#   2  预检失败（缺 docker/go、集群起不来、环境不自洽）
#   3  目标迁移文件不存在（task-1 还没落地）
#   4  完整迁移链 up 失败
#   5  单文件 down 失败（goose 或版本前置条件）
#   6  down 之后的对象断言失败（有对象残留）
#   7  再次 up 失败
#   8  up 之后的对象断言失败（对象没重建全）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"

TARGET_VERSION="${SFORUM_MIGRATION_TARGET_VERSION:-202611100002}"
TARGET_FILE="${SFORUM_MIGRATION_TARGET_FILE:-$API_DIR/database/migrations/${TARGET_VERSION}_badges_and_user_activity.sql}"
EXPECTED_PREVIOUS="${SFORUM_MIGRATION_PREVIOUS_VERSION:-202611100001}"
EXPECTED_BADGES="${SFORUM_EXPECTED_BUILTIN_BADGES:-12}"
EXPECTED_GROUPS="${SFORUM_EXPECTED_BADGE_GROUPS:-3}"
IMAGE="${SFORUM_TEST_POSTGRES_IMAGE:-postgres:17-alpine}"
KEEP_CLUSTER="${SFORUM_VERIFY_KEEP_CLUSTER:-0}"

PG_USER=sforum
PG_PASSWORD=sforum
PG_DB=sforum_migration_verify

owner="$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
container="sforum-migration-verify-${owner}"
cid=""
work_dir=""

step() { printf '\n== %s\n' "$*"; }
ok() { printf '  ok   %s\n' "$*"; }
bad() { printf '  FAIL %s\n' "$*" >&2; }
die() {
  local code="$1"
  shift
  printf '\n[exit %s] %s\n' "$code" "$*" >&2
  exit "$code"
}

cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$work_dir" && -d "$work_dir" ]]; then
    rm -rf "$work_dir"
  fi
  if [[ -n "$cid" && "$KEEP_CLUSTER" != 1 ]]; then
    local label
    if label="$(docker inspect --format '{{index .Config.Labels "com.sforum.migration-verify-owner"}}' "$cid" 2>/dev/null)"; then
      if [[ "$label" != "$owner" ]]; then
        printf '容器所有权已变化，跳过清理：%s\n' "$cid" >&2
      else
        docker rm -fv "$cid" >/dev/null || printf '清理临时集群失败：%s\n' "$cid" >&2
      fi
    fi
  elif [[ -n "$cid" ]]; then
    printf '\n保留临时集群 %s（SFORUM_VERIFY_KEEP_CLUSTER=1），排错后手动 docker rm -fv %s\n' "$cid" "$cid" >&2
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 一次性的读值助手：容器内 psql，-tA 只输出值本身。
psql_value() {
  docker exec -e PGPASSWORD="$PG_PASSWORD" "$cid" \
    psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAc "$1"
}

# 断言标量结果；失败时打印期望值与实际值，返回非 0 由调用方累计。
assert_value() {
  local name="$1" sql="$2" expected="$3" actual
  if ! actual="$(psql_value "$sql" 2>&1)"; then
    bad "$name（查询失败：$(printf '%s' "$actual" | tr '\n' ' ')）"
    return 1
  fi
  actual="$(printf '%s' "$actual" | tr -d '[:space:]')"
  if [[ "$actual" != "$expected" ]]; then
    bad "$name（期望 $expected，实际 ${actual:-<空>}）"
    return 1
  fi
  ok "$name"
}

step "预检"
command -v docker >/dev/null || die 2 '缺少 docker。'
command -v go >/dev/null || die 2 '缺少 go。'
docker info >/dev/null 2>&1 || die 2 'docker daemon 不可用。'
[[ -d "$API_DIR" ]] || die 2 "找不到 API 目录：$API_DIR"
if [[ -n "${DATABASE_URL:-}" ]]; then
  printf '  注意 继承的 DATABASE_URL 被忽略；本脚本只用自建的一次性集群。\n'
fi
if [[ ! -f "$TARGET_FILE" ]]; then
  cat >&2 <<EOF

[exit 3] 目标迁移文件不存在：
  $TARGET_FILE

这个脚本验证的是「该文件能 down 回干净状态再 up 回来」，没有文件就无法验证。
处理方式：
  - 等 M0（task-1）把迁移文件落地后重跑本脚本；或
  - 用 SFORUM_MIGRATION_TARGET_VERSION / SFORUM_MIGRATION_TARGET_FILE 指向别的迁移做链路自检。
EOF
  exit 3
fi
ok "目标迁移文件存在：${TARGET_FILE#"$ROOT_DIR"/}"

step "启动一次性 PostgreSQL 集群（$IMAGE）"
cid="$(docker run -d --name "$container" \
  --label "com.sforum.migration-verify-owner=$owner" \
  -e POSTGRES_DB="$PG_DB" -e POSTGRES_USER="$PG_USER" -e POSTGRES_PASSWORD="$PG_PASSWORD" \
  -p 127.0.0.1::5432 "$IMAGE" \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off)" || die 2 '创建临时集群失败。'

# postgres 官方镜像首次启动会跑 initdb 再重启一次；pg_isready 在重启窗口里可能
# 假阳性，因此用真实认证连接连续两次成功作为就绪判据。
ready=0
stable=0
for _ in {1..60}; do
  if docker exec -e PGPASSWORD="$PG_PASSWORD" "$cid" \
    psql -U "$PG_USER" -d "$PG_DB" -tAc 'SELECT 1' >/dev/null 2>&1; then
    stable=$((stable + 1))
    if [[ "$stable" == 2 ]]; then
      ready=1
      break
    fi
  else
    stable=0
  fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then
  docker logs "$cid" >&2 || true
  die 2 '临时集群未能就绪。'
fi

binding="$(docker port "$cid" 5432/tcp)"
if [[ "$binding" != 127.0.0.1:* || "$binding" == *$'\n'* ]]; then
  die 2 "临时集群端口绑定异常：$binding"
fi
port="${binding##*:}"
export DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${port}/${PG_DB}?sslmode=disable"
# 开发库哨兵：命中就立刻中止，绝不在这台库上继续。
guard="$(psql_value "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL")" || die 2 '无法探测开发库哨兵。'
[[ "$guard" == f ]] || die 2 '目标库带 public.sforum_dev_guard 哨兵（开发库），拒绝执行。'
psql_value "COMMENT ON DATABASE ${PG_DB} IS 'sforum.disposable-migration-verify.v1';" >/dev/null
ok "集群就绪：$container，端口 $port，无开发库哨兵"

step "完整迁移链 up（apps/api/cmd/migrate）"
if ! (cd "$API_DIR" && go run ./cmd/migrate); then
  die 4 '完整迁移链 up 失败（看上面的 migrator 日志）。'
fi
ok '迁移链已到最新'

# goose 单步 down 需要一个能读到编译进二进制的迁移文件系统的程序；
# goose CLI 会拉一堆无关驱动，这里只生成一个最小 Go 程序复用模块依赖。
step "生成 down 助手"
work_dir="$(mktemp -d)"
cat >"$work_dir/main.go" <<'GO'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: migration-step dbversion | down <version>")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fail("DATABASE_URL is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fail("open database: %v", err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		fail("create goose provider: %v", err)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "dbversion":
		version, err := provider.GetDBVersion(ctx)
		if err != nil {
			fail("read applied version: %v", err)
		}
		fmt.Println(version)
	case "down":
		if len(os.Args) != 3 {
			fail("down needs an explicit target version")
		}
		target, err := strconv.ParseInt(os.Args[2], 10, 64)
		if err != nil {
			fail("parse target version: %v", err)
		}
		current, err := provider.GetDBVersion(ctx)
		if err != nil {
			fail("read applied version: %v", err)
		}
		// 只回滚「最新已应用」的那一条：目标不是最新时不能假装验证过。
		if current != target {
			fail("latest applied migration is %d, expected %d; the verified migration must be the newest applied one", current, target)
		}
		result, err := provider.Down(ctx)
		if err != nil {
			fail("down %d: %v", target, err)
		}
		fmt.Printf("rolled back %d (%s)\n", result.Source.Version, result.Source.Path)
	default:
		fail("unknown mode: %s", os.Args[1])
	}
}
GO
step_helper() { (cd "$API_DIR" && go run "$work_dir/main.go" "$@"); }
applied="$(step_helper dbversion)" || die 5 '读取已应用迁移版本失败。'
ok "已应用的最新迁移版本：$applied"

step "对 $TARGET_VERSION 单步 down"
if [[ "$applied" != "$TARGET_VERSION" ]]; then
  die 5 "最新已应用迁移是 $applied，不是 $TARGET_VERSION；本脚本只验证最新那条，先确认迁移链顺序。"
fi
if ! step_helper down "$TARGET_VERSION"; then
  die 5 "down $TARGET_VERSION 失败（看上面的 goose 错误）。"
fi
ok "已回滚 $TARGET_VERSION"

step "down 之后的对象断言（应全部消失）"
down_failures=0
# 每个迁移的对象断言不同：本脚本只验证「最新那条」，所以断言必须跟着目标走。
# 新增迁移时在这里加一个分支，不要指望通用断言能覆盖所有迁移（权限键 vs 表 vs 视图形态完全不同）。
if [[ "$TARGET_VERSION" == "202611100004" ]]; then
  # trust.manage：Down 段是「按 key 全删关联行 + 删权限行」，断言就是这两条都归零。
  assert_value '权限行 trust.manage 已删除' "SELECT NOT EXISTS (SELECT 1 FROM permissions WHERE key = 'trust.manage')" t || down_failures=$((down_failures + 1))
  assert_value '角色授权 trust.manage 已全部删除' "SELECT NOT EXISTS (SELECT 1 FROM role_permissions WHERE permission_key = 'trust.manage')" t || down_failures=$((down_failures + 1))
  assert_value "goose 版本回退到 $EXPECTED_PREVIOUS" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$EXPECTED_PREVIOUS" || down_failures=$((down_failures + 1))
else
assert_value '表 user_activity 已删除' "SELECT to_regclass('public.user_activity') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '表 user_visits 已删除' "SELECT to_regclass('public.user_visits') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '表 badge_groups 已删除' "SELECT to_regclass('public.badge_groups') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '表 badges 已删除' "SELECT to_regclass('public.badges') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '表 user_badges 已删除' "SELECT to_regclass('public.user_badges') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '列 users.top_badge_id 已删除' "SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'top_badge_id')" t || down_failures=$((down_failures + 1))
assert_value '视图 sforum_core_v1.forum_badges 已删除' "SELECT to_regclass('sforum_core_v1.forum_badges') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '视图 sforum_core_v1.forum_user_badges 已删除' "SELECT to_regclass('sforum_core_v1.forum_user_badges') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '视图 sforum_core_v1.forum_user_activity 已删除' "SELECT to_regclass('sforum_core_v1.forum_user_activity') IS NULL" t || down_failures=$((down_failures + 1))
assert_value '通知描述符 badge_granted 已删除' "SELECT NOT EXISTS (SELECT 1 FROM notification_type_descriptors WHERE type = 'badge_granted')" t || down_failures=$((down_failures + 1))
assert_value '通知通道策略 badge_granted 已删除' "SELECT NOT EXISTS (SELECT 1 FROM notification_type_policies WHERE type = 'badge_granted')" t || down_failures=$((down_failures + 1))
assert_value "goose 版本回退到 $EXPECTED_PREVIOUS" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$EXPECTED_PREVIOUS" || down_failures=$((down_failures + 1))
fi
if [[ "$down_failures" -ne 0 ]]; then
  die 6 "down 之后有 $down_failures 项残留，迁移不可逆（Down 段漏删）。"
fi

step "再次 up（只补这一条迁移）"
if ! (cd "$API_DIR" && go run ./cmd/migrate); then
  die 7 '再次 up 失败（Down 段删掉了 Up 段依赖的东西？看上面日志）。'
fi
ok '迁移链重新到最新'

step "up 之后的对象断言（应全部重建）"
up_failures=0
if [[ "$TARGET_VERSION" == "202611100004" ]]; then
  # trust.manage：权限行 1 条；授权 2 条（operator + super_admin）；版主 0 条。
  assert_value '权限行 trust.manage 已重建' "SELECT count(*) FROM permissions WHERE key = 'trust.manage'" 1 || up_failures=$((up_failures + 1))
  assert_value '角色授权 trust.manage 两条（operator + super_admin）' "SELECT count(*) FROM role_permissions WHERE permission_key = 'trust.manage'" 2 || up_failures=$((up_failures + 1))
  assert_value '版主没有 trust.manage' "SELECT count(*) FROM role_permissions rp JOIN roles r ON r.id = rp.role_id WHERE rp.permission_key = 'trust.manage' AND r.key = 'moderator'" 0 || up_failures=$((up_failures + 1))
  assert_value "goose 版本回到 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || up_failures=$((up_failures + 1))
else
assert_value '表 user_activity 存在' "SELECT to_regclass('public.user_activity') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '表 user_visits 存在' "SELECT to_regclass('public.user_visits') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '表 badge_groups 存在' "SELECT to_regclass('public.badge_groups') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '表 badges 存在' "SELECT to_regclass('public.badges') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '表 user_badges 存在' "SELECT to_regclass('public.user_badges') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '列 users.top_badge_id 存在' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'users' AND column_name = 'top_badge_id')" t || up_failures=$((up_failures + 1))
assert_value '视图 sforum_core_v1.forum_badges 存在' "SELECT to_regclass('sforum_core_v1.forum_badges') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '视图 sforum_core_v1.forum_user_badges 存在' "SELECT to_regclass('sforum_core_v1.forum_user_badges') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '视图 sforum_core_v1.forum_user_activity 存在' "SELECT to_regclass('sforum_core_v1.forum_user_activity') IS NOT NULL" t || up_failures=$((up_failures + 1))
assert_value '通知描述符 badge_granted 存在' "SELECT count(*) FROM notification_type_descriptors WHERE type = 'badge_granted'" 1 || up_failures=$((up_failures + 1))
assert_value '通知通道策略 badge_granted 三条' "SELECT count(*) FROM notification_type_policies WHERE type = 'badge_granted'" 3 || up_failures=$((up_failures + 1))
assert_value "内置徽章 ${EXPECTED_BADGES} 个" "SELECT count(*) FROM public.badges" "$EXPECTED_BADGES" || up_failures=$((up_failures + 1))
assert_value "徽章分组 ${EXPECTED_GROUPS} 个" "SELECT count(*) FROM public.badge_groups" "$EXPECTED_GROUPS" || up_failures=$((up_failures + 1))
assert_value '分组键 activity/content/honor 齐全' "SELECT count(*) FROM public.badge_groups WHERE "key" IN ('activity', 'content', 'honor')" "$EXPECTED_GROUPS" || up_failures=$((up_failures + 1))
assert_value "goose 版本回到 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || up_failures=$((up_failures + 1))
fi
if [[ "$up_failures" -ne 0 ]]; then
  die 8 "up 之后有 $up_failures 项没重建出来。"
fi

printf '\n全部通过：%s 可 down 回干净状态并重新 up 重建。\n' "$TARGET_VERSION"
