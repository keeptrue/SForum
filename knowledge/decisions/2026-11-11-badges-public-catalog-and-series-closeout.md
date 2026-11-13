# 2026-11-11 `/badges` 公共目录与互动系列收口：D35 / D36 / D37 / D38

Status: **accepted**（YG 与 Lead 在 2026-11-11 会话裁决；**未提交，本会话工作树**，采集时点
2026-11-11T14:1x+08:00，`HEAD = 9e3e1cbe8`，复算 `git rev-parse HEAD`）。
计划：`../plans/2026-11-11-badges-public-catalog.md`（§3 口径、§4 里程碑、§3.5 D37、§3.6 D38）。
验收：`../reports/2026-11-11-badges-public-catalog-matrix.md`（B-01..B-30）；
独立判定：`storage/qa/badges-public-catalog/VERDICT.md`（reviewer，PASS-with-residual）。
前序决策：`2026-11-10-badges-and-user-activity.md`（D1–D11）、
`2026-11-11-trust-level-baseline-narrowing.md`（D34）。
本文件是**追加**决策，不改写既有条目（D1–D34 冻结约定：新决策追加）。

> 四条裁决里 **D35 / D36 只登记不实现**（本刀范围只有 `/badges` 公共目录页）。
> 登记的目的单一：避免下一个人把同一件事重推一遍。

## D35：不给默认分档限额；「推荐分档」改后台一键应用

**裁决**：**不给**信任等级默认分档限额，**D27 保持**——TL1–TL4 默认仍继承站点默认
（冷却 / 日限额都是 0）。「推荐分档」做成后台**一键应用**：预填推荐值，运营**显式点击**才落库。

**理由**：

| # | 理由 | 量级对比 |
| --- | --- | --- |
| 1 | **破坏面**：默认收紧 TL1 会让**存量站点全体会员**突然有冷却 | D34 只影响 TL0（新用户 + 未达 TL1 的存量用户）；D35 若按「默认收紧」会影响**所有已过新人窗口**的会员，破坏面大一个数量级 |
| 2 | **默认值即承诺**：分档限额是运营策略，不是产品常识 | 不同站点对「TL2 每天能发几帖」没有共识；写死默认等于替运营做决定 |
| 3 | **一键应用仍兑现价值**：把推荐值算好、摆在后台 | 运营不必自己推参数，但落库动作是显式的、可审计的 |

**被否方案**：

- **默认收紧 TL1–TL4**（每个等级一组递减限额）：破坏面见上表第 1 条；且与 D34 的
  「有界静音」叠加后，升级站点的行为变化无法在 release note 里讲清。
- **只收紧 TL1**（最保守的「至少让 TL1 有牙」）：仍然是存量会员突然多出冷却；
  且会造成「TL1 有牙、TL2 反而更松」的倒挂错觉（因为 TL2+ 继承站点默认）。
- **不做任何推荐值**：等于把参数推导工作推回给每个运营，与「新手友好默认」的仓库口径冲突。

**本刀不实现 D35 的 UI。** 矩阵不覆盖 D35。

## D36：下一产品纵列 = 关注 / 订阅（follow + watch）

**裁决**：互动系列（点赞 → 收藏 → 徽章+统计 → 信任等级 → `/badges` 目录）收口后，
下一个产品纵列是**关注 / 订阅**：`follow`（关注用户 / 分类 / 标签）+ `watch`（订阅主题 / 评论），
**复用 `content_bookmarks` 的提醒管线**（`forum.bookmark_reminders` 周期任务 + `bookmark_reminder`
通知的形状：抢占式扫描、目标不可见只写 `reminded_at` 不发通知、邮件默认开）。

**理由**：

1. **管线已存在且被验证**：收藏那一刀已经解决「周期扫描 + 幂等 + 可见性失败静默」三件事，
   关注/订阅不需要重造。
2. **它是论坛产品面的自然缺口**：用户今天只能靠「回到版块翻」发现新内容；
   收藏解决的是「我记得这条」，关注解决的是「有新东西告诉我」。
3. **数据面可复用**：`content_bookmarks` 是多态关系表，`follow` 只是另一种 `target_type`；
   这比新开一张表更符合「四个受批面」的扩展数据策略。

**被否方案**：

- **先做通知中心 / 邮件摘要**：没有订阅源就没有内容可聚合，顺序反了。
- **先做搜索增强**：与「新内容主动到达」不是同一问题，且搜索已有既有实现。
- **新开 `user_follows` 表**：与 `content_bookmarks` 的提醒管线形成第二套「谁提醒谁」，
  是本仓反复抓的「同一判断两份实现」。

**本刀不做 D36**；只登记。计划与矩阵均不覆盖。

## D37：`/badges` 响应的缓存头必须 `private`，禁止 `public`

