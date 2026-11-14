# 决策：6 个「没有内置角色授予」权限键的归属

日期：2026-11-14
状态：已采纳（PRD2-12 落地）

## 背景

PRD2-12 的取证结论：`SeedPermissions` 里曾有 6 个键没有任何内置角色模板持有——
`entity_meta.manage`、`moderation.manage`、`extension.manage`、`role.manage`、
`settings.manage`、`user.permission_override`。

「没有任何内置角色持有」不等于「坏键」，但它在运行时是**静默**的：端点永远 403、
日志里没有任何告警，只有人手工比对权限目录与角色授予才会发现。因此每个这样的键
都必须有一个明确归属，或者一份写下来的、可复核的不授予理由。

## 裁决

### 逐键归属

| 权限键 | 当前归属 | 理由 | 改动入口 |
|--------|----------|------|----------|
| `entity_meta.manage` | `operator`（+ `super_admin`） | 实体自定义字段定义是站点数据面，与运营岗同批 | `seeds.go` operator 包；DB 由 `202611140003_role_template_grants_parity.sql` 收敛 |
| `moderation.manage` | `operator`（+ `super_admin`） | 审核设置、报告/决策列表与设置重置（`app/Support/Routes/core_catalog_gen.go:198-203` 的 6 条路由）是站点策略 + 审计面。`moderator` 只做内容审核（`moderation.review` / `moderation.view_ip`），拿设置与审计属于越界 | `seeds.go` operator 包；DB 由 `202611140004_moderation_manage_operator.sql` 收敛；生成物 `apps/web/app/config/roleTemplates.ts` 随 seeds 重生成 |
| `role.manage` | 不授予任何内置模板；按需单独授予 | 角色与角色权限写路径等于「铸角色」能力，属于高风险授权，交给站点显式指派（或 `user_permission_overrides` 单人例外），不放进任何岗位包 | 守卫 `app/Http/core_guard_authorizer.go:805,813,819,829,844`；授予入口是后台角色页 + 个人权限例外 |
| `user.permission_override` | 不授予任何内置模板；按需单独授予 | 个人权限例外会绕过岗位边界，必须显式单独授予；`user.manage` 的父权限展开也刻意**不**包含它（`app/Models/Identity/permission_compat.go:23-27`） | 守卫 `app/Http/core_guard_authorizer.go:808`；`ReplaceUserPermissionOverrides` |
| `settings.manage` | 不授予任何内置模板；按需单独授予 | legacy 父权限：持有即展开 7 个细粒度子权限（`permission_compat.go:9-17`，含 `settings.*`、`forum.settings.manage`、`entity_meta.manage`）。整包授予等于一次性绕过全部设置岗位边界 | 子权限各自归岗（operator / tech_admin）；父键只作守卫 OR 备选（`app/Http/Controllers/Webhooks/controller.go:166`、`app/Models/Options/feature_flags.go:196,221`） |
| `extension.manage` | 不授予任何内置模板；按需单独授予 | 同上：legacy 父权限，持有即展开 `extension.view` / `extension.plugin.manage` / `extension.theme.manage`（`permission_compat.go:18-22`），把「看扩展」和「装插件」压成一个键 | 守卫 OR 备选 `app/Http/core_guard_authorizer.go:291,305,312,352,683` |

### 两条适用条件（读这张表前先看）

1. **「不预置进岗位模板」不等于「这些键失效」。** 上表后 4 个键仍然完全可用：
   它们作为守卫的 OR 备选参与判定（例如 `extension.manage` 在
   `app/Http/core_guard_authorizer.go:289-292`、`:302-306` 的 `requireCoreGuardPermission`
   备选列表里），也可以经 `user_permission_overrides` 单独授予某个账号，
   或由站点在后台给自建角色勾选。本表拒绝的只是「把它们塞进内置岗位模板」这一步。
2. **口径不含 `builtin_auto`。** 它是 DB-only 的系统角色（Host 自动升级受保护内置插件用），
   不在 `SeedRoleTemplates` 里，也不在「内置角色模板」的统计口径内；本轮不动它。

## 闸门

`app/Models/Identity/seeds_test.go` 的
`TestPermissionKeysWithoutBuiltinGrantStayDeliberate` 把结论钉成断言：

```
SeedPermissions − (SeedRoleTemplates 全部权限 ∪ SeedMemberPermissions)
  == { extension.manage, role.manage, settings.manage, user.permission_override }
```

排序后逐项比较（严格相等，不是子集），并在同一条测试里钉住
`moderation.manage` 的岗位归属（operator 持有、moderator 不持有）。
新增一个谁都不持有的权限键会让这条测试变红——此时要么给内置岗位，
要么把键加进白名单**并**更新本决策记录。绕过方式（改成子集、加例外名单）
会让漂移重新变成静默问题，因此不接受。

## 后果

- 运营岗拿到审核设置与审计历史，`moderator` 的岗位边界保持不变。
- 4 个高风险 / legacy 父键继续「可授予但不预置」，站点需要时显式指派。
- 权限目录新增键时，白名单测试与本文档是同一处检查点：键没有归属就进不了主干。
