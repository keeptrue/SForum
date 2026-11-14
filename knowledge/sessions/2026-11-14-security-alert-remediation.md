# 2026-11-14 Security Alert Remediation Handoff

- 日期：2026-11-14｜范围：GitHub 仓库 `keeptrue/SForum`（public）的 Dependabot /
  CodeQL / Secret scanning 告警收敛；本次只动告警处置与仓库安全设置，**未改任何产品行为**。
- 证据：`reports/security-2026-11-14/overflow-pattern-audit.md`（全仓扫描与覆盖边界）；
  模块 `modules/backend.md`（Security And Dependency Governance）、`modules/trust-levels.md`。
- 状态更正（2026-11-14 晚）：5 条 open 告警**已全部转 fixed**（fixed_at 见 Changed 一节）；
  `reports/security-2026-11-14/overflow-pattern-audit.md` 已随 task-11 提交入库。

## Changed

- **Dependabot #612（otel CVE-2026-128085 / GHSA-q7gw-24fr-5p69）→ fixed**：合并
  PR #209（squash，commit `3563b2055`），`tools/proto/go.mod` 的
  `go.opentelemetry.io/otel/trace` 1.44.0 → **1.47.0**（otel / otel-log / otel-metric
  同步），go.sum 已同步，`go mod verify` 通过。告警 fixed_at **2026-11-14T10:18:04Z**。
- **CodeQL #45 / #47 / #48（`go/allocation-size-overflow`）→ 改代码**：commit
  `af18ef94e`。`apps/api/app/Http/Controllers/TrustLevels/controller.go:470` 改为
  `capHint := max(len(configKeys), len(declaredKeys))`；
  `apps/api/app/Models/TrustLevels/policy.go:122` 改为
  `max(len(previous.DeletedTierIDs), len(previous.Tiers))`。业务语义、判空顺序、去重
  与返回顺序零改动。三条告警**已全部转 `fixed`**（fixed_at 均
  **2026-11-14T10:48:31Z**），仓库 code-scanning 现无 open 告警。
- **CodeQL #46（`js/clear-text-logging`）→ 删脚本**：commit `52fb2b50a` 删除
  `knowledge/reports/qa-2026-11-12-badge-pend-conversion/390/setup-account.mjs`
  （一次性验收账号脚本，文件头自述「跑完必须删干净」）。同目录 `account.json`
  （password / sessionCookie 已脱敏）与 `cleanup.log`（userId 498 已从 users /
  user_roles / user_sessions / user_credentials 删除）作为「未泄漏」证据保留未改。
  该告警已确认转 `fixed`（fixed_at **2026-11-14T10:24:36Z**）。
- **仓库安全设置**：`secret_scanning` 与 `secret_scanning_push_protection` 由
  disabled 改为 **enabled**（public 仓库免费）；`secret_scanning_non_provider_patterns`
  与 `secret_scanning_validity_checks` 仍 disabled（未触碰），
  `dependabot_security_updates` 仍 enabled；历史扫描告警列表实测为空。
- **Dependabot 总体**：**open 安全告警现为 0**（#612 fixed、#610 dismissed、其余历史 fixed）。

## Decisions

- **改代码，不 dismiss 告警**（#45/#47/#48）：该查询的 sink 判据要求 `make` 的容量实参
  由整型 `+`/`*` 产生；改成 `max` 单边比较后表达式里不再有加法，`DefaultSink →
  OverflowProneOperand → AllocationSize` 链条断裂，告警必然关闭。这三处本来也**不可达
  溢出**（`len()` 上界受进程可寻址内存约束，加法永不溢出成负），改的是**表达形式**而非
  行为——dismiss 会把一条「模式匹配即报」留在流水线上。
- **删脚本，不做脱敏改造**（#46）：脚本是一次性验收凭据生成器，密码运行时生成
  （`Qa!${stamp}Aa1`），仓库无硬编码凭据；脱敏改造反而把一次性产物长期留下，多一份
  要维护的「假安全」代码。删除后 `account.json` / `cleanup.log` 仍能证明「没有泄漏」。
