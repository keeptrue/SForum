# objectRef 候选（`refCandidates`）残留收口：R1–R5

- 执行：2026-11-13 12:25–13:20（Asia/Shanghai）；lead（单人执行，团队看板本会话未启用）
- 基线 commit：`fab8d708a`（实现为未提交工作树改动）
- 上一刀验收：`reports/2026-11-13-badge-ref-candidates-acceptance-run.md`（accepted，28 ok / 0 FAIL / 2 pend）——**本文件不改写它**，残留清单只在 §9-A 定义，本刀只负责关掉
- 判据来源：本刀任务书 §1（R1–R5 的「改法 / 判据」逐条抄自验收报告 §9-A 与 lead 裁决）
- 环境：:8080 dev API（air 热重载，实测已换新二进制）、:3000 dev web（本轮由我拉起，`bun run dev`）、`sforum-postgres`（127.0.0.1:15432）、headed Chrome + CDP 9333、管理员真实会话（沿用 `/tmp/qa-cookies.json` 的 `sforum_session`，未猜账号密码）

## 0. 结论

| 残留 | 结果 | 一句话 |
| --- | --- | --- |
| R1 「取不到」与「本来就没有」共用一句话 | **已闭** | `refKindMode` 三态 + 两句文案 + `data-ref-manual-state` |
| R2 未知 refKind 静默退文本框、目录不变量只查「有没有数组」 | **已闭** | `declaredRefKinds` / `refCandidatesMissingKinds` + `data-ref-unknown-kind` + 规则区可见诊断 |
| R3 手动徽章 rule-impact 422 → 弹窗必现「估算失败」 | **已闭** | 后端 200 `estimated:null`；前端不请求且把「无法估算」（中性）与「获取失败」（红）拆开 |
| R4 手动徽章带 rule → 422 泛码 | **已闭** | 新码 `forum.badge_rule_mode_conflict`（sentinel / 常量 / 两处 wrap / mapBadgeError / zh+en / OpenAPI） |
| R5 覆盖缺口 A5-c 长列表滚动 | **已闭** | 注入 35 条候选，390×844 实测 `scrollHeight 988 > clientHeight 211`、滚到底最后一项在视口内 |
| R5 覆盖缺口 A3-b 服务端真失败 | **已闭（换了注入点，原因见 §5.2）** | 任务书指定的「只改表名」会让整条请求 500（鉴权也 JOIN `roles`）；改成「改名 + 缺 `alias` 的同名视图顶替」后，端到端得到 role 组 `unavailable:true`、其余四类照常有值、控件退文本 |

门禁：`./scripts/test.sh --fast` **三线 PASS**（api / web / static，见 §6.1），架构 216（回到基线）、OpenAPI 3312 refs OK、docs OK、tzdata OK、`bun run typecheck` exit 0、`bun test` 2643 pass / 0 fail。
验收脚本两轮：**无库轮 ok 20 / FAIL 0 / pend 53（API 在跑）**、**带库轮 ok 78 / FAIL 0 / pend 9**，与任务书给的基线一致。

## 1. R1：三态 + 两句提示

### 改了什么

- `apps/web/app/utils/admin/badgeRefCandidates.ts`：`refKindManual(map, kind): boolean` → **`refKindMode(map, kind): 'select' | 'unavailable' | 'empty'`**（旧函数删除，不留别名——留着就会被下一个「顺手」的人继续用）。渲染分支仍只有两叉（后两者都出文本框），三态只服务文案。
- `apps/web/app/components/admin/badges/rule/SFAdminBadgeRuleParamField.vue`：文本框加 `:data-ref-manual-state="refMode"`；提示改成 `refEmpty` / `refUnavailable` 两句；未知 kind 走另一条提示（见 R2）。
- i18n（zh + en 同时）：新增 `admin.badges.conditions.refEmptyHint`、`refUnavailableHint`；**删除** `refManualHint`（唯一引用点已改，`adminBadgesI18n.test.ts` 的「缺键 / 死键」双向断言会把残留或漏加都判红）。

