-- +goose Up
-- +sforum OnlineSafe
-- 庆祝展示层（presentation.celebration）+ 每用户庆祝游标。
--
-- 这一刀只做「展示层」：徽章授予照旧写 `notifications` 行（类型 `badge_granted`），
-- 这里补两件事：
--
--   1. 给 `badge_granted` 的描述符打 `presentation.celebration = true`。
--      放在 presentation jsonb 里而不是加新列：插件也要能声明庆祝类型
--      （`notificationTypes[].celebration`），读面若用新列就得同时维护
--      「Core 列」与「插件 presentation」两套判据。jsonb 合并是原子的，
--      且 `presentation || '{"celebration":true}'` 保留既有 icon 等键。
--
--   2. 建每用户的庆祝游标表。**首访不回放历史**靠它实现：没有行 → 返回
--      `initialized=false` + 空 items，并把游标种子指向该用户当前最大通知 id。
--
-- 刻意不建索引在 notifications 上加「celebration」过滤：读面已经限定
-- `recipient_user_id`（走 notifications_recipient_... 的既有索引）+ `id > cursor`，
-- 且 7 天窗口把候选压得很小；为一次展示层查询新增大索引不划算。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 只标 badge_granted：Core 里只有徽章授予是「值得庆祝一次」的事件。
UPDATE notification_type_descriptors
SET presentation = presentation || '{"celebration":true}'::jsonb,
    updated_at = now()
WHERE type = 'badge_granted';

CREATE TABLE IF NOT EXISTS user_celebration_cursors (
  user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  -- 已确认到的通知 id。GREATEST 语义写入（见 celebrations_store.go），
  -- 因此这里只会单调递增；0 = 该用户还没有任何通知被确认过。
  cursor_notification_id BIGINT NOT NULL DEFAULT 0 CHECK (cursor_notification_id >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE user_celebration_cursors IS
  'Per-user celebration cursor (monotonic notification id). Missing row = celebration layer not initialized yet; history is never replayed on first visit.';
COMMENT ON COLUMN user_celebration_cursors.cursor_notification_id IS
  'Highest celebration notification id the user has acknowledged (GREATEST upsert; never moves backwards).';

-- 新对象与既有 Core 对象同源：复制 `notifications` 的 owner。
--
-- 为什么必须：扩展平台 kernel 要求 `public` / `sforum_core_v1` 里每个对象的 owner
-- 都等于 Core owner role，而它的收敛步骤只搬「owner 恰好等于运行时角色」的对象。
-- owner 不一致的迁移对象永远不会被收敛，运行期租约签发时报
-- `extension database physical resource conflict`（见 202611120003 的同一段注释）。
--
-- 从 `notifications` 抄而不是写死角色名：它同域、同生命周期、且一定已被收敛过。
-- +goose StatementBegin
DO $$
DECLARE target_owner text;
BEGIN
  SELECT pg_get_userbyid(classes.relowner) INTO target_owner
  FROM pg_class AS classes
  JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
  WHERE namespaces.nspname = 'public' AND classes.relname = 'notifications';

  IF target_owner IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.user_celebration_cursors OWNER TO %I', target_owner);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- 先拆表再退标记：反过来会让「表还在、读面却不再返回任何条目」的中间态
-- 在回滚过程中持续一段时间。
DROP TABLE IF EXISTS user_celebration_cursors;

UPDATE notification_type_descriptors
SET presentation = presentation - 'celebration',
    updated_at = now()
WHERE type = 'badge_granted';
