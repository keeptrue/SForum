# 决策：徽章自由佩戴（`top_badge_id` 语义改为「当前展示的徽章」）

日期：2026-11-13
状态：accepted
上游：`decisions/2026-11-10-badges-and-user-activity.md`（`top_badge_id` 的初版语义与失真窗口）、
`decisions/2026-11-12-badge-rule-engine.md`（`topBadgeOrderSQL` 单源排序）

## 背景：`top_badge_id` 原来只有一种来源

2026-11-10 那一刀里，`users.top_badge_id` 是**纯派生列**：谁排第一就是谁，由
`RecomputeTopBadge` / `RecomputeAll` 按 `topBadgeOrderSQL` 算出来。列里没有任何位
表达「这是用户自己选的」，所以任何一次评估或重算都会按排序把它覆盖掉。

要做「自由佩戴」，最省事的写法是「用户写 `top_badge_id`」，但它必然失败：
下一次 `badges.evaluate_user` job 或运营点一次重算，用户的选择就被排序改回去。
症状是「佩戴了，刷新就变回去」，而且**没有任何报错**——写入成功、读取也成功，
只是另一个写者按自己的口径覆盖了它。

## 裁决

### D1 `top_badge_id` 的语义改为「**当前展示的**徽章」，新增 `top_badge_manual` 区分来源

迁移 `202611130003_badge_wear.sql`：

```sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS top_badge_manual BOOLEAN NOT NULL DEFAULT FALSE;
```

两列的语义写进 `COMMENT ON COLUMN`（不只是文档）：`top_badge_id` 是「当前展示的徽章」，
`top_badge_manual = TRUE` 表示它是用户选的而不是排序推导的。

为什么是**布尔**而不是 `display_source TEXT`（`auto` / `manual` / `stale`）：
状态只有两种；第三个状态（「用户选了但已失效」）在有效期为 0 的瞬间就被回落逻辑
清成 FALSE，不需要持久化。多一个枚举值只会多一处「忘了处理它」。
`DEFAULT FALSE` + `IF NOT EXISTS` 让存量行自动落到「自动推导」，与旧语义一致。

Down 段只删 `top_badge_manual`（`top_badge_id` 本来就在）：删了之后已佩戴的用户会保留
那枚徽章作为展示徽章，直到下一次重算按排序改回来。这是可接受的回滚代价——没有这一位就无法
表达「用户选择」，写入它反而是假状态。

### D2 佩戴写入是**一条 CTE**，校验与写入同一个快照

`SetWornBadge`（`Badges/wear_store.go`）：

```sql
WITH target AS (
  SELECT badges.id FROM badges
  JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
  JOIN user_badges ON user_badges.badge_id = badges.id AND user_badges.user_id = $1
  WHERE badges.slug = $2 AND badges.enabled
)
UPDATE users SET top_badge_id = (SELECT id FROM target), top_badge_manual = TRUE
WHERE users.id = $1 AND EXISTS (SELECT 1 FROM target)
```

校验（徽章启用 + 等级启用 + 该用户持有）与写入分两条语句时，两个并发请求之间可能夹着一次
收回，结果是把已经不该展示的徽章写进展示位。`RowsAffected() == 0` 即 `ErrBadgeNotOwned`。

**不在 Go 里做二次校验**：两处校验迟早分叉，而分叉后前端看到的是「接口说成功、展示位没变」。

### D3 拒绝码只有一个：`ErrBadgeNotOwned` → `422 forum.badge_not_owned`

不区分「没持有」「已停用」「等级停用」「slug 不存在」。对手方（普通用户）不需要知道
站内是否存在这枚徽章，区分只会变成一个探测面；前端也只需要一句「先获得它」。

`PUT` 的 body 解析失败是**另一码**：`400 forum.badge_worn_body_invalid`。
合成一码会让前端把解析失败也提示成「这枚徽章不在你的徽章墙里」，用户会去重新获得
一枚他早就有的徽章。用 400 而不是 422 的理由是：此时服务端连 slug 都没读到，
「你没持有这枚徽章」是一句谎话。

`errWearStoreUnavailable`（装配里没有佩戴能力）上抛 **500**，绝不折成
`ErrBadgeNotOwned`——那会让「忘了接 store」表现为「你没获得过这枚徽章」，运维永远查不出来。

### D4 排序**只有一份实现**：`ROW_NUMBER() OVER (ORDER BY topBadgeOrderSQL)`

`DisplayBadge` 一次查询取回「该用户持有的全部可展示徽章 + 名次」：

