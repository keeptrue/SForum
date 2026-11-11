# 2026-11-11 信任等级：可解锁能力（earnable）保持默认空集，由运营开启

Status: **accepted**（2026-11-11 裁决，实施中）
计划：`../plans/2026-11-11-trust-levels-closing.md` §1（D33 正文）
前置：`2026-11-10-trust-levels.md`（D1–D32）。本文件是**追加**决策，不改写既有条目
（该文件 §13 的冻结约定：新决策追加，不原地改写）。

## 问题

「信任等级解锁能力」是这层机制的产品主张，但**默认安装下它对权限没有任何影响**：

- 默认策略里每档的 `earnable` 都是空集（`TrustLevels/types.go:173-174` 声明「默认空集」，
  `policy.go:11-62` 的 `DefaultPolicy` 压根不设 `Earnable`，D17 的裁决）。
- 默认注册用户组 `member` 已经持有正白名单的全部 7 个键。

两条合起来，白名单集合与 `member` 基线集合**相等**：

| 来源 | 7 个键 | 位置 |
| --- | --- | --- |
| `SeedMemberPermissions` | `topic.create` / `topic.edit_own` / `topic.delete_own` / `post.create` / `post.edit_own` / `post.delete_own` / `attachment.upload` | `apps/api/app/Models/Identity/seeds.go:157-165`（常量字面量在 `:29-59`） |
| `earnableWhitelist` | 同上 7 个，**顺序不同**（`topic.create` / `post.create` / `attachment.upload` / `topic.edit_own` / `post.edit_own` / `topic.delete_own` / `post.delete_own`） | `apps/api/app/Models/TrustLevels/registry.go:26-34` |

复核命令与时间（2026-11-11 09:21 +08:00）：

```bash
grep -n "SeedMemberPermissions = \[\]string{" -A 10 apps/api/app/Models/Identity/seeds.go
grep -n "earnableWhitelist = \[\]string{" -A 9 apps/api/app/Models/TrustLevels/registry.go
rg -n "PermissionTopicCreate\s*=|PermissionAttachmentUpload\s*=" apps/api/app/Models/Identity/seeds.go
```

结论：**集合相等、顺序不同**。顺序差异不是缺陷（前者是种子权限的声明顺序，后者是
UI 展示顺序的唯一权威），但任何「逐字相同」的说法都不精确，别拿它当断言。

由此得出本决策要解决的问题：**「TL2 解锁 `topic.create`」对默认安装是零行为变化**——
每个会员本来就有 `topic.create`，等级解锁的是他**已经拥有**的键。默认安装里等级只影响
两件事：**分档限额**与**等级展示**（含进度提示）。

同时，机制侧已经建好（领域包、白名单校验、`Earnable.Grants` 累积、后台
`eligiblePermissions` 下发、`trust.manage` 写权限、`PUT` 的严格解码与 422 拒绝），
**却没有任何配置入口**。按 D17 的判据，**建好而无人能配置的机制等于死代码**——
它不会报错，只会让人以为「信任等级能解锁能力」，然后在实测里发现什么都不变。

## 决定 D33：走 A 路——把配置入口做完，默认空集保持不变

- **默认 `earnable = ∅` 不动**（D17 不变）。默认安装零行为变更。
- **补上 earnable 编辑器**：后台「站点设置 → 信任等级」按档勾选正白名单内的键；
  白名单来自 `GET /admin/trust-levels` 的 `eligiblePermissions`，**前端零硬编码**
  （硬编码 7 键 = 第二份副本，本仓库最大的 bug 源）。
- **累积语义必须显式披露**：`Earnable.Grants(level)` 从 TL1 累加到目标档
  （`registry.go:55-71`），勾「TL2: `topic.create`」等于 TL2/TL3/TL4 全解锁。
  矩阵按「该档**新增**」呈现，禁止画成「该档拥有」。
- **TL0 不参与**：`Grants()` 对 `level <= LevelNewcomer` 直接返回 nil，TL0 的行为
  限制由 `trust.new_user_*` 七键决定。

## 被否：B 路（Z3，收窄 `member` 基线，7 键改由 TL1 授予）

否决的是「让等级**真正**决定发帖能力」这条更彻底的路：把 `member` 组收窄到不含这
7 个键，由 TL1 的 `earnable` 授予。它能让「TL 解锁能力」在默认安装里可见，但代价不在
配置层，而在**语义层**：

- 现 D9 的开关语义是「关闭 → 已挣得集合恒空」（`ErrTrustLevelsDisabled` 让
  `TrustLevelResolved == false`，D7 失败关闭）。收窄基线之后，**一关这个开关，
  全站会员立刻不能发帖、不能回帖、不能上传附件**——一个「可选功能开关」变成了
  「全站发言总闸」。
- 所以 B 路的前置是**重写「开关关闭」的语义**：从「已挣得集合恒空」改成
  「关闭 = 人人按 TL1 处理」。那是 F 组（开关态行为）口径的整体重写，外加一次
  数据迁移（存量 `member` 组的权限行要按新基线重算），还要重新回答「关开关时
  TL0 限额是否仍然生效」。
