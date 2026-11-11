# 信任等级 N 档改造 — 对拍矩阵（硬编码点 / 契约消费方 / 断言清单）

- 任务：task-1（只读调查，产出一份报告）
- 基线：`git rev-parse --short HEAD` = `03fc95d78`（2026-11-11 采集）
- 范围：`apps/api`、`apps/web`、`contracts/openapi/{paths,schemas}/trust_levels.yaml`、
  `scripts/verify-trust-levels-acceptance.sh`、两份 i18n locale
- 口径依据：`knowledge/plans/2026-11-11-trust-levels-n-tiers.md`（D-N1..D-N12）、
  `knowledge/decisions/2026-11-11-trust-levels-n-tiers.md`
- 证据纪律：所有路径都带行号；否定结论都附可复算命令与实测输出（§1）。

> 一句话结论：**「5」只有一个真正的地基**——`TrustLevels` 包里的定长数组 + `nameSlugs` +
> `LevelCount`。它往上辐射出 21 行 Go 生产代码、约 30 处 Web 生产代码、2 处 i18n 键组、
> 以及 5 个测试文件 + 验收脚本的写死断言。数据库确实零命中（§1-B），因此这次改造
> **没有迁移成本，全部成本在形状与断言**。

---

## 1. 四条否定判据（实测，命令可直接复跑）

### A. `LevelCount` 在生产代码的命中数

```bash
rg -n "LevelCount" apps/ | wc -l          # 27
rg -n "LevelCount" apps/ | rg -v "_test\.go" | wc -l   # 21
```

```
27
21
```

按文件（`rg -c "LevelCount" apps/` 实测）：

| 文件 | 行数 | 性质 |
| --- | --- | --- |
| `apps/api/app/Models/TrustLevels/types.go` | 7 | 生产 |
| `apps/api/app/Models/TrustLevels/config.go` | 6 | 生产 |
| `apps/api/app/Models/TrustLevels/registry.go` | 3 | 生产 |
| `apps/api/app/Models/TrustLevels/policy.go` | 2 | 生产 |
| `apps/api/app/Http/Controllers/TrustLevels/payload.go` | 2 | 生产 |
| `apps/api/app/Models/TrustLevels/level.go` | 1 | 生产 |
| `apps/api/app/Models/TrustLevels/config_test.go` | 2 | 测试 |
| `apps/api/app/Models/TrustLevels/registry_test.go` | 1 | 测试 |
| `apps/api/app/Models/TrustLevels/level_test.go` | 1 | 测试 |
| `apps/api/app/Models/TrustLevels/level_purity_test.go` | 1 | 测试 |
| `apps/api/app/Models/Identity/default_install_grant_source_test.go` | 1 | 测试 |

**判据读数：不是 0。** 「目标 0」是改造完成后的门禁，不是现状。现状为
**生产 21 行 / 含测试 27 行**，全部集中在 `app/Models/TrustLevels`（外加 `payload.go`
两处生成循环）；`Identity`/`Forum`/`Profile`/`Options` 的生产代码**零命中**——
这正好印证计划 §1 的洞见：`Actor.TrustLevel` 继续当 rank 用，下游不用动。

### B. `trust_level` 在迁移与 sqlc 生成代码里的命中数

```bash
rg -n "trust_level" apps/api/database/migrations/ apps/api/database/sqlc/
# 无输出，退出码 1
```

```
exit=1
```

**判据成立：0 命中 ⇒ 零数据库迁移。** 等级是 `user_activity` + `users.created_at` 的
纯函数（`level.go:19-32`），配置只存在 options 的 `trust.levels` 单键里
（`apps/api/app/Models/Options/trust_level_options.go:16`）。

### C. 前端 `TRUST_LEVEL_SLUGS` / `TRUST_LEVEL_MAX` 的全部引用文件

```bash
rg -l "TRUST_LEVEL_SLUGS|TRUST_LEVEL_MAX" apps/
rg -c "TRUST_LEVEL_SLUGS" apps/ ; rg -c "TRUST_LEVEL_MAX" apps/
```

```
apps/web/tests/profile/trustLevelCard.test.ts
apps/web/tests/admin/forum/trustLevels.test.ts
apps/web/app/composables/profile/useTrustLevel.ts
apps/web/app/composables/admin/forum/trustLevels/useTrustLevelsForm.ts
apps/web/app/components/profile/SFTrustLevelCard.vue

SLUGS：useTrustLevel.ts:4 · useTrustLevelsForm.ts:4 · SFTrustLevelCard.vue:2
       trustLevelCard.test.ts:2 · trustLevels.test.ts:2
MAX  ：useTrustLevel.ts:4 · SFTrustLevelCard.vue:4 · trustLevelCard.test.ts:2
```

