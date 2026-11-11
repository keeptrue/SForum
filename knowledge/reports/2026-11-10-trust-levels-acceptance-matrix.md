# 信任等级 — 验收执行矩阵（谁验 / 怎么验 / 证据放哪）

Source: `reports/2026-11-10-trust-levels-capability-map.md` §6 + lead 的 M4 端点清单
Owner: pm（阿策）
Status: **第二版（2026-11-10）**。**2026-11-11 修订：I-16 判据不可满足，已改为 I-16a/I-16b（见 v3 §11 与本文件 §3 I 组后的留档），其余 123 条不变。** 第一版「怎么验」列写了 30 个**不存在的测试名**（小审实测：`-run` 匹配不到时退出码 0、输出 `ok`，等于 29 条永远绿）。本版所有测试名来自 `go test -list '.*'` 实测清单，并新增**证据规则**（§1.4）与**对照表**（§7）。总数仍 124 条（I-16 拆分后内部为 a/b 两条断言，ID 不增）。

---

## 0. M4 端点复核结果

| # | 结论 | 状态 |
| --- | --- | --- |
| 0-1 | 公开端点形状与 lead 表格一致 | ✅ |
| 0-2 | 公开响应**无 `limits`**：`dto.go` 是 `*trustLevelLimits \`json:"limits,omitempty"\``，公开路径传 nil → 键整个消失（不是 `null`）。**但 OpenAPI 契约与之矛盾**（`paths/trust_levels.yaml:6`、`schemas:48` 的 `required`） | ✅ 已裁决：**改契约**，不改代码 |
| 0-3 | PUT 请求体 `{config}`，bind 失败 → 422 `validation.invalid`；decode 失败 → 422 `trust_levels.invalid_config` | ✅ |
| 0-4 | 契约承诺「`limits`/`earnable` 省略即 422」，代码原本静默通过 | ✅ 已裁决：**指针化改代码**（`Limits *LevelLimits`、`Earnable *[]string`），省略 → 422，错误信息说明「省略会让该档限额被静默重置为全继承」。**现已有测试** `TestDecodeConfigRequiresLimitsAndEarnable` + `TestConfigRoundTrip` |
| 0-5 | PUT 权限是两层：控制器判 `admin.access`，写权限 `trust.manage` 由 `options.Update` 按 `managePermission` 再判 | ⚠️ 判据写 403（不是 500/200） |
| 0-6 | 关闭态四条路由各 404 | ✅ 已有测试 `TestFeatureDisabledReturns404OnEveryRoute`（四条全枚举） |
| 0-7 | `viewer` 恒取 session actor → **本端点无法验「他人视角」** | 移到 M5 资料页（§2.2） |

---

## 1. 公共前置

### 1.1 服务与命令

| 项 | 值 |
| --- | --- |
| API | `http://127.0.0.1:8080/api/v1`（`./scripts/api-dev.sh`） |
| Web | `http://127.0.0.1:3000`（用户自己的，**不要 kill**）；QA 另起 3001 |
| DB | `docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum` |
| 证据根 | **`storage/qa/trust-levels/`** |

```bash
API=http://127.0.0.1:8080/api/v1
ORIGIN=http://127.0.0.1:3000
EV=storage/qa/trust-levels
Q()  { docker exec -e PGPASSWORD=sforum sforum-postgres psql -U sforum -d sforum -tAc "$1"; }

login() { # $1=email $2=password $3=jar
  curl -s -c "$3" "$API/auth/session" -o /dev/null
  local t; t=$(awk '/csrf_/{v=$7} END{print v}' "$3")
  curl -s -b "$3" -c "$3" -X POST "$API/auth/login" \
    -H 'Content-Type: application/json' -H "X-CSRF-Token: $t" \
    -H "Origin: $ORIGIN" -d "{\"login\":\"$1\",\"password\":\"$2\"}" -o /dev/null -w 'login=%{http_code}\n'
}
call() { # $1=jar $2=METHOD $3=/path [$4=json]
  local t; t=$(awk '/csrf_/{v=$7} END{print v}' "$1")
  curl -s -b "$1" -c "$1" -X "$2" "$API$3" -H "X-CSRF-Token: $t" \
    -H 'Content-Type: application/json' -H "Origin: $ORIGIN" ${4:+-d "$4"} -w '\nHTTP=%{http_code}\n'
}
```

### 1.2 账号矩阵

