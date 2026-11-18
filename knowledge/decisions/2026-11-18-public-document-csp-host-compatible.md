# Decision: 公开文档 CSP 基线按「宿主文档可满足」定，`style-src` 给 `'unsafe-inline'`

Date: 2026-11-18
Status: accepted

## Context

`publicPageDocumentPolicy()`（`apps/api/app/Models/Extensions/public_frontend_policy.go`）
把 L2 扩展片段与 Host 基线合并成整份 Nuxt SSR 文档的 `Content-Security-Policy`，
由 `applyPublicPageDocumentPolicy()` 在 SSR 响应头上写回浏览器。旧基线是
`script-src 'self'; style-src 'self'; img-src 'self'`。

这条基线拦的是宿主**自己**的注入，而不是扩展：

- Nuxt UI 把设计 token 写进 `<style id="nuxt-ui-colors">`；
- Nuxt Icon 在 SSR 期输出 `<style>`；
- Vue 模板里的 `:style` 属性（实测 75 处）；
- 宿主内联的 `data:`（占位/内联图标）与前端生成的 `blob:` 图片。

`style` 属性在规范上**永远**无法携带 nonce/hash，因此 `style-src 'self'`
对宿主自身就是不可满足的约束：真实浏览器里所有内联样式被拦，首帧样式与设计
token 全丢。

更糟的是旧的合并后断言：「合并后可执行指令必须恰好 `['self']`」。一旦基线放宽，
这条断言会让 `publicPageDocumentPolicy` 恒返回 `PublicPagePolicyDirectiveInvalid`，
策略端点 500，`applyPublicPageDocumentPolicy` 拿到 `null` 后**静默不写 CSP 头**——
表面上"更安全"，实际是整页失去策略下发，而且没有任何报错。

## Decision

### 1. 基线是「宿主文档可满足的来源白名单」，不是内联执行防线

```
base-uri 'none'; connect-src 'self'; default-src 'none'; font-src 'self';
form-action 'self'; frame-ancestors 'none'; img-src 'self' blob: data: https:;
media-src 'self'; object-src 'none'; script-src 'self';
style-src 'self' 'unsafe-inline'; worker-src 'self'
```

`img-src` 增加 `data:` / `blob:` / `https:`；`style-src` 增加 `'unsafe-inline'`；
`script-src` 与 `worker-src` 仍然恰好 `'self'`。

### 2. 脚本执行边界不随之放开

CSP 在这个位置承担两件事：把可执行来源钉在同源制品上，以及把文档限制成
「只有同源脚本能跑」。`style-src` 的放宽只影响样式注入面，不改变脚本信任根：
注入一个 `style` 属性拿不到 JS 执行权，而 `script-src 'self'` 依旧意味着任何
`<script>` 都必须是同源制品（L2 模块经 exact-artifact digest 校验后才发布）。
因此 `script-src` / `worker-src` 拒绝 `'unsafe-inline'`、nonce 与远程源。

### 3. `style-src` 里禁止混用 nonce/hash

CSP3 规定：同一指令只要出现 nonce 或 hash，该指令中的 `'unsafe-inline'`
即被忽略。所以给 `style-src` 加 nonce/hash 不是"加固"，而是把 `'unsafe-inline'`
静默作废、让 SSR 内联样式重新被拦。约束写死在
`publicPageHostBaselinePolicy()` 的注释里，并有测试
（`TestPublicPageDocumentPolicyHostBaselineIsSSRSatisfiable`）断言 header 里
不含 `'nonce-` / `'sha`。

### 4. 合并后的不变量收窄为只约束扩展片段

「可执行指令必须恰好 `['self']`」改成：**扩展片段**可以为可执行指令复述基线
来源，但不能超出基线。`strictPublicPagePolicySource` 已逐条把关（可执行指令只
接受 `'self'` / `'none'`），合并后仍照旧走边界、排序与 `'none'` 互斥校验。
宿主自己的 `'unsafe-inline'` 是宿主注入的权利，不是扩展可声明的来源。

### 5. `X-SForum-Document-Policy-Digest` 标识 Host 策略快照，不是最终 header 校验和

`PublicFrontendDocumentPolicy.digest` 是对「schema 版本 + graph digest + 扩展策略
digest + 指令集 + `headerValue`」的 sha256，描述的是 **API 下发的那份策略**。

