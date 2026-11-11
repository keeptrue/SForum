# 信任等级收尾（M9–M13）— M13 最终合并表

Status: **定稿（2026-11-11T10:01 +08:00）** —— 已与 pm 的矩阵 §10、ops 的 `_summary.txt` @ 10:00:56 三方交叉对账一致
作者：reviewer 小审
编号源：`reports/2026-11-11-trust-levels-acceptance-matrix-v3.md`（**149 条** = v2 124 + 本刀 25）
运维摘要：`storage/qa/trust-levels/_summary.txt` @ `2026-11-11T10:00:56`（`ok 121 · FAIL 0 · pend 2 · 未实现 0 · 作废 1`，覆盖 v2 124 条）
矩阵 §10 终值（pm）：`149 = ok 145 · FAIL 0 · pend 2 · 规则 1 · 作废 1 · 未实现 0` —— 与本表 §0 逐字一致
证据根：`storage/qa/trust-levels/{A…I,K,J,L}/`

合并口径（沿用 M8，不另发明）：
**reviewer 独立证据优先 → `_summary.txt`（矩阵 ID 口径）→ 两边都无则「未实现」+ 理由。**
每条带**证据类型**：`Go 断言` / `浏览器实测` / `变异红绿` / `静态链路` / `运维自动化`。

---

## 0. 数字对账（四类互斥，无空白行）

| 类别 | 数量 | 说明 |
| --- | --- | --- |
| ok | **145** | v2 121（114 + B7/F4/H15/I-22/I-23/I-03/I-04）+ v3 24 |
| FAIL | **0** | I-16 判据不可满足，已按 v3 §11 修订为 I-16a/b 并转绿 |
| pend | **2** | D2（日限额被冷却抢先）、I-05（无 TL4 账号） |
| 规则 | **1** | L-02（规则型：jq 必须落在 `.data` 层；没有也不该有 `Test*`） |
| 作废 | **1** | H13（D18 后由 H15 取代） |
| 未实现 | **0** | 本刀无欠账 |
| **合计** | **149** | 145 + 0 + 2 + 1 + 1 + 0 |

提交信息里请写：**149 条 / ok 145 / FAIL 0 / pend 2 / 规则 1 / 作废 1 / 未实现 0**。

> **算术对账**：145 + 2 + 1 + 1 = 149，与 pm 的矩阵 §10 逐字一致。
> 两边独立算：`_summary.txt` @ 10:00:56 给 v2 = **121 ok**（+ 2 pend + 1 作废 = 124），
> 本表给 v3 = **24 ok + 1 规则型 = 25**；121 + 24 = **145**。
> 此前口头出现过的 `ok 140` 与明细差 5，差的是 v2 里原本「未实现」、由 reviewer 证据补齐的
> 五条（B7/F4/H15/I-22/I-23）—— ops 的 10:00:56 版已按同一口径把它们记为 ok。

---

## 1. v2 124 条：四栏（§10 口径）

| 栏 | 数量 | 条目 |
| --- | --- | --- |
| 上一刀 ok（75）→ 本刀仍 ok | **75** | 全部复跑确认，无一条掉绿 |
| 上一刀 ok → 本刀 FAIL/变化 | **0** | L-03 只改错误路径（422 `validation.invalid` → 500 `options.store_failure`），成功路径未动；无成功路径证据变化 |
| 上一刀「未实现」（49）→ 本刀 ok | **47** | A11–A12、C13–C16、C20、D3、D10、E5、E6、F3、F5、F8、F9、G1–G6、H1–H3/H6–H12/H14/H16/H17、I 组（除 I-05）、B7/F4/H15/I-03/I-04/I-22/I-23 |
| 仍「未实现」/pend | **2** | 只剩 D2、I-05 两条 pend（环境限制）；I-03/I-04 已由 reviewer 证据补齐，H13 作废 |

**本刀由 reviewer 补齐的 7 条**（原「未实现」，现 ok）：

