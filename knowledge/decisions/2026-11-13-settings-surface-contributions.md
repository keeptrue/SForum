# 2026-11-13 插件设置面贡献点（settingsSection）

Status: **landed（未提交，工作树改动）**
模块笔记：`knowledge/modules/profile.md`、`extensions.md`、`frontend.md`、`options.md`
Handoff：`knowledge/sessions/2026-11-13-settings-surfaces.md`
作者指南：`docs/extensions/authoring-guide.md`「Settings tabs and profile fields (`settingsSection`)」
代码事实源：`apps/api/app/Support/ExtensionManifest/settings_section_{payload,validate}.go`、
`apps/api/app/Providers/settings_surface_{contributions,sync,governance,policy,meta}.go`、
`apps/api/database/migrations/202611130001_settings_surface_items.sql`

## 问题

插件要「一点设置」，此前只有两条路：整页 Settings Document（站点级、运营可见），
或者自己造一个插件页面（要建路由、要建表、要自己画 UI）。个人侧的设置根本没有出口——
用户想调一个只作用于自己的偏好，插件只能塞进自己的库表，于是：

- 值不跟用户走（换账号、卸载重装就丢），也不享受宿主既有的可见性过滤；
- 每个插件自己实现一遍「谁能看、谁能改、出错贴哪个输入框」；
- 运营对插件塞进设置页的东西没有任何统一开关：不喜欢只能卸插件。

本刀的目标是把「设置页上的一个 tab / 一个字段」变成**宿主拥有的贡献点**，
让插件只声明形状，存储、权限、治理、错误定位全部复用宿主既有机制。

## 决定 1 — 两个贡献点，而不是一个泛化点

`forum.settings.profile.tabs`（个人设置页）与 `admin.settings.tabs`（站点设置页）
分开声明，共用宿主拥有的 payload 类型 `settingsSection`。

为什么不合并成一个 `settings.tabs` 再靠 `scope` 字段区分：两个表面的差不是命名空间，
而是**三个不可互换的事实**——存储位置（按用户 entity-meta / 插件站点设置存储）、
权限基准（登录用户 / `admin.access`）、允许的 tab 形态（`fields`|`component` / 仅 `settings`）。
一个泛化点会把这些差异推到运行期判断：校验器要先读 `scope` 才知道该不该要求 `tabId`，
前端要先读 `scope` 才知道面板归谁保存，扩展点目录也不再能一行说清「这个点允许什么」。
分开声明后，`settingsSectionTabKinds(point)` 是一张编译期写死的矩阵，
`validateSettingsSectionContributionPayload` 不需要任何运行期开关。

**代价**：同一份 payload 结构被两个点引用，字段级的形态互斥（`fields` 与 `componentId`
与 `tabId` 不能同时出现）必须在校验里逐条写出来，而不是靠类型系统。

## 决定 2 — 站点设置页只支持 `settings` 形态（admin 侧没有可复用的 L2 运行时）

`admin.settings.tabs` 只接受 `type=settings`，即引用本包 Settings Document 里的一个 tab
（`tabId`），不支持 `fields`，也不支持 `component`。

理由是可复用面不存在，而不是不想做：

- **`component`**：公开 L2 运行时是站点**公开面**的挂载通道（公开组件、公开 digest、
  SRI、公开页 CSP）。管理端没有对应的运行时——后台的插件 UI 走的是另一条
  「可信 admin 组件 + 预构建产物 + 精确授予」的路，权限模型、CSP 聚合、失败隔离都不是
  同一套。把公开 L2 挂到后台等于给插件在管理会话里开一个公开面级的执行面，
  这是权限模型的倒退。
- **`fields`**：站点设置的值属于**站点**，宿主既有的存储与「推荐默认 + 一键恢复默认」
  语义都在插件 Settings Document 与 settings 存储里。再开一套 admin 字段会得到
  两份站点配置真相（一份在 document，一份在 entity-meta 式的表），而运营看到的
  「恢复默认」到底恢复哪一份将无法回答。