### 判据与证据

纯函数三态（`apps/web/tests/admin/badgeRefCandidates.test.ts`）：

```
(pass) refKindMode：三态（select / unavailable / empty） > 有候选 → select；取不到 → unavailable；取到了但为空 → empty
(pass) refKindMode：三态（select / unavailable / empty） > 缺失 kind / 未知 kind / undefined 一律 unavailable（不能冒充 empty）
(pass) excludeBadgeSlug：规则不能引用自身 > 只有自身一个候选时退成手填，不补占位项      # 剔自身后为空 = empty
```

渲染判据走 DOM 属性而不是文案字符串（`apps/web/tests/admin/adminBadgeRuleLeafParams.test.ts`）：

```
(pass) SFAdminBadgeRuleLeaf params > 不可用 / 空候选：退成文本框 + 提示，绝不留空下拉   # 逐态断言 data-ref-manual-state
(pass) SFAdminBadgeRuleLeaf params > 白名单外的 refKind：给可见诊断，不是静默文本框
```

浏览器（CDP 拦截 `GET /admin/badges/conditions`，把 `badge` 类候选清空；390×844）：

```json
"empty": {
  "found": true,
  "manualState": "empty",
  "hint": "该类型目前没有任何候选项，可直接填写标识（slug / key）。保存后规则健康检查会告诉你它是否存在。",
  "hasUnknownKindNotice": false,
  "hasSelectMenu": false
}
```

即：**正常状态不再被说成故障**——同一路径在 `unavailable` 下给的是「候选取不到，请直接填写标识（slug / key）…」（见 §5.2 的真实故障证据）。

### 变异证据（改了会红）

把 `refKindMode` 的 `empty` 合并回 `unavailable`（就是上一版的缺陷）：

```
(fail) SFAdminBadgeRuleLeaf params > 不可用 / 空候选：退成文本框 + 提示，绝不留空下拉 [13.37ms]
(fail) refKindMode：三态（select / unavailable / empty） > 有候选 → select；取不到 → unavailable；取到了但为空 → empty [0.21ms]
(fail) excludeBadgeSlug：规则不能引用自身 > 只有自身一个候选时退成手填，不补占位项 [0.21ms]
 40 pass / 3 fail
```

## 2. R2：未知 refKind 与目录不变量

### 改了什么

- `badgeRefCandidates.ts` 新增两个纯函数：
  - `declaredRefKinds(items) → { registered: BadgeRefKind[]; unknown: string[] }`：从 `items[].params[]` 收 `kind === 'objectRef' && refKind !== 'fact'`，去重后按 `BADGE_REF_KINDS` 顺序输出（顺序稳定，断言才比得了列表）。
  - `refCandidatesMissingKinds(items, raw) → BadgeRefKind[]`：**只有响应给了数组时才做差集**；老后端（字段整个缺失/非数组）返回 `[]`；白名单外的 kind **不进这个桶**（它不可能出现在响应里，报「缺失」只会误导）。
- `useAdminBadgeConditions` 暴露 `refCandidatesMissingKinds`（与既有的 `refCandidatesIncomplete` 分工：后者是「整个字段都没有」的非 UI 记账，前者是「给了数组却缺类」的**真回归**）。
- `SFAdminBadgeRuleParamField.vue`：`refUnknownKind` 命中时渲染 `data-ref-unknown-kind`（文案 `refUnknownKind`，带 `{kind}`），并**压掉**那句笼统的「候选取不到」——根因先说话。值仍可见可改（老规则可能已经存了该参数）。
- `SFAdminBadgeRuleSection.vue`：`data-ref-candidates-incomplete` 横幅（`refMissingKinds`，带 `{kinds}`），四跳 prop 链 `badges.vue → tab → 编辑弹窗 → 规则区` 全部接通。