| ID | 断言 | 证据类型 | 证据 |
| --- | --- | --- | --- |
| B7 | catalog 来源是 Go 常量，不是正则扫迁移 | 静态链路 | `B/B7-review.md`（生成器 0 处 `migration`；守卫来自静态表；白名单纯字面量） |
| F4 | 关开关 → 资料页等级卡**元素不存在** | 浏览器实测 + 静态链路 | `F/F4-evidence.md`（先验前提 404，再断言 API 无 `trustLevel` + SSR 卡片计数 0） |
| H15 | 改 `Badges.ActivityCounters` 字段名 → `go build ./...` 必须失败 | 变异红绿 | `H/H15-red.txt`（`exit=1`，两处 undefined）+ `H/H15-green.txt`（`exit=0`） |
| I-22 | 恢复默认只回填、不自动保存 | 浏览器实测 | `I/I-22-evidence.md`（0 次 PUT、API config 逐字节未变） |
| I-23 | 390×844 无横向溢出 | 浏览器实测 | `I/I-23-390x844.png` + `scrollW=390=clientW` |
| I-03 | 匿名 `has("viewer") == false` | 静态链路（HTTP） | `I/I-03-reviewer.txt`（前置：admin 读面 200 证开关 enabled；匿名 200 + 无 `viewer` 键 + 5 档）。注：`I/I-03.txt` 是 ops 的登记文件，指向 `I-03-I-04-otherview.txt`（另一条断言），**两者不可互相替代** |
| I-04 | 甲/丙 登录后的 viewer 形状 | 静态链路（HTTP） | `I/I-04.txt`（甲 TL1 / 丙 TL0，`nextLevel=level+1`、`bottleneck` 与公开阈值一致；**先验 session=200**，过期会话会伪造「无 viewer」） |

---

## 2. v3 新增 25 条

> 本表「证据」列写的是**实际落点**（逐条 `ls` 过，R14）。J-01/J-02/J-09 的落盘缺口已于
> 2026-11-11T10:09:36 由 ops 补齐（`J/J-01.txt`、`J/J-09-red.txt`、`J/J-09-green.txt`），
> §8.1 可改「已闭环」。

