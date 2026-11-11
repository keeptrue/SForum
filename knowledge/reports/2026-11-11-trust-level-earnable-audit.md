# 信任等级 earnable 反向审计（D33 A 路前置）

Status: **完成（2026-11-11 09:18–09:35 +08:00）**
审计对象：`knowledge/plans/2026-11-11-trust-levels-closing.md`（下称「收尾计划」）
基线：`HEAD = 55ee9c366`，`go1.27.1 linux/amd64`，工作树**脏**（frontend/backend/writer 正在并行改，见 §7）
证据等级：**观察级**（探针实跑 + 逐行核对），不是提交级。提交级由 ops 在收尾时回填 `git log -S`。

审计口径：**不照抄计划**。计划 §2 的十条「已核实事实」我逐条重核，其中 1 条前提不成立（§6-R5），
3 条需要加限定（§6-R1/R2/R3）。凡「X 存在 / X 不存在 / X 零消费者」均附命令与时间戳。

---

## 0. 方法与可复现性

本报告的关键结论不是读代码推出来的，是**跑出来的**。因为 `TrustLevels` 的 `Earnable` 与
`Identity` 的 `mergeEarnedPermissions` 都在包内/包外可测，我用 `go test -overlay` 注入临时探针
文件——**不落仓库文件**，因此没有污染工作树、也没有越出我的写域（计划 §4：researcher 只写本文件）。

```
# 探针源码见 §8；两条命令即全部复现步骤
cd apps/api && go test -overlay=/tmp/tlprobe/overlay.json  -count=1 -v -run '^TestProbeCumulative$'      ./app/Models/TrustLevels/
cd apps/api && go test -overlay=/tmp/tlprobe/overlay2.json -count=1 -v -run '^TestProbeMergeScenarios$'  ./app/Models/Identity/
```

另外跑了既有守卫测试（真实执行，不是引用）：

```
cd apps/api && go test -count=1 -v -run 'TestFeatureDisabledReturns404OnEveryRoute|RequiresTrustManage|TestAdmin' ./app/Http/Controllers/TrustLevels/
# === RUN TestFeatureDisabledReturns404OnEveryRoute --- PASS
# === RUN TestAdminRoutesRequirePermissions            --- PASS
# ok  …/app/Http/Controllers/TrustLevels  0.039s
```

---

## 1. 累积语义的边界

### 1.1 结论

| 问题 | 答案 | 证据 |
| --- | --- | --- |
| 运营勾了 **TL4** 的键，TL2 用户拿得到吗？ | **拿不到。** 累积是**单向向上**的（低档配置流向高档），不是向下 | 探针：`TL4-only 配置: Grants(TL2)=[] Grants(TL4)=[topic.create]` |
| 运营勾了 **TL2** 的键，TL4 用户拿得到吗？ | **拿得到**，这就是 T21 要披露的语义 | 探针：`TL1 配置: Grants(TL0)=[] Grants(TL1)=[topic.create] Grants(TL2)=[topic.create] Grants(TL4)=[topic.create]` |
| TL0 能配吗？ | 不能。`DecodeConfig` 对 TL0 **422 拒绝**（不是静默丢弃） | `config.go:89-91` |
| `Grants(TL0)` 恒空吗？ | 恒空，且是 `nil` 而非 `[]` | `registry.go:56` |

### 1.2 机制

- `Grants(level)`（`registry.go:55-71`）：`for current := LevelMember; current <= level; current++` 取并集，
  用 `seen` 去重，顺序 = 档位升序 × 档内声明序。`level <= LevelNewcomer` 直接 `return nil`（`:56`）。
- `Permissions(level)`（`registry.go:47-52`）：`slices.Clone(e[level])`——**恰好该档新增**，不含更低档。

### 1.3 两者之差与各自的消费者（这是本刀最容易搞反的地方）

| 方法 | 语义 | 生产消费者 | 用途 |
| --- | --- | --- | --- |
| `Grants(level)` | **累积并集** | `resolver.go:90`（唯一生产出口）→ `identity/trust_levels.go:41` → `postgres_store_roles.go:250` → `mergeEarnedPermissions:343` | **裁决**：决定 `Actor.Permissions` 里多出哪些键 |
| `Permissions(level)` | **该档新增** | ① `payload.go:44`（公开 + 后台 payload 的每档 `earnable`）② `config.go:148`（`EncodeConfig` 回写 option） | **展示与持久化**：增量口径 |
| `All()` | `Grants(LevelLeader)` | 仅测试 `registry_test.go:18` | 结构断言 |

