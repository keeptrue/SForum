# 信任等级 — M8 最终状态表（124 条，矩阵 ID 口径）

合并口径：reviewer 独立证据优先 → 运维 `_summary.txt`（矩阵 ID 口径，ok 67 / 未实现 57）→ 两边都无则 `未实现` + 理由。
证据类型在备注里显式区分：**浏览器实测** / **变异红绿** / **代码级** / **静态链路** / **运维自动化**。

## 三个数字

**ok 75 · FAIL 0 · 未实现 49**（合计 124）

| ID | 状态 | 证据 / 未实现理由（含证据类型） | 归属 |
| --- | --- | --- | --- |
| A1 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A2 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A3 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A4 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A7 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A8 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A9 | **ok** | 运维自动化证据 · storage/qa/trust-levels/A/A1-A9.txt | ops |
| A5 | **ok** | `TestDeniedPermissionKeysIncludesDenyOnlyKeys`, `TestMergeEarnedPermissionsHonoursDeny` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| A6 | **ok** | `TestDeniedPermissionKeysIncludesDenyOnlyKeys`, `TestMergeEarnedPermissionsHonoursDeny` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| A10 | **ok** | `TestActorZeroValueHasNoEarned` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| A11 | **未实现** | 矩阵标注无现成测试；`A/A11-A12-pend.md` | ops |
| A12 | **未实现** | 矩阵标注无现成测试；`A/A11-A12-pend.md` | ops |
| A13 | **ok** | `TestMergeEarnedPermissionsSkipsStaticKeys` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| A14 | **ok** | `TestCanTruthTable` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| B1 | **ok** | `TestEarnableValidateRejectsDangerousKeys` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| B2 | **ok** | `TestEarnableValidateRejectsDangerousKeys` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| B3 | **ok** | 变异红/绿：白名单塞 `user.ban` → `TestEligiblePermissionsIsLiteralWhitelist` 红；改回绿 · storage/qa/trust-levels/B/B3-mutation-red.txt · storage/qa/trust-levels/B/B3-restored-green.txt | rev |
| B4 | **ok** | 变异红/绿：白名单塞 `moderation.review` → 同上测试红；改回绿 · storage/qa/trust-levels/B/B3-mutation-red.txt · storage/qa/trust-levels/B/B3-restored-green.txt | rev |
| B5 | **ok** | `TestEligiblePermissionsIsLiteralWhitelist` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| B6 | **未实现** | 矩阵标注「无现成测试」；`B/B6.txt` | ops |
| B7 | **ok** | 代码审阅：catalog 来源是 Go 常量，非正则扫迁移 SQL · storage/qa/trust-levels/B/B7-review.md | rev |
| B8 | **ok** | `TestDefaultEarnableIsEmpty` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| B9 | **ok** | 运维自动化证据 · storage/qa/trust-levels/B/B9.txt | ops |
| B9b | **ok** | 运维自动化证据 · storage/qa/trust-levels/B/B9.txt | ops |
| B10 | **ok** | `TestEligiblePermissionsIsLiteralWhitelist` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C1 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C2 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C3 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C4 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C5 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C6 | **ok** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C7 | **ok** | `TestLevelAgeGate` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C8 | **ok** | `TestLevelBoundaries`, `TestLevelMetricsAreIndependentlyRead` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C9 | **ok** | `TestLevelMetricsAreIndependentlyRead` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C10 | **ok** | `TestLevelReverseCountsDemote` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C11 | **ok** | `TestLevelReverseCountsDemote` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C12 | **ok** | `TestLevelReverseCountsDemote` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C13 | **未实现** | 矩阵标注「无现成测试」；`C/C13-C15-psql.txt` | ops |
| C14 | **未实现** | 矩阵标注「无现成测试」；`C/C13-C15-psql.txt` | ops |
| C15 | **未实现** | 矩阵标注「无现成测试」；`C/C13-C15-psql.txt` | ops |
| C16 | **未实现** | 矩阵标注「无现成测试」；`C/C16.txt` | ops |
| C17 | **ok** | `TestLevelAgeGate` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C18 | **ok** | `TestLevelAgeGate` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C19 | **ok** | `TestLevelNonMonotonicPolicyKeepsHighest` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| C20 | **未实现** | 矩阵标注「无现成测试」；`C/C20.txt` | ops |
| D1 | **ok** | `TestCooldownErrorPreservesCauseAndRetryAt` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D2 | **未实现** | 矩阵标注「无现成测试」；`D/D2-D3.txt` | ops |
| D3 | **未实现** | 矩阵标注「无现成测试」；`D/D2-D3.txt` | ops |
| D4 | **ok** | `TestLimitsForActorLevelOneHasNoNewUserTightening` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D5 | **ok** | `TestLimitsForActorDaysZeroDisablesLadder` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D6 | **ok** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D7 | **ok** | `TestResolverSkipsNonHumanAccounts`, `TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D8 | **ok** | `TestLevelZeroCreatedAtIsStrict` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D9 | **ok** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| D10 | **未实现** | 矩阵标注「无现成测试」；`D/D10-baseline.diff` | ops |
| E1 | **ok** | `TestResolverFailClosed`, `TestResolverNilDependenciesFailClosed` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| E2 | **ok** | `TestResolverFailClosed`, `TestResolverNilDependenciesFailClosed` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| E3 | **ok** | `TestResolverFailClosed`, `TestResolverNilDependenciesFailClosed` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| E4 | **ok** | `TestActorZeroValueHasNoEarned` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| E5 | **未实现** | 无自动化断言；有 reviewer 手工证据（无 DB / 无 `time.Now` / 无包级状态 / 500 次零漂移）——**手工证据不是断言** · storage/qa/trust-levels/E/E5-purity-reviewer.md | rev |
| E6 | **未实现** | 矩阵标注「无现成测试」；`E/E6.txt` | ops |
| E7 | **ok** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| F1 | **ok** | `TestCanTruthTable` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| F2 | **ok** | `TestTrustLevelDisabledSentinelsAreIdentical` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| F3 | **未实现** | 矩阵标注「无现成测试」；`F/F3.txt` | ops |
| F4 | **ok** | 浏览器三态：开→卡片渲染、关→0 标记、恢复→再渲染；他人 `progressbar=0` / 本人 `progressbar=1` · storage/qa/trust-levels/F/F4-evidence.md | rev |
| F5 | **未实现** | 矩阵标注「无现成测试」；`F/F5.txt` | ops |
| F6 | **ok** | `TestDefaultEarnableIsEmpty` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| F7 | **ok** | `TestFeatureFlagsRecommendedDefaultsAndRestore` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| F8 | **未实现** | 矩阵标注「无现成测试」；`F/F8.txt` | ops |
| F9 | **未实现** | 矩阵标注「无现成测试」；`F/F9-pend.md` | ops |
| G1 | **未实现** | 矩阵标注「无现成测试」；`G/G1-G2.txt` | ops |
| G2 | **未实现** | 矩阵标注「无现成测试」；`G/G1-G2.txt` | ops |
| G3 | **未实现** | 矩阵标注「无现成测试」；`G/G3.txt` | ops |
| G4 | **未实现** | 矩阵标注「无现成测试」；`G/G4.txt` | ops |
| G5 | **未实现** | 矩阵标注「无现成测试」；`G/G5.txt` | ops |
| G6 | **未实现** | 矩阵标注「无现成测试」；`G/G6.txt` | ops |
| H1 | **未实现** | 矩阵标注「无现成测试」；`H/H1.txt` | ops |
| H2 | **未实现** | 矩阵标注「无现成测试」；`H/H2.json` | ops |
| H3 | **未实现** | 矩阵标注「无现成测试」；`H/H3.txt` | ops |
| H4 | **ok** | 运维自动化证据 · storage/qa/trust-levels/H/H4.txt | ops |
| H5 | **ok** | 运维自动化证据 · storage/qa/trust-levels/H/H5.txt | ops |
| H6 | **未实现** | 矩阵标注「无现成测试」；`H/H6.json` | ops |
| H7 | **未实现** | 矩阵标注「无现成测试」；`H/H7.txt` | ops |
| H8 | **未实现** | 矩阵标注「无现成测试」；`H/H8.txt` | ops |
| H9 | **未实现** | 矩阵标注「无现成测试」；`H/H9.txt` | ops |
| H10 | **未实现** | 矩阵标注「无现成测试」；`H/H10.txt` | ops |
| H11 | **未实现** | 矩阵标注「无现成测试」；`H/H11-H12.txt` | ops |
| H12 | **未实现** | 矩阵标注「无现成测试」；`H/H11-H12.txt` | ops |
| H13 | **未实现** | 矩阵标注「无现成测试」；— | — |
| H14 | **未实现** | 矩阵标注「无现成测试」；`H/H14.txt` | ops |
| H15 | **ok** | 变异红/绿：Badges 内部同步改名、adapter 不动 → 编译错误精确落在 `trust_levels_wiring.go:69`；改回 `BUILD_EXIT=0` · storage/qa/trust-levels/H/H15-red.txt · storage/qa/trust-levels/H/H15-green.txt | rev |
| H16 | **ok** | `TestLevelSlugIsNotRoleKey` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H17 | **未实现** | 矩阵标注「无现成测试」；`H/H17.json` | ops |
| H18 | **ok** | `TestDecodeConfigValidation` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H19 | **ok** | `TestDecodeConfigRejectsUnknownField` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H20 | **ok** | `TestDecodeConfigAcceptsValid` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H21 | **ok** | `TestConfigRoundTrip`, `TestLimitsNullRoundTrip` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H22 | **ok** | `TestEarnableValidateRejectsDangerousKeys` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| H23 | **ok** | `TestDecodeConfigEmptyFallsBackToDefault` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| I-01 | **未实现** | 矩阵标注「无现成测试」；`I/I-01.json` | ops |
| I-02 | **未实现** | 矩阵标注「无现成测试」；`I/I-01.json` | ops |
| I-03 | **ok** | 匿名 SSR 载荷 `trustLevel={level,levelSlug}`，全文 `progress`/`bottleneck` 计数 0 · storage/qa/trust-levels/I/I-03-I-04-otherview.txt | rev |
| I-04 | **ok** | 同上（他人视角无进度块） · storage/qa/trust-levels/I/I-03-I-04-otherview.txt | rev |
| I-05 | **未实现** | 矩阵标注「无现成测试」；`I/I-04.json` / `I/I-05.json` | ops |
| I-06 | **ok** | `TestPublicPayloadHasNoLimits` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| I-07 | **ok** | 运维自动化证据 · storage/qa/trust-levels/I/I-07.txt | ops |
| I-08 | **ok** | 运维自动化证据 · storage/qa/trust-levels/I/I-07.txt | ops |
| I-09 | **未实现** | 矩阵标注「无现成测试」；`I/I-09.json` | ops |
| I-10 | **ok** | `TestAdminRoutesRequirePermissions` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| I-11 | **未实现** | 矩阵标注「无现成测试」；`I/I-11.json` | ops |
| I-12 | **ok** | `TestConfigRoundTrip`, `TestDecodeConfigRequiresLimitsAndEarnable`, `TestUpdateRejectsInvalidConfig` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| I-13 | **未实现** | 矩阵标注「无现成测试」；`I/I-13.json` | ops |
| I-14 | **ok** | `TestFeatureDisabledReturns404OnEveryRoute` 全 PASS（`=== RUN` 见日志） · `storage/qa/trust-levels/_reviewer-logs/*.log` | ops |
| I-15 | **未实现** | 矩阵标注「无现成测试」；`I/I-15.txt` | ops |
| I-16 | **未实现** | 矩阵标注「无现成测试」；`I/I-16.txt` | ops |
| I-17 | **未实现** | 矩阵标注「无现成测试」；`I/I-17.txt` | ops |
| I-18 | **未实现** | 矩阵标注「无现成测试」；`I/I-17.txt` | ops |
| I-19 | **未实现** | 矩阵标注「无现成测试」；`I/I-19.txt` / `I/I-20.txt` | ops |
| I-20 | **未实现** | 矩阵标注「无现成测试」；`I/I-19.txt` / `I/I-20.txt` | ops |
| I-21 | **ok** | 运维自动化证据 · storage/qa/trust-levels/I/I-21.json | ops |
| I-22 | **ok** | **浏览器实测（headless Chrome，戊会话）**：点「恢复推荐默认」→ 表单回填 TL1=3/5（推荐值），全程观测到 1311 个请求、**写请求 0 个**（PUT/POST/PATCH/DELETE 皆无）。**代码级佐证**：`useSettingsSection.runRestore()` 只做 `apply()`+toast、无网络调用；`SFAdminTrustLevelsTab.vue` 唯一 watch 是 `watch(props.items, resetFromItems)`；`save()` 仅提交处理器可达 ⇒ 覆盖「点了之后再点保存」这类组合路径 · storage/qa/trust-levels/I/I-22-restore-probe.txt | rev |
| I-23 | **ok** | 390×844 实测：`innerWidth=390`、`scrollWidth==390` 无溢出、30 个 number 输入（6×5）、TL0 6 格 disabled、**D12 文案在页面实读为 true**（`已获得的徽章与已发布的内容不受影响`）；文案静态链路亦完整（`zh-CN.json:4552` + 组件 `UAlert:125`）。`刷新` 按钮越界由前端四条判据证明为**既有缺陷**，记 residual · storage/qa/trust-levels/I/I-23-390x844.json | rev |
| I-24 | **未实现** | 矩阵标注「无现成测试」；`I/I-24.txt` | ops |
