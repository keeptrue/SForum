# objectRef 候选：task-4 端到端验收记录（终态）

- 执行人：小审（reviewer）
- 执行时间：2026-11-13 10:52–11:15（Asia/Shanghai）；早前 04:26–05:00 的纯后端取证也并入本记录
- 基线 commit：`7c07edf9d`（实现为未提交工作树改动）
- 判据来源：`knowledge/reports/2026-11-13-badge-ref-candidates-acceptance.md`（A1–A6 共 30 条子断言，A5 含 lead 追加的两条 placeholder 断言）
- 载体：headed Chrome + CDP（`--remote-debugging-port=9333 --user-data-dir=/tmp/qa-chrome`）+ 管理员真实会话 + `docker exec sforum-postgres psql`
- 硬约束：**未修改任何业务代码**；测试期间改动过的徽章配置已逐项复原（见 §6）

## 0. 结论：**accepted（带残留清单）**

| 计数 | 数量 |
| --- | --- |
| ok | **28 / 30** |
| FAIL | **0** |
| pend（未覆盖，不记 ok） | **2**：A3-b 端到端「改表注入」、A5-c 长列表滚动到末项 |

本刀唯一阻断项（F1 `identity.oauth_link` 存不下）已按方案 ① 修复并由我亲自复跑通过；A6 门禁终态 **`All gates passed.`**（api 347s / web 54s / static 17s，一次跑满三线）。残留按性质分两类放在 §9，**不要把"本刀挖出但已修的历史缺陷"和"本刀遗留技术债"混在一起**——混写会让下一个人以为都是新坑。

## 1. A1 简单模式「已拥有徽章」：5/5 ok

被测：徽章 `quality-answer`（id 12，原为手动）→ 规则 `badge.owned{slug:"veteran"}` → 测完已复原为手动/NULL。

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A1-a 候选含其他徽章 | **ok** | 弹层 11 个选项（12 枚徽章 − 自身），含「元老」；`badge` 组 `unavailable=false, truncated=false` |
| A1-b 不含自身 | **ok** | DOM `[role=option]` 集合无「优质解答」；接口侧同名排除由 `excludeBadgeSlug` 在前端完成 |
| A1-c 保存成功 | **ok** | 走完整 UI：保存 → 「保存前确认影响面」→ 仍然保存 → 200；未出现「缺少必填参数。」 |
| A1-d 落库值级正确 | **ok** | `select rule::text from badges where id=12` → `{"v":1,"root":{"op":"all","children":[{"type":"badge.owned","params":{"slug":"veteran"}}]}}`，`params.slug` **精确等于** `veteran`（不是占位串、不是空） |
| A1-e 健康面不受影响 | **ok** | `/admin/badges/rule-health` → `items: 0` |

## 2. A2 高级模式四条 objectRef：4/4 ok（`identity.oauth_link` 于 F1 修复后复跑通过）

逐条 PATCH 到 `community-contribution`（id 11，原手动），每条都用 SQL 读回值级结果，测完复原为手动/NULL。

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A2-a `object.authoring` | **ok** | http 200；SQL `params.targetKind=category, targetSlug=general, window.kind=all, cmp={gte,1}` |
| A2-a `identity.role` | **ok** | http 200；SQL `params.roleKey=member` |
| A2-a `badge.owned` | **ok** | http 200；SQL `params.slug=veteran` |
| A2-a `identity.oauth_link` | **ok（F1 修复后复跑）** | `providerId=sforum.connect.auth` → **200**，SQL 落库 `{"type":"identity.oauth_link","params":{"providerId":"sforum.connect.auth"}}`，公开说明渲染「已绑定「SForum」登录」；负向对照 `__nonexistent__` 仍 **422** `forum.badge_rule_reference_invalid`；写入后 `rule-health` `items:0`（已声明零绑定不再被判失效） |
| A2-b 落库值级 | **ok** | 四条逐条 SQL 精确比对：`targetKind=category/targetSlug=general`、`roleKey=member`、`slug=veteran`、`providerId=sforum.connect.auth` |
| A2-c 健康清单不新增 `missing_*` | **ok** | 四条尝试后 `rule-health` 仍 `items: 0` |
| A2-d 负向对照 | **ok** | `providerId=__nonexistent__` → 422 `forum.badge_rule_reference_invalid`（期望分支：被拒；说明校验确实在跑，A2-c 不是真空绿） |

