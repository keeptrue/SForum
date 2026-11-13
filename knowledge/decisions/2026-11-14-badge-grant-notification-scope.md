# 决策：徽章授予通知的去重键改为收件人作用域（修订 D5）

日期：2026-11-14
状态：accepted
上游：`decisions/2026-11-13-badge-notification-and-celebration.md`（**修订其 D5 的键格式一句**，
其余 D1–D11 全部保留）、`decisions/2026-11-10-badges-and-user-activity.md`

## 背景：一条约束把「四条路径都发了通知」变成了空话

2026-11-13 的四条路径补齐本身是对的，但去重键写成 `badge:{slug}`，而
`notifications.dedupe_key` 是**全局唯一**（`202607110016_notifications_mail_deliveries.sql`
的 DDL，插入处 `postgres_store.go` 的 `ON CONFLICT (dedupe_key) DO NOTHING`）。
两个事实相乘的结果是：**全站第一个**拿到某枚徽章的人永久占住这个键，之后每个用户的投影
都命中冲突、静默返回已有行。

症状极难自查：授予行写成功、路径日志正常、收件箱空着、**零报错**。
dev 站实证（2026-11-14）：

| 徽章 | 自动授予人数 | 实际通知条数 |
| --- | --- | --- |
| `first-topic` | 13 | 1（recipient 479） |
| `topic-author` | 3 | 1 |
| `discussant` | 2 | 1 |

用户 1 于 2026-11-14 01:28:43 自动获得 `first-topic`，`notifications` 无对应行。

对照：其余类型一直自带收件人身份——`moderation-pending:{type}:{id}:{rev}:{reviewerID}`、
`moderation:{decisionID}:{kind}:{authorID}`、`comment:{commentID}:reply:{id}`、
`topic:{topicID}:mention:{id}`、插件 `plugin:sha256(extensionID‖idempotencyKey‖type‖recipientID)`。
徽章是唯一漏掉的一处。

## 裁决

### D12 键必须自证收件人：`badge:{recipientUserID}:{slug}`

`fanout.go` 的 `NotifyBadgeGrantedTx` 改用 `fmt.Sprintf("badge:%d:%s", event.RecipientUserID, event.BadgeSlug)`，
同一个键继续用于邮件 `IdempotencyKey`。

这不是「顺手加个字段」，而是**约束驱动的必然**：`dedupe_key` 全局唯一，任何不含收件人
（或等价收件人唯一标识）的键，在多收件人事件上都必然塌缩成一条。**新增通知类型时这条是
硬约束**——模块笔记里写成判据，而不是备忘。

### D13 语义不变：同一用户同一徽章终身一条

键里多了一个维度，但「收回后再授予不重复通知」的终局荣誉口径保持原样：收回不删通知行，
键仍被占用，重新授予仍命中冲突。**不改成按授予行 id 去重**（原 D5 的结论仍然成立：
那会把「授予是否新建」与「通知去重键」耦合进 `GrantAutomatic` 的签名）。

### D14 历史行改写键，但**不补发**被吞掉的通知

迁移 `202611140001_badge_grant_notification_dedupe_scope.sql` 只做一件事：把
`type='badge_granted'` 且键等于 `'badge:' || payload->>'badgeSlug'` 的行改写成新格式。

- 判据用等值而不是 `LIKE 'badge:%'`：后者会把已是新格式的行也一起改写，且 slug 含冒号时
  按位置切分会切错。payload 里 `badgeSlug` 一定存在（`NotifyBadgeGrantedTx` 写入），
  但空值仍用 `payload ? 'badgeSlug'` 兜住。
- **不补发**：与庆祝层 D8「首访不回放历史」同一口径。补发会让老用户一次收到十几条几个月前
  的徽章通知，比「没收到」更糟；而且补发要伪造 `created_at`，等于篡改事件时间。
- Down 故意为空：反向退键会让同一徽章的多条通知撞全局唯一约束，只能靠删他人通知完成回滚。
  代码回退是安全的——旧代码按 slug 去重会把新键行视为另一条，最坏结果是个别用户再收到一条
  授予通知，不会产生死链或重复行。

### D15 `mail_deliveries` 的 idempotency_key 不回改

