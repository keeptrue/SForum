-- +goose Up
-- +sforum OnlineSafe
-- 信任等级的**变更账本**（计划 2026-11-14-trust-level-conditions §4.5 M4.3）。
--
-- 这张表**只做变更检测，不参与授权**。
--
-- 授权仍然走热路径实时求值（D2 的既定路线：等级不落授权列、不缓存）：每次装载都从
-- `user_activity` + 策略现算。本表存在的唯一目的，是让「这次算出来的等级和上次不一样」
-- 这件事可见——否则 `trust_level.changed` 事件没有任何比较基准，只能靠猜。
--
-- **不要把它当权威**：一旦有人从这张表读等级用于鉴权，就会出现「库里 level=2、实时算
-- 出来 level=3」的双真相；而这种不一致只在「求值 incomplete」或「事件丢失」时暴露，
-- 属于最难查的一类故障。判断等级请调 `trustlevels.Resolver.Evaluate`。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

CREATE TABLE IF NOT EXISTS public.user_trust_levels (
  user_id BIGINT PRIMARY KEY REFERENCES public.users (id) ON DELETE CASCADE,
  -- level 是 rank（0..MaxLevel）。不存 tier_id 的引用完整性：档位配置在 options JSON 里，
  -- 没有可引用的表；运营删档后旧 id 只作为历史值保留。
  level INTEGER NOT NULL,
  tier_id TEXT NOT NULL,
  computed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.user_trust_levels IS
  'Change-detection ledger for trust levels. NOT authoritative: authorization always recomputes via trustlevels.Resolver.Evaluate (D2). Used only to decide whether to emit trust_level.changed.';

COMMENT ON COLUMN public.user_trust_levels.level IS
  'Rank (TL0..TLn) at the time of the last complete evaluation. Never read for authorization.';

COMMENT ON COLUMN public.user_trust_levels.tier_id IS
  'Tier identity id (newcomer/member/tier_<n>) at the time of the last complete evaluation; kept as a historical value even if the tier is later deleted.';

-- 视图：给插件与运维一个「当前账本快照」的只读面。
--
-- **必须 DROP + CREATE，不能 CREATE OR REPLACE**：schema 隔离测试库会在固定 schema 上
-- 重放整条迁移链，`CREATE OR REPLACE VIEW` 在列形状变化时会撞 42P16
-- (`cannot change name of view column`)，而那条错误信息完全指不到「这是重放导致的」。
DROP VIEW IF EXISTS sforum_core_v1.forum_user_trust_levels;
CREATE VIEW sforum_core_v1.forum_user_trust_levels
WITH (security_barrier = true, security_invoker = false) AS
SELECT ledger.user_id, users.username, ledger.level, ledger.tier_id, ledger.computed_at
FROM public.user_trust_levels AS ledger
JOIN public.users ON users.id = ledger.user_id AND users.status = 'active'
OFFSET 0;

COMMENT ON VIEW sforum_core_v1.forum_user_trust_levels IS
  'SForum core view v1: trust level change ledger for active users (user_id/username/level/tier_id/computed_at). A snapshot of the last complete evaluation, NOT an authorization source — see the table comment.';

-- 新对象与既有 Core 对象同源：复制 `badges` 的 owner。
--
-- 为什么必须做：扩展平台 kernel 只收敛「owner 恰好等于运行时角色」的对象
-- （`relowner = roleName` 过滤）。新建的表/视图如果 owner 与既有 Core 表不同，就永远不会
-- 被收敛，运行期租约签发时报 `extension database physical resource conflict` ——
-- 那个报错完全指不到 owner 头上。
--
-- 从 `badges` 抄而不是写死角色名：它同域、同生命周期、且一定已被收敛过。
-- +goose StatementBegin
DO $$
DECLARE target_owner text;
BEGIN
  SELECT pg_get_userbyid(classes.relowner) INTO target_owner
  FROM pg_class AS classes
  JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
  WHERE namespaces.nspname = 'public' AND classes.relname = 'badges';

  IF target_owner IS NOT NULL THEN
    EXECUTE format('ALTER TABLE public.user_trust_levels OWNER TO %I', target_owner);
    EXECUTE format('ALTER VIEW sforum_core_v1.forum_user_trust_levels OWNER TO %I', target_owner);
  END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- 严格逆序：先拆视图再拆表。视图用 IF EXISTS（重放时 Up 段可能已把它 DROP 过）。
DROP VIEW IF EXISTS sforum_core_v1.forum_user_trust_levels;
DROP TABLE IF EXISTS public.user_trust_levels;