**判据读数：生产 3 文件 / 测试 2 文件。** 定义点是
`useTrustLevel.ts:14`（`['newcomer','member','resident','veteran','leader'] as const`）
与 `:18`（`TRUST_LEVEL_MAX = TRUST_LEVEL_SLUGS.length - 1`）；其余全是消费者。

### D. `scripts/verify-trust-levels-acceptance.sh:76` 的 `OPENAPI_TL` 是否零使用

```bash
rg -n "OPENAPI_TL" scripts/verify-trust-levels-acceptance.sh
ls contracts/openapi/paths/ | rg -i trust
test -f contracts/openapi/paths/trust-levels.yaml || echo "trust-levels.yaml: absent"
```

```
76:OPENAPI_TL="$ROOT_DIR/contracts/openapi/paths/trust-levels.yaml"

contracts/openapi/paths/trust_levels.yaml
trust-levels.yaml: absent
```

**判据成立且比预期更糟：不仅零使用，路径本身还是错的。** 实际契约文件是
`contracts/openapi/paths/trust_levels.yaml`（下划线），脚本第 76 行写的是
`trust-levels.yaml`（连字符），该文件不存在。因为全脚本再无第二处引用，这个错误
一直沉默；任何后续想「顺手引用 OPENAPI_TL」的改动都会立刻炸。

---

## 2. 表 1：硬编码点表

语义分类：**身份**（谁是第几档 / tier id）、**序号**（档数、上下界、下标）、
**展示**（图标、名字、布局行）、**校验**（拒绝条件）、**回退**（默认值/降级）。
处置：**替换**（N 档后必须改）、**保留**（D-N2 语义不变）、**改语义**（保留代码但要重述判据）。

### 2.1 Go 生产代码（21 行 `LevelCount` + 相邻写死）