但真正到达浏览器的 header 已经不再等于 `headerValue`：Nuxt SSR 会把页面数据与
引导代码写成内联 `<script>`，前端在不放宽 `script-src 'self'` 的前提下，只能按
请求给 `script-src` 追加一个 `'nonce-…'`（`buildDocumentCspHeaderValue`），并给
渲染出的内联脚本打同一个值。nonce 是 per-request 的，因此：

- 这条头的语义边界是「这页用的是哪一版 Host 策略快照」——用于关联审计、缓存键与
  前后端对账，**不能**用来校验浏览器实际收到的 header；
- 想校验「真正下发的策略」，唯一权威是响应头本身（`Content-Security-Policy`），
  以及浏览器门禁里对它的实测断言（见「已知缺口」）。

### 6. 文档 header 组装不得静默丢弃 Host 下发的指令

`buildDocumentCspHeaderValue` 历史上对「零来源」指令是整条省略：`default-src` 这类
兜底指令从 header 里消失后，浏览器回落到更宽松的默认，是 fail-open。现按本节契约
改为显式输出 `name 'none'`——Chrome 实测裸指令名与 `'none'` 等效，两者都能压过更
宽松的 `default-src`，所以显式 `'none'` 只是可读/可审计口径，不是行为差异；要防的
是「少一条」，不是「空着」。

约束：**文档 header 组装不得静默丢弃 Host 下发的任何指令**。零来源只有两种合法
结局——要么保留该指令并显式写 `'none'`，要么在组装阶段报错并 fail closed（不写
header、由调用方按 L2 失败关闭处理）。不得出现「悄悄少一条」的第三种路径。
（实现属前端 `documentCspNonce.ts`，本条只登记契约。）

### 7. `img-src` 允许 `https:`，`script-src` / `style-src` 不允许

这条策略正从「只给挂了 L2 岛的页」变成「所有公开页」下发。公开页的图片本来就在
站外：Gravatar 头像（`avatar.gravatar_base_url` 默认 `https://gravatar.com/avatar/`）
与用户贴进富文本的外链图片（`sfSanitize.ts` 对 `img[src]` 允许 http(s)）。
`img-src 'self'` 一旦全站生效，会把「图片不显示」从少数页面扩到全站，却换不到任何
执行边界。

理由分三层：

- 图片不执行代码。`<img>` 的字节只会被解码成像素，不会被当作 JS/CSS 解析，放行
  图片来源不扩大脚本信任根。
- 外部图片在无 CSP 时代就是允许的。这次没有引入新能力，只是不再收回既有能力。
- 代码边界仍由 `script-src` / `style-src` / `worker-src` 卡住：`script-src` 与
  `worker-src` 仍拒绝 `'unsafe-inline'`、nonce 与远程源，`style-src` 的
  `'unsafe-inline'` 是第 3 节记录的唯一例外。`media-src` / `font-src` /
  `connect-src` 没有证据需要放宽，保持 `'self'`。

放弃的东西（明确记账）：外部图片可以用作跟踪像素——第三方能从请求里拿到访问者
IP、User-Agent、Referer 与访问时间；用户贴的外链图也让图片托管方能统计阅读量。
这是论坛富文本的固有性质（sanitize 本就允许 `img[src]` 的 http(s)），不是 CSP 单独
能解决的产品问题；要收，应走「图片本地化/代理」，而不是在 CSP 里假装能拦。

兼容性影响（公共契约）：

- `PublicFrontendDocumentPolicy.headerValue` 的 `img-src` 多一个来源，`digest` 随之
  变化；用 digest 做缓存键的调用方自动失效重取，无需迁移。
- 扩展片段**不能**声明 `img-src https:`：`strictPublicPagePolicySource` 只接受
  `scheme://host[:port]` 形式，裸 scheme 会被拒为
  `PublicPagePolicyDirectiveInvalid`（测试子项 `img broad scheme`）。`https:` 是宿主
  基线独有的权利。
- 前端那份手抄基线（见「已知缺口」）要同步加 `https:`，否则浏览器门禁会先红。

边界条件：`publicPageHostBaselinePolicy()` 是纯常量表，无 I/O、无共享状态、无超时；
空扩展片段（`Directives` 为空）仍产出完整基线
（`TestPublicPageDocumentPolicyHostBaselineIsSSRSatisfiable` 覆盖）；合并阶段只有排序
与去重，`https:` 与具体主机 `https://cdn.example.com` 并存且顺序确定
（`https:` 是 `https://…` 的前缀，排在其前）。

