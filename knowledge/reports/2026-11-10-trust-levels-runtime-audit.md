# 2026-11-10 信任等级 · 运行时证据审计（只读）

审计人：researcher｜方式：只读 `rg` / 读文件，未改任何生产代码
对象：`knowledge/plans/2026-11-10-trust-levels.md` §1 §2 §3 §10
基线：工作区当前 HEAD（未做 `git stash`/分支切换）

---

## 0. 结论摘要（先看这 5 条）

1. **§5 的能力映射表在现状下是空操作。** 默认注册组 `member` 已经在**迁移里**静态持有 `topic.create` / `post.create` / `attachment.upload`。TL2「解锁」这些键对任何已注册用户零效果 —— earned 是纯加性的，加一个已经有的键不改变任何真值。这是本刀最大的设计缺口，不是实现细节。
2. **`Can()` 里有一个会让 earned 永远失效的早退分支**：`policy.go:29 if a.Permissions == nil { return false }`。naive 合并 earned 会在这里被吞掉。
3. **`identity` 包不能 import `Options`，也不能 import `Badges`**（两者都 import identity）。所以 D4「`LoadActor` 第 3 次查询」在结构上不成立；阈值、开关、活动计数都必须经 `Providers` 注入。
4. **`LoadActor` 现在发 4 次查询，不是 2 次**；且它有 45 个非测试调用点、单请求可能调用 ≥2 次。G 组的性能预算是 +N 次而不是 +1 次。
5. **`trust.new_user.forbid_attachments` 是一个死键**（第二个 `badge.view`）：UI 能写、normalize 认、public 可见、`TrustPolicy` 结构体有字段，但**零消费者**。收编时"顺手修好"就会破 F 组等价。

---

## 1. §2「现状核实」表逐行判定

| # | 计划所述 | 判定 | 实际证据 |
| --- | --- | --- | --- |
| 1 | 权限裁决在领域层，路由目录不是运行时守卫；`route_ingress.go:43` 只做 planning | **证实（行号偏 3）** | `app/Http/route_ingress.go:23-25` 注释明写 "performs planning only. It never loads an actor"；`/api/v1` 分支在 `:36-41`，`:40` 直接 `c.Next()`；非 API 路径在 `:59 classifyRouteIngress` 才 `plans.BuildExecutionPlan`，`:85` 返回 `c.Next()`。计划写的 43 是块尾括号，关键行是 **40**。 |
| 2 | `Can` 语义 = active → super_admin 绕过 → `hasExpandedPermission`；`policy.go:22`、`permission_compat.go:57` | **证实（漏一个分支）** | `app/Models/Identity/policy.go:19` `func (a Actor) Can`；`:20` active 判定；`:26-28` super_admin 直接 true（`:23-25` 注释说明它**故意**绕过 deny）；**`:29-31` `Permissions == nil → false`（计划未提，见 §8 T13）**；`:33` → `permission_compat.go:57 hasExpandedPermission`（父权限继承子权限）。 |
| 3 | deny 覆盖已在 DB 层生效，`postgres_store_permissions.go:49` | **证伪（位置错，语义成立）** | `postgres_store_permissions.go:49` 属于 `ListActiveUserIDsWithPermissionTx`（**通知收件人**解析，`:13` 注释自述"matching Actor.Can"），不是 actor 装载路径。actor 的 deny 排除真身在 `database/queries/identity.sql:60-76`（`denied_permission_keys` CTE + `WHERE NOT EXISTS`），生成物 `database/sqlc/identity.sql.go:396`。**结论对、引用的文件错**；实施方按计划去读 `:49` 会得出"deny 在 Go 层过滤"的错误模型。 |
| 4 | `LoadActor` 目前发 2 次查询；`postgres_store.go:380` | **证伪（数字错）** | `postgres_store.go:380` 函数本身：① `GetCurrentUser`（`:104-117` 一条 users 三表 LEFT JOIN）② `loadCurrentUserAccess`（`postgres_store_roles.go:246-263`）内部 `ListUserRoleKeys` + `ListUserPermissions` = 2 条 ③ `postgres_store.go:392` `SELECT created_at, kind FROM users`。**合计 4 次往返**。计划说 2 次，把 `GetCurrentUser` 当成 1 条了。 |
| 5 | 已存在「新人信任阶梯」：`service_ops.go:513 trustForActor`、`service_comment_limits.go`、`WithTrustPolicy(service.go:90)`、`Providers/forum.go` | **证实（调用点数量与判据需修正）** | 全部存在：`service_ops.go:513`（定义）、`service_comment_limits.go:48-57 trustLimits` + `:63-71 TrustPolicyResolver`、`service.go:89-92 WithTrustPolicy`、`Providers/forum.go:76-90` 装配 + `:216-266 TrustPolicyAdapter`（6 个方法）。**调用点 6 个**：`service_ops.go:342`、`service_ops.go:480`、`service_comment_limits.go:22`、`service.go:584`、`service.go:752`、`service.go:986`。 |
| 6 | 该阶梯的配置面：`Options/types.go:115-121` 七个键 | **证实（行号准确）** | `app/Models/Options/types.go:115-121` 七行常量一一对应。全链见 §3。但其中一个键无消费者（见下条与 §3）。 |
| 7 | 判据只有账号年龄（`actor.CreatedAt`），两态 | **证伪** | `service_ops.go:513-548` 实际有 **5 道判据**：`:516` `s.trust == nil || CreatedAt.IsZero()` → 不限制；`:521` `actor.Kind.IsBot()` → 不限制；`:527` `days <= 0` → **整条阶梯关闭**；`:530` `time.Since(CreatedAt) > days*24h` → 不限制；`:534` `Can(settings.manage) || Can(topic.edit_any)` → 不限制。收编时必须逐条保留这 4 个旁路，否则 F 组（关开关=逐字节等价）必红。 |
| 8 | 权限键落库有奇偶校验测试 `permission_seed_parity_test.go`（`TestEverySeededPermissionReachesTheDatabase`） | **证实** | `database/migrations/permission_seed_parity_test.go:23` 函数存在；`:13-22` 注释说明它钉的是「`seeds.go` 有、迁移 SQL 没有」。**它只覆盖权限键，不覆盖 option 键** —— 新增 `trust.*` option 时它不会报红。 |
| 9 | 统计层就绪：`Badges/activity_store.go` / `visits.go` 是读 `user_activity` / `user_visits` 的现成入口 | **部分证伪** | `activity_store.go:58 GetCounters` 确实读 `user_activity` 单行（`:70-76` 无行返回全 0 且不报错，正好是 fail-closed 要的语义）。但 **`visits.go` 不读 `user_visits`**：它只有 Redis SETNX 去重（`VisitDedupKey` `:20`、`RedisVisitDeduper` `:33`），文件内无任何 SQL；`user_visits` 的读写都在 `activity_store.go`（`:105-138 RecordVisit`、`:152 Recompute` 里的 `COUNT(*) FROM user_visits`）。所以「`user_visits` 的读入口」目前**不存在**，`days_visited` 只能从 `user_activity` 读。 |