| # | 位置 | 语义 | 写死了什么 | 处置 |
| --- | --- | --- | --- | --- |
| 1 | `apps/api/app/Models/TrustLevels/types.go:1` | 身份 | 包注释「派生用户信任等级（TL0–TL4）」 | 替换 |
| 2 | `types.go:17-23` | 身份+序号 | `LevelNewcomer..LevelLeader = 0..4` 五个具名常量 | 替换（改为「内置五档 id 常量」而非档数常量） |
| 3 | `types.go:24-25` | 序号 | `LevelCount = 5`，同时当数组长度与遍历上界 | 替换（运行期 activeCount） |
| 4 | `types.go:29` | 校验 | `Valid()` 上界 = `LevelCount` | 替换 |
| 5 | `types.go:32-38` | 身份 | `nameSlugs = [LevelCount]string{newcomer..leader}` 定长数组，是 tier id 唯一来源 | 替换（tier 自带 id） |
| 6 | `types.go:41-46` | 身份 | `Slug()` 用数组下标取 id，越界返回 `""` | 替换（按 tier 查 id） |
| 7 | `types.go:169-172` | 序号 | `Policy.Levels [LevelCount]Requirements`、`Limits [LevelCount]LevelLimits` 定长数组 | 替换（切片 + 长度校验） |
| 8 | `types.go:166-167` | 身份 | `NewUserDays` 注释「最高只到 TL1」 | 改语义（判据仍成立，措辞改「最低 active 档」） |
| 9 | `level.go:5` | 序号 | 注释「输出 0..4」 | 替换 |
| 10 | `level.go:19-32` | 序号+回退 | 账龄闸压到 `LevelMember`；活动闸从最高档向下扫；兜底 `LevelMember` | 替换（上界 = 最高 active rank；地板语义保留） |
| 11 | `level.go:26` | 序号 | 循环起点 `Level(LevelCount - 1)` | 替换 |
| 12 | `policy.go:14` | 序号 | `Levels: [LevelCount]Requirements{...}` 五档字面量 | 替换 |
| 13 | `policy.go:47` | 序号 | `Limits: [LevelCount]LevelLimits{...}` 五档字面量 | 替换 |
| 14 | `policy.go:15-45` | 身份 | TL1..TL4 的推荐阈值逐档字面量（唯一推荐默认来源） | 保留数值，改为内置 tier 数组（D-N9 唯一来源仍在后端） |
| 15 | `config.go:57` | 校验 | 注释「必须给出 TL1..TL4 四档」 | 替换 |
| 16 | `config.go:82` | 校验 | `make(map[Level]bool, LevelCount)` | 替换 |
| 17 | `config.go:88-90` | 校验 | 显式拒绝 TL0 出现在 `levels` 里 | **保留**（D-N2） |
| 18 | `config.go:120` | 序号 | `make(Earnable, LevelCount)` | 替换 |
| 19 | `config.go:125-129` | 校验 | 强制补齐 `LevelMember..LevelCount-1`，缺档即 422 | **改语义**（改为 id 唯一 + activeCount ∈ 1..8 + retired 仍在数组内） |
| 20 | `config.go:141` | 序号 | `make([]LevelConfig, 0, LevelCount-1)` | 替换 |
| 21 | `config.go:143-162` | 序号+身份 | `EncodeConfig` 逐档以 `Level` 下标驱动、`level.Slug()` 写 `levelSlug` | 替换（改为遍历 tiers，写 id） |
| 22 | `config.go:171` | 校验 | `parseLevelSlug` 遍历 `LevelCount` 反查 | **改语义**（v1 兼容期保留；v2 改为查 `tiers[].id`） |
| 23 | `registry.go:47-52` | 校验 | `Permissions` 依赖 `Valid()` | 替换 |
| 24 | `registry.go:55-71` | 序号 | `Grants` 从 `LevelMember` 累加到目标档（累积语义） | 改语义（上界改 rank，累积规则保留） |
| 25 | `registry.go:74-76` | 序号 | `All()` = `Grants(LevelLeader)`——把 TL4 当顶 | **替换**（改为最高 active rank；这是最隐蔽的一处「5」） |
| 26 | `registry.go:79-86` | 序号 | `IsEarnable` 全档线性扫描 | 替换 |
| 27 | `registry.go:89-96` | 序号 | `UnlockLevel` 返回 `Level` | 替换 |
| 28 | `registry.go:104-114` | 序号 | `Validate` 全档扫描 | 替换 |
| 29 | `baseline_grants.go:33-37` | 身份 | `level >= int(LevelMember)` 即不扣基线键（「TL1 起是成员」） | **保留**（D-N2 的 TL0 门） |
| 30 | `resolver.go:94-99` | 校验 | `LimitsFor` 依赖 `Valid()` | 替换 |
| 31 | `resolver.go:115-116` | 序号 | `Progress` 用 `level >= LevelLeader` 判「无下一档」 | **替换**（改为 `rank >= activeMax`） |
| 32 | `payload.go:7` | 展示 | 注释「五档定义 + 阈值 + 限额 + 账龄闸」 | 替换 |
| 33 | `payload.go:18-19` | 展示 | 档目录按 `LevelNewcomer..LevelCount` 生成，`levelDefinition.Level = int(level)` | 替换 |
| 34 | `controller.go:108` | 序号 | `actor.TrustLevel >= int(trustlevels.LevelLeader)` 判顶 | **替换**（否则 8 档站点永远拿不到 nextLevel） |
| 35 | `dto.go:13` | 展示 | 注释「五档定义」 | 替换 |
| 36 | `dto.go:22-24` | 展示 | `levelDefinition{Level int, LevelSlug string}` | **保留字段名**（D-N5）；v2 需决定是否加 `id`/`name`/`retired` |
| 37 | `Profile/service.go:679` | 展示 | `trustlevels.Level(level).Slug()` 生成他人视角 `levelSlug` | 替换（改为查 tier id） |
| 38 | `Forum/service_trust_limits.go:41` | 身份 | `actor.TrustLevel <= 0` ⇒ 走新人限额（TL0） | **保留**（rank 0 = TL0 不变） |
| 39 | `Providers/forum.go:281-296` | 回退 | 解码失败/开关关闭返回零值限额；成功则 `LimitsFor(Level(level))` | **保留**（自动跟随动态下标） |
| 40 | `Identity/trust_levels.go:35-40` | 身份 | 注释「`BaselineGrantsFor`：TL1 及以上返回 nil」 | 改语义（措辞改「任一 active 档」） |
| 41 | `Options/trust_level_options.go:32-49` | 回退 | 归一化 = 严格解码后重新编码；空值回 `DefaultPolicy` | **保留**（形状变了但路径不变） |
| 42 | `bootstrap/trust_levels_wiring.go:34-49` | 回退 | 解码失败即向上返回错误（未解析态） | **保留** |
| 43 | `Identity/types.go:218-220` | 身份 | `Actor.TrustLevel` 注释写死「0..4」 | 改语义（`int` 语义保留，措辞改 rank） |
| 44 | `Identity/types.go:256-258` | 展示 | 第二个结构里的 `TrustLevel int` 注释同样写死「0..4」 | 改语义 |

### 2.2 Web 生产代码