### F1（P1，**已修**，方案 ①）：provider 候选与保存期校验口径互斥

- 现象：候选来源是 identity registry 快照（`app/Models/Badges/ref_candidates.go:136` 一段，未注入则整组 `unavailable`），保存期校验却是
  `app/Models/Badges/rule_store.go` 的 `checkRuleReferences`：`SELECT EXISTS (SELECT 1 FROM identity_external_links WHERE provider_id = $1)`。
- 本机 `identity_external_links` **0 行** → 候选里唯一的 provider（`sforum.connect.auth`）必然存不进去。
- 代码注释里这个取舍是**刻意**的（「保存期只认有人绑定过的 provider，已声明但零绑定的交给 rule_health」）。判断没错，但与本刀新增的候选面拼在一起就出现矛盾：候选表说"这些可选"，保存说"只有被绑定过的才存在"，而错误文案还写成「可能已被删除」。
- 处置（lead 已拍板 ①）：`checkRuleReferences` 的 `identity.oauth_link` 分支改为复用 `rule_health.go` 的 `knownProviders`（绑定表 ∪ registry 已声明），没有第二份 SQL。**我的复跑证据见 §2 表格 A2-a 行**——已声明零绑定 → 200 且落库；既未声明也无绑定 → 422，wire body 原样。
- 复现：`PATCH /api/v1/admin/badges/11`，body `{"grantMode":"automatic","rule":{"v":1,"root":{"type":"identity.oauth_link","params":{"providerId":"sforum.connect.auth"}}}}` → 422。

### F2（P2，**本刀不修**，见 §9 残留 R4）：手动徽章只给 `rule` 不给 `grantMode` → 笼统错误码

`{"rule":{...}}` 单独 PATCH 到手动徽章 → 422 `forum.badge_rule_invalid`（文案「请检查指标与阈值组合」）。真实原因是「规则与手动授予方式冲突」。补上 `"grantMode":"automatic"` 即 200。plan §13 #19 警告过「笼统映射会让两种修法不同的错误长得一样」，这里是同族：本该有一个 `forum.badge_rule_mode_conflict` 之类的码。

### F3（P3，**本刀不修**，见 §9 残留 R5）：手动徽章的 rule-impact 让弹窗常驻「获取失败」

`GET /admin/badges/11/rule-impact`（手动徽章）→ 422 `forum.badge_rule_invalid`；因此编辑弹窗在「手动 → 自动」这条最常见的路径上必然显示「影响面估算获取失败，仍可继续保存」。文案本身不假，但对"刚把授予方式改成自动"的运营是纯噪声。

## 3. A3 候选不可用 → 文本兜底：2 ok + 1 pend

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A3-a 后端降级不 500 | **ok** | 隔离 PG：`TestBadgeConditionsRefCandidatesUnavailableWithoutStorePort`、`…KeepsPartialResultsOnError` PASS（后者证明单类失败保留其余四类） |
| A3-b 单类故障端到端（改表） | **pend** | **未执行**：改 dev 库 `roles` 表名会在窗口内打断权限判定，且有并发用户在场，风险不值得。等价证据已取（`TestPostgresStoreRefCandidatesSingleKindFailure` PASS + A3-c 的响应级故障注入） |
| A3-c 前端渲染文本框 | **ok** | CDP `Fetch.fulfillRequest` 把 `refCandidates[role]` 改成 `unavailable:true, candidates:[]` 后重载真实页面：`[data-param=roleKey]` 内是 `input[type=text][data-ref-manual][data-ref-kind=role]` + 提示「候选暂时读不到，请直接填写标识（slug / key）。」，**没有** `select`、**没有** 只有占位项的 USelectMenu；键入 `qa_ro_sj8zl` 后规则预览渲染出「拥有「QA readonly」角色」→ 手填值真的进了草稿并被服务端解析。截图 `390x844-03-fallback-text.png` |