**陷阱（新增，计划未列）**：`payload.go:44` 与 `config.go:148` 都用 `Permissions`，这是**必须**的。
若有人为了「让前端看到累积」而把 `payload.go:44` 改成 `Grants`，后果不是显示错，而是
**保存时重复计入**：编辑器读回累积值 → `trustLevelsPayload` 原样带回 → `EncodeConfig` 把累积值
写进每档 `earnable` → 下一次读取 TL2 档就多出 TL4 的键，每存一次膨胀一轮。
即：**展示口径必须增量，裁决口径必须累积，两个方向都不可交换。**

### 1.4 适用条件

累积语义只在「用户真实等级 ≥ 配置档位」时生效；等级由 `LevelOfAt`（`level.go:35-37`）算出，
受账龄闸约束（`policy.NewUserDays`，默认 7 天，`policy.go:13`）——新号 7 天内最高 TL1，
所以「勾 TL4 的键对新号无效」还有第二重原因（等级压根没到）。

---

## 2. 白名单可达性（纪律 #8）——这条要说死

### 2.1 7 键是否全部已在默认基线里：**是，全部 7 个，两个层面都是**

**层面一：源码常量。** `SeedMemberPermissions`（`seeds.go:157-165`）逐键包含
`topic.create / topic.edit_own / topic.delete_own / post.create / post.edit_own / post.delete_own / attachment.upload`。
探针逐键输出 `inSeedMember=true` ×7。

**层面二：真实生效来源（迁移，不是常量）。** 常量只是文档，`member` 角色的实际授权由三条迁移拼成：

| 迁移 | 行 | 授予 member |
| --- | --- | --- |
| `202607040001_identity_rbac.sql` | `109-113` | `topic.create`, `post.create`, `post.edit_own`, `post.delete_own` |
| `202607050004_attachments.sql` | `61-64` | `attachment.upload` |
| `202607120001_fine_grained_permissions_phase1.sql` | `47-52` | `topic.edit_own`, `topic.delete_own` |

合计 7/7，与 `earnableWhitelist`（`registry.go:26-34`）**集合相等**。
`EligiblePermissions()`（`registry.go:122-135`）= 白名单 ∩ `SeedPermissions`，探针实测
`len=7`，顺序与 `EarnableWhitelist()` 逐字一致。

### 2.2 结论（写给 D33 的裁决依据）

**默认安装下，运营在任意档勾任意白名单键，零可观测变化。** 注意措辞比计划更狠：

- 不只是「行为不变」——`mergeEarnedPermissions` 的 `present[key]` 检查（`postgres_store_roles.go:353`）
  会把这 7 个键全部跳过，所以 **`Actor.Earned` 恒为空 map**；
- 于是 `GrantSource`（`permission_origin.go:42-59`）对这 7 个键**永远返回 `static`，不会返回 `earned`**。
- 探针 A 实测：`A 默认基线: Earned=map[] len(perms)=7`。

即：默认安装下 earnable 是「**零行为 + 零来源可解释性**」的变化。计划 §1 说「零行为变化」是对的，
但少了后半句——**连 `GrantSource` 都看不出等级起过作用**，这会让「按来源审计等级收益」这类
后续需求拿到全 `static` 的空结论。

### 2.3 earnable 在什么配置下才真的有牙

判据只有一条：**该用户的静态集（角色 ∪ 用户 allow）不含该键，且该键未被 deny。**
满足它的现实路径有三条，按可能性排序：

| # | 配置 | 结果 | 证据 |
| --- | --- | --- | --- |
| 1 | 运营在后台把 `member` 角色的某个键**摘掉**（角色权限编辑），该键留在 earnable 里 | 立刻生效，来源变 `earned` | 探针 B：`Earned=map[topic.create:true] perms=[post.create topic.create]`，`GrantSource(topic.create)=earned` |
| 2 | 站点用**自定义低权角色**（非 `member` 基线）承载用户 | 同上，只要该角色不含该键 | 同上机制 |
| 3 | 未来白名单新增某键、但没同步进 `member` 基线 | 该键对基线用户仍无效，对低权角色有效 | `EligiblePermissions` 只与 `SeedPermissions` 求交（`registry.go:123-126`），与角色基线无关 |

