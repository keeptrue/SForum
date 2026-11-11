# 信任等级收尾（M9–M13）— 实施计划

Status: **in progress（2026-11-11 开工）**
前置：`plans/2026-11-10-trust-levels.md`（D1–D32、T1–T19、§13–§17）、
`reports/2026-11-10-trust-levels-acceptance-matrix.md`（**唯一编号源**，124 条）、
`reports/2026-11-10-trust-levels-m8-final-status.md`（ok 75 / 未实现 49 / FAIL 0）。
基线提交：`f63ad2cf9`（代码）+ `55ee9c366`（docs），`main`，工作树干净。

**读这份之前先读父计划 §13 的「已被本文件自己推翻的说法」清单。** 父计划改过 32 条决策、
视觉规范冻了 4 次；直接读 §3/§5 会拿到过期口径。

---

## 0. 目标与验收口径

三件事，按此优先级：

1. **D33 裁决落地**：把 earnable 编辑器做完（A 路），让「TL 解锁能力」这条产品主张可被运营开启。
2. **M5 收尾**：资料页等级卡（**已核实已接线，见 §2-1**）+ earnable 编辑入口（真缺口）。
3. **49 条未实现收口**：能自动化的落证据，不能的**显式标未实现 + 交代**（省略等于宣称覆盖了）。
4. **两条 dev 库的账**：`mapOptionsError` 四类错误归一化；dev 库夹具数据污染清理（dry-run + 用户确认）。

**M8 门槛不变**：0 FAIL、0 空白行、每个未实现条目写明「环境限制」还是「待落地（谁、哪个里程碑）」。
**编号源不变**：矩阵是唯一编号源；本刀新增条目追加到矩阵（§7），不另起一份。

---

## 1. 核心判断与裁决（D33）

**问题**：默认 `earnable = ∅`，而 `SeedMemberPermissions` 已含正白名单的全部 7 键
（`Identity/seeds.go:157-165` 与 `TrustLevels/registry.go:26-34` **集合相等、顺序不同**，已实测）。
> **别写「逐字相同」**：seeds 顺序是 `topic.create→topic.edit_own→topic.delete_own→post.create…`，
> 白名单顺序是 `topic.create→post.create→attachment.upload→topic.edit_own…`。集合相等是 D33 的
> 立论基础；顺序不同意味着**白名单顺序是 UI 展示序的唯一权威**，不能拿 seeds 的顺序当断言。
> （行号由 writer 2026-11-11 09:21 CST 实测纠正，本计划初稿写的是 153-161 / 29-37。）
所以「TL2 解锁 `topic.create`」对默认安装是**零行为变化**——等级只影响限额与展示。
机制建好了但没人能配置，等于死代码（D17）。

**裁决：走 A 路**——把 earnable 编辑器做完，**默认空集保持**，运营按需开启。

| 路 | 内容 | 本刀 |
| --- | --- | --- |
| **A（选定）** | 后台按档勾选白名单键；默认 ∅；零行为变更 | **做** |
| B（Z3） | 收窄 `member` 基线，7 键改由 TL1 授予 | **不做**，另开一刀 |

**B 为何不做**：前置是重写「开关关闭」的语义——现 D9 是「关闭 → 已挣得集合恒空」，收窄基线后
一关开关全站会员就不能发帖。必须先改成「关闭 = 人人按 TL1 处理」，那是 F 组口径重写 + 迁移，
不是本刀能塞进去的量。

**A 路的代价**：UI 要设计「勾选哪些键」+ 白名单校验 + **累积语义披露**（见 §5-T2）。

---

## 2. 已核实事实（带命令与时间，2026-11-11 09:15–09:30 +08:00）

> 纪律（父计划 §14-5）：凡「X 存在 / X 不存在 / X 零调用者」必须附命令与时间；
> 消费方自己重跑一次。以下是**观察级**证据，不是提交级——提交级由 ops 在收尾时回填。