**对 lead 那个问题的正面回答（A3 自动化替代是否成立）**：**成立，且已在浏览器里实测，不再是纸面推理**——「候选不可用 → 文本框 → 手填值进草稿」这三步用响应级故障注入完整跑通了，不需要改库。但注意边界：它证明的是**前端对已降级响应的处理**，不等于证明「某一类查询真的会失败并只影响该类」——后者由隔离 PG 的单类失败测试证明。两条合起来覆盖 A3，A3-b 的"真改表"仍记 pend 以便区分证据强度。

## 4. A4 响应形状：7/7 ok

基线快照已落盘：`knowledge/reports/baseline-2026-11-13-conditions.json`（真实响应，`items=12, factCandidates=0, refCandidates=5`）。

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A4-a 顶层键 | **ok** | `['factCandidates','items','refCandidates']`，精确三键 |
| A4-b items/factCandidates 形状 | **ok** | 形状指纹与基线逐路径一致；`items=12`（`fact.count` 因无扩展事实候选而不发布，符合设计）、`factCandidates=0` |
| A4-c 五 kind 固定顺序 | **ok** | `["badge","category","tag","role","provider"]` |
| A4-d 四键 + 类型 + 数组不变 null | **ok** | 每组 `{kind,candidates,unavailable,truncated}`；`candidates` 恒数组（空为 `[]`），两个布尔恒为 `boolean` |
| A4-e flags 值级 | **ok** | 逐值对库核对：分类 `flags∋hidden ⟺ visibility<>'public'`、徽章 `∋disabled ⟺ enabled=false`、角色 `∋disabled ⟺ is_enabled=false`，**0 mismatch**（38 分类 / 24 标签 / 12 徽章 / 12 角色 / 1 provider 全量核对，脚本 `qa-flag-crosscheck.py`）；停用徽章 `collector` 实测 `flags=["disabled"]` |
| A4-f 值非空组内唯一 | **ok** | 全量断言通过（空 value 0、重复 0） |
| A4-g flags 只标记不过滤 | **ok** | 停用 `collector` 期间 `badge` 组候选数仍为 12，该候选照常出现并带标记 |

## 5. A5 浏览器 QA：6 ok + 1 pend（桌面 1440×900 + 390×844）

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A5-a 页面无横向溢出 | **ok** | 390×844：`documentElement.scrollWidth-innerWidth = 0` |
| A5-b 弹层完全在视口内 | **ok** | 桌面 1416×808 视口：listbox `x 761→953, y 510→750`；390×844：`x 56→324, y 373→601`，四边均在视口内 |
| A5-c 长列表滚动到末项 | **pend** | 本机 badge 候选仅 11 条，弹层未触发滚动（`scrollHeight==clientHeight`），**该子项未被覆盖**，不记 ok |
| A5-d 弹窗不横向滚动 | **ok** | 390 宽下弹窗 `w=340`、`dialog.scrollWidth-clientWidth=0` |
| A5-e 键盘可达 | **ok** | Escape 关闭弹层且弹窗存活；Enter 打开；ArrowDown+Enter 选中首项（实际选中「主题作者」）；触发按钮带 `aria-label`（「徽章标识」/「角色」） |
| A5-f 未选值显示 placeholder | **ok** | 两个断点均为「搜索并选择」= `refCandidatesPlaceholder`，**不是空白**。lead 担心的 `:model-value="''"` 未导致 placeholder 消失 |
| A5-g 清空回显 / 设计选择 | **ok（按设计选择记录）** | `:clear="false"`：字段内除触发按钮（aria-label=徽章标识）外**无任何清除控件**（`hasClear=false`），因此「清空后 placeholder 回来」不可达。这是显式设计选择：改值只能改选另一个候选，不允许回到空值 |

