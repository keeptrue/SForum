-- +goose Up
-- +sforum OnlineSafe
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 自由佩戴：`users.top_badge_id` 的语义从「自动推导的最高级徽章」改为
-- 「当前展示的徽章」，新增 `top_badge_manual` 记录它是不是用户自己选的。
--
-- 为什么需要这一列：只改 `top_badge_id` 无法区分「自动推导的结果」与「用户选择」，
-- 于是下一次评估/重算会把用户的选择按排序覆盖掉——症状是「佩戴了，刷新就变回去」，
-- 而且没有任何报错。有了这一位，重算只在「用户选择仍有效」时让路。
--
-- 为什么是布尔而不是 `display_source TEXT`：状态只有两种（用户选的 / 系统推导的），
-- 而第三个状态（「用户选了但已失效」）在有效期为 0 的瞬间就被回落逻辑清成 FALSE，
-- 不需要持久化。多一个枚举值只会多一处「忘了处理它」。
ALTER TABLE users
  ADD COLUMN IF NOT EXISTS top_badge_manual BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN users.top_badge_id IS
  'Currently displayed badge. Semantics changed in 2026-11-13: it is either the user''s manual pick (top_badge_manual = TRUE) or the auto-derived best badge.';
COMMENT ON COLUMN users.top_badge_manual IS
  'TRUE when top_badge_id is a user pick rather than derived from badge ordering. Cleared automatically when the pick stops being valid (revoked/disabled/deleted badge or disabled tier).';

-- +goose Down
-- 删列即回到「top_badge_id 只表示自动推导」的旧语义；已经佩戴过的用户会保留
-- 那枚徽章作为展示徽章，直到下一次重算/评估把它按排序改回来。这是可接受的回滚
-- 代价：没有这一位就无法表达「用户选择」，写入它反而是假状态。
ALTER TABLE users
  DROP COLUMN IF EXISTS top_badge_manual;
