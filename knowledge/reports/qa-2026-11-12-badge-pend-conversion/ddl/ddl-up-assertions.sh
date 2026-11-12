#!/usr/bin/env bash
# A-01 / B-04 的 **Up 侧** DDL 断言：对 `scripts/verify-migration-reversible.sh`
# 留下的一次性集群（SFORUM_VERIFY_KEEP_CLUSTER=1）做只读校验。
#
# 那个脚本已经覆盖 Down 侧（表/两索引/序列消失 + goose 版本退回 202611120002）
# 与再次 up；它没有 Up 侧结构断言，本脚本补的就是这一半。
#
# 用法：
#   CID=sforum-migration-verify-<hex> bash ddl-up-assertions.sh
# 环境变量：
#   CID          必填，一次性集群容器名或 id
#   PG_USER      default sforum
#   PG_DB        default sforum_migration_verify
set -uo pipefail

CID="${CID:?need CID}"
PG_USER="${PG_USER:-sforum}"
PG_DB="${PG_DB:-sforum_migration_verify}"

pass_n=0
fail_n=0

sqlv() { docker exec -e PGPASSWORD=sforum "$CID" psql -U "$PG_USER" -d "$PG_DB" -v ON_ERROR_STOP=1 -tAq -c "$1" 2>&1; }

# expect <id> <label> <want> <sql>
expect() {
  local id=$1 label=$2 want=$3 sql=$4 got
  got="$(sqlv "$sql")"
  if [[ "$got" == "$want" ]]; then
    printf 'PASS\t%s\t%s\t%s\n' "$id" "$label" "$got"
    pass_n=$((pass_n + 1))
  else
    printf 'FAIL\t%s\t%s\twant=[%s] got=[%s]\n' "$id" "$label" "$want" "$got"
    fail_n=$((fail_n + 1))
  fi
}

# 每个对象一条独立的「存在性」闸门：环境不对时后面的值级断言会串成一堆假红。
printf 'INFO\tcontainer\t%s\n' "$CID"
printf 'INFO\tpsql_version\t%s\n' "$(sqlv 'SHOW server_version')"
printf 'INFO\tguard\t%s (must be f: dev guard absent)\n' "$(sqlv "SELECT to_regclass('public.sforum_dev_guard') IS NOT NULL")"

# ---- A-01 表存在 ----
expect A-01a '表 public.badge_rule_backfill_runs 存在' t \
  "SELECT to_regclass('public.badge_rule_backfill_runs') IS NOT NULL"

# ---- A-01 列集合 == 冻结集合（缺列红，多列也红）----
expect A-01b '列集合 == 冻结 10 列' \
  'badge_id,batch,error,failed_cursor_user_id,finished_at,id,last_user_id,started_at,status,updated_at' \
  "SELECT string_agg(column_name, ',' ORDER BY column_name) FROM information_schema.columns
    WHERE table_schema='public' AND table_name='badge_rule_backfill_runs'"

# ---- A-01 序列存在且真的绑定 id 列 ----
expect A-01c '序列 badge_rule_backfill_runs_id_seq 存在' t \
  "SELECT to_regclass('public.badge_rule_backfill_runs_id_seq') IS NOT NULL"
expect A-01d 'id 列的 serial 绑定指向该序列' 'public.badge_rule_backfill_runs_id_seq' \
  "SELECT pg_get_serial_sequence('public.badge_rule_backfill_runs','id')"

# ---- A-01 两个索引都在，且定义值级正确 ----
expect A-01e '索引 _badge_started_idx 定义含 (badge_id, started_at DESC, id DESC)' \
  'yes' \
  "SELECT CASE WHEN indexdef LIKE '%(badge_id, started_at DESC, id DESC)%' THEN 'yes' ELSE 'no' END
     FROM pg_indexes WHERE schemaname='public'
      AND indexname='badge_rule_backfill_runs_badge_started_idx'"
expect A-01f '索引 _running_idx 是 status=running 的局部索引' 'yes' \
  "SELECT CASE WHEN indexdef LIKE '%WHERE (status = ''running''::text)%' THEN 'yes' ELSE 'no' END
     FROM pg_indexes WHERE schemaname='public'
      AND indexname='badge_rule_backfill_runs_running_idx'"
expect A-01g '索引总数 == 3（主键 + 两业务索引，多一个即红）' 3 \
  "SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND tablename='badge_rule_backfill_runs'"

# ---- A-01 / B-04 FK 指向 public.badges 且 ON DELETE CASCADE ----
expect A-01h 'FK 指向 public.badges（regclass 全等）' 'yes' \
  "SELECT CASE WHEN confrelid = 'public.badges'::regclass THEN 'yes' ELSE 'no' END
     FROM pg_constraint
    WHERE conrelid='public.badge_rule_backfill_runs'::regclass AND contype='f'"
expect A-01i 'FK 列 == badge_id' badge_id \
  "SELECT a.attname FROM pg_constraint c
     JOIN unnest(c.conkey) k(attnum) ON true
     JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum
    WHERE c.conrelid='public.badge_rule_backfill_runs'::regclass AND c.contype='f'"
expect B-04 'confdeltype == c（CASCADE），删 badges 行不留孤儿' c \
  "SELECT confdeltype::text FROM pg_constraint
    WHERE conrelid='public.badge_rule_backfill_runs'::regclass AND contype='f'"

# ---- A-01 status CHECK 恰为三值 ----
expect A-01j 'status CHECK 的枚举值集合恰为 completed,failed,running（少一个红、多一个也红）' \
  'completed,failed,running' \
  "SELECT string_agg(DISTINCT m.arr[1], ',' ORDER BY m.arr[1]) FROM pg_constraint c
     CROSS JOIN LATERAL regexp_matches(pg_get_constraintdef(c.oid), '''([a-z_]+)''::text', 'g') AS m(arr)
    WHERE c.conrelid='public.badge_rule_backfill_runs'::regclass
      AND c.contype='c' AND pg_get_constraintdef(c.oid) LIKE '%status%'"
expect A-01k 'status 默认值 == running' "'running'::text" \
  "SELECT column_default FROM information_schema.columns
    WHERE table_schema='public' AND table_name='badge_rule_backfill_runs' AND column_name='status'"

# ---- A-01 owner 与 public.badges 同源（表 / 序列 / 两索引）----
expect A-01l '表 owner == badges owner' t \
  "SELECT (SELECT relowner FROM pg_class WHERE oid='public.badge_rule_backfill_runs'::regclass)
        = (SELECT relowner FROM pg_class WHERE oid='public.badges'::regclass)"
expect A-01m '序列 owner == badges owner' t \
  "SELECT (SELECT relowner FROM pg_class WHERE oid='public.badge_rule_backfill_runs_id_seq'::regclass)
        = (SELECT relowner FROM pg_class WHERE oid='public.badges'::regclass)"
expect A-01n '两索引 owner == badges owner' t \
  "SELECT bool_and((SELECT relowner FROM pg_class WHERE oid=i.indexrelid)
                 = (SELECT relowner FROM pg_class WHERE oid='public.badges'::regclass))
     FROM pg_index i WHERE i.indrelid='public.badge_rule_backfill_runs'::regclass"
printf 'INFO\towner_actual\t%s (badges owner: %s)\n' \
  "$(sqlv "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.badge_rule_backfill_runs'::regclass")" \
  "$(sqlv "SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='public.badges'::regclass")"

printf '\nSUMMARY pass=%d fail=%d\n' "$pass_n" "$fail_n"
[[ "$fail_n" -eq 0 ]] || exit 1
