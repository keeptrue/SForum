# 2026-11-17 前端二次净化收口成单一白名单（首屏权威是 SSR）

- 状态：已采纳
- 相关 issue：#218（R7-FE-4）
- 相关文件：`apps/web/app/utils/sfSanitize.ts`、`apps/web/tests/security/sfSanitize.test.ts`、
  `apps/web/tests/security/sanitizeEquivalence.test.ts`

## 背景

`utils/sfSanitize.ts` 有两条内置分支：SSR 走 parse5 白名单序列化（无 DOM 依赖），
客户端走 DOMPurify。两条分支**共用同一份标签/属性数组**，但 DOMPurify 的**默认开关**
与 SSR 的策略不同，实际口径并不等价：

| 维度 | DOMPurify 默认 | SSR 策略 |
|---|---|---|
| 任意 `data-*` / `aria-*` | 放行 | 只认 `ALLOWED_ATTR` |
| `form` / `object` / `embed` 子树 | 保留子节点（KEEP_CONTENT） | 整棵丢弃 |
| `svg` / `math` 等外来内容子树 | 整棵丢弃（`FORBID_CONTENTS`） | 原先保留子节点 |
| URL 协议 | 额外放行 `xmpp:` `sms:` `callto:` `cid:` | 只认 6 种 |
| 非 URL 属性 | 也做 URI 过滤（`title="javascript:…"` 会被删） | 只校验 `href`/`src` |
| `id`/`name` 与 document 属性同名 | 剥掉（`SANITIZE_DOM`） | 无 document，无法判定 |

同时，Vue 3.4+ 把 `v-html` 编译成 `innerHTML` prop，水合阶段**跳过子节点比对**：
首屏实际落 DOM 的是 SSR 分支的结果，客户端分支的结果在水合时并不参与首屏。
于是「客户端兜底更严」这个说法在运行时并不成立 —— 用户先看到的是两套口径里较松的那套。

## 决策

1. **单一来源**：`SF_SANITIZE_POLICY` 是唯一白名单定义（标签、属性、禁止属性、
   整棵丢弃集合、void 集合、URL 属性、data: URI 标签、允许协议）。
   SSR 直接消费它；客户端由 `SF_PURIFY_CONFIG` 把它**投影**成 DOMPurify 的配置项，
   包括那些「默认值与策略不同」的开关（`ALLOW_DATA_ATTR` / `ALLOW_ARIA_ATTR` /
   `ADD_FORBID_CONTENTS` / `ADD_URI_SAFE_ATTR` / `ALLOWED_URI_REGEXP`）。
   不再维护第二份白名单，也不再用注释描述「两端已知差异」。

2. **首屏权威是 SSR**：任何「两端不一致」的修复方向都是**让客户端对齐 SSR**，
   而不是反过来。这条是上一条的直接推论（水合跳过子节点）。

3. **等价性有断言**：`tests/security/sanitizeEquivalence.test.ts` 用同一份语料
   （脚本、事件属性、任意 data-/aria-、禁止容器子树、外来内容、协议变体、
   void/布尔属性、实体、注释、未知标签、表格对齐…）逐条断言
   `sanitizeHtmlServer(x) === client(x)`，并断言客户端配置确实是策略的投影。
   变异自查：把 `ALLOW_DATA_ATTR` 改回 `true`、或把 `form` 从
   `ADD_FORBID_CONTENTS` 拿掉（都是**只放宽客户端**），该测试立刻红。

4. **删除「双链等价」类宣称**：`sfSanitize.ts` 头部与
   `knowledge/decisions/2026-07-09-security-fixes.md` 里「SSR 信任服务端 bluemonday」
   的旧描述都已改正 —— 前端 SSR 分支自己净化，且它与客户端同口径。

## 保留的两处差异（都有断言，都不许默默变化）

1. **`SANITIZE_DOM`（id/name 与 document 属性同名）**：DOMPurify 会剥掉
   `id="cookie"` 这类属性，SSR 跑在没有 `document` 的环境里无法复刻这份名单。
   保留方向是「客户端更严」：只删属性、不删内容、不新增标签。
   断言：`sanitizeHtmlServer('<p id="cookie" class="c">')` 保留 id，
   客户端输出等于 SSR 去掉该 id 的结果。
   选择保留而不是关掉 `SANITIZE_DOM`：关掉等于为了对齐字符串而放弃 DOM clobbering 防护。

2. **输入首个顶层节点是 `<noscript>` 时 DOMPurify 提升 fallback 子节点**：
   这是 DOMPurify 在「输入以该标签开头」时的初始化路径怪癖，不是策略差异
   （两端策略都把 `noscript` 记为整棵丢弃；同样内容包一层容器后两端一致）。
   生产输入是服务端渲染出的完整片段，且首屏走 SSR（会丢弃），所以记录 + 断言，
   不为它放宽策略。

## 为什么不用别的方案

- **删掉 DOMPurify，两端都跑 parse5**：能拿到「结构上等价」，但等于放弃浏览器解析器
  这一层，超出本 issue 的范围；而且 DOMPurify 的 mXSS 防护是仓库刻意保留的一层。
- **把 `FORBID_CONTENTS` 整份替换成自己的列表**：会丢掉 DOMPurify 默认对
  `svg`/`math`/`annotation-xml` 等外来内容的子树丢弃，反而更松。所以只用
  `ADD_FORBID_CONTENTS` 补差额（`form`/`object`/`embed`），并把 `svg`/`math`
  一族补进 SSR 的丢弃集合，让两端都丢。
- **关掉 `SANITIZE_DOM`**：见上，为对齐字符串牺牲 clobbering 防护，不划算。

## 测试环境注意事项

- DOMPurify 在 **happy-dom 下是空操作**（实测把 `<script>` 原样返回），所以
  客户端分支的真实行为必须在 **jsdom** 里跑：`createDOMPurify(jsdomWindow)`。
  为此 `apps/web` 增加 devDependency `jsdom`（仅测试用，不进产物）。
- `mock.module('dompurify', …)` 在 bun 里是**整轮测试共享**的
  （`tests/content/richContentMermaid.test.ts` 会替换裸模块名），所以
  sanitize 相关测试用包的 UMD 子路径 `dompurify/dist/purify.js` 导入真实实现，
  否则会被别人的替身污染成假绿。
- parse5 会把 HTML 标签移出外来内容（`<svg><p>x</p></svg>` 解析成空 svg + 兄弟 p），
  也会把 body 内容移出 `<head>`；「整棵丢弃」的全量扫描因此跳过这些标签，
  它们的覆盖放在等价性语料的外来内容用例里。
