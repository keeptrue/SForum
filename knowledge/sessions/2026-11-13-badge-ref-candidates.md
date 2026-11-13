# 2026-11-13 徽章 objectRef 候选（`refCandidates`）hot handoff

## Changed

- **后端**（`apps/api`）：`conditions/refcandidates.go`（类型 + 唯一顺序定义 + 截断/排序）、
  `Models/Badges/ref_candidates.go`（`PostgresStore.RefCandidates`，四类各一条 SQL + provider 注入）、
  `admin_service.go`（窄接口 `RefCandidateStore` + `BadgeConditionCatalog.RefCandidates`，best-effort）、
  `Controllers/Badges/{dto,rule_controller}.go`（additive 顶层 `refCandidates`）、
  `bootstrap/api_assembly_domains.go`（补 `SetDeclaredProviders` + 改写过期注释）、
  `contracts/openapi/schemas/badges.yaml`（`BadgeRefCandidateGroup` / `BadgeRefCandidate`）。
- **前端**（`apps/web`）：`utils/admin/badgeRefCandidates.ts`（归一化 / 失效值 / `refKindManual` /
  `excludeBadgeSlug`）、`useAdminBadgeConditions.ts`（暴露 `refCandidates`）、
  `SFAdminBadgeRuleParamField.vue`（objectRef 分支：`USelectMenu` ↔ 文本兜底）、
  8 层 prop 链接线、`SFAdminBadgeEditDialog.vue`（剔自身 slug）、i18n zh/en（+6 键，删 `candidatesPending`）。
- **路由 tripwire 补正**：`registry_test.go` 422 → **423**。这不是本刀抬基线——`7c07edf9d`
  把 `core.route.badges.admin_badge_rule_text` 写进 identities 与 `core_catalog_gen.go` 却漏改计数，
  HEAD 上 `go test ./app/Support/Routes/` 本来就是红的。
- **脚本去腐坏**：`scripts/verify-badge-rules-acceptance.sh` 的 X-02 删掉 `expected=418/421` 手工账，
  判据交给 `go test ./app/Support/Routes/`（解析值只作报告）。当前 `EXIT=0 / ok 15 / FAIL 0 / pend 57`。
- **顺带修掉的历史缺陷（本刀挖出）**：
  - `identity.account_age` 混合日历（Go 进程日 − PG 会话日历日）→ 部分用户**永久**多算 1 天，
    门槛提前一天满足。实测 11 → 10，改法见 `modules/badges.md`。
  - `conditions` 两条时区 fixture（`24h` 算术冒充日历日）在本地 00:00–08:00 恒红。
  - 路由 tripwire：`7c07edf9d` 把 `admin_badge_rule_text` 写进 identities 与生成物却漏改计数，
    HEAD 上 `go test ./app/Support/Routes/` 本来就红 → 已是 423。
  - F1（验收期 P1）：provider 候选来自 registry 已声明，保存期只认绑定表 → 候选里唯一那个
    provider 必 422。改为两边共用 `knownProviders`（见 decisions D9）。
  - `scripts/verify-badge-rules-acceptance.sh` 四处腐坏：X-02 硬编码 421、M1.1-01 硬编码迁移
    版本、M1.4-14 grep 已删除的 `metricLabels`、两条 absence-grep 缺「判据文件存在」前置
    + 四条静态断言坐在 DB 分支里。
- **测试**：Go 六组（refCandidates 四组 + account_age 时区 + provider 引用 + calendarDays）、
  前端 `badgeRefCandidates.test.ts`（17 用例）+ 重写 `adminBadgeRuleLeafParams.test.ts` 的原占位断言。
  `bun test` 2631 pass / 0 fail；typecheck 0 error；`./scripts/test.sh --fast` **编排器级三线 PASS**
  （api 347s / web 54s / static 17s，`All gates passed`），另有一次独立全量 Go 复跑 144 包 ok。
  **注意**：11:15 那轮 api 线的 `cmd/sforum: undefined: validProtocolV2QueryIdentity` 是与并发编辑
  撞车的假红（同树 `go build ./...` 通过、静下来重跑全绿），不要把它记成缺陷。

## Decisions

- 候选在 `Models/Badges` 直查 Core 表（先例 `ref_names_postgres.go`），不走 Forum/Identity service。
- 聚合在既有 `conditions` 端点（additive 顶层键），**不新增路由**。
- 恒五项固定顺序；`unavailable`（取不到）与「候选为空」分开记录、UI 上都退文本输入。
- `flags: hidden|disabled` 只做标记**不过滤**（引用隐藏分类是合法历史事实）。
- 每类上限 500 + `truncated`；`fact` 分支一行不动（下一刀统一控件族）。
- 失败语义比计划更宽：`error` 时保留部分结果，不让一次局部失败放大成五项全空。

## Next

- **验收结论：accepted（带残留）**。30 条子断言 28 ok / 0 FAIL / 2 pend；
  报告 `reports/2026-11-13-badge-ref-candidates-acceptance-run.md`（§9 分两类残留）。
  2 条 pend 是覆盖缺口：A3-b（真改表造失败，用拦截响应代替）、A5-c（候选 11 条未触发滚动）。
- **可单开一刀的四条残留**（报告 §9-A）：R1 `unavailable` 与「候选确实为空」共用同一句提示；
  R2 未知 `refKind` 静默退文本（目录不变量只查「有没有数组」）；R3 手动徽章的 `rule-impact`
  返回 422 → 「手动→自动」弹窗必然显示「估算失败」；R4 手动徽章只给 `rule` 不给 `grantMode`
  → 422 用泛码（建议独立码 `forum.badge_rule_mode_conflict`）。
- **下一刀（控件族统一）**：`fact.count` 的 factKey/variant 仍是原生 `<select>`；
  `object.engagement.topicId` 仍是 int 自由数字。两者理由见模块笔记残留 28/29。

## Open Questions

- 无阻塞项。`USelectMenu` 的 `:model-value="''"` 在真实浏览器里**不**影响 placeholder（A5 实测），
  所以不需要改成 `textValue || undefined`——记在这里防止下一个人「顺手修」它。
- `site.timezone` 全仓零消费（残留 #12）仍在：本刀只让 `account_age` 的日界回到「进程本地日」
  这**一个**来源，没有引入站点时区语义。若将来要做站点级时区，`nowTime()` 是唯一的改点。
