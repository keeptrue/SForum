-- +goose Up
-- +sforum OnlineSafe
-- 徽章等级表：把 badges.tier 从写死的 bronze/silver/gold 枚举升级为可管理的数据表。
--
-- 为什么需要它：tier 之前是 CHECK 枚举 + Go 侧 Weight() 三元映射，运营无法新增
-- 一档「活动限定」或调整展示顺序；颜色/图标也只能全局共享一份。等级表把这三件事
-- 变成数据：双语名、图标、颜色、position（展示顺序）、enabled（停用即不再参与
-- 「最高级徽章」竞争）。
--
-- 为什么 OnlineSafe：新建表 + 新种子行 + 把 CHECK 换成外键。已有 badges.tier 的取值
-- 全部落在新表种子里（bronze/silver/gold），外键校验对存量行零失败；加外键会在
-- badges 上取 SHARE ROW EXCLUSIVE 锁，锁等待由下面的 lock_timeout 兜住。
--
-- key 的形状与 slug/group key 同族（小写字母开头 + 小写字母/数字/下划线，2..32 位）：
-- 它同时是 URL 段（PATCH /admin/badges/tiers/:tierKey）和 data-tier 的取值，
-- 所以不允许大写、连字符或空格——那种 key 会在 URL 里被转义，前端 data-tier 也要
-- 跟着编码，得不偿失。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS badge_tiers (
  key        TEXT PRIMARY KEY,
  name_zh    TEXT NOT NULL,
  name_en    TEXT NOT NULL,
  icon       TEXT NOT NULL DEFAULT 'i-lucide-medal',
  color      TEXT NOT NULL DEFAULT '',
  position   INTEGER NOT NULL DEFAULT 0,
  enabled    BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT badge_tiers_key_check CHECK (key ~ '^[a-z][a-z0-9_]{1,31}$'),
  CONSTRAINT badge_tiers_name_zh_check CHECK (btrim(name_zh) <> ''),
  CONSTRAINT badge_tiers_name_en_check CHECK (btrim(name_en) <> '')
);

-- 三个内置档位。position 与旧 Weight() 的排序**等价**（gold 0 < silver 1 < bronze 2），
-- 因此升级后「最高级徽章」的选择结果与升级前逐人一致——这是可逆性的前提：
-- down 段把非内置 key 的徽章落回 bronze 之后，旧 CHECK 才能重新加上。
--
-- color 刻意不用 hex 而用 token（amber/slate）：token 由前端映射为 class / CSS 变量，
-- 换主题时能跟着主题走；内置档位的识别色属于站点主题资产，不是运营品牌色。
INSERT INTO badge_tiers (key, name_zh, name_en, icon, color, position, enabled) VALUES
  ('gold',   '金牌', 'Gold',   'i-lucide-crown',  'amber',  0, TRUE),
  ('silver', '银牌', 'Silver', 'i-lucide-medal',  'slate',  1, TRUE),
  ('bronze', '铜牌', 'Bronze', 'i-lucide-award',  '#b45309', 2, TRUE)
ON CONFLICT (key) DO NOTHING;

-- CHECK → 外键：等级的存在性从「代码里的枚举」变成「表里的行」。
-- ON UPDATE CASCADE 让 key 改名（后台不支持，但 SQL 层面允许）不撕裂引用；
-- ON DELETE RESTRICT 是刻意的：删一档必须先指定 migrateTo，让运营显式选择
-- 「这些徽章归到哪一档」，而不是让数据库替他们决定（或级联删掉徽章）。
ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_tier_check;
ALTER TABLE badges
  ADD CONSTRAINT badges_tier_fkey FOREIGN KEY (tier) REFERENCES badge_tiers(key)
  ON UPDATE CASCADE ON DELETE RESTRICT;

-- 按档位筛徽章（后台列表按等级过滤、删除前数 badgeCount）。
CREATE INDEX IF NOT EXISTS badges_tier_idx ON badges (tier);

-- +goose Down
-- 可逆性：外键存在时无法把 tier 落回旧枚举之外的值，也无法在非内置 key 还挂在
-- badges.tier 上时删掉等级表。所以顺序必须是「先把徽章挪回内置档 → 拆外键 →
-- 恢复 CHECK → 删表」。
--
-- 这里把**所有**非内置 key 的徽章落回 bronze，而不是拒绝回滚：迁移的 down 段
-- 是运维在事故现场执行的动作，此时「回滚失败、库停在半路」比「自定义档位的徽章
-- 降级为铜牌（数据仍在，等级信息丢失）」危险得多。丢失的等级信息是运营可重建的
-- 配置，不是用户数据。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

UPDATE badges SET tier = 'bronze' WHERE tier NOT IN ('bronze', 'silver', 'gold');

ALTER TABLE badges DROP CONSTRAINT IF EXISTS badges_tier_fkey;
DROP INDEX IF EXISTS badges_tier_idx;

-- 恢复原 CHECK：与 202611100002 里那条**逐字相同**，PG 会重新生成同名约束
-- badges_tier_check，所以「再 up 一次」时 DROP CONSTRAINT IF EXISTS 仍能命中。
ALTER TABLE badges
  ADD CONSTRAINT badges_tier_check CHECK (tier IN ('bronze', 'silver', 'gold'));

DROP TABLE IF EXISTS badge_tiers;
