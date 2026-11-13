# 2026-11-13 徽章外观的唯一真源：论坛读 SQL，而不是装饰器

## 背景

用户报告「帖子/评论旁的徽章 chip 颜色与后台配置不一致」（图1）。实测链路：

```
$ curl /api/v1/topics/1/comments
"topBadge": { "slug": "dalao", "name": "大佬", "tier": "gold",
              "icon": "i-lucide-thumbs-up",
              "tierName": { "zh": "", "en": "" }, "tierColor": "" }
```

- 后台 `badge_tiers` 里 `gold.color = #fac400`。
- 前端 `forumBadgeTierColor()` 在载荷为空时回落 `BUILTIN_TIER_COLORS['gold'] = 'amber'`，
  即 `--sf-tl-amber-light = #92400e`（棕色）。于是「缺数据」被画成了「另一种颜色」。

根因不是一处，而是**两套并存的投影**：

1. `Models/Forum/badge_ref_scan.go` 的 `topBadgeSelectSQL()` 只投影 4 列
   （`slug,name,tier,icon`），`topBadgeJoinSQL()` 不 JOIN `badge_tiers`。
2. `Models/Badges/badge_reader.go` 的 `Reader`（实现 `forum.BadgeReader`）会带外观，
   但 `PublicReadService.WithBadges` **在生产装配里从未被调用**（`Controllers/Forum/controller.go`
   构造 `NewPublicReadService(service)` 时不注入）。它只在单测里活着。

结果：生产走第 1 条，外观永远为空；第 2 条看起来「已经实现了」，让缺口更难被发现。

## 决定

**论坛读模型里的徽章外观只有一个来源：读 SQL 的 `topBadgeSelectSQL`/`topBadgeJoinSQL`。**

1. 投影扩成 7 列（4 列徽章 + 3 列等级外观），等级用 `LEFT JOIN badge_tiers`：
   等级行缺失时徽章仍展示，只是没有外观（比把整枚徽章丢掉更合理的降级）。
2. 删除第二条投影：`Models/Badges/badge_reader.go`、`forum.BadgeReader`、
   `PublicReadService.badges`/`WithBadges`、`decorateTopic`/`decorateComments` 的 badge 分支。
3. 前端**不再猜颜色**：`forumBadgeTierColor()` 缺载荷时一律返回中性 `slate`，
   删掉内置 `gold/silver/bronze → amber/slate/rose` 的硬编码映射。
   图标的三档兜底保留（图标不是颜色，且未知等级落通用奖章是可接受的）。
4. 读模型新增字段必须换缓存前缀（沿用 `cached_store.go` 的既有约定）：
   `forum:topic:v3→v4`、`forum:topic-slug:v3→v4`、`forum:topic-id-slug:v3→v4`、
   `forum:topics:→v2`、`forum:comments:v2→v3`。

## 为什么不是「装配 WithBadges 就行」

- 它只补 `topic.TopBadge == nil` 的主题，而缓存里的旧读模型是**非 nil 但缺外观**，
  这条分支不会触发；评论路径虽然每次覆盖，却要多一次整页查询。
- 两条路径同时存在时，「有没有外观」取决于走了哪条路——这正是本次 bug 的形态。
- 单一真源后，新增字段只需要改一处列清单 + 一处扫描函数，`badge_columns_test.go`
  的列尾/列数断言会立刻报错。

## 代价与残余

- 论坛读 SQL 每行多 3 列 + 一次 `LEFT JOIN`（`badge_tiers.key` 是主键，代价可忽略）。
- `TopBadgesFor`（store 能力）暂时没有生产调用点：保留是因为它有测试覆盖、且是
  「按用户批量取最高级徽章」的自然读面；模块笔记已标注。若长期无消费者，应连同
  接口一起删除，而不是让它慢慢变成第二个投影入口。
- 前端仍保留 `forumBadgeTierIcon()` 的三档图标兜底：等级 key 可运营自定义，
  未知 key 会落 `i-lucide-award`，这是形状而非颜色，不构成「猜配置」。

## 证据

- 修前：`tierColor: ""`（2026-11-13 20:08 实测）。
- 修后：`tierColor: "#fac400"`、`tierName: {zh:"金牌",en:"Gold"}`（同日 20:48 实测，API 热重载后）。
- 集成测试：`TestPostgresCommentReadCarriesTierAppearance`、
  `TestPostgresTopicDetailCarriesTierAppearance` 在临时 PG 上插 gold 档断言外观非空。
