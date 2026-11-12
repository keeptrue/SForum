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
#   9  X-04b 负向断言失败：多条件规则下 down 本应拒绝，却成功了（或拒绝时留下了残留）
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_DIR="$ROOT_DIR/apps/api"

TARGET_VERSION="${SFORUM_MIGRATION_TARGET_VERSION:-202611120003}"
# 文件名后缀按版本号 glob 解析，而不是写死某条迁移的名字：
# 这个脚本的用途就是「验证最新的那条迁移」，写死后缀会让换目标版本时必须改脚本。
if [[ -n "${SFORUM_MIGRATION_TARGET_FILE:-}" ]]; then
  TARGET_FILE="$SFORUM_MIGRATION_TARGET_FILE"
else
  shopt -s nullglob
  candidates=("$API_DIR/database/migrations/${TARGET_VERSION}"_*.sql)
  shopt -u nullglob
  if [[ ${#candidates[@]} -eq 1 ]]; then
    TARGET_FILE="${candidates[0]}"
  elif [[ ${#candidates[@]} -eq 0 ]]; then
    TARGET_FILE="$API_DIR/database/migrations/${TARGET_VERSION}_<unknown>.sql"
  else
    printf '\n[exit 3] 版本 %s 命中多个迁移文件，请用 SFORUM_MIGRATION_TARGET_FILE 指定：\n' "$TARGET_VERSION" >&2
    printf '  %s\n' "${candidates[@]}" >&2
    exit 3
  fi
fi
EXPECTED_PREVIOUS="${SFORUM_MIGRATION_PREVIOUS_VERSION:-202611100001}"
EXPECTED_BADGES="${SFORUM_EXPECTED_BUILTIN_BADGES:-12}"
# 内置 12 枚徽章里 10 枚是 automatic、2 枚是 manual（community-contribution /
# quality-answer）。规则转换只覆盖 automatic 那 10 枚，别把 12 当成自动徽章数。
EXPECTED_AUTO_BADGES="${SFORUM_EXPECTED_AUTOMATIC_BADGES:-10}"
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
if [[ "$TARGET_VERSION" == "202611120003" ]]; then
  # badge_backfill_runs：Down 段是「删索引 → 删表」，纯新增无数据回填，
  # 断言就是这两件事都发生且 goose 版本退回上一条。
  assert_value '表 badge_rule_backfill_runs 已删除' "SELECT to_regclass('public.badge_rule_backfill_runs') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '索引 badge_rule_backfill_runs_badge_started_idx 已删除' "SELECT to_regclass('public.badge_rule_backfill_runs_badge_started_idx') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '索引 badge_rule_backfill_runs_running_idx 已删除' "SELECT to_regclass('public.badge_rule_backfill_runs_running_idx') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '序列 badge_rule_backfill_runs_id_seq 已删除' "SELECT to_regclass('public.badge_rule_backfill_runs_id_seq') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value "goose 版本回退到 202611120002" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" 202611120002 || down_failures=$((down_failures + 1))
elif [[ "$TARGET_VERSION" == "202611120002" ]]; then
  # badge_rules：Down 段是「拒绝多条件规则 → 反推 metric/threshold → 重建旧视图
  # → 还原旧 CHECK → 拆三列/三表」。断言这五件事都发生了。
  assert_value '表 badge_facts 已删除' "SELECT to_regclass('public.badge_facts') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '表 user_fact_counters 已删除' "SELECT to_regclass('public.user_fact_counters') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '表 badge_rules_rollback 已删除' "SELECT to_regclass('public.badge_rules_rollback') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '列 badges.rule 已删除' "SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'badges' AND column_name = 'rule')" t || down_failures=$((down_failures + 1))
  assert_value '列 badges.requirement_summary_zh 已删除' "SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'badges' AND column_name = 'requirement_summary_zh')" t || down_failures=$((down_failures + 1))
  assert_value '视图 forum_user_badge_facts 已删除' "SELECT to_regclass('sforum_core_v1.forum_user_badge_facts') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '视图 forum_badges 仍存在（已还原为旧定义）' "SELECT to_regclass('sforum_core_v1.forum_badges') IS NOT NULL" t || down_failures=$((down_failures + 1))
  assert_value '视图 forum_badges 不再输出 rule' "SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'sforum_core_v1' AND table_name = 'forum_badges' AND column_name = 'rule')" t || down_failures=$((down_failures + 1))
  assert_value '旧 CHECK badges_rule_check 已恢复（含 threshold > 0）' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_rule_check' AND pg_get_constraintdef(oid) LIKE '%threshold > 0%')" t || down_failures=$((down_failures + 1))
  assert_value '自动徽章仍带 metric/threshold' "SELECT count(*) FROM public.badges WHERE grant_mode = 'automatic' AND metric IS NOT NULL AND threshold > 0" "$EXPECTED_AUTO_BADGES" || down_failures=$((down_failures + 1))
  assert_value "goose 版本回退到 202611120001" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" 202611120001 || down_failures=$((down_failures + 1))
elif [[ "$TARGET_VERSION" == "202611120001" ]]; then
  # badge_tiers：Down 段是「先把非内置档位的徽章落回 bronze → 拆外键 → 恢复旧 CHECK
  # → 删表」。断言这四件事都发生了，而不是「表没了就算过」。
  assert_value '表 badge_tiers 已删除' "SELECT to_regclass('public.badge_tiers') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '外键 badges_tier_fkey 已删除' "SELECT NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_tier_fkey')" t || down_failures=$((down_failures + 1))
  assert_value '索引 badges_tier_idx 已删除' "SELECT to_regclass('public.badges_tier_idx') IS NULL" t || down_failures=$((down_failures + 1))
  assert_value '旧 CHECK badges_tier_check 已恢复' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_tier_check' AND contype = 'c')" t || down_failures=$((down_failures + 1))
  assert_value '没有徽章落在内置三档之外' "SELECT NOT EXISTS (SELECT 1 FROM badges WHERE tier NOT IN ('bronze','silver','gold'))" t || down_failures=$((down_failures + 1))
  assert_value "goose 版本回退到 202611100004" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" 202611100004 || down_failures=$((down_failures + 1))
elif [[ "$TARGET_VERSION" == "202611100004" ]]; then
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
if [[ "$TARGET_VERSION" == "202611120003" ]]; then
  assert_value '表 badge_rule_backfill_runs 存在' "SELECT to_regclass('public.badge_rule_backfill_runs') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '索引 badge_rule_backfill_runs_badge_started_idx 存在' "SELECT to_regclass('public.badge_rule_backfill_runs_badge_started_idx') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '索引 badge_rule_backfill_runs_running_idx 存在' "SELECT to_regclass('public.badge_rule_backfill_runs_running_idx') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '外键指向 badges 且级联删除' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badge_rule_backfill_runs_badge_id_fkey' AND confrelid = 'public.badges'::regclass AND confdeltype = 'c')" t || up_failures=$((up_failures + 1))
  assert_value 'status CHECK 只认三态' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'public.badge_rule_backfill_runs'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%running%' AND pg_get_constraintdef(oid) LIKE '%completed%' AND pg_get_constraintdef(oid) LIKE '%failed%')" t || up_failures=$((up_failures + 1))
  # owner 对齐（计划 §13#16）：owner 与 public.badges 不一致的对象永远不会被
  # kernel 收敛，运行期租约签发时报 extension database physical resource conflict。
  assert_value '表 owner 与 public.badges 一致' "SELECT pg_get_userbyid(c.relowner) = (SELECT pg_get_userbyid(b.relowner) FROM pg_class b WHERE b.oid = 'public.badges'::regclass) FROM pg_class c WHERE c.oid = 'public.badge_rule_backfill_runs'::regclass" t || up_failures=$((up_failures + 1))
  assert_value "goose 版本回到 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || up_failures=$((up_failures + 1))
elif [[ "$TARGET_VERSION" == "202611120002" ]]; then
  assert_value '表 badge_facts 存在' "SELECT to_regclass('public.badge_facts') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '表 user_fact_counters 存在' "SELECT to_regclass('public.user_fact_counters') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '表 badge_rules_rollback 存在' "SELECT to_regclass('public.badge_rules_rollback') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '索引 badge_facts_dedupe_idx 存在' "SELECT to_regclass('public.badge_facts_dedupe_idx') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '索引 badge_facts_window_idx 存在' "SELECT to_regclass('public.badge_facts_window_idx') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '列 badges.rule 存在' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'badges' AND column_name = 'rule')" t || up_failures=$((up_failures + 1))
  assert_value '列 badges.requirement_summary_en 存在' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'badges' AND column_name = 'requirement_summary_en')" t || up_failures=$((up_failures + 1))
  assert_value '全部自动徽章都转成了规则' "SELECT count(*) FROM public.badges WHERE grant_mode = 'automatic' AND rule IS NOT NULL" "$EXPECTED_AUTO_BADGES" || up_failures=$((up_failures + 1))
  assert_value '手动徽章没有规则' "SELECT count(*) FROM public.badges WHERE grant_mode = 'manual' AND rule IS NOT NULL" 0 || up_failures=$((up_failures + 1))
  assert_value '回滚快照行数 = 自动徽章数' "SELECT count(*) FROM public.badge_rules_rollback" "$EXPECTED_AUTO_BADGES" || up_failures=$((up_failures + 1))
  assert_value '规则是 activity.count + window.all 单条件' "SELECT count(*) FROM public.badges WHERE rule->'root'->>'type' = 'activity.count' AND rule->'root'->'params'->'window'->>'kind' = 'all' AND rule->'root'->'cmp'->>'op' = 'gte'" "$EXPECTED_AUTO_BADGES" || up_failures=$((up_failures + 1))
  assert_value '新 CHECK 只认 rule' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_rule_check' AND pg_get_constraintdef(oid) LIKE '%rule IS NOT NULL%' AND pg_get_constraintdef(oid) NOT LIKE '%threshold > 0%')" t || up_failures=$((up_failures + 1))
  assert_value '视图 forum_badges 输出 rule' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'sforum_core_v1' AND table_name = 'forum_badges' AND column_name = 'rule')" t || up_failures=$((up_failures + 1))
  assert_value '视图 forum_badges 输出 requirement_summary_zh' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'sforum_core_v1' AND table_name = 'forum_badges' AND column_name = 'requirement_summary_zh')" t || up_failures=$((up_failures + 1))
  assert_value '视图 forum_user_badge_facts 存在' "SELECT to_regclass('sforum_core_v1.forum_user_badge_facts') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '事实视图不输出 delta/dedupe_key/source_extension_id' "SELECT count(*) FROM information_schema.columns WHERE table_schema = 'sforum_core_v1' AND table_name = 'forum_user_badge_facts' AND column_name IN ('delta','dedupe_key','source_extension_id')" 0 || up_failures=$((up_failures + 1))
  assert_value "内置徽章仍为 ${EXPECTED_BADGES} 个" "SELECT count(*) FROM public.badges" "$EXPECTED_BADGES" || up_failures=$((up_failures + 1))
  assert_value "goose 版本回到 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || up_failures=$((up_failures + 1))
elif [[ "$TARGET_VERSION" == "202611120001" ]]; then
  assert_value '表 badge_tiers 存在' "SELECT to_regclass('public.badge_tiers') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '内置等级三行' "SELECT count(*) FROM public.badge_tiers" 3 || up_failures=$((up_failures + 1))
  assert_value 'gold 位次为 0（最高档）' "SELECT position FROM public.badge_tiers WHERE key = 'gold'" 0 || up_failures=$((up_failures + 1))
  assert_value '外键 badges_tier_fkey 存在' "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_tier_fkey' AND contype = 'f')" t || up_failures=$((up_failures + 1))
  assert_value '旧 CHECK badges_tier_check 已移除' "SELECT NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'badges_tier_check' AND contype = 'c')" t || up_failures=$((up_failures + 1))
  assert_value '索引 badges_tier_idx 存在' "SELECT to_regclass('public.badges_tier_idx') IS NOT NULL" t || up_failures=$((up_failures + 1))
  assert_value '内置徽章仍为 12 个（回滚不丢数据）' "SELECT count(*) FROM public.badges" "$EXPECTED_BADGES" || up_failures=$((up_failures + 1))
  assert_value "goose 版本回到 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || up_failures=$((up_failures + 1))
elif [[ "$TARGET_VERSION" == "202611100004" ]]; then
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

if [[ "$TARGET_VERSION" == "202611120002" ]]; then
  # X-04b：多条件规则无法反推 metric/threshold，down 必须**拒绝**而不是猜。
  # 负向断言放在最后：它会把 resident 的规则临时改成多条件，再改回来。
  step "X-04b 多条件规则下 down 拒绝并保留原状态"
  original_rule="$(psql_value "SELECT rule::text FROM public.badges WHERE slug = 'resident'")" || die 9 '读取 resident 原始规则失败。'
  multi_rule='{"v":1,"root":{"op":"all","children":[{"type":"identity.email_verified"},{"type":"activity.count","params":{"metric":"topic_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":1}}]}}'
  psql_value "UPDATE public.badges SET rule = '${multi_rule}'::jsonb WHERE slug = 'resident'" >/dev/null \
    || die 9 '造多条件规则失败。'
  refuse_log="$work_dir/down-refuse.log"
  if step_helper down "$TARGET_VERSION" >"$refuse_log" 2>&1; then
    die 9 '多条件规则下 down 竟然成功了——Down 段缺少不可反推规则的拒绝检查。'
  fi
  if ! grep -q 'Refusing to roll back badge rules' "$refuse_log"; then
    printf '%s\n' "$(cat "$refuse_log")" >&2
    die 9 'down 失败了，但不是因为多条件规则拒绝（错误信息里没有预期文案）。'
  fi
  ok 'down 被拒绝，错误信息为多条件规则拒绝'
  # 「同一事务」的证明：拒绝之后库必须原封不动（rule 列还在、goose 版本没退）。
  assert_value '拒绝后 badges.rule 列仍在（无半回滚残留）' "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'badges' AND column_name = 'rule')" t || die 9 'down 拒绝时留下了半回滚残留。'
  assert_value "拒绝后 goose 版本仍是 $TARGET_VERSION" "SELECT max(version_id) FROM goose_db_version WHERE is_applied" "$TARGET_VERSION" || die 9 'down 拒绝时 goose 版本已被回退。'
  psql_value "UPDATE public.badges SET rule = '${original_rule}'::jsonb WHERE slug = 'resident'" >/dev/null \
    || die 9 '恢复 resident 原始规则失败。'
  ok 'resident 规则已恢复'

  # X-04c（P-02 的核心）：规则被改成「另一条可逆规则」时，down 必须按**当前**规则
  # 反推，而不是拿快照里的旧值覆盖。这里故意让 metric/threshold 停在旧值
  # （days_visited/30）不动，只改 rule；若 Down 写成 COALESCE(legacy, ...)，
  # 断言会看到 days_visited/30 而不是 comment_count/5。
  step "X-04c 规则改过但可逆：down 按当前规则反推"
  reversible_rule='{"v":1,"root":{"type":"activity.count","params":{"metric":"comment_count","window":{"kind":"all"}},"cmp":{"op":"gte","value":5}}}'
  psql_value "UPDATE public.badges SET rule = '${reversible_rule}'::jsonb WHERE slug = 'resident'" >/dev/null \
    || die 9 '造可逆规则失败。'
  if ! step_helper down "$TARGET_VERSION" >"$work_dir/down-reversible.log" 2>&1; then
    printf '%s\n' "$(cat "$work_dir/down-reversible.log")" >&2
    die 9 '可逆规则下 down 本应成功，却失败了。'
  fi
  ok '可逆规则下 down 成功'
  assert_value 'down 后 metric 取当前规则（comment_count）' "SELECT metric FROM public.badges WHERE slug = 'resident'" comment_count || die 9 'down 没按当前规则反推 metric（用了陈旧快照？）'
  assert_value 'down 后 threshold 取当前规则（5）' "SELECT threshold FROM public.badges WHERE slug = 'resident'" 5 || die 9 'down 没按当前规则反推 threshold（用了陈旧快照？）'
  # 复原：先把内置值写回 metric/threshold，再 up（up 会从这两列重新生成规则）。
  psql_value "UPDATE public.badges SET metric = 'days_visited', threshold = 30 WHERE slug = 'resident'" >/dev/null \
    || die 9 '复原 metric/threshold 失败。'
  if ! (cd "$API_DIR" && go run ./cmd/migrate) >"$work_dir/reup.log" 2>&1; then
    printf '%s\n' "$(tail -20 "$work_dir/reup.log")" >&2
    die 7 'X-04c 复原时再次 up 失败。'
  fi
  assert_value '复原后规则回到内置值' "SELECT (rule->'root'->'params'->>'metric') || '/' || (rule->'root'->'cmp'->>'value') FROM public.badges WHERE slug = 'resident'" days_visited/30 || die 9 '复原后规则不是内置值。'
  ok '迁移链仍在最新版本'
fi

printf '\n全部通过：%s 可 down 回干净状态并重新 up 重建。\n' "$TARGET_VERSION"
