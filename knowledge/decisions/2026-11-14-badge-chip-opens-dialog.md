# 决策：徽章 chip 的点击行为改为「打开徽章说明」，不再跳资料页徽章 tab

日期：2026-11-14
状态：accepted
上游：`decisions/2026-11-13-profile-top-badge-source.md`（资料页展示位与 `topBadge` 的来源）

## 背景

chip（昵称旁那枚 18px 小徽章）原本是链接，指向 `/u/{username}?tab=badges`。用户实测的观感是：
点一枚徽章却整页跳走，看到的是一墙徽章，而不是「这枚是什么」。帖子作者、评论作者、资料页头部
三处入口都有这个问题；而资料页徽章墙里点卡片本来就有徽章说明弹窗（图标/名称/等级/分组/说明/
获得条件/获得时间），两者体验割裂。

## 裁决

### D1 chip 是 button，点击开全局弹窗

`SFBadgeChip` 有 `username` → `<button type="button" aria-haspopup="dialog">`，点击调
`useForumBadgeDialog().openBadgeDialog({ username, badge })`；没有 `username`（用户预览卡这种
「名字本身已经是链接」的场景）仍是 `<span role="img">` 纯展示。

保留 `data-badge-slug` / `data-tier` / `:style` 与 18px 行盒：chip 与昵称同一行、有无 chip 行高差为 0
是硬约束，button 分支显式写 `font-family: inherit; appearance: none; cursor: pointer` 以免原生按钮
撑高或换字体。

### D2 弹窗复用徽章墙的那一套组件，不新建第二个弹窗

`SFBadgeDetailDialogHost` 挂在 `app.vue`（与 `SFCelebrationHost` 同层、不随路由卸载），渲染
`SFBadgeDetailModal`，信息块是 `SFBadgeDetailBody`——与徽章墙卡片弹窗、站内庆祝浮卡同一份。
「同一枚徽章在不同入口长得不一样」这类漂移因此不可能发生。

### D3 必须拉徽章墙，且失败不伪造

chip 的载荷只有 `ForumBadgeRef`（slug/name/tier/icon/等级名色），**没有**说明、获得条件、获得时间。
这些只在 `GET /profiles/{username}/badges` 的条目里。所以：

- 按 username 分桶缓存（`useState`），同一用户第二次点不再请求；
- `pending` 期间不重复请求；失败不清已加载的墙，且 `loaded[username]` 保持 false → 重开会重试；
- 墙里没有这个 slug（徽章停用/删除）或请求失败，都走「徽章说明暂时不可用」文案。
  **不拿 chip 的字段拼一颗缺说明、缺条件的假徽章**——那比空态更像真的。

加载中给骨架 + 可见的关闭出口（`UModal` 的 `#content` 会顶掉内置 header，所以失败态与加载态各自带
关闭按钮）：点了 chip 必须有反应，且不能被挂住的请求困住。

### D4 佩戴按钮只在「本人 + 已获得」出现

`self` 判 `state.username === 当前登录用户 username`，`worn` 走 `loadedFor(viewerId)`（账号归属，见
`2026-11-13-profile-top-badge-source.md` D5 的补充）。他人页与未获得的徽章只读。
动作与资料页徽章墙共用新抽出的 `useBadgeWearToggle`（语义：已佩戴 → `hide()` = 不佩戴任何徽章；
未佩戴 → `wear()`；失败常驻错误 toast），toast 文案零变化。

### D5 chip 移出交互元素之外

chip 变成 button 之后，任何调用点都不能让它在 `<button>` 或 `<a>` 里。评论作者行原本把 chip 放在
用户预览按钮内部的 `#name-suffix` 槽，本刀把它提到按钮**外面**当兄弟节点（「楼主」标留在按钮内）；
预览卡不传 username。顺带把该行 interactive 分支里 `SFUserIdentityName` 的 `:to` 去掉（`linked=false`），
这是该文件最后一处 `button > a`。

## 被否方案

| 方案 | 为什么否 |
| --- | --- |
| chip 仍跳 `?tab=badges`，只在徽章墙里给说明 | 用户点的是一枚具体徽章，落地却是一墙列表；且三个入口里有两个（主题/评论作者）跳到的是**别人**的徽章墙，信息不相关 |
| 弹窗直接用 chip 载荷画（名称/等级/图标） | 没有说明、没有获得条件、没有获得时间——图1 那三行恰恰是用户要看的 |
| 复用 `useProfileBadges` | 它绑「资料页当前是否停在徽章 tab」且结果只活在页面组件树；弹窗是全局的，打开时资料页可能不存在 |
| 新建一个徽章详情弹窗 | 与徽章墙弹窗会长出两套信息项与两套加载/失败态，正是本仓库反复踩过的分叉 |
| 打开时顺手把 chip 做成链接 + 弹窗二选一（修饰键开新页） | 移动端没有修饰键；两条路径的判定逻辑会逐渐漂移 |

## 后果

- 一次点击最多多一次 `GET /profiles/{u}/badges`（已按 username 缓存；该端点本就为徽章墙存在）。
- 徽章发生变化（停用/新获得）时旧缓存仍会在本次会话内命中最长到刷新——与徽章墙 30s 缓存同源，
  可接受；切到另一个用户会各取一份。
- `utils/forum/forumBadge.ts` 的 `forumUserBadgesPath` 失去调用方（后续清理）。
