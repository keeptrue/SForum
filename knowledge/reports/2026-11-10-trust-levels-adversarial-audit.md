# 2026-11-10 信任等级 · 反向审计（假设 D13–D20 已实现，找静默出错点）

审计人：researcher｜方式：只读 `rg` / 读文件；未改任何生产代码、未改计划
前提：按 lead 口径，D13 合并点在 `loadCurrentUserAccess`；D18 由 `Identity` 声明窄接口 `TrustLevelSource`，`TrustLevels` 实现，bootstrap 注入；activity 复用 `Badges.GetCounters`。
结论口径：只报「会静默出错」——不报会立刻报错的地方。

---

## 0. 结论摘要（按严重度）

| # | 静默出错点 | 后果 |
| --- | --- | --- |
| S1 | **存在第二个 actor loader `LoadEffectiveActorTx`，不在 D18 注入面上** | 插件命令路径永远看不到 earned：TL 挣来的 `attachment.upload` 在 HTTP 放行、在 3 个 HostAPI 命令被拒 = 两个答案 |
| S2 | **合并点选在 `loadCurrentUserAccess` 会销毁来源信息** | `CapabilitySource` 无法区分 static/earned，只能再查一次库（违反 T3）或另存 earned 集合 |
| S3 | **`loadCurrentUserAccess` 也是登录/注册/改偏好/CLI 的装载点** | TL 解析被拖进登录与注册事务；resolver 为 nil 时不能 panic；登录响应与会话开始出现瞬时不一致 |
| S4 | **后台用户详情与会话对"该用户有什么权限"给出两个答案** | 运营看到"没有 topic.create"，用户却能发帖 → 被判定为权限系统故障（不是 D13 担心的"earned 显示成角色授予"，那条目前不存在） |
| S5 | **`earned` 若在父→子展开之后合并，裸 map 查表的守卫不会继承父权限** | 插件/路由守卫与 `Can()` 分叉 |
| S6 | **`edit_any` 类键若进 earned → 无审计的版主编辑** | `revisionOrigin` 只比 ID、`isAuthorOnly*Edit` 看权限，两者不一致时"有特权、无审计" |
| S7 | **bot 会拿到最高 TL**（活动量最大） | 今天无害（earned 只含非管理键），下一次往 TL4 加键就是自动化越权 |
| S8 | **PAT `RestrictActor` 把 earned 冻结成静态并随活动漂移** | scope 静默消失；`CapabilitySource` 对这些键答 static |
| S9 | **注册/改偏好响应里会带 earned** | 多一次 tx 内查询；首屏 UI 与随后请求可能不一致 |
| S10 | **父→子展开逻辑已有三份，第二份（tx loader）完全不展开** | 前端副本缺 `entity_meta.manage`；合并顺序一改就漂移 |
| S11 | **`Attachments/service.go:591` 手写权限 map 的系统 actor 是范例** | 照抄一个带 earned 类键的系统 actor = "TL0 却能发帖"的幽灵 |
| S12 | **`seed:forum` 走 `CreateTopic`，会吃 TL 限额** | 与 `trust.new_user_days=0` 语义冲突同源；今天不是回归，改语义后是 |

---

## 1. 方向一：`CurrentUser.Permissions` 的第二类消费者

### 1.1 先回答被问的那件事：后台会不会把 earned 当「角色授予」显示

**目前不会，原因是后台详情走的是另一个 loader，而不是因为设计上做了隔离。**

- 后台用户详情：`app/Models/Identity/postgres_store_roles.go:229 loadAdminUserAccess` → `:235 ListUserPermissions` + `:241 ExpandEffectivePermissions` → 写入 `AdminUserDetail.Permissions`（`types.go:339`）。**不经过 `loadCurrentUserAccess`**，所以不含 earned。
- 角色权限编辑：前端状态来自 `ListRoles → role_permissions`（`postgres_store_roles.go:14-30` 逐角色 `listRolePermissionKeys`），保存走 `ReplaceRolePermissions`（`app/Http/Controllers/Identity/admin_access_handlers.go:261`）。全程静态。
- 用户权限例外编辑：读 `AdminUserDetail.PermissionOverrides`（`types.go:340/306`），同样是静态 loader。

**所以 D13 担心的"earned 显示成角色授予"当前不成立。真正的副作用是 S4：两个视图不一致。** 运营在后台看到 `topic.create` 不在列表里（静态确实没有），而该用户因为 TL2 挣得了它、能发帖。后台是运营排查"为什么他能发"的唯一入口，那里给出否定答案，等于把可解释性做成了误导。