**给运营的一句话**：想让「TL2 解锁发主题」真有牙，不是去勾矩阵，而是**先把 `member` 的
`topic.create` 摘掉**——那就是收尾计划明确不做的 B 路（§6-1）。所以 A 路的诚实定位是：
**给「已经收窄过基线」或「用自定义角色」的站点一个配置界面，而不是给默认安装一个新能力。**
UI 文案若写成「勾选后 TL2 用户解锁发主题」，对默认安装是**假的**（他们在 TL1 就已经能发）。
建议 M10 在矩阵上方加一句条件说明，把这句话写进去。

**附带发现**：7 个键里，`attachment.upload` 是**唯一**可能已经被运营手工摘过的（它在
`20260705` 才追加，且附件是运营最常按角色收窄的能力）。若要挑一个键做端到端实测
「勾了真的生效」，选它成功率最高。

---

## 3. deny 与 earned 的交互

### 3.1 合并顺序（实测核对，与计划描述一致，但计划漏了第一步）

`applyTrustLevel`（`postgres_store_roles.go:228-258`）之前还有一步，顺序是：

1. `loadCurrentUserAccess`（`:300-317`）
   - `current.Permissions = ExpandEffectivePermissions(effectivePermissionKeys(decisions))`（`:314`）
   - `current.denied = deniedPermissionKeys(decisions)`（`:315`）
2. `applyTrustLevel`（`:228-258`）：读 `created_at/kind`（`:236`，全请求唯一一次 users 行读）→
   `resolveTrustGrants`（`:250`）→ `mergeEarnedPermissions`（`:256`）
3. `mergeEarnedPermissions`（`:343-365`）：对每个 earned 键，`key == "" || denied[key] || present[key]` → `continue`；
   否则追加进 `Permissions` 并记入 `earnedOnly`；末尾 `slices.Sort`。

### 3.2 构造场景：deny 一个**无静态授予**的键 + 该键在 earnable 里

探针 C 实测（`CurrentUser{Permissions:["post.create"], denied:{"topic.create"}}`，
`mergeEarnedPermissions(_, ["topic.create"])`）：

```
C deny+earnable: Earned=map[] perms=[post.create]
C GrantSource(topic.create)=none
```

**结果：deny 赢，且来源报 `none`（不是 `earned`、也不是 `static`）。** 用户看到的是
「我没有这个能力，且没有任何来源授予它」——这是正确形态：被显式拒绝的能力不该出现在
「你等级够了但被拒」的解释里。

### 3.3 这条为什么能成立（关键前提，容易被打穿）

`deniedPermissionKeys` 只从 `ListUserPermissions` 的结果里取 `Effective=false` 的行。而
**deny-only 键（没有任何角色授予）默认不会出现在候选集里**——如果 SQL 不做 UNION，`denied`
集合就看不到它，earned 就会把它复活。防线是 `identity.sql` 的
`UNION SELECT denied_permission_keys.key, FALSE`，由
`trust_levels_deny_only_postgres_integration_test.go:24-90` 守着（该测试的注释明确写了
变异要求：删掉 UNION 必须红）。

**适用条件**：deny 优先于 earned 是**无条件**的，但只对「用户级 deny override」成立。
「角色没授予」不是 deny——它只让 `present[key]` 为 false，于是 earned 能追加。这两者在
`mergeEarnedPermissions` 里走的是不同分支，别混。

---

## 4. `eligiblePermissions` 的消费者盘点

### 4.1 命令与时间戳（父计划 §14-5 要求）

```
$ date -Iseconds
2026-11-11T09:20:31+08:00
$ rg -n "eligiblePermissions|EligiblePermissions" --glob '!knowledge/**' --glob '!docs/**' .
```

（**不用 `rg -c`**：本仓存在同一行多个调用点的情况，按行计数会少算。以下按命中行逐条分类。）

### 4.2 盘点结果

| 类别 | 数量 | 位置 |
| --- | --- | --- |
| **Go 生产消费者** | **1** | `controller.go:144` `Eligible: nonNilStrings(trustlevels.EligiblePermissions())` |
| Go 定义 | 1 | `registry.go:122`（`EarnableWhitelist()` 在 `:37`，两者不同函数） |
| Go 测试 | 2 | `registry_test.go:44`（字面集合守卫）、`controller_test.go:253-254`（非 null 形状） |
| 契约声明 | 3 | `schemas/trust_levels.yaml:119,128`、`paths/trust_levels.yaml:32` |
| DTO 字段 | 1 | `dto.go:71` `Eligible []string \`json:"eligiblePermissions"\`` |
| 验收脚本 | 3 | `scripts/verify-trust-levels-acceptance.sh:488,653,1456`（引用测试名，不是消费者） |

