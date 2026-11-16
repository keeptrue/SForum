-- +goose Up
-- PAT 纳入 token_version 失效（P2-2）。
--
-- 强制下线与改密都只递增 users.current_token_version 并撤销 user_sessions 行，
-- 而 api_tokens 完全不受约束：拿到 PAT 的机器客户端在管理员「下线全部设备」之后
-- 仍能继续访问。这里给每条 PAT 记录签发时的用户版本，鉴权时与
-- users.current_token_version 比对，不一致即失效。
--
-- revoke_reason 与 user_sessions.revoke_reason 同口径：区分 admin_revoke /
-- password_reset / 用户自助撤销，便于审计回答「这条令牌为什么没了」。
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS token_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS revoke_reason TEXT NOT NULL DEFAULT '';

-- 回填：存量 PAT 一律视为「与当前版本一致」，避免升级瞬间把所有机器客户端踢下线。
UPDATE api_tokens t
SET token_version = u.current_token_version
FROM users u
WHERE u.id = t.user_id;

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN IF EXISTS revoke_reason;
ALTER TABLE api_tokens DROP COLUMN IF EXISTS token_version;
