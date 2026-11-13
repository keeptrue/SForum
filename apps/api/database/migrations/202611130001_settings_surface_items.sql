-- +goose Up
-- +sforum OnlineSafe
-- 设置面治理覆盖：运营对「插件注册的设置 tab / 字段」的可见性与顺序覆盖。
--
-- 语义刻意是「只存覆盖项」：
--   - 没有行          → 用 manifest 声明（默认可见、顺序取贡献/字段的 order）
--   - 有行 enabled=false → 该 tab（或 item_key 指名的那个字段）被站点关掉
--   - 删行            → 恢复 manifest 默认
-- 因此这张表永远可以被安全清空：它不是一个投影，而是 sparse overlay。
--
-- 为什么把扩展 id 与贡献 id 存成文本而不是外键：manifest 的贡献 id 在扩展升级
-- 时由包自己维护，宿主不该因为一次「覆盖行指向一个已被删掉的 tab」而拒绝升级；
-- 悬空覆盖行在读取侧自然失效（匹配不到任何贡献），由下一次投影（启用/升级/启动
-- reconcile 走的 SettingsSurfaceFieldSync）清掉——不是治理保存时清。
--
-- 不建 sforum_core_v1 视图、不建 HostQuery：这是宿主治理态（谁还能看见什么），
-- 不是产品数据。插件读自己的声明从 manifest 来，读覆盖值没有公开面。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS settings_surface_items (
  id BIGSERIAL PRIMARY KEY,
  extension_id TEXT NOT NULL CHECK (extension_id <> ''),
  point TEXT NOT NULL CHECK (point <> ''),
  contribution_id TEXT NOT NULL CHECK (contribution_id <> ''),
  -- 空串 = 整个 tab；否则是该 tab 内字段声明的 key（不含 x.<extension>. 前缀）。
  item_key TEXT NOT NULL DEFAULT '' CHECK (item_key = '' OR item_key ~ '^[a-z][a-z0-9_-]{1,40}$'),
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  -- NULL = 不覆盖顺序，沿用 manifest 声明的顺序。
  sort_order INTEGER,
  -- 操作人注销后保留覆盖行（治理结果不该随账号消失）。
  updated_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT settings_surface_items_unique UNIQUE (extension_id, point, contribution_id, item_key)
);

-- 读路径是「按表面取全部覆盖」，扫描量小但每个设置页都会走一次。
CREATE INDEX IF NOT EXISTS settings_surface_items_point_idx
  ON settings_surface_items (point, extension_id, contribution_id, item_key);

-- +goose Down
DROP INDEX IF EXISTS settings_surface_items_point_idx;
DROP TABLE IF EXISTS settings_surface_items;