| 代号 | 角色/等级 | 用途 |
| --- | --- | --- |
| 甲 | member，TL0 | TL0 限额、`viewer` 瓶颈 |
| 乙 | member，TL2 | 分档限额、`viewer.nextLevel` |
| 丙 | member，TL4 | `viewer` 无 `nextLevel`/`bottleneck` |
| 丁 | 仅 `admin.access`，无 `trust.manage` | I-10 |
| 戊 | `admin.access` + `trust.manage` | I-11/I-13 |
| 己 | super_admin | A7/A8、对照组 |
| 匿 | 无 cookie | 公开端点、I-03 |

### 1.3 开关与配置读写

```bash
Q "INSERT INTO options(name,value) VALUES('features.trust_levels','disabled')
   ON CONFLICT (name) DO UPDATE SET value='disabled';"
Q "SELECT value FROM options WHERE name='trust.levels';"
```

### 1.4 证据规则（**本版新增，比条目本身更重要**）

| 规则 | 内容 |
| --- | --- |
| **R1 测试名必须实测存在** | 本文件所有 `Test*` 名都来自 `go test -list '.*'`。**执行时先跑一次 `-list` 校验**：名字不在清单里 → 该条记 **FAIL（未执行）**，不是 PASS |
| **R2 `ok` 和退出码 0 都不是证据** | `-run` 匹配不到测试时**退出码仍是 0、输出仍是 `ok`**（小审实测）。所有测试类条目必须用 `-count=1 -v -run '^Name$'`，并断言输出里出现 **`=== RUN   Name`**；只有 `ok … [no tests to run]` 或没有 `=== RUN` 的，判 FAIL |
| **R3 依赖 DB 的条目必须走 isolated harness** | 裸 `go test` 在缺 `SFORUM_TEST_DATABASE_URL` 时会 **SKIP 且 exit 0**（小审实测 `TestUpdateTrustLevelsPersistsNormalizedConfig`）。写法：`./scripts/test-api-isolated.sh ./app/Models/Options/ -run '^Name$'`（非 flag 参数会透传给 `go test`）。出现 SKIP → 记 **SKIP（有交代）**，不记 PASS |
| **R4 SKIP/PEND 必须有交代** | M8 门槛：0 FAIL，且每个 SKIP/PEND 写明「环境限制」还是「待落地（谁、哪个里程碑）」。「没法验」不是交代 |
| **R5 否定式断言用 `has()`** | `jq 'has("limits") == false'`，不要 `.limits == null`（后者在「键存在且为 null」时为真，会把漏字段判成通过） |

---

## 2. 三类专项

### 2.1 公开端点的字段边界（逐字段否定）

```bash
call "" GET /trust-levels | sed '$d' | jq -e '
  (.levels | length == 5) and
  ([.levels[] | has("limits")] | any | not) and
  ([.levels[].requirements | keys] | all(. == ["bookmark_count","comment_count","days_visited","like_given_count","like_received_count","topic_count"]))
'
```

必须逐字段否定的位置：每档 `limits`、顶层 `config` / `eligiblePermissions` / `recommendedConfig`、匿名时 `viewer`、以及任何 `trust.level.*` / `trustLevels.*` 形态的键。

### 2.2 `viewer` 只对本人：本端点能验什么、不能验什么

| 视角 | 本端点行为 | 断言 |
| --- | --- | --- |
| 匿名 | 无 `viewer` 键 | `has("viewer") \| not` |
| 本人（已解析） | `{level, levelSlug, nextLevel?, bottleneck?}` | `bottleneck.metric` ∈ 六 metric，`current < threshold` |
| 本人 TL4 | 只有 `level` + `levelSlug` | `has("nextLevel")==false and has("bottleneck")==false` |
| 本人未解析 | 无 `viewer` | 关→开切换后首个请求 |
| **他人视角** | **本端点无入口** | **移到 M5 资料页**：甲登录看乙资料页 → 只有 `level` + `levelSlug`，无 `progress`/`bottleneck`/剩余量。数据源是 profile 载荷的 `trustLevel` 字段（backend 正在加；没加则 FAIL，不 pend） |

### 2.3 关闭态 404：四条各一次 + 判定顺序

```bash
for spec in "GET /trust-levels" "GET /admin/trust-levels" \
            "PUT /admin/trust-levels" "POST /admin/trust-levels/restore-defaults"; do
  set -- $spec
  call "" "$1" "$2" '{"config":"{}"}' | tee -a "$EV/I/I-14-closed-404.txt"; done
```

