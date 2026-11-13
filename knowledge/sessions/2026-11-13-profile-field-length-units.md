# 2026-11-13 资料字段长度口径：字节 → Unicode 码点

Status: **landed（未提交，工作树；`HEAD = 53928c4a8`）**。只换长度单位，不改字段、不改权限：
`PUT /api/v1/profile` 仍然只允许本人写自己的资料。上限数值当时未动，`bio` 其后单独降到 160 码点，
见「后续调整」。

## Changed

- 后端 `apps/api/app/Models/Profile/service.go`：`bio` / `signature` / `location` / `websiteUrl`
  的上限校验从 `len()`（UTF-8 字节）改为 `utf8.RuneCountInString`；上限值当时未动，仍是 `types.go`
  的 500 / 200 / 100 / 200（旧值），现在读作码点。
- 前端 `SFProfileStoryTab.vue`：计数器从 `bioDraft.length`（UTF-16 码元）改为
  `Array.from(value).length`（码点）；两个输入框的 `maxlength` 取 2× 码点上限（当时 `1000` / `400`，
  `bio` 降为 160 后是 `320` / `400`）。
- 契约 `contracts/openapi/schemas/profile.yaml`：四个 `maxLength` 补 description，写明计码点、
  不计字节（当时举例 178 汉字合法、501 码点不合法；随 160 上限同步改成 160 汉字合法、161 不合法。
  违规是 `422 profile.invalid` + `data.fields.bio`）。
- 回归防线三处：`apps/api/app/Models/Profile/service_test.go` 的
  `TestServiceUpdateMyProfileCountsRunesNotBytes`（当时 178 汉字过、500 过、501 带字段级错误；
  随上限降为 160 后是 160 过、161 带字段级错误）、
  `apps/api/app/Http/Controllers/Profile/profile_field_errors_test.go` 的
  `TestUpdateProfileBioCountsRunesNotBytes`（同口径走 HTTP 422）、
  `apps/web/tests/settings/profileStoryTabCounter.test.ts`（挂真 SFC，断言计数器文本与 `maxlength`）。
- 补收口（同轮）：`SFProfileLinksTab.vue` 的 `location` / `website` 也从静态 `maxlength="100"` / `"200"`
  （码元）改成 `:maxlength="200"` / `"400"`（2× 码点上限），由 `profileStoryTabCounter.test.ts` 的
  「所在地 / 网站的 maxlength 也是 2× 码点上限」钉住；上一轮 Open Questions 里那条待办已关闭。

## Decisions

- **单位统一为 Unicode 码点**，与 OpenAPI `maxLength`、管理员路径
  `Identity/admin_user_update_validation.go` 的 `len([]rune(...))` 同尺。旧实现三处三把尺：后端按字节、
  前端按码元、契约写码点。中文 1 字 3 字节，当时的 500 字节上限实际只有 166 汉字，178 字（534 字节）
  被 422，而计数器正显示 178/500 —— 用户看到的数字和真正拒绝的理由不是同一件事。
- **前端 `maxlength` 取 2×**：HTML `maxlength` 数 UTF-16 码元，一个码点最多 2 码元，只有 2× 才不会
  在合法输入上提前截断。控件属性只是输入辅助，真上限仍由服务端与计数器负责。
- **emoji 用例是承重墙，不是冗余**：汉字全在 BMP，1 码点 = 1 码元，把 `Array.from` 改回 `.length`
  时纯汉字用例照样全绿；只有非 BMP（emoji 1 码点 = 2 码元）会让期望值整体翻倍变红。
  删掉 emoji 用例 = 拆掉唯一能抓到这个变异的东西。
- 模块记忆：`../modules/profile.md` 的 `PUT /api/v1/profile` 段补了口径说明。

## 后续调整（同日）：`bio` 500 → 160 码点

- 自助 `Profile/types.go` 的 `maxBioLength`、管理员 `Identity/service.go` 的 `maxAdminBioLength`、
  OpenAPI `UpdateProfileRequest.bio.maxLength` 同步为 **160 码点**；前端 `maxlength` 随之 320（2×160），
  `signature` 仍是 200 / 400。三条路径同一个数，才不会有「管理员能存、作者本人存不回去」。
- 三处回归防线同步到新数：160 汉字过 / 161 带字段级错误（HTTP 同）、计数器 `160 / 160`、
  emoji `160 / 160`（不是 UTF-16 的 320）。
- **不需要迁移**：dev 库实测 `user_profiles` 共 86 行，`char_length(bio) > 160` 为 **0 行**，最长 139 字
  （`SELECT count(*) FROM user_profiles WHERE char_length(bio) > 160`，容器 `sforum-postgres`）。
- 码点口径不因降上限而失效：160 个 emoji 是 640 字节（UTF-8 每个 4 字节；实测
  `len(('😀' * 160).encode('utf-8'))` = 640，同一段文本是 320 个 UTF-16 码元），按字节校验会误拒合法的
  160 码点简介；反过来 160 个汉字只有 480 字节，按字节校验又会放宽到 166 字。单位错了两头都不对，
  跟上限是 500 还是 160 无关。

## Next

- `knowledge/index.md` 的 Latest Handoff 指针**本轮未动**（badges 工作流正在改它），由 badges 线统一更新。
- 浏览器复验（未做）：`/settings/profile` 简介/签名计数器在 390×844 下不被长数字挤破；422 的字段级
  错误仍贴在对应输入框旁。
- 提交时与工作树同批改动一起走。`contracts/openapi/schemas/profile.yaml` 属本线；
  badges 那批（`wear_*`、`SFProfileBadgeTab`、`useBadgeWorn` 等）不属本线，别混进同一提交说明。

## Open Questions

1. 2× 的魔数（story `320` / `400`，links `200` / `400`）只在 web 测试里断言；将来调整上限需要同步改
   这几处，是否抽成常量由后续重构决定。
