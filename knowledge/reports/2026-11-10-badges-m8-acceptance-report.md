# 徽章 + 用户活动统计 — M8 验收报告

Date: 2026-11-10
审查：小审（reviewer）
对象：`plans/2026-11-10-badges-and-user-activity.md` §0 十条验收口径 + §6 边界与失败模式
环境：dev 库（`sforum`，15432）+ dev API（8080）+ dev Web（3000），一次性写操作经 `SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1` 显式放行

---

## 结论

**通过。84 条断言 0 FAIL，浏览器 16 条结构断言全过，2 条 pend 为限流且已由前一轮 403 交叉验证。**

| 维度 | 结果 |
| --- | --- |
| 后端验收断言 | **84 ok / 0 FAIL / 2 pend**（`--phase=full`，退出码 0） |
| 浏览器结构断言 | **16 / 16 通过**（1633×810 与 390×844 各一轮；截图 15 张 = 9 张页面级 + 6 张结构审计） |
| 双门禁 | `generate.mjs --check` → `V3 P0 catalogs are current (18 files)` EXIT 0；`validate-architecture-boundaries.mjs` → `passed (2053 files, 206 above 500)` EXIT 0 |
| 目录 diff | 10 文件 **+532 / −0，零删除**；身份总数 787 = 405 路由 + 292 组件 + 90 页面 |
| 棘轮 | `SFTopicShowPage.vue` 1127 = 1127、`postgres_store_ops.go` 1136 = 1136，**贴顶合法**（`实际 > cap` 才判红） |

**2 条 pend 的性质**：`S7-2 普通用户改徽章`、`S7-3 普通用户触发全量重算` 本轮返回 429（dev 的 admin 写端点限流）。**同两条在上一轮为 403 通过**。交叉验证的价值不在于「这次能过」，而在于「同一断言在两次独立运行中结论一致」——限流是环境状态，不是权限语义变化。

### §0 十条口径 → 证据映射

| 口径 | 判定 | 证据段 |
| --- | --- | --- |
| 1 发主题/评论计数 +1 且刷新保持 | 通过 | S1-1 / S1-2 |
| 2 获赞 / 给赞 +1、取消回退 | 通过 | S1-3a / S1-3b / S4-1 / S4-2 |
| 3 同日访问只算 1 天、跨天 +1、`(user_id, visited_on)` 唯一 | 通过 | S3-1…S3-5 |
| 4 达阈值自动授予、重复触发不重复授予 | 通过 | S2-1 / S2-2 / S12-1f |
| 5 资料页分组 + tier 排序；帖子旁只显示最高级 1 个 | 通过 | S8-1 / S10-2 / 浏览器 C1 |
| 6 手动授予/收回生效、授予记录可查、自动徽章不可手动收回 | 通过 | S6-1 / S6-4 / S12-2b |
| 7 `features.badges` 关闭：徽章面 403、统计照常 | 通过 | S6-5a…S6-5d |
| 8 迁移 up/down 实机可逆 | 由 `scripts/verify-migration-reversible.sh` 覆盖 | 本报告未复跑该脚本，结论以它自身输出为准 |
| 9 门禁全绿 | 通过 | 双门禁 + 棘轮 |
| 10 浏览器 QA（两档视口、无溢出、aria） | 通过 | 浏览器 A/B/C 组 |

---

## 缺陷栏（产品问题，均已修）

### 1. P0 — 首页主题列表全站 500