期望：四行全 404，且 body 的 code 全是 `trust_levels.not_found`（403 说明开关判定被权限判定抢先）。

---

## 3. 验收矩阵（124 条）

**「怎么验」列的两类写法**：
- `T: ^Name$ @ pkg` = `go test -count=1 -v -run '^Name$' ./pkg/`，**须见 `=== RUN Name`**（R2）。
- `ISO: ^Name$ @ pkg` = `./scripts/test-api-isolated.sh ./pkg/ -run '^Name$'`，出现 SKIP 记 SKIP 不记 PASS（R3）。

### A 能力解析（14）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| A1–A4, A7–A9 | ops | `T: ^TestCanTruthTable$ @ app/Models/Identity`（表驱动，静态×earned×deny×super_admin 四组） | `A/A1-A9.txt` |
| A5–A6 | ops | `T: ^TestMergeEarnedPermissionsHonoursDeny$ @ app/Models/Identity` + `T: ^TestDeniedPermissionKeysIncludesDenyOnlyKeys$ @ 同包` | `A/A5-A6.txt` |
| A10 | ops | `T: ^TestActorZeroValueHasNoEarned$ @ app/Models/Identity` | `A/A10.txt` |
| A11–A12 | ops | **无现成测试**（`CapabilitySource` 无测试覆盖）→ 记 **PEND**，交代：待 backend 在 M2 收尾补 `TestCapabilitySource*`；期间用后台用户详情手工对照一次（`loadAdminUserAccess` 不合并 earned） | `A/A11-A12-pend.md` |
| A13 | ops | `T: ^TestMergeEarnedPermissionsSkipsStaticKeys$ @ app/Models/Identity` | `A/A13.txt` |
| A14 | ops | **无现成测试** → 跑 `T: ^TestCanTruthTable$` 两次（本刀前 commit / 当前），`diff` 输出；记 **PEND** 若无法切 commit | `A/A14-before.txt` / `A/A14-after.txt` |

### B 边界隔离（11）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| B1–B2 | ops | `T: ^TestEarnableValidateRejectsDangerousKeys$ @ app/Models/TrustLevels`（子用例：管理类 `user.ban`、`moderation.review`、父键 `settings.manage`/`extension.manage`/`user.manage`、不存在键、空键） | `B/B1-B2.txt` |
| **B3–B4** | rev | **变异验证，真守卫是 `T: ^TestEligiblePermissionsIsLiteralWhitelist$`**（字面集合相等）。往白名单塞 `user.ban` → 该测试必须红 → 改回必须绿 | `B/B3-red.txt` + `B/B3-green.txt` |
| B5 | ops | `T: ^TestEligiblePermissionsIsLiteralWhitelist$`（字面 7 键，且显式断言 7 个禁键不泄漏） | `B/B5.txt` |
| B6 | ops | `rg -c 'Key: Permission' apps/api/app/Models/Identity/seeds.go` = 51 | `B/B6.txt` |
| B7 | rev | 代码审阅：catalog 来源是 Go 常量而非正则扫迁移（`registry.go`） | `B/B7-review.md` |
| B8 | ops | `T: ^TestDefaultEarnableIsEmpty$ @ app/Models/TrustLevels` | `B/B8.txt` |
| B9 / B9b | ops | **同 B1-B2 的父键子用例**（`settings.manage` / `extension.manage` / `user.manage` 三例都已在 `TestEarnableValidateRejectsDangerousKeys` 里）；白名单面另由 `TestEligiblePermissionsIsLiteralWhitelist` 的字面集合覆盖 | `B/B9.txt` |
| B10 | ops | `T: ^TestEligiblePermissionsIsLiteralWhitelist$`（同一测试，不同断言角度：7 键逐字相等） | `B/B10.txt` |

> **B1 判据纠正（小审实测）**：`TestEarnableValidateCoversWholeCatalog` **不是**变异守卫——它只断言「白名单 ↔ Validate 一致」，白名单扩了它两边一起动、仍然绿。真正的守卫是 `TestEligiblePermissionsIsLiteralWhitelist` 的字面集合。