| # | 位置 | 语义 | 写死了什么 | 处置 |
| --- | --- | --- | --- | --- |
| 1 | `apps/web/app/composables/profile/useTrustLevel.ts:14` | 身份 | `TRUST_LEVEL_SLUGS = ['newcomer','member','resident','veteran','leader']` | **替换**（后端目录下发才算权威） |
| 2 | `useTrustLevel.ts:15` | 身份 | `TrustLevelSlug = typeof TRUST_LEVEL_SLUGS[number]` 收窄成 5 个字面量 | **替换**（自定义 id 不在类型里，TS 会直接拒绝） |
| 3 | `useTrustLevel.ts:18` | 序号 | `TRUST_LEVEL_MAX = TRUST_LEVEL_SLUGS.length - 1` | **替换**（改为 `levels.length - 1`，运行期来源） |
| 4 | `useTrustLevel.ts:21-27` | 展示 | `TRUST_LEVEL_ICONS` 五个图标（芽/叶/屋/山/冠） | 改语义（D-N4：保留为默认序列的前 5 个，允许配置覆盖） |
| 5 | `useTrustLevel.ts:106-109` | 展示 | `trustLevelSlug(level)` = 数组下标 + clamp | **替换**（按 rank 查后端目录） |
| 6 | `useTrustLevel.ts:111-114` | 展示 | `trustLevelIcon(level)` = 数组下标 + clamp | **替换**（图标随 tier 下发或走白名单映射） |
| 7 | `useTrustLevel.ts:198-212` | 回退+展示 | `level = clamp(base.level, 0, MAX)`；`levelSlug: trustLevelSlug(level)` **覆盖了后端下发的 `base.levelSlug`** | **替换**（这是最危险的一处：自定义 id 会被本地数组静默改写回内置值） |
| 8 | `useTrustLevelsForm.ts:37` | 展示 | `TRUST_LEVEL_ROWS = [0,1,2,3,4]` | **替换**（含 TL0 的常驻行 + 动态 active 档） |
| 9 | `useTrustLevelsForm.ts:40` | 展示+校验 | `TRUST_LEVEL_EDITABLE_LEVELS = [1,2,3,4]` | **替换**（改为按 id 选择，不再用 rank 当主键） |
| 10 | `useTrustLevelsForm.ts:98-103` | 回退 | 本地 `RECOMMENDED` 四档阈值（与 Go `DefaultPolicy` 双份真相） | **替换**（D-N9：以后台读面 `recommendedConfig` 为唯一来源，本地表降级兜底 + 等值测试） |
| 11 | `useTrustLevelsForm.ts:105-113` | 回退 | `recommendedTrustLevelsForm()` 按 `[1..4]` 造表 | 替换 |
| 12 | `useTrustLevelsForm.ts:270-280` | 校验 | 「非可编辑档（TL0）一律拒绝」，判据是 `EDITABLE_LEVELS.includes(level)` | 替换（判据改 id 集合） |
| 13 | `useTrustLevelsForm.ts:291` | 校验 | 按 `EDITABLE_LEVELS` 净化输入 | 替换 |
| 14 | `useTrustLevelsForm.ts:324` | 回退 | 按 `EDITABLE_LEVELS` 回填 | 替换 |
| 15 | `useTrustLevelsForm.ts:379-393` | 展示 | `bySlug[TRUST_LEVEL_SLUGS[level]]` 用 rank→内置 slug 反查配置 | **替换**（改为按 id 直查；这里假设了「rank 与 id 一一对应」） |
| 16 | `useTrustLevelsForm.ts:407-429` | 身份 | `trustLevelsPayload` 用 `TRUST_LEVEL_SLUGS[level]` 产出 `levelSlug`、按 `EDITABLE_LEVELS` 遍历 | **替换**（新档 id 由后端生成，前端不得自造） |
| 17 | `SFTrustLevelCard.vue:27` | 展示 | `clamp(level, 0, TRUST_LEVEL_MAX)` | 替换 |
| 18 | `SFTrustLevelCard.vue:28` | 展示 | `earned = level >= 1`（TL0 未获得） | **保留**（rank 0 语义不变） |
| 19 | `SFTrustLevelCard.vue:29-30` | 展示 | 名字走 `t('profile.trustLevel.name.' + slug)`，键只有 5 个 | **替换**（自定义名必须优先用 tier.name，i18n 仅兜底内置 5 档） |
| 20 | `SFTrustLevelCard.vue:36` | 展示 | `level >= TRUST_LEVEL_MAX` ⇒ 不渲染进度 | 替换 |
| 21 | `SFTrustLevelCard.vue:70-74` | 展示 | `rungs` 由 5 个 slug 固定生成（「五格固定渲染」） | **替换**（档数变 8 会渲染 8 格，布局未验证） |
| 22 | `SFTrustLevelCard.vue:113` | 展示 | `v-else-if="levelValue >= TRUST_LEVEL_MAX"` 显示 maxed | 替换 |
| 23 | `tabs/SFAdminTrustLevelsOverviewTab.vue:47` | 序号 | 按 `EDITABLE_LEVELS` 汇总 | 替换 |
| 24 | `tabs/SFAdminTrustLevelsOverviewTab.vue:60-61` | 展示 | 档名 `t('...level.' + trustLevelSlug(level))` | 替换（同上：自定义名优先） |
| 25 | `tabs/SFAdminTrustLevelsOverviewTab.vue:164` | 展示 | `v-for="level in TRUST_LEVEL_ROWS"` 阶梯 | 替换 |
| 26 | `tabs/SFAdminTrustLevelsOverviewTab.vue:182,200` | 展示 | `level > 0` / `level === 0` 的 TL0 特例 | **保留**（TL0 恒为首行） |
| 27 | `tabs/SFAdminTrustLevelThresholdsTab.vue:17,80` | 展示 | `TRUST_LEVEL_ROWS` 六指标 × 五档网格 | 替换（8 档网格需视觉 QA） |
| 28 | `tabs/SFAdminTrustLevelThresholdsTab.vue:84,96,106` | 校验 | `level === 0` 只读 + 占位 | **保留** |
| 29 | `tabs/SFAdminTrustLevelThresholdsTab.vue:149` | 展示 | 注释「沿用五档阶梯」+ 24px 图标尺寸假设 | 替换 |
| 30 | `tabs/SFAdminTrustLevelEarnableMatrix.vue:22,28,217` | 展示 | 注释「五档图标阶梯」+ `v-for="level in TRUST_LEVEL_ROWS"` 列 | 替换（D-N12：7–8 档改按档分组卡片） |
| 31 | `tabs/SFAdminTrustLevelEarnableMatrix.vue:221-222,260` | 校验 | `level === 0` 只读/禁用 | 保留 |
| 32 | `composables/.../useTrustLevelEarnableMatrix.ts:48` | 展示 | 注释与分母「可操作键数 × 4」 | 替换（改 × activeCount） |
| 33 | `useTrustLevelEarnableMatrix.ts:68,87` | 序号 | 遍历 `EDITABLE_LEVELS`；`totalCells = EDITABLE_LEVELS.length * operableKeys.length` | 替换 |
| 34 | `useTrustLevelEarnableMatrix.ts:104` | 展示 | 孤儿键文案 `TL${entry.level}`（rank 当标识） | 改语义（自有 id 后应显示名字 + rank） |