- `Automatic` = 名次 1（与 chip、`top_badge_id` 的自动推导完全一致）；
- `Worn` = `users.top_badge_id` 指向的那一行（**按 id 精确对，不按名次猜**）；
- `Upgrade` = 手动模式下展示位不是名次 1（`wornChoiceRank > automaticRank`）时给出的候选。

在 Go 里另写一次「谁更好」的比较是这一节最容易被重构掉的东西：运营调档位后 chip 与提示会分叉，
而两条路径都不会报错。名次一律由 SQL 给。

### D5 重算**让路**，失效则**回落并清标记**

`RecomputeTopBadge` 与 `RecomputeAll` 都读同一段 `wornBadgeStillValidSQL`：

```sql
EXISTS (SELECT 1 FROM user_badges AS worn
        JOIN badges AS worn_badge ON worn_badge.id = worn.badge_id AND worn_badge.enabled
        JOIN badge_tiers AS t ON t.key = worn_badge.tier AND t.enabled
        WHERE worn.user_id = users.id AND worn.badge_id = users.top_badge_id)
```

三个条件与 `topBadgeOrderSQL` 的 JOIN 完全一致（enabled + 等级 enabled + 仍持有）：
排序查询排除掉的行，这里也必须判为失效，否则会出现「展示位保留了一枚排序里根本不存在的徽章」
——chip 不显示，而佩戴状态说在戴。

`top_badge_manual = (users.top_badge_manual AND wornBadgeStillValidSQL)` 与
`top_badge_id = CASE WHEN manual 且仍有效 THEN 保持 ELSE 排序第一 END` **必须同一条语句**：
分两条会让「标记已清、展示位还是旧的」或反过来。

### D6 `ClearWornBadge` **先清标记再重算**

顺序反了的话 `RecomputeTopBadge` 会看到 `manual=TRUE` 而拒绝覆盖，于是「取消佩戴」
变成一个只改了标记、展示位没动的假操作。

### D7 三条端点返回**同一个** `BadgeDisplay`；PUT/DELETE 后**重读**

`GET/PUT/DELETE /api/v1/badges/worn` 都是 login 守卫（`h.actor` → 未登录 401）、
`private, no-store`、响应体都是 `{mode, worn, automatic, upgrade}`。

PUT/DELETE 成功后不回 `{ok:true}` 而是再读一次 `DisplayBadge`：展示位可能在同一条语句里
被回落逻辑改写（例如佩戴的徽章刚好被停用），只有重新读出来的才是真的。
前端也因此只有一种响应形状，不需要为「改完之后长什么样」维护第二套推导。

`worn` / `automatic` 允许为 nil（没有任何可展示徽章）；`upgrade` 只在「用户选择比自动推导差」
时非 nil——**它不是错误，是提示**。三者都用 `BadgeRef` 而不是只给 slug：
前端要立刻渲染「你现在可以换成 X」，只有 slug 会迫使它再查一次徽章目录。

### D8 三条路由是**静态段**，必须排在任何含参数段之前

Fiber 按注册顺序匹配。顺序错了的症状是 `/badges/worn` 被当成 slug 解析
（200 返回空徽章而不是 404，很难反推）。`route_order_test.go` 钉住了
「任何含参数段都不得排在它前面」，将来加 `/badges/:slug` 时会立刻红。

### D9 脏数据按**自动模式**呈现，不在读路径清标记

`top_badge_manual = TRUE` 但 `top_badge_id` 指向已停用/已删徽章时，`DisplayBadge` 返回
`mode=auto`、`worn=automatic`。理由：chip 那边同样不显示那枚无效徽章，报 `manual`
会让前端显示「佩戴中」而 chip 是空的。真正的清标记发生在下一次评估/重算（见 D5）。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| 只写 `top_badge_id`，不加标记位 | 下一次评估/重算按排序覆盖，症状是「佩戴了，刷新就变回去」且无报错（见背景） |
| `display_source TEXT` 三态枚举 | 第三态有效期为 0，不需要持久化；多一个值多一处「忘了处理」（见 D1） |
| 新开 `user_badge_display` 表 | 展示位本来就是 `users` 上的一个反规范化列，列表页靠它单列 JOIN；搬进新表等于给每次列表读加一次 JOIN，换来的只是「语义更纯」 |
| 校验放 Go、写入放 SQL | 两条语句之间可能夹着收回（见 D2） |
| 在 Go 里再算一次「谁更好」 | 运营调档位后 chip 与 `upgrade` 提示会分叉，且两边都不报错（见 D4） |
| 失效时把展示位留成 NULL | 用户看到 chip 消失却不知道为什么；「失效即回落」才是可解释的 |
| 读路径顺手清 `top_badge_manual` | 读路径变成写路径；且一次读失败会把标记留在半清状态（见 D9） |
| 拒绝时区分「不存在」与「没持有」 | 多一个探测面（见 D3） |
| PUT/DELETE 回 `{ok:true}` | 展示位可能被回落逻辑改写，前端拿到的是过期事实（见 D7） |