**必须写进 ADR 的一句**：`loadAdminUserAccess` **永远不合并 earned**；若要显示"他还因为等级拥有 X"，另开一个字段（如 `EarnedPermissions []string` + `TrustLevel int`），不要塞进 `Permissions`。否则后台立刻开始说谎。

### 1.2 真正被污染的第二类消费者（按危险度）

| 消费者 | 位置 | 污染方式 |
| --- | --- | --- |
| 插件/路由守卫的权限快照 | `app/Http/route_dispatcher.go:380-381`、`app/Http/route_dispatcher.go:80`、`app/Http/core_guard_authorizer.go:210/926-945`、`app/Http/core_guard_forum_resource.go:71/107`、`app/Http/plugin_guard_runtime.go:103`、`app/Http/route_dispatcher_v2.go:72` | 把 `actor.Permissions` 摊成 `map[string]bool` 后**裸查表**，不走 `Can()`。earned 会随 map 一起流进去（语义上正确），但这些点不享受 `hasExpandedPermission` 的父→子继承 —— 见 S5 |
| 扩展管理面可见性 | `app/Http/Controllers/Extensions/admin_surfaces.go:179-180` | 同上，且影响"这个 admin surface 对谁可见" |
| 主题 ViewModel / 导航过滤 | `app/Http/Controllers/Pages/controller.go:831-832`、`app/Support/ThemeCompiler/viewmodel_registry.go:395-404`、`app/Support/NavigationRegistry/compose.go:498-506` | earned 键会让导航/区块对用户可见 —— 这是想要的行为，但意味着**导航可见性开始随活动量变化**，缓存（如有）必须按用户失效 |
| 插件 HostAPI 查询授权 | `bootstrap/query_registry.go:70`、`bootstrap/privacy_registry.go:66` | 用 `actor.Can(...)`，与 earned 一致；但 privacy 那行还有 `actor.IsSuperAdmin()` 短路，注意别让 earned 影响 |
| 前端 `can()` | `apps/web/app/composables/identity/usePermissions.ts:65`（`can()` 在 `:68`） | 读会话里的 `permissions` 数组，earned 会进去（想要）；但它自带一份 `LEGACY_PERMISSION_CHILDREN`（`:44-59`）**缺 `entity_meta.manage`**（S10） |

**S2（本条方向最重要的结论）**：`loadCurrentUserAccess` 是**扁平并集**的产出点。合并发生在这里之后，`Actor.Permissions`（`postgres_store.go:386-389` 只是把 `current.Permissions` 拷成 map）里**没有任何字段能区分 static / override / earned**。于是：

- `CapabilitySource(perm)` 无法回答"从哪来"，只能重算 TL（要 `Activity`，`Actor` 里没有）或再查一次库（违反 T3）；
- 后端也无法告诉前端"再获 3 个赞可解锁"（T22 已经提过）。

**修法（不改 D1，只改落点）**：`Actor` 上保留独立的 `Earned map[string]bool`（或 `EarnedKeys []string`），`CurrentUser` 保持**静态**语义；合并发生在 `Actor` 组装末尾或 `Can()` 内。这样 §1.1 的两视图一致性问题也自动缓解（`CurrentUser` 仍是"角色+覆盖"，后台与会话同源）。

---

## 2. 方向二：`LoadActor` 调用点里用 `Actor` 做写审计归因

### 2.1 结论：归因本身是 ID 驱动的，earned 改不了"谁"

- 审计写入一律 `ActorUserID: actor.ID`：`app/Models/Forum/revision_writes.go:442/454`、`app/Models/Forum/revisions_redaction.go:74`、`app/Models/Identity/postgres_store_roles.go:100-110`（`role.permissions.replace`）等。
- 没有发现任何"用 `Can()` 决定 `ActorUserID` 写谁"的地方。所以 earned 不会造成"张三代李四写"的归因错误。

### 2.2 但有一条**派生归因**会被权限翻转，而且翻转后审计会消失（S6）

链条：

1. `app/Models/Forum/service.go:831-836 revisionOrigin(actorID, authorID)` —— **只比 ID**，`actorID == authorID` → `self`，否则 `staff`。
2. `app/Models/Forum/revision_writes.go:435` / `:447` —— **只有 `Origin == staff` 才写审计**（`s.auditor.AppendTx`）。
3. `app/Models/Forum/service_ops.go:556 isAuthorOnlyTopicEdit` / `:560 isAuthorOnlyCommentEdit` —— 用 `!actor.Can(PermissionTopicEditAny) && author == ID` 判定"是否只允许作者编辑"；`service_ops.go:331-332`（"作者编辑窗口：拥有 post.edit_any 的版主不受限"）也依赖 `Can(post.edit_any)`。

