# 信任等级 — 验收矩阵增量：D34 基线收窄（N 组）

Owner: pm（阿策）
Status: **草案，待实施**（2026-11-11，D34 已裁决；本文件只定义 N 组，不重排既有编号）
基线：`HEAD = 368434f25`（`git rev-parse HEAD` = `368434f258b8760e7f17bf549ba3aa596d566666`）
前序：`2026-11-11-trust-levels-acceptance-matrix-v3.md`（**唯一编号源**，A–M 共 150 条：149 + M-01 单列）、
`2026-11-10-trust-levels-acceptance-matrix.md`（v2，A–I 组定义源，366 行）；
决策：`../decisions/2026-11-11-trust-level-baseline-narrowing.md`（D34）；
计划：`../plans/2026-11-11-trust-levels-baseline-narrowing.md`（§1 死锁、§4-bis 裁决）；
返工面清单：`storage/qa/trust-levels-z3/inventory-f-group.md`（117 行，researcher，采集 2026-11-11T11:17:58）。

## 0. 本文件与既有矩阵的关系

| 事项 | 处理 |
| --- | --- |
| 编号 | N 组**新增**，不与 A–M 重排。v3 的 150 条继续由 v3 定义 |
| 证据规则 | 沿用 v3 §1.3 的 R6 / R7 / R10 / R11 / R13 / R14 / R15 / R16，本文件不新造规则 |
| 前置与命令 | 沿用 v3 §1.1（`API` / `EV` / `Q()` / `login()` / `call()`，jq 一律 `(.data // .)`）与 §1.2 账号矩阵 |
| 新增证据目录 | `storage/qa/trust-levels-z3/N/`（**约定命名**：`ls` 该目录当前报 not found，首条证据落盘时创建） |

### 0-1 v3 §4 有一句被本刀修正（先读）

v3 §4 标题是「默认安装下『TL 解锁能力』是空操作，这是正确行为」，判据是「配置生效 ≠ 行为变化」（R8）。
本刀后要分两半看：

- **仍然成立**：`Actor.Earned` 在默认安装下**恒为空**（D34 不往 `DefaultPolicy().Earnable` 写任何键，D17 不动）。
- **已被 D34 推翻**：由「Earned 恒空」推出的「默认安装下 TL0 与 TL1 权限集合相等」不再成立。
  TL0 少两个键，这是**装载期减法**的结果，不是 earnable 配置的结果。

⇒ **R8 对 N 组不适用**。N 组断言的正是**行为**（能不能开主题 / 传附件），因为行为第一次真的变了；
把 N-02/N-03 写成「只断言配置生效」等于把本刀唯一的交付物断言掉。

### 0-2 「基线实测」列的证据来源（R13 第四项）

N-01..N-04 的基线值来自 backend 的 `storage/qa/trust-levels-z3/D34-01-deadlock-red.txt` 第 2 条
（无 `post.create` 的 TL0 actor → `identity: permission denied`，同输入对照组 `<nil>`；采集
2026-11-11T11:16:29~11:17:07）。**该证据只证明机制，没有实测「今天 TL0 能开主题」**，
所以 N-02/N-03 的「FAIL（今天能）」是**机制推演**（TL0 今天持 7 键 ⇒ 按 D24 白名单必然能开主题/传附件），
**不是已实测结论**；转成实测后必须回填采集时间。

### 0-3 D 组那 5 条不是回归，是前提消失

D9 / D1 / D2 / D3 / D10 的旧判据都建立在一个前提上：**甲（member，TL0）能发主题**。
D34 改变了这个前提，于是旧断言从 PASS 变红。**这不是「本刀弄坏了」，而是「本刀换了语义，旧判据的地基没了」。**
不写清这一点，下一个人会把 D1–D3 的红当成回归、把 D10 的绿当成通过。

## 1. 编号分配

| 段 | 归属 | 内容 |
| --- | --- | --- |
| **N-01..N-06** | backend 任务书 | 四条验收门（N-01..N-04）+ 双 loader 一致（N-05）+ 头像上传（N-06） |
| **N-07..N-12** | pm（本文件） | F2 重写、D9 重写、D1/D2/D3 连带、D10 硬编码修复、F3 三份收口、关态权限基线复原（新增） |
| **N-13..N-15** | backend 任务书剩余三条 | bot 不受影响、显式 allow 不被扣、合并点唯一守卫（**编号为本文件所补**，见 §7-1） |
| **N-16** | pm（本文件） | 基线键门控的 UI 形状 + 存值原样回写（**取代 J-05**，见 §6-1） |