### C 升降级（20）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| C1–C6 | ops | `T: ^TestLevelBoundaries$ @ app/Models/TrustLevels`（六指标 × threshold-1/threshold/threshold+1） | `C/C1-C6.txt` |
| C7 | ops | `T: ^TestLevelAgeGate$`（窗口内最高 TL1，含「活动够也到不了 TL2」） | `C/C7.txt` |
| C8 | ops | `T: ^TestLevelMetricsAreIndependentlyRead$`（每指标独立生效）+ `T: ^TestLevelBoundaries$` | `C/C8.txt` |
| C9 | ops | `T: ^TestLevelMetricsAreIndependentlyRead$`（六项全满足才升，AND 语义） | `C/C9.txt` |
| C10–C12 | ops | `T: ^TestLevelReverseCountsDemote$` | `C/C10-C12.txt` |
| C13–C15 | ops | **无测试**（软删/驳回/封禁不改计数是 `user_activity` 的性质）→ psql 改 `status` 后读 `Q "SELECT topic_count,…"`，再 `GET /trust-levels` 对比 `viewer.level` | `C/C13-C15-psql.txt` |
| C16 | ops | psql 同日插 5 行 `user_visits`（PK 冲突应忽略）→ 读 `days_visited` | `C/C16.txt` |
| C17–C18 | ops | `T: ^TestLevelAgeGate$` | `C/C17-C18.txt` |
| C19 | ops | `T: ^TestLevelNonMonotonicPolicyKeepsHighest$` | `C/C19.txt` |
| C20 | ops | psql 查降级后 topics/comments 行数与 `status` | `C/C20.txt` |

### D 限额收编（10）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| D1 | ops | 甲连发两次（间隔 <300s）→ 429 `forum.topic_cooldown`；支撑测试 `T: ^TestCooldownErrorPreservesCauseAndRetryAt$ @ app/Models/Forum` | `D/D1.txt` |
| D2–D3 | ops | 甲当日第 4 帖 → 429 `forum.daily_topic_limit`；含 `http://` → 拒 | `D/D2-D3.txt` |
| D4 | ops | `T: ^TestLimitsForActorLevelOneHasNoNewUserTightening$ @ app/Models/Forum` + 乙间隔 1s 连发 200 | `D/D4.txt` |
| D5 | ops | `T: ^TestLimitsForActorDaysZeroDisablesLadder$` | `D/D5.txt` |
| D6 | ops | `T: ^TestLimitsForActorKeepsFiveCriteria$`（含员工豁免）+ 新建 moderator（age=1d）连发 200 | `D/D6.txt` |
| D7 | ops | `T: ^TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots$` + `T: ^TestResolverSkipsNonHumanAccounts$ @ TrustLevels` | `D/D7.txt` |
| D8 | ops | `T: ^TestLevelZeroCreatedAtIsStrict$ @ TrustLevels` | `D/D8.txt` |
| D9 | ops | 甲仍能发主题（`Can(topic.create)`=true）+ `T: ^TestLimitsForActorKeepsFiveCriteria$` | `D/D9.txt` |
| D10 | ops | 关开关后重跑 D1–D4，与基线 diff | `D/D10-baseline.diff` |

### E 失败关闭（7）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| E1–E3 | ops | `T: ^TestResolverFailClosed$ @ TrustLevels` + `T: ^TestResolverNilDependenciesFailClosed$` | `E/E1-E3.txt` |
| E4 | ops | `T: ^TestActorZeroValueHasNoEarned$ @ Identity` | `E/E4.txt` |
| E5 | ops | **无测试**（`Level()` 纯度无断言）→ 记 **PEND**，交代：待 backend 补「fake store 调用计数 == 0」；期间用 `T: ^TestLevelBoundaries$` 无 DB 依赖间接证明 | `E/E5-pend.md` |
| E6 | ops | 同 E1 场景断言 `len(earned)==0`（测试内已断言，读输出确认） | `E/E6.txt` |
| E7 | ops | `T: ^TestLimitsForActorKeepsFiveCriteria$ @ Forum`（五道判据逐条） | `E/E7.txt` |

