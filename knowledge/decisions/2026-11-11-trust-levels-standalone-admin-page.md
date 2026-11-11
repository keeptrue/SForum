# 2026-11-11 信任等级：后台提为独立页（论坛管理 → 信任等级），三 tab 整页一次保存

Status: **accepted**（2026-11-11 用户批准，实施中）
前置：`2026-11-10-trust-levels.md`（D1–D32）、`2026-11-11-trust-level-earnable.md`（D33）、
`2026-11-11-trust-level-baseline-narrowing.md`（D34）。本文件**追加**决策，不改写既有条目。

## 本决策显式取代的两条旧结论

| 位置 | 被取代的原话 | 取代者 |
| --- | --- | --- |
| `../modules/trust-levels.md`「已作废结论」D 组 #18（修订后位于该文件 `537` 行起的条目；按 `18. ~~后台新开独立页` 检索） | 「后台新开独立页 —— 否。需要第二套页头/工具栏，还改 V3 目录计数，收益为零；它是设置页内的固定 tab。」 | 本文件 D-A |
| `../reports/2026-11-10-trust-level-visual-spec.md` §0 第 23 行 | 「阈值页是**既有 tab 数组里的一项**，不是新页面」 | 本文件 D-A |

两条旧结论的**共同前提**是「信任等级只有阈值表单一个可编辑面」。D33 之后这个前提消失：
同一份配置现在有**三种读法**（这是什么 / 门槛多少 / 解锁什么），且其中「门槛」与「解锁」
共用同一个 JSON 键。前提变了，结论跟着变，不是「当初判断错了」。

旧结论里唯一仍然成立的一半是**成本**：新增后台页确实要动 V3 目录计数
（`adminSurfaceCatalog` 的 placements 记数 +1、新增一条 page 级 UI identity、`adminPagePathsById`
一行；具体数字以生成器 `--check` 输出为准，本文件不预先断言）。本次裁决认为收益（三区分区 + 诚实的读门控，见 D-B）大于这笔成本，而不是成本消失了。

## 问题

信任等级的后台配置面长在 `/control-panel/settings?tab=trustLevels`
（`apps/web/app/pages/admin/settings/index.vue:25` 的 `SettingsTab` 联合类型、`:32` `validTabs`、
`:40` tab 列表、`:51` tab→组件映射）。三件事同时不对劲：

1. **信息架构错位。** 同一页的其余 tab 是站点级开关（基本/注册/新人限制/维护/客户端/更新），
   信任等级是**论坛运营策略**，与「新人限制」是同一件事的两半（TL0 由新人限制决定，TL1–TL4
   由门槛决定）。把两半拆在两个一级入口下，运营每次调等级都要跨页。
2. **读面在说谎（这是本次真正的驱动因素）。** 见 D-B：没有 `trust.manage` 的账号读到的是
   **推荐默认值**，而前端把它当「当前配置」渲染。
3. **表单没有解释面。** 五档 × 六指标 + 白名单勾选直接铺开，没有一处说明「TL0 不在这里配」
   「等级只叠加不剥夺」「当前是推荐默认还是自定义」。这些只能靠文案补，而 tab 里没有位置。

## 决定 D-A：独立页 `/control-panel/forum/trust-levels`，页内三 tab

- 位置：`apps/web/app/config/adminModules.ts` 新增
  `{ id: '/forum/trust-levels', labelKey: 'admin.nav.trustLevels', icon: 'i-lucide-gauge',
  componentName: 'AdminForumTrustLevels', requiredPermissions: ['trust.manage'] }`，
  侧栏「论坛管理」children 顺序 moderation → categories → tags → settings → **trust-levels** → content。
- 页内三 tab（`SFAdminFixedTabNav`，query 同步 `?tab=`）：`overview` 等级总览（新写；组件 `SFAdminTrustLevelsOverviewTab.vue`，缺省 tab 为 overview）、
  `thresholds`（zh 标签「阈值」，原 5 行 fieldset 几何不变）、`earnable`（zh 标签「能力解锁」，
  原矩阵整块）。标签名以 `apps/web/i18n/locales/*.json` 的
  `admin.forum.trustLevels.tabs.*` 为唯一来源，本文件不复制文案。
