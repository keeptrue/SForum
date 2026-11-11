# 徽章验收：人工检查清单

配套脚本：`scripts/verify-badges-acceptance.sh`（自动化部分）。
逐条操作步骤、账号矩阵、证据落点见 pm 的 `knowledge/reports/2026-11-10-badges-acceptance-criteria.md`，
本文件只写**脚本判不了、必须人眼/人脑判**的项，避免两边重复劳动。

判定只有三种：**通过** / **失败** / **待定（前置不满足）**。待定必须写清缺什么，不许含糊过关。

## 0. 脚本已覆盖，人工不要重做

| 脚本段 | 覆盖内容 |
| --- | --- |
| S0 | 表 / 视图 / 内置 12 徽章 / 通知描述符 / CHECK 与唯一约束 / 视图禁止列 |
| S1 | 发主题、发评论、点赞（作者+操作者）、收藏 → 计数增量对照 |
| S2 | 10 次评估后 `user_badges` 恰好 1 行、`top_badge_id` 指向 |
| S3 | 同日 5 次请求 → 1 行 `user_visits`、`days_visited` 与行数一致 |
| S4 | 取消点赞回退到基线、不为负 |
| S5 | 并发 5 次点赞只 +1（`user_activity` 与 `topics.like_count` 双查） |
| S6 | 停用不展示、删除后 `top_badge_id` 置空、自动徽章手动收回 422 |
| S7 | 无 `badge.manage` 直接 curl 四个后台端点 → 403；匿名 → 401/403 |
| S6-5 | `features.badges` 关闭 → 端点 403 `badges_disabled` + 资料页统计照常（API 半句） |
| S7b | 只持 `badge.view` 的账号（丁）：能读、写被 403 且 reason 不是 `badges_disabled` |
| S7c | 公开徽章响应不含 `grantedCount` / `enabled` / `grantedBy` / `grantReason` |
| S8 | 两个缓存时限分开判：资料页 ≤10s 出现、帖子旁 ≤30s 属正常 |
| S9 | `SFTopicShowPage.vue` / `postgres_store_ops.go` 棘轮基线 |
| S10 | 公开读路径：列表/搜索不 500、topBadge 装饰、零基线用户键缺省 |

> **S10-1a/1b/1c/1d 不是徽章检查，是「扫描器列数」探针。**
> `scanTopicSummaryWithAvatar` 被三处 SELECT 复用，任何一处少一列就是运行时 500
> （`number of field descriptions must equal number of destinations`）。2026-11-10 的首页全站 500 就是这么来的。
> 任何给读路径加字段的改动都要重跑这四条，别因为「看着和徽章无关」删掉。
> 搜索探针必须用 `query=` 参数并确认命中 > 0——命中 0 条会 early return，那个 200 是假通过（`q=` 会被忽略）。
>
> **验证工具本身也会撒谎**：选择器/断言必须对着真实 DOM 校准过；未校准的守卫比没有守卫更坏（稳定假 FAIL 会伪装成结论）。断言过严与过松同样危险。

## 1. 浏览器几何（1633×810 与 390×844 各跑一遍）

三处页面，每处四条断言，缺一条即失败：

1. 资料页第 5 个 tab「徽章」：切换后分组分区与 tier 排序正确，切走再切回内容不丢（计划 §0-5、§M5）。
2. 帖子旁 chip：**只显示 1 个**最高级徽章；同一作者在评论旁与主题旁显示的是同一个。
3. 后台徽章页：列表 / 新建 / 编辑规则 / 手动授予 / 重算五个动作都有入口。
4. 几何：`document.documentElement.scrollWidth <= clientWidth`（无横向溢出）、无元素被裁切（`getBoundingClientRect()` 完全在视口内）、标题与工具栏左对齐、窄屏下工具栏按钮折叠为图标但仍保留 `aria-label` 与 `title`。

取证：`storage/qa/badges/<编号>-1633.png` / `-390.png`（沿用 pm 1.5 的落点约定）。

## 2. 可访问性