### 4.3 结论

**计划 §2-4「白名单已下发给后台」成立，但计划 §2-5「前端 tab 拿不到它」在审计时刻已经过期一半。**

```
$ date -Iseconds
2026-11-11T09:24:06+08:00
$ rg -n "admin/trust-levels" apps/web
apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts:157  (注释)
apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts:163  export const TRUST_LEVEL_ADMIN_READ_PATH = '/admin/trust-levels'
apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts:413  outcome = { ok: true, body: await request<unknown>(TRUST_LEVEL_ADMIN_READ_PATH) }
apps/web/tests/admin/settings/trustLevels.test.ts:294,295
$ rg -n "Whitelist|whitelist" apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue apps/web/app/pages/admin/settings/index.vue
exit=1   # 零命中
```

准确表述：**前端 fetcher 已存在（`useTrustLevelEarnableWhitelist()`，`useTrustLevelsThresholds.ts:400-424`），
但 tab 尚未接线（`rg` 零命中）。** 也就是说：

- 「为编辑器准备但还没人用」——**对 tab 成立**；
- 「零前端消费者」——**已不成立**，写这句话会立刻被下一个人打回（T20 型滞后否定句，只是这次
  滞后方向是「我说没有、其实刚有」）。

因此本刀的定位是：**`eligiblePermissions` 的第一个真实 UI 消费者，就在 `SFAdminTrustLevelsTab.vue`
接线的那一刻诞生**。建议 M10 的判据写成「tab 里出现对 `useTrustLevelEarnableWhitelist` 的调用，
且失败态渲染错误条」，而不是「新增消费者」——前者可 grep 验证，后者无法证伪。

---

## 5. 开关关闭时 earnable 的行为

### 5.1 后端行为（已实测）

| 面 | 开关关闭时 | 证据 |
| --- | --- | --- |
| 4 条路由 | 全部 **404** | `controller.go:86`（公开）、`:245`（requireAdmin 内）；`TestFeatureDisabledReturns404OnEveryRoute` 实测 PASS |
| 装载期 | `ErrDisabled` → `resolveTrustGrants` 静默返回 `ok=false`，不写 warn | `identity/trust_levels.go:43-45` |
| 结果 | `trustLevelResolved=false`，`TrustLevel=0`，`Earned` 恒空 | `postgres_store_roles.go:251-253` |

### 5.2 静默落差：**存在，而且比计划描述的更宽**

计划只说了「运营配了 earnable 但关了开关」这一种。实测是**三种同时存在**：

1. **阈值面**：`trust.levels` option 是 `public: true`（`community_policy_options.go:82`），与 feature flag
   **无关**；后台 tab 读的是 options map（`useTrustLevelsThresholds.ts` + `trustLevelsPayload`），
   **不是** `/admin/trust-levels`。而保存走 `saveMany` → `PUT /admin/web-options`
   （`useWebOptions.ts:390-394`），该接口**只校验 `managePermission`（trust.manage），不查 feature flag**。
   ⇒ 开关关闭时，运营**可以正常保存阈值并看到「已保存」Toast**，但公开页 404、等级完全不生效。
2. **earnable 面（本刀之后）**：编辑器若按 M10 要求从 `/admin/trust-levels` 取白名单，会拿到 404 →
   按 M10 硬要求 #1 应显示「不可编辑 + 错误条」。这**反而是正确形态**：失败可见，不是空矩阵。
   但错误条文案如果只写「加载失败」，运营会去查网络，而不是查开关。
3. **导航面**：tab 在 `pages/admin/settings/index.vue:40,51` **无条件注册**，
   `rg "features\.trust_levels|featureFlag"` 在该 tab 与 settings 页 → `exit=1`（零命中）。
   ⇒ 开关关闭时 tab 照常出现在侧栏和 tab 条里。