- 页头几何逐字复用 `pages/admin/settings/index.vue` 与 `pages/admin/forum/settings.vue`：
  `h2.text-xl` + 图标、`UDashboardToolbar`、`SFAdminFixedTabNav`、`min-w-0` 单活动面板、
  页级吸底 footer。**不自创第二套页头/tab 系统**——这正是旧结论担心的成本，本次按
  AGENTS.md 的固定 tab 契约承担，而不是绕开它。
- 旧链接兼容：`/control-panel/settings?tab=trustLevels` 在设置页初始化前 `replace` 到新页；
  设置页的 `trustLevels` tab 四处登记**全部删除**（联合类型 / validTabs / tab 列表 / 组件映射），
  不留 re-export、不留双入口。

**被否方案**：

| 方案 | 否决理由 |
| --- | --- |
| 维持设置页内 tab（现状） | 三分区放不下；读面谎无法在该页修（D-B 的根因是 tab 与权限不匹配，而设置页门控是 `settings.site.manage`）；新人限制与等级门槛仍然跨一级入口 |
| 独立页但不分 tab（单页长滚动 + 锚点） | 与固定 tab 契约冲突（单活动面板 + `SFAdminFixedTabNav`）；overview 的说明文字与表单混排会把「读」变成「必须滚过表单」 |
| 独立页放在 `/control-panel/settings/trust-levels` | 仍是设置页的子路由，IA 问题原样保留；且 `settings` 页是固定 tab 页，子路由会与 tab 状态互相争夺 query |
| 保留 tab，另加一个「说明」折叠块 | 说明块救不了读门控；且折叠块在移动端会把表单推到首屏之外 |

## 决定 D-B：页面门控用 `trust.manage`（读与写同权）

证据链（2026-11-11 实测，行号为当日工作区）：

1. `trust.levels` 的 option 定义声明 `managePermission: trust.manage`：
   `apps/api/app/Models/Options/community_policy_options.go:82`
   —— `{name: NameTrustLevels, public: true, managePermission: identity.PermissionTrustManage}`。
2. 后台读面按 `managePermission` **逐条过滤**：`GET /admin/web-options`
   （`apps/api/app/Http/Controllers/Options/routes.go:9` → `controller.go:60` →
   `Options/service.go:333`）→ `adminOptions`（`service.go:710`）→
   `service.go:717` `if !actor.Can(definition.managePermission) { continue }`。
   于是**没有 `trust.manage` 的账号拿到的载荷里根本没有 `trust.levels` 这个键**。
3. 前端把「键缺失」当「用推荐默认」：`useTrustLevelsForm.ts:366` `readTrustLevelsConfig`，
   `:368` `if (!option) return recommendedTrustLevelsForm()`。
4. 旧入口不拦这类账号：`apps/web/app/middleware/admin.ts` 只校验 `admin.access`，
   `useAdminPage`（`composables/admin/useAdminPage.ts`）不校验 `requiredPermissions`；
   设置页的 tab 是**静态数组**（旧形态，四处登记在 `pages/admin/settings/index.vue` 的
   `:25` 联合类型 / `:32` `validTabs` / `:40` tab 列表 / `:51` 组件映射；**这四行已于同日删除**，
   现在 grep 不到是预期的，别当成本文件写错），所以 `admin.access` 持有者直连
   `?tab=trustLevels` 就能看到那张表。

结论：旧形态下，无 `trust.manage` 的账号看到的是**推荐默认值冒充当前配置**——不是「少看了
一部分」，是**看了假的一份**。门控改成 `trust.manage` 之后，这类账号看到的是无权限态
（页面五态之一），这比一份假的现值诚实。

**副作用（属修正，不是回归）**：`moderator` 与 `tech_admin` 原先可以（仅经直连 URL）看到
该 tab，现在看不到。权限现状：`settings.site.manage` 只授给 operator
（`apps/web/app/config/roleTemplates.ts:50`）、`trust.manage` 同样只在 operator 模板里
（`:69`）；`moderator`（`:22`）与 `tech_admin`（`:73`）两个模板都没有这两个键。也就是说
他们原本连侧栏「设置」入口都没有，只有手敲 URL 这一条路——那条路上给的是假数据。