截图（6 张，固定命名，均已落盘）：

```
knowledge/reports/2026-11-13-badge-ref-candidates-qa/
  desktop-1440x900-01-editor-open.png
  desktop-1440x900-02-badge-menu-open.png
  desktop-1440x900-03-selected-saved.png
  390x844-01-editor-open.png
  390x844-02-badge-menu-open.png
  390x844-03-fallback-text.png
```

## 6. 变异样本：9/9 判红（真实响应上复跑）

```
node qa-mutations.mjs conditions-data2.json conditions-data2.json
  RED M1 unavailable 强 true        -> A1-a
  RED M2 flags 恒空                 -> A4-e（另有交叉核对：26 处 mismatch，见下）
  RED M3 五 kind 只留一个           -> A4-c
  RED M4 删 refCandidates           -> A4-a | A4-c | A4-d | A1-a
  RED M5 候选重复成"自身"           -> A4-f
  RED M6 candidates 变 null         -> A4-d
  RED M7 漏 truncated 键            -> A4-d
  RED M8 items 多一个键             -> A4-b
  RED M9 flags 塞契约外值           -> A4-e
变异判红 9/9；原始响应全绿
```

**lead 要求的复验**：M9 加严成 `flags ⊆ {hidden, disabled}` 之后再变异一次，**仍判红**（见上）。另外 M2 不只被 JSON 断言判红，还被 **DB 交叉核对**判红：把全部 flags 清空后 `qa-flag-crosscheck.py` 报 26 处 mismatch（隐藏分类不再带 hidden 等）。

**本轮我自己修掉的三处断言缺陷**（记录在案，不当小事藏起来）：

1. 形状指纹从根节点 walk 时用 `path[0]` 过滤，根路径是空数组 → 两侧都返回空、恒等（M8 第一次没红）。改为从 `items`/`factCandidates` 各自下潜。
2. A4-e 原先要求「badge 组必须存在带标记候选」，这是把环境假设写进断言——停用徽章被复原后它就假红。改为「flags ⊆ 枚举」+「任一组的候选带标记」（隐藏分类是常态），值级精确性交给 DB 交叉核对。
3. DB 交叉核对脚本把 psql 的 `t/f` 当成 `true/false`，导致角色组 12 条误报。修的是脚本，不是断言。

## 7. A6 门禁

| 子项 | 结果 | 证据 |
| --- | --- | --- |
| A6-a Go 测试 | **ok** | `./scripts/test-api-isolated.sh ./app/Models/Badges/conditions/ -count=1` 在 `TZ=Asia/Shanghai` / `UTC` / `America/New_York` 三时区分别 `ok 2.741s / 2.696s / 2.573s`；隔离集群 `RefCandidate` 8 条 `--- PASS` |
| A6-b 架构边界 | **ok** | `Architecture boundary validation passed (2185 production files scanned, 216 files above the 500-line threshold)`，exit 0 |
| A6-c OpenAPI 引用 | **ok** | `OpenAPI references OK: checked 3312 refs across 65 files`，exit 0 |
| A6-d `test.sh --fast` | **ok（编排器级：All gates passed）** | 终态一次跑完：`PASS api 347s / PASS web 54s / PASS static 17s`，`All gates passed.`（日志 `/tmp/qa-test-fast4.log`，逐线日志 `tests/.logs/{api,web,static}.log`）。另有一次独立的全量 Go 复跑 144 包 ok / 0 FAIL。详见 §7.4 |

### 7.1 时区 fixture（**已修**，我用三时区独立复核）

根因有两层：① fixture 用 `now() - make_interval(days => N)` 的 24h 算术冒充日历日；② `account_age` 的 SQL 把 Go 进程本地日与 PG 会话时区的日历日相减。

修复后我的复核（不看他人转述，全部自己跑）：

```
TZ=Asia/Shanghai        ./scripts/test-api-isolated.sh ./app/Models/Badges/conditions/ → ok 2.741s
TZ=UTC                  … 同上                                                      → ok 2.696s
TZ=America/New_York     … 同上                                                      → ok 2.573s
```