- **现象**：`GET /api/v1/topics` 及其所有参数组合一律 500；`/api/v1/topics?limit=10`、`?limit=5&page=1&category=general` 同样。
- **日志**：`request failed error="number of field descriptions must equal number of destinations, got 38 and 42"`（同一错误累计 8 次）。
- **根因**：`scanTopicSummaryWithAvatar` 新增 4 个徽章 scan 目的地后共 **42** 个；`topicSummarySQL()` 同步带了 `topBadgeSelectSQL()`/`topBadgeJoinSQL()`（42 列），但 `ListTopics` 的 hydrate SELECT（`list_topics.go`）与 `search_live.go` 各自**另抄了一份列清单**，都缺那 4 列 → **38 列 vs 42 目的地**。`postgres_store_bookmarks.go` 复用 `topicSummarySQL()`，未受影响。
- **潜伏同类**：`/search` 当时返回 200 只是因为命中 0 条走了 early return，第一个搜索结果出来即 500。
- **修法**：抽共享列清单 `topicSummaryColumns` 落到三处（`list_topics.go` / `search_live.go` / `topic_summary_sql.go`），不是补 4 列。
- **验证**：S10-1a/1b/1c/1d 四条探针（见下）。

### 2. 后台 13 条徽章路由无服务端权限校验

- **现象**：纯 `member`（甲，user 3）`GET /api/v1/admin/badges` → **200**；`POST /admin/badges/recompute` → **200**（真的执行）；只持 `badge.view` 的账号 `POST /admin/badges` → **201，真建出了徽章**。
- **根因**：路由目录 `core_catalog_gen.go` 里 `badge.view`/`badge.manage` 声明齐全，但**那不是运行时守卫**；handler 只做了认证（`actor()`），领域层方法签名不带 actor，权限校验无处生效。
- **修法**：权限收进领域层——`AdminService` 12 个方法全部接收 actor，`badge.*` 在 Go 代码中从 **0 处引用变为 16 处**。
- **验证**：S7（甲四写全 403、匿名 401、戊 200）、S7b（丁 `GET` 200 / 三写 403 且 reason 不是 `badges_disabled`）。
- **夹具纯度由脚本自证**：S7 跑前查甲是否持有 `badge.manage`/`super_admin`（有则 pend）；S7b 跑前查丁的角色与权限行数（本轮：`member,qa_badge_viewer`，`badge.view` 1 行、`badge.manage` 0 行）。

---

## 统计正确性（含最硬的并发不变量）

| 断言 | 操作 | 期望 → 实际 |
| --- | --- | --- |
| S1-1 | 发主题 | `topic_count` +1 → +1 |
| S1-2 | 发评论 | `comment_count` +1 → +1 |
| S1-3a/3b | 乙赞甲的主题 | 甲 `like_received` +1、乙 `like_given` +1 → 均 +1 |
| S1-4 | 乙收藏 | `bookmark_count` +1 → +1 |
| **S1-5a/5b/5c** | **甲自赞** | `topics.like_count` +1 → +1；甲自己两个 like 计数 **0 变化** → 0 |
| S4-1/4-2 | 取消点赞 | 回到基线、且 >= 0 |
| S3-1…S3-5 | 同日 5 次详情请求 | `user_visits` 今日 1 行、`days_visited` +1；清 `badges:visit:{uid}:*` 后再请求仍 1 行、不重复累加 |
| **S5-1/5-2/5-3** | **10 个不同账号并发点赞** | 10 次全部成功；`content_reactions` 恰好 **+10**；作者 `like_received_count` **恰好 +10**；`GROUP BY user_id HAVING count(*)>1` = **0** |

S5 是这一刀最硬的不变量证明：**最终数值精确等于行数**，且每个用户至多 1 行——单测证明不了并发最终值，只有真打接口 + 真查库能证明。

自赞（D3）值得单列：它把「内容 `like_count` 照常 +1」与「两个统计列不动」拆成两条独立断言。合成一条会掩盖「要么全加、要么全不加」这类最常见的实现错误。

---

## 徽章与后台语义