**被否方案**：

| 方案 | 否决理由 |
| --- | --- |
| 沿用 `admin.access` 门控 | 等于保留「假现值」这个缺陷；且让页面成为唯一一个读得到、写不了、显示值还是错的后台页 |
| 新增 `trust.view` 只读权限 | D9 已裁决不做第五个权限位（`modules/trust-levels.md`「Feature Flag And Permissions」）；且本页没有「只读但可操作」的形态——overview 之外两个 tab 都是表单 |
| 页面门控用 `forum.settings.manage` | 与写权限 `trust.manage` 不一致，会出现「能进页、保存 403」的分裂；`trust.manage` 本来就是为了这个域单独建的键 |

## 决定 D-C：整页一次保存（三 tab 共用一个保存按钮）

理由不是审美，是**存储形态**：阈值、分档限额、能力解锁全在**单个 JSON option 键
`trust.levels`** 里（`apps/api/app/Models/Options/community_policy_options.go:82` 的
`NameTrustLevels`；解码 `TrustLevels/config.go:60 DecodeConfig`，`:66` `DisallowUnknownFields`）。
一次保存写的是**整个键**，不是字段增量。

提交形状把这件事钉死了：`useTrustLevelsForm.ts:407-429` 的 `trustLevelsPayload` 只产出一条
`{ name: 'trust.levels', value: <整个 JSON> }`——**一次保存 = 整键覆盖**，没有字段增量。

因此「每个 tab 各自一个保存按钮」等于**三个写者对同一个键做全量覆盖**：
在 thresholds 保存会带上表单里的 `limits` 与 `earnable`（`useTrustLevelsForm.ts:80` 明确要求
`limits` 原样带回，`:115-121` 的 `cloneTrustLevelsForm` 把三块一起快照），只要任何一个 tab 的
表单状态不是最新的一份，另两个分区就会被静默回滚。这不是理论风险——`limits` 本轮没有编辑器，
全靠「原样带回」，一旦某个分区只提交自己那一半，`limits` 就会被打回默认。

落地形态：页级**唯一**吸底 footer（`SFAdminFormFooter`），左侧 slot 放 `saveScope` 文案
（「保存会一并提交门槛与能力解锁的全部改动」），脏状态跨 tab 保留；`恢复推荐默认`
只重置 `requirements`，按钮留在 thresholds 分区（沿用 D12 的常驻 `UAlert` 披露）。

**被否方案**：

| 方案 | 否决理由 |
| --- | --- |
| 每 tab 独立保存 | 上述全量覆盖；要修就得后端支持字段级 patch，而 `trust.levels` 是单键 JSON，patch 语义要么新增 API 要么在服务端做读-改-写（并发下仍有覆盖窗口） |
| 每 tab 独立保存 + 保存前先 GET 合并 | 多一次请求且引入「保存时读到的是别人刚改的值」的隐式合并；比一次保存更难解释，收益只是「少点一次」 |
| 自动保存（change 即写） | 六指标 × 四档的输入过程中会产生大量中间态（例如把 60 清空再输 80），每次都是全键覆盖；且与「恢复推荐默认」按钮语义冲突 |

## 决定 D-D：分档限额编辑器本轮不做，只留一行披露

`limits` 四个字段是 `*int`（`TrustLevels/types.go:157-162`：`topicCooldownSeconds` /
`commentCooldownSeconds` / `dailyTopicLimit` / `dailyCommentLimit`），语义是
**`null` = 继承站点默认，`0` = 免冷却/不限**（`useTrustLevelsForm.ts:30`，D27）。
这不是普通数字输入：

- 三态（继承 / 显式 0 / 显式 N）需要一种 UI 表达，而现有 `SFAdminFormFooter` 与
  `blockNonIntegerKey` 只处理「有值」与「空」两态；
- 生效判据在 `Forum/service_trust_limits.go` 的 `limitsForActor` / `resolveLimit`，
  改错一次是**静默策略变更**（把 null 存成 0 就是全站免冷却）；