---

## 2. `identity.Actor` 全部构造点清单

`type Actor struct` 定义：`app/Models/Identity/types.go:209-218`，7 个字段（`ID / Status / RoleKeys / Permissions / CreatedAt / Kind`）。

计数（`grep -rn "Actor{" --include=*.go apps/api`）：

| 类别 | 数量 | 说明 |
| --- | --- | --- |
| `identity.Actor{}` 零值（全部） | 150 | 其中 **66 处非测试** |
| `identity.Actor{字段…}` 带字段（全部） | 248 | 其中 **仅 3 处非测试** |
| identity 包内裸 `Actor{…}` 非测试 | 20 | `LoadActor` 返回值、store 错误路径 |
| `Actor{` 全形态总计 | **664** | 非测试 **98**，测试 **566** |

### 2.1 生产代码（代表性位置，非测试）

- **唯一权威装载点**：`app/Models/Identity/postgres_store.go:380 LoadActor`（`:400-408` 组装 `ID/Status/RoleKeys/Permissions/CreatedAt/Kind`）。
- **HTTP 每请求装载**：`app/Http/auth_user.go:47`（必需）、`app/Http/auth_user.go:85`（可选）。`app/Http/auth_user.go:39/42/45/49/80/83/87` 是零值错误返回。
- **控制器二次装载（各 domain 自带 helper）**：`app/Http/Controllers/{Attachments/controller.go:531, Database/controller.go:94, Moderation/controller.go:246, AdminOverview/controller.go:49, Options/controller.go:171, Webhooks/controller.go:203, AI/controller.go:426, SEO/controller.go:91, Profile/controller.go:195, Extensions/controller.go:817, Mail/controller.go:40, Notifications/controller.go:113, SiteChrome/controller.go:414, Forum/controller.go:750 & 905, SystemUpdates/controller.go:49, Jobs/controller.go:160, Pages/controller.go:941}` —— 共 18 个 helper 直接转调 `apphttp.LoadActor`。
- **公开读路径也会装载**：`app/Http/Controllers/Forum/controller.go:750` `viewer, _ := apphttp.LoadActor(...)`（列表页拿 viewer 算权限）。这是 G 组"公开列表不许变慢"的正面风险点。
- **插件桥 / HostAPI 合成 actor（不经 LoadActor）**：`app/Http/core_guard_authorizer.go:210` `identity.Actor{ID: …, Permissions: maps.Clone(...)}`，`:212` 才补 `Status=active`，`:215-217` **用 `Permissions["*"]` 反向合成 `RoleKeys=[super_admin]`** —— 这是 `LoadActor` 之外的第二个 super_admin 来源，TL 合并必须对它中性。另有 `app/Support/HostAPI/v2_command_actor_postgres.go:98-101`（失败返回零值）、`bootstrap/query_registry.go:86-96`、`bootstrap/privacy_registry.go:66`、`bootstrap/notification_adapter.go:41`、`app/Support/Extensions/protocol_v2_{routes,guards,actor_delegation,identity_runtime}.go`（构造的是 `protocolv2.Actor`，不是 `identity.Actor`，经上面那两处转换）。
- **CLI**：`cmd/sforum/{reset_password.go:312, seed_taxonomy.go:249/262/268/272, ai_derived.go:177/280, seed_run.go:163/200, seed_profiles.go:99, privacy_erase_user.go:102/120}` —— 共 5 处 `identity.Actor{` 字面量 + 多处 `LoadActor` 调用。
- **bot / system actor**：`app/Models/Extensions/systemactor/system_actor.go:44-102`（三处 `identity.Actor{}` 错误返回 + `:53/97/102` 转调 `LoadActor`）、`app/Models/AIReply/poster.go:52`、`app/Models/AITools/aitools.go:229-236`、`app/Models/Extensions/dev_auto_runtime.go:329`、`app/Models/APITokens/{scoped_store.go:15, service.go:148}`、`app/Providers/ai_reply_image_reader.go:31`、`app/Models/Attachments/UploadPolicy/service.go:156/177/200`。
- **系统内部写权限的合成 actor（非人类）**：`app/Models/Attachments/service.go:591` `identity.Actor{ID: 0, Status: active, Permissions: {attachment.settings.manage: true}}` —— 存储 provider 回落的系统操作。
- **零值 actor 传进领域层**：`app/Http/Controllers/Profile/controller.go:63`、`app/Http/Controllers/Profile/bookmarks.go:33`、`app/Http/route_dispatcher.go:303`、`app/Models/Profile/service.go:89`。