## 2. backend 验收门（N-01..N-06）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **N-01** | 默认安装、账龄 <7d、零活动的 **TL0 会员能发一条回复** | backend | **无现成测试**（本刀新增）。拟名 `TestD34TL0CanStillComment`，走真实 `Forum.Service.CreateComment`，断言 `error == nil` 且评论行落库 | 变异「把扣除集合扩到 `post.create`」→ 红在点名句（`want nil error, got identity: permission denied`） | `N/N-01.txt` + `N/N-01-red.txt` |
| **N-02** | 同一个人 **不能开新主题**（`topic.create` 被扣） | backend | **无现成测试**（本刀新增）。拟名 `TestD34TL0CannotCreateTopic`，**必须走真实 `Forum.Service` 调用路径**（不是断言 map 里没这个 key），断言 `errors.Is(err, identity.ErrPermissionDenied)` | 变异「去掉 `TrustLevel < LevelMember`」→ 红在点名句（`want permission denied, got nil`） | `N/N-02.txt` + `N/N-02-red.txt` |
| **N-03** | 同一个人 **不能传附件** | backend | **无现成测试**（本刀新增）。拟名 `TestD34TL0CannotUploadAttachment`，同上走真实 service 路径，断言 `ErrPermissionDenied` | 同 N-02 变异 → 红在点名句（`want permission denied, got nil`） | `N/N-03.txt` + `N/N-03-red.txt` |
| **N-04** | 过了新人窗口（账龄 >7d，TL1）**三个键全部回来** | backend | 档位判定用既有 `T: ^TestLevelAgeGate$ @ app/Models/TrustLevels`（已实测存在）证明 `LevelOf(8d)=TL1`；三键回到 `Actor.Permissions` 需**新增**拟名 `TestD34TL1RegainsBaselineGrants` | 变异「闸口写成 `<= LevelMember`」→ 红在点名句（`want topic.create present`） | `N/N-04.txt` + `N/N-04-red.txt` |
| **N-05** | **装载路径一致性**：`LoadActor`（L1）与 `LoadEffectiveActorTx`（L4）对**同一用户**给出**相同的键集合**，TL0 与 TL1 各测一次 | backend 落 / **reviewer 实测** | **待 backend 落地后由 reviewer 实测**（**不记 `pend`**）。范围源 = `storage/qa/trust-levels-z3/inventory-loaders.md`（106 行，11 条产出路径）：断言范围 L1/L2/L3/L4/L7/L9/L10；L5（`postgresTxStore.LoadCurrentUserAccess`，注释自认不合并 earned）与 L8（CLI 无 `WithTrustLevelSource`）是**既有缺口**、单列残留。前提是 backend 已修 L4 的 `CreatedAt` 未赋值缺陷（工作树 `actor_postgres_tx.go` 已改成 `SELECT status, kind, created_at` + `Scan(&actor.CreatedAt)`） | 修前形态就是红态：同一 TL0 用户在 L1 得 5 键、在 L4 得 7 键 → 红在点名句（`want equal key sets, got L1=5 L4=7`）。这条红是「插件命令路径把零值当注册时间」的机械判据 | `N/N-05.txt`（L1/L4 × TL0/TL1 四组，附 `git show` 的 `CreatedAt` 赋值行） |
| **N-06** | TL0 **不能上传头像** | backend | **无现成测试**（本刀新增）。拟名 `TestD34TL0CannotUploadAvatar`：`Profile/service.go:497 @ 368434f25` 用 `PermissionAttachmentUpload` 鉴权，与 N-03 同键；断言 `ErrPermissionDenied`。**这是已知有意副作用**，不是缺陷——要写进 release note | 变异「头像路径改用另一个键」→ 红在点名句（`want permission denied, got nil`）；反过来说，这条若因「头像不该受影响」被改成绿，等于给减法门开了后门 | `N/N-06.txt` + `N/N-06-red.txt` |