| 断言 | 结论 |
| --- | --- |
| S2-1 / S2-2 | 10 次评估 → `user_badges` 恰好 1 行；`top_badge_id` 指向正确 |
| S6-1 | 授予后 `top_badge_id` 指向临时徽章 |
| S6-2a/2b/2c | 停用后稳定视图不输出该授予行、公开端点不返回它、**`top_badge_id` 触发重算回落到 4** |
| S6-3a/3b | 删除后 `top_badge_id` 不再指向它、`user_badges` 级联清空 |
| S6-4 | 自动徽章手动收回 → 422 `forum.badge_manual_only` |
| S6-5a…5d | 关闭开关：端点 403 `forum.badges_disabled`、资料页仍 200 且带 `topicCount`、`forum_user_activity` 视图仍有行、恢复成功 |
| S6-6b/6c/6f | 部分更新：只改 `enabled` 其余原样；只改 `name` **不复活 `enabled=true`**；自动改手动后 `metric`/`threshold` 变 NULL |
| S7c | 公开徽章响应不含 `grantedCount`/`enabled`/`grantedBy`/`grantReason`/`createdAt` |
| S8-1 | 资料页 ≤10s 出现已授予徽章（不走 Forum 缓存） |
| S12-1b…1f | 重算**真的补授予**：`granted` 非零、行带 `granted_by IS NULL` + `grant_reason='recompute'`、二次重算 `granted=0`、`sum(topic_count)` 前后不变（重算不抹历史） |
| S12-2b | 对未授予用户收回 → 200 且 `revoked=false` |
| S12-3b | 分组 `PATCH {"position":5}` → `position\|description = 5\|keep me`（description 不被清空） |
| S12-4d | 停用后 `grantedCount` 仍非零 |

---

## 浏览器 QA

三处页面 × 两档视口，16 条结构断言全过；每张截图都带必需元素校验，缺元素即非 0 退出并落现场图（不把「截图成功」当「页面正确」）。

| 组 | 断言 |
| --- | --- |
| A 后台 `/control-panel/badges` | 12 行数据、含内置徽章名、1633 与 390 均无横向溢出 |
| B 帖子旁 chip | 存在 2 个、`a a` 嵌套 = 0、byline chip 是 `<a href="/u/…?tab=badges">` 且在作者链接**外面**、与作者链接矩形不重叠、aria-label 完整 |
| C 资料页（丙零基线） | 12 个徽章全渲染、未获得的手动徽章 aria-label 为「金牌徽章：社区贡献，未获得」「银牌徽章：优质解答，未获得」、页面无「0/0」文案、两档无溢出 |

### chip 三处两形态（设计决定，不是缺陷）

| 位置 | 形态 | 原因 |
| --- | --- | --- |
| 主题 byline | `<a href="/u/…?tab=badges">` | 在作者链接**外面**，可独立点击 |
| 评论身份行 | `<span role="img">` | 在作者 anchor **内部**；嵌套 `<a>` 是 HTML 规范禁止的 |
| 评论预览按钮 | `<span>` | 按钮里不能再放可点元素 |

评论身份行若改成独立 `<a>`，会与作者链接并排两个同目标链接：屏幕阅读器重复播报「某某某，链接」，视觉上两个可点区域指向同一处。现方案（span + `role="img"` + 点击走父 anchor）是正确取舍。

---

## 验证工具修复（**不是产品缺陷**）

这一节是给下一个写验证脚本的人看的流程教训，不计入缺陷栏。

1. **`storage/qa/badges-shots.mjs` 的后台选择器从来不存在**：脚本要求 `[data-badge-admin-page]` 与 `.sf-admin-badge-row`，而实现是 Nuxt UI 的 `<UTable>`，数据行就是 `table tbody tr`。原样跑会**稳定假 FAIL**（第一次运行即 `FAIL(3)`），如果没人跑，M8 会交出一张「后台页不可用」的假证据。已校准为 `table tbody tr`，现 `ok:true`、12 行可见。
2. **断言过严同样有害**：结构探针初版要求「所有 chip 都是 `<a>`」，把合法的 `<span role="img">` 判成失败。断言过严和过松一样危险——前者制造假 FAIL，最终让人开始忽略真 FAIL。已按容器拆成 byline / 评论身份行两条。
3. **`--read-only` 曾在非开发库上不生效**：原先只在「开发库哨兵」分支里关写操作，换成一次性集群时仍会造数据，把「只读」变成「只读但会写」。已改为无条件 `MUTATIONS=0`，实测 `badges 12→12`、`topics 1258→1258`。
4. **`-o` 文件不截断导致假证据**：curl 连接失败时不截断 `-o` 目标，上一份响应会冒充本次结果（曾出现「实际 000，body 却是上一个 session 响应」）。已改为每次调用前清空 body，并对 000 短退避重试。

