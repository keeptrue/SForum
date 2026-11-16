-- +goose Up
-- +sforum OnlineSafe
-- 信任等级变更账本加乐观锁版本列（安全审计 P3-10）。
--
-- 背景：`Recomputer.RecomputeUser` 是「读账本 → 求值 → 写账本 → 发事件」四步，
-- 原本写账本是无条件 upsert。手动批量重算与单用户 job 并发同一用户时，两边都带着
-- 旧的 FromLevel 发事件：账本最终一致，但下游按事件发通知/徽章会重复或错序。
--
-- 版本口径：revision 从 1 开始，每次 CAS 成功 +1。用 1 而不是 0 作为既有行/新行的
-- 起始值，是为了让 `expectedRevision = 0` 唯一地表示「调用方认为这行还不存在」，
-- 从而与「行存在但版本恰好是 0」区分开——后者会让首次写入的 INSERT 语义被误用。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 纯元数据变更（PG 11+ 常量默认值不重写表），历史行自动取 1。
ALTER TABLE public.user_trust_levels
  ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 0 专指「调用方认为还没有行」，账本里不允许出现 —— 用约束把这条不变量钉在库上。
-- CHECK **不能内联**进 ADD COLUMN：那会在 ACCESS EXCLUSIVE 下扫全表校验，把账本的
-- 读写挡到 statement_timeout。NOT VALID 只登记定义，VALIDATE 单独扫表且锁级别
-- 不阻塞读写（与 202611150001_moderation_report_revision.sql 同一形态）。
--
-- **幂等前提（改这段前先读）**：本表被 202611140002 钉在 `public`
-- （`CREATE TABLE IF NOT EXISTS public.user_trust_levels`），而 schema 隔离的集成测试
-- 会把整条迁移链反复应用到**同一个 public 表**上。`ADD COLUMN IF NOT EXISTS` 天然幂等，
-- 但 PG **没有** `ADD CONSTRAINT IF NOT EXISTS`：重复应用会撞 42710
-- (`constraint ... already exists`)。所以约束必须用 `pg_constraint` 存在性检查登记；
-- `VALIDATE CONSTRAINT` 对已存在的已验证约束重复执行是安全的。
-- 同一口径也解释了 202611140002 为什么用 `CREATE TABLE IF NOT EXISTS`：这条链必须能在
-- 已应用的 public 表上重放。不要改成先 `DROP CONSTRAINT IF EXISTS` —— 那会在真库上
-- 短暂移除约束（破坏性），而不是幂等。
-- （对照：202611150001 的 `moderation_reports` 建表**不带** schema 前缀，随 search_path
--  落在隔离 schema 里，每个隔离库各有一份，因此不存在这条重复应用风险。）
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'user_trust_levels_revision_positive'
      AND conrelid = 'public.user_trust_levels'::regclass
  ) THEN
    ALTER TABLE public.user_trust_levels
      ADD CONSTRAINT user_trust_levels_revision_positive CHECK (revision > 0) NOT VALID;
  END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE public.user_trust_levels VALIDATE CONSTRAINT user_trust_levels_revision_positive;

COMMENT ON COLUMN public.user_trust_levels.revision IS
  'Optimistic-lock revision. Incremented on every successful CAS write; expectedRevision=0 means "no row yet". Not a trust signal.';

-- +goose Down
-- 约束引用该列，删列时随列一起消失，无需单独 DROP CONSTRAINT。
ALTER TABLE public.user_trust_levels DROP COLUMN IF EXISTS revision;