那张表没有 user 维度，旧键 `badge:{slug}` 与新键 `badge:{id}:{slug}` 结构上不相撞；
回改只会制造一批无意义的行更新。

### D16 规则回扫是第五条授予路径，必须发通知

`bootstrap/badges_wiring.go` 的 `badgesRuleBackfillRunner` 早期丢弃 `EvaluateUser` 的返回值，
于是「保存规则 → 回扫补授予」这条运营最常用的批量路径全程静默（`Recompute` 会通知，回扫不会，
两者在后台看起来都是「补授予」）。

- 逐用户批量通知：一次 `NotifyBadgeGranted` 带该用户本页全部新授予，减少出口调用次数，
  **不改变投影粒度**（仍是一枚徽章一条通知行，与原 D5 的「按用户聚合只减少调用次数」同口径）。
- **通知失败只 warn、不中断本页**，理由比 D2 更硬：授予已落库，上抛会让 river 重跑整页，
  而重跑时 `EvaluateUser` 因已持有而返回空 → 通知**永远不会再发**，错被重试洗掉。
- **评估错误仍上抛**：授予状态未知，重试是安全且必要的（授予幂等）。
- **不设数量上限**：回扫是异步分页（`DefaultBackfillBatch = 500`），`Recompute` 的 200 上限
  是同步请求的保护，二者语义不同，不要合并成一个常量。
- 装配守卫：`badges_wiring_test.go` 断言回扫 worker 的 runner 带非 nil 的
  `users`/`evaluator`/`notifier`，且 notifier 复用调用方传入的同一个 outbox（两套 outbox 会让
  渠道偏好与品牌解析各读一份配置）。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| 把 `dedupe_key` 的唯一约束改成 `(recipient_user_id, dedupe_key)` | 全表约束变更 + 所有既有类型都要复核；而既有类型本来就带收件人，改约束等于为了一处 bug 动全局不变量 |
| 键里放 `user_badge_id` | 要改 `GrantAutomatic` 签名；且收回再授予会重复通知，与「终局荣誉」冲突（原 D5 已否） |
| 补发被吞掉的历史通知 | 老用户被几个月前的徽章刷屏；且要伪造 `created_at` |
| 回扫通知失败时上抛让 river 重试 | 重跑时授予已存在 → 通知永远发不出去（见 D16） |
| 回扫沿用 `Recompute` 的 200 上限 | 异步分页没有同步请求的保护需求；上限只会让「谁被通知到」取决于扫描顺序 |

## 陷阱

**T1 「授予成功」被当成「通知已发」**
现象：运营批量授予后用户说没收到。
根因：`ON CONFLICT (dedupe_key) DO NOTHING` 让冲突完全静默，`CreateNotificationTx` 返回的是
既有行、`created=false`，调用方拿不到任何信号。
判据：查 `notifications` 行的 `dedupe_key` 是否带收件人；不含收件人的键在多收件人事件上
必然是 bug。

**T2 用「通知条数 == 授予条数」当验收**
现象：单用户单徽章的用例全绿，线上仍然漏。
根因：一个用户只能暴露一次冲突。
判据：集成测试必须**两个用户、同一 slug**，断言两行、各归各人（
`TestNotifyBadgeGrantedTxScopesDedupeKeyToRecipient`）。

**T3 把回扫当成「不产生新授予的维护任务」**
现象：改规则后回扫跑完，运营以为「没人达标」。
根因：回扫恰恰是批量补授予的主路径（改严规则时它是唯一路径）。
判据：`badges.backfill_rules` 每页跑完后 `user_badges` 有新增，就必须有对应通知行。

**T4 空页 panic 把「回扫不结束」伪装成「回扫还在跑」**
现象：状态行永远是 running，job 列表里同一页反复失败。
根因：`userIDs` 为空时两条返回都要取 `userIDs[len-1]`，空切片 index -1 panic；
river 重试再次命中同一页 → 无限崩溃循环。
判据：游标走到末页、或站上活跃用户数正好是 batch 的整数倍时都会出现。
本刀在同一函数里收口：`len(userIDs) == 0` → 直接返回 `(afterUserID, true, nil)`，
回归测试 `TestBackfillRunnerEmptyPageCompletesWithoutPanic`。