守则（已写进三个脚本头部）：**选择器与断言必须对着真实 DOM 校准过；未校准的守卫比没有守卫更坏。**

---

## 残留与已知行为（**不要当 bug 修**）

1. `Recompute` 的 `granted` 只数新插入行，`refreshed` 只数 `top_badge_id` 实际变化人数，两者独立——`{granted:0, refreshed:0}` 是正常的幂等结果。
2. `revoked: false` 表示该用户本来就没持有这枚徽章，不是失败；DELETE 保持幂等，两种结果都是 200。
3. 删除用户**当前最高级**徽章后 `top_badge_id` 变 NULL，即使他还有其它徽章，帖子旁 chip 也会消失到下一次评估/重算。计划 §6 明确（`ON DELETE SET NULL`），运营视角容易误读，建议后台删除前提示「该用户还有 N 个徽章，删除后需重算才恢复显示」。
4. `badge.view` 单独存在是**死权限**：可达性前提是 `admin.access`，只给 `badge.view` 不给后台入口权限时，用户进不去后台。
5. 徽章进 `CachedStore`：授予后帖子旁最多 **20–30s** 延迟可见（topicDetail 30s / comments 20s）；资料页不走 Forum 缓存，≤10s 可见。两个时限不同，别用同一窗口判。
6. dev 的 admin 写端点有 IP 限流，连续跑验收脚本会撞 429；脚本按「退避重试 → 仍 429 记 pend」处理，不判 FAIL。

---

## 复现方式

```bash
# 后端验收（84 条断言）
DATABASE_URL='postgres://sforum:sforum@127.0.0.1:15432/sforum?sslmode=disable' \
SFORUM_ACCEPT_ALLOW_DEV_MUTATION=1 \
./scripts/verify-badges-acceptance.sh --phase=full

# 只读模式（不造数据，写类断言全 pend）
./scripts/verify-badges-acceptance.sh --read-only

# 浏览器结构断言（16 条）+ 截图链路
node storage/qa/audit-badges-m8.mjs
node storage/qa/badges-shots.mjs --target profile-badges --user 甲 --viewport pc
node storage/qa/badges-shots.mjs --target topic-chip --topic 1099 --viewport mobile
node storage/qa/badges-shots.mjs --target admin-badges --viewport pc
```

退出码：`0` 无 FAIL（允许 pend）／`1` 有 FAIL／`2` 预检失败（含 API 在重建窗口不稳定）／`3` 开发库守卫拒绝写操作（只跑只读段）／`4` `--strict` 下存在 pend。

账号矩阵取自 `storage/qa/creds.txt`（甲 user 3 / 乙 user 4 / 丙 user 457 零基线 / 丁 user 469 只读 / 戊 user 1 super_admin，统一密码）。

---

## 未覆盖与风险

- **S10-5 采样**：该轮所选主题下没有「有徽章作者」的评论，已 pend；评论装饰的等价证据由 S10-4（无空对象徽章）与 S10-6b（零基线键缺省）覆盖。
- **无障碍只到结构层**：aria-label 文案、`role`、可点区域均已断言；未做真实屏幕阅读器（NVDA/VoiceOver）实测。
- **浏览器 QA 跑在 dev 数据上**：12 个内置徽章、甲持有 2 枚。分页、超长徽章名、tier 混排等极端数据未构造。
- **未验运营场景**：批量授予、超大量徽章（>100）时的列表性能、`Recompute` 在大用户量下的耗时。
- **迁移可逆性**由 `scripts/verify-migration-reversible.sh` 独立覆盖（一次性集群 up→down→up），不在本报告断言内。