| # | 结论 | 证据 |
| --- | --- | --- |
| 1 | **资料页等级卡已接线，不是 M5 缺口。** `useTrustLevel.ts` 全实现（`loadViewer()` 真请求 `/trust-levels`），`SFProfileShowPage.vue:110` 传 `computed(() => profile.value?.trustLevel)`，`SFProfileRightRail.vue:74` 消费 | `rg -n "useTrustLevel" apps/web/app`；`cat apps/web/app/composables/profile/useTrustLevel.ts` |
| 2 | **模块笔记 residual 17 与 `index.md:19-20` 的「数据源仍恒 null（M5）」是滞后型否定句** | 同上；F4 的浏览器三态证据（`storage/qa/trust-levels/F/F4-evidence.md`）也佐证卡片真渲染 |
| 3 | **earnable 后端校验已完整**：`registry.go:103` `Earnable.Validate()`（白名单 + 管理类禁令 + 父键禁令），调用点在 `config.go:131`；`decodeEarnableList`（`config.go:194`）处理 `null`→空集 / `[]`→空集 / 严格解码三态；**只有省略才 422** | `grep -n "func (e Earnable) Validate" apps/api/app/Models/TrustLevels/registry.go`；`sed -n '193,205p' …/config.go` |
| 4 | **白名单已下发给后台**：`GET /api/v1/admin/trust-levels` 的 `eligiblePermissions`（`controller.go:144`，OpenAPI `schemas/trust_levels.yaml:128` 已声明） | 同上 + `core_catalog_gen.go:231` |
| 5 | **但前端 tab 拿不到它**：`SFAdminTrustLevelsTab.vue` 只读 `props.items`（admin options map），**没有任何 `/admin/trust-levels` 请求**。编辑器必须新增白名单数据源，**不许硬编码 7 键**（那就是第二份副本） | `sed -n '1,120p'` 该文件 + `rg -n "admin/trust-levels" apps/web` 零命中 |
| 6 | **`Earnable.Grants(level)` 是累积语义**：`registry.go:57-75` 从 `LevelMember` 循环到 `level` 取并集。勾「TL2: topic.create」= TL2/TL3/TL4 都解锁 | `sed -n '55,80p' apps/api/app/Models/TrustLevels/registry.go` |
| 7 | **`mapOptionsError` 只有 3 分支**：`ErrPermissionDenied`→403、`ErrInvalidOption`→422 `validation.invalid`、其余透传。四类失败（归一化 / `isValidValueSet` / store 写入 / body 绑定）里前三类都收敛到 `ErrInvalidOption` | `sed -n '174,186p' apps/api/app/Http/Controllers/Options/controller.go` |
| 8 | 四条路由 + guard 实测：`GET /trust-levels`(public) / `GET·PUT /admin/trust-levels`(admin.access·trust.manage) / `POST …/restore-defaults`(trust.manage) | `core_catalog_gen.go:231/232/233/396` |
| 9 | `SFAdminTrustLevelsTab.vue` 310 行、`useTrustLevelsThresholds.ts` 267 行 —— 加勾选矩阵会破 500 行提示线 | `wc -l` |
| 10 | 目录身份已存在：`catalog-identities.json:3458` 登记了 `SFAdminTrustLevelsTab.vue`。**新增组件要另登记** | `rg -n "TrustLevels" docs/extensions/v3/catalog-identities.json` |

---

## 3. 里程碑与派工

每个里程碑先落文件、再派活；员工读文件不读转述。**写域见 §4，不得越界。**

### M9 — 契约与白名单数据源（backend 主导，pm 校契约）

- 确认 `eligiblePermissions` 的**语义与顺序**稳定（当前 = `earnableWhitelist` 字面顺序），
  并在 OpenAPI 的 `eligiblePermissions` 描述里写清「顺序即 UI 展示顺序、七键固定」。