### 2.3 i18n（展示层写死）

| 位置 | 内容 | 处置 |
| --- | --- | --- |
| `apps/web/i18n/locales/zh-CN.json:6692-6696` | `profile.trustLevel.name.{newcomer,member,resident,veteran,leader}` | 保留为内置 5 档兜底；自定义名走配置 |
| `apps/web/i18n/locales/zh-CN.json:5338-5342` | `admin.forum.trustLevels.level.*` 同 5 键 | 同上 |
| `apps/web/i18n/locales/en-US.json:5298-5302`、`:6652-6656` | 英文同 5 键 | 同上 |

> 注：i18n 键是**按 slug 命名**的。N 档后新增档位没有对应键，`t()` 会直接回显 key
> 字符串——这是「自定义名称」必须随 tier 一起下发的硬理由（D-N3 只存 `name{zh,en}`）。

---

## 3. 表 2：契约消费方表（`levelSlug` 与 `level`）

判据：D-N5 保留字段名 `levelSlug`、值 = tier id、内置五档 id 不变；
`level` 继续表示 rank。**「断」= 需要改代码；「不断」= 形状不变可直接用。**

### 3.1 `levelSlug`

| 消费方 | 用法 | 改成 tier id 后 |
| --- | --- | --- |
| `apps/api/app/Models/TrustLevels/types.go:41-46` | **生产者**：`Slug()` 读 `nameSlugs` | **断**——id 不再来自定长数组 |
| `apps/api/app/Http/Controllers/TrustLevels/dto.go:24,56` | wire 字段 `json:"levelSlug"` | 不断（字段名保留） |
| `apps/api/app/Http/Controllers/TrustLevels/payload.go:24` | `LevelSlug: level.Slug()` | **断**（间接依赖 `Slug()`） |
| `apps/api/app/Http/Controllers/TrustLevels/controller.go:106` | viewer 的 `LevelSlug` | **断**（同上） |
| `apps/api/app/Models/Profile/service.go:679` | 他人视角 `PublicTrustLevel.LevelSlug` | **断**（同上） |
| `apps/api/app/Models/TrustLevels/config.go:39,84,156` | 配置读写字段名 | **断**——v2 改成 `tiers[].id`；v1 兼容期仍需接受 `levelSlug` |
| `contracts/openapi/paths/trust_levels.yaml:7,13,50,75` | 描述与 `TrustLevelConfigInput` 引用 | 不断（结构），但描述文字写死「五档」→ 需改 |
| `contracts/openapi/schemas/trust_levels.yaml:48,52-54,89,93,166` | `required: [level, levelSlug, ...]` + 描述里列 5 个 slug | 不断（字段名保留）；描述需改（无 `enum` 约束，实测 `rg minItems|maxItems|enum` 零命中） |
| `apps/web/app/composables/profile/useTrustLevel.ts:67` | `TrustLevelView.levelSlug: TrustLevelSlug`（5 字面量联合） | **断**（类型拒绝自定义 id） |
| `useTrustLevel.ts:92` | `PublicTrustLevel.levelSlug: string` | 不断 |
| `useTrustLevel.ts:201` | `if (!base?.levelSlug) return null` | 不断 |
| `useTrustLevel.ts:209` | 用本地数组重算并覆盖后端值 | **断**（会静默改写 id） |
| `apps/web/app/components/profile/SFTrustLevelCard.vue:29,70` | 名字/刻度由 slug 派生 | **断**（自定义名、档数） |
| `apps/web/app/composables/admin/forum/trustLevels/useTrustLevelsForm.ts:386,419` | rank→slug 反查 / payload 产出 | **断** |
| `.../tabs/SFAdminTrustLevelsOverviewTab.vue:61` | 档名 i18n 键 | **断**（无键） |
| `scripts/verify-trust-levels-acceptance.sh:1873,1880,2064,2115` | 断言 slug 顺序与 viewer 形状 | **断**（§4.3） |
| `apps/web/tests/admin/forum/trustLevels.test.ts:371,423,482,531,719` | 断言 slug 列表 | **断** |
| `apps/api/app/Http/Controllers/TrustLevels/controller_test.go:161,165,199` | fixture 里的 `levelSlug` | **断** |

