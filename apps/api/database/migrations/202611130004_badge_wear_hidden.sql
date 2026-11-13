-- +goose Up
-- +sforum OnlineSafe
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 「不佩戴徽章」hidden 展示态：`top_badge_hidden = TRUE` 表示用户主动选择
-- 什么都不戴。三列（top_badge_id / top_badge_manual / top_badge_hidden）必须
-- 同源写入：隐藏 = `top_badge_id=NULL, top_badge_manual=FALSE, top_badge_hidden=TRUE`，
-- 由 HideWornBadge 一条 UPDATE 完成，重算对 hidden 行让路（不回落写入）。
--
-- 为什么是布尔而不是把「不佩戴」编码成某个哨兵徽章 id：哨兵会污染排序、目录与
-- 通知目标的口径，而这一位只被三处读写（隐藏、解除、重算让路）。
-- DEFAULT FALSE 让存量行自动落在「展示自动推导」，与旧语义一致。
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS top_badge_hidden BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN users.top_badge_hidden IS
  'TRUE when the user chose to wear nothing: top_badge_id is NULL and recomputation must not fall back. Cleared by wearing a badge (PUT) or clearing the slot (DELETE).';

-- +goose Down
-- 删列即回到「只有 auto/manual 两态」：隐藏中的用户（top_badge_id 已是 NULL）
-- 会立即按重算回落到自动推导的最佳徽章。这是可接受的回滚代价——没有这一位
-- 就无法表达「用户选择不戴」，保留 NULL 反而是假状态。
ALTER TABLE users
  DROP COLUMN IF EXISTS top_badge_hidden;
