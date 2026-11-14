# 2026-11-14 资料页头部移除个人简介（bio）行

## Changed

前端一处产品面，全部在 `apps/web`，**后端、契约与 i18n 键零改动**。

- `app/components/profile/SFProfileShowPage.vue`：头部 `sf-profile-intro` 里两段 bio `<p>`（正文段与
  `--empty` 空态段）删除。头部身份块由「昵称行 → 身份行 → 简介 → meta」收敛为
  「昵称行（h1 + 佩戴徽章 chip）→ 身份行（@handle / UID n / TL 等级徽章）→ meta（位置 / 网站 / 加入时间）」。
  「关于」tab 内的 bio 与空态（`sf-profile-about`）保持原样；SEO description 仍读
  `profile.profile.bio`；右栏 `SFProfileRightRail.vue` 未动。
- `app/assets/css/sforum-profile.css`：删除 `.sf-profile-intro__bio` 与 `.sf-profile-intro__bio--empty`
  两条规则。`.sf-profile-intro__meta` 的 `margin-top: 10px` 未调——身份行自身已有 4px 行距，
  10px 足够分隔，不再新增类名或层级。
- 测试：新增 `tests/profile/profileHeaderBio.test.ts`（7 个用例，静态源契约断言，沿用
  `tests/profile/` 既有风格）：头部 intro 段与整页都不含 `sf-profile-intro__bio`；CSS 不含同名规则；
  「关于」tab 段仍含 `profile.profile.bio` 与 `t('profile.bioEmpty')`；SEO description 仍用 bio；
  `zh-CN.json` / `en-US.json` 的 `profile.bioEmpty` 键仍存在且非空。
- 模块笔记：`modules/profile.md`「header identity block」由三层改为两层，写明 bio 只在「关于」tab 呈现。

## Decisions

- **bio 在资料页只出现一次**：头部与「关于」tab 同时渲染时，同一句话在首屏与详情里重复占位，
  且空态文案「这个人很懒，还没有写简介。」在头部尤其像噪声。保留在「关于」tab 是因为那里才是
  简介的上下文（与位置、网站、签名、自定义字段同列）。这是取舍，不是缺陷。
- **`profile.bioEmpty` 键不许删**：关于 tab 的空态与资料设置页的预览（`SFProfileSettingsPreview.vue`）
  共用它，删键会让「没写简介」在两个地方同时失去文案。头部不再用它是渲染层的事，不是 i18n 的事。
- **不动 SEO description**：SEO 仍用 bio 做 description（bio 缺失时回落 `profile.metaDescription`），
  简介仍是有价值的搜索摘要来源，与「头部不重复展示」互不冲突。
- **CSS 只删不加**：删掉两条 bio 规则后头部节奏仍成立，因此没有为「补视觉」新增类名或容器层。

## Next

- 前端门禁：`bun test` 全量 **3056 pass / 0 fail**（293 个文件，含新增 7 个用例）；
  `bun run typecheck` exit 0；`node tests/validate-architecture-boundaries.mjs` 在「仅本次改动 + HEAD」
  的干净副本上 exit 0（当前工作树里该门禁报 `SFTopicShowPage.vue` 1127→1129 行超 cap，
  那是并行任务改的注释，与本次改动无关）。
- 浏览器证据（`http://127.0.0.1:3000/u/zhuchunshu`，headless Chrome + CDP）：
  桌面 1440×900 与移动 390×844 头部 `.sf-profile-intro__bio` 数量均为 **0**，头部可见文本为
  「大佬 @zhuchunshu UID 1 TL1 加入于 2026-10-04」（无简介行）；点「关于」tab 后该用户 bio
  仍渲染（`nbnb`）。截图与 JSON 报告见 `work-team/evidence/`。
- 未提交。

## Open Questions

- 头部 meta 行（位置 / 网站 / 加入时间）目前与「关于」tab 的详情列表重复同一批字段。
  既然 bio 已经收进「关于」，是否顺势把 meta 行也精简（只留 UID / 等级）属于产品取舍，
  本次没有动。
- 若产品希望头部在**有** bio 时仍给一句预览（例如单行省略 + 「更多」），那是新增能力而非本次取舍，
  需要新的类名与截断规则，未擅自实现。