**建议（M10 必须处理，否则是新的静默落差）**：tab 顶部按 `features.trust_levels` 渲染一条
「当前未启用：等级不参与判定，保存不会改变任何人的能力」的提示，并在关闭态禁用保存。
数据源现成：`useWebOptions.ts:448` 的 `trustLevelsEnabled`（`featureEnabled('features.trust_levels')`，已在 `:531` 导出；后端字段见 `feature_flags.go:130`）。
> 注意 `featureEnabled(name, fallback = true)`（`useWebOptions.ts:410`）：**option 行缺失时前端按「已启用」**，与后端 `Default: true`（`feature_flags.go:82`）一致，但意味着「缺失」和「开启」在 UI 上不可区分——禁用保存的逻辑不能只看 `!trustLevelsEnabled` 就以为覆盖了全部关闭态。

> 附带：这也解释了为什么 F 组「关闭 = 逐字节等价」的断言只测了**行为**没测**UI**——
> 行为确实等价（404 + 未解析），但后台看起来一切正常。这不是 F 组的错，是 UI 缺一条状态。

---

## 6. `trust.manage` 的可达性

### 6.1 谁持有它

| 角色 | trust.manage | 证据 |
| --- | --- | --- |
| `super_admin` | 有（全量） | `202611100004_trust_manage_permission.sql:23-27` 显式插入 |
| `operator`（站点运营） | **有** | 同上 + `apps/web/app/config/roleTemplates.ts:68-69`（两边必须同改） |
| `moderator`（版主） | **无** | 迁移测试 `trust_manage_permission_migration_test.go:48` 断言 moderator 0 条 |
| `tech_admin` | **无** | 模板 `seeds.go:224-235` 不含 |

### 6.2 从哪个界面到达——**四道门，四个不同权限**

| 门 | 要求 | 位置 |
| --- | --- | --- |
| 路由页 `/control-panel/settings` | `admin.access` | `apps/web/app/middleware/admin.ts:18` |
| 侧栏入口 `/settings` | `settings.site.manage` | `adminModules.ts:111-116` + `canAccessAdminPage`（`:594-606`） |
| tab `trustLevels` | **无任何过滤**（无条件列出） | `pages/admin/settings/index.vue:40,51` |
| 读 API `GET /admin/trust-levels` | `admin.access` | `controller.go:129-136` |
| 写 API `PUT` / `restore-defaults` | `trust.manage` | `controller.go:149-152,178-181` |

### 6.3 回答「只有 admin.access 的用户看得到矩阵吗？」

**分两半，两半都不是计划预设的那个答案：**

- **通过侧栏：看不到。** 侧栏 `/settings` 要 `settings.site.manage`，而 `moderator` / `tech_admin`
  只有 `admin.access` → 侧栏没有入口。
- **直接输 URL：看得到、能读、能编辑、保存 403。** 页面 middleware 只查 `admin.access`，
  tab 无过滤，读 API 只查 `admin.access`，写 API 查 `trust.manage`。
  且 tab 内**没有任何 `can()` 判断**：

```
$ rg -n "can\(" apps/web/app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue \
                    apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts
exit=1   # 零命中
```

同目录的既有范式是有的：`SFAdminSiteUpdatesTab.vue:22` `const canManage = computed(() => can('settings.site.manage'))`，
并在 `:114,177,188` 用 `:disabled="!canManage"`。**信任等级 tab 缺的就是这一行。**

### 6.4 这是「正确形态」还是「新的 `badge.view`」？

**是正确形态，不是新的 `badge.view`；缺的是一个 `canManage` 禁用，不是一个新的权限键。**

- 权限模型本身正确：读 `admin.access`（D9 刻意不做 `trust.view`）、写 `trust.manage`。
  `controller_test.go:107-126` 已断言「有 admin.access 无 trust.manage → 读 200、写 403、恢复默认 403」，
  后端裁决是对的。
- 与 `badge.view` 不同：`badge.view` **有真实生产消费者**——`core_catalog_gen.go:44,48,51` 的 GET 守卫、
  `adminModules.ts:229` 的侧栏 `requiredPermissions`、`roleTemplates.ts:39,65`。它是活的读权限。
  ⇒ 结论仍是「不做 `trust.view`」（读面已被 `admin.access` 覆盖，再加一个键只会多一份授予矩阵），
  但**理由要换**：不是「badge.view 是死权限」，而是「读面已有等价覆盖，新键零增量」。
- 真正的问题不是权限，是 **UI 没有告知**：moderator 直接进 URL 后可以一路编辑到点保存才吃 403。
  按仓库既有约定（`SFAdminSiteUpdatesTab` 范式），应 `can('trust.manage')` 禁用输入 + 保存按钮 + 提示。

---

## 7. 残余风险（residual）