### 2.2 测试与 fake（决定「零值 TL 能不能不改测试」）

- `Actor{` 出现在测试文件共 **566** 处，覆盖 `app/Models/**`、`app/Http/**`、`app/Support/**`、`cmd/**`。
- **fake store**：`app/Models/Identity/component_permissions_test.go:16`（`componentPermissionActorStore.LoadActor`）、`app/Models/Identity/service_test.go:1032`（`fakeStore.LoadActor`）—— 计划 T2 点名的两个都在。
- **接口桩**：`app/Models/APITokens/service.go:27`、`app/Models/Identity/store.go:69`、`bootstrap/notification_adapter.go:21`、`cmd/sforum/seed_run.go:42` 都声明了 `LoadActor(ctx, int64) (identity.Actor, error)` 的最小接口 —— **`Actor` 加字段不会破这些接口**（返回结构体，不加返回值）。

**结论**：零值 `TrustLevel` 语义（=0，无 earned）**可以**做到不改任何既有测试，前提是：
- `Actor` 只**新增字段**、不改既有字段名与类型；
- `Can()` 在 earned 为空时走**与今天逐字节相同**的分支（注意 §8 T13 的 `Permissions == nil` 早退必须先于 earned 判断，否则 nil map 的测试 actor 行为会变）；
- 不新增 `LoadActor` 的返回值（否则 4 个接口声明 + 2 个 fake 要改）。

---

## 3. `trust.new_user_*` 七键全链（漏一处就是两个答案）

`apps/api` 侧注册面（每个键都要过这 5 道；`forbid_attachments` 已走完但无消费者）：

