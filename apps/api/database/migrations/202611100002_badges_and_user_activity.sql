-- +goose Up
-- +sforum OnlineSafe
-- 徽章（badge）与用户活动统计是本次新增的核心数据域：
--   * user_activity  —— 用户维度的反规范化计数，行懒创建；
--   * user_visits    —— 日粒度访问记录，是 days_visited 的唯一真值来源；
--   * badge_groups / badges / user_badges —— 徽章定义与授予关系；
--   * users.top_badge_id —— 列表/帖子旁只显示最高级 1 个徽章的反规范化列。
-- 全部是新增表 + 新增可空列，因此标记 OnlineSafe。
-- 统计口径只认 users.status 与 badges.enabled；内容可见性不参与徽章与统计，
-- 稳定视图里也不允许另写一套「什么算公开」的规则。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- 用户活动统计：一行一用户，写路径 INSERT ... ON CONFLICT DO UPDATE SET x = x + 1，
-- 不做回填（懒创建）。阅读类指标（posts_read_count / read_time_seconds）为信任等级
-- 预留，v1 不采集、恒为 0，稳定视图也不输出它们。
CREATE TABLE IF NOT EXISTS user_activity (
  user_id              BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  topic_count          BIGINT  NOT NULL DEFAULT 0 CHECK (topic_count >= 0),
  comment_count        BIGINT  NOT NULL DEFAULT 0 CHECK (comment_count >= 0),
  like_received_count  BIGINT  NOT NULL DEFAULT 0 CHECK (like_received_count >= 0),
  like_given_count     BIGINT  NOT NULL DEFAULT 0 CHECK (like_given_count >= 0),
  bookmark_count       BIGINT  NOT NULL DEFAULT 0 CHECK (bookmark_count >= 0),
  days_visited         INTEGER NOT NULL DEFAULT 0 CHECK (days_visited >= 0),
  posts_read_count     BIGINT  NOT NULL DEFAULT 0 CHECK (posts_read_count >= 0),
  read_time_seconds    BIGINT  NOT NULL DEFAULT 0 CHECK (read_time_seconds >= 0),
  last_visited_on      DATE,
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 日粒度访问记录：主键 (user_id, visited_on) 让写入天然幂等，
-- 采集路径用 ON CONFLICT DO NOTHING；days_visited 由本表 COUNT(*) 反规范化。
CREATE TABLE IF NOT EXISTS user_visits (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  visited_on DATE   NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, visited_on)
);

-- 运营侧「最近活跃」扫描：按日期倒序取最近访问者。
CREATE INDEX IF NOT EXISTS user_visits_recent_idx ON user_visits (visited_on DESC);