| # | 残余 | 严重度 | 归属 |
| --- | --- | --- | --- |
| R1 | **开关关闭时后台仍可保存阈值**（`PUT /admin/web-options` 不查 flag），运营会看到「已保存」但前台 404 且等级不生效 | 中（静默落差，§5-2） | frontend（M10 加关闭态提示/禁用） |
| R2 | 信任等级 tab **无 `canManage` 禁用**：只有 `admin.access` 的账号可编辑到保存才 403 | 中（§6-3） | frontend（M10 顺手补，同 `SFAdminSiteUpdatesTab` 范式） |
| R3 | 四道门权限不一致（页 `admin.access` / 侧栏 `settings.site.manage` / 读 `admin.access` / 写 `trust.manage`），且 tab 无过滤 | 低（现状可用，但审计者会反复问） | pm（写进矩阵一条，别让下一个人重新推一遍） |
| R4 | **A 路对默认安装是假能力**（§2-2）：勾矩阵零效果，因为 7 键全在基线里。UI 文案若写成「解锁」即说谎 | 高（产品诚实性） | designer + writer（M10 文案必须带条件说明） |
| R5 | **计划 §6-2 的前提「`badge.view` 是死权限」不成立**（§6-4）。结论可保留，论证链要重写 | 中（决策记录会留错证据） | writer（`2026-11-11-trust-level-earnable.md`） |
| R6 | `attachment.upload` 是唯一「可能已被运营手工摘掉」的白名单键 → 端到端实测「勾了真生效」应选它 | 低（建议，非缺陷） | reviewer（M13 浏览器/curl 选键） |

**已排除的怀疑（查过，不是问题）**：

- TL0 的 earnable 会不会被静默丢弃？**不会**——`config.go:89-91` 直接 422 拒绝，早于 `Normalize`。
- `Grants` 会不会把同一键重复计入 `Permissions`？**不会**——`mergeEarnedPermissions:353` 的 `present[key]` 挡住，
  `registry.go:59-68` 的 `seen` 也去重。
- deny 会不会被 earned 绕过？**不会**——§3，且有 PostgreSQL 集成测试守 UNION。

---

## 8. 附录：探针源码与原始输出

### 8.1 `TrustLevels` 包外探针

```go
package trustlevels_test

import (
	"fmt"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

func TestProbeCumulative(t *testing.T) {
	tl4only := trustlevels.Earnable{trustlevels.LevelLeader: []string{"topic.create"}}
	t.Logf("TL4-only 配置: Grants(TL2)=%v Grants(TL4)=%v", tl4only.Grants(trustlevels.LevelRegular), tl4only.Grants(trustlevels.LevelLeader))

	tl2 := trustlevels.Earnable{trustlevels.LevelMember: []string{"topic.create"}}
	t.Logf("TL1 配置: Grants(TL0)=%v Grants(TL1)=%v Grants(TL2)=%v Grants(TL4)=%v",
		tl2.Grants(trustlevels.LevelNewcomer), tl2.Grants(trustlevels.LevelMember),
		tl2.Grants(trustlevels.LevelRegular), tl2.Grants(trustlevels.LevelLeader))

	mixed := trustlevels.Earnable{
		trustlevels.LevelMember: []string{"topic.create"},
		trustlevels.LevelLeader: []string{"post.create"},
	}
	t.Logf("mixed: Permissions(TL4)=%v  Grants(TL4)=%v", mixed.Permissions(trustlevels.LevelLeader), mixed.Grants(trustlevels.LevelLeader))

	base := map[string]bool{}
	for _, k := range identity.SeedMemberPermissions {
		base[k] = true
	}
	for _, k := range trustlevels.EarnableWhitelist() {
		t.Logf("whitelist %-20s inSeedMember=%v", k, base[k])
	}
	t.Logf("EligiblePermissions()=%v (len=%d)", trustlevels.EligiblePermissions(), len(trustlevels.EligiblePermissions()))
	t.Logf("EarnableWhitelist()=%v (len=%d)", trustlevels.EarnableWhitelist(), len(trustlevels.EarnableWhitelist()))
	fmt.Println("PROBE-DONE")
}
```

原始输出（2026-11-11 09:19 +08:00）：