### 判据与证据

纯函数（`badgeRefCandidates.test.ts`，任务书要求的三条齐全）：

```
(pass) 完整响应 → 没有缺失 kind
(pass) 缺了目录已声明的 kind → 报出来（按 BADGE_REF_KINDS 顺序，稳定可比）
(pass) 老后端（不是数组）→ 空：合法，不许报缺失
(pass) 白名单外的 refKind 不进「缺失」桶（它不可能出现在响应里），单独报 unknown
(pass) `fact` 不进这两个桶：它走 factCandidates，是另一个面
```

composable（老后端无字段 → `failed === false` 且无可见诊断）：`(pass) useAdminBadgeConditions 暴露 refCandidates > 老后端（字段整个缺失）：合法静默——failed=false、无可见诊断、无缺失 kind`

浏览器（同一套 CDP 拦截，两种被篡改的响应；390×844）：

```json
"unknownKind": {
  "found": true,
  "hasNotice": true,
  "noticeText": "引用类型「themes」未注册：后端或插件版本不一致，候选无法加载。手填的值仍会保存，但可能不被识别。",
  "noticeKind": "themes",
  "hasGenericHint": false
},
"missingKind": {
  "hasBanner": true,
  "bannerText": "候选目录不完整：缺少 role。这通常意味着后端与插件版本不一致，请检查升级状态。",
  "overflowX": 0
}
```

### 变异证据

```
# ① refCandidatesMissingKinds 退化成「只查有没有数组」
(fail) declaredRefKinds / refCandidatesMissingKinds > 缺了目录已声明的 kind → 报出来（按 BADGE_REF_KINDS 顺序，稳定可比）
(fail) useAdminBadgeConditions 暴露 refCandidates > 响应有数组却缺了目录已声明的 kind → 可见诊断（真回归）
 23 pass / 2 fail

# ② refUnknownKind 恒返回 ''（未知 kind 静默退文本框）
(fail) SFAdminBadgeRuleLeaf params > 白名单外的 refKind：给可见诊断，不是静默文本框
 17 pass / 1 fail
```

## 3. R3：手动徽章的 rule-impact 不再 422

### 改了什么

- **后端**（唯一真相源）：`AdminService.EstimateRuleImpact` 先读徽章，`grant_mode = 'manual'` 时直接返回
  `{estimated: null, method: "unsupported_condition", partial: false, unsupportedConditions: []}`。
  契约本来就允许 `estimated: null`（「无法估算」≠「0 人」）。`automatic` 路径一字未动。
- **前端**：`useAdminBadgeRuleImpact` 新增状态 `'manual'` + `markManual()`；编辑弹窗的判据抽成纯函数
  `shouldLoadRuleImpact({isEdit, badgeId, draftMode, savedMode})`——**读的是库里这枚徽章的授予方式，不是草稿的
  `grantMode`**（原缺陷正是拿草稿 `automatic` 去请求库里 `rule IS NULL` 的徽章）。
- 呈现拆分：`status === 'error'` → 红（`text-red-600`）「影响面估算获取失败」；`status === 'manual'` → 中性
  「无法估算」+ `impact.manualHint`（「这枚徽章当前是手动授予，没有可评估的规则…」），不渲染「不可估条件」
  清单，按钮回到普通「继续保存」。

### 判据与证据

真机 curl（dev 库徽章 11 是 manual、`rule` 为 NULL；徽章 1 是 automatic）：

```
=== R3: 手动徽章 11 rule-impact ===
HTTP 200
{"code":200,"message":"OK","data":{"estimated":null,"method":"unsupported_condition","partial":false,"unsupportedConditions":[]}}
=== 自动徽章 1 rule-impact（回归对照）===
HTTP 200
{"code":200,"message":"OK","data":{"estimated":0,"method":"single_leaf","partial":false,"unsupportedConditions":null}}
```

