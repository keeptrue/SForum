# M3.4 夹具 + 产品路径信任弹窗 QA（task-9）

日期：2026-11-12 20:44–21:00（Asia/Shanghai）
执行者：ops（老运）
只动 `extensions/fixtures/**` 与 `knowledge/reports/**`；未改 `apps/api/app/**`（老后同时在写 `Models/Badges/**`），未 commit。

## 1. 夹具清单

### 1.1 回归夹具 `sforum-query-surface-reference`（+53/−2）

`extensions/fixtures/plugins/sforum-query-surface-reference/sforum.extension.json`：

- `badgeConditions` 两条，逐字照 spec §6.2：
  - `sforum.query-surface-reference.onboarding.tasks@1`（`compare:["gte","lte"]` + variants `tutorial`/`profile`）
  - `sforum.query-surface-reference.verified_reports@1`（`compare:["gte"]`，无 variants）
- `capabilities: ["host.api","badges.fact.write"]`
- `database.grants: ["core_views","host_commands"]`
- digest 未手改（`badgeConditions` 不是 packageFile），`extension digest --write` 复算后与已提交值一致。

### 1.2 新夹具 `sforum-badge-facts`（静态清单，非 `.tmpl`）

```
extensions/fixtures/plugins/sforum-badge-facts/
  README.md
  sforum.extension.json         # manifestVersion 3，badgeConditions + events + schemas
  schemas/reaction-created-input.json
  schemas/reaction-created-result.json
  backend/go.mod  backend/go.sum  backend/main.go
```

- `badgeConditions[0]` = `sforum.badge-facts.reactions.given@1`（`compare:["gte","lte"]`，无 variants）
- `events[0]` = observe `reaction.created`，handler `sforum.badge-facts.observe`，input/result schema 各带 `@1`
- `capabilities: ["host.api","badges.fact.write"]`，`docker grants: ["host_commands"]`
- `backend/main.go` 的 `InvokeHook`：
  - 校验 `hook_id`/`hook_name`，先跑 `Server.Health`（与握手绑定的精确制品身份）
  - 从负载取 **真实主体** `actorUserId`（同时接受 JSON 数字与十进制字符串，非正数判失败，不写死用户）
  - `dedupeKey` = `request.GetDeliveryId()`（事件投递 id 即幂等键）
  - 调 SDK `Host.RecordBadgeFact(ctx, parent, pluginv2.BadgeFactInput{...Delta:1...})`
  - Host 回执 `inserted:false` 不视为错误；`total` 由 `BadgeFactRecordResult` 解析成十进制字符串写进 result 文档
- `backend/plugin` 为 gitignored 构建产物（与参考夹具一致）。

## 2. 校验与门禁（原始 exit code）

| 命令 | exit | 关键输出 |
|---|---|---|
| `go run ./cmd/sforum extension validate ../../extensions/fixtures/plugins/sforum-badge-facts` | **0** | `events: 1`、`v3.packageFiles: 3`、`backend: backend/plugin (hashicorp-go-plugin)` |
| `go run ./cmd/sforum extension validate ../../extensions/fixtures/plugins/sforum-query-surface-reference` | **0** | `routes: 1`、`v3.packageFiles: 3` |
| `bash scripts/ci/reference-packages-gate.sh`（20:53 复跑） | **0** | `reference packages checked: 15` / `reference package gate: OK` |
| `go test ./app/Support/ExtensionManifest/...` | **0** | `ok … (cached)` |

门禁「被纳入」证据（`SFORUM_REFERENCE_VERBOSE=1` 日志 `gate2.log`）：

```
  sforum-badge-facts: backend built
      updated .../sforum-badge-facts/sforum.extension.json
        ✓ manifest.ok [sforum.extension.json] — manifest valid: sforum.badge-facts@1.0.0 (plugin)
  sforum-badge-facts: digest + validate + test passed
  ...
  sforum-query-surface-reference: backend built
        ✓ capabilities.resolved [capabilities] — effective capabilities: badges.fact.write, host.api
        ✓ manifest.ok [sforum.extension.json] — manifest valid: sforum.query-surface-reference@1.0.0 (plugin)
  sforum-query-surface-reference: digest + validate + test passed
```

