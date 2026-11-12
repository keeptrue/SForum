-- +goose Up
-- +sforum OnlineSafe
-- 徽章规则回扫（`badges.backfill_rules`）的运行状态表。
--
-- 为什么需要它：回扫是一条「一页一个 job」的链（见 app/Jobs/Badges/backfill_rules.go），
-- 链断在半路时运营侧只拿到过入队时的 `queued: N` 回执，之后没有任何地方能看到
-- 「跑到第几个用户」「哪一页失败了」「是不是根本没入队」。日志能看，但要翻 worker
-- 日志、还得自己拼游标。这张表把状态变成可查询的一行。
--
-- 状态**只由 job 侧写**（入队侧建 running 行、每页写游标/结束/失败），
-- 绝不从 `user_badges` 或授予数反推——那是「用结果猜过程」，规则不满足时反推出来的
-- 进度会倒退，而真实回扫是在前进的。
--
-- 一张表同时承担「进度」与「最近一次结论」：每个徽章保留多行历史，读端点取
-- `started_at DESC, id DESC` 的第一行。刻意不做唯一约束（一次回扫一行，重算再开一行），
-- 也不做 TTL 清理——这是运维态的低频小表，保留历史比省空间更有用。
--
-- OnlineSafe 的依据：纯新增表 + 纯新增索引，不改既有表；并发 DDL 不重写任何数据。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS badge_rule_backfill_runs (
  id BIGSERIAL PRIMARY KEY,
  -- 徽章被删则回扫行一起消失：读端点 JOIN badges，孤儿行既查不到也没意义。
  badge_id BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'failed')),
  -- 最后一个**成功**页的游标（该页最后处理的 user_id）。0 表示还没跑完任何一页。
  last_user_id BIGINT NOT NULL DEFAULT 0,
  -- 失败页的 afterUserID（job args 里的 CursorUserID）。非失败状态一律 0，
  -- 这样「重试成功」不会留下上一次失败的位置让人误判。
  failed_cursor_user_id BIGINT NOT NULL DEFAULT 0,
  -- 本页使用的批量大小（排查「是不是 batch 太大超时」用）。NULL = 未知/旧行。
  batch INT,
  -- 失败原因原文（截断 500 字符，截断在 Go 侧做，这里不再加长度约束）。
  error TEXT NOT NULL DEFAULT '',
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- 只有 completed 会写 finished_at；failed 留空是因为 river 还会重试，
  -- 失败不等于「结束」。
  finished_at TIMESTAMPTZ
);

-- 读端点取「每个徽章的最新一行」；这个索引直接支撑 DISTINCT ON (badge_id) 的排序。
CREATE INDEX IF NOT EXISTS badge_rule_backfill_runs_badge_started_idx
  ON badge_rule_backfill_runs (badge_id, started_at DESC, id DESC);

-- 局部索引只为「有没有卡住的回扫」这类运维查询服务，比全表索引小得多。
CREATE INDEX IF NOT EXISTS badge_rule_backfill_runs_running_idx
  ON badge_rule_backfill_runs (badge_id) WHERE status = 'running';

COMMENT ON TABLE badge_rule_backfill_runs IS
  'Per-badge progress of the badges.backfill_rules chain (one row per run; job side is the only writer).';
COMMENT ON COLUMN badge_rule_backfill_runs.last_user_id IS
  'Cursor (user_id) of the last successfully processed page; 0 = no page finished yet.';
COMMENT ON COLUMN badge_rule_backfill_runs.failed_cursor_user_id IS
  'afterUserID of the failed page; reset to 0 on any success so a retry does not look stuck.';

-- 新对象与既有 Core 对象同源：复制 `badges` 的 owner。
--
-- 为什么必须：扩展平台 kernel 要求 `public` / `sforum_core_v1` 里每个对象的 owner
-- 都等于 Core owner role，而它的收敛步骤只搬「owner 恰好等于运行时角色」的对象
-- （`loadExtensionDatabaseKernelRelations` 的 `relowner = roleName` 过滤）。owner 不
-- 一致的迁移对象永远不会被收敛，运行期租约签发时报
-- `extension database physical resource conflict`。见计划 §13#16。
--
-- 从 `badges` 抄而不是写死角色名：它同域、同生命周期、且一定已被收敛过。
-- +goose StatementBegin
DO $$
DECLARE target_owner text;
BEGIN
  SELECT pg_get_userbyid(classes.relowner) INTO target_owner
  FROM pg_class AS classes
  JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
  WHERE namespaces.nspname = 'public' AND classes.relname = 'badges';

  IF target_owner IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.badge_rule_backfill_runs OWNER TO %I', target_owner);
    EXECUTE format('ALTER SEQUENCE public.badge_rule_backfill_runs_id_seq OWNER TO %I', target_owner);
    EXECUTE format('ALTER INDEX public.badge_rule_backfill_runs_badge_started_idx OWNER TO %I', target_owner);
    EXECUTE format('ALTER INDEX public.badge_rule_backfill_runs_running_idx OWNER TO %I', target_owner);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- 纯新增表：Down 段只需要拆干净（索引随表消失，显式 DROP 只为可读性）。
DROP INDEX IF EXISTS badge_rule_backfill_runs_running_idx;
DROP INDEX IF EXISTS badge_rule_backfill_runs_badge_started_idx;
DROP TABLE IF EXISTS badge_rule_backfill_runs;
