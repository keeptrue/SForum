# 2026-11-13 插件设置面贡献点（settingsSection）

Status: **landed（未提交，工作树改动）**——个人设置页与站点设置页的插件 tab/字段第一次有宿主契约。
模块笔记：`../modules/profile.md`「插件设置面」节、`../modules/extensions.md`「设置面贡献点」节、
`../modules/frontend.md`「插件设置面 tab」节、`../modules/options.md`「Boundaries」；
决策：`../decisions/2026-11-13-settings-surface-contributions.md`；
作者文档：`docs/extensions/authoring-guide.md`「Settings tabs and profile fields (`settingsSection`)」。

## Changed

一句话：插件可以把「一个设置 tab / 一组按用户存的表单字段」声明给宿主，不用自建页面；
声明、存储、权限、运营治理、错误定位全部复用宿主既有机制。

- **契约层**（`apps/api/app/Support/ExtensionManifest/`）：新增 `settings_section_payload.go`
  （两个贡献点 `forum.settings.profile.tabs` / `admin.settings.tabs`、payload 结构、
  字段键换算 `SettingsSurfaceFieldKey` / `SettingsSurfaceBareFieldKey` / `SettingsSurfaceSplitFieldKey`、
  上限常量）与 `settings_section_validate.go`（形态矩阵 `settingsSectionTabKinds`、
  字段/约束/`select`/默认值校验、整包重复裸键检测）。`manifest.go` 只以零行增量把两个
  贡献点组合进目录（它是精确到行的架构基线文件）。
- **投影收尾（差集清扫 + 悬空清理）**：`SettingsSurfaceFieldSync.SyncManifest` 现在三件事一起做
  —— upsert 本次声明、把该扩展仍有但**不再声明**的定义 `SetEnabledByOwnerField(false)`（值保留，
  重新声明即恢复）、用 `pruneDanglingOverrides` 删掉指向已消失 tab / 字段的治理覆盖行
  （`slog.Info` 带 `rows` 计数）。entity-meta 为此新增
  `ListOwnerExtensionDefinitions` / `SetEnabledByOwnerField`。
- **投影单漏斗**：投影从 service 层包装挪到改 status 的原语上——
  `PostgresStore.Enable`（同事务内投影，失败 `extension.preflight_failed` + 回滚）、
  `syncLifecycleSettingsSurfaceFields`（生命周期状态发布事务内按目标 active version 投影）、
  `PostgresStore.Disable`（提交后去投影，失败只告警）、启动 reconcile
  `projectEnabledSettingsSurfaces`。原先的旁路（dev 零命令启用最先被打到；store 注释同列
  SystemTier 恢复、legacy 发布）会留下「已启用但没有字段定义」→ **表单能看、保存必 422**。
  带外恢复 CLI 不走这条漏斗（见 Decisions 第 11 条），这是刻意的。
- **治理表**（`apps/api/database/migrations/202611130001_settings_surface_items.sql`）：
  `settings_surface_items(extension_id, point, contribution_id, item_key, enabled, sort_order,
  updated_by_user_id)`，语义是 **sparse overlay**（无行=manifest 默认、`enabled=false`=运营关掉、
  删行=恢复默认），扩展/贡献 id 存文本不存外键（升级删 tab 不阻断升级，悬空行读取侧自然失效）。
  不建 `sforum_core_v1` 视图、不建 HostQuery。
- **模型层**（`apps/api/app/Models/Extensions/settingssurface/`）：`OverrideKey` / `Override` /
  `Section` / `Field` / `SurfaceTab`（tab id 命名空间 `ext.<extensionId>.<contributionId>`）、
  读模型与治理写契约、`EffectiveOrder` 与 `OverrideIndex`。
- **Provider 层**（`apps/api/app/Providers/settings_surface_*.go`）：
  `contributions`（裁决管线：启用态 → 治理覆盖 → 权限过滤 → 排序 → 单表面 ≤12 截断）、
  `sync`（`fields` 声明投影进 entity-meta，幂等、不碰已存值；`Disable` 只翻 `enabled=false`）、
  `governance`（读模型 `Contributions` / 站点侧 `SurfaceTabs`（内联设置文档）/ `Mutate` / `Reset`）、
  `policy`（读写策略 = 「这个键是否出现在当前投影里」，fail closed）、
  `meta`（值的读写适配，逐字段写、错误翻译成字段级消息键）。
- **entity-meta**：新增 `policy.go`（只作用于 `owner_extension_id` 非空的字段）、
  `EnsureOwnerExtensionField` / `SetEnabledByOwnerExtension`（宿主内部同步路径，不要求 actor），
  `UpdateDefinition` / `DeleteDefinition` 对插件字段返回 `ErrPermission`（管理端只读）。