库内形态（`docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -tAc "select id, slug, grant_mode, (rule is null) …"`）：

```
1|regular-visitor|automatic|f
11|community-contribution|manual|t
12|quality-answer|manual|t
```

前端（`apps/web/tests/admin/adminBadgeRuleImpact.test.ts`）：

```
(pass) manual（库里是手动徽章）是中性态：说「无法估算」，不是「获取失败」
(pass) manual 与 error 互斥：两种状态不会同时渲染对方的块
(pass) shouldLoadRuleImpact：只有「库里已是自动」才请求估算
(pass) each method gets its own explicit sentence…            # 六种 method 分支行为不变
```

### 变异证据

```
# ① 后端：去掉手动徽章的中性返回（回到旧 422）
--- FAIL: TestEstimateRuleImpactOnManualBadgeIsNeutral (0.20s)
    rule_store_postgres_integration_test.go:545: 手动徽章的影响面必须成功返回（旧实现是 ErrBadgeRuleInvalid → 422），得到 badges: invalid rule

# ② 前端判据改回读草稿
(fail) SFAdminBadgeRuleImpactDialog · 三种呈现 > shouldLoadRuleImpact：只有「库里已是自动」才请求估算

# ③ manual 与 error 压成同一块红字（上一版呈现）
(fail) SFAdminBadgeRuleImpactDialog · 三种呈现 > manual（库里是手动徽章）是中性态：说「无法估算」，不是「获取失败」
(fail) SFAdminBadgeRuleImpactDialog · 三种呈现 > manual 与 error 互斥：两种状态不会同时渲染对方的块
```

## 4. R4：`forum.badge_rule_mode_conflict`

### 改了什么（任务书五步，逐条落实）

| 步骤 | 落点 |
| --- | --- |
| 1 sentinel | `Models/Badges/errors.go`：`ErrBadgeRuleModeConflict = errors.New("badges: rule mode conflict")` |
| 2 错误码常量 | 同文件：`CodeBadgeRuleModeConflict = "forum.badge_rule_mode_conflict"` |
| 3 两处 wrap | `normalizeNewBadge`（创建）与 `UpdateBadge`（更新）的「manual 却带 rule」→ 新 sentinel；「automatic 必须有规则」**保持** `ErrBadgeRuleInvalid`（那句文案是对的） |
| 4 mapBadgeError | `Controllers/Badges/controller.go`：新 case 放在 `ErrBadgeRuleInvalid` **之前**（顺序敏感）；`ErrorCode()` 同样放在它之前 |
| 5 文案与契约 | `Support/Localization/messages.go` zh + en；`contracts/openapi/paths/badges.yaml`（POST 描述 + PATCH 描述）、`contracts/openapi/schemas/badges.yaml`（`BadgeCreateInput` 的两码分工） |

### 判据与证据

真机 curl（管理员会话，`X-CSRF-Token` 取自既有 `csrf_` cookie）：

```
=== R4-1: POST 手动徽章 + rule → 期待 422 forum.badge_rule_mode_conflict ===
HTTP 422
{"code":422,"message":"授予方式与规则冲突：手动徽章不能携带规则，请改回自动授予或清空规则。","data":{"reason":"forum.badge_rule_mode_conflict"}}
=== R4-3: POST automatic 无 rule → 期待 422 forum.badge_rule_invalid（不变）===
HTTP 422
{"code":422,"message":"徽章规则无效：请检查指标与阈值的组合。","data":{"reason":"forum.badge_rule_invalid"}}
=== en-US 同码文案 ===
{"code":422,"message":"The grant mode conflicts with the rule: a manual badge must not carry a rule. Switch it back to automatic or clear the rule.","data":{"reason":"forum.badge_rule_mode_conflict"}}
=== R4-2: PATCH /admin/badges/11 {"rule":null,"grantMode":"manual"} → 200（不变）===
HTTP 200
=== R4-4: POST 手动徽章无 rule → 201，随后 DELETE 200 ===
```