**如果 `topic.edit_any` / `post.edit_any` 变成 earnable（TL4「领袖」是很自然的候选），就会出现**：作者编辑自己的帖子 → `isAuthorOnly*Edit` 返回 false（因为 `Can(edit_any)` 为真）→ 绕过编辑窗口/作者限定 → 但 `revisionOrigin` 仍返回 `self` → **审计不写**。结果是一条**有特权效果、无审计记录**的编辑路径，且没有任何报错。

**对 D6 的直接影响**：D6 现在写的是"earned ∩ 管理类权限 == ∅"，并计划用"遍历 catalog 断言 ∩ admin/manage == ∅"。这个判据抓不到 `edit_any`/`delete_any`（它们不带 `manage` 后缀，也不在 `admin` 模块）。**结构断言必须显式列出危险键族**：`*.edit_any`、`*.delete_any`、`*.lock`、`*.pin`、`admin.access`、`moderation.*`、`user.*`、`settings.*`、`extension.*`、`attachment.manage`、`attachment.settings.manage`、`attachment.upload_policy.manage`、`role.*`、`badge.manage`。

### 2.3 第二处同类：Moderation

`app/Models/Moderation/service.go:45-125` 全部以 `Can(moderation.manage|review|view_ip)` 为闸。若任何 `moderation.*` 进 earned，审核动作会由非版主账号执行、且审计里 `ActorUserID` 看起来是个普通用户。同属"权限即身份"的一类，D6 必须覆盖。

---

## 3. 方向三：绕过 `LoadActor` 直接构造带权限 `Actor` 的路径

### 3.1 S1（P0）：`LoadEffectiveActorTx` 是第二个 loader，且不在 D18 的注入面上

`app/Models/Identity/actor_postgres_tx.go:112-213`：

- 自己写 SQL 读 `users.status`（`FOR SHARE`）、`user_roles→roles`、`role_permissions→permissions`、`user_permission_overrides`（allow→加、deny→删）；
- **不读 `users.created_at` / `kind`** → `Actor.CreatedAt` 与 `Kind` 恒零值；
- **不调用 `ExpandEffectivePermissions`**（该文件内出现次数 = 0）→ 父→子继承在这条路径上**今天就不生效**；
- 非测试调用点 4 个：
  - `app/Support/HostAPI/v2_command_actor_postgres.go:96`（`loadProtocolV2CommandActor`）→ `:36` 用 `actor.Can(permission)` 校验 `RequiredPermissions`。**3 个命令要求 `attachment.upload`**（`v2_command_attachment_extensions.go:87/177/228`）。
  - `app/Models/Identity/actor_postgres_tx.go:71`（`loadAuthorizedAdminActorTx`，后台写权限闸）
  - `app/Models/Identity/user_fields_postgres_authority.go:230`
  - `app/Models/Identity/session_policy_selection_postgres_authority.go:205`

**静默出错的方式**：D18 只把 `TrustLevelSource` 注入 `PostgresStore.LoadActor`。于是 HTTP 上传（`app/Models/Attachments/upload.go:59` 等 5 处）认 earned，而插件命令路径不认 → TL2 用户在插件提供的上传流程里被 403，主站能传。**这正是计划要消灭的"两个答案"，而且它藏在一个没有任何测试覆盖 earned 的 loader 里。**

**同时注意**：即使把 resolver 也注入 `LoadEffectiveActorTx`，它不读 `created_at`/`kind`，`trustForActor`（`service_ops.go:516` 的 `CreatedAt.IsZero()` 早退）在这条路径上永远返回"不限制"；而 earned 层没有这层保护，行为会不对称。

### 3.2 其余绕过点（逐个判定）

