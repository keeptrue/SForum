# 2026-11-13 徽章系统 UI 收口（五处）+ 外观单一真源

## Changed

用户报的五处，逐条落点：

1. **chip 颜色与后台配置不一致（真 bug，已修根因）**：论坛读 SQL 的徽章投影从 4 列扩到 7 列
   （加 `badge_tiers` 的名称/颜色），删除从未装配的第二条装饰路径（`badges.Reader` /
   `forum.BadgeReader` / `PublicReadService.WithBadges`），前端缺外观时改中性 `slate`。
   决策见 `decisions/2026-11-13-badge-appearance-single-source.md`。
2. **资料页「你现在可以换成 X」提示条**：整块删除（模板、样式、`upgradeName`/`wearUpgrade`/
   `badgeNameOf`、i18n `badges.worn.upgradeHint`）。`useBadgeWorn.upgradeSlug` 解析保留。
3. **左下角「获得新徽章」浮卡 → 居中 modal**：`SFCelebrationHost.vue` 内部换 `UModal`，
   聚合展示（`celebrations.dialog.aggregate`），Esc/遮罩/关闭按钮统一 `dismiss()`。
4. **通知详情「目标不可用」误报**：正文与右栏统一到 `hasTarget`（`!unavailable && path`），
   新增「目标卡 + 查看我的徽章」分支；真正不可用才显示原提示。
5. **徽章卡可点 + 详情 modal + `?badge=` 深链**：新 `SFBadgeDetailModal.vue`、
   `useBadgeDetailQuery.ts`；`SFBadgeCard` 覆盖整卡的真 `<button>`；`profileBadgeItem`
   增 `description`；徽章通知目标改为 `/u/{username}?tab=badges&badge={slug}`。
6. **（追加）庆祝弹窗的徽章信息与详情弹窗同源**：用户要求「庆祝弹窗关于徽章信息的显示要和
   详情弹窗类似」。抽出纯展示组件 `SFBadgeDetailBody.vue`（等级色图标/名称/等级徽标/分组/
   说明/条件/进度或获得时间），两个弹窗共用；庆祝条目新增 `params.badgeSlug`（后端白名单
   只加这一个键），前端按 slug 去 `/profiles/{me}/badges` 取**当前**资料，查不到回落旧纯文本行。
   关键坑：该端点返回「全部启用徽章」而非「我已获得」，索引必须只收 `earned` 条目，
   否则同一会话第二枚徽章不补拉、按未获得渲染（冒进度条、丢获得时间）——已修并有生产形状用例。

审查后补的修复（小审发现，均已带变异验证过的回归测试）：

- **庆祝游标 ack 恒为 no-op**（高）：`dismiss()` 原先 ack 服务端「读取前游标」，
  而条目 id 恒大于它 → 关闭后下一次 refresh 又弹。改为 ack `max(cursorId, items[].id)`。
- **`?badge=` 深链在数据到达前被清**（中）：`useProfileBadges` 导出 `loaded`，
  三处守卫（页面 / tab / 目录页）统一成 `loading || failed || !loaded`。
- **佩戴按钮 label-in-name**（中）：可访问名回到可见文字，状态用 `aria-pressed`，动作放 `title`。
- **`DisplayBadge` 候选含停用档徽章**（中）：判别列改为 `CASE WHEN t.key IS NULL THEN NULL ...`，
  rank 同步；两个方向的集成用例（有 fallback / 只剩一枚则全 nil）。
- 深链跟随前进后退、`hasTarget` 提成纯函数 `notificationHasTarget`、契约措辞（ack 语义、
  threshold 可能被短暂超过）、多处失真注释。
- 复审（第二轮）又补两条：**chip 的徽章 JOIN 现在也要求等级启用**（`EXISTS (… badge_tiers … enabled)`，
  用 EXISTS 不用 INNER JOIN 以免丢掉「无徽章」的行），于是「停档 = 处处不可见」真正成立；
  庆祝读面去掉无用的 `limit+1` 并补 `TestListCelebrationsCapsAtLimit` 钉住「返回 id 升序前缀、条数 ≤ limit」。

门禁既有红灯（来自工作区未提交的佩戴/庆祝改动，非本轮引入）：路由守卫期望清单补两条
celebrations 路由；`202611130003_badge_wear.sql` 补 `SET LOCAL lock_timeout/statement_timeout`。

## Decisions

- 徽章外观只有一个来源：论坛读 SQL。缓存前缀随之 bump（topic v4 / topics v2 / comments v3）。
- 颜色缺数据 = 中性，不猜等级色；图标的三档兜底保留（形状不是颜色）。
- 升级提示不做（不搬进 modal）；佩戴能力由卡片按钮与详情 modal 承载。
- 庆祝层保持「关闭即消费」，但消费值必须是已看到条目的最大 id。

## Next

- **YG 需重启 `apps/web` dev server**（`bun run dev`）：新 i18n 键在 SSR 正确、客户端 bundle
  仍是旧 locale，水合后会出现 `notifications.actions.viewBadges` 这类原始 key。重启后复验文案。
- 待人工复验：庆祝 modal 实机弹窗（DB 里 notification 592 仍未 ack，可直接复现）、
  modal 内佩戴切换（写库，只对测试账号做并恢复）。
- 可选收口：三份「未知 slug 清理」守卫收敛进 `useBadgeDetailQuery`（本轮按最小改动各修一处）。
- 可选收口：`TopBadgesFor` 已无生产调用方，若确认不再需要应连接口一起删，避免再长出第二个投影入口。

## Open Questions

- `useBadgeWorn` 的 `useState` 在「登出后不整页刷新就换号」时会保留上一个账号的 `wornSlug`
  且 `loaded=true` 不再重取（登录流程是否整页刷新未确认）。与本次改动无关，但佩戴态会误导。
- 庆祝层的契约把「ack 值」写成游标而不是「已消费最大 id」曾是缺口来源，契约已补，但
  `CelebrationFeed.cursorId` 的字段名仍容易被读成「已消费游标」。
- **已知残留（本轮不做）**：`dismiss()` 与「已经在途的 refresh」之间有窄竞态——点击瞬间若有一个
  revision 触发的 refresh 尚未返回，它会把同一批条目填回（ack 仍会落库，只是卡片闪回一次）。
  要彻底消掉需要 refresh/dismiss 共享世代号。
- **已知残留**：`./scripts/test.sh --fast` 的 api 线偶发 `TestLifecycleCleanupPurgeRetainsIdentityWhenPublished`
  （bootstrap，`duplicate key users_pkey`）。单跑与整包隔离都 PASS，判为测试隔离抖动，非本轮回归。
