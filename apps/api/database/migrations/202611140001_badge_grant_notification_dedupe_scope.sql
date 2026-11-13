-- +goose Up
-- +sforum OnlineSafe
-- 徽章授予通知的去重键改为收件人作用域。
--
-- `notifications.dedupe_key` 是**全局唯一**（202607110016 的 DDL + 插入时的
-- `ON CONFLICT (dedupe_key) DO NOTHING`）。徽章授予最初写成 `badge:{slug}`，
-- 于是全站第一个拿到该徽章的人永久占住这个键，之后每个用户的投影都被静默吞掉：
-- 「授予成功、收件箱空着、零报错」。其余通知类型一直自带收件人身份
-- （`moderation-pending:…:{reviewerID}`、`comment:…:reply:{id}`、插件
-- `plugin:sha256(…recipientID)`），徽章是唯一漏掉的一处。
--
-- 这里把历史行改写成 `badge:{recipient_user_id}:{slug}`，与运行期新键一致。
-- 判据用 `dedupe_key = 'badge:' || (payload->>'badgeSlug')` 而不是「以 badge: 开头」：
-- 后者会连带命中新格式的行（重复执行也不该改写它们，虽然幂等），而按位置切分
-- 在 slug 含冒号时会切错。
--
-- **不回填被吞掉的通知**：历史授予不补发（与庆祝层的「首访不回放历史」同一口径，
-- 老用户不该被几个月前的徽章刷屏）。这次迁移只修键，不造行。
--
-- `mail_deliveries` 的 idempotency_key 保持原样：那张表没有 user 维度，且旧键
-- `badge:{slug}` 与新键 `badge:{id}:{slug}` 结构上不相撞。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

UPDATE notifications
SET dedupe_key = 'badge:' || recipient_user_id || ':' || (payload->>'badgeSlug')
WHERE type = 'badge_granted'
  AND payload ? 'badgeSlug'
  AND payload->>'badgeSlug' <> ''
  AND dedupe_key = 'badge:' || (payload->>'badgeSlug');

-- +goose Down
-- 数据变更不可逆：把键退回 `badge:{slug}` 会让同一徽章的多条通知撞上全局唯一
-- 约束，只能靠删除他人通知来完成回滚——比保留新键有害得多。
-- 代码回退是安全的：新键与旧键并存时，旧代码按 slug 去重仍会把同一用户
-- 已有的新键行视为「另一条」，最坏结果是某用户再收到一条授予通知，
-- 而不会产生死链或重复行。
-- 因此 Down 故意为空（先例见 202607090001_topic_slug_unique.sql）。