- 本轮没有验证过「四档 × 四字段」的浏览器证据，写进页面等于给一个没人验证过的编辑器发版。

所以本轮只在 overview 留一行小字披露「分档冷却/每日上限当前不在此页编辑」，`limits`
在保存时**原样带回**（`useTrustLevelsForm.ts:80` 的注释即这条约束）。这不是遗漏，
是显式边界。

**回退条件**：若运营反馈必须在本页配限额（真实需求，不是「顺便」），单开一刀：
先定三态控件与 `null` 往返测试，再动页面。

## 实施期教训（2026-11-11 落地，三个真实缺陷）

落地的这一版先过了一轮验收，抓到三个**单测与静态门禁都看不见**的缺陷。它们的共同形状是
「契约在源码里成立、在真实运行时里不成立」，所以留在这里当判据，而不是当事故记录。

### L1（P1，最贵）：SSR-first 页面禁止用「副作用布尔」表达「已加载」

原实现 `const hasLoaded = ref(false)`，靠 `useAsyncData` 的 handler 里置 `true`。硬加载
（新标签直开 / F5 / 从 `?tab=trustLevels` 重定向）会**复用 SSR payload 水合**，handler 不重跑
→ `hasLoaded` 恒 `false` → `pageState` 永久卡在 `loading`，整页只显示骨架。
**单测全绿**，因为挂载测试走的是 handler 会执行的路径。

正确形态是**从数据派生**：`computed(() => items.value !== null && items.value !== undefined)`。
两个细节别省：

- 不能退化成「有数据就算 ready」——`useWebOptions` 自带默认值，那样会把首帧判成 `ready`
  并闪一下推荐默认值，等于把 L1 换成一个更隐蔽的谎。
- `pageState` 的判定顺序即语义：`forbidden → failed → loading → feature-off → ready`，
  且 failed 只看 `error`（原实现多一个 `&& !hasLoaded`，正好把硬加载的失败态也吞进 loading）。
  编排层依赖 Nuxt 自动导入、在 `bun test` 里挂不起来，所以测试用**源码分支顺序**钉语义
  （见 `../reports/2026-11-11-trust-levels-admin-page-spec.md` §14.7）。

**可复用判据**：任何 SSR-first 页面的「已加载」都必须能被**水合路径**证明；断言写成
「源码里不存在 `const hasLoaded = ref(`」，而不是断言「加载完成后是 ready」。

### L2（P2）：`type="submit"` 与 `@click="save"` 并存 = 两次 PUT

两者同在一个 `@submit.prevent` 的表单里，点一次按钮触发两条提交路径 → 两次 `PUT`、两条 toast。
**happy-dom 不实现隐式表单提交**，所以挂载测试只看到一次调用，抓不到。只有浏览器网络面板能坐实。
修法是去掉 `@click`，只留 `type="submit"` + 表单 `@submit.prevent`。

**可复用判据**：**测试环境的 DOM 保真度就是断言能力上限**。凡是「浏览器原生行为」参与的逻辑
（隐式提交、原生校验、焦点顺序、滚动），单测绿不能当证据。

### L3（P3）：重复请求与「命令被挤出视口」

- 白名单请求被 `onMounted` 与 `watch(items)` 各触发一次 → 同轮去重只短路
  `status === 'loading'`（**不能**写成 `status !== 'idle'`，那会挡掉保存后的刷新与失败重试）。
- 390 宽下 `UDashboardToolbar` 左槽不收缩，内层出现横向滚动、刷新按钮被推到视口外
  （AGENTS.md：命令可折叠但必须可达）→ 左右槽 `min-w-0 flex-1` / `shrink-0`，刷新按钮补
  `aria-label` / `title`。

**可复用判据**：工具栏的「不溢出」不是 `body` 无横向滚动，而是**内层槽位**的
`scrollWidth == clientWidth` 且每个命令的 `right ≤ viewport`。

### 与冻结稿的两处增量（不是漂移）

落地时新增了冻结稿没有的两项，已在 `../sessions/2026-11-11-trust-levels-admin-page.md` 登记：

