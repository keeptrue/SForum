-- +goose Up
-- +sforum OnlineSafe
-- 点赞（reaction）是第一个「用户 × 内容」关系域：单 like、可撤销、计数反规范化。
-- 多态 target_id 刻意不加外键（与 entity_meta 同姿态）：完整性由服务层事务保证，
-- 内容删除是软删，reaction 行保留即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_reactions_target_type_check CHECK (target_type IN ('topic', 'comment')),
  CONSTRAINT content_reactions_reaction_check CHECK (reaction IN ('like')),
  CONSTRAINT content_reactions_target_positive CHECK (target_id > 0),
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);

-- 名单读路径：按目标取最近 N 个点赞者。
CREATE INDEX IF NOT EXISTS content_reactions_roster_idx
  ON content_reactions (target_type, target_id, reaction, created_at DESC, id DESC);

-- 已读/回填类读路径：按用户取最近行为（当前仅用于 likedByMe 批量查询的辅助索引）。
CREATE INDEX IF NOT EXISTS content_reactions_user_idx
  ON content_reactions (user_id, created_at DESC);

-- 计数列：列表与详情读路径直接取值，避免每次 COUNT(*)。
ALTER TABLE topics ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE comments ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0;

-- 非负约束用 NOT VALID + VALIDATE，避免对大表做阻塞式全表校验。
ALTER TABLE topics ADD CONSTRAINT topics_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE comments ADD CONSTRAINT comments_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE topics VALIDATE CONSTRAINT topics_like_count_nonnegative;
ALTER TABLE comments VALIDATE CONSTRAINT comments_like_count_nonnegative;

-- 稳定读视图：谓词必须逐字镜像 sforum_core_v1.forum_topics / forum_comments，
-- 不允许在插件数据面上另写一套可见性规则。
-- 用 OR REPLACE：视图落在固定 Host schema（非 search_path 隔离的测试 schema），
-- 在 schema 隔离的测试库里重放整条迁移链时会命中同一对象；定义相同，替换即收敛。
CREATE OR REPLACE VIEW sforum_core_v1.forum_reactions
WITH (security_barrier = true, security_invoker = false) AS
SELECT
  reactions.id,
  reactions.target_type,
  reactions.target_id,
  reactions.reaction,
  reactions.created_at,
  users.id AS user_id,
  users.username,
  users.display_name
FROM public.content_reactions AS reactions
JOIN public.users ON users.id = reactions.user_id
WHERE users.status = 'active'
  AND (
    (
      reactions.target_type = 'topic'
      AND EXISTS (
        SELECT 1
        FROM public.topics
        JOIN public.categories ON categories.id = topics.category_id
        WHERE topics.id = reactions.target_id
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
    OR (
      reactions.target_type = 'comment'
      AND EXISTS (
        SELECT 1
        FROM public.comments
        JOIN public.topics ON topics.id = comments.topic_id
        JOIN public.categories ON categories.id = topics.category_id
        WHERE comments.id = reactions.target_id
          AND comments.status = 'active'
          AND comments.deleted_at IS NULL
          AND topics.status IN ('active', 'locked')
          AND topics.deleted_at IS NULL
          AND categories.visibility = 'public'
      )
    )
  )
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_reactions IS
  'SForum core view v1: public like relations on visible topics/comments; no private targets, no moderation fields.';

-- 通知类型：站内默认开，邮件与推送默认关（点赞频率高，默认不打扰收件箱）。
INSERT INTO notification_type_descriptors (
  type, contract_version, payload_version, category, active, required,
  presentation, target_contract
) VALUES (
  'reaction', 1, 1, 'engagement', TRUE, FALSE,
  '{"icon":"i-lucide-heart"}'::jsonb,
  '{"kind":"entity","id":"core.entity.notification_target"}'::jsonb
)
ON CONFLICT (type) DO NOTHING;

INSERT INTO notification_type_policies (
  type, channel, enabled, recommended_enabled, user_configurable, required
) VALUES
  ('reaction', 'in_app', TRUE, TRUE, TRUE, FALSE),
  ('reaction', 'email', FALSE, FALSE, TRUE, FALSE),
  ('reaction', 'web_push', FALSE, FALSE, TRUE, FALSE)
ON CONFLICT (type, channel) DO NOTHING;

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;

-- +goose Down
DROP VIEW IF EXISTS sforum_core_v1.forum_reactions;

ALTER TABLE comments DROP CONSTRAINT IF EXISTS comments_like_count_nonnegative;
ALTER TABLE topics DROP CONSTRAINT IF EXISTS topics_like_count_nonnegative;
ALTER TABLE comments DROP COLUMN IF EXISTS like_count;
ALTER TABLE topics DROP COLUMN IF EXISTS like_count;

DROP INDEX IF EXISTS content_reactions_user_idx;
DROP INDEX IF EXISTS content_reactions_roster_idx;
DROP TABLE IF EXISTS content_reactions;

DELETE FROM notification_preferences WHERE type = 'reaction';
DELETE FROM notification_type_policies WHERE type = 'reaction';
DELETE FROM notification_type_descriptors WHERE type = 'reaction';

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;