- 给 `GET /admin/trust-levels` 补一条**形状测试**：`eligiblePermissions` 长度 7、**字面集合相等**
  （集合，不是顺序——顺序另有断言 J-02）、且**与 `EarnableWhitelist()` 同源**（不许测试断言
  自己的实现——用字面集合，见 §5-T5）。
- 判据：`go test -count=1 ./app/Http/Controllers/TrustLevels/` 绿；`ruby scripts/validate-openapi-refs.rb` 绿。

### M10 — earnable 编辑器（frontend 主导，designer 出规范）

**新增组件** `apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue`：
按 TL1–TL4 × 7 白名单键的勾选矩阵；TL0 整行 disabled（TL0 由 `trust.new_user*` 七键决定，
不参与 earnable，`Grants()` 对 `level <= LevelNewcomer` 直接返回 nil）。

**四条硬要求**：

1. **白名单来自 API**（`GET /admin/trust-levels` 的 `eligiblePermissions`），前端零硬编码；
   请求失败 → 矩阵不可编辑 + 显式错误条，**不许**回落成空矩阵（空矩阵 = 「没有可勾的键」= 谎）。
2. **累积语义必须显式披露**（§5-T2）：勾选按「该档**新增**」呈现，且矩阵上方常驻一条说明
   「勾选 TL2 的键会让 TL2 及以上全部解锁」。**禁止**把矩阵画成「该档拥有」。
3. **`earnable` 仍必须原样带回**：`trustLevelsPayload` 已把 `form.earnable[level] ?? []` 写进
   payload，编辑器接入后这条不能丢。三态语义（frontend 2026-11-11 10:0x 实测
   `config.go:194 decodeEarnableList` 纠正本计划初稿）：`null` → 空集、`[]` → 空集、
   **只有省略才 422**（`len(RawMessage)==0`）。所以正确断言是「键必须始终存在且为数组」，
   **不是**「`null` 会 422」——写后者是假断言。
4. **登记点 8 处**（模块笔记「登记点」节）：组件文件 + 两份 locale + `catalog-identities.json` +
   `node scripts/v3-catalog/generate.mjs` 生成物。漏 #7 生成器抛错、漏 #8 `--check` 红。

**几何规范由 designer 先出**（`reports/2026-11-11-trust-level-earnable-spec.md`）：
7 列 × 5 行的窄屏断点（7 是奇数 → 允许末项 `span 2`，与阈值网格的「6 偶不跨」规则相反，
写进规范）、勾选控件尺寸、TL0 禁用态、空态文案、`aria` 标签。

### M11 — 49 条未实现收口（ops 主导，backend 补测试）

按矩阵 §3 逐条：

| 组 | 条目 | 做法 |
| --- | --- | --- |
| A11–A12 | `CapabilitySource`/`GrantSource` 无测试 | backend 补 `TestGrantSource*`（**与 `Can()` 走不同路径**：用字面期望，不用 `Can()` 反推） |
| C13–C16, C20 | 软删/驳回/封禁不改计数、`user_visits` 同日 PK 冲突 | ops：psql 造数 → `GET /trust-levels` 读 `viewer.level` 对比，落 `C/*.txt` |
| D2–D3, D10 | 日限额 / 外链拒绝 / 关开关基线 diff | ops：curl 循环 + `--flag-off-evidence` |
| E5 | `Level()` 纯度无断言 | backend 补「fake store 调用计数 == 0」（当前只有 reviewer 手工证据，**手工证据不是断言**） |
| E6, F3, F5, F8, F9 | 开关态 HTTP + flag catalog 完整性 | F9 由 backend 补「常量数 == Catalog 条目数」测试；其余 ops curl |
| G1–G6 | 性能 + `LoadActor` 查询计数 | ops：`trust-levels-perf.sh` 同二进制翻开关 A/B；G3/G6 用 `pg_stat_statements`，backend 补可执行断言则升级为测试 |
| H1–H3, H6–H12, H14, H16, H17 | 契约/门禁/i18n/依赖方向 | ops：`validate-openapi-refs.rb`、`validate-architecture-boundaries.mjs`、`go list -deps`、`jq` 双语键族 |
| I-01/02/05/09/11/13/15–20/24 | 端点行为 | ops：矩阵 §1.1 的 `call` 助手逐条 curl，落 `I/*.json` |