| 环节 | 位置 | 备注 |
| --- | --- | --- |
| 常量 | `app/Models/Options/types.go:115-121` | 7 行 |
| 目录定义（public + managePermission） | `app/Models/Options/community_policy_options.go:74-80` | 全部 `public: true, managePermission: forum`（`:75` 注释"对前端 composer 提示公开"） |
| 推荐默认值 | `app/Models/Options/community_policy_options.go:103 communityPolicyRecommendedDefaults`（7 键在 `:114-120`） | `forbid_attachments` 默认 `false`（`:120`） |
| coerce（布尔/整数规范化） | `app/Models/Options/community_policy_options.go:143` 起；布尔组在 `:163-174`、`:279-290`（isValid 同款循环） | 三处独立 `[]string{...}` 枚举列表，**同一个键要出现在 3 个列表里** |
| normalize | `app/Models/Options/service_normalize.go:135-172` case 组（信任键在 `:146-151`）→ `normalizeCommunityPolicyOption`（`community_policy_options.go:343`），具体分支：`:359-360`（两个布尔）、`:374`（days）、`:376-377`（冷却）、`:378-379`（日限额） | `default: return "", false`（`:398-399`）= 未登记的键直接拒绝 |
| 持久化白名单 | `service_normalize.go:16 allOptionNames()` 从 `optionDefinitions` 派生 | 未进 definitions 的键即使 normalize 通过也会被 `UpdateMany` 静默丢弃（`service.go:520-524` 只遍历 `allOptionNames()`） |
| 写入校验 | `app/Models/Options/service.go:477 UpdateMany`：`:492` `optionDefinitionFor` 不存在 → `ErrInvalidOption`；`:496` 无 `managePermission` → `ErrPermissionDenied`；`:503` normalize 失败 → `ErrInvalidOption`；`:512` `isValidValueSet` | 三道闸，任一缺失即"保存失败但说不出为什么" |
| Forum resolver | `app/Models/Forum/service_comment_limits.go:63-71 TrustPolicyResolver`（6 方法）→ `app/Providers/forum.go:216-266 TrustPolicyAdapter`（6 方法）→ `app/Models/Options/community_policy_options.go:535 TrustPolicy(ctx)` 返回 `TrustPolicy` 结构体（`:525-533`，含 `ForbidAttachments` 字段 `:532`） | **接口只有 6 个方法，没有 forbid_attachments** |
| 消费 | `app/Models/Forum/service_ops.go:513-548 trustForActor` → `:536-546` 只读 5 个值 + days | `forbidAttach` 字段（`service_comment_limits.go:54`）**从未被赋值、从未被读** |
| 后台 UI | `apps/web/app/components/admin/settings/site/tabs/SFAdminSiteNewcomersTab.vue`：读 `:26-32`、写 `:54-61`、恢复默认 `:75-83`、控件 `:134`（forbid_attachments 复选框） | 挂在 `pages/admin/settings/index.vue` 的 `newcomers` 固定 tab |
| i18n | `apps/web/i18n/locales/zh-CN.json:4371-4372`、`apps/web/i18n/locales/en-US.json:4331-4332` | 其余 6 键在同一 `admin.settings.newcomers.*` 命名空间 |
| 测试 | `apps/api/app/Models/Options/community_policy_options_test.go:141`（`forbid_attachments: "disabled"` 参与 normalize 断言） | 另有 `service_test.go` 的 option 全量校验 |

**判定：`trust.new_user.forbid_attachments` 是死键。** 证据链：类型有、目录有（public）、默认有、normalize 有、UI 有、i18n 有、`TrustPolicy.ForbidAttachments` 字段有 —— 但 `TrustPolicyResolver` 无对应方法（`service_comment_limits.go:63-71`）、`TrustPolicyAdapter` 无对应实现（`Providers/forum.go:220-266` 只 6 个）、`app/Models/Attachments/**` 全包 grep `Trust|trust` **零命中**。这是 `badge.view`（D9 点名的死权限）的同构问题。

---

## 4. options 面是否支持嵌套键 `trust.level.*`

**结论：不支持"前缀/通配"语义，只支持显式枚举。** `trust.level.*` 不会被自动接受 —— 每加一个键必须在 5 处登记，缺一处就是静默失败或保存报错。

证据：
1. **写入**：`service.go:492-494` `optionDefinitionFor(name)` 精确匹配 `optionDefinitions`，找不到即 `ErrInvalidOption`（无前缀回退）。
2. **归一化**：`service_normalize.go:146-151` 是显式 `case` 列表；`community_policy_options.go:398-399` `default: return "", false`。
3. **持久化**：`service.go:520-524` 只写 `allOptionNames()`（= `optionDefinitions` 的 name 集）里的键 —— **normalize 通过但没进 definitions 的键会被静默丢弃**，这是最隐蔽的一处。
4. **公开可见性**：`community_policy_options.go` 的 `{name, public, managePermission}` 三元组；`public: true` 才进 web options，`adminOptions`（`service.go:710`）再按 `Can(managePermission)` 过滤。