| 路径 | 位置 | TL 恒 0 的后果 |
| --- | --- | --- |
| 插件桥合成 actor | `app/Http/core_guard_authorizer.go:210-217` | `Permissions` 来自 dispatcher 已合并的 map（所以 earned 在）；但 `Permissions["*"]` 会**反向合成 `RoleKeys=[super_admin]`** —— 这是 `LoadActor` 之外第二个 super_admin 来源，与 TL 无关但必须留在 D6 的对照里 |
| 附件系统 actor | `app/Models/Attachments/service.go:591-595` | 手写 `Permissions: {attachment.settings.manage: true}`，管理键，安全；但它是"手写权限 map"的唯一先例（S11） |
| system actor | `app/Models/Extensions/systemactor/system_actor.go:44-102` | 走 `LoadActor`，TL 会被解析；系统账号活动量为 0 → TL0 → earned 空。安全，前提是它用的都是静态键 |
| dev-auto 运行时 | `app/Models/Extensions/dev_auto_runtime.go:329` | 走 `LoadActor` 拿 operator actor → TL 取决于该账号活动。若 TL0 限额被配置成"限制发帖"，开发期插件写内容会开始被限流（今天靠 `Can(settings.manage)` 旁路躲过，`service_ops.go:534`） |
| bot（AI 回复） | `app/Models/AIReply/poster.go:52`、`app/Models/AITools/aitools.go:229-236` | 走 `LoadActor` → **earned 会生效**，而限额层有 `Kind.IsBot()` 旁路（`service_ops.go:521`）—— 两层不对称（S7） |
| CLI | `cmd/sforum/{seed_run.go:163/200, seed_profiles.go:99, seed_taxonomy.go:270, privacy_erase_user.go:102/120, ai_derived.go:177/280}` | 全部 `LoadActor`；CLI 装配里若没注入 resolver，必须降级为 0 而不是 nil panic |
| PAT | `app/Models/APITokens/scoped_store.go:15-24` + `service.go:164 RestrictActor` | 见 S8 |
| 零值 actor 直接进领域层 | `app/Http/route_dispatcher.go:303`、`app/Http/Controllers/Profile/controller.go:63`、`bookmarks.go:33`、`app/Models/Profile/service.go:89` | 零值 → `IsActive()` false → `Can()` 全 false，与今天一致 |

### 3.3 S8：PAT 会把 earned 固化成静态

`app/Models/APITokens/service.go:164-200 RestrictActor`：对每个 scope 做 `current.Can(scope)` 求交，结果写进新 actor 的 `Permissions`。earned 参与求交后：

- 语义上更正确（PAT 上限=用户当前权限），但 **scope 会随活动计数漂移**：TL 掉级 → PAT 的某个 scope 静默消失（403，无审计、无提示）；
- 求交结果把 earned 键**写成了静态键**，后续 `CapabilitySource` 会答 `static`，与真实来源不符（S2 的放大版）。

### 3.4 S12：`seed:forum` 会吃 TL 限额（但今天不是回归）

`cmd/sforum/seed_run.go:248 registerSeedUser` 走 `identity.Service.Register`（`created_at = now`），`:163/:200` 用 `LoadActor` 后调 `forumService.CreateTopic/CreateComment` —— 即**经过完整限额闸**。今天：新账号 → 新人阶梯生效（冷却 300s、日限 3）。收编后：新账号 → TL0 → 同一组限额。**两者一致，不是回归**。但 `trust.new_user_days = 0` 今天是"整条阶梯关闭"，收编后若变成"TL0 限额"，seed 脚本会开始撞冷却 —— 与 T15 同源，这里是第二个受害者。（`--bulk` 路径 `cmd/sforum/seed_bulk.go` 直接 COPY 进 `posts`，不经 service，不受影响。）

---

## 4. 方向四：`ExpandEffectivePermissions` 的其余调用点与合并顺序

### 4.1 调用点清单（生产 2 处 + 测试 1 处）

| 位置 | 用途 | 合并 earned 后是否一致 |
| --- | --- | --- |
| `app/Models/Identity/postgres_store_roles.go:263`（`loadCurrentUserAccess`） | 会话 + `LoadActor` 的权限来源 | 若 earned 在展开**之前**并入 → earned 键也会被展开。今天安全（3 个父键全是管理键，`permission_compat.go:11-28`），但这条安全性依赖 D6，不是结构保证 |
| `app/Models/Identity/postgres_store_roles.go:241`（`loadAdminUserAccess`） | 后台用户详情 | 不合并 earned（应保持，见 §1.1） |
| `app/Models/Identity/policy_test.go:70` | 单测 | — |

**另一条同义实现**：`app/Models/Identity/permission_compat.go:57 hasExpandedPermission` 自己再遍历一遍同一张 `legacyPermissionChildren`，不复用 `ExpandEffectivePermissions`。同一份语义两个实现，改一处必漏另一处。

### 4.2 第三份副本在前端，且已经漂移

`apps/web/app/composables/identity/usePermissions.ts:44-59 LEGACY_PERMISSION_CHILDREN`：