**收口纪律（父计划 §14 + 模块笔记「验收与证据」）**：
- 每条必须落 `storage/qa/trust-levels/<ID>`；脚本按矩阵 ID 命名，**矩阵有而脚本没有的显式标「未实现」**。
- 测试类条目必须 `-count=1 -v -run '^Name$'` 且日志里出现 `=== RUN <name>`；
  只有 `ok … [no tests to run]` 判 **FAIL（未执行）**，不是 PASS。
- `./scripts/test-api-isolated.sh` **必须带 `-v`**，否则 A-3 判「测试没跑」。
- 收尾由 ops 出**新一版** `_summary.txt`，reviewer 出**新一版**合并表（`…-m13-final-status.md`），
  两份数字不一致时按 M8 的合并口径解释，不许让下一个人以为其中一份错了。

### M12 — 两条 dev 库的账（backend + ops 并行）

**M12-a `mapOptionsError` 拆 reason**（backend）：

- `ErrInvalidOption` 再细分：**归一化失败** 与 **`isValidValueSet` 拒绝** 各给独立 reason
  （前者是「值不合法」，后者是「值不在枚举内」，运营看到的修复动作完全不同）。
- **store 写入失败必须是 500**，不是 422——写库失败不是用户的输入错误。
- 判据：三条路径各一个断言，且**断言 reason 字符串字面量**（不是断言「等于某个常量」——
  那就是自证）。已授权面：`Options/controller.go:174` + `Models/Options` 的 error 定义。

**M12-b dev 库夹具污染清理**（ops）：

- 现状：`topics` 19 行、`categories` 26 行名为 `notif-page-*` / `notification-target-*`，
  本应跑在一次性集群。
- **禁止 `DELETE FROM topics` 批量删**——删 `topics` 会影响 `user_activity.topic_count`，
  而那正是等级判定的输入，删完所有人的等级都会掉。
- 做法：先出 **dry-run 清单**（`SELECT id, title, created_at` + 关联行计数 +
  受影响 `user_activity` 行数与将变化的等级），交用户确认后再执行；执行走
  「软删 + 计数重算」而不是物理删。
- 证据落 `storage/qa/dev-db-cleanup/<ts>/dry-run.txt`。

### M13 — 验收 + 扫上一刀（reviewer 主导，pm 出矩阵增量）

- **矩阵增量**（pm）：把 M9–M13 的新断言按现有 ID 体系追加（`J` 组 = earnable 编辑器，
  `K` 组 = 上一刀复查），编号连续、不重排既有 124 条。
- **扫上一刀**（reviewer）：徽章那刀的四个跨域缺口（`badges` i18n 整块键、路由 tripwire、
  六个错误码、资料页两个统计键）逐条确认已闭合；**本刀验收必须顺带扫上一刀**，
  因为那四条一个都不在徽章自己的 84 条断言里。
- **变异验证**（reviewer）：新增分支至少各做一次变异（改坏看红、改回看绿），
  **守卫测试先证明会误报**；只测拒绝不测允许的测试要配对照组。
- **浏览器实测**：earnable 矩阵 desktop + `390×844`；勾选 → 保存 → 重载 → 勾选态保持；
  TL0 行 disabled；白名单外无键可勾。

---

## 4. 派工与文件域（避免写冲突）