### F 开关（9）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| F1 | ops | 关开关 → `T: ^TestCanTruthTable$` + 51 键快照 diff | `F/F1.diff` |
| F2 | ops | 关开关 → `GET /trust-levels` 404（同 I-14）；earned 空集由 `T: ^TestTrustLevelDisabledSentinelsAreIdentical$ @ Identity` 佐证 | `F/F2.txt` |
| F3 | ops | 关开关 → `call "" GET /trust-levels` → 404 | `F/F3.txt` |
| F4 | rev | 关开关 → 浏览器开资料页，等级卡**元素不存在**（不是隐藏） | `F/F4-profile-off.png` |
| F5 | ops | 关开关 → 戊 `GET /admin/trust-levels` → 404（开关先于权限） | `F/F5.txt` |
| F6 | ops | 开开关 → `jq '.levels[].requirements'` 与 `DefaultPolicy()` 逐值比对（`T: ^TestDefaultEarnableIsEmpty$` 只覆盖 earnable，阈值靠 jq） | `F/F6.json` |
| F7 | ops | `T: ^TestFeatureFlagsRecommendedDefaultsAndRestore$ @ app/Models/Options` | `F/F7.txt` |
| F8 | ops | `rg -n 'NameFeatureTrustLevels' apps/api/app/Models/Options/feature_flags.go` → 5 处 + 读取 | `F/F8.txt` |
| F9 | ops | **无完整性测试**（常量数 == Catalog 条目数）→ 记 **PEND**，交代：待 backend 补；期间用 F8 的 grep 逐处核对 | `F/F9-pend.md` |

### G 性能（6）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| G1–G2 | ops | `hey -n 500 -c 20` 未认证/已认证各一轮，对比本刀前 p95 | `G/G1-G2.txt` |
| G3 | ops | `pg_stat_statements` 计数：每请求 `user_activity` 恰 1 次（**无测试**） | `G/G3.txt` |
| G4 | ops | `Q "SELECT indexname FROM pg_indexes WHERE tablename='user_activity'"` → 仅 PK | `G/G4.txt` |
| G5 | ops | 20 条主题的列表请求，`user_activity` 读取计数 == 1 | `G/G5.txt` |
| G6 | ops | 单请求 `LoadActor` 调用次数实测（45 个调用点，按 +N 计） | `G/G6.txt` |

### H 契约与门禁（17）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| H1 | ops | `ruby scripts/validate-openapi-refs.rb` | `H/H1.txt` |
| H2 | ops | `jq` 对比响应与 `schemas/trust_levels.yaml`（注意 §0-2 契约矛盾，先按裁决修契约） | `H/H2.json` |
| H3 | ops | 戊 PUT → 200；丁 PUT → 403（见 I-10） | `H/H3.txt` |
| H4 | ops | 四处 grep：`seeds.go` / `202611100004_*.sql` / 两份 locale / `roleTemplates.ts:69`；迁移侧另有现成测试 `ISO: ^TestTrustManagePermissionMigrationShape$ @ database/migrations` 与 `ISO: ^TestTrustManagePermissionMigrationIsIdempotent$`（DB 依赖） | `H/H4.txt` |
| H5 | ops | `ISO: ^TestEverySeededPermissionReachesTheDatabase$ @ app/../database/migrations`（DB 依赖 → isolated） | `H/H5.txt` |
| H6 | ops | `jq` 读两个真实键族各 5 条 × 双语（`profile.trustLevel.name.*` / `admin.settings.trustLevels.level.*`）；**无测试，纯 jq** | `H/H6.json` |
| H7 | ops | `node tests/validate-architecture-boundaries.mjs` | `H/H7.txt` |
| H8 | ops | 同上（单列便于定位） | `H/H8.txt` |
| H9 | ops | V3 catalog `--check` + `validate-v3-p0-catalogs` | `H/H9.txt` |
| H10 | ops | `scripts/verify-migration-reversible.sh` + `git diff --stat apps/api/database/migrations` | `H/H10.txt` |
| H11–H12 | ops | 关 `features.reactions` / `features.bookmarks` → 造数后 `GET /trust-levels` 看等级仍可达 TL2/TL3 | `H/H11-H12.txt` |
| H13 | — | 已由 D18 作废，由 H15 取代 | — |
| H14 | ops | `go list -deps ./app/Models/Identity` 不含 `TrustLevels`/`Badges`/`Options` | `H/H14.txt` |
| H15 | rev | **变异**：改 `Badges.Activity` 字段名 → `go build ./...` 必须失败；改回必须绿 | `H/H15-red.txt` + `H/H15-green.txt` |
| H16 | ops | `T: ^TestLevelSlugIsNotRoleKey$ @ TrustLevels` + `jq` 断言字段名是 `levelSlug`、全文无裸 `slug` | `H/H16.txt` |
| H17 | ops | `jq` 两个键族五档 zh/en 值 + zh 集合 ∩ `admin.roleCatalog.*` == ∅（en 交集 `{Member}` 已知） | `H/H17.json` |