**对本刀的真实约束（要写进 M1/M4）**：

- 若按 §5 做「每级每指标一个阈值键」：5 级 × 6 指标 ≈ 20–30 个键 × 5 处登记 ≈ **100–150 处改动**，外加 i18n 每条 2 份、后台 UI 每个键一个控件。`isValidValueSet` 还要做跨字段单调性校验（TL3 阈值 ≥ TL2 阈值）。
- 若改成一个 JSON 值键（如 `trust.level.thresholds`）：1 个键 × 5 处登记，normalize 里做严格 schema + 版本字段解析，`public: true` 让前端能算"再获 3 个赞"。代价是破坏本仓库"一键一设置"的惯例，后台要写结构化编辑器，且 `UpdateMany` 的 `isValidValueSet` 需要一条整体校验。
- **两种都可实现**；计划 §4 只写了"走既有 options 面"，没写这个量级差。建议在 M0 的 ADR 里定稿形状，别留到 M4。

---

## 5. `user_activity` 的现成读入口：能不能复用

**能复用，但不能靠 import。** 三条硬事实：

1. **读入口存在且语义正好**：`app/Models/Badges/activity_store.go:20-25` 的 `ActivityStore` 接口暴露 `BumpTx / GetCounters / RecordVisit / Recompute`；`GetCounters`（`:58-87`）一条 PK 查询取 6 个计数 + `days_visited`，**行缺失返回全 0 且不报错**（`:73-75`）——正是 D7 fail-closed 要的形状，不需要第二份 SQL。
2. **但 `Identity` 不能 import `Badges`**：`app/Models/Badges/admin_service.go:8` 已经 `import identity`。反向 import = 编译期循环。**同理 `Identity` 也不能 import `Options`**（`app/Models/Options/service.go:477 UpdateMany` 签名收 `identity.Actor`，`feature_flags.go:5` 直接 import identity）。
3. **现有代码的先例就是"消费方定义接口 + Providers 装配"**：Forum 定义 `TrustPolicyResolver`（`service_comment_limits.go:63`），`Providers/forum.go:216` 提供适配器。TL 必须照抄这个方向。

**推荐形状**（不改 D1/D4 的语义，只修结构）：

- 新包 `app/Models/TrustLevels/` 定义 `Activity`（6 计数 + days）、`LevelPolicy`、纯函数 `Level(activity, accountAge, policy)`、`Reader interface { Counters(ctx, userID) (Activity, error) }`。该包**不得 import identity**。
- `identity` 侧加一个注入点（字段或 setter）：`TrustResolver interface { Resolve(ctx, userID int64, createdAt time.Time) (level int, earned map[string]bool) }`，`LoadActor` 调它，失败即 `(0, nil)`。
- `Providers`（或 `bootstrap`）提供适配器：`badges.PostgresActivityStore.GetCounters` + `Options` 阈值 + `features.trust_levels` → `TrustResolver`。

**如果实施方选择在 `identity` 里再写一条 `SELECT … FROM user_activity`**：那就产生了第二份读逻辑（列名/语义与 `GetCounters` 并行维护，`days_visited` 的真值来源还要再解释一次）。这是真实风险，必须在 ADR 里显式否掉或显式接受，不能默认发生。

---

## 6. `.Can(` 的真实数量与「限额/权限错位」抽查

### 6.1 数量（**更正**：计划写的 256 是对的）

上一版把这里写成"226，256 是证伪"——**那次证伪本身是错的**。两个数都真实，但度量不同：

| 度量 | 非测试 | 全仓含测试 | 命令 |
| --- | --- | --- | --- |
| `.Can(` **出现次数**（= 计划口径，正确的"调用点"） | **256** | **280** | `rg -o "\.Can\(" -g '*.go' -g '!*_test.go' \| wc -l` |
| `.Can(` **所在行数**（我上一版用的口径，度量错了） | 226 | — | `grep -rn "\.Can(" --include=*.go . \| grep -v _test.go \| wc -l` |
| `Can(` 出现次数（含方法声明与 `CanEditPost` 等） | 230 | 256 | `rg -o "Can\(" -g '*.go' -g '!*_test.go' \| wc -l` |