- 这不是本刀能塞进去的量，也不该和 earnable 编辑器混在同一次提交里——两者混在一起，
  出问题时无法判断是编辑器写坏了配置，还是基线收窄错了。

因此 B 路**另开一刀**，本刀只做 A 路。

## 后果与残留

1. **A 路落地后，「TL 解锁能力」仍然需要运营主动配置才有效。** 编辑器存在 ≠ 等级
   自动给能力。默认安装的行为与今天完全一致。
2. **默认安装的等级差异仍然只有 TL0 与 TL1+ 之间的限额差异。** 原因不在 earnable，
   而在 D27：TL1–TL4 的限额默认全部继承站点默认，而站点默认的冷却与日限额都是 `0`。
   运营要让「高等级更宽松」，得自己配限额——这是可配置性，不是缺陷。
3. **累积语义会持续误导运营**：勾选动作发生在一个档位行上，生效范围却延伸到更高档。
   UI 文案、行内说明、`aria-label` 三处都必须用「新增」措辞，并在矩阵上方常驻一句
   「勾选某档的键会让该档**及以上**全部解锁」。
4. **白名单是封闭的，且不随运营扩展**：`*_any`、置顶、锁定、封禁、审核、管理类权限
   永远不可勾选（后端 `Earnable.Validate()` 三层校验：白名单 + 管理类禁令 +
   父键禁令，`registry.go:103`，调用点 `config.go:131`；UI 拿不到这些键）。理由是特权缺审计——与 D6 同源。
5. **阈值全 0 = 人人达标**（父计划 §16-1）。earnable 一旦配置，把某档阈值全调成 `0`
   等于「所有人直接落进该档并拿到该档 earnable 的全部权限」（账龄闸仍生效：新人信任期内
   最高 TL1）。这是构造必然，不是 bug，必须写进运营文档。
6. **不做 `trust.view`**（D9 不变），但**论证已换**：不能拿「`badge.view` 是死权限」当前提
   （2026-11-11 实测推翻：`core_catalog_gen.go:44` 有守卫、`adminModules.ts:229` 侧栏在用）。
   现行论证是成本对收益——`trust.manage` 已有四道门（页面 `admin.access` / 侧栏
   `settings.site.manage` / 读 `admin.access` / 写 `trust.manage`，
   `core_catalog_gen.go:231-233` + `adminModules.ts:111-116`），读面已被 `admin.access`
   覆盖；再加 `trust.view` 就是第五个权限位，而新增一个权限位要付 seeds + 迁移 + 权限目录 +
   前端登记点 + 可达性审计。结论不变，理由换掉。

## 实施期实测口径：earnable 的三态（2026-11-11 09:24 补）

计划初稿把 `earnable: null` 写成会 422。**实测不成立**——三态里只有「省略」才 422。
（来源：frontend 对拍时报出，backend 侧代码为判据。）

| 输入 | `decodeEarnableList`（`config.go:194`）怎么走 | 结果 |
| --- | --- | --- |
| `"earnable": null` | `:195` 命中 `null` 分支 → `return nil, nil` | **空集，合法** |
| `"earnable": []` | 走 `json.Unmarshal` → 空切片 | **空集，合法** |
| 省略 `earnable` 键 | 不进解码器：`config.go:102` 的 `len(levelConfig.Earnable) == 0` 先拒 | **422**（`ErrInvalidConfig`，理由是「省略会静默清空该档的已挣得映射」） |

两条连带结论：

1. **`null` 与 `[]` 语义等价**（都表示「该档无新增」），所以「`[]` 与 `null` 语义不同」的说法是错的。
   区别只在形状与可读性：`config.go:118` 只在非空时写进 map，`Normalize()` 后该档为 nil，
   `EncodeConfig`（`config.go:149-151`）把 nil 编回 `[]` ⇒ **往返恒为 `[]`**（J-08 的依据）。
2. 「省略 = 422」不是形式主义：它挡的是「保存成功、配置被静默清空」，与 `limits` 的省略拒绝同源
   （`config.go:98-101`）。所以 UI 侧仍应显式发 `[]`，别靠省略。

复核命令：`sed -n '95,125p;193,206p' apps/api/app/Models/TrustLevels/config.go`（2026-11-11 09:24）。

## 实施要点（给下一位）

- 编辑器接入后，`trustLevelsPayload` 的 `form.earnable[level] ?? []` 兜底**保持 `[]`**：
  三态见上一节（`[]` 与 `null` 都合法且都等于空集，**只有省略才 422**）。别改成发 `null`——
  语义虽同，但 `[]` 是显式意图、后端回读也是 `[]`。
- 「恢复推荐默认」只重置阈值，**不重置 earnable**（重置它等于不可见地改运营配置）。
- 白名单请求失败时，矩阵必须**显式不可编辑 + 错误条**，不许回落成空矩阵——
  空矩阵的语义是「没有可勾的键」，那是谎。
- 文档侧不印阈值数字，也不印端点路径：以站点设置页显示的当前值为准
  （`tests/validate-docs.mjs` 还会把文档里每个 `METHOD /api/v1/...` 与 OpenAPI 对账）。