### JSON 形状（6）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| H18 | ops | `T: ^TestDecodeConfigValidation$ @ TrustLevels`（含 TL0/重复/越界）+ PUT 实测 422 | `H/H18.txt` |
| H19 | ops | `T: ^TestDecodeConfigRejectsUnknownField$`（幻影字段 `account_age_days` 回归）+ PUT 实测 | `H/H19.txt` |
| H20 | ops | `T: ^TestDecodeConfigAcceptsValid$` + PUT camelCase 200 / snake_case 422 | `H/H20.txt` |
| H21 | ops | `T: ^TestLimitsNullRoundTrip$`（null vs 0 语义）+ `T: ^TestConfigRoundTrip$` | `H/H21.txt` |
| H22 | ops | `T: ^TestEarnableValidateRejectsDangerousKeys$` + PUT 实测 422 | `H/H22.txt` |
| H23 | ops | `T: ^TestDecodeConfigEmptyFallsBackToDefault$`（空串 → 默认）+ PUT 实测 | `H/H23.txt` |

### I 组 — M4 端点行为（24）

| ID | 谁验 | 怎么验 | 证据 |
| --- | --- | --- | --- |
| I-01/I-02 | ops | `call "" GET /trust-levels` + jq 断言 5 档与 slug 顺序 | `I/I-01.json` |
| I-03 | ops | 匿 → `has("viewer") == false` | `I/I-03.txt` |
| I-04/I-05 | ops | 甲/丙登录 → viewer 形状（TL4 无 nextLevel/bottleneck） | `I/I-04.json` / `I/I-05.json` |
| **I-06** | ops | `T: ^TestPublicPayloadHasNoLimits$ @ app/Http/Controllers/TrustLevels` + §2.1 的 jq 否定断言 | `I/I-06.txt` |
| I-07/I-08 | ops | `T: ^TestPayloadArraysAreNeverNull$` + jq 断言无 `config`/`eligiblePermissions`/`recommendedConfig`、requirements 键集合精确相等 | `I/I-07.txt` |
| I-09 | ops | 戊 `GET /admin/trust-levels` → 200，四字段齐全 | `I/I-09.json` |
| I-10 | ops | 丁 PUT → **403**（不是 500/200）；支撑 `T: ^TestAdminRoutesRequirePermissions$ @ Controllers/TrustLevels` | `I/I-10.txt` |
| I-11 | ops | 戊 PUT 合法 config → 200，形状同 GET admin | `I/I-11.json` |
| **I-12** | ops | **判据（lead 裁决）**：省略 `limits` → **422** 且错误信息含「省略」/「limits」且可读；省略 `earnable` → 422；对照组 `limits: null` + `earnable: []` → 200。测试：`T: ^TestDecodeConfigRequiresLimitsAndEarnable$` + `T: ^TestConfigRoundTrip$` + `T: ^TestUpdateRejectsInvalidConfig$ @ Controllers` | `I/I-12-omit-limits.txt` / `I/I-12-omit-earnable.txt` / `I/I-12-control-200.txt` |
| I-13 | ops | `POST …/restore-defaults` → 200 且回推荐值 | `I/I-13.json` |
| I-14 | ops | `T: ^TestFeatureDisabledReturns404OnEveryRoute$ @ Controllers`（四条全枚举）+ §2.3 的 curl 循环 | `I/I-14-closed-404.txt` |
| I-15 | ops | 404 body code 全为 `trust_levels.not_found` | `I/I-15.txt` |
| **I-16（已修订 2026-11-11，见 v3 §11）** | ops | 原判据「关→开后首个 GET 无 `viewer`」**不可满足**（`LoadActor` 是请求级，恢复后首个请求必然已解析）。改为两条正面断言：**I-16a** 关闭期间任何请求无 `viewer`（引用 F3/I-15）；**I-16b** 恢复后首请求 `viewer` 在、`level ∈ {0..4}`、`bottleneck` 若存在则 `current < threshold`（**不在 shell 里重算档位**：等级 = 六项计数 + 账龄两段式，见 v3 §1.4） → 完整判据见 `2026-11-11-trust-levels-acceptance-matrix-v3.md` §11 | `I/I-16a.txt` + `I/I-16b.json` |
| I-17/I-18 | ops | 匿名 admin GET → 401；普通 member → 403 `admin.access_required` | `I/I-17.txt` |
| I-19/I-20 | ops | `newUserDays` == `trust.new_user_days`（改 14 → 重启 → 复读） | `I/I-19.txt` / `I/I-20.txt` |
| I-21 | ops | `eligiblePermissions` 恰 7 键且不含 `trust.manage`（`TestEligiblePermissionsIsLiteralWhitelist` 佐证） | `I/I-21.json` |
| I-22/I-23 | rev | 浏览器：restoreDefaults 只回填不自动保存；390×844 无横向溢出 | `I/I-22.png` / `I/I-23-390x844.png` |
| I-24 | ops | PUT 后 `Q "SELECT value FROM options WHERE name='trust.levels'"` 与响应 `config` 逐字节相同（DB 依赖，需 isolated 或 dev 库） | `I/I-24.txt` |