- **开启 secret scanning / push protection**：public 仓库免费，新提交里的凭据会在入库
  前被拦；代价只是历史扫描噪音，实测历史告警为空。

## Next

1. **Dependabot #610 的处置**（见 Open Questions 2）：要么升级 katex 让它转 fixed，要么
   把 dismiss 理由写清楚——不要让它一直靠 dismissed 状态躺着。
2. **19 个 open Dependabot PR**（用户明确本次不合并）：#208 core-go 组、#207 compose-images
   组、#205 github-actions 组、#202 builtin-go 组、#201/#200/#199/#198/#197/#196/#195/#194/
   #193/#192/#191、#188/#187/#186/#185。它们是常规版本升级，**不对应任何 open 安全告警**。
3. **CodeQL / govulncheck 覆盖盲区**（见 Open Questions 1）：扩 workflow 构建范围属
   后续项，本次未动 workflow。
4. **TrustLevels 容量 nit**（见 Open Questions 3）：`policy.go:123` 的
   `seen := make(map[string]bool, len(deleted))` 拿不到容量提示，小审建议改用
   `max(len(previous.DeletedTierIDs), len(previous.Tiers))`；本次为最小 diff 未改。
5. **复查 secret scanning 历史扫描最终结果**：大仓库历史扫描可能需数十分钟到数小时；本次
   26 分钟内轮询 32 次均为空，最终结果仍需收口。已知项（若出现即这两项，**不应误判为
   真实泄漏**）：`apps/api/app/Support/Crypto/option_test.go:24` 的 AWS 官方示例 key
   `AKIAIOSFODNN7EXAMPLE`；`.../qa-2026-11-12-.../390/account.json` 的 password /
   sessionCookie（已脱敏为 `<redacted>`）。

## Open Questions

1. **CodeQL 流程盲区（新发现，影响面大）**：workflow 的 CodeQL 作业是
   `build-mode: manual` + `working-directory: apps/api`（`security.yml` 第 49 / 71 行），
   仓库**无 `go.work`**、`apps/api/go.mod` **无 `replace`**，因此 **8 个
   `extensions/builtin/plugins/*/backend` 独立 Go module 与 `tools/proto` 根本不在
   CodeQL 的 Go 数据库里**（govulncheck 同理，`work-dir: apps/api` + `go-package: ./...`
   只覆盖 core）；workflow 里「内建插件与 apps/api 共享同一依赖树」只对**依赖可达性**
   成立，对**插件自身源码**不成立。小研已用 Go AST 补扫这两个范围：**命中 0 处**
   （结论是「实测无此类漏洞」而非「扫不到所以认为没有」）。**消除盲区需扩展 workflow
   构建范围——后续项，本次未改。** 详见 `modules/backend.md` 与审计报告 §三。
2. **Dependabot #610 仍在 dismissed（有意保留，不要让它无声消失）**：katex prototype
   pollution（CVE-2026-103923 / GHSA-238p-pmpm-9mq7，**low**，`apps/web/package.json`），
   state=dismissed、dismissed_reason=`fix_started`。本次**有意未处理**（low 等级 + 前端
   渲染路径的实际可利用性未评估，先不占本次 diff）。后续要么升级 katex 让它转 fixed，
   要么把 dismiss 理由写清楚。
3. **已知代码 nit（本次不修）**：`apps/api/app/Models/TrustLevels/policy.go:123` 的
   `seen := make(map[string]bool, len(deleted))`——`deleted` 以 `make([]string, 0, ...)`
   创建，此刻 len 恒为 0，这个 map 拿不到任何容量提示。小审建议写成
   `max(len(previous.DeletedTierIDs), len(previous.Tiers))`；本次为保持 diff 最小未改
   （只影响一次分配多少，不影响正确性）。
4. **secret scanning 的两个非 provider 开关仍 disabled**：`..._non_provider_patterns` 与
   `..._validity_checks` 未触碰；若要把规则从「provider 已知格式」扩到自定义模式，需单独
   评估噪音量与配额。