## 3. 语义变更连带（N-07..N-12，pm）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **N-07** | **F2 重写**：关开关 → `GET /trust-levels` **404**；同时 `Earned` **不再是空集**（关态 = 不做减法，七键全在） | pm 改文本 / ops 重采 | 404 半条沿用既有 404 断言（脚本 `:2038` 的真 F3 同批输出）；「不再恒空」用**正向行为观测**：关态同一 TL0 账号发主题/传附件成功（201），开态 403。佐证测试 `T: ^TestTrustLevelDisabledSentinelsAreIdentical$ @ app/Models/Identity`（已实测存在）需按新语义复核 | 实现若「关态也扣」→ 关态发主题 403，红在点名句（`want 201, got 403`）；**证据文件反向**：`storage/qa/trust-levels-flag-off-evidence.txt` 的 `earned=empty` 是**由 403 反推**的（`storage/qa/fixture-trust-earned.sh:323`），基线复原后夹具必 200/201，该文件整体作废。**不许改判据去迁就旧文件**，必须重采 | `N/N-07.txt` + `N/N-07-red.txt`；旧 `storage/qa/trust-levels-flag-off-evidence.txt` 标注「作废，勿引用」 |
| **N-08** | **D9 重写**：甲（member，TL0）**不能**发主题（403 `identity: permission denied`）；`T: ^TestLimitsForActorKeepsFiveCriteria$ @ app/Models/Forum` 仍绿（限额执行点未变） | pm 改文本 / backend 复核 | 旧文本见 v2 `:181`（「甲仍能发主题 `Can(topic.create)`=true」）。新断言拆两半：① 权限面 403；② 限额面测试名照旧、实跑必须绿 | 旧证据 `D/D9.txt` **作废**：它证明的是旧期望，继续留在目录里就是「用旧期望判新设计」的第一条假 FAIL。新红态：把减法关掉 → 甲能发主题 → 红在点名句（`want 403, got 201`） | `N/N-08.txt`（覆盖 `D/D9.txt` 的语义，旧文件标作废） |
| **N-09** | **D1/D2/D3 连带**：甲连发两次 → 429 `forum.topic_cooldown`；当日第 4 帖 → 429 `forum.daily_topic_limit`；含 `http://` → 拒 | ops 换账号重采 | **前提已消失**（甲发不出主题 ⇒ 拿不到 429）。处置：把三条的被执行账号从甲（TL0）换到**TL1 账号**——账龄闸内满足 requirements 也能到 TL1（`TrustLevels/level.go:21-23 @ 368434f25`），所以 481 造够活动即 TL1，不必等 7 天。**不许**把断言改成「TL0 也能触发 429」（那要绕过 403，等于测另一个东西） | 若只换账号不改证据头 → 红在点名句（`want 429 forum.topic_cooldown, got 403 identity: permission denied`）——这条红本身就是「旧前提没换掉」的机械判据 | `N/N-09.txt`（覆盖 D1–D3 三条证据的账号字段，旧 `D/D1..D3.txt` 标作废） |
| **N-10** | **D10 硬编码修复**：关开关后「TL 限额阶梯不参与判定」的判定必须**带条件**，且必须引用开态基线；开态基线缺失时**不许**判 ok | backend（写权在脚本） | `scripts/verify-trust-levels-acceptance.sh:2047-2050` 现在是 `c_finish D10 D ok '…'`，**不带任何条件 → 今天永远绿**。改成：开态 D1–D4 有 ok 证据时才判 D10；否则 `pend`/FAIL。红态：故意让开态基线缺失（或把 D1 判红）→ D10 必须红在点名句（`开态基线缺失，diff 无对照物`） | 现形态是第七条「让绿变假的方式」：**它绿着掩盖 D1–D4 全红**。修完必须同时落 `-red`（基线缺失时红）与 `-green`（基线齐时绿） | `N/N-10-red.txt` + `N/N-10-green.txt` |
| **N-11** | **F3 三份收口**：同一个 ID 的 404 断言只允许**一份**权威实现；三处（F3a 矩阵行 / F3b 脚本静态段 / F3c 脚本运行段）任一处单独绿**不算绿** | pm 定口径 / backend 改脚本 | 三处实名：F3a = v2 §3 F 组第 3 行（证据 `F/F3.txt`）；F3b = `scripts/verify-trust-levels-acceptance.sh:1152-1154` 的 `expect_present 'F-3 …'`（只 `grep -rqE "features\.trust_levels\|trustLevels"`，**grep 到任何一处开关字符串就通过**，既不验 404 也不验限额两态）；F3c = `:2038` 的真 404 断言 | F3b 是**假绿源**：把开关消费点全删掉它也绿，把 404 改坏它也绿。判据：**删掉全部开关消费点 → F3b 必须红**（它现在不会）；同时 404 改坏（返回 200）→ F3c 必须红在点名句（`关开关后公开端点应为 404，实得 200`）。F3a 的文本不得再指向 `F/F3.txt` 单份证据 | `N/N-11-red.txt` + `N/N-11-green.txt`；`F/F3.txt` 由 F3c 输出覆盖 |
| **N-12** | **新增：关闭 = 权限基线复原**。关态同一 TL0 账号的 `topic.create` / `attachment.upload` **两个动作都成功**（201），开态同一账号 403——**双向对照**，不是「和以前一样」 | ops | **`pend（集合等价半条）`**。行为半条可先落：`storage/qa/fixture-trust-earned.sh` 已有 403/201 观测能力，改成两态 × 两动作的矩阵输出。**逐字节集合等价半条 pend**：目前没有返回「当前 actor 全量权限集合」的端点（`GET /trust-levels` 的 `viewer` 只给 level/bottleneck），解除条件 = 找到该端点或夹具能读出有效键集合；解除前**不许记 ok** | 实现成「关态也扣」→ 关态 403，红在点名句（`want 201, got 403`）；实现成「开态不扣」→ 开态 201，红在点名句（`want 403, got 201`） | `N/N-12.txt`（行为半条）+ `N/N-12.md`（pend 条件与解除条件） |
| **N-16** | **基线键在矩阵里没有勾选框，只有同尺寸静态门控标记**；**可操作键 = 5、分母 = 5×4**；且**存值原样回写**（加载时某档含基线键 → 保存 payload 仍含它，API 回读一致） | frontend 落地 / **reviewer 浏览器实测** | ① 形状：S2 单元格内**不存在** `input[type=checkbox]` / `button` / `[tabindex]`，只有 `data-sf-earnable-gate` 静态标记（16×16，与勾选框同槽位）；可操作勾选框总数 = **20**（5 键 × TL1–TL4），全表 7 键 × 5 档 = 35 格。② 现有两条前端用例**必须重写**（它们断言的是「勾选框的 aria-label 带门控语义」，控件消失后用例跟着改）：`carries the gating semantics into the checkbox accessible name`、`keeps TL0 disabled for baseline keys too`（`apps/web/tests/admin/settings/trustLevelEarnableMatrix.test.ts`）。③ 回写：读面字面集合已有 `T: ^TestAdminBaselinePermissionsLiteral$ @ app/Http/Controllers/TrustLevels`（实测存在）；**存值往返需新增**拟名 `TestD34BaselineKeyRoundTripsThroughSave`——某档 `earnable` 含 `topic.create` → PUT 同 payload → 回读该档 `earnable` 仍含它 | ① 形状红态：把 S2 改回勾选框 → 计数 20→28（7×4），红在点名句（`want 20 operable checkboxes, got 28`），**不许只红在分母常量**；② 回写红态：保存路径若「自动剔除基线键」→ 回读缺 `topic.create`，红在点名句（`want earnable to still contain topic.create`）。**②比①严重：它是静默改写运营配置** | `N/N-16.png`（≥1280 与 390×844 各一张）+ `N/N-16-dom.txt`（计数、`aria-label`/`title`、无 `tabindex`）+ `N/N-16.txt`（往返 payload 与回读）+ `N/N-16-red.txt` |