差额 30 = 30 行里出现了 2 个及以上 `.Can(`（典型是 `a && b` 短路表达式，如 `revisions_mutation.go:16`、`revisions_service.go:105-111`）。行数永远小于等于次数，把行数当调用点数是我上一版的错。

**结论**：§1/§11 的 256 **不用改**。§6.2 的分布数字（按目录）也是按行统计的，量级可用但同样偏小，若要写进验收脚本请统一用 `rg -o` 口径重算。

### 6.2 Forum / Attachments 创建路径抽查（有没有"本该限额却用了权限"）

- **Forum 发主题**：`app/Models/Forum/service.go:556` `Can(topic.create)`（能力闸）→ `:579 enforceTopicCreateLimitsForActor`（限额闸）→ `:584 trustForActor` 外链闸。**分层正确**，符合 D8。
- **Forum 发回复**：`service.go:954` `Can(post.create)` → `:986` 外链闸 + `service_comment_limits.go:22` 冷却/日限。**分层正确**。
- **Attachments 上传**：同一能力有 **5 个独立判断点**，全部走 `Actor.Can`：`app/Models/Attachments/upload.go:59`、`upload_policy.go:27`、`upload_policy.go:134`、`app/Models/Attachments/UploadPolicy/service.go:25`、`app/Http/core_guard_authorizer.go:398`；另有 3 处 HostAPI 命令声明 `RequiredPermissions: [attachment.upload]`（`v2_command_attachment_extensions.go:87/177/228`）。**没有**发现"用权限表达限额"的错位（尺寸/数量限额走 `upload_policy` 的 policy 值，不走权限键）。
- **但发现一处"用权限当限额豁免"**：`service_ops.go:534` `if actor.Can(settings.manage) || actor.Can(topic.edit_any) { return trustLimits{} }` —— 管理权限被当作"免新人限制"的开关。这在今天合理，但收编后含义会变：**TL 挣来的 `topic.create` 不会带来豁免，静态的管理权限会**。D8"权限与限额分离"在这一行上已经被打破过一次，收编时要显式决定是否保留。
- **另发现两个零调用的死包装**：`service_ops.go:471 enforceTopicCreateLimits(ctx, authorUserID, …)` 与 `:509 enforceCommentCreateLimits(...)` 在全仓（含测试）**没有任何调用点**。它们内部 `identity.Actor{ID: authorUserID}` → `CreatedAt` 零值 → `trustForActor` 直接返回"不限制"。收编时应删除，否则将来有人调它 = 静默跳过全部 TL 限额。

---

## 7. 与计划冲突之处（计划 / 代码 / 建议）

### C1（严重）§5 的 TL 解锁集合全员已持有 → TL 的权限维度是空操作

- **计划**：§5 表 TL2 解锁 `topic.create`、`attachment.upload`；D3 说"只复用现有权限键"；§5 尾注要求 PM 只核实"键存在且不属于管理类"。
- **代码**：`app/Models/Identity/seeds.go:153-161 SeedMemberPermissions` 已含 `topic.create`、`post.create`、`attachment.upload`、`topic.edit_own`、`post.edit_own`、`post.delete_own`、`topic.delete_own`；迁移侧 `database/migrations/202607040001_identity_rbac.sql:109`（member ← topic.create/post.create/post.edit_own/post.delete_own）与 `database/migrations/202607050004_attachments.sql:57-61`（member ← attachment.upload）已写入库。默认注册角色 `is_default`（`202607040001_identity_rbac.sql:34/42-43`）保证每个新用户都拿到。
- **后果**：earned 纯加性 ⇒ 给一个已有的键不改变任何 `Can()` 真值 ⇒ TL 对"能不能发帖/上传"零影响。§5 的整张表除了限额列之外全是装饰。
- **建议**：三选一并写进 ADR，不能含糊：
  1. **承认 TL 的载荷是限额**（D 组），§5 权限列只保留"将来可能"的注释，M8 不为此写断言；
  2. **真的收走 member 的键**：`SeedMemberPermissions` 与迁移同步移除 `topic.create`/`attachment.upload`，TL2 的 earned 集合补回。**这必然要求改 D9/F 组**：开关关闭时不能是"已挣得集合恒为空"，而必须是"全部成员按今天的 member 模板静态授予"（否则关开关 = 全站不能发帖）。计划 `:118` 与 `:154` 在这里直接矛盾。
  3. 只在**不在 member 模板里**的键上做 TL 解锁 —— 按当前 catalog，这类非管理键基本不存在，等于方案 1。