### 3.2 `level`（rank）

| 消费方 | 用法 | 改成 tier id 后 |
| --- | --- | --- |
| `dto.go:23,55` | wire `level int` | 不断（仍是 rank） |
| `payload.go:23` | `Level: int(level)` | 不断 |
| `controller.go:104-121` | `Level` + `NextLevel = level+1` | 不断（但 108 行的「顶」判定要改） |
| `Profile/types.go:85-89,103-106` | `PublicTrustLevel.Level` | 不断 |
| `Identity/types.go:220` | `Actor.TrustLevel int` | 不断（计划 §1 的洞见） |
| `Forum/service_trust_limits.go:40-42` | `TrustLevel <= 0` 走新人限额 | 不断 |
| `Identity/trust_level_grants.go` / `trust_levels.go:31-40` | `BaselineGrantsFor(level)` | 不断（判据 `level >= 1` 保留） |
| `useTrustLevel.ts:65,107,112,202,204` | clamp / 下标 / 自身一致性判断 | **断**（上界用 `TRUST_LEVEL_MAX`） |
| `SFTrustLevelCard.vue:19,27,36,80,113` | `props.level` / `data-level` | **断**（clamp 与 maxed 判据） |
| `useTrustLevelsForm.ts:82-84,153-160` | 表单以 `Record<string, ...>` 用 rank 当 key | 改语义（新主键应为 id） |
| `useTrustLevelEarnableMatrix.ts:18,104` | `level: number` + `TL${level}` | 改语义 |
| `contracts/openapi/schemas/trust_levels.yaml:48,89` | `required` 含 `level` | 不断 |
| 验收脚本 `:1901,1906,1921,2068` | `select(.level==4)`、`0..4` | **断**（§4.3） |

---

## 4. 表 3：断言清单（会被 N 档改动打破的）

### 4.1 Go 测试