**代价**：想在后台做一个纯前端表单（不落插件设置）的插件做不到，必须先把字段写进
Settings Document。这是有意的收窄：站点级配置本来就该有 schema、默认值与恢复默认。

## 决定 3 — 治理用独立表，而不是塞进 options JSON

运营对 tab / 字段的可见性与顺序，存在独立表 `settings_surface_items`
（`extension_id, point, contribution_id, item_key, enabled, sort_order, updated_by_user_id`）。

为什么不用 `web_options` 的一个 JSON 键：

- **并发**。options 是「一个键一个值」的整块写入。两个管理员同时关掉两个不同插件的
  tab，后写者会把前者的覆盖整块抹掉。独立表是行级写入，天然按目标隔离。
- **语义**。options 是「运营配置的站点参数」，有公开读面（`GET /web-options`）、
  有推荐默认、会进缓存。治理覆盖是**宿主治理态**（谁还能看见什么），没有公开面，
  也不该被「恢复默认设置」这类一键操作顺手清掉。
- **可追溯**。行上有 `updated_by_user_id`，注销后 `ON DELETE SET NULL` 保留覆盖结果
  （治理结果不该随账号消失）。JSON 里塞 `{enabled, sortOrder, by, at}` 也能做到，
  但那就等于在 options 里手搓一张表。
- **悬空容忍，且会被清掉**。扩展 id 与贡献 id 存文本不存外键：插件升级删掉一个 tab 后，
  旧覆盖行不阻断升级，读取侧自然失效（匹配不到任何生效声明即被忽略）。
  外键会把它变成「升级失败」这种更难查的故障。
  清理发生在**下一次投影**（`SettingsSurfaceFieldSync.pruneDanglingOverrides`，
  挂在 enable / upgrade / 启动 reconcile 的同一入口）：`contributionId` 整条消失、
  或 `item_key` 指名的字段不再声明，对应覆盖行都被 `DeleteOverride` 删掉，
  并留一条 `slog.Info`（`rows` 计数）。这也是迁移注释那句「悬空覆盖行…
  下次…被清掉」的实现；注意**清理时机是投影，不是治理保存**。

不建 `sforum_core_v1` 视图、不建 HostQuery：这是宿主治理态，不是产品数据；
插件读自己的声明从 manifest 来，读覆盖值没有公开面。这条与
`decisions/2026-10-18-extension-data-surface-policy.md` 一致——治理表不属于
「要给插件读的数据面」。

**语义是 sparse overlay，不是投影**：无行 = 用 manifest 默认（可见、顺序取声明值）；
`enabled=false` = 站点关掉；删行 = 恢复默认。因此这张表**永远可以安全清空**，
也可以随时重建，不存在「必须先投影一次」的初始化步骤。

Goose 迁移是纯 `CREATE TABLE IF NOT EXISTS` + 一个 `CREATE INDEX IF NOT EXISTS`，Down 侧同样
`IF EXISTS`，因此可重放。Up 段声明 `-- +sforum OnlineSafe`：按
`decisions/2026-08-02-declared-online-core-migrations.md`，这条声明是「扩容后的 schema 与数据语义
让源二进制与目标二进制都保持可用」的可审查承诺（纯新增表，旧二进制不读它），
并且迁移保持事务性、带 `lock_timeout` / `statement_timeout` —— 超时失败只会让候选启动失败、
旧槽继续服务，而不是把站点卡在半迁移状态。

## 决定 4 — 同步「禁用不删值」+ 差集清扫

插件 enable / upgrade 时把 `forum.settings.profile.tabs` 里声明的 `fields` 投影进
entity-meta（`EnsureOwnerExtensionField`，`entityType=user`，`owner_extension_id=扩展 id`）；
disable / uninstall 只调 `SetEnabledByOwnerExtension(id, false)` 把该扩展的字段整体翻成
`enabled=false`，**不删定义、不删值**。