```
=== RUN   TestProbeCumulative
    TL4-only 配置: Grants(TL2)=[] Grants(TL4)=[topic.create]
    TL1 配置: Grants(TL0)=[] Grants(TL1)=[topic.create] Grants(TL2)=[topic.create] Grants(TL4)=[topic.create]
    mixed: Permissions(TL4)=[post.create]  Grants(TL4)=[topic.create post.create]
    whitelist topic.create         inSeedMember=true
    whitelist post.create          inSeedMember=true
    whitelist attachment.upload    inSeedMember=true
    whitelist topic.edit_own       inSeedMember=true
    whitelist post.edit_own        inSeedMember=true
    whitelist topic.delete_own     inSeedMember=true
    whitelist post.delete_own      inSeedMember=true
    EligiblePermissions()=[topic.create post.create attachment.upload topic.edit_own post.edit_own topic.delete_own post.delete_own] (len=7)
    EarnableWhitelist()=[topic.create post.create attachment.upload topic.edit_own post.edit_own topic.delete_own post.delete_own] (len=7)
PROBE-DONE
--- PASS: TestProbeCumulative (0.00s)
ok  	…/app/Models/TrustLevels	0.010s
```

### 8.2 `Identity` 包内探针

```go
package identity

import (
	"fmt"
	"testing"
)

func TestProbeMergeScenarios(t *testing.T) {
	// A: 默认安装（member 基线含全部 7 键）—— earnable 勾 topic.create
	a := &CurrentUser{ID: 7, Permissions: []string{"attachment.upload", "post.create", "post.delete_own", "post.edit_own", "topic.create", "topic.delete_own", "topic.edit_own"}}
	a.denied = map[string]bool{}
	earnedA := mergeEarnedPermissions(a, []string{"topic.create"})
	fmt.Printf("A 默认基线: Earned=%v len(perms)=%d\n", earnedA, len(a.Permissions))

	// B: 运营把 member 基线收窄到只有 post.create
	b := &CurrentUser{ID: 8, Permissions: []string{"post.create"}}
	b.denied = map[string]bool{}
	earnedB := mergeEarnedPermissions(b, []string{"topic.create"})
	fmt.Printf("B 收窄基线: Earned=%v perms=%v\n", earnedB, b.Permissions)
	actorB := Actor{ID: 8, Status: UserStatusActive, Permissions: map[string]bool{"post.create": true, "topic.create": true}, Earned: earnedB}
	fmt.Printf("B GrantSource(topic.create)=%v\n", actorB.GrantSource("topic.create"))

	// C: deny 一个无静态授予的键，且该键在 earnable 里
	c := &CurrentUser{ID: 9, Permissions: []string{"post.create"}}
	c.denied = map[string]bool{"topic.create": true}
	earnedC := mergeEarnedPermissions(c, []string{"topic.create"})
	fmt.Printf("C deny+earnable: Earned=%v perms=%v\n", earnedC, c.Permissions)
	actorC := Actor{ID: 9, Status: UserStatusActive, Permissions: map[string]bool{"post.create": true}, Earned: earnedC}
	fmt.Printf("C GrantSource(topic.create)=%v\n", actorC.GrantSource("topic.create"))
}
```

原始输出（2026-11-11 09:21 +08:00）：

```
=== RUN   TestProbeMergeScenarios
A 默认基线: Earned=map[] len(perms)=7
B 收窄基线: Earned=map[topic.create:true] perms=[post.create topic.create]
B GrantSource(topic.create)=earned
C deny+earnable: Earned=map[] perms=[post.create]
C GrantSource(topic.create)=none
--- PASS: TestProbeMergeScenarios (0.00s)
ok  	…/app/Models/Identity	0.020s
```

### 8.3 审计时刻工作树状态（提醒：本文结论的「前端消费者」一条是移动靶）

```
$ git status --porcelain | head
 M apps/api/app/Http/Controllers/Options/controller.go
 M apps/api/app/Models/Options/service.go
 M apps/api/app/Models/Options/types.go
 M apps/web/app/composables/admin/settings/useTrustLevelsThresholds.ts
 M apps/web/tests/admin/settings/trustLevels.test.ts
 M docs/en-US/usage/README.md
 M docs/en-US/usage/trust-levels.md
 M docs/zh-CN/usage/README.md
 M docs/zh-CN/usage/trust-levels.md
 M knowledge/modules/trust-levels.md
```

`useTrustLevelsThresholds.ts` 正在被改（M10 进行中）。§4.3 的「tab 未接线」以 09:24:06 为准；
若 frontend 已接线，该条自动失效——**这正是 T20 说的滞后型否定句，重测成本一条 `rg`。**