修复前同一命令在 `TZ=Asia/Shanghai` 下 FAIL（`注册 10 天不应满足 gte 11` / `以昨天收尾的连续 3 天应满足`），并且 HEAD `7c07edf9d` 的 pristine 快照同样 FAIL、`TZ=UTC` PASS——三重对照见 §7.4。

### 7.2 `calendar_days_test.go` 的 tzdata 硬约束（**已修**）

```
$ node tests/validate-runtime-tzdata.mjs
Runtime timezone validation passed.        # exit 0
```

文件现在走 `timezone.Load("America/New_York")`（`calendar_days_test.go:18`）。镜像不带系统 tzdata，这是部署硬约束，不走 `time.LoadLocation`。

### 7.3 `account_age` ±1 天（**已修**，且有变异证据）

实现现状：`accountAgeSQL` 只取 `created_at`，日期折算全部在 Go 侧 `calendarDaysBetween`（各自折算成该时区日历日 → UTC 午夜相减，DST 安全，负数夹 0），复用 resolver 唯一的 `nowTime()`。

**我做的变异验证**（证明这条回归测试真的有牙，不是摆设）：把工作树快照复制到 `/tmp/qa-age`，只把 `loadAccountAge` 的折算改回"左侧 Go 本地日、右侧 PG 会话时区（UTC）日历日"的旧语义，再跑常驻回归：

```
=== RUN   TestAccountAgeCountsProcessLocalCalendarDays
    account_age_timezone_integration_test.go:86: 账户年龄应为 10 个进程本地日历日，得到 11（旧实现把会话时区的日历日 2026-11-02 拿来相减，会给 11）
--- FAIL: TestAccountAgeCountsProcessLocalCalendarDays（三个子用例全红）
--- PASS: TestAccountAgeNeverNegative
```

改回正确实现后三个子用例全 PASS。**一处精度说明**：我的变异把"右侧"固定成 UTC（等价于会话 UTC 的旧行为），所以连"会话与进程同为 Asia/Shanghai"的对照子用例也红了；若严格复刻旧 SQL（会话时区随子用例变化），预期是前两个子用例红、第三个绿。结论不变——断言能判红，且能报出 11 这个具体错值。

### 7.4 门禁终态与一次假红（保留，别让后人误读）

- **第一次**（04:27）：`Gate failed: api`，唯一红包 `app/Models/Badges/conditions`（两条时区 fixture）→ 见 §7.1。
- **第二次**（10:55）：api 144 包 ok / 0 FAIL（388s），但 **static 线红**（`calendar_days_test.go` 用 `time.LoadLocation`）→ 见 §7.2。
- **第三次**（11:15）：api 线红，失败项是 `cmd/sforum` 的 CLI 构建报 `undefined: validProtocolV2QueryIdentity` 等 7 个符号——**那是与并发编辑撞车的假红**：同一时刻 `go build ./...` 在当前树上通过，`v2_query_postgres.go`（tracked）里符号齐全。门禁自身不会告诉你"别人正在改文件"，所以这类红必须复跑澄清，不能直接当本刀 FAIL。
- **澄清用的复跑**：`./scripts/test-api-isolated.sh ./...` → **144 包 ok / 0 FAIL / exit 0**（当前树，跑于 11:22–11:29）。这一条否掉了第三次那条假红，也解释了它：`v2_query_postgres.go` 现在编译通过，七个符号齐全。
- **三线各自终态**：api = 144 包 ok（388s，run 2；并由上面的全量复跑再次确认）；web = `bun test` 2631 tests / 0 fail + typecheck 通过（run 3，22.43s）；static = `Admin framework validation passed` + `Runtime timezone validation passed`（run 3，14s，exit 0）。
- **终态（一次跑满三线）**：
  ```
  =========================== gate summary ===========================
  PASS  api     347s  tests/.logs/api.log
  PASS  web      54s  tests/.logs/web.log
  PASS  static   17s  tests/.logs/static.log
  All gates passed.
  ```
  即 run 3 的 api 红确认为并发假红：同一棵树、同一命令，静下来重跑即全绿（api 347s / web 54s / static 17s）。