为什么不删：

- 值属于用户。运营停用一个插件，用户此前填的东西不该被宿主单方面销毁；
  重装回来必须是原样，否则「先停一下看看」变成不可逆操作。
- 删除会让下次 enable 的同步重建一行空字段，期间已存值成为孤儿（`entity_meta_values`
  指向一个不存在的定义），比留着更难清理。
- 管理端本来就没有「手动删插件字段」的入口：`UpdateDefinition` / `DeleteDefinition`
  对 `owner_extension_id` 非空的行直接返回 `ErrPermission`（403）。插件字段的唯一真相是
  manifest + 同步，运营手改会在下一次 sync 被抹掉，所以前端把这类行渲染成只读。
  留一条「宿主能删但运营不能删」的暗道是自相矛盾的。

**差集清扫（升级改名/删字段的正确结局）**：每次投影都拿本次声明与该扩展当前拥有的
定义做差集，**未再声明的定义置 `enabled=false`**（`disableUndeclaredFields` →
`SetEnabledByOwnerField`），已存值原样留在 `entity_meta_values`。

为什么必须清扫而不是「留一个 enabled 的孤儿」：旧定义只要还是 `enabled=true` 且
`visibility=public`，历史值就继续在 `GET /entity-meta/{user}/{id}` 匿名可读、并且仍可写——
插件改名于是变成一次**静默的暴露面扩大**。这不是洁癖问题。

重新声明同一个裸键即恢复（同一行、同一批值、重新 `enabled=true`），所以「先删字段、
下个版本再加回来」是可逆的。宿主仍然**不做值迁移**：它不知道新旧裸键是同一个语义字段，
猜一个等于替插件改数据；要保留语义就由插件自己在版本里迁移。

升级失败回滚走的是同一套原语：`syncLifecycleSettingsSurfaceFields` 按目标 active version
的 manifest 投影，回滚到旧版本时旧声明被重新投影（重新启用），新版本独有的键被差集清扫停用。

## 决定 5 — 公开面复用 `GET /profile` 与 `GET /profiles/{username}`，不新增端点

个人设置页读 `GET /profile`（新增可选字段 `settingsSections` + `settingsValues`），
公开资料页读 `GET /profiles/{username}`（既有 `meta` 字段）。

为什么不开 `GET /profile/settings-sections`：

- 设置页一次保存只发一个 `PUT /profile`（核心字段与插件字段同体提交）。读侧再多一个端点，
  就会出现两个「设置页初始状态」来源，第二个来源永远比第一个晚到或早到一格。
- 公开面已经有 `meta`：`visibility=public` 的插件字段本来就是「用户元数据」的一部分，
  运营手建的 public 用户字段也在里面。再开一个插件专属字段面等于把同一个资料页拆成两半，
  主题要渲染两次。
- 未注入 provider 时 `GET /profile` 逐字节等价于改造前（`WithSettingsSections` /
  `WithSettingsMeta` 是链式可选注入），所以这是纯增量，不是「新端点还没接好」。

**代价**：`GET /profile` 的响应体变大，且插件分区读失败会降级成「没有插件 tab」
（只留 warn 日志）。降级是有意的：一个插件字段读不出来，不该让用户连自己的资料都改不了。

## 决定 6 — profile 与 meta 双写非原子，可接受

`PUT /profile` 的顺序是：核心资料字段 →（大头像）→ 插件字段。
没有跨域事务。

为什么接受：

- 两者没有共同不变量：资料行字段与插件字段互不约束，不存在「要么都对要么都不对」的
  一致性要求。
- 反过来的顺序更糟：先写 meta 再写资料，会出现「昵称被拒但插件字段已经存了」——
  把失败留在用户最在意的字段上。