| ID | 断言（摘要） | 结论 | 证据类型 | 证据 |
| --- | --- | --- | --- | --- |
| J-01 | `eligiblePermissions` 恰 7 键、字面集合相等 | ok | Go 断言 | `J/J-01.txt`（ops 10:09:36：`=== RUN` + `--- PASS`）；本人 09:28:50 亦实跑过 |
| J-02 | 顺序 = `EarnableWhitelist()` 顺序且两次读稳定 | ok | Go 断言 | 同 `J/J-01.txt`（同测试的断言 2） |
| J-03 | 勾 TL2 → 保存 → 重载勾选态保持；取消后回空 | ok | 浏览器实测 | `K/logs/browser-j03-2026-11-11-094543.json`（4/4） |
| J-04 | 白名单外无键可勾（UI）+ 构造请求 422（API） | ok | 浏览器实测 + Go 断言 | `K/logs/browser-j04-2026-11-11-094645.json`（列集合逐字相等、banned=0）+ `K/J-04-J-08-api.md`（庚 PUT `user.ban` → 422 `trust_levels.invalid_config`） |
| J-05 | TL0 行 `aria-disabled=true`、控件不可勾 | ok | 浏览器实测 | `K/logs/browser-assert-2026-11-11-094056.json` |
| J-06 | 累积披露文案 `innerText` 实读含「及以上」 | ok | 浏览器实测 | 同上（实读整句） |
| J-07 | 白名单 500 → 不可编辑 + 错误条，不回落空矩阵 | ok | Go 断言（组件用例） | `J/J-07.txt`：`bun test tests/admin/settings/trustLevelsTabMount.test.ts`（10:06:15，4 pass / 17 expect，带对照组）；头部含三个实现文件的 sha256 基线 |
| J-08 | `earnable: []` 往返仍为 `[]` | ok | Go 断言（HTTP） | `K/J-04-J-08-api.md`（PUT→200，重载 `[]` list len=0） |
| J-09 | 变异：白名单塞 `user.ban` → J-01 红，改回绿 | ok | 变异红绿 | `J/J-09-red.txt`（`controller_test.go:312: 长度 = 8` → FAIL）+ `J/J-09-green.txt`（撤 overlay → PASS），ops 10:09:36；本人 09:31/09:52 亦各跑过一次 |
| J-10 | 390×844 无横向溢出、7 列不裁切 | ok | 浏览器实测 | `K/logs/browser-assert-2026-11-11-094056.json`（`clipped=0`） |
| J-11 | 开关关闭：同一原因只允许一条 alert | ok | 浏览器实测 + Go 断言 | `K/logs/browser-j11-2026-11-11-094118.json`（disabled=1 / err=0）+ 组件用例「0 次请求」 |
| J-12 | 只 `admin.access`：矩阵不可勾、保存 disabled、PUT 403 | ok | 浏览器实测 + 静态链路 | `K/logs/browser-j12-2026-11-11-094727.json`（35 控件全 disabled、403 `permission.denied`） |
| J-13 | `Grants` 累积语义（正向含、反向不含） | ok | Go 断言 + 变异红绿 | `J/J-13-red.txt`（干净 PASS；把循环上界改 `LevelCount` → `TL1 = [post.create topic.create]` 红） |
| J-14 | `DefaultPolicy` + `SeedMemberPermissions` → 7 键 `GrantSource == static` | ok | Go 断言 + 变异红绿 | `J/J-14.txt`：`-list` 复核 `TestGrantSourceIsStaticForDefaultInstallWhitelistKeys`（09:58:16 PASS）；变异 `if a.Earned[permission] → if true` → 红「`topic.create` 的来源 = earned，期望 static」 |
| K-01a | `badges.*` zh/en 15/15 成对 | ok | 静态链路 | `K/K-01-badges-i18n.md` §1、`K/logs/K-01-keyset.txt` |
| K-01b | `admin.badges.*` 双向键集相等（missing=0 **且** extra=0） | ok | 静态链路 + 运行时解析 | `K/K-01-badges-i18n.md` §4：`used=defined=75`、逐个 `t()` `bad=0 / intlify_warnings=0` |
| K-02 | 徽章 13 条路由 tripwire 在 | ok | Go 断言 + 变异红绿 | `K/K-02-route-tripwire.md`（PASS + 删一条 → `count=408` 红 + `--check` 漂移；caveat 见该文件 §4） |
| K-03 | 徽章六错误码双语文案 | ok | Go 断言 | `K/K-03-error-codes.md`（zh `:61-66` / en `:415-420`） |
| K-04 | 资料页两个统计键在 payload 与 UI | ok | Go 断言（HTTP）+ 浏览器实测 | `K/K-04-profile-stats.md` §5（`likeReceivedCount=55 / daysVisited=1`；SSR 实读「获赞」+ `<strong>55</strong>`） |
| L-01 | M12-a 三个新 reason 有双语文案 | ok | Go 断言 | 09:27:04 `--- PASS: TestAPIErrorCodesHaveLocalizedMessages` |
| L-02 | 规则：读 HTTP 响应的 jq 必须落在 `.data` 层 | **ok（规则型）** | 静态链路 | `L/L-02-rule.md`：16 处 `HTTP_BODY`+jq 逐处分类（5 处 `(.data // .)`、9 处硬 `.data.…`、2 处深搜），**零处信封盲**；执行结果即 I-09/I-11/I-24 由假 FAIL 转 ok |
| L-03 | `TestOptionsErrorMappingHasSingleOwner` 当前绿 + 加回副本必红 | ok | Go 断言 + 变异红绿 | `L/L-03.txt`：干净 PASS（0.58s）→ 真实新增副本 `zz_dup_options.go` → **红**（实际 2 个 owner）→ 删除后复跑 PASS；现场已清理 |
| L-04 | 代码审阅：用例名与构造一致 | ok | 静态链路 | `K/L-04-review.md`（`:121` 名不副实且与 `:104` 重复；**函数是 `TestGrantSourceMatchesCan`（`:88` 起）**） |
| L-05 | `TestUpdateAdminStoreFailureIsServerError`：写库失败必须 500 | ok | Go 断言 + 变异红绿 | `L/L-05.txt`：干净 PASS；变异把 `updateAdmin` 的写库失败改回 422 → 红（`status = 422（期望 500）`） |
| L-06 | `TestQueryCatalogHTTPReturnsSealedCatalogForViewers` 复跑 | ok | Go 断言 | `L/L-06.txt`：`viewer` / `legacy_manager` 两个子用例 PASS（09:59:30） |

