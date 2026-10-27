# 2026-10-27 首页右栏问候语按时段生成

## Changed

- `SFHomeRightRail` 的固定「晚上好，{name}」改为按时段生成：
  `apps/web/app/utils/forum/forumHomeGreeting.ts` 负责分档（05 早 / 11 中 / 13 下 /
  18 晚 / 23 深夜，左闭右开），`apps/web/app/composables/forum/useForumHomeGreeting.ts`
  负责时钟口径与 i18n key。
- `zh-CN` / `en-US` 新增 `home.rightRail.greeting.{morning,noon,afternoon,evening,lateNight}`；
  `welcomeUserTitle` 降级为缺键兜底，中文文案改中性「你好，{name}」。
- 新增 `apps/web/tests/forum/forumHomeGreeting.test.ts`：11 个时段边界、纽约冬夏令时、
  午夜 `24→0` 归一、非法时区回落，以及两份 locale 的文案完整性。

## Decisions

- 问候语跟随**读者**时钟：SSR 与首帧用 `site.timezone`（服务端可确定），`onMounted`
  后改用 `Intl.DateTimeFormat().resolvedOptions().timeZone`。同区用户零抖动，跨区用户
  最多一次同位置文本切换。只按站点时区不可行：`site.timezone` 推荐默认是 `UTC`
  （`apps/api/app/Models/Options/site_datetime_options.go`），未配置的中文站会在北京时间
  早上 8 点显示「夜深了」。
- 首帧时间戳用 `useState('forum-home-greeting-now')` 固定，服务端值随 payload 下发，
  水合文本与 SSR 一致；60s 轮询让长挂页面跨时段边界后自行更新（与 `useForumContentTime`
  的「SSR 定值 + onMounted 校正」同一模式）。
- 不改主题包、不新增后台设置：问候语在 Core 岛内，`templates/home.html` 没有右栏标记，
  第三方主题也不能消费宿主 i18n key，因此无需重建/激活主题。

## Next

- 渲染 QA：桌面 1633×810 首页 + 移动 390×844 右抽屉各一次；DevTools Sensors 覆盖时区
  为 `America/New_York` 验证挂载后切换到访客本地时段；控制台确认无 hydration mismatch。
- 若运营要固定文案，再补 `home.right_rail.greeting_mode`（`time` / `fixed`）主题设置。

## Open Questions

- 英文 noon 与 afternoon 共用 `Good afternoon`（中文的中午/下午在英文没有对应词），
  是否需要为 noon 单独造词。