## 4. backend 任务书剩余三条（N-13..N-15）

| ID | 断言原文 | 谁验 | 怎么验 | 红态形态（R10） | 证据落点 |
| --- | --- | --- | --- | --- | --- |
| **N-13** | **关闭开关 → 七键全在**（不做减法）：`features.trust_levels=disabled` 时 TL0 仍能开主题 | backend | **无现成测试**（本刀新增）。拟名 `TestD34DisabledFlagKeepsSevenKeys`：开关关 + TL0 actor → `Can(topic.create)` 为真，且七键集合与基线逐键相等 | 变异「删除 `TrustLevelResolved` 判断（无条件扣）」→ 红在点名句（`want topic.create, got false`） | `N/N-13.txt` + `N/N-13-red.txt` |
| **N-14** | **显式 allow 不被扣**：给 TL0 用户加 `user_permission_overrides(topic.create, allow)` → 他能开主题 | backend | **无现成测试**（本刀新增）。拟名 `TestD34ExplicitAllowSurvivesDeduction`：只加 override，断言 `Can(topic.create)` 为真 | 变异「删掉『不扣 allow 覆盖』」→ 红在点名句（`want true, got false`）——backend 任务书点名的第二组变异 | `N/N-14.txt` + `N/N-14-red.txt` |
| **N-15** | **bot 不受影响**：bot actor 仍能开主题 | backend | **无现成测试**（本刀新增）。拟名 `TestD34BotSkipsBaselineDeduction`：`Kind.IsBot()` 为真且 `TrustLevel < LevelMember` 时仍保留 `topic.create` | 变异「删除 `Kind.IsBot()` 分支」→ 红在点名句（`want true, got false`） | `N/N-15.txt` + `N/N-15-red.txt` |

