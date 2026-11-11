# 2026-11-10 信任等级：能力来源的单一权威

Status: **accepted**（2026-11-10 立，实施中）
计划：`../plans/2026-11-10-trust-levels.md`
前置：`2026-11-10-badges-and-user-activity.md`（统计层已 landed）

## 问题

在既有 RBAC 之上加一层「按累计行为自动升降」的能力层时，每个 route guard、每个
policy helper 都必须回答「这个能力是静态授予的，还是等级解锁的」。SForum 里
`identity.Actor.Can` 已经被 256 个非测试调用点共享；如果新增第二个裁决点，就必然
出现「两个地方对同一个问题给出不同答案」——这是本仓库历史上最大的 bug 类。

## 决定

### D1 `Can()` 是唯一裁决点，信任等级是它的**第四个**授予来源

```
Can(perm) = super_admin 绕过
          || 静态授予（角色 + 用户 allow 覆盖；deny 已在 DB 层排除）
          || 已挣得授予（TL 解锁，纯加性，永不否定）
```

三个来源在**装载时**合并进 `Actor`，`Can()` 只做内存判断：不查库、不读 options、
不拿锁。因此 256 个调用点、插件桥、CLI、bot 全部零改动。

需要区分来源的只有两类消费者，它们读只读派生视图 `Actor.CapabilitySource(perm)`：

1. 前端解释「为什么我不能发帖」（提示「再获 3 个赞可解锁」）；
2. 后台显示「这个能力谁能给、从哪来」。

派生视图**只解释** `Can()` 的结论，不能反过来影响它。

### D2 TL 吸收既有的「新人信任阶梯」，不并存两套动态阶梯

今天 `Forum.trustForActor` 按**账号年龄两态**产出限额与 forbid 位，那就是 Discourse
的 TL0。本刀把它改为 `limitsForActor`：按 `Actor.TrustLevel` 取限额，TL0 用
`trust.new_user_*` 七键现值，TL1+ 用站点默认。年龄从「唯一判据」降级为**上限闸**
（`trust.new_user_days` = 最多当多久新成员）。

七个人 option key 全部保留、不迁移数据、不改后台页归属。

### D3 只复用现有权限键，不新增能力词汇

已挣得集合里的每个键都必须在 `Identity.SeedPermissions` 里已存在，且不属于管理类。
本刀新增的权限键**只有** `trust.manage`（后台写阈值）。

### D4 等级是纯函数派生值，不落列、不缓存

`trustlevels.Level(activity, accountAge, policy)` 无 DB、无 `time.Now`（`now` 由调用
方注入，`LevelAt` 是它的时间戳包装）。读取位置是 `LoadActor` 的第 3 次查询。

代价（每个已认证请求 +1 次 PK 查询 + 1 次 deny 子查询）必须实测；p95 增量超过 1ms
则本决策复议。

### D5 降级只由可逆计数驱动

赞/收藏/访问天数下降才降级；内容软删、审核驳回、封禁都不降级。降级不回溯已有内容。

### D6 已挣得集合与「管理类权限」硬不相交

由结构测试遍历权限目录断言，不靠 code review。

### D7 失败关闭

`user_activity` 读失败、行缺失、开关关闭 → `Actor.TrustLevelResolved == false`：
已挣得集合为空，限额回落到本刀之前的年龄两态。**不是**按最高级放行。
`Actor` 零值（测试 / 插件桥 / CLI / bot 直接构造）同样等价于「无已挣得权限」。

### D8 权限与限额分离

`Can` 回答「能不能」，限额回答「多频繁/多大」，限额永不进 `Can`。

### D9 开关与权限键

`features.trust_levels`（`public: true`，推荐开）；后台写权限 `trust.manage`。
**不做 `trust.view`**：可达性依赖 `admin.access` 的读权限是死权限（`badge.view` 的
教训），后台读页复用 `admin.access`。

> 订正（2026-11-11）：本条的前提「`badge.view` 是死权限」已被实测推翻（`core_catalog_gen.go:44` 守卫 + `adminModules.ts:229` 侧栏），见 `modules/trust-levels.md` 与 `reports/2026-11-11-trust-level-earnable-audit.md` §6。结论不变：不做 `trust.view`。

### D10 五档命名

TL0 新成员 / TL1 成员 / TL2 常驻 / TL3 资深 / TL4 领袖，zh-CN + en-US 双语，
后端只输出稳定 i18n 键。

### D11 不做提名/晋升审批

纯规则、纯派生。Discourse TL3 的 promotion 流程不在本刀范围。

### D12 一键恢复推荐默认

后台阈值页必须能恢复内置推荐值，并明说「恢复默认不会影响用户已获得的内容」。

## 被否方案（都试过或推演过）

| 方案 | 为什么否 |
| --- | --- |
| 在每个 guard 上挂 `RequireTrustLevel(n)` | 256 个点漏一个就是静默越权；且「能发帖但要 TL2」与「角色给了 `topic.create`」立刻互相打架 |
| 新开 `Actor.CanEarned()` 让调用方自己选 | 调用方必须懂两套语义——这正是「两套权限互相打架」的定义 |
| 把 TL 解锁物化成真实权限行写进库 | 派生值变成第二份副本，需要写库 + 失效 + 对账，直接复制 `top_badge_id` 的三个失真窗口 |
| `users.trust_level` 反规范化列 | 需要迁移 + 三个失真窗口 + 一次重算 job；TL 是每请求都要正确的东西，缓存它只会制造「刚解锁但还不能用」的假故障 |
| 让两套阶梯并存（TL 管权限、旧阶梯管限额） | 「这个用户现在受不受限制」会有两个答案 |
| 新增 `trust.level2.post` 这类影子权限键 | `roleTemplates.ts`、`adminModules.ts`、权限本地化、后台角色页全要认识一套影子词汇 |

## 实施要点（给下一位）

- **deny 不能复活**：`user_activity` 之外还要读一次 `user_permission_overrides`
  的 deny 键（并入 `LoadActor` 第 3 次查询的子查询，不新增往返），装载时把被 deny
  的键从已挣得集合里剔除。`Can` 里没有任何机会知道 deny。
- **零值语义**：`Actor{}` 的 `TrustLevelResolved == false` 是刻意设计，别把它改成
  「默认 TL0 已解析」——那会同时改掉几十个测试与插件桥的行为。
- **`Options.Service` 已顶到接收者上限 48**：新开关/新阈值一律通过既有
  `TrustPolicy(ctx)` 的返回值扩展，或装配层适配器，不加方法。
- **M0 不得单独上线**：只声明不执行的注册表比没有更糟。