- 失败可收敛：meta 失败返回 422 + `data.fields[fieldKey]`，前端**保留 dirty 状态**，
  页脚显示第一个出错分区与计数，点一下跳到出错 tab。用户重发即可收敛。
- 逐字段写入是刻意的：entity-meta 的错误不带字段名，一次批量写只能得到「有一项不合法」。
  逐字段写换来「每个错误精确贴到 fieldKey」，代价是同一请求内可能部分成功——
  在「静默丢弃用户修改」与「部分成功 + 可重试」之间，后者可收敛。

**代价**：极端情况下用户会看到「资料保存了、插件字段报错」。因此前端在失败时不清空
草稿、不刷新快照，只在成功后才 `resetToSnapshot`。

## 决定 7 — 字段键 `x.<扩展段>.<裸键>`，扩展段无条件带 8 位哈希

组合键由 `SettingsSurfaceFieldKey` 唯一产生：`x.` + 扩展段 + `.` + 小写裸键。

扩展段 = 可读前缀（≤11 字符，`[a-z0-9_-]`，不足 8 补 `_`）+ `-` + 8 位 FNV-1a 十六进制，
总长 ≤20。于是 `2 + 20 + 1 + 41 = 64`，正好是 entity-meta `fieldKeyPattern` 的长度上限。

哈希是**无条件**的，不只用于碰撞兜底：`sforum.smtp` 与 `sforum-smtp` 压平后完全同形，
可读前缀无法区分它们；身份必须由哈希保证，否则两个扩展的字段会落进同一行
（表现为「一处保存、另一处也变」）。因此**插件作者不要自己拼这个键**——
它由宿主产生、由宿主下发，插件只读 `fieldKey`、原样回写。

同一个函数还被治理写端点复用（`SettingsSurfaceBareFieldKey`）：治理表的 `item_key` 存裸键，
HTTP 入参两种都收（组合键与裸键），换算只走这一处。在别处再拼一次就是漂移的起点。

## 决定 8 — 上限存在，且校验严格（fail closed）

- 每 tab ≤24 字段、每扩展 ≤60 字段、每表面 ≤12 个插件 tab：manifest 校验直接拒绝。
- 渲染侧还有一次 ≤12 的截断（按生效顺序，跨所有插件），并打一条 `slog.Warn`——
  治理读模型**不截断**，只用 `suppressedByCap` 标记，否则运营会看到「这条我关了也看不到变化」
  却不知道为什么。
- 插件字段的 `constraints` 严格解析（未知键、类型错配、`enum` 与 `options` 不一致都拒绝）。
  这与 entity-meta 既有字段的宽松历史行为**分开**：运营手建字段「解析不了就当没有约束」，
  而插件声明是本次新开的入口，一条写错的约束会变成「看起来有上限、实际什么都不校验」。
- 声明上限的理由不是洁癖：一次渲染的成本由 manifest 决定，宿主不能在读到第 500 个字段时
  才发现要为设置页渲染 500 个控件。

## 决定 9 — 裁决顺序：治理覆盖先于权限过滤

Provider 的管线固定为：

```
插件启用态 + enabledBySetting 闸门（EffectiveContributions 已做）
→ 治理覆盖（tab/字段的 enabled、sort_order）
→ 权限过滤（服务端；权限键永不下发）
→ 排序（覆盖顺序优先，否则声明顺序）
→ 超过单表面上限时截断并告警
```

顺序是判决：运营关掉的东西，不因为某个访客恰好有权限而复活。反过来（先权限后治理）
在功能上「看起来一样」，但一旦有人问「为什么我有权限还是看不到」，两种顺序给出的
答案完全不同——我们要的是运营的否决不可被权限绕过。

**写入策略复用同一份投影**（`SettingsSurfaceFieldPolicy`：字段键是否出现在该访客当前的
`settingsSections` 里）。因此「治理关闭」同时阻止写入，而不是只隐藏：`PUT /profile` 的
meta 键与 `/entity-meta` 写路径共用这一条策略，不存在「表单藏起来了但接口还能写」的缺口。
策略读不出来时 fail closed（不显示、不写入），而不是放行。

