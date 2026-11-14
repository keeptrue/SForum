# 信任等级 — 插件可用的声明面

> **语言切换：** [English (canonical)](../../extensions/trust-levels.md) · 本文为中文翻译。
> 字段名、命令、错误码以英文原文和代码为准；中文版若与代码不一致，以代码为准。

信任等级（`TL0..TLn`）由用户行为 + 可选布尔规则派生。本页只讲**插件今天能声明什么**。
范围刻意收窄：信任等级的事件与读查询尚未实现（见文末「尚未提供」）。

## 1. 事实条件（`userFactConditions`）

通过 Host 命令 `badges.fact.record` 上报事实的插件，要在清单里声明合法的 factKey。
**同一份声明同时喂徽章规则与信任等级规则**。

```json
{
  "userFactConditions": [
    {
      "key": "acme.wiki.pages",
      "contractVersion": "acme.wiki.pages@1",
      "label": { "zh-CN": "页面数", "en-US": "Pages" },
      "unit": { "zh-CN": "个", "en-US": "pages" },
      "compare": ["gte", "lte"]
    }
  ]
}
```

- `userFactConditions` 是中性名，也是 canonical；
- `badgeConditions` 是**兼容别名**：老名字照样能用，两个数组走同一套校验、共享同一个上限；
- 两个数组共同的规则：合计最多 **32 条**、`key` 跨数组唯一、`contractVersion` 必须恰好
  是 `key@N`、扩展必须有 `backend` 且 `protocolVersion: 2`；
- 声明本身不产生数据。**已声明**的 factKey 查不到行时按 `0` 算；**未声明**的 factKey 会让
  叶子变 *unknown*，它所在的档位因此不成立（fail-closed）。

停用插件后，它的 key 会在 5 秒的目录刷新窗口内从候选里消失；引用过它的规则仍然能解码，
只是不再命中。

**目录现状。** 共享条件目录今天 **13** 条，信任等级可用其中 **11** 条（`content.pinned` /
`content.hidden` 只在徽章侧使用）。这两个数字分别由
`apps/api/app/Models/Badges/requirement_text_test.go` 与
`apps/api/app/Models/TrustLevels/config_rule_test.go` 的计数断言钉住。
`identity.profile`（资料填写度：`bio` / `signature` / `website` / `location` 中对应列非空）
计划加入共享目录，届时两边变成 **14 / 12**；今天**尚未提供**，归属见
`knowledge/plans/2026-11-14-trust-level-conditions.md` §3.3。

## 2. 可授予权限（`trustLevelEarnable`）

插件可以允许**自己的**某个权限被信任等级授予：

```json
{
  "permissionDefinitions": [
    {
      "key": "acme.wiki.pages.read",
      "contractVersion": "acme.wiki.pages.read@1",
      "label": { "zh-CN": "阅读页面", "en-US": "Read pages" },
      "description": { "zh-CN": "阅读受限页面", "en-US": "Read restricted pages" },
      "assignmentPolicy": "host",
      "trustLevelEarnable": true
    }
  ]
}
```

- 缺省为 `false`。插件键必须先声明为 `true`，运营才能在后台把它勾进某一档；
- 后台可勾选列表还有第二个来源：核心权限键白名单
  （`trustlevels.EarnableWhitelist()`，七个键）。核心键不经过本字段；
- 字段长在权限定义上，所以「必须在本清单声明过」是**结构保证**，不存在第二个声明入口；
- 核心权限键永远由核心说了算：插件对核心键的声明被忽略，不会覆盖；
- **停用插件不会让已保存的配置失效**：那个键变成 `inactive`——配置保留、不授予、后台显示为
  已停用。

核对时的现状：仓库内置扩展里**没有**任何插件声明 `trustLevelEarnable`，所以默认安装下
后台可勾选列表只有那七个核心键。矩阵要出现插件键，先得有一个插件在清单里声明它。

## 3. 状态与错误码

| 状态 | 含义 | 会授予吗 | 能保存吗 |
| --- | --- | --- | --- |
| `valid` | 核心键，或已启用插件声明 | 会 | 能 |
| `inactive` | 声明它的插件已停用/仅安装 | 不会 | 能 |
| `unknown` | 从未被任何扩展声明 | 不会 | 不能 → `422 earnable_not_declared` |

`earnable_not_declared` **只有一种成因**：当前没有任何扩展声明这个键（也不在核心白名单）。
修法是取消勾选。**没有** `plugin_disabled` 变体——插件停用只会让该键变成 `inactive`，
它仍然可以保存。

解码期**绝不**拒绝未声明键：那会让一次插件停用使整份 `trust.levels` 解不开。解码失败时
整站按「未解析」处理——没有任何已挣得权限（效果等同 TL0），资料页不渲染等级卡，
`identity` 侧每次求值记一条 warn。

## 4. 已提供的事件与尚未提供的读面

落地计划与归属：`knowledge/plans/2026-11-14-trust-level-conditions.md`。

已提供（M4b）：

- `trust_level.changed` —— observe、fail-open。载荷：`userId`、`fromLevel`、`toLevel`、
  `fromTierId`、`toTierId`、`computedAt`。只在**完整**求值结果与变更账本不一致时发出；
  incomplete 时既不发送也不动账本；用户首次出现只落账本、不发事件。

尚未提供：

- 等级目录与单用户当前等级的 HostQuery 读面（M4b，计划 §4.5）；
- 任何让插件在运行期自行求值规则条件的 RPC（计划 §6 第 4 条：求值留在 Host）。

## 5. 回滚口径

`trust.levels` 是单键 JSON，解码走 `DisallowUnknownFields`：配置里出现当前二进制不认识的
字段，**整份**配置被拒绝，不是跳过那一档。所以从带规则的新版本回滚到不认识 `rule` 的旧版本，
要先用新版本把**所有**档位的 `rule` 清空并保存，再换二进制；`color` / `deletedTierIds`
对更早的版本同理（`apps/api/app/Models/TrustLevels/config.go` 记着同一条口径）。
解码失败的后果见 §3：整站未解析，等同 TL0。