- **端点**：`GET /admin/extensions/contributions`、`PUT|DELETE /admin/extensions/contributions/:point/:contributionId`、
  同形 `/items/:itemKey`（`extension.plugin.manage` 写、`extension.view` 读）、
  `GET /admin/settings/surface-tabs`；`GET /profile` 增加可选 `settingsSections` + `settingsValues`，
  `PUT /profile` 接受 `meta`；`GET /profiles/{username}` 的 `meta` 只出 `visibility=public`。
- **前端**：`components/settings/SFSettingsTabNav.vue`（tab 呈现与键盘语义唯一所有者）+
  `SFProfileSettingsTabNav.vue`（分区清单 → tab 条目）+ `components/settings/profile/SFProfileExtensionTabPanel.vue`
  （`fields` 用 `SFDeclarativeSettingsField`，`component` 挂 `SFExtensionWidget` 并禁用页脚保存）+
  `composables/settings/useProfileSettingsDraft.ts`（一次 `PUT /profile`，失败保留 dirty）+
  `utils/settings/profileSettings.ts`（合成分区/字段值/错误分区映射）；
  站点设置页 `pages/admin/settings/index.vue` 混排 `surface-tabs`，面板
  `components/admin/settings/contributions/SFAdminSettingsExtensionTab.vue`；
  治理页 `pages/admin/extensions/contributions.vue` + `composables/admin/useAdminContributionGovernance.ts`
  （乐观更新、失败整表回滚、行内错误、成功 Toast）。
- **契约**：`contracts/openapi/{paths/extensions,schemas/extensions,schemas/profile}.yaml` 补
  `settingsSection` / `SettingsSection` / `settingsSections` / `settingsValues` / 治理端点。
- **fixture**：`extensions/fixtures/plugins/sforum-settings-surface-demo`（`fields` / `component` /
  `settings` 三形态各一条，`extensions/fixtures/README.md` 已登记）。
- **文档**：作者指南新增整节（两个贡献点的 payload 示例与字段表、上限、字段键组合规则与
  哈希段、visibility 三档、约束严格性、权限、运营治理、最小可运行目录、生命周期、常见失败），
  贡献点总表补两行。

## Decisions

1. **两个贡献点，不是一个泛化点**：存储位置、权限基准、允许形态三者不可互换，合并会把差异推到运行期。
2. **站点设置页只支持 `settings` 形态**：admin 侧没有可复用的公开 L2 运行时；`fields` 会让站点配置
   出现两份真相（插件设置文档 + 字段表），「恢复默认」无法回答。
3. **治理用独立表**：options 是整块写入（并发覆盖）、有公开读面、会被「恢复默认」波及；
   行级写入 + `updated_by_user_id` + 悬空容忍才够用。悬空行由**下一次投影**清掉，
   不是治理保存时清（迁移注释那句措辞需要改成「下次投影」）。
4. **同步「禁用不删值」+ 差集清扫**：值属于用户，重装回来必须原样；管理端本来也拒绝改插件字段。
   未再声明的定义由差集清扫置 `enabled=false`（值保留、重新声明恢复）——留着 `enabled=true` 的
   孤儿在 `visibility=public` 时会让历史值继续匿名可读、并且仍可写。宿主不做值迁移。
5. **复用 `GET /profile` / `GET /profiles/{username}`**：设置页只有一份初始状态；未注入时响应与改造前逐字节等价。
6. **profile 与 meta 双写非原子可接受**：两者无共同不变量，反序会让失败落在昵称上；逐字段写换来
   精确到 `fieldKey` 的错误，代价是可能部分成功，前端保留 dirty 重发收敛。
7. **裁决顺序治理先于权限**：运营的否决不可被权限绕过；写入策略复用同一份投影，
   因此「治理关闭」同时阻止写入，不是只隐藏。
8. **约束与上限 fail closed**：插件声明是本功能新开的入口，不能沿用 entity-meta 既有字段
   「解析不了就当没有约束」的宽松历史行为。
9. **投影只有一个入口**：挂在改 status 的原语上，失败即阻断状态变更。活环境踩过
   「表单能看、保存必 422」——查 `entity_field_definitions WHERE owner_extension_id = '<id>'` 是否 0 行
   比看接口报错快（接口只会说 `profile.settings.field_unknown`）。
10. **`settingsValues` 恒在下发**（有 sections 时空 map 也发 `{}`）：`omitempty` 整键消失会让前端
    分不清「没有插件字段」与「没填」。测试 `TestAttachSelfSettingsAlwaysSendsSettingsValues`。
11. **插件设置分区 = 扩展管理员面（B 方案，不动 RBAC）**：看要 `admin.access` + `extension.view`
    （`surface-tabs` 守卫），存要 `extension.plugin.manage`（`PUT /admin/extensions/{id}/settings`
    的 `requireExtensionSettingsAuthority`）。`RoleOperator` 内置授权里没有任何 `extension.*`，
    看不到是正确结果；不补 view（会造出「看得见、保存必 403」的 tab，且 surface-tabs 内联完整
    设置文档）、不补 plugin.manage（装卸/信任权力越界），要拆需独立 `extension.settings.manage`，
    属于另一刀。