1. i18n 键 `admin.forum.trustLevels.page.loadFailedReason`（zh「失败原因：{reason}」/
   en `Reason: {reason}`）：`failed` 态展示后端 reason，与矩阵失败态口径一致。
2. 测试钩子 `data-sf-trust-levels-reset` 与 `data-sf-trust-levels-footer`
   （冻结稿 §12 原表只有 `-save`）。

冻结稿 `../reports/2026-11-11-trust-levels-admin-page-spec.md` 的 §12 表与 i18n 清单**尚未回填**
这两项（该文件归 designer），未回填前以本决策与 handoff 的记录为准。

## 回退条件（整体）

1. **门控过严**：若出现真实「只读需求」岗位（需要看当前门槛但不应改），回退到
   `trust.manage` ∪ 新只读键——但那需要新决策推翻 D9；**不要**用 `admin.access` 直接放开
   （那是回到 D-B 的假现值）。中间态可先只开放 overview 分区。
2. **移动端不可用**：若 390×844 下 tab 条换行 + 表单溢出无法在固定 tab 契约内修好，
   回退为单页 + 锚点导航（放弃 D-A 的 tab 形态，保留独立页与门控）。
3. **保存失败半径变大**：若整页保存被证明会把「一处输入错误」放大成「另两个分区也存不进去」
   且无法用字段级 422 定位，回退为按 tab 分保存 + 服务端读-改-写（需后端支持，另开决策）。
4. **回退成本**：目录身份 `id` 与 `contractVersion` 不变（见 `../modules/trust-levels.md`
   的 source 迁移规则），回退只需改 `source`、路由与导航项；旧链接重定向建议保留双向。

## 证据（2026-11-11 收尾）

- **浏览器**：reviewer 实测（`../reports/2026-11-11-trust-levels-admin-page-spec.md`
  §14.6 / §14.7；截图在 `storage/qa/trust-levels/`，该目录被 `.gitignore` 忽略），Lead 复核。
  硬加载三条路径（直接访问 / F5 / `?tab=trustLevels`）均 `pageState=ready` 且
  `GET /api/v1/admin/trust-levels` 恰 1 次；3 tab × 5 轮 = 15/15 稳定；390 刷新按钮
  `right 357 ≤ 390` 且工具栏无内层滚动；门槛 6 列×5 行（桌面）/ 2 列末项满宽（390）；
  吸底 footer gap 42 / 34px。**本文件作者未重跑浏览器**，只重跑了下面两条静态门禁与聚焦单测。
- **本文件作者实测（2026-11-11 16:50 前后）**：`node scripts/v3-catalog/generate.mjs --check`
  → `V3 P0 catalogs are current (18 files).`；`node tests/validate-admin-framework.ts` →
  passed；`node tests/validate-architecture-boundaries.mjs` → passed（2092 files）；
  `cd apps/web && bun test tests/admin` → 735 pass / 0 fail。全量 `bun test`（2112 pass / 0 fail /
  236 files）与 `bun run typecheck` exit 0 由 reviewer/Lead 提供，本文件作者未重跑。
- 新页在生成产物里是两条**不同 id 空间**的登记：`core.admin.page.forum_trust_levels`
  （Admin Surface）与 `core.component.page.admin.forum.trust_levels`（UI identity）。
- 页文件实测 315 行（`wc -l apps/web/app/pages/admin/forum/trust-levels.vue`）。

## 仍未验证（本文件不声称）

1. **无 `trust.manage` 账号的 `forbidden` 态没有浏览器证据**：dev 库只有 `zhuchunshu`
   （`super_admin`，持有 `trust.manage`）与 20+ `seed_*`（`member`，随机密码且无
   `admin.access`），**未取得符合条件的账号**。目前只有单测覆盖（`trustLevels.test.ts` 的
   `pageState` 判定式、`trustLevelsOverview.test.ts` 的 shell `forbidden` 分支）。
2. **关闭态警告没有 `role="alert"` / `aria-live`**：属共享 `UAlert` 的既有行为，本轮未修
   （矩阵加载态有 `aria-live="polite"`，不影响本次验收）。
