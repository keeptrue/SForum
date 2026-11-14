# 信任等级 — 插件可用的声明面

[English](../../extensions/trust-levels.md) · 简体中文

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

- 缺省为 `false`。只有声明为 `true` 的键才会出现在后台可勾选列表里；
- 字段长在权限定义上，所以「必须在本清单声明过」是**结构保证**，不存在第二个声明入口；
- 核心权限键永远由核心说了算：插件对核心键的声明被忽略，不会覆盖；
- **停用插件不会让已保存的配置失效**：那个键变成 `inactive`——配置保留、不授予、后台显示为
  已停用。

## 3. 状态与错误码

| 状态 | 含义 | 会授予吗 | 能保存吗 |
| --- | --- | --- | --- |
| `valid` | 核心键，或已启用插件声明 | 会 | 能 |
| `inactive` | 声明它的插件已停用/仅安装 | 不会 | 能 |
| `unknown` | 从未被任何扩展声明 | 不会 | 不能 → `422 earnable_not_declared` |

`earnable_not_declared` 带 `params.variant`：

- `undeclared`——从来没有谁声明过这个键：取消勾选；
- `plugin_disabled`——声明它的插件未启用：启用它（插件确实没了就取消勾选）。

解码期**绝不**拒绝未声明键。否则一次插件停用就让整份 `trust.levels` 解不开，全站等级静默
退回默认策略。

## 4. 尚未提供

- `trust_level.changed` 事件（M4b）；
- 等级目录与单用户当前等级的 HostQuery 读面（M4b）；
- 任何让插件在运行期自行求值规则条件的 RPC。