1. chip 的 `aria-label` 是完整语义（例：「金牌徽章：众望所归」），不是只有图标名。
2. 仅图标按钮都有 `aria-label` + `title`；tier 色不是唯一信息载体（色盲可辨）。
3. 键盘可达：tab 可用左右方向键切换，chip 可用 Tab 聚焦且焦点环可见。
4. 徽章进度条有可读文本（`current/threshold`），不依赖纯视觉长度。

## 3. 缓存语义（最容易误判成 bug，先看这条）

徽章进 `CachedStore`：`topicDetail` 30s、`comments` 20s。

- 判定方式：授予后**立刻查 SQL**（`users.top_badge_id` 已变）→ 页面 30s 内不变 = **正常**，不是失败。
- **两个时限不同，别用同一个窗口判**：资料页不走 Forum 缓存，脚本按 ≤10s 轮询（`SFORUM_ACCEPT_PROFILE_WAIT`）；帖子旁走 `CachedStore`，脚本按 ≤35s 复核（`SFORUM_ACCEPT_TOPIC_WAIT` / `SFORUM_ACCEPT_CACHE_WAIT`）。
- 只有**超过各自窗口仍不变**才算失败。
- 后台点「重算」后看不到即时变化同理；后台文案若没说明这一点，记一条文案缺陷。

## 4. `features.badges` 关闭（API 半句脚本已验，UI 半句必须人工）

脚本 S6-5 已经验过：端点 403 + `forum.badges_disabled`、资料页统计照常、`user_activity` 仍在库、开关能恢复。
人工只补 UI 半句：

1. 关闭后资料页徽章 tab 消失、帖子旁 chip 消失（脚本判不了渲染）。
2. **统计数字照常显示**（主题/回复/获赞/给赞/收藏/访问天数）——看页面，不看接口。
3. 重新打开开关：tab 与 chip 回来，已授予关系未丢（`user_badges` 没被清）。
4. 循环翻转一次，证据存 `storage/qa/badges/flag-evidence.txt`。

## 5. 权限（脚本 S7/S7b 已覆盖 curl 拒绝侧，人工只补角色矩阵）

1. 脚本会自动给丙挂一个只带 `badge.view` 的 `qa_badge_viewer` 角色当「丁」，验完不摘——这是 QA 夹具，别当脏数据。
2. 人工补：`moderator` 只有 view、`operator` 与 `super_admin` 都能写；角色矩阵在后台用户页看得到。
3. 前端隐藏菜单永远不算证据；判定只看服务端状态码与 `data.reason`。

## 6. 棘轮（脚本 S9 自动判数值，人工判质量）

1. `SFTopicShowPage.vue` 抽取 `SFTopicAuthorBlock.vue` 后必须**净减**，且基线同轮下调；脚本会在「文件变短但基线没降」时直接 FAIL。
2. 人工看 diff：抽取是否**只搬不改**；若作者块与详情页状态耦合过紧，是否退到计划 §9.2 的退路（只评论旁挂 chip）并在文档里写明。
3. `postgres_store_ops.go` 同理：净零或净减，不许为了挂 hook 撑破 cap。

## 7. 通知与邮件

1. 授予徽章后站内通知出现，图标为 `i-lucide-award`。
2. 邮件通道默认关；打开后邮件正文有中英模板（`forum.badge_granted`），不是空模板。
3. 管理员给自己授予**要发**通知（与自赞不同）。

## 8. 边界的人工部分

1. 用户被 ban/disabled：徽章行保留（软状态），稳定视图与读模型都按 `users.status='active'` 过滤 → 资料页看不到，后台授予记录仍在。
2. 徽章停用后：已授予行保留、UI 立即不再展示（脚本查视图，人工看 UI）。
3. 规则阈值改低：**不自动追溯**；点「重算」后才补授；文案要提醒这一点。

## 9. 提交给 lead 的证据格式

每条：`编号 · 结论（通过/失败/待定）· 证据路径 · 一句说明`。
失败项必须带复现路径（点了什么、看到什么、期望什么），不接受「感觉不对」。