> **I-16 修订留档（2026-11-11）**：原判据与判据依据如下，**不再执行**，仅作历史证据。
>
> `I-16` ｜ ops ｜ 关→开切换后首个 GET：`has("viewer")==false` ｜ `I/I-16.txt`
>
> 错因：`viewer` 由 `apphttp.LoadActor` **每请求**装载（`auth_user.go:47`），开关恢复后首个请求即已解析态；
> 「未解析」只存在于「装载时开关是关的」那个请求里，而那个请求已被 F3/I-15 的 404 覆盖。
> jq 的 `.data` 层是对的——**错的是期望值，不是信封**。新判据见 v3 §11（I-16a / I-16b）。

---

## 4. 明确**不**计入 FAIL

| 项 | 归属 | 理由 |
| --- | --- | --- |
| 控制器 5 条单测 | backend 自测 | **但本版把它们当「佐证」列进 I-06/I-07/I-10/I-12/I-14**（存在且可跑，是加分证据，不计 FAIL 计数） |
| `sessionPolicyStore` 未接 `TrustLevelSource` | 已知不做 | 该路径只判 super_admin，可挣得键碰不到 |

---

## 5. 证据目录

```
storage/qa/trust-levels/
  A/ … I/
  <ID>.txt / .json / .png
  <ID>-red.txt / -green.txt      # 变异红绿两态
  <ID>-pend.md                   # PEND 的交代（环境限制 / 待落地给谁）
  _summary.txt                   # 脚本生成：每 ID 一行 PASS/FAIL/SKIP/PEND + 原因
```

规则：一条 ID 一个文件；`_summary.txt` 由脚本生成；浏览器证据带视口尺寸；变异必须红绿两份都在（T10）。

---

## 6. 派修

| 派给 | 事项 |
| --- | --- |
| backend | 补 4 个缺失测试：`CapabilitySource`（A11/A12）、`Level()` 纯度（E5）、flag catalog 完整性（F9）、`LoadActor` 查询计数（G3/G6）；并给 I-12 的指针化补 `Encode→Decode` 往返断言（`TestConfigRoundTrip` 已有，确认覆盖 limits/earnable 两字段） |
| backend | 修契约（§0-2） |
| ops | 按 §1.4 的 R1–R3 起脚本骨架，**先跑 `go test -list` 校验名字存在**，再跑 I 组 |
| rev | 变异 2 处（B3/B4 用 `TestEligiblePermissionsIsLiteralWhitelist`、H15）+ 浏览器 3 处（F4/I-22/I-23） |
| pm | M5 资料页「他人视角」条目等 profile `trustLevel` 字段落地后另立 |

---

## 7. 矩阵名 → 真实名 对照表（小审按此复跑）