### C2（严重）D4 的「LoadActor 第 3 次查询」结构上不可行

- **计划**：`:103` "读取位置：`LoadActor` 的第 3 次查询（`user_activity` 单行 PK）"。
- **代码**：`LoadActor` 现在就是 **4 次**查询（§1 行 4）；`identity` 不能 import `Badges`/`Options`（§5 第 2 条）。所以"在 LoadActor 里直接查"要么复制 SQL，要么先建接口注入。
- **建议**：改成"由注入的 `TrustResolver` 解析；`LoadActor` 自身不新增 SQL"。同时把 G 组的基线从"2 → 3"改成"4 → 5（每 `LoadActor` 调用一次）"，并统计单请求的 `LoadActor` 次数（§2.1 的 18 个控制器 helper + `auth_user.go` 两处）。

### C3 §2 行 7「判据只有账号年龄」低估了 4 个旁路

- **计划**：§2.1 "年龄从唯一判据降级为上限闸"。
- **代码**：`service_ops.go:516/521/527/534` 还有 `CreatedAt` 零值、bot、`days<=0`、管理权限四个旁路。
- **建议**：`limitsForActor` 必须逐条保留这 4 条，并在 D 组加 4 条断言（bot 不受 TL 限额、`days=0` 保持"整条阶梯关闭"、`CreatedAt` 零值不限额、管理权限豁免）。`trust.new_user_days = 0` 的语义尤其危险：今天它是"关闭"，改成分档后若变成"所有人 TL0"就是行为反转。

### C4（已撤回）§1/§11 的 256

- **本条作废**：256 是对的，我上一版用行数当次数，误判成 226。见 §6.1 的度量对照表。保留此条只为留下纠错痕迹，计划无需改动。

### C5 §2 行 3 的 deny 引用位置错

- 见 §1 行 3。建议引用改为 `database/queries/identity.sql:60-76` + `database/sqlc/identity.sql.go:396`，并把 T4 的验证点从"Go 层再排除一次"改成"证明 earned 集合不含 deny 键"。

### C6 §2 行 9 把 `visits.go` 当成 `user_visits` 读入口

- 见 §1 行 9。建议改成"`activity_store.go:58 GetCounters` 是唯一读入口；`user_visits` 只被 `RecordVisit` 写、被 `Recompute` 的 SQL 聚合读"。

### C7 §3 D9「不做 trust.view」的理由与代码不符

- **计划**：`:117` "上一刀的教训：`badge.view` 是死权限（可达性依赖 `admin.access`）"。
- **代码**：`badge.view` 不是死权限 —— 它在 `seeds.go:184`（moderator 模板）与 `apps/web/app/config/roleTemplates.ts` 的 moderator 包里被真实授予，且 `app/Models/Badges/admin_service.go` 用它做只读判定。它"看起来死"是因为后台页可达性另有 `admin.access` 前置。结论（不做 `trust.view`）仍成立，但**理由要换成**"后台读页已有 `admin.access` 前置，再加 `trust.view` 会产生两个都必须为真的闸"，不要引用一个不准确的先例。

---

## 8. 计划没写、实施方一定会踩的坑（T13–T22）

**T13 `Can()` 的 `Permissions == nil` 早退会吞掉 earned。**
`policy.go:29-31`：`Permissions` 为 nil 直接 `return false`。今天所有"只有角色没有权限 map"的 actor 靠它短路。合并 earned 时必须把顺序改成 `active → super_admin → (static || earned)`，并单独处理 nil map，否则：nil-map actor 的 earned 全部失效；或者改错方向让 nil-map actor 意外通过。

**T14 deny 在 Actor 里没有留痕，earned 会复活被 deny 的键。**
deny 是在 SQL 里**减掉**的（`identity.sql:60-76`），`Actor.Permissions` 里根本没有"这个键被 deny 了"的信息。若 earned 集合包含 `topic.create` 而该用户被 deny 了 `topic.create`，`Can()` 的 OR 合并会让它复活 —— 直接违反 T4。修法只有两条：① earned 集合在**装载时**与 deny 集合求差（需要 `Actor` 新增 `DeniedKeys` 或让 resolver 拿到 deny 集）；② `Can()` 里保留 deny 集合查询（违反 T3）。计划必须选 ①，且 `LoadActor` 要多取一次 overrides（`listPermissionOverrides`，`postgres_store_roles.go` 已有现成方法，但只在 admin 路径用）。