清理：`select count(*) from badges where slug like 'qa-r4-%'` → `0`（测完无残留）。

单测（新码必须同批进映射，否则 500 —— plan §13 #19）：

```
(pass) TestMapBadgeErrorKeepsTwoForbiddenPathsApart        # 含 rule mode conflict → 422 + 精确码
(pass) TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid  # 同时包两条链时必须 mode 冲突胜出
(pass) TestManualBadgeWithRuleIsModeConflict               # 三种组合：mode_conflict / rule_invalid / 200
(pass) TestAdminCreateBadgeRejectsInvalidRules             # 表里 "manual with rule" → ErrBadgeRuleModeConflict
```

### 变异证据

```
# ① 忘了把新码加进 mapBadgeError（plan §13 #19 的原始事故）
--- FAIL: TestMapBadgeErrorKeepsTwoForbiddenPathsApart (0.00s)
    controller_test.go:50: rule mode conflict: expected *fiber.Error, got *errors.errorString
--- FAIL: TestMapBadgeErrorPutsModeConflictBeforeRuleInvalid (0.00s)
    controller_test.go:81: code = "forum.badge_rule_invalid", want "forum.badge_rule_mode_conflict" (mode conflict must win over rule invalid)

# ② 两处 wrap 退回笼统 ErrBadgeRuleInvalid
--- FAIL: TestManualBadgeWithRuleIsModeConflict (0.19s)
    rule_store_postgres_integration_test.go:605: manual + rule 必须报 ErrBadgeRuleModeConflict，得到 badges: invalid rule: manual badge must not carry a rule
```

## 5. R5：两条覆盖缺口

### 5.1 A5-c 长列表滚动（pend → ok）

手法：CDP `Fetch.enable` 拦截 `GET /admin/badges/conditions`，把 `badge` 类候选替换成 35 条；390×844 打开编辑弹窗 →
切「自动授予」→ 选「已拥有徽章」→ 点开 `USelectMenu` → 量测。

一个方法学修正（记在这里，避免下一个人重踩）：第一次我是「弹窗停在顶部、直接点底部字段」，弹层贴着视口底边（`boxBottom 952 > 844`），
最后一项虽在滚动容器内却在屏幕外。真实用法是先把字段滚进视野（`scrollIntoView({block:'center'})`）再点，
这不是放宽断言，而是复现操作路径。两种姿态的原始数字都留着：

```
# 姿态 A（不自然）：弹层底边 952 > 视口 844
"scrolled": {"scrollTop":777,"scrollHeight":988,"clientHeight":211,"lastText":"QA 长列表候选 35","lastVisibleInBox":true,"boxBottom":952,"lastBottom":948}

# 姿态 B（真实用法，采信这一条）
"scrollFieldIntoView": {"top":455,"bottom":697,"innerHeight":844}
"menuMetrics": {"optionCount":37, "scroller":{"sh":988,"ch":211,"sw":277,"cw":277}}
"page": {"innerWidth":390,"docScrollWidth":390,"overflowX":0}
"scrolled": {"ok":true,"scrollTop":777,"scrollHeight":988,"clientHeight":211,
             "lastText":"QA 长列表候选 35","lastVisibleInBox":true,"boxBottom":697,"lastBottom":693}
```

截图：`reports/2026-11-13-badge-ref-candidates-residuals-qa/390x844-01-long-list-open.png`、
`…-02-long-list-bottom.png`（第二张能看到最后一项「QA 长列表候选 35」在视口内）。

### 5.2 A3-b 服务端真失败（pend → ok，注入点变更并说明原因）

**任务书指定的注入点实测不可用**（原始输出）：