## 顺带修掉的两个既有缺陷（都不是本轮引入）

**F1 集成 fixture 缺 `top_badge_manual` 列，该包原本就是红的**
现象：`go test ./app/Models/Badges/` 在干净 HEAD 上以
`column users.top_badge_manual does not exist` 失败。
根因：测试自己建的 `users` DDL 是手抄的，加列时没人同步。
判据：fixture 的 DDL 必须与迁移逐列对齐；漏列会让整包测试变红——**看到这条报错先查 fixture，
不要怀疑迁移没跑**。`badges_postgres_integration_test.go` 与
`badges_tiers_postgres_integration_test.go` 两处都已补上，并在注释里写明原因。

**F2 `DisplayBadge` 的两个写法会让整个佩戴面板 500 或给出空徽章**
现象一：`cannot scan NULL into *string`，佩戴面板 500。
根因：`LEFT JOIN` 对「没有徽章 / 徽章已停用」的行给出全 NULL，直接把 NULL 扫进 `string`。
修法：`COALESCE(badges.slug,'')` 等，判别「这行有没有徽章」靠**另一列**（`badges.id IS NULL`）。
现象二：`automatic` 变成一枚空 slug 的 `BadgeRef`，chip 渲染出没有名字的空徽章。
根因：用 `user_badges.badge_id` 判别「这行有没有徽章」——徽章停用/删除时授予行仍在，
只有 `badges.id` 变 NULL，于是这类行被当成「有效候选」。
判据：判别列必须是 JOIN 后的 `badges.id`；`COALESCE` 只负责让 Scan 不炸，不承担判别职责。

## 陷阱（现象 → 根因 → 判据）

**T1 「佩戴了但重算改回去」**
根因：`RecomputeTopBadge` / `RecomputeAll` 少读 `top_badge_manual`（或读了但没判有效）。
判据：`manual=TRUE` 且徽章仍有效时，重算后 `top_badge_id` **不变**；`manual=TRUE` 但徽章已停用时，
重算后 `top_badge_id` 变成排序第一、`top_badge_manual` 变 FALSE。两个方向都要断言。

**T2 取消佩戴是假操作**
根因：`ClearWornBadge` 先重算后清标记（或只清标记不重算）。
判据：取消后 `top_badge_manual = FALSE` **且** `top_badge_id` 等于排序第一（不是 NULL、不是原值）。

**T3 停用档位后佩戴位残留**
根因：`wornBadgeStillValidSQL` 少了 `JOIN badge_tiers AS t ... AND t.enabled`。
判据：停用徽章所在档位 → 佩戴位回落。与 `topBadgeOrderSQL` 的三条 JOIN 必须逐条对齐，
**用 JOIN 不用 LEFT JOIN**（停用一档等价于「这一档不存在」）。

**T4 跨用户写**
根因：`SetWornBadge` 的 UPDATE 少了 `WHERE users.id = $1`，或 CTE 的 `user_badges.user_id` 用了
请求体里的 userId。
判据：A 的会话 PUT 一枚只有 B 持有的徽章，必须 `422 forum.badge_not_owned`，且 B 的展示位不变。

**T5 前端把 `mode` 当「有没有徽章」**
根因：`worn` 可以为 nil，而 `mode` 恒有值。用 `mode === 'manual'` 判断渲染佩戴态，
在「manual 标记指向脏数据」时会显示「佩戴中」而 chip 是空的。
判据：以 `worn` 是否为 nil 决定渲染，`mode` 只决定文案与按钮。

## 残留（2026-11-13 审查后补，R1 / R2）

来源：`../reports/2026-11-13-badge-notify-wear-progress-review.md`。两条都不是用户可见缺陷，
但都会误导后来的人，所以按「现象 → 根因 → 判据」写死。

### R1 `RecomputeAll` 在「佩戴的徽章被删除、且该用户没有其它可展示徽章」时不清 `top_badge_manual`

**现象**：库里留下 `top_badge_manual = TRUE` + `top_badge_id = NULL` 的行。
用户看不到任何异常：`DisplayBadge` 在 `manual && wornChoice == nil` 时按 auto 呈现
（`wear_store.go:199-210`），chip 也不显示那枚已删徽章。