| 角色 | 文件域（**只写这些**） |
| --- | --- |
| pm 阿策 | `knowledge/reports/2026-11-11-trust-levels-acceptance-matrix-v3.md`（增量）、账号矩阵 |
| frontend 小前 | `apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue`（新）、`SFAdminTrustLevelsTab.vue`、`apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts`、`apps/web/i18n/locales/{zh-CN,en-US}.json`、`apps/web/tests/admin/settings/trustLevels.test.ts`、`docs/extensions/v3/catalog-identities.json` |
| backend 老后 | `apps/api/app/Http/Controllers/Options/controller.go`、`apps/api/app/Models/Options/*.go`、`apps/api/app/Models/TrustLevels/*_test.go`、`apps/api/app/Models/Identity/*_test.go`、`contracts/openapi/schemas/trust_levels.yaml`、`apps/api/app/Http/Controllers/TrustLevels/*_test.go` |
| reviewer 小审 | `storage/qa/trust-levels/**`（gitignored）、`knowledge/reports/2026-11-11-trust-levels-m13-final-status.md` |
| researcher 小研 | `knowledge/reports/2026-11-11-trust-level-earnable-audit.md` |
| designer 小设 | `knowledge/reports/2026-11-11-trust-level-earnable-spec.md` |
| ops 老运 | `scripts/verify-trust-levels-acceptance.sh`、`storage/qa/**`、`scripts/dev-db-cleanup-dryrun.sh`（新） |
| writer 笔笔 | `knowledge/decisions/2026-11-11-trust-level-earnable.md`、`docs/{zh-CN,en-US}/usage/trust-levels.md`、`knowledge/modules/trust-levels.md`、`knowledge/sessions/2026-11-11-trust-levels-closing.md` |

**重叠点**：`storage/qa/` 由 ops 与 reviewer 共用 —— ops 写 `_summary.txt` 与 `<ID>.txt`，
reviewer 写 `_reviewer-logs/` 与 `*-evidence.md`，**文件名不得相撞**。
`knowledge/reports/` 每人一个文件，不共写。

---

## 5. 本刀陷阱（T20–T25，动手前必读）

**T20 滞后型否定句。** 模块笔记 residual 17、`index.md:19-20` 的「数据源仍恒 null」在
2026-11-11 09:20 实测**已不成立**（§2-1/2-2）。漂移方向是「滞后」不是「漏写」：
正确修法是**删掉过期的否定句**，不是补一句新的肯定句。凡「没有 X」必须每次对拍重测。

**T21 累积语义是这一刀最大的误导源。** `Earnable.Grants(level)` 累积（§2-6）。
UI 画成「该档拥有」会让运营以为只给 TL2，实际 TL4 也拿到。矩阵标题、行内说明、
`aria-label` 三处都必须用「新增」措辞。

**T22 白名单不许有第二份副本。** 前端硬编码 7 键 = 本仓库最大的 bug 源（纪律 #2）。
数据源只有 `GET /admin/trust-levels` 的 `eligiblePermissions`。请求失败要**显式不可编辑**，
不许回落成空矩阵。

**T23 `null` 与 `[]` 等价，只有省略是 422。** `decodeEarnableList`（`config.go:194`，2026-11-11
实测）明写 `null → 空集`、`[] → 空集`；只有 `len(RawMessage)==0`（键整个不出现）才 422。
`Grants()` 对两者结果一致。所以断言要钉「键始终存在且为数组」，**不要写「`null` 会 422」** ——
那是本计划初稿的错误，已被 frontend 对拍纠正。

**T24 权限判定在解码之前。** 顺序固定：开关（关闭→404）→ 权限（不足→403）→ 解码（非法→422）→
业务。新加的矩阵不改这个顺序，也不新增权限键（读复用 `admin.access`，写用 `trust.manage`，
**不做 `trust.view`**——`badge.view` 是死权限的教训）。

**T25 新增组件 = 8 处登记点。** 见模块笔记「登记点」节。漏 #1–#4 是**静默回落**（tab 不显示），
漏 #7 生成器抛错，漏 #8 `--check` 红。别按「6 处」记。