```
$ PSQL "ALTER TABLE roles RENAME TO roles_qa_tmp"
ALTER TABLE
=== 窗口内 GET /admin/badges（只需鉴权 + badges 表） ===
HTTP 500
=== 窗口内 GET /api/v1/profile（纯鉴权） ===
HTTP 500
{"code":500,"message":"服务器暂时不可用，请稍后再试。","data":{"reason":"internal_error"}}
=== 窗口内 GET /admin/badges/conditions ===
HTTP 500
=== 复原 ===
ALTER TABLE
$ PSQL "select to_regclass('public.roles'), to_regclass('public.roles_qa_tmp')"
roles|
```

根因：鉴权每请求都 `JOIN roles`（`identity.sql` 的 `ListUserRoleKeys` / `ListUserPermissions`），
表名一改，**任何**请求都到不了候选面——「只有 role 组 unavailable」在这种注入下结构上不可能。

**采用的等价注入**（同样「真失败」，不是拦截响应）：`roles` 改名 + 用**缺 `alias` 列的同名视图**顶替。
鉴权只读 `id/key/is_enabled`，照常工作；只有候选那条 `SELECT key, alias, is_enabled FROM roles` 报 42703。
窗口先按任务书要求确认无并发写入会话（`pg_stat_activity`：`active_others 0 / idle_in_tx 0`），结束后立即复原。

原始输出：

```
=== 窗口内：/profile（鉴权是否活着）===
HTTP 200
=== 窗口内：/admin/badges/conditions（期待 200 + role 组 unavailable）===
HTTP 200
  badge unavailable= False n= 12
  category unavailable= False n= 38
  tag unavailable= False n= 24
  role unavailable= True n= 0
  provider unavailable= False n= 1
 items 12
```

同一窗口内的浏览器断言（390×844）：

```json
"roleField": {"found":true,"hasTextInput":true,"manualState":"unavailable",
  "hint":"候选取不到，请直接填写标识（slug / key）。保存后规则健康检查会告诉你它是否存在。",
  "hasSelectMenu":false,"hasNativeSelect":false},
"categoryField": {"found":true,"hasSelectMenu":true,"hasManualInput":false,"aria":"对象标识"},
"page": {"innerWidth":390,"overflowX":0}
```

复原证据：

```
=== 复原 ===
$ PSQL "select to_regclass('public.roles'), to_regclass('public.roles_qa_tmp')"
roles|
$ PSQL "select count(*) from roles"   →  12
```

**为什么这一条同时是 R1 的反向证据**：同一次真实故障里，role 字段给的是「候选取不到」那句，
而 §1 的「清空候选」场景给的是「该类型目前没有任何候选项」——两句话在真实运行时分得开。

## 6. 门禁

### 6.1 `./scripts/test.sh --fast`（三线）

```
=========================== gate summary ===========================
PASS  api     347s  tests/.logs/api.log
PASS  web      50s  tests/.logs/web.log
PASS  static   16s  tests/.logs/static.log
All gates passed.
exit=0
```

（api 347s 与上一刀基线同值；web 54s → 50s、static 17s → 16s 是同一量级的抖动。）

（第一次跑是 **FAIL web**：12 fail / 3 errors，全部是我改动的连带——两个 SFC 测试 harness 少注入新符号
（`shouldLoadRuleImpact`、`refCandidatesMissingKinds`）。已修，见 §7「过程中修掉的三处自己的问题」；
第二次跑满三线绿。）

### 6.2 其它门禁（逐条 exit 0）

```
$ node tests/validate-architecture-boundaries.mjs
Architecture boundary validation passed (2185 production files scanned, 216 files remain above the 500-line review threshold).   exit=0

$ ruby scripts/validate-openapi-refs.rb
OpenAPI references OK: checked 3312 refs across 65 files.   exit=0

$ node tests/validate-docs.mjs
validate-docs.mjs: all checks passed   exit=0

$ node tests/validate-runtime-tzdata.mjs
Runtime timezone validation passed.   exit=0

$ cd apps/web && bun run typecheck   → exit=0
$ cd apps/web && bun test            → 2643 pass / 0 fail（264 文件，exit=0）
```