**根因**：删除徽章时 FK `ON DELETE SET NULL` 把 `users.top_badge_id` 置空
（`202611100002_badges_and_user_activity.sql:105`），于是 `RecomputeAll` 的子查询里
`best.badge_id` 也是 NULL（该用户没有其它可展示徽章）。整行的 `SET` 本会写
`top_badge_manual = FALSE`，但 `WHERE users.top_badge_id IS DISTINCT FROM
CASE WHEN best.manual_valid THEN users.top_badge_id ELSE best.badge_id END`
两侧同为 NULL → `IS DISTINCT FROM` 为假 → **整行被跳过**，标记停在 TRUE。

**判据 / 纪律**：

- 这是**自愈型脏标记**：下一次 `RecomputeTopBadge`（授予、收回、取消佩戴都会触发）会把它清成 FALSE；
  在此之前读面行为正确，所以**不要**为它加迁移或补扫 job。
- **不要为了清这个标记去放宽 `refreshed` 的计数语义**。`refreshed` 只数 `top_badge_id` 真的变了的行
  （残留 1：它与 `granted` 相互独立）；这里确实没有任何可见变化，把「标记清理」算进计数会造出一个
  运营看不懂的数字。真要修，判据是「`top_badge_manual` 变了但 `top_badge_id` 没变也算一行」——
  那是改 `refreshed` 的产品口径，属独立开单，不是顺手改 SQL。

### R2 `RecomputeAll` 的失效回落只覆盖四种失效里的一种

**现象**：`RecomputeTopBadge` 的失效回落有四个子测试（徽章停用 / 授予被收回 / 徽章被删除 /
档位停用，见 `TestRecomputeFallsBackWhenWornBadgeStopsBeingValid`），`RecomputeAll` 只有
`TestRecomputeAllClearsInvalidWornBadge`（徽章停用）+ `TestSetWornBadgeIsKeptByBothRecomputePaths`
（有效 manual 不被覆盖）。

**根因**：两条路径的 manual 判断各写了一遍（单用户 `CASE` 与全量 `manual_valid` 子查询），
但**有效性谓词是同一个常量** `wornBadgeStillValidSQL`（`postgres_store.go:435`），
四条件已被单用户路径逐一钉死；两条路径的差异只剩 `CASE`/`WHERE` 分支逻辑，
而该分支已由「1 个失效 + 1 个有效」用例覆盖。

**判据**：风险实质低，但**覆盖不对称**。下一刀按「4 种失效 × `RecomputeAll`」参数化补齐
（把 `TestRecomputeFallsBackWhenWornBadgeStopsBeingValid` 的 `cases` 表复用到全量路径），
**不要**为了「让两条路径看起来一样」去合并两段 SQL——单用户是一条 `UPDATE`、全量是
`FROM (SELECT …) best` 的自连接，合并只会让两边都变慢。

## 复核点

1. 任何新的「展示位写者」都必须同时写 `top_badge_manual` **与** `top_badge_hidden`，否则会出现
   「自动推导覆盖用户选择」、「用户选择永久粘住失效徽章」，或 hidden 态佩戴只写半边得到
   「200 + mode=hidden」的假成功（D11）。
2. 排序、有效性、回落三处读的是**同一段** `topBadgeOrderSQL` / `wornBadgeStillValidSQL`；
   想加一条排序键时三处一起改。
3. `upgrade` 是提示不是错误；把它渲染成警告色或 toast 会让用户以为佩戴失败了。

## 2026-11-13 修订二 · D10 取消佩戴 = 不佩戴任何徽章，「自动展示」是独立出口

> 上一轮（同日「徽章 UI 五处收口」，见 `../sessions/2026-11-13-badges-ui-closeout.md`）的
> label-in-name 结论**仍然成立**：可访问名就是可见文字，状态只走 `aria-pressed`。本修订只改
> **可见文字的内容**（状态词 → 动作词）与它背后的请求，不改「可见文字 = 可访问名」这条绑定。

**裁决**：

1. 卡片与详情弹窗上那颗按钮，已佩戴时可见文字与 `title` 都是「取消佩戴」，点了发
   `PUT /badges/worn {hidden:true}`（`useBadgeWorn.hide()`）。它**不再**走 `DELETE`（`clear()`）：
   「取消佩戴」若回落成自动展示，用户看到的是「换了一枚」而不是「不戴了」——一个按钮吞掉了
   两个语义，而且「什么都不戴」从此没有入口。