**外加父计划三条仍然生效**：`go build` 不编译 `_test.go`（§14）；改被 fixture 复制的查询要
`rg` 该列名在 `_test.go` 建表语句（§17 T6/T7 复发）；架构 cap 是双向棘轮，文件变短要同轮下调。

---

## 6. 明确不做

1. **B 路 / Z3**（收窄 `member` 基线）——见 §1，另开一刀。
2. **`trust.view` 权限键** —— 后台读复用 `admin.access`（D9 的裁决不变）。
3. **`trust.new_user.forbid_attachments` 接线** —— D20 不变，仍是死配置残留。
4. **`/badges` 公共目录页** —— 要 Page Registry 全套（catalog / route shell / ViewModel /
   Host island / 主题模板 / 完整性测试），是独立一刀，本刀只登记。
5. **`SFComment.vue` 537 行的拆分** —— 与本刀无关，登记不修。
6. **`sqlc generate` 修复** —— 已知不可安全运行（模块笔记 residual 14），另开一刀。
7. **`LoadEffectiveActorTx` 不展开父→子继承** —— 已知缺口（residual 11），另开一刀。

---

## 7. 验收矩阵增量（M9–M13 新条目，追加到矩阵）

| ID | 断言 | 谁验 | 怎么验 |
| --- | --- | --- | --- |
| J-01 | 后台 `eligiblePermissions` 恰 7 键、字面集合相等 | ops | `T: ^TestAdminEligiblePermissionsLiteral$ @ Controllers/TrustLevels` |
| J-02 | 白名单顺序 = `EarnableWhitelist()` 顺序（UI 展示序稳定） | ops | 同上 + `jq '.data.eligiblePermissions'` |
| J-03 | 编辑器勾选 TL2 `topic.create` → PUT 200 → 重载后该键在 TL2 的 `earnable` 内 | rev | 浏览器实测 |
| J-04 | 白名单外的键无法勾选（UI 无该键 + 构造请求 422） | rev | 浏览器 + `PUT` 手造 body |
| J-05 | TL0 行整行 disabled，不可勾 | rev | 浏览器 + `aria-disabled` |
| J-06 | 累积披露文案在页面实读为 true | rev | 浏览器 `innerText` 含「及以上」 |
| J-07 | 白名单请求失败 → 矩阵不可编辑 + 错误条（**不**回落空矩阵） | frontend 自测 | 组件测试（mock 500） |
| J-08 | `earnable: []` 保存后重载仍为 `[]`（不丢成 `null`/省略） | ops | `jq` + `Q "SELECT value FROM options …"` |
| J-09 | 变异：白名单塞 `user.ban` → J-01 红；改回绿 | rev | 变异红绿两份 |
| J-10 | 390×844 无横向溢出、7 列不裁切 | rev | 浏览器视口实测 |
| K-01 | 徽章 i18n 整块键在两份 locale 存在且 zh/en 成对 | ops | `jq` 键集对比 |
| K-02 | 徽章路由 tripwire 在（catalog ↔ 目录奇偶） | ops | `T: ^TestCoreRouteCatalogHasExactReviewedGuardParity$ @ Support/Routes` |
| K-03 | 徽章六个错误码各有本地化文案 | ops | `rg` 两份 locale |
| K-04 | 资料页两个统计键（获赞/访问天数）在 payload 与 UI 都存在 | rev | 浏览器 + SSR 载荷 |

---

## 8. 提交纪律

- 提交前：`./scripts/test.sh --fast` 三线 exit 0 + 四条静态门禁绿 + 矩阵 0 FAIL 0 空白。
- 文档回填独立提交（纯文档不影响构建，分开更清楚）——照上一刀的做法。
- 提交后：所有「观察级」存在性结论回填**提交级**证据（`git log -S` + `git grep … HEAD`），
  只补证据、不改结论。
- 提交信息里的数字必须与 `…-m13-final-status.md` 一致，由 reviewer 核对后 Lead 才落提交。