**T15 `trustForActor` 有 6 个调用点 + 2 个死包装。** 见 §6.2。改名 `limitsForActor` 时一并删 `service_ops.go:471`/`:509`。

**T16 `trust.new_user_days = 0` 今天是"整条阶梯关闭"。** 见 C3。

**T17 `LoadActor` 的放大效应。** 45 个非测试调用点、18 个控制器 helper、公开列表路径 `Forum/controller.go:750` 也装载。G 组要按"单请求 `LoadActor` 次数 × 1 查询"估，而不是"每请求 +1"。

**T18 `features.trust_levels` 的注册面比 `trust.*` 多一处。** 除了 §3 的 5 道，feature flag 还要：`feature_flags.go` 常量（`:20-32` 区）、`featureFlagOptionDefinitions()`（`:47-61`，靠 `init()` `:43-45` append 进 `optionDefinitions`）、`FeatureFlags` 结构体字段（`:110-120`）、`featureFlagsFromValues`（`:131-144`）、`isKnownFeatureFlag`/`FeatureFlagCatalog`（`:223-230`）。漏 normalize（`service_normalize.go:189`）的表现是"保存 400 但 UI 只说失败"。

**T19 `roleTemplates.ts` 是手抄副本且没有 member 条目。** 文件头（`apps/web/app/config/roleTemplates.ts:1-12`）自述"独立的一份定义，不是从 seeds.go 生成的"，只含 moderator/operator/tech_admin。`trust.manage` 若进 operator 模板要**双改**（`seeds.go:188-215` + `roleTemplates.ts`）；member 权限不在这个文件里，所以 C1 的方案 2 只改后端 seeds + 迁移。

**T20 `forbid_attachments` 是死键。** 见 §3。收编时"顺手接上"= 行为变更，F 组会红。建议本刀保持死，另开一刀处理，并在 knowledge 里记一笔。

**T21 奇偶校验测试不覆盖 option 键。** `permission_seed_parity_test.go` 只钉权限键（`:13-22` 注释自述）。新增 `trust.level.*` 没有任何结构性守卫保证"代码里有、默认值里有、后台能写" —— 建议顺手加一条同构的 option parity 测试（或至少 `community_policy_options_test.go` 里断言 definitions ∩ defaults ∩ normalize 三集合相等）。

**T22 `CapabilitySource` 要回答"为什么不能发帖"，但 `Actor` 里没有"差多少"的信息。** `LoadActor` 只装载当前值；前端要"再获 3 个赞可解锁"就必须拿到 (metric, current, threshold)。若派生视图只给 `earned|static|none` 而不给余量，M5 的进度卡只能前端重算一遍阈值（= 第三份副本）。建议派生视图直接给 `[]Requirement{metric, current, threshold}`，由 `TrustLevels` 包算，随 actor 一起装载。

---

## 9. 核实方式（可复现）

```bash
cd apps/api
grep -n "c.Next()" app/Http/route_ingress.go                      # 40 / 55 / 85
grep -n "" app/Models/Identity/policy.go | sed -n '19,34p'        # Can 全分支
grep -n "effect = 'deny'" app/Models/Identity/postgres_store_permissions.go
grep -n "denied_permission_keys" -A 6 database/queries/identity.sql
sed -n '380,410p' app/Models/Identity/postgres_store.go           # LoadActor
sed -n '246,263p' app/Models/Identity/postgres_store_roles.go     # loadCurrentUserAccess
sed -n '513,548p' app/Models/Forum/service_ops.go                 # trustForActor 五道判据
grep -rn "trustForActor" app/ | cat                               # 6 调用点 + 定义
grep -rn "forbid_attachments\|ForbidAttachments" --include=*.go app/
rg -o "\.Can\(" -g '*.go' -g '!*_test.go' | wc -l            # 256（出现次数，正确口径）
grep -rn "\.Can(" --include=*.go . | grep -v _test.go | wc -l     # 226（行数，勿当调用点）
grep -rn "identity\.Actor{}" --include=*.go . | wc -l             # 150 / 非测试 66
grep -rn "SeedMemberPermissions" -A 10 app/Models/Identity/seeds.go
grep -rn "topic.create" database/migrations/202607040001_identity_rbac.sql
grep -rn "attachment.upload" database/migrations/202607050004_attachments.sql
grep -rn "Models/Identity" --include=*.go app/Models/Badges/      # Badges → Identity
```

注：本次审计未执行任何测试、未连接数据库、未改动任何文件（除本报告）。