## Accepted Risk

- `style-src 'unsafe-inline'` 让"能注入 HTML 就能注入样式"成立：可做视觉钓鱼、
  覆盖安全提示的呈现、用 CSS 选择器侧信道（如 `:has()`/属性选择器）做粗粒度探测。
  这条风险在引入 L2 之前就存在（宿主自己就必须内联样式），本次只是把策略改成
  与现实一致，没有新增可利用面。
- `img-src data: blob:` 让图片源可以是不透明字节，绕过 `connect-src` 的抓取路径
  观测（图片不进 XHR/fetch 记录）。影响限于取证可观测性，不影响脚本执行边界。
- `img-src https:` 让外部图片可作跟踪像素、泄露访问者 IP 与阅读行为（详见第 7 节）。
- 真正的 HTML 注入防线仍在内容层（sanitize 单白名单 + Tiptap schema），
  CSP 只是纵深防御的一层，不能当作唯一屏障。
- **dev 与生产下发同一份 CSP 的代价**：开发期同样写 `Content-Security-Policy`，
  缺陷才能在开发期暴露（而不是上线后才发现内联脚本被拦）。代价是工具链自己会
  撞策略：Vite/Nuxt 的 HMR websocket 与 Iconify 的客户端兜底请求都不是同源静态
  资源，会被 `connect-src 'self'` 拦掉。
  边界：缓解只允许在**前端 dev 分支**对 `connect-src` 追加工具链专用源，且
  `import.meta.dev` 之外不得生效；`connect-src` 的 Host 基线保持恰好 `'self'`，
  工具链源不得写进 `publicPageHostBaselinePolicy()`，也不得随 `documentPolicy`
  下发给生产。生产 header 与本文基线逐字一致，是这条缓解的验收口径。

## 已知缺口

- **跨语言基线只有浏览器门禁在守**：`apps/web/tests/errors/publicPageDocumentPolicy.test.ts`
  里的 `HOST_BASELINE_DIRECTIVES` 是 Go `publicPageHostBaselinePolicy()` 的手抄副本
  （连来源顺序都是手写的 `'self', 'data:', 'blob:'`，与 Go 侧排序后的
  `'self', 'blob:', 'data:'` 不同）。Go 侧再改基线，这份副本照样绿——它测的是前端
  组装逻辑，不是基线本身。
  缓解：`node tests/validate-document-csp.mjs`（真实 Chrome + CDP，不依赖
  Playwright）是唯一的跨语言守卫——它断言目标页确实下发 `Content-Security-Policy`、
  `securitypolicyviolation` 零触发、`--ui-primary` 与至少一条内联样式真的生效、
  `script-src` 恰好一个 nonce 且渲染出的内联 `<script>` 用的是同一个、
  `style-src` 不含 nonce/hash。该门禁的 `--self-test`（本地夹具，无服务）已接进
  `scripts/test.sh` 的 static 线，必须通过；真实服务那一跑在 CI 里，服务不在时退 2
  记 SKIP。任何基线改动都必须让这条门禁在真实服务上跑一次绿，静态单测不算证据。

## Consequences

- 公开页在真实浏览器里恢复设计 token、Icon SSR 样式与 `:style` 属性；策略端点
  不再 500，SSR 一定写出 `Content-Security-Policy`。
- 文档策略 digest 随基线变化，旧 digest 不再匹配；缓存键含 digest，无需迁移。
- `X-SForum-Document-Policy-Digest` 只标识 Host 策略快照；它与最终 header 不再
  逐字对应（per-request nonce），任何「用 digest 校验 header」的用法都是错的。
- 主题资产响应自带的资产级 CSP
  （`default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; font-src 'self'`）
  是另一条链路，不随本文档基线变化，契约里已注明两者不同。

## Revisit Triggers

- Nuxt / Nuxt UI 不再输出内联样式或 `:style`（例如改用可 hash 的静态 CSS）时，
  应移除 `'unsafe-inline'` 并回到 `style-src 'self'`。
- 需要给 L2 样式引入 nonce 时：必须同时撤掉 `'unsafe-inline'`，并证明宿主
  SSR 已无内联样式注入，否则两者互斥。
- 引入允许远程图片源（运营配置 CDN 白名单）时，`img-src` 的放宽要走运营配置
  路径，不在基线里硬编码。