---

## 9. 修订记录（本刀边做边改，读之前先看这里）

### 9.1 计划初稿被推翻的说法（看到请当过期）

| 初稿说法 | 实际 | 谁纠正 / 证据 |
| --- | --- | --- |
| 「资料页等级卡数据源仍恒 `null`（M5）」 | **已接线**：`useTrustLevel.ts` 全实现，`SFProfileShowPage.vue:110` 传 `profile.value?.trustLevel` | Lead 2026-11-11 09:20 实测；writer 已改 `index.md` 与模块笔记 |
| `SeedMemberPermissions` 在 `seeds.go:153-161` | **`157-165`**（常量字面量 `:29-59`） | writer 09:21 实测 |
| `earnableWhitelist` 在 `registry.go:29-37` | **`26-34`** | writer 09:21 实测 |
| 「`Earnable.Validate()` 在 `config.go:131`」 | `Validate()` 在 **`registry.go:103`**；`config.go:131` 是调用点 | writer 09:21 实测 |
| 7 键与 `SeedMemberPermissions` **「逐字相同」** | **集合相等、顺序不同**。seeds 序：`topic.create→topic.edit_own→topic.delete_own→post.create→post.edit_own→post.delete_own→attachment.upload`；白名单序：`topic.create→post.create→attachment.upload→topic.edit_own→post.edit_own→topic.delete_own→post.delete_own`。**白名单顺序是 UI 展示序的唯一权威** | writer 09:21 + Lead 复核 |
| 「`[]` 与 `null` 语义不同，`null` 会 422」 | **`null` 与 `[]` 语义等价**（`decodeEarnableList` `config.go:194`：`null → nil`、`[] → 空切片`）；**只有省略才 422**（`len(RawMessage)==0`，`config.go:102`）。往返恒为 `[]`（`EncodeConfig` 把 nil 编回 `[]`） | frontend 10:0x 实测；writer 已把决策记录改成实测口径 |
| §4 文件域写 locale 在 `apps/web/app/lang/` | **`apps/web/i18n/locales/{zh-CN,en-US}.json`** | frontend 实测 |

### 9.2 首轮扫描新增的事实（全部已派修，见 §9.3）