### 2.1 否定用例（自建 harness 在 `~/.cache/dsh-tmp/negcase`，不进仓）

| 变异 | JSON Schema 路径 | Go 路径 |
|---|---|---|
| control（未改） | `nil` | `nil` |
| key 不带前缀（`quiz.answers`） | `nil`（schema 只查字符集） | 拒：`versionedID pattern/ownership failed (family=badgeConditions …)` |
| `compare: []` | 拒：`minItems: got 0, want 1` | 拒：`v3 badgeConditions rule #4` |
| `contractVersion` 漂移 | `nil` | 拒：`contractVersion drift (key=… contractVersion=…)` |

## 3. 产品路径 QA：上传安装 → 启用信任弹窗（390×844）

环境：真实 Chrome 154 headless + CDP `Emulation.setDeviceMetricsOverride(390×844 dpr3)` + `Network.setCookie`（一次性超管 `qa_trust_204905`，id 497，用完删除）。`--no-proxy-server`。

### 3.1 上传安装（先踩到一个真实约束）

- **zip 必须让 `sforum.extension.json` 在压缩包根目录**。第一次打包带了一层 `sforum-query-surface-reference/` 目录 → `POST /api/v1/admin/extensions` 返回 **422**，错误文案「压缩包结构无效，无法作为扩展包读取」。改成 `(cd pkg && zip -qr out.zip .)` 后 → 安装成功，`GET /admin/extensions` 出现 `{"id":"sforum.query-surface-reference","status":"installed"}`。
  - 依据：`app/Support/ExtensionPackage/archive_reader.go:69` 只把**根级** `sforum.extension.json` 当清单。
- 安装只是暂存制品，不启用（页面文案一致）。

### 3.2 启用信任弹窗（截图证据）

- 走插件列表 → 点「启用」→ 签发一次性 exact-artifact 挑战 → 确认按钮「授权当前精确制品」。
- 弹窗里 `data-testid="extension-capabilities"` 段（390 宽）实测：

| 断言 | 实测 |
|---|---|
| 视口 | `innerWidth=390`，`dpr=3` |
| 页面横向溢出 | `documentElement.scrollWidth = 390 <= 390` → 无 |
| 段自身溢出 | `sectionScrollWidth <= sectionClientWidth`，`sectionBox.right <= 391` |
| 能力行 | 2 行：`badges.fact.write` / `host.api` |
| `badges.fact.write` label | `写入插件事实计数`（`labelZh`）/ `Write plugin fact counters`（`labelEn`） |
| 披露句 `[data-capability-disclosure]` | `允许该扩展为本论坛徽章写入自定义事实计数；徽章的公开说明与授予规则可能因此变化。` |
| 控制台异常 | 仅 1 条与本页无关的入口异常记录，未影响渲染 |

截图：`extension-enable-trust-390x844.png`（整屏）、`extension-capabilities-390x844.png`（能力段裁切）。

### 3.3 `GET /admin/badges/conditions` 的 factCandidates 值级断言（HTTP 原始响应）

`HTTP=200`，响应存 `badges-conditions-390-session.json`。断言结果（`ASSERT_EXIT` 首轮为 1，只因我把 `items[].type` 写成了 `key` 口径；改口径后逐条为真）：

| 断言 | 实测 |
|---|---|
| `items[].key` 含 `fact.count` | 是（`items` 13 条，`fact.count` 在列） |
| `factCandidates.length` | 2（都来自刚启用的 `sforum.query-surface-reference`） |
| `label.zh` 长度 > 0 | `完成新手任务数`（7）、`已提交的校验报告数`（9） |
| `label.en` 长度 > 0 | `Completed onboarding tasks`（26）、`Submitted verified reports`（26） |
| `variants[].value` 非空 | `["tutorial","profile"]`；第二条按 spec 无 variants（`[]`，合法） |
| `unit` / `compare` 透传 | `个`/`份`、`["gte","lte"]` / `["gte"]` |

## 4. 清理核查

产品路径顺序：`POST …/disable` → `DELETE /admin/extensions/:id` → `POST …/uninstall-cleanup/finalize`。

