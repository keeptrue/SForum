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

COMMENT ON COLUMN public.user_trust_levels.revision IS
  'Optimistic-lock revision. Incremented on every successful CAS write; expectedRevision=0 means "no row yet". Not a trust signal.';

-- +goose Down
ALTER TABLE public.user_trust_levels DROP COLUMN IF EXISTS revision;