## 8. 验收脚本复跑（我自己跑的两轮）

| 跑法 | 我的实测 | 备注 |
| --- | --- | --- |
| 裸跑（无 `DATABASE_URL`） | **ok 20 / FAIL 0 / pend 53** | 与后端转述的 16/0/57 有 4 条差值（他们都已 0 FAIL）。差值来自本机 env 与 task-7 落地时点不同，我按**自己跑出来的数字**记 |
| 带 `DATABASE_URL`（dev 库） | **ok 78 / FAIL 0 / pend 9** | 与后端转述一致；上一轮的 3 条 FAIL（旧迁移版本 `202611120002` + 两条时区 fixture）已全部消失 |

## 9. 残留清单（分两类，别混写）

### A. 本刀遗留的技术债（都是"能用但不理想"，需单开刀）

| # | 残留 | 影响 | 建议 |
| --- | --- | --- | --- |
| R1 | `unavailable` 与"候选确实为空"共用同一句提示（`refManualHint`：「候选暂时读不到，请直接填写标识」） | 正常状态被说成故障 | 按 `group.unavailable` 分两句 |
| R2 | 未知 `refKind` 静默退成文本框；`refCandidatesInvariantBroken` 只检查"响应里有没有数组" | 插件把 `refKind` 拼错时只有"候选读不到"，没有任何目录诊断 | 目录声明的 kinds ⊆ 响应 kinds 的比较，或给未知 kind 一条可见诊断 |
| R3 | 手动徽章的 `rule-impact` 返回 422 → 「手动 → 自动」这条最常见路径上弹窗必然显示「影响面估算获取失败」 | 噪声，容易被读成功能坏了 | 手动徽章直接返回 `estimated:null, method:"unsupported_condition"`，或前端对"无规则"不请求 |
| R4 | 手动徽章只给 `rule` 不给 `grantMode` → 422 **泛码** `forum.badge_rule_invalid`（文案「请检查指标与阈值组合」） | 真实原因是状态冲突，修法完全不同（plan §13 #19 同族） | 新增 `forum.badge_rule_mode_conflict` 之类的独立码 |
| R5 | 覆盖缺口：A3-b「真改表注入端到端」、A5-c「长列表滚动到末项」未覆盖（候选 11 条不触发滚动） | 证据缺口，不是功能缺口 | 造 >500 候选（或把 `RefCandidatesLimit` 调小做一次性验证）后补测 |

`:clear="false"` 使已选值不可清空**不列为技术债**：实测字段内没有任何清除控件，这是显式设计选择（A5-g 已记录），改值路径是"改选另一个候选"。

### B. 本刀挖出、但已修的历史缺陷（不是本刀引入，别当成新坑）

| # | 历史缺陷 | 谁修 | 我的复核 |
| --- | --- | --- | --- |
| H1 | `identity.account_age` 永久多算 1 天（Go 本地日 − PG 会话日），注册落在偏移窗口的用户门槛提前满足；同一根因下 `conditions` 两条时区 fixture 每天本地 00:00–08:00 恒红 | task-5 | 三时区全绿 + 变异证明回归测试能报出 11（§7.3） |
| H2 | 路由计数 tripwire 423：`core.route.badges.admin_badge_rule_text` 早就在 identities/`core_catalog_gen.go` 里，`registry_test.go` 漏改，导致 HEAD 上 `go test ./app/Support/Routes/` 本身红 | 老后 | `go test ./app/Support/Routes/ -count=1` → `ok 1.709s`，`registry_test.go:525` 断言 423 |
| H3 | 验收脚本三处腐坏：X-02 手工账（`expected=418/421`）、M1.1-01 钉死旧迁移版本、以及 §13 #22 记录的"脚本调用当前 store 导致签名/语义漂移" | 老后 | 带库复跑 `FAIL 0`（§8），三处 FAIL 全消 |
