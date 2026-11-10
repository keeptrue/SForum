-- +goose Up
-- +sforum OnlineSafe
-- 收藏（bookmark）是「用户 × 内容」关系域的第二个成员：单行幂等、可切换
-- 公开/私密、公开计数反规范化。与 content_reactions 同姿态：多态 target_id
-- 刻意不加外键（完整性由服务层事务保证，内容删除是软删，收藏行保留）。
-- visibility 默认 private：收藏首先是个人行为，公开是显式选择。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS content_bookmarks (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  visibility TEXT NOT NULL DEFAULT 'private',
  remind_at TIMESTAMPTZ,
  reminded_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_bookmarks_target_type_check CHECK (target_type IN ('topic', 'comment')),
  CONSTRAINT content_bookmarks_visibility_check CHECK (visibility IN ('private', 'public')),
  CONSTRAINT content_bookmarks_target_positive CHECK (target_id > 0),
  CONSTRAINT content_bookmarks_unique UNIQUE (user_id, target_type, target_id)
);

-- 公开名单读路径：按目标取最近的公开收藏者。
CREATE INDEX IF NOT EXISTS content_bookmarks_public_idx
  ON content_bookmarks (target_type, target_id, created_at DESC, id DESC)
  WHERE visibility = 'public';

-- 本人列表读路径：keyset/分页取最近收藏（含私密行，属于行主本人可见）。
CREATE INDEX IF NOT EXISTS content_bookmarks_user_idx
  ON content_bookmarks (user_id, created_at DESC, id DESC);

-- 到期提醒扫描：只扫待提醒的行，提醒完成后自然退出索引。
CREATE INDEX IF NOT EXISTS content_bookmarks_due_idx
  ON content_bookmarks (remind_at)
  WHERE remind_at IS NOT NULL AND reminded_at IS NULL;

-- 公开收藏计数列：列表与详情读路径直接取值，避免每次 COUNT(*)。
-- 只统计 visibility='public' 的行，由服务层在写行的同一事务里维护。
ALTER TABLE topics ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE comments ADD COLUMN IF NOT EXISTS public_bookmark_count BIGINT NOT NULL DEFAULT 0;

-- 非负约束用 NOT VALID + VALIDATE，避免对大表做阻塞式全表校验。
ALTER TABLE topics ADD CONSTRAINT topics_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID;
ALTER TABLE comments ADD CONSTRAINT comments_public_bookmark_count_nonnegative CHECK (public_bookmark_count >= 0) NOT VALID;
ALTER TABLE topics VALIDATE CONSTRAINT topics_public_bookmark_count_nonnegative;
ALTER TABLE comments VALIDATE CONSTRAINT comments_public_bookmark_count_nonnegative;

-- 稳定读视图：可见性谓词逐字镜像 sforum_core_v1.forum_reactions，
-- 不允许在插件数据面上另写一套可见性规则。
-- 只输出公开收藏行；remind_at / reminded_at 是用户私有计划数据，永不外泄。
-- 用 OR REPLACE：视图落在固定 Host schema（非 search_path 隔离的测试 schema），
-- 在 schema 隔离的测试库里重放整条迁移链时会命中同一对象；定义相同，替换即收敛。
CREATE OR REPLACE VIEW sforum_core_v1.forum_bookmarks
WITH (security_barrier = true, security_invoker = false) AS
SELECT
  bookmarks.id,
  bookmarks.target_type,
  bookmarks.target_id,
  bookmarks.visibility,
  bookmarks.created_at,
  bookmarks.updated_at,
  users.id AS user_id,
  users.username,
  users.display_name
FROM public.content_bookmarks AS bookmarks
JOIN public.users ON users.id = bookmarks.user_id
WHERE users.status = 'active'
  AND bookmarks.visibility = 'public'
  AND (
    (
      bookmarks.target_type = 'topic'
      AND EXISTS (
        SELECT 1
        FROM public.topics
        JOIN public.categories ON categories.id = topics.category_id
        WHERE topics.id = bookmarks.target_id
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
    OR (
      bookmarks.target_type = 'comment'
      AND EXISTS (
        SELECT 1
        FROM public.comments
        JOIN public.topics ON topics.id = comments.topic_id
        JOIN public.categories ON categories.id = topics.category_id
        WHERE comments.id = bookmarks.target_id
          AND comments.status = 'active'
          AND comments.deleted_at IS NULL
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
  )
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_bookmarks IS
  'SForum core view v1: public bookmark relations on visible topics/comments; no private bookmarks, no private targets, no reminder fields.';

-- 通知类型：收藏提醒是用户主动设定的计划动作，站内与邮件默认开；
-- web_push 默认关（与 reaction 同姿态，避免默认打扰）。
INSERT INTO notification_type_descriptors (
  type, contract_version, payload_version, category, active, required,
  presentation, target_contract
) VALUES (
  'bookmark_reminder', 1, 1, 'engagement', TRUE, FALSE,
  '{"icon":"i-lucide-bookmark"}'::jsonb,
  '{"kind":"entity","id":"core.entity.notification_target"}'::jsonb
)
ON CONFLICT (type) DO NOTHING;

INSERT INTO notification_type_policies (
  type, channel, enabled, recommended_enabled, user_configurable, required
) VALUES
  ('bookmark_reminder', 'in_app', TRUE, TRUE, TRUE, FALSE),
  ('bookmark_reminder', 'email', TRUE, TRUE, TRUE, FALSE),
  ('bookmark_reminder', 'web_push', FALSE, FALSE, TRUE, FALSE)
ON CONFLICT (type, channel) DO NOTHING;

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;

-- +goose Down
DROP VIEW IF EXISTS sforum_core_v1.forum_bookmarks;

ALTER TABLE comments DROP CONSTRAINT IF EXISTS comments_public_bookmark_count_nonnegative;
ALTER TABLE topics DROP CONSTRAINT IF EXISTS topics_public_bookmark_count_nonnegative;
ALTER TABLE comments DROP COLUMN IF EXISTS public_bookmark_count;
ALTER TABLE topics DROP COLUMN IF EXISTS public_bookmark_count;

DROP INDEX IF EXISTS content_bookmarks_due_idx;
DROP INDEX IF EXISTS content_bookmarks_user_idx;
DROP INDEX IF EXISTS content_bookmarks_public_idx;
DROP TABLE IF EXISTS content_bookmarks;

DELETE FROM notification_preferences WHERE type = 'bookmark_reminder';
DELETE FROM notification_type_policies WHERE type = 'bookmark_reminder';
DELETE FROM notification_type_descriptors WHERE type = 'bookmark_reminder';

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;