| 文件:行 | 现断言 | 应改写为 |
| --- | --- | --- |
| `Models/TrustLevels/config_test.go:14-31` | 固定 4 条 `member/resident/veteran/leader` fixture 才能解码成功 | 「v1 fixture 解码后 active 档数 = 4 且 id 顺序不变」 |
| `config_test.go:39-46` | 「缺少等级」= 少一个固定 slug 即拒绝 | 「active 档数不在 1..8、或 tier id 重复/未知时才拒绝」 |
| `config_test.go:48-52` | 「等级重复」用两个 `member` | 用同一 `id` 重复（不再依赖 slug 列表） |
| `config_test.go:53-57` | 「未知等级」= `newcomer` 出现在 levels 里 | 保留（D-N2 仍拒绝 TL0），另加「未知 id」用例 |
| `config_test.go:147,185` | `for level := LevelMember; level < LevelCount` | 遍历 `policy.Levels`（切片） |
| `config_test.go:198-257` | 显式 `"leader"` 档位往返/限额用例 | 用内置 id 常量或 v2 tier fixture |
| `Models/TrustLevels/registry_test.go:21` | `LevelNewcomer..LevelCount` 全档扫描 | 遍历 `DefaultPolicy()` 的 tier 列表 |
| `Models/TrustLevels/level_test.go:24` | 同上全档扫描构造「满活动」 | 由最高 active rank 生成活动量 |
| `level_test.go:121` | 非单调策略「期望取最高档 TL3」 | 「取被满足的最高 active 档」（不写死数字） |
| `level_test.go:93-96` | 账龄闸「窗口内最高 TL1 / 窗口外至少 TL1」 | **保留判据**，措辞改「最低 active 档」 |
| `Models/TrustLevels/level_purity_test.go:73` | 构造严策略时遍历 `LevelCount` | 遍历动态档位 |
| `Models/TrustLevels/resolver_test.go:104,137` | 「满活动 + 老账号 = TL4」 | 「= 最高 active 档」 |
| `Http/Controllers/TrustLevels/controller_test.go:155-175` | 4 条 slug fixture 的 422 用例 | v1/v2 各一组；`newcomer` 拒绝用例保留 |
| `controller_test.go:199` | body 含 `levelSlug`/`requirements` | 保留（D-N5），另加「body 不含 `truncated` 档」类断言 |
| `Models/Identity/default_install_grant_source_test.go:35` | `LevelNewcomer..LevelCount` 遍历基线扣除 | 遍历动态档位 |
| `Models/Identity/default_install_grant_source_test.go:51` | `TrustLevel: int(LevelMember)` 构造已解析 actor | 用「最低 active rank」常量 |
| `Models/Options/trust_levels_options_write_integration_test.go:63,114` | v1 四档 JSON 写入后读回等值 | 保留 v1 用例（证明兼容），另加「写入 v2 → 读回 v2」 |
| `Models/Options/trust_level_options_test.go:18-40` | 归一化只验 `schemaVersion:1` 合法/9 非法 | 增加 v2 合法、v2 档数越界非法 |

### 4.2 Web 测试

| 文件:行 | 现断言 | 应改写为 |
| --- | --- | --- |
| `tests/admin/forum/trustLevels.test.ts:371` | `.toEqual(['member','resident','veteran','leader'])` | 「内置四档 id 顺序不变」（v1 兼容） |
| `trustLevels.test.ts:423,719` | payload fixture 里写 `levelSlug:'leader'` | v2 用 `tiers[].id`；v1 用例保留 |
| `trustLevels.test.ts:482,531` | `for (const slug of [5 个 slug])` | 内置 id 列表 + 自定义 id 用例 |
| `trustLevels.test.ts:456` | `TRUST_LEVEL_SLUGS[0] === 'newcomer'` | 「首个内置档 id 为 newcomer」或直接删（不再由前端定义） |
| `tests/profile/trustLevelCard.test.ts:87,192` | `rungs.length === 5` | 「rungs 数 = 后端目录档数」 |
| `trustLevelCard.test.ts:6-7,50-51` | 直接 import `TRUST_LEVEL_MAX`/`SLUGS` | 改为从目录 fixture 派生 |
| `tests/admin/forum/trustLevelsTabMount.test.ts:173,243` | `findAll('fieldset').toHaveLength(5)` | 「= 1（TL0）+ active 档数」 |
| `tests/admin/forum/trustLevelsOverview.test.ts:95,157` | `rows).toHaveLength(5)` | 同上 |
| `tests/admin/forum/trustLevelEarnableMatrix.test.ts:120,253,287` | 5 组 fieldset / 每行 5 个复选框 | 按 active 档数 |
| `trustLevelEarnableMatrix.test.ts:397` | 分母 = 白名单 × 4 | 分母 = 白名单 × active 档数 |
| `trustLevelEarnableMatrix.test.ts:367` | 文案 `TL2 · user.ban` | 「档名（rank）· 键」的新格式 |
| `trustLevelEarnableMatrix.test.ts:245-301` | 门控标题/`TL0 不生效，TL1 起生效` 静态文案 | 保留（D-N2 不变），但 `TL2 = resident` 一类映射断言要删 |
| `trustLevelsTabMount.test.ts:26,184-191` | `TL0 只读 / 五档 × 六指标` | 「TL0 只读 / active 档 × 六指标」 |

### 4.3 `scripts/verify-trust-levels-acceptance.sh`