2. 资料页底部状态条常驻两个并列出口：「自动展示徽章」（`DELETE` → auto）与「不佩戴徽章」
   （`PUT {hidden:true}`）。按 `mode` 高亮（`auto` / `hidden` 各自 `aria-pressed`），当前态那颗
   点了不发请求——guard 在 handler 里，**不用 `disabled`**（键盘用户不能因此丢掉这一格）。
3. `mode=auto` 时旧的「恢复自动展示」按钮是**可见空操作**：它本来就处于 auto，点它不产生任何
   状态变化。页面上留一个点了什么都不发生的按钮，比不显示更糟；改为「自动展示徽章」并让它按下，
   用户才能读懂「当前就是自动」。
4. 文案族随之收口：删 `badges.worn.wearing` / `badges.worn.restoreAuto`，新增 `badges.worn.auto`，
   `unwearSuccess` 改名 `autoSuccess`（只可能由 auto 出口触发，名字必须跟出口对齐），
   `hideSuccess` 口径改为「已不佩戴任何徽章」。locale 来源登记在
   `apps/web/i18n/locales/README.md` 的 `badges.worn.*` 一节（YG UI 复核，非规范原文）。

**为什么不把「不佩戴」编码成某个哨兵徽章 id**：哨兵会污染排序、目录与通知目标的口径。第三态
由 `users.top_badge_hidden` 承载，与 D1 拒绝 `display_source` 枚举同一个理由——能用一个布尔
表达的状态，不要开枚举。

**判据**（删掉实现必须红）：点已佩戴那颗 → `PUT {hidden:true}` 且整轮无 `DELETE`；点当前态底部
出口 → `calls` 长度不变且无 toast；已佩戴按钮的可见文字与 `title` 都是「取消佩戴」。

## 2026-11-13 修订三 · D11 hidden 行不进全量重算作用域，半坏行由单用户重算收敛

**背景**：hidden 落地后 YG 实测「hidden 态点佩戴，提示成功但没戴上」。两处根因都落在「三列同源」
这条不变量上：`SetWornBadge` 漏清 `top_badge_hidden`（200 + `mode=hidden` 的假成功），
`RecomputeAll` 把 hidden 行算进作用域（全量重算会碰它，变更计数与「什么都不戴」的语义打架）。

**裁决**：

1. `SetWornBadge` 一条 UPDATE 写齐三列（`id=X, manual=TRUE, hidden=FALSE`）；`ClearWornBadge`
   同时清两标记再重算。这是 D10「三个出口」在服务端的对应实现，**前端不承担补偿**。
2. `RecomputeAll` 用**作用域闸门** `AND NOT users.top_badge_hidden` 把 hidden 行挡在重算域外，
   `SET` 里的 hidden 分支只作纵深防御。**不用「把 `WHERE` 的 `CASE` 镜像成 `SET` 的三分支」**：
   那种写法靠两份 `CASE` 恰好相等推出不命中，改 `SET`（多一种展示态）忘了同步 `WHERE` 就原地
   复现，且除测试外没有任何现象提示。闸门只有一处定义「谁属于重算域」。
3. `RecomputeTopBadge` 对 hidden 行写 `top_badge_id = NULL`（不是保持原值）：`hidden=TRUE` 时展示位
   的定义就是空，`(hidden=TRUE, id 非空)` 是半坏行——chip 会显示一枚，而面板说「未佩戴」。写 NULL
   对规范行幂等、对脏行是收敛；`manual` 分支同时加 `NOT top_badge_hidden`，避免把 hidden 行当成
   「用户选择」保留下来。
4. **不清 manual 计数**：`refreshed` 仍只数 `top_badge_id` 真的变了的行。半坏行收敛时 `manual` 可能
   由 TRUE 变 FALSE 而 `top_badge_id` 本来就是 NULL，这类「用户看不见的变化」不计数——沿用 R1 的
   纪律，别为了清标记放宽计数语义。

**判据**：`TestHideWornBadgeClearsSlotAndSurvivesRecompute`（隐藏后两条重算都不回落，第二次
`RecomputeAll` 的 `refreshed = 0`）、`TestWearingBadgeAfterHideUnhides`（hidden → `PUT {slug}` 得到
`mode=manual` + `worn` + `upgrade`，且 `hidden=FALSE`）、`TestRecomputeTopBadgeRepairsHalfBrokenHiddenRow`
（半坏行收敛成 `(NULL, FALSE, TRUE)`，`RecomputeAll` 不碰它且 `refreshed = 0`）。
