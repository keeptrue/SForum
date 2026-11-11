# 信任等级 — M8 最终状态表（reviewer）

生成时间：2026-11-10 21:13:26　矩阵：124 条（本表覆盖其中可由 reviewer 独立取证的 84 行）

判据：`go test -count=1 -v` 输出里必须同时出现 `=== RUN <Name>` 与 `--- PASS: <Name>`；变异见红/绿两态；浏览器见 DOM 计数。

| ID | 谁验 | 状态 | 证据 / 未取证原因 |
| --- | --- | --- | --- |
| A5–A6 | ops | **PASS** | `TestDeniedPermissionKeysIncludesDenyOnlyKeys`, `TestMergeEarnedPermissionsHonoursDeny` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| A10 | ops | **PASS** | `TestActorZeroValueHasNoEarned` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| A11–A12 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| A13 | ops | **PASS** | `TestMergeEarnedPermissionsSkipsStaticKeys` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| A14 | ops | **PASS** | `TestCanTruthTable` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| B1–B2 | ops | **PASS** | `TestEarnableValidateRejectsDangerousKeys` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| B3–B4 | rev | **PASS** | 变异：白名单塞 user.ban / moderation.review → TestEligiblePermissionsIsLiteralWhitelist 红；改回绿 · B/B3-mutation-red.txt · B/B3-restored-green.txt |
| B5 | ops | **PASS** | `TestEligiblePermissionsIsLiteralWhitelist` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| B6 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| B7 | rev | **PASS** | 代码审阅：catalog 来源是 Go 常量，非正则扫迁移 · B/B7-review.md |
| B8 | ops | **PASS** | `TestDefaultEarnableIsEmpty` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| B10 | ops | **PASS** | `TestEligiblePermissionsIsLiteralWhitelist` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C1–C6 | ops | **PASS** | `TestLevelBoundaries` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C7 | ops | **PASS** | `TestLevelAgeGate` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C8 | ops | **PASS** | `TestLevelBoundaries`, `TestLevelMetricsAreIndependentlyRead` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C9 | ops | **PASS** | `TestLevelMetricsAreIndependentlyRead` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C10–C12 | ops | **PASS** | `TestLevelReverseCountsDemote` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C13–C15 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| C16 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| C17–C18 | ops | **PASS** | `TestLevelAgeGate` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C19 | ops | **PASS** | `TestLevelNonMonotonicPolicyKeepsHighest` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| C20 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| D1 | ops | **PASS** | `TestCooldownErrorPreservesCauseAndRetryAt` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D2–D3 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| D4 | ops | **PASS** | `TestLimitsForActorLevelOneHasNoNewUserTightening` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D5 | ops | **PASS** | `TestLimitsForActorDaysZeroDisablesLadder` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D6 | ops | **PASS** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D7 | ops | **PASS** | `TestResolverSkipsNonHumanAccounts`, `TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D8 | ops | **PASS** | `TestLevelZeroCreatedAtIsStrict` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D9 | ops | **PASS** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| D10 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| E1–E3 | ops | **PASS** | `TestResolverFailClosed`, `TestResolverNilDependenciesFailClosed` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| E4 | ops | **PASS** | `TestActorZeroValueHasNoEarned` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| E5 | ops | **PEND** | 无自动化断言；已挂 reviewer 手工证据（无 DB / 无 time.Now / 无包级状态 / 500 次零漂移） · E/E5-purity-reviewer.md |
| E6 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| E7 | ops | **PASS** | `TestLimitsForActorKeepsFiveCriteria` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| F1 | ops | **PASS** | `TestCanTruthTable` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| F2 | ops | **PASS** | `TestTrustLevelDisabledSentinelsAreIdentical` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| F3 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| F4 | rev | **PASS** | 三态：开→渲染（档位名+卡片）、关→0 标记、恢复→再渲染；他人视角 progressbar=0 · F/F4-evidence.md |
| F5 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| F6 | ops | **PASS** | `TestDefaultEarnableIsEmpty` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| F7 | ops | **PASS** | `TestFeatureFlagsRecommendedDefaultsAndRestore` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| F8 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| F9 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| G1–G2 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| G3 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| G4 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| G5 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| G6 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H1 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H2 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H3 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H4 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H5 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H6 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H7 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H8 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H9 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H10 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H11–H12 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H13 | — | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H14 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H15 | rev | **PASS** | 变异：Badges 内部同步改名、adapter 不动 → 编译错误精确落在 trust_levels_wiring.go:69；改回 BUILD_EXIT=0 · H/H15-red.txt · H/H15-green.txt |
| H16 | ops | **PASS** | `TestLevelSlugIsNotRoleKey` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H17 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| H18 | ops | **PASS** | `TestDecodeConfigValidation` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H19 | ops | **PASS** | `TestDecodeConfigRejectsUnknownField` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H20 | ops | **PASS** | `TestDecodeConfigAcceptsValid` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H21 | ops | **PASS** | `TestConfigRoundTrip`, `TestLimitsNullRoundTrip` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H22 | ops | **PASS** | `TestEarnableValidateRejectsDangerousKeys` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| H23 | ops | **PASS** | `TestDecodeConfigEmptyFallsBackToDefault` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| I-03 | ops | **PASS** | 匿名 SSR：trustLevel = {level, levelSlug}，全文 progress/bottleneck 计数 0 · I/I-03-I-04-otherview.txt |
| I-06 | ops | **PASS** | `TestPublicPayloadHasNoLimits` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| I-09 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-10 | ops | **PASS** | `TestAdminRoutesRequirePermissions` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| I-11 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-12 | ops | **PASS** | `TestConfigRoundTrip`, `TestDecodeConfigRequiresLimitsAndEarnable`, `TestUpdateRejectsInvalidConfig` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| I-13 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-14 | ops | **PASS** | `TestFeatureDisabledReturns404OnEveryRoute` 全 PASS（`=== RUN` 见日志） · `/tmp/tlreview/results/*.log` |
| I-15 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-16 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-21 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |
| I-24 | ops | **未取证** | ops `_summary.txt` 的 ID 体系与本矩阵不同（`C-1` ≠ `C1`），无法映射；本条无我的独立证据 |

统计（已解析 84 行）：PASS 45 · FAIL 0 · PEND 1 · 未取证 38
## 未解析的 40 行

A1–A4 / I-01–I-02 / I-04–I-05 / I-07–I-08 / I-17–I-18 / I-19–I-20 / I-22–I-23 / JSON 形状 6 —— 表格格式不同，我的解析器未覆盖。其中：
- **I-22**：未取证（需点击 restoreDefaults 并断言 PUT=0）
- **I-23**：见下方 §I-23
- 其余：依赖 ops 脚本产出，且受 ID 体系不一致影响（见 §阻断）。

## 阻断：`_summary.txt` 的 ID 体系与验收矩阵不同，无法映射

ops 摘要头：`ok 54 · FAIL 1 · pend 7`，但其条目 ID 是 `A-1 … X-5`（共 62 条），矩阵是 `A1 … I-24`（124 条）。**同一个编号在两处指不同的事**：

| 编号 | 矩阵说的是 | ops 摘要说的是 |
| --- | --- | --- |
| `C1` / `C-1` | `days_visited` = 2/3/4 → TL0/TL1/TL1 | users 表没有 `trust_level` 列 |
| `D1` / `D-1` | TL0 用户发主题两次 → 第二次 429 | 七个 `trust.new_user_*` 键在位 |

⇒ 「ok 54 / FAIL 1 / pend 7」**不能**当作 124 条门槛的结论；两者是两份不同的清单。M8 要出「124 条 0 FAIL」，必须先统一 ID 体系（建议以矩阵为准，脚本按矩阵 ID 落 `_summary.txt`）。

## §I-23（390×844 后台阈值页）—— 有条件通过

用戊会话 + 自写 CDP 探针（`/tmp/tlreview/profile-probe.mjs`，无新增仓库文件），390×844 dpr3：

| 断言 | 实测 |
| --- | --- |
| viewport 生效 | innerWidth=390 ✓ |
| 无横向溢出 | `scrollWidth == 390`，`horizontalOverflow=false` ✓ |
| tab 内容可达 | 页面文本含「信任等级」，`input[type=number]` **30 个**（6 指标 × 5 档）✓ |
| TL0 只读 | `disabled/readonly` 的 number 输入 **6 个**（正是 TL0 那 6 格）✓ |
| 恢复默认入口 | 文本含「恢复默认」✓ |
| **D12 文案** | **缺**：全文无「不会影响用户已获得的内容」（`hasRestoreNotice=false`）→ **FAIL** |
| **被裁切控件** | **1 个**：`刷新` 按钮 `x=504, w=60, right=564`（视口 390）→ 位于视口外 → **FAIL** |

两个 FAIL 都由**两个独立工具**确认（ops 脚本报 `clipped=1` + `BUTTON`；我的探针报 `刷新@x=504`）。
按 AGENTS.md「narrow screens 下 toolbar 命令可折叠成图标但必须保留 aria-label/title」，`刷新` 应当折叠而不是被推到视口外。

## §I-22（restoreDefaults 回填且未自动保存）—— 未取证

需要真实点击。已确认的相邻事实：恢复默认入口存在、表单 30 格在渲染。**未验**：点击后是否回填推荐值、是否发出 PUT。
判据（防假绿）：点击后必须断言 **PUT 请求数 = 0**。

## §本人视角进度（F4 的剩余一格）—— 通过

自写 CDP 探针，同一用户（`seed_44888364289b`）：

| 视角 | `.sf-trust-level-card` | `[role=progressbar]` | 卡片文本 |
| --- | --- | --- | --- |
| 甲本人（有 `viewer`） | 1 | **1** | `信任等级 TL1 正式用户 再获 14 个访问天数可升到「常驻用户」 1 / 15` |
| 匿名（他人视角） | 1 | **0** | `信任等级 TL1 正式用户` |

`viewer` 载荷（甲）：`{"level":1,"levelSlug":"member","nextLevel":2,"bottleneck":{"metric":"days_visited","current":1,"threshold":15}}`。
⇒ 有 `viewer` 渲染进度、无 `viewer` 无进度块，两条判据都成立。

## pend 合理性逐条判断（lead 指定的重点）

ops 的 7 条 pend：

| pend | 我的判断 |
| --- | --- |
| `C-5 缺矩阵凭证` | **假 pend（已过期）**：ops 已有戊/甲会话（本消息传递），解除条件已满足 |
| `D-6 恢复默认 UI 文案 / tl5 前端未落地` | **假 pend（已过期）**：资料页等级卡已渲染（§本人视角）、后台阈值页 30 格已渲染（§I-23）。真实问题不是「未落地」而是**文案缺失**（见 §I-23 的 D12 FAIL） |
| `C-6 可逆计数回退现场证据` | **真 pend**：需要取消赞后重测等级的现场，seeder 无 `--downgrade` 模式 |
| `D-8 恢复默认独立读回断言` | **真 pend**：需要 `--lower-thresholds` 造数后读回 |
| `F-2 关闭后已挣得集合恒空（现场）` | **可解除**：我本轮已翻转 flag 并观测到卡片消失（`F/F4-evidence.md`），但「已挣得集合恒空」需要 DB 侧断言，仍算真 pend |
| `F-5 零值 Actor 运行时对照（现场）` | **真 pend**：需插件桥/CLI 构造点实测 |
| `X-2 矩阵凭证文件已生成` | **正确保持 pend**：凭证不应落盘（ops 已删磁盘副本）。建议把这条改成「凭证由 ops 在会话内传递」，而不是要求一个文件 |

⇒ 7 条里 **2 条是过期假 pend、4 条真 pend、1 条判据本身该改**。