| 行 | 现断言 | 应改写为 |
| --- | --- | --- |
| `:76` | `OPENAPI_TL=.../trust-levels.yaml` | 改 `trust_levels.yaml`，或删除死变量 |
| `:1871-1878` | `lv_n == 5` 才算 I-01 ok | `lv_n` 等于后台配置的 active 档数（默认 5） |
| `:1880-1884` | `slug_ok == "newcomer,member,resident,veteran,leader"` 才算 I-02 ok | 与后台读面的 id 列表逐项相等（默认站点=内置五档） |
| `:1890-1924` | 用 `select(.level==4)` 找 TL4 账号 / 断言 `viewer.level == 4` | 用「最高 active rank」或 id 定位；判据改「最高档无 nextLevel」 |
| `:2064-2068` | `level >= 0 and level <= 4` 形状自洽 | `0 <= level <= activeMax` |
| `:2111-2115` | 每档键必须恰为 `earnable,level,levelSlug,requirements` | 若 v2 公开面加 `id`/`name`，此断言同步扩充 |
| `:766,783` | 账号矩阵 code 只认 `^TL[0-4]$` | 8 档站点需扩到 `^TL[0-8]$`，或改按 id 命名 |
| `:725` | 注释「公开目录 levels[] + newUserDays + viewer.level」 | 跟随 v2 形状（若改名 `tiers[]`） |

---

## 5. 未决点（需要 lead 裁决，本报告不擅自定口径）

1. **公开载荷是否改名。** 计划 §3 D-N2 定义的是**存储** v2（`tiers[]`）；公开读面
   （`publicPayload.levels[]`）与后台读面（`adminPayload.levels[]`）是否同步改名
   `tiers[]`，计划未写。若改名，§3.1 与 §4.3 的断言全部要跟着动；若不改名，
   「`levels[].levelSlug` = tier id」可以零断点兼容（D-N5 的意图）。
2. **`level`（rank）与 `id` 在 wire 上是否并存。** 现在只有 `level` + `levelSlug`；
   N 档下前端需要 `id` 才能稳定编辑（rank 会因插序变化）。D-N1 只说身份/序号分离，
   没说契约字段名。
3. **前端 `TRUST_LEVEL_SLUGS` 的去向。** 若保留为「内置 5 档兜底」，它就是第二份
   真相；若删除，`TrustLevelSlug` 联合类型要放宽成 `string`，会让 5 个测试的
   字面量断言失去类型保护。计划未裁决。
4. **`LevelLeader` 这个常量名。** 它现在同时是「TL4」和「最高档」两种语义
   （`registry.go:75`、`resolver.go:116`、`controller.go:108`）。N 档下必须拆成
   「TL4 的 id」与「activeMax」两个概念，否则 8 档站点的 `All()` 会静默漏掉 4 档以上的键。
5. **`Provider`/`Options` 的 `trust.levels` 归一化在 v1→v2 写回时的行为。**
   `normalizeTrustLevelsOption`（`trust_level_options.go:32-49`）当前是
   「解码→重新编码」，若 v1 输入被编码成 v2，则**已有站点升级后第一次保存就改写库值**；
   这与「只写 v2」一致，但与「回滚到旧版本能读」冲突（计划 §9 已列为待裁决）。

---

## 6. 复算命令

```bash
cd /home/inkedus/Code/github/SForum

# §1-A
rg -n "LevelCount" apps/ | wc -l
rg -n "LevelCount" apps/ | rg -v "_test\.go" | wc -l
rg -c "LevelCount" apps/

# §1-B
rg -n "trust_level" apps/api/database/migrations/ apps/api/database/sqlc/ ; echo "exit=$?"

# §1-C
rg -l "TRUST_LEVEL_SLUGS|TRUST_LEVEL_MAX" apps/
rg -c "TRUST_LEVEL_SLUGS" apps/ ; rg -c "TRUST_LEVEL_MAX" apps/

# §1-D
rg -n "OPENAPI_TL" scripts/verify-trust-levels-acceptance.sh
ls contracts/openapi/paths/ | rg -i trust

# §2 Go 侧
rg -n "LevelNewcomer|LevelMember|LevelRegular|LevelVeteran|LevelLeader" apps/api --glob '!**/*_test.go'

# §2 Web 侧
rg -n "TRUST_LEVEL_ROWS|TRUST_LEVEL_EDITABLE_LEVELS" apps/web/app
rg -n "level === 0|level > 0|level >= TRUST_LEVEL_MAX" apps/web/app/components/admin apps/web/app/components/profile

# §3 契约
rg -n "levelSlug|level" contracts/openapi/paths/trust_levels.yaml contracts/openapi/schemas/trust_levels.yaml

# §4.3 验收脚本
rg -n "lv_n == 5|newcomer,member,resident,veteran,leader|level==4|0\.\.4|TL\[0-4\]" scripts/verify-trust-levels-acceptance.sh
```