## 5. 判据细节

### 5-1 N-02/N-03 必须走真实调用路径，且必须有对照组

**N-02/N-03 必须走真实 `Forum.Service` 调用路径**，不是断言 map 里没这个 key；
**N-01/N-04 就是它们的对照组**。

理由（写进判据，别当备注）：一个「一律扣除」的坏实现会让 N-02/N-03 **照样绿**——它们只断言「不能」。
没有 N-01（TL0 能回帖）与 N-04（TL1 三键回来），「扣得对」与「扣得过头」在证据里长得一样。
与上一刀 L-06 同形：**只看一半的断言，红和绿都不算证据。**

### 5-2 N-05 为什么不是 `pend`

`pend` 在上一刀的语义是「**环境限制**，不是欠账」（v2 D2 / I-05：缺凭据、缺端口）。N-05 不属于那类：
它有明确修复路径——`LoadEffectiveActorTx` 把 `CreatedAt` 传成零值（`inventory-loaders.md` §2 结论 1），
账龄恒过闸 ⇒ 插件命令路径永远不低于 TL1 ⇒ 同一用户在两条 loader 上两个答案。

- 现状：backend 已在工作树修（`SELECT status, kind, created_at` + `Scan(&actor.CreatedAt)`），
  所以 N-05 的措辞是「**待 backend 落地后由 reviewer 实测**」。
- 修前形态本身就是红态（L1 5 键 vs L4 7 键），**不需要另造变异**。
- L5 / L8 是既有缺口（不合并 earned / CLI 无 trust source），**不算 N-05 变红**，单列残留；
  它们在本刀下表现为 fail-open（TL0 多键），不是挡人。

### 5-3 N-15 的红态必须停在点名句

L-06 的 A 变异证明过：「总数不变、只改坏一个 ID」才能让点名断言先于计数断言执行。
N-15 同理：变异**不能只让副本数量从 1 变 2**，红输出里必须是「第二份实现在哪个文件哪一行」那句，
而不是 `count = 2（期望 1）` 这种只有数字的红。

### 5-4 F6 **不扩展**（推翻 researcher 的建议，理由是实现机制变了）

`inventory-f-group.md` §1 F6 行建议「若 Z3-b′ 把『TL1 earnable 默认 = 2 键』写进 `DefaultPolicy()`，
F6 应扩展为同时比 `earnable` 默认值」。**该前提在 D34 下不成立**：D34-b 第 1 条明确
`BaselineGrants()` 是**代码常量、不进** `DefaultPolicy().Earnable`。所以：

- F6 保留原样（只比 `requirements` 默认值）。
- 「TL1 默认拿到两键」这条新默认值由 **N-04** 断言，不靠 F6 扩展。
- 若有人后来把两键写进 `DefaultPolicy().Earnable`，F6 与 N-04 会**同时红**——那是正确的信号（D17 被违反）。

### 5-5 E6 的分支提醒已由 D34-b 关闭

`inventory-f-group.md` §4 E6 行留了分支：若实现选择「resolver 在 disabled 时返回 TL1 grants」，
则 `Identity/trust_levels_disabled_silent_test.go:32`/`:43` 的 `ok==false` 与 F2 佐证测试都要改口径。
**D34-b 选的是 identity 侧「不做减法」**，resolver 不动 ⇒ E6 保留、两处 `ok==false` 不动、F2 佐证测试保留。
这条写下来是为了避免下一个人再推一遍。

## 6. 本组不覆盖（避免把矩阵绿冒充仓库绿，R16）

- **不覆盖** `./scripts/test.sh --fast` 三线：N 组全绿也不构成可提交依据。
- **不覆盖** D20（`forbid_attachments` 死配置）：减法门实现了它想表达的行为，但那条 option 仍无消费者。
- **不覆盖** D24 白名单封闭性：白名单仍 7 键，`eligiblePermissions` 形状不变，J-01/J-02 原样有效。
- **不覆盖** F4/F5/F7/F8/F9 与 v3 增量 25 条：`inventory-f-group.md` §5 判定 0 条要动，本文件不重开。
- **不覆盖** 页面布局与文案本身：N-16 只覆盖矩阵单元格控件形态与存值回写；badge/文案细节由 designer 与 frontend 对拍（`storage/qa/trust-levels-z3/ui-spec-gated-keys.md`）。
- **不覆盖** 管理面 `GetUserUploadPolicy` 的等级联动（`inventory-loaders.md` §3-3 列为未确认）：本文件裁决为**正确行为**——管理面展示的是该用户的**有效**权限，必须与他自己动手时的结果一致；为它设例外＝造第二个权限答案。