## 决定 10 — 投影只有一个入口：挂在状态变更原语上

投影（`SyncManifest` / `Disable`）不挂在管理面 handler 上，也不挂在 `LifecycleService` 的
enable/upgrade 包装上，而是挂在**真正改 status 的原语**上：

- `PostgresStore.Enable`：在同一事务里先读 active version 的 manifest、投影，再 `Commit`；
  投影失败直接返回 `ErrPreflightFailed`，`defer tx.Rollback` 撤销 status 变更。
- `PostgresStore.syncLifecycleSettingsSurfaceFields`（生命周期状态发布事务内）：
  目标不是 `enabled` → 只去投影；目标是 `enabled` → 按目标 active version 的 manifest 投影，
  失败即整笔状态发布回滚。
- `PostgresStore.Disable`：提交后去投影（只翻 `enabled=false`），失败只告警——
  停用必须永远可用。
- 启动 reconcile（`bootstrap.projectEnabledSettingsSurfaces`）：对所有已启用插件幂等重投影，
  收敛任何历史残留。

**活环境联调暴露的坑**：投影最初挂在 service 层的 enable/upgrade 包装上，凡是**不经过那两个
包装**就改状态的路都绕过了 preflight——dev 零命令自动启用是第一个被打到的；`PostgresStore`
的注释把 SystemTier 恢复与 legacy 发布也列进同一个漏斗（带外恢复 CLI 是例外，它根本不加载
应用代码，见文末缺口）。症状极具误导性：**表单能正常渲染**
（读模型读 manifest，字段当然在），**保存却必 422**（entity-meta 里没有定义 →
`entity_meta.not_found` → `profile.settings.field_unknown`）。

怎么发现：不要只盯着前端报错。查库比接口快——

```sql
SELECT field_key, enabled, owner_extension_id
FROM entity_field_definitions
WHERE owner_extension_id = '<extension-id>';
-- 0 行 = 投影没跑（或没跑成功）；有行但 enabled=false = 差集清扫/停用态
```

接口侧只能看到「保存 422」，看不到「定义不存在」，两者组合才是判断依据。

**代价**：投影走独立连接（entity-meta 有自己的事务边界），极端情况下可能「字段已落库、
status 回滚」的残留；这些行 `enabled=true` 但没有插件引用，读模型不可见，
下一次 enable 或启动 reconcile 收敛。这是刻意不引入跨库分布式事务的结果。

## 决定 11 — `settingsValues` 恒在下发（有 sections 就下发空对象）

`PublicProfile.settingsValues` 是 `*map[string]any` + `omitempty`。若按普通 map 处理，
「有 tab、一个值都没存」会得到一个 nil map → 键整键消失，于是前端无法区分：

- 「这个插件没有可编辑字段」（不该渲染输入框）与
- 「有字段、还没填」（该渲染空的输入框）

两者在 UI 上的正确行为相反，而 `undefined` 与 `{}` 只差一个键，但足以让前端写错分支。
因此有 `settingsSections` 时**恒下发**：先放空对象，再按实际值覆盖
（`attachSelfSettings`：`empty := map[string]any{}` → `view.SettingsValues = &empty` →
过滤后非空才替换）。没有 sections 时仍然整键省略（此时确实没有插件字段）。

测试钉住：`TestAttachSelfSettingsAlwaysSendsSettingsValues`
（`apps/api/app/Models/Profile/profile_settings_sections_test.go`）——
既有「空则 `{}`」也有「有值则带值」两段断言。

## 决定 12 — 插件设置分区属于扩展管理员面（不动 RBAC）

裁决：站点设置页里的插件设置 tab **不是**「站点设置的一部分」，它是**扩展管理面**在站点设置页上的
一个入口。因此权限按扩展管理面走，按 B 方案处理——不改 RBAC、不给内置角色补权限：