---

## 3. 五条「判据改对才闭合」：本刀最值钱的产出

这五条（I-16 / L-02 / K-01b / E5 / J-13+J-14）的**共同点不是产品错了，而是断言的前提从没被验证过**：
判据写下来的那一刻起就没人见过它绿，或者它注定不可能红。I-16 要求「关→开后首请求没有 viewer」，
可 `LoadActor` 每请求重装，那条判据**永远绿不了**；L-02 的断言指在信封外层，值对、位置错，
产出三条假 FAIL；K-01b 只查「键存在」不查「无多余」，于是死文案永远查不出来；E5 断言「`LevelOf`
不碰 store」，而纯函数根本不持有 source —— 断言了一个在任何实现下都成立的性质，与「断言自己的
前缀」同形；J-13/J-14 想在**默认安装**上观测 `earned`，可 7 键已被 `SeedMemberPermissions` 静态
授予，那个状态在生效集合里根本不可见。**这五条都不是靠改代码变绿的，是靠把判据挪到能失败的地方。**

验收层因此多了一条通用自检：**每条断言先问三句——它在什么实现下会红？它红的时候我信不信？
它绿的时候排除了什么？** 三句都答不出来，就不是断言，是装饰。这与 pm 附录 A 的四句自检同源
（尤其第四句「这条绿排除了什么」），下一刀请把这段读一遍，比逐行读 149 条表更快。

## 4. 未实现：本刀为 0；只有 2 条 pend（环境限制，不是欠账）

| ID | 类型 | 说明 | 解除条件 |
| --- | --- | --- | --- |
| D2 | pend | 日限额未测到：请求先被 `trust.new_user.topic_cooldown_seconds` 冷却拦下（429 被冷却抢先） | 置 `topic_cooldown_seconds=0` 后复跑 |
| I-05 | pend | TL4 视角形状（无 `nextLevel`/`bottleneck`）无法验：现库 `trust.new_user_days=7`，483/484/485 账龄 12h ⇒ 全部封顶 TL1 | ops 排期把 `trust.new_user_days` 设 0 后造 TL4 账号；与「造 TL0」互斥 |

`L-02` 是**规则型**，不是欠账：它的判据是「检查验收脚本本身」，没有也不该有 `Test*`；
执行结果已体现在 I-09/I-11/I-24 转 ok（证据 `L/L-02-rule.md`）。

## 5. 与 `_summary.txt` 的差异（必须解释，不能让人以为其中一份错了）

1. `_summary.txt` 只覆盖 **v2 124 条**；本表 **149 条**。差值 25 = J 14 + K 5 + L 6。
2. **两边的「未实现 0」现在一致**：`_summary.txt` @ 10:00:56 已把 v2 的 7 条（B7/F4/H15/I-03/I-04/I-22/I-23）记为 ok（采用本表的独立证据）；本表的 v3 5 条（J-14/L-02/L-03/L-05/L-06）为 4 ok + 1 规则型。此前 `_summary.txt` @ 09:50:24 曾记 `未实现 7`，那是收口前的版本，不要再用它做对账。
3. **`_summary.txt` 的叙述段已于本刀废弃，以明细表为准**（明细表 pend 只有 D2/I-05；叙述段里的 C-5/C-6/C-12/D-6/F-5 是旧版残留，不是失败项）。读者只需看明细表。
4. I-16：`_summary.txt` 最新版已 FAIL 0；v2 原判据不可满足的经过见 v3 §11，本表按修订判据记 ok。

---

## 6. 未在本表新增结论的地方（防止过度声称）

- 本表不声称「默认安装下 TL 解锁能力有行为变化」——它是**空操作**，见 v3 §4；J-13 只验 `Grants` 语义。
- 本表不声称 `GrantSource` 有生产消费者 —— 它目前**零消费者**（登记为 residual）。
- K-02 的 caveat 不变：Go 测试只锁总数 409 + guard 形状，逐条路由身份靠 `generate.mjs --check`（已用变异证明它会红）。