| 步骤 | HTTP | 结果 |
|---|---|---|
| disable | 200 | `status: disabled` |
| uninstall | 200 | `{"uninstalled":true}` |
| uninstall-cleanup/finalize | 200 | `{"uninstalled":false}`（无待收敛墓碑） |

一次性账号（id 497）删除并复核：

```
delete from user_roles where user_id=497      -- DELETE 2
delete from user_sessions where user_id=497   -- DELETE 1
delete from user_credentials where user_id=497 -- DELETE 1
delete from users where id=497                -- DELETE 1
→ users/user_roles/user_sessions/user_credentials 计数均为 0
```

探针徽章规则与 run 行：本轮未创建，复核 `badge_rule_backfill_runs=0`、`badges` 里无 `query-surface-reference|badge-facts` 规则 = 0。

扩展残留（**未清零，附原因**）：

| 表 | 计数 | 说明 |
|---|---|---|
| `extensions` | 1（`status=disabled`） | 产品「卸载后保留不可变身份」的墓碑 |
| `extension_versions` | 1（id 33810） | 被 `plugin_runtime_publication_members` 以 `ON DELETE RESTRICT` 引用 |
| `plugin_runtime_publication_members` | 1 | **追加写表**：`BEFORE DELETE OR UPDATE` 触发器 `reject_plugin_runtime_desired_mutation()` 直接 `RAISE EXCEPTION`，`TRUNCATE` 同样被拦 |
| `extension_database_grants/leases/powers` | 0 / 0 / 0 | 已按依赖顺序删除 |

已执行的清理（可复现）：

```sql
delete from extension_events            where extension_id='sforum.query-surface-reference';
delete from extension_trust_challenges where extension_id='sforum.query-surface-reference';
delete from extension_database_runtime_leases where extension_id='sforum.query-surface-reference';
delete from extension_database_grant_powers where grant_id in (select id from extension_database_grants where extension_id='sforum.query-surface-reference');
delete from extension_database_grants  where extension_id='sforum.query-surface-reference';
-- 以下两条被追加写门禁拒绝，未执行成功：
-- update/delete plugin_runtime_publication_members → ERROR: plugin runtime desired publications are append-only
-- delete extension_versions / extensions           → ERROR: violates FK plugin_runtime_publication_me_extension_version_id_extensi_fkey
```

### 4.1 未清零项（Lead 裁决：保持现状，不硬清）

| 表 | 行数 | 为什么不能清 | 为什么不需要清 |
|---|---|---|---|
| `plugin_runtime_publication_members` | 1 | `BEFORE DELETE OR UPDATE` 触发器 `reject_plugin_runtime_desired_mutation()` 直接 RAISE，`TRUNCATE` 同样被拦；这是「不可变发布成员」的产品不变量 | 它是运行时发布历史的组成部分，删除等于破坏完整性换一张好看的表；不属于本轮验收对象 |
| `extension_versions`（id 33810） | 1 | 被上表以 `ON DELETE RESTRICT` 引用，行本身无法物理删除 | 卸载后的不可变身份记录，产品设计明确保留 |
| `extensions`（`status=disabled`） | 1 | 其 `active_version_id`/`staged_version_id` 与上表的版本行绑定 | 卸载墓碑（保留以便审计），不是脏数据；不影响任何断言，也不影响后续门禁 |

`DISABLE TRIGGER` 属于「为了让证据好看而破坏完整性」，本仓明确禁止；本轮**不再尝试删除**这三行。其余探针数据（一次性账号、events、grants/leases/powers、徽章规则与 run 行）均已清零并复核。

### 4.2 范围外观察（不在徽章这一刀改）

`DELETE /admin/extensions/:id` 返回 `uninstalled:true`，而库里扩展行仍是 `disabled` 墓碑（identity-retained 路径）。语义容易误读，但属扩展平台的既有行为，已报 Lead 记入收口报告的「范围外观察」。

## 5. 产物

- `probe-install-390.mjs`：上传安装驱动（含 zip 根目录约束的失败取证）
- `probe-enable-390.mjs`：插件列表 → 启用 → 信任弹窗测量 + 截图 + 授权
- `extension-enable-trust-390x844.png`、`extension-capabilities-390x844.png`
- `badges-conditions-390-session.json`：`GET /admin/badges/conditions` 原始响应