| # | 事实 | 证据 | 影响 |
| --- | --- | --- | --- |
| F1 | **默认安装下 earnable 是「零行为 + 零来源可解释性」**：7 键全在 `member` 基线 ⇒ `mergeEarnedPermissions` 的 `present[key]`（`postgres_store_roles.go:353`）全跳过 ⇒ `Actor.Earned` 恒空 ⇒ `GrantSource` 对这 7 键**永远返回 `static`** | researcher 探针 A：`Earned=map[] len(perms)=7` | **A 路的 UI 文案若写「解锁」，对默认安装是说谎。** 已在规范里加 §3.3 强制披露 |
| F2 | **累积是单向向上的**：`Earnable{TL4:[topic.create]}.Grants(TL2)` = `[]`；勾 TL2 → TL4 拿到，反之不成立 | researcher §1 | 矩阵「按该档新增」的呈现口径正确 |
| F3 | **`Permissions(level)` 是增量（消费者 `payload.go:44`、`config.go:148`），`Grants(level)` 是累积（唯一生产出口 `resolver.go:90`）**。把 payload 改成 `Grants` 会导致**每存一次膨胀一轮** | researcher §1.3 | **展示口径不可换**——已加进 reviewer 的变异配方 |
| F4 | **deny 无条件赢，且来源报 `none`**（不是 `earned`、也不是 `static`） | researcher 探针 C：`perms=[post.create] Earned=map[]` | 正确形态，无需改 |
| F5 | **开关关闭的静默落差比计划描述的更宽**：`PUT /admin/web-options` **不查 feature flag**（`useWebOptions.ts:390-394` 只校 `trust.manage`）⇒ 运营能保存并看到「已保存」，但前台 404、等级不生效 | researcher §5.2 | 派 frontend 加关闭态 `UAlert`（**不改后端行为**）。矩阵 `J-11` |
| F6 | **缺 `canManage`**：只有 `admin.access` 的账号（moderator / tech_admin）侧栏看不到 tab，但直连 URL 能读、能编辑，**保存才 403** | researcher §6.3 | 派 frontend 参考 `SFAdminSiteUpdatesTab.vue:22` 补禁用。矩阵 `J-12` |
| F7 | **`badge.view` 不是死权限**：`core_catalog_gen.go:44` 有守卫、`adminModules.ts:229` 在用 | researcher §6.4 | 父计划 §6 的论证链要重写；**结论保留**（不做 `trust.view`）。父计划不动（历史证据），在模块笔记里加订正 |
| F8 | **`admin.badges.*` 整块缺失**：`apps/web/app` 用了 **69 个**键，两份 locale **各 0 个定义**，`/control-panel/badges` 全裸键（`badges.*` public 侧 15/15 已闭合） | reviewer `K-01-keyset.txt` / `K-01-resolve.txt`；Lead 09:30 复核 69/0 | 派 writer 出文案表 + frontend 合入。矩阵拆 `K-01a`/`K-01b` |
| F9 | **K-04 是 payload 缺字段不是 UI 缺字段**：`PublicProfile`（`Profile/types.go:70-71`）只有 `topicCount`/`commentCount`；`rg likeReceivedCount\|daysVisited --glob '*.go'` 在 profile 路径零生产者；`contracts/` 未声明。前端 `SFProfileFeedTab.vue:74-79` 用 `?? 0` 兜底 ⇒ **页面恒显示 0**（比裸键更难发现） | reviewer `K-04-payload.txt` | 派 backend 加字段 + OpenAPI + 断言 |
| F10 | **新增 `Code*` 常量不补双语文案 = 门禁红**，且红在**另一个包的测试**里（写代码的人看不到） | Lead 09:29 实跑：`TestAPIErrorCodesHaveLocalizedMessages` FAIL × 3 codes | 「声明 ≠ 执行」的第六个同形案例。矩阵 `L-01` |
| F11 | **`eligiblePermissions` 的 Go 生产消费者 1 个**（`controller.go:144`，DTO 里），不是「只有测试」。09:24 起 frontend 已有 fetcher（`useTrustLevelsThresholds.ts:413`），**tab 仍未接线** | researcher §4 | 本刀定位 = 「tab 接线那一刻诞生的第一个 UI 消费者」 |

### 9.3 首轮派修清单（归属与状态）

| 项 | 归属 | 状态 |
| --- | --- | --- |
| `messages.go` 三个 code 双语（`L-01`） | backend | 在修（**门禁红，最高优先**） |
| K-04 profile 两个统计字段 | backend | 在修 |
| M10 矩阵主体 + 8 处登记点 | frontend | 在修 |
| `J-11` 关闭态 `UAlert` / `J-12` `canManage` | frontend | 在修 |
| `K-01b` 69 键文案表 | writer | 在修（frontend 等表合入） |
| F7 `badge.view` 论证订正 | writer | 在修 |
| 规范 §3.3「默认安装下是空操作」披露 | designer | 在修 |
| 矩阵 v3（含 J-11/J-12/K-01a/K-01b/L-01） | pm | 在修 |

### 9.4 一条给下一个会话的判断

**A 路（做完编辑器）之后，默认安装的「TL 解锁能力」仍然是休眠的。** 这不是实现缺陷，是 D33 的
诚实定位：A 路给的是「已经收窄过基线」或「用自定义低权角色」的站点一个配置界面，
**不是给默认安装一个新能力**。想让「TL2 解锁发主题」真有牙，唯一路径仍是 B 路（Z3，本刀不做）。
任何文档、UI 文案、验收断言若暗示「勾了就有行为变化」，都是假的。