> **2026-11-13 修订索引**：见 `2026-11-13-badge-progress-from-rule.md` 的「显式修订 D37」小节（本条原文不改）。

**裁决**：`GET /api/v1/badges` 的 `Cache-Control` 必须是 `private`（实现取 `private, no-store`），
**不得**出现 `public`。既有 13 条徽章端点的头**不动**。

**理由**：

| 端点 | 返回谁的数据 | 正确缓存级别 |
| --- | --- | --- |
| `GET /api/v1/profiles/:username/badges`（既有） | **被查看者**的 earned（actor 无关，同一 URL 对所有人相同） | `public, max-age=30`（`Badges/controller.go:713` `setPublicCacheable`） |
| `GET /api/v1/badges`（本刀新增） | **查看者自己**的 earned（actor 相关） | **`private, no-store`**（`:723` `setViewerScopedNoStore`） |

写 `public` 的后果具体到一句：**共享缓存会把 A 的 earned 投给 B**——这不是「缓存不准」，
是把一个用户的成就状态泄露给另一个用户。

**不靠 `Vary` 补救**：该控制器的 `RegisterRoutes` **不设 `Vary`**（零命中）；运行时响应里的
`Vary: Cookie, Accept-Encoding` 来自中间件，不由控制器保证覆盖 earned 维度。

**被否方案**：

- **沿用 `setPublicCacheable`**（最省事）：见上，越权泄露。
- **加 `Vary: Cookie` 后继续 `public`**：把正确性押在缓存键实现上；`no-store` 是零假设方案。
- **把既有 13 条也改成 `private`**：拿掉一项既有正确行为（那些端点确实 actor 无关），
  不是修 bug。

## D38：`RequiresFeatures` 关闭时页面 404 / API 403 不统一，本刀不动全局映射

**裁决**：`features.badges=false` 时，页面 `/badges` 返回 **404**、API `GET /api/v1/badges`
返回 **403 `forum.badges_disabled`**。两者语义不同，**都成立**；本刀**不动**
`Pages/controller.go:247` 的全局 404 映射。

**理由**：

| 面 | 结果 | 机制 | 为什么不能统一 |
| --- | --- | --- | --- |
| 页面 | 404 | `catalog` 条目 `RequiresFeatures` → `PageViewModels/source.go:697` 的 `requirePageFeatures`（调用 `:114`、分支 `:702-708`）返回 `ErrCorePageDataNotFound` → `Pages/controller.go:247` 映射 404 `pages.data_not_found` | 该映射是**全局**的：改它同时影响 `forum.search`（`features.search`）与 `forum.profile.show`（`features.public_profiles`） |
| API | 403 `forum.badges_disabled` | 徽章域既有口径（`Badges/controller.go:108` `requireFeature`），与其余 13 条端点一致 | 改成 404 会与同域 13 条端点分叉，且「产品没开」与「资源不存在」是两件事 |

**被否方案**：

- **统一成 404**：要动全局映射，影响另外两个 feature-gated 页面；且丢掉「产品未启用」这个可操作信号。
- **统一成 403**：页面 403 会让访客看到「禁止访问」，而实际是运营没开这个功能——
  错误页语义错位；且要改 `PageViewModels` 的 sentinel 语义。
- **给 `/badges` 单独开一个 403 页面**：为一次性差异造第二个错误页渲染路径。

## 与既有决策的关系

| 既有决策 | 本刀后 | 说明 |
| --- | --- | --- |
| **D1–D11**（徽章 + 统计） | **不变** | 本刀只加公开读面；统计层、授予三来源、`top_badge_id` 失真窗口原样 |
| **D17 / D27**（信任等级默认） | **不变** | D35 明确「不给默认分档限额」，D27 保持 |
| **D34**（TL0 减法门） | **不变** | 与本刀无交集；`/badges` 目录对所有访客可见（含未登录） |
| `modules/badges.md` 的 Cache Semantics | **被本刀修正** | 旧文案「徽章可以进 `CachedStore`」实指 `BadgeRef` 随 Forum 载荷进缓存，**Badges store 从未被包装**（第六条幻影） |

## 后果与残留

1. **`/badges` 的视觉 QA（B-26）未取证**：ops 在采、reviewer 待独立复核。
   在它到货前不要对运营宣布「视觉已验」。
2. **`admin.nav.badges` 在两份 locale 里都不存在**（HEAD 已有，非本刀回归）：
   后台侧栏「用户与权限」下的徽章项显示裸键名。frontend 正在修；
   根因是现有 admin i18n 测试只查**文件夹级** `labelKey`，页级不查（2079 条全绿也照不出来）。
3. **D35 / D36 只有决策，没有代码**：D35 的「一键应用」UI 与 D36 的整条纵列都未开工。
4. **本刀未提交**：所有「已提交 hash」的说法以 `git log` 为准；本文件落盘时为工作树状态。