-- 徽章分组：key 是稳定标识（前端/插件引用它，不用数字 id）。
CREATE TABLE IF NOT EXISTS badge_groups (
  id          BIGSERIAL PRIMARY KEY,
  key         TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 徽章定义：自动徽章必须带 metric + 正数 threshold，手动徽章必须两者皆空，
-- 二者不可混用（badges_rule_check）；metric 白名单与 user_activity 计数列一一对应
-- （badges_metric_check），新增指标必须先加列再加白名单。
CREATE TABLE IF NOT EXISTS badges (
  id          BIGSERIAL PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  group_id    BIGINT REFERENCES badge_groups(id) ON DELETE SET NULL,
  tier        TEXT NOT NULL CHECK (tier IN ('bronze','silver','gold')),
  icon        TEXT NOT NULL DEFAULT 'i-lucide-award',
  grant_mode  TEXT NOT NULL CHECK (grant_mode IN ('automatic','manual')),
  metric      TEXT,
  threshold   BIGINT,
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badges_rule_check CHECK (
    (grant_mode = 'automatic' AND metric IS NOT NULL AND threshold IS NOT NULL AND threshold > 0)
    OR (grant_mode = 'manual' AND metric IS NULL AND threshold IS NULL)
  ),
  CONSTRAINT badges_metric_check CHECK (
    metric IS NULL OR metric IN (
      'topic_count','comment_count','like_received_count',
      'like_given_count','bookmark_count','days_visited'
    )
  )
);

-- 自动授予的评估路径：只扫启用中的自动徽章，按 metric 分组取阈值。
CREATE INDEX IF NOT EXISTS badges_enabled_auto_idx ON badges (metric) WHERE enabled AND grant_mode = 'automatic';

-- 授予记录：(user_id, badge_id) 唯一，重复授予由 ON CONFLICT 吸收。
-- granted_by 是操作者身份，只用于审计，稳定视图不输出。
CREATE TABLE IF NOT EXISTS user_badges (
  id           BIGSERIAL PRIMARY KEY,
  user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  badge_id     BIGINT NOT NULL REFERENCES badges(id) ON DELETE CASCADE,
  granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
  grant_reason TEXT NOT NULL DEFAULT '',
  CONSTRAINT user_badges_unique UNIQUE (user_id, badge_id)
);

-- 本人徽章墙：按授予时间倒序。
CREATE INDEX IF NOT EXISTS user_badges_user_idx ON user_badges (user_id, granted_at DESC);
-- 徽章成员名单：按授予时间倒序。
CREATE INDEX IF NOT EXISTS user_badges_badge_idx ON user_badges (badge_id, granted_at DESC);

-- 帖子旁只显示最高级 1 个徽章：反规范化到 users，避免列表查询做 DISTINCT ON。
ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_id BIGINT REFERENCES badges(id) ON DELETE SET NULL;

-- 稳定读视图：徽章目录只按 badges.enabled 过滤（禁用徽章对新授予关闭，但已授予
-- 关系由 forum_user_badges 的 badges.enabled 谓词同步收敛）。
-- grant_mode / metric / threshold 必须输出：插件要能只靠稳定数据面重建「徽章目录 +
-- 获得条件」，否则做徽章墙就得直读 core 表，违反 extension-data-surface-policy。
-- metric 是白名单枚举（见 badges_metric_check），暴露不构成契约风险。
-- 用 OR REPLACE：视图落在固定 Host schema（非 search_path 隔离的测试 schema），
-- 在 schema 隔离的测试库里重放整条迁移链时会命中同一对象；定义相同，替换即收敛。
CREATE OR REPLACE VIEW sforum_core_v1.forum_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT badges.id, badges.slug, badges.name, badges.description,
       badge_groups.key AS group_key, badges.tier, badges.icon, badges.position,
       badges.grant_mode, badges.metric, badges.threshold
FROM public.badges
LEFT JOIN public.badge_groups ON badge_groups.id = badges.group_id
WHERE badges.enabled
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_badges IS
  'SForum core view v1: enabled badge catalog with group key and grant rule (grant_mode/metric/threshold) so plugins can rebuild catalog and earn conditions; no audit fields.';

-- 授予关系：只输出 active 用户 × 启用徽章的授予行，供插件读面使用。
-- granted_by / grant_reason 属于审计数据，永不外泄。
CREATE OR REPLACE VIEW sforum_core_v1.forum_user_badges
WITH (security_barrier = true, security_invoker = false) AS
SELECT user_badges.id, user_badges.user_id, user_badges.badge_id, user_badges.granted_at,
       users.username, users.display_name, badges.slug, badges.tier
FROM public.user_badges
JOIN public.users  ON users.id  = user_badges.user_id AND users.status = 'active'
JOIN public.badges ON badges.id = user_badges.badge_id AND badges.enabled
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_user_badges IS
  'SForum core view v1: badge grants for active users on enabled badges; no grantor identity, no grant reason.';

-- 活动统计：只输出 active 用户的行，且不输出未采集的阅读类指标
-- （输出 0 会让调用方误以为是真实读数）。
CREATE OR REPLACE VIEW sforum_core_v1.forum_user_activity
WITH (security_barrier = true, security_invoker = false) AS
SELECT user_activity.user_id, users.username, users.display_name,
       user_activity.topic_count, user_activity.comment_count,
       user_activity.like_received_count, user_activity.like_given_count,
       user_activity.bookmark_count, user_activity.days_visited
FROM public.user_activity
JOIN public.users ON users.id = user_activity.user_id AND users.status = 'active'
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_user_activity IS
  'SForum core view v1: activity counters for active users; read metrics are not collected in v1 and are not exposed.';

-- 通知类型：徽章授予是系统动作，站内默认开；邮件与 web_push 默认关
-- （与 reaction / bookmark 同姿态，避免默认打扰）。
INSERT INTO notification_type_descriptors (
  type, contract_version, payload_version, category, active, required,
  presentation, target_contract
) VALUES (
  'badge_granted', 1, 1, 'engagement', TRUE, FALSE,
  '{"icon":"i-lucide-award"}'::jsonb,
  '{"kind":"entity","id":"core.entity.notification_target"}'::jsonb
)
ON CONFLICT (type) DO NOTHING;

INSERT INTO notification_type_policies (
  type, channel, enabled, recommended_enabled, user_configurable, required
) VALUES
  ('badge_granted', 'in_app', TRUE, TRUE, TRUE, FALSE),
  ('badge_granted', 'email', FALSE, FALSE, TRUE, FALSE),
  ('badge_granted', 'web_push', FALSE, FALSE, TRUE, FALSE)
ON CONFLICT (type, channel) DO NOTHING;

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;

-- 内置默认数据：3 个分组 + 12 个徽章。全部 ON CONFLICT DO NOTHING，
-- 运营者可改名、调阈值、禁用或删除，重放迁移不会覆盖运营侧修改。
INSERT INTO badge_groups (key, name, description, position) VALUES
  ('activity', '活跃', '持续回到社区', 0),
  ('content', '内容', '发帖、讨论与被认可', 1),
  ('honor', '荣誉', '由运营手动授予', 2)
ON CONFLICT (key) DO NOTHING;

INSERT INTO badges (slug, name, description, group_id, tier, icon, grant_mode, metric, threshold, position) VALUES
  ('regular-visitor', '常客', '累计访问 7 天', (SELECT id FROM badge_groups WHERE key = 'activity'), 'bronze', 'i-lucide-calendar', 'automatic', 'days_visited', 7, 0),
  ('resident', '老住户', '累计访问 30 天', (SELECT id FROM badge_groups WHERE key = 'activity'), 'silver', 'i-lucide-house', 'automatic', 'days_visited', 30, 1),
  ('veteran', '元老', '累计访问 180 天', (SELECT id FROM badge_groups WHERE key = 'activity'), 'gold', 'i-lucide-hourglass', 'automatic', 'days_visited', 180, 2),
  ('first-topic', '初次发帖', '发布首个主题', (SELECT id FROM badge_groups WHERE key = 'content'), 'bronze', 'i-lucide-sprout', 'automatic', 'topic_count', 1, 0),
  ('topic-author', '主题作者', '发布 10 个主题', (SELECT id FROM badge_groups WHERE key = 'content'), 'bronze', 'i-lucide-square-pen', 'automatic', 'topic_count', 10, 1),
  ('discussant', '讨论者', '发布 25 条评论', (SELECT id FROM badge_groups WHERE key = 'content'), 'bronze', 'i-lucide-message-circle', 'automatic', 'comment_count', 25, 2),
  ('rising-star', '小有名气', '收到 10 个赞', (SELECT id FROM badge_groups WHERE key = 'content'), 'bronze', 'i-lucide-star', 'automatic', 'like_received_count', 10, 3),
  ('well-received', '广受好评', '收到 100 个赞', (SELECT id FROM badge_groups WHERE key = 'content'), 'silver', 'i-lucide-thumbs-up', 'automatic', 'like_received_count', 100, 4),
  ('widely-acclaimed', '众望所归', '收到 500 个赞', (SELECT id FROM badge_groups WHERE key = 'content'), 'gold', 'i-lucide-trophy', 'automatic', 'like_received_count', 500, 5),
  ('collector', '收藏家', '收藏 10 条内容', (SELECT id FROM badge_groups WHERE key = 'content'), 'bronze', 'i-lucide-bookmark', 'automatic', 'bookmark_count', 10, 6),
  ('community-contribution', '社区贡献', '由运营手动授予', (SELECT id FROM badge_groups WHERE key = 'honor'), 'gold', 'i-lucide-users', 'manual', NULL, NULL, 0),
  ('quality-answer', '优质解答', '由运营手动授予', (SELECT id FROM badge_groups WHERE key = 'honor'), 'silver', 'i-lucide-circle-check-big', 'manual', NULL, NULL, 1)
ON CONFLICT (slug) DO NOTHING;

-- +goose Down
-- 严格逆序拆除：视图 → users 列 → 关系表/定义表/分组表 → 访问记录 → 统计表
-- → 通知策略/描述符 → bump revision。只拆自己创建的对象，不做级联删除，
-- 也不整表删除其它模块的对象。
DROP VIEW IF EXISTS sforum_core_v1.forum_user_activity;
DROP VIEW IF EXISTS sforum_core_v1.forum_user_badges;
DROP VIEW IF EXISTS sforum_core_v1.forum_badges;

ALTER TABLE users DROP COLUMN IF EXISTS top_badge_id;

DROP INDEX IF EXISTS user_badges_badge_idx;
DROP INDEX IF EXISTS user_badges_user_idx;
DROP TABLE IF EXISTS user_badges;

DROP INDEX IF EXISTS badges_enabled_auto_idx;
DROP TABLE IF EXISTS badges;

DROP TABLE IF EXISTS badge_groups;

DROP INDEX IF EXISTS user_visits_recent_idx;
DROP TABLE IF EXISTS user_visits;

DROP TABLE IF EXISTS user_activity;

DELETE FROM notification_preferences WHERE type = 'badge_granted';
DELETE FROM notification_type_policies WHERE type = 'badge_granted';
DELETE FROM notification_type_descriptors WHERE type = 'badge_granted';

UPDATE notification_policy_revisions
SET revision = revision + 1, updated_at = now()
WHERE singleton = TRUE;