- 看：`GET /admin/settings/surface-tabs` 的守卫是 **admin.access 且 extension.view**
  （`app/Http/core_guard_authorizer.go` 的 `core.route.extensions.surface_tabs`）。
- 存：`PUT /admin/extensions/{id}/settings` 走 `requireExtensionSettingsAuthority`
  （`app/Http/core_guard_extensions.go`）：插件类型要求 **extension.plugin.manage**
  （或上位 `extension.manage`；声明了邮件 provider 的再加 `settings.mail.manage`）。

`RoleOperator`（站点运营）的内置授权里**没有任何 extension._ 键**（`app/Models/Identity/seeds.go`：
admin.access + 用户/内容/站点/邮件/外观/SEO/分类/标签/附件/entity-meta/身份 provider/徽章），
扩展读取与插件管理都在 `RoleTechAdmin`。所以运营看不到这块**是正确结果**，不是漏配。

**为什么否掉「给 RoleOperator 补 extension.view」**：补了会造出一个
「看得见、点保存必 403」的 tab——`surface-tabs` 已经把该插件的完整设置文档内联下发
（含非 secret 项），看得见就等于拿得到读面；而保存权限不在他手上，于是每个分区都是一个
必然失败的按钮。要一致就得连 `extension.plugin.manage` 一起给，那是下一步。
另外 `surface-tabs` 不只是一个 tab 列表：它透出插件设置文档，把扩展读面整体交给运营等于扩权。

**为什么否掉「给 RoleOperator 补 extension.plugin.manage」**：这个键管的是装机/升级/信任/
生命周期与运行时发布——把装卸插件的权力交给运营是明显越界，也违背「技术发布留给技术管理」的
既有角色划分。运营的职责边界是站点内容与配置，不是代码分发。

**正解（属于另一刀，本文只登记）**：要真正拆开，需要独立的
`extension.settings.manage`（必要时再加 `extension.settings.view`），语义是「配置某个插件的设置」，
与「装卸插件」解耦；落点应包括 seeds、权限目录文案、OpenAPI、以及 surface-tabs 与
`PUT /admin/extensions/{id}/settings` 两侧的守卫。在它落地之前维持现状。

**但有一个不可退让的产品要求**：运营不能「以为这里没有」。缺权限时必须是
**解释性提示**（「插件设置需要扩展管理权限」），而不是一片空白或一个静默消失的分区——
静默消失会被读成「本站没有插件设置」，运维据此排查会走错方向。

**落地状态（已落地，task-13）**：`/admin/settings/surface-tabs` 的加载改成
loading / error / empty / ready 三态（不再 `catch → []`），403 时在 tab 栏上方给**非阻塞行内提示**
——`apps/web/app/pages/admin/settings/index.vue` 的 `data-testid="admin-settings-surface-tabs-hint"` /
`role="status"`，文案点名「需要「查看扩展」权限（extension.view）」，附 `<details>` 展开原始 message
与重试按钮；状态判定的纯函数在 `apps/web/app/utils/admin/adminSettingsSurfaceTabs.ts`；
单测 `apps/web/tests/admin/settingsSurfaceTabs.test.ts` 覆盖 403/网络 → 0 个插件 tab 且出提示、
ready+0 → 无提示、ready+1 → 正常、以及在两种 locale 下文案都点名 `extension.view`。

## 如实的缺口：带外恢复命令不做去投影

