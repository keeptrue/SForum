-- +goose Up
-- +sforum OnlineSafe
-- 举报结案的乐观锁版本列（NF-02）。
--
-- moderation_reports 此前没有任何版本谓词：审核更新只按 id 定位，多审核员并发
-- 处理同一条举报时后写覆盖先写，且结案结论可以被再次改写。补 revision 作为 CAS
-- 令牌，写入路径用 `WHERE revision = $expected` 判定，0 行命中即冲突。
--
-- 扩张性由两段共同保证，缺一不可：
--   1. `ADD COLUMN ... NOT NULL DEFAULT 1` 在 PG 11+ 只改元数据、不重写表，
--      旧二进制的写入不设置 revision，落默认值 1 仍然合法；
--   2. CHECK **不能内联**在 ADD COLUMN 里 —— 那样会在 ACCESS EXCLUSIVE 下扫全表
--      校验，把 moderation_reports 的读写挡到 statement_timeout。必须
--      `NOT VALID` 登记 + 单独 `VALIDATE`（只需 SHARE UPDATE EXCLUSIVE，不挡读写）。
-- 依据：knowledge/decisions/2026-11-15-online-safe-migration-human-contract.md 清单第 4 条。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

ALTER TABLE moderation_reports
  ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- NOT VALID 只登记约束定义、不校验存量行；随后的 VALIDATE 才扫表，且锁级别不阻塞读写。
ALTER TABLE moderation_reports
  ADD CONSTRAINT moderation_reports_revision_positive CHECK (revision > 0) NOT VALID;
ALTER TABLE moderation_reports VALIDATE CONSTRAINT moderation_reports_revision_positive;

-- +goose Down
-- 约束引用该列，删列时随列一起消失，无需单独 DROP CONSTRAINT。
ALTER TABLE moderation_reports DROP COLUMN IF EXISTS revision;