| 第一版矩阵里写的（**多数不存在**） | 真实存在 | 所在包 |
| --- | --- | --- |
| `TestCanTruthTable`（我写对了一半：写成 `TestCanStatic`/`TestCanEarnedMerge`/`TestCanDenyOverridesEarned`/`TestCanSuperAdmin`/`TestCanInactive`/`TestCanAdditiveOnly`） | **`TestCanTruthTable`** | `app/Models/Identity` |
| `TestCanDenyOverridesEarned` | `TestMergeEarnedPermissionsHonoursDeny` | Identity |
| `TestActorZeroValue` / `TestZeroActorNoEarned` | `TestActorZeroValueHasNoEarned` | Identity |
| `TestDeniedKeys…`（未写全） | `TestDeniedPermissionKeysIncludesDenyOnlyKeys` | Identity |
| `TestCapabilitySource` | **不存在** → PEND | — |
| `TestCanCatalogSnapshot` | **不存在** → 手工 diff | — |
| `TestEarnableDisjointFromAdmin` | `TestEarnableValidateRejectsDangerousKeys` + `TestEligiblePermissionsIsLiteralWhitelist` | TrustLevels |
| `TestEligibleExcludesParentKeys` / `TestConfiguredEarnableExcludesParentKeys` | 并入 `TestEarnableValidateRejectsDangerousKeys`（三父键子用例） | TrustLevels |
| `TestEligibleExactlySeven` | `TestEligiblePermissionsIsLiteralWhitelist` | TrustLevels |
| `TestDefaultEarnableEmpty` | `TestDefaultEarnableIsEmpty` | TrustLevels |
| `TestLevelBoundary` | `TestLevelBoundaries` | TrustLevels |
| `TestNewUserWindowCapsAtTL1` / `TestAccountAgeGate` | `TestLevelAgeGate` | TrustLevels |
| `TestLeaderThresholds` / `TestAllMetricsRequired` | `TestLevelMetricsAreIndependentlyRead` | TrustLevels |
| `TestReversibleDowngrade` | `TestLevelReverseCountsDemote` | TrustLevels |
| `TestNonMonotonicConfig` | `TestLevelNonMonotonicPolicyKeepsHighest` | TrustLevels |
| `TestZeroCreatedAtSkipsLimits` | `TestLevelZeroCreatedAtIsStrict` | TrustLevels |
| `TestFailClosedOnStoreError` / `…MissingRow` / `…Timeout` | `TestResolverFailClosed`（+ `TestResolverNilDependenciesFailClosed`） | TrustLevels |
| `TestLevelIsPure` | **不存在** → PEND | — |
| `TestFlagOffNoEarned` | `TestTrustLevelDisabledSentinelsAreIdentical`（仅哨兵一致性）+ HTTP 快照 | Identity |
| `TestFeatureFlagCatalogComplete` | **不存在** → PEND | — |
| `TestLoadActorQueryCount` | **不存在** → pg_stat 实测 | — |
| `TestSoftDeleteNoDowngrade` | **不存在** → psql + HTTP | — |
| （第一版完全没写） | `TestLimitsForActorKeepsFiveCriteria` | `app/Models/Forum` |
| （未写） | `TestLimitsForActorDaysZeroDisablesLadder` | Forum |
| （未写） | `TestLimitsForActorLevelOneHasNoNewUserTightening` | Forum |
| （未写） | `TestResolveLimit` | Forum |
| （未写） | `TestServiceCreateCommentAppliesNewUserLinkRuleButSkipsBots` | Forum |
| （未写） | `TestCooldownErrorPreservesCauseAndRetryAt` | Forum |
| （第一版写「控制器无测试」） | `TestFeatureDisabledReturns404OnEveryRoute` | `app/Http/Controllers/TrustLevels` |
| （未写） | `TestAdminRoutesRequirePermissions` | Controllers/TrustLevels |
| （未写） | `TestUpdateRejectsInvalidConfig` | Controllers/TrustLevels |
| （未写） | `TestPublicPayloadHasNoLimits` | Controllers/TrustLevels |
| （未写） | `TestPayloadArraysAreNeverNull` | Controllers/TrustLevels |
| （未写） | `TestDecodeConfigRequiresLimitsAndEarnable` | TrustLevels |
| （未写） | `TestConfigRoundTrip` | TrustLevels |
| （未写） | `TestLimitsNullRoundTrip` | TrustLevels |
| （未写） | `TestDecodeConfigAcceptsValid` / `…RejectsUnknownField` / `…Validation` / `…EmptyFallsBackToDefault` | TrustLevels |
| （未写） | `TestEarnableGrantsCumulative` / `TestLevelSlugIsNotRoleKey` / `TestResolverGrants` / `TestResolverSkipsNonHumanAccounts` | TrustLevels |
| （未写） | `TestFeatureFlagsRecommendedDefaultsAndRestore` / `TestNormalizeOptionValueCoversTrustLevels` | `app/Models/Options` |
| （未写，**DB 依赖 → 必须 isolated**） | `TestUpdateTrustLevelsPersistsNormalizedConfig` / `TestInvalidTrustLevelsWriteIsAtomic` | Options |
| （未写，**DB 依赖 → 必须 isolated**） | `TestEverySeededPermissionReachesTheDatabase` | `database/migrations` |
| （未写） | `TestCoreRouteCatalogHasExactReviewedGuardParity` | `app/Support/Routes` |

**共 30 个第一版名字：29 个不存在（1 个 `TestCanTruthTable` 是我在正文里偶然写对的），已全部替换或标 PEND。**