- `settings.manage` 下只列 6 个子键（`site/mail/notifications/avatar/appearance/forum.settings`）；
- Go 侧 `permission_compat.go:11-20` 有 **7** 个，多一个 `PermissionEntityMetaManage`（键名是 `entity_meta.manage`，`seeds.go:73`）。
- 今天看不出错，因为两处使用都做了 OR 兜底：`apps/web/app/config/adminModules.ts:160` 用 `['entity_meta.manage','settings.manage']`，`apps/web/app/pages/admin/entity-meta.vue:39` 用 `can('entity_meta.manage') || can('settings.manage')`。
- 一旦有人删掉那个 OR（很自然的"清理"），前端就会对一个只有旧父权限的管理员隐藏实体元数据页 —— 而后端放行。**这是既存的第二份副本，TL 只是让它更容易被踩。**

### 4.3 顺序约束（必须写进 ADR 的一条）

裸查表点（§1.2 表里那一串）不享受 `hasExpandedPermission`，它们只读 map。因此装载期顺序被唯一确定：

```
静态（角色 ∪ allow − deny） → earned 并入 → ExpandEffectivePermissions → 下发给 Actor / CurrentUser
```

任何把 earned 放到展开之后的实现，都会让"earned 的父键"在裸查表点失效、在 `Can()` 里生效 = 第五个"两个答案"。

---

## 5. 给 lead 的最小修正建议（不改计划，只报选项）

1. **D18 补一句**：`TrustLevelSource` 必须同时注入 `PostgresStore.LoadActor` **与** `LoadEffectiveActorTx`（`actor_postgres_tx.go:112`），否则插件命令路径永远无 earned；顺带把该 loader 补上 `ExpandEffectivePermissions`（这是既存缺口，不修就会在 TL 之后被误归因为 TL 的 bug）。
2. **D13 落点改一处**：合并不要发生在 `loadCurrentUserAccess`，而是在 `Actor` 组装处，并保留 `Earned` 独立集合；`CurrentUser.Permissions` 维持静态语义。否则 `CapabilitySource` 无法实现（S2），后台与会话也会给出两个答案（S4）。
3. **D6 的判据改成显式危险键族清单**（§2.2 末），不要用"manage 后缀"这种启发式；并把"`*.edit_any`/`*.delete_any` 一旦 earnable 就会产生无审计特权编辑"写成 ADR 里的理由，而不是只写"零交集"。
4. **D7 补一条**：`Kind != human` → earned 恒空（S7）。bot 的活动量注定最高，这是唯一能防止"下次加键就自动化越权"的位置。
5. **新增一条测试**：`LoadEffectiveActorTx` 与 `LoadActor` 对同一用户的 `Can()` 结果必须逐键相等（用 legacy 父权限 + earned 两种场景各跑一遍）。这条测试同时覆盖 S1 和既存的展开缺口。
6. **PAT 语义显式记录**（S8）：earned 参与 scope 求交 = scope 随活动漂移；如果不想这样，就在 `RestrictActor` 里只用静态集合求交，并单独说明"PAT 不继承等级能力"。

---

## 6. 复现命令

```bash
cd apps/api
# S1 第二 loader 与它不展开
sed -n '112,213p' app/Models/Identity/actor_postgres_tx.go
grep -c "ExpandEffectivePermissions" app/Models/Identity/actor_postgres_tx.go      # 0
grep -rn "LoadEffectiveActorTx(" --include=*.go . | grep -v "func "                 # 4 处
grep -rn "RequiredPermissions: \[\]string{identity.PermissionAttachmentUpload}" app/Support/HostAPI/

# S2/S3 合并点的调用者
grep -rn "loadCurrentUserAccess" app/Models/Identity/
grep -rn "GetCurrentUserByEmail(\|GetCredentialByLogin(" --include=*.go . | grep -v _test

# S4 两个视图
sed -n '229,243p' app/Models/Identity/postgres_store_roles.go                      # 后台：不合并
sed -n '246,263p' app/Models/Identity/postgres_store_roles.go                      # 会话：合并点

# S6 归因
sed -n '831,836p' app/Models/Forum/service.go                                      # revisionOrigin 只比 ID
sed -n '430,455p' app/Models/Forum/revision_writes.go                              # 只有 staff 才写审计
sed -n '556,562p' app/Models/Forum/service_ops.go                                  # 但权限看 Can(edit_any)

# S8 PAT
sed -n '164,205p' app/Models/APITokens/service.go

# S10 三份展开
grep -rn "ExpandEffectivePermissions(" --include=*.go . | grep -v "func "
grep -n "LEGACY_PERMISSION_CHILDREN" -A 16 ../../apps/web/app/composables/identity/usePermissions.ts

# S12 seed
sed -n '155,205p' cmd/sforum/seed_run.go
```

本次审计未执行测试、未连数据库、未修改任何生产文件（除本报告与上一份报告的 §6.1 更正）。