### 6-2 M-02（residual，本刀不修）

| ID | 断言 | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- | --- |
| M-02 | **residual（本刀不修）**：`POST /auth/register` 响应 `data.permissions` 与随后 `GET /auth/session` 的键集合**不相等**（差 2 个基线键 `topic.create`/`attachment.upload`） | rev | `H: H-09`（`storage/qa/trust-levels-z3/http-leg.sh`）——注册后立刻比对两处 permissions | `storage/qa/trust-levels-z3/http-leg/20261111-113613-matrix.txt`（H-09）+ 同目录 `-register-body.json` |

**为什么是 residual 而不是 FAIL（2026-11-11T11:36，reviewer 实测）**

- 实测：新注册账号 **491**（`created_at 2026-11-11 11:36:14`，`viewer.level=0`）的注册响应
  `data.permissions` = **8 键**（含 `topic.create`、`attachment.upload`、插件键
  `sforum.oauth2-provider.create_clients`）；同一秒 `GET /auth/session` = **6 键**（两键已扣）。
- 机制：注册响应由 `LoadCurrentUserAccess`（`inventory-loaders.md` **L5**）产出，该路径**不合并信任等级**；
  授权裁决不受影响（守卫走 `Can()`），失败方向是「UI 亮一个必然 403 的按钮」，不是越权。
- **本刀不修的理由**：修它要把 trust source 接进 `postgresTxStore`（L5），那正是本刀一直在防的
  「再加一条装载路径」。塞进来会把「合并点只有一个」变成两个。
- **这条断言的价值**：将来有人修 L5 时，它从「不相等」翻成「相等」，是唯一能证明修好了的东西。

> 与上一刀已提交的 `acceptance-matrix-v3.md` 的关系：M-02 是本刀新发现的残余，**只记在本文件**；
> v3 一个字节未动（`git status -- knowledge/reports/2026-11-11-trust-levels-acceptance-matrix-v3.md` 为空）。
> 本文件不把两刀的条目数相加——要表达「跨两刀的残余」，以上一句为准。

### 6-1 对 v3 J 组的处置（**不改历史**）

- `J-05 被 N-16 取代（本刀改了 UI 形状：7 个 disabled 勾选框 → 5 勾选框 + 2 标记），J-05 原文与证据保持原样，不再代表当前实现。`
- **J-10**（390×844 无横向溢出）**判据不变，但本刀重测**：单元格结构变了（勾选框 → 静态标记 + 窄屏格内标注），溢出必须重新取证。`J/J-10-390x844.png` 保留原样，新证据落 `N/N-16.png` 的 390 版，不覆盖旧图。
- v3 文件**零改动**：`git status -- knowledge/reports/2026-11-11-trust-levels-acceptance-matrix-v3.md` 为空，最后一次提交 `368434f25`。上一刀已提交的证据不得回溯编辑——要改口径只能在新矩阵里加「取代」行。

## 7. 待确认项（不猜）

1. **N-13..N-15 的编号是我补的**：lead 的编号分配只覆盖到 N-06（backend）与 N-07..N-12（pm），
   而「bot 不受影响 / 显式 allow 不被扣 / 合并点唯一守卫」三条也是 backend 任务书里的硬要求。
   本文件按「不重排、不丢项」原则补为 N-13..N-15；若 lead 有别的归并方式（例如并入 N-01..N-06），
   改号不影响内容，只需同步脚本 ID。
2. **N-12 的取证端点**未确认存在（`GET /trust-levels` 的 `viewer` 不给权限集）。行为半条可先落，
   集合等价半条按 N-12 行内的 pend 条件与解除条件处理。
3. **N-09 的 TL1 账号**尚未造数（481 现为 12h 账龄 + 零活动）：需先造够 requirements，
   且**不能**同时把 `trust.new_user_days` 设为 0（那会让 TL0 不可造，与 N-01/N-02/N-03 互斥，v3 §1.4 已记）。
