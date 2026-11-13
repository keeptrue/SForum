# 2026-11-14 会话交接：自动授予的徽章不发通知（去重键作用域 + 回扫出口）

## Changed

- `apps/api/app/Models/Notifications/fanout.go`：`NotifyBadgeGrantedTx` 去重键
  `badge:{slug}` → `badge:{recipientUserID}:{slug}`，邮件 `IdempotencyKey` 同键；
  方法注释改成「dedupe_key 全局唯一 → 键必须自证收件人」。
- `apps/api/database/migrations/202611140001_badge_grant_notification_dedupe_scope.sql`（新）：
  把历史 `badge_granted` 行改写为新键格式（等值判据，不用 `LIKE 'badge:%'`），
  **不补发**被吞掉的通知，Down 故意为空。
- `apps/api/bootstrap/badges_wiring.go`：`badgesRuleBackfillRunner` 增加
  `users` / `evaluator`（窄接口）/ `notifier` / `logger`，授予后逐用户批量通知，
  通知失败只 warn 不中断本页、评估错误仍上抛；`newBadgesBackfillWorker` /
  `registerBadgesBackfillWorker` 增加 outbox 参数。
- **顺带修掉同函数里的空页 panic**：`userIDs` 为空时两条返回都取 `userIDs[len-1]` →
  index -1 崩溃，job 重试会再次命中同一页（游标到头后 river 仍可重入），回扫永不结束、
  状态行永远 running。现在 `len(userIDs) == 0` 直接返回 `(afterUserID, true, nil)`。
  这条与本刀相邻但不是「顺手重构」：崩溃点在本次改动函数的同一路径上，且它会连带
  让新加的通知出口永远跑不到。
- `apps/api/bootstrap/worker_forum_jobs.go`：回扫 worker 复用同一个 outbox。
- 测试：`database/migrations/badge_grant_dedupe_scope_migration_test.go`（新，静态契约 + 隔离 schema
  真跑 Up + 幂等 + 「旧键必撞唯一约束」前置证明）、`bootstrap/badges_backfill_notification_test.go`
  （新，6 条 runner 行为，含空页回归）、`bootstrap/badges_wiring_test.go`（签名跟随 + 两条装配守卫）、
  `Notifications/badge_granted_projection_postgres_integration_test.go`（键格式 + 两用户同 slug 回归）。
- 文档：`modules/notifications.md`、`modules/badges.md`、决策
  `decisions/2026-11-14-badge-grant-notification-scope.md`（D12–D16，修订 2026-11-13 决策 D5 的键格式一句）。

## Decisions

- D12 键必须自证收件人（全局唯一约束驱动的硬约束，不是风格）。
- D13 「同一用户同一徽章终身一条」语义不变；不改成按授予行 id 去重。
- D14 历史行只改键、不补发；Down 为空（反向退键会撞唯一约束）。
- D15 `mail_deliveries.idempotency_key` 不回改（无 user 维度，新旧键不相撞）。
- D16 规则回扫是第五条授予路径；通知失败只 warn（上抛会让重跑把通知永久洗掉），评估错误仍上抛。

## 证据

- `./scripts/test-api-isolated.sh ./database/migrations/... ./app/Models/Notifications/... ./bootstrap/...`
  → 全 `ok`；最终一轮（含 `./app/Models/Badges/...`、`./app/Jobs/Badges/...`）：
  migrations 0.957s、Notifications 1.329s、Badges 18.449s、Badges/conditions 2.924s、
  Jobs/Badges 0.004s、bootstrap 39.961s，**0 FAIL**。
- 聚焦断言 28 条（`-run 'TestBadges|TestBadge|TestBackfill'` 于 bootstrap）：14 条
  `--- PASS`（含 6 条 runner 行为 + 3 条装配守卫 + 5 条目标解析/适配器）；
  Notifications + migrations 侧 9 条全 PASS（含新的
  `TestNotifyBadgeGrantedTxScopesDedupeKeyToRecipient` 与两条迁移契约）。
- `cd apps/api && go build ./...` exit 0；`go vet` 三个包无输出。
- dev 库迁移已执行（`goose_db_version` = 202611140001），6 条历史行全部带收件人。
- 真实 job 回放：删除 user 1 的 `first-topic` 授予行 → 入队真实 `badges.evaluate_user`
  （river_job 61633，completed）→ 重新授予（user_badges 1229）+ 写出
  `notifications` 行 `badge:1:first-topic`（target `badge/4`）。旧代码下该键被 user 479 占住，
  这一行不可能出现。

## Next

- 前端未改（`badge_granted` 的图标/文案/收件箱/庆祝卡渲染本轮核对齐全）。若要在浏览器里看
  实机通知，用 user 1 打开铃铛即可——行已经在库里。
- 浏览器 QA 未做：本轮是后端契约修复，UI 无改动。
- 若日后新增通知类型，键里必须出现 recipient 或等价收件人唯一标识（见 `modules/notifications.md`
  的硬约束段），集成测试至少覆盖「两个用户、同一业务对象」。

## Open Questions

- 回扫通知是否需要与 `Recompute` 一样在后台显示「本次通知了多少人」？目前只有 `slog.Warn`
  与通知行本身。若运营反复问「回扫到底通知了谁」，可在 `badge_rule_backfill_runs` 上加一列
  `notified_users`（本轮刻意不加：进度表口径是「扫到第几个用户」，掺通知计数会让两个语义混在一起）。