`sforum extension disable` / `disable-all` / `quarantine`（`apps/api/cmd/sforum/recovery.go`）
是**应急处置通道**：它走独立 `pgx` 连接与自带 SQL，**不加载应用代码、不装配 Provider**，
因此**不会**调用 `SettingsSurfaceFieldSync.Disable`，也不做任何设置面去投影。
被它停用的插件，`owner_extension_id` 指向它的 entity-meta 字段会保持 `enabled=true`。
启动 reconcile（`projectEnabledSettingsSurfaces`）**收敛不了这个窗口**：它只遍历
`status = enabled` 的插件并重投影声明，从不遍历已停用的插件去投影。
真正把它翻掉的是下一次**正常的管理端停用**（`PostgresStore.Disable` → `Disable`）。
注意 V2 插件的停用动作要求当前 `status=enabled`（`disableLifecycleV2` 的前置检查），
所以实操路径是「启用 → 停用」；只启用也能立刻收敛（`Enable` 的投影会把未声明键差集清扫掉）。

这是设计取舍而不是疏漏：恢复通道必须在「应用代码起不来」的场景下可用，
任何依赖 Provider 装配的动作都会把这条通道拖回同一个失败面。代价是恢复后存在一段
「字段仍启用、但插件已停用」的窗口，此时公开面可能继续读到 `visibility=public` 的历史值。
运营侧的正确收尾是：恢复通道把站点救起来之后，走一次正常的「启用 → 停用」（启用会重新投影并清扫，
停用会整体去投影；V2 插件的停用要求当前是 enabled，所以不能直接再停一次）。

## 迁移上线约束（运维实测）

`202611130001_settings_surface_items.sql` 里 `updated_by_user_id` 引用 `users(id)`，
因此 `CREATE TABLE` 会对 `users` 取 `ShareRowExclusiveLock`。上线时：

- 执行该迁移必须保证不存在长于 **5s** 的、对 `users` 的写事务，否则 DDL 会排在它们后面
  并与后续读写互锁（迁移文件已 `SET LOCAL lock_timeout = '5s'`，超时会明确失败而不是挂住）。
- `SET LOCAL` 只在事务块内生效。goose 默认把每个迁移包在事务里，所以迁移器路径下有效；
  **手工 `psql -f` 必须显式 `BEGIN; ... COMMIT;`**，否则两条 `SET LOCAL` 只作用于各自的
  隐式单语句事务，`lock_timeout` 实际没生效。

## 残留与开放问题

- **未提交**：本刀全部改动仍在工作树。
- `admin.settings.tabs` 的治理覆盖只影响「tab 是否出现 / 排第几」，不涉及插件设置文档内容的
  字段级治理（那份文档的字段由插件 schema 与 settings 存储拥有）。是否需要字段级治理，
  等有运营诉求再谈。
- 治理写端点的 `extensionId` 允许缺省：`contributionId` 在生效声明里唯一时由服务层解析，
  同时出现在多个扩展上则报 `validation.invalid`。这是刻意的（猜一个扩展去写覆盖比报错危险），
  但读模型只能靠 `point + id` 组合定位，前端要自己带上扩展 id。
- **带外恢复命令的窗口**（见「如实的缺口」）：`sforum extension disable` 之后，字段仍是
  `enabled=true`，直到下一次正常停用。给运营的 runbook 必须写「救起来之后再走一次管理端停用」。
- 迁移注释里那句「悬空覆盖行…下次治理保存时被清掉」现在**承诺兑现但时点不同**：
  清理发生在投影（enable / upgrade / 启动 reconcile），不是治理保存。建议把注释改成
  「下次投影时被清掉」，免得读注释的人去治理页等一次自动清理。
- **治理界面目前只暴露整条贡献（tab）级**的开关与排序。字段级的 `…/items/{itemKey}` 端点、
  `ContributionView.fields[]` 读模型都已具备，但 `contributions.vue` 没有渲染字段行——
  字段级治理今天只有 API 入口。要么补界面，要么在文档里如实说「页面上没有」，
  不要向运营承诺一个屏幕上不存在的开关。
- `GET /admin/extensions/contributions` 的 `suppressedByCap` 是**提示位**：位次按生效顺序算，
  而权限/字段过滤仍可能让实际渲染顺序与此略有出入。