> 架构门禁第二行一度是 **217**：`SFAdminBadgeEditDialog.vue` 被我加到 503 行（超过 500 的评审线，任务书明确要求前端文件保持 500 以下）。
> 处置：把「无展示位字段错误 → 一行文本」的职责搬进 `utils/admin/badgeFieldSlots.ts`（`unmappedFieldMessages`，
> 与 `FIELD_SLOT_KEYS` 同源、同文件），弹窗回到 **492** 行，计数回到基线 216。

### 6.3 `verify-badge-rules-acceptance.sh` 两轮

| 跑法 | 实测 | 与任务书基线 |
| --- | --- | --- |
| 不带 `DATABASE_URL`（**:8080 API 在跑**） | **ok 20 / FAIL 0 / pend 53**，exit 0 | 一致（基线 20/0/53） |
| 带 `DATABASE_URL`（dev 库 `sforum-postgres`） | **ok 78 / FAIL 0 / pend 9**，exit 0 | 一致（基线 78/0/9） |

脚本本身只加了一处（不是放宽）：`X-03` 的「本刀新增错误码」清单追加 `forum.badge_rule_mode_conflict`，
让静态存在性检查覆盖新码（真正的本地化判据仍由 `go test -run TestAPIErrorCodes ./app/Support/Localization/` 自动发现）。

## 7. 未做与原因

1. **`site.timezone`（模块笔记 Known Residuals #12）未动**：站点级产品决策，需用户拍板；本刀不引入任何时区语义。
   本刀没有任何「按日」判据，因此三时区纪律不适用于本刀（无新增按日断言）。
2. **`fact.count` 的 `factKey`/`variant` 仍是原生 `<select>`**（残留 #29）：M3 已验收面，本刀一行未动（`git diff` 可验：
   `SFAdminBadgeRuleParamField.vue` 的 `refKind === 'fact'` 分支未改）。控件族统一留给下一刀。
3. **`object.engagement.topicId` 仍是 int 自由数字**（残留 #28）：同上，不在本刀范围。
4. **没有新增路由**：R3 只改返回值、R4 只加错误码，路由计数仍 423（`go test ./app/Support/Routes/` 在 api 线里跑过）。
5. **没有改 `knowledge/reports/2026-11-13-badge-ref-candidates-acceptance-run.md`**：它是历史证据。
6. **没有改库结构/产品常量来「造」场景**：R5 的两次注入都是**临时且可逆**（视图顶替 + 拦截响应），
   没有改 `RefCandidatesLimit`、没有加表、没有改迁移。
7. **`RuleImpact` 的 store 层没有放宽**：`GetBadgeRule` 对 NULL 规则仍返回 `ErrBadgeRuleInvalid`（那是「读不到规则」的正确语义）；
   中性返回只加在 service 层（它知道授予方式）。这条边界写进了模块笔记，防止下一个人去 store 里「顺手放宽」。

## 8. 过程中修掉的三处自己的问题（不藏）

1. 第一次 `test.sh --fast` **web 线红**（12 fail / 3 errors）：SFC 测试 harness 是**显式注入符号**的，
   新加的 `shouldLoadRuleImpact`（编辑弹窗）与 `refCandidatesMissingKinds`（壳层 composable stub）没注入 →
   `ReferenceError`。已补注入，复跑 2643 pass / 0 fail。
2. 架构计数一度 217：弹窗 503 行越线，已按 §6.2 的职责搬迁移回 492。
3. 浏览器取证时踩了两个坑，记在这里防复发：**rAF 在遮挡窗口里可能永不触发**（脚本挂死，改 `setTimeout`）；
   **CDP 拦截重放必须重新包裹 `{code,message,data}`**（`useApiClient` 取 `.data`，双重包裹会让目录整体失败，
   表现成「条件下拉是空的」这种假象）。