12. **如实的缺口**：带外恢复命令 `sforum extension disable|disable-all|quarantine`
    （`cmd/sforum/recovery.go`，独立 SQL、不装配 Provider）不做去投影，字段保持 `enabled=true`；
    启动 reconcile 只遍历已启用插件，收敛不了，需恢复后走一次正常的「启用 → 停用」。

## Next

- **提交拆分建议**：契约 + 迁移 + 模型/Provider（api）、契约（contracts）、前端（web）、
  fixture、文档与知识库各自成笔；知识库改动（本文件与四个模块笔记 + 决策 + `index.md`）与代码同批提交，
  避免出现「代码有、记忆没有」。
- **提交前门禁**（本刀是写作任务，未运行）：`./scripts/test.sh --fast`（Go 测试 + Nuxt typecheck + `bun test`）、
  `node tests/validate-architecture-boundaries.mjs`（新增文件多，必跑）、
  `ruby scripts/validate-openapi-refs.rb`（contracts 改过）；
  迁移侧：`apps/api/database/migrations/settings_surface_items_test.go` 与
  `cd apps/api && DATABASE_URL=<一次性集群> go run ./cmd/migrate --check-online-safe`（期望 exit 0）。
- 浏览器证据（本刀未取，需补）：桌面与 390×844 下 `/settings/profile` 出现插件分区并能保存；
  治理页关掉一个 tab 后该分区从 tab 栏消失；关掉一个字段后该输入框消失，
  且直接 `PUT /profile` 提交该 `fieldKey` 返回 422。
- `docs/extensions/catalogs/contribution-points.md` 已由生成器刷新（两行新贡献点），
  别再手改；`--check` 需在提交前跑一次。
- **给 `apps/**` 属主的两条注释纠偏**（本刀写域不含）：
  ① 迁移头注释「悬空覆盖行…下次治理保存时被清掉」的时机写错了，实际清理在投影
  （enable / upgrade / 启动 reconcile），建议改成「下次投影时被清掉」；
  ② `PostgresStore.Disable` 的去投影失败分支注释写「下次启动 reconcile 会收敛」——
  启动 reconcile 只遍历 `status=enabled` 的插件，**收敛不了停用侧的残留**，
  实际收敛路径是「启用 → 停用」（V2 插件的停用前置要求 `status=enabled`，不能直接再停一次）。
- 新增定向测试（提交前应绿）：`TestSettingsSurfaceFieldSyncDisablesUndeclaredFields`、
  `TestSettingsSurfaceFieldSyncPrunesDanglingOverrides`（`app/Providers/settings_surface_sync_test.go`）、
  `TestAttachSelfSettingsAlwaysSendsSettingsValues`（`app/Models/Profile/profile_settings_sections_test.go`）、
  `apps/api/bootstrap/settings_surface_reconcile_integration_test.go`（启动 reconcile，需一次性 PG 集群）。

## Open Questions

- **迁移上线窗口**：`settings_surface_items.updated_by_user_id → users(id)` 使 `CREATE TABLE` 对 `users`
  取 `ShareRowExclusiveLock`。运维实测口径：执行时需保证不存在长于 5s 的对 `users` 写事务；
  且 `SET LOCAL` 只在事务块内生效，**手工 `psql -f` 必须显式 `BEGIN/COMMIT`**（goose 路径自带事务）。
- **站点侧字段级治理**：`admin.settings.tabs` 的治理只覆盖「tab 是否出现 / 排第几」，
  不涉及插件设置文档内部的字段。是否需要字段级治理，等真实运营诉求。
- **治理目标定位**：写端点 `extensionId` 可缺省（`contributionId` 在生效声明里唯一时由服务层解析，
  多扩展同名则 422）；读模型只能靠 `point + id` 组合定位，前端必须自己带扩展 id。
- **`suppressedByCap` 是提示位**：位次按生效顺序算，权限/字段过滤仍可能让实际渲染顺序略有出入。
- ~~运营侧解释文案尚未落地~~ **已解决（task-13）**：`pages/admin/settings/index.vue` 的
  surface-tabs 改为三态，403 时出非阻塞行内提示（`data-testid="admin-settings-surface-tabs-hint"`、
  `role="status"`、点名 `extension.view`、`<details>` 原始 message + 重试），纯函数在
  `app/utils/admin/adminSettingsSurfaceTabs.ts`，单测 `tests/admin/settingsSurfaceTabs.test.ts`。
- **带外恢复窗口的运维收尾**：`sforum extension disable` 之后字段仍 `enabled=true`，
  公开面可能继续读到 `visibility=public` 的历史值，直到下一次正常停用。runbook 必须写这一句。
- **治理界面只有 tab 级开关**：字段级 `…/items/{itemKey}` 端点和 `contributions[].fields[]` 已具备，
  `contributions.vue` 没渲染字段行。补界面或明确不接受字段级治理，别留半截承诺。
