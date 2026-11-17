import { describe, expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'
import {
  SF_DROP_SUBTREE_TAGS,
  SF_PURIFY_CONFIG,
  SF_SANITIZE_POLICY,
  sanitizeHtmlServer
} from '../../app/utils/sfSanitize'

/**
 * SSR（parse5）与客户端（DOMPurify）两条分支的等价性。
 *
 * 为什么必须等价：Vue 3.4+ 把 `v-html` 编译成 `innerHTML` prop，水合阶段跳过子节点
 * 比对，**首屏实际落 DOM 的是 SSR 分支**。两条分支若给出不同结果，用户先看到的
 * 就是其中较松的那套，而「客户端兜底」在首屏根本没生效。
 *
 * 为什么用 jsdom：DOMPurify 依赖真实 DOM 的节点遍历。happy-dom 下它会把输入
 * 原样返回（`<script>` 都不剥），断言会变成假绿；jsdom 是 DOMPurify 官方测试
 * 使用的 DOM，绑定 `DOMPurify(jsdomWindow)` 后行为与浏览器一致。
 *
 * 两处**已点名**的残留差异（各自有断言，理由见决策记录）：
 * 1. DOMPurify 的 `SANITIZE_DOM` 会额外剥掉与 document 属性同名的 id/name；
 * 2. 输入首个顶层节点是 `<noscript>` 时 DOMPurify 会提升 fallback 子节点。
 */
// runScripts 打开后 jsdom 的解析语义与浏览器一致：<noscript> 内容是原始文本而不是子元素，
// 否则 noscript 用例会测出「jsdom 的 scripting 开关」而不是两端策略差异。
// `mock.module('dompurify', …)` 是整轮测试共享的（tests/content/richContentMermaid.test.ts
// 会把**裸模块名**换成记录型替身），按裸名导入会拿到替身 → 断言变假绿。
// 这里用包的 UMD 子路径导入真实实现（exports 里公开声明，不受裸名替身影响）。
const createDOMPurify = ((await import('dompurify/dist/purify.js')) as unknown as {
  default: typeof import('dompurify').default
}).default

const { window } = new JSDOM('')
const purify = createDOMPurify(window as unknown as Parameters<typeof createDOMPurify>[0])

function sanitizeHtmlClient(html: string) {
  return String(purify.sanitize(html, SF_PURIFY_CONFIG))
}

/**
 * 同一份语料同时喂给两端。每条都必须两端逐字相同。
 *
 * 语料刻意覆盖「两端默认行为不同」的每一处：任意 data- 与 aria- 属性、禁止容器的子树、
 * 协议白名单、非 URL 属性的 URI 过滤、void 元素与布尔属性、实体、注释、未知标签。
 */
const EQUIVALENT_CORPUS: ReadonlyArray<{ name: string, html: string }> = [
  { name: 'script 子树', html: '<p>ok</p><script>alert(1)</script>' },
  { name: 'style 子树与 style 属性', html: '<style>p{color:red}</style><p style="color:red">t</p>' },
  { name: '事件处理器属性', html: '<img src="/a.png" alt="x" onerror="alert(1)" onload="x()">' },
  { name: '任意 data-*/aria-*（两端都应丢弃）', html: '<p data-track="x" data-x="1" aria-label="a" aria-hidden="true">t</p>' },
  { name: '白名单内的 data-sforum-image-*（两端都应保留）', html: '<img src="/b.png" data-sforum-image-viewer="1" data-sforum-image-size="large" data-sforum-image-long="0">' },
  { name: 'form 子树（两端都应整棵丢弃）', html: '<form action="/x"><input type="text"><p>in</p></form>' },
  { name: 'object/embed 子树', html: '<object data="/x"><param name="a" value="b"><p>fallback</p></object><embed src="/y">' },
  { name: 'iframe 子树', html: '<iframe src="/x"><p>inner</p></iframe>' },
  { name: 'template/noscript/title 子树', html: '<div><template><p>t</p></template><noscript><b>n</b></noscript><title>ti</title></div>' },
  { name: 'plaintext/xmp/noembed/noframes 子树', html: '<plaintext>x<p>y</p></plaintext><xmp><p>z</p></xmp><noembed><p>n</p></noembed><noframes><p>f</p></noframes>' },
  { name: 'javascript: / vbscript: 协议', html: '<a href="javascript:alert(1)">a</a><a href="JaVaScRiPt:alert(1)">b</a><a href="vbscript:msgbox(1)">c</a>' },
  { name: '控制字符与实体变形的 javascript: 协议', html: '<a href="java\tscript:alert(1)">a</a><a href="&#106;avascript:alert(1)">b</a><a href=" javascript:alert(1)">c</a>' },
  { name: 'SSR 与 DOMPurify 默认都放行之外的协议（xmpp/sms/callto/cid）', html: '<a href="xmpp:a@b">x</a><a href="sms:123">s</a><a href="callto:123">c</a><a href="cid:x">i</a>' },
  { name: '相对路径、锚点、协议相对地址、无 scheme 路径', html: '<a href="/rel">a</a><a href="#anchor">b</a><a href="//cdn.test/x">c</a><a href="foo/bar">d</a>' },
  { name: 'img 的 data: URI（两端都放行）', html: '<img src="data:image/png;base64,iVBORw0KGgo=">' },
  { name: 'a 的 data: URI（两端都拒绝）', html: '<a href="data:text/html,<script>alert(1)</script>">x</a>' },
  { name: '非 URL 属性里带协议文案（两端都不做 URI 过滤）', html: '<p title="javascript:alert(1)" class="c">t</p>' },
  { name: '表格对齐与跨列', html: '<table><tbody><tr><td align="center" colspan="2">1</td><th rowspan="2">h</th></tr></tbody></table>' },
  { name: 'void 元素与布尔属性', html: '<p>a<br>b</p><hr><input type="checkbox" checked disabled>' },
  { name: 'details/summary 的 open 属性', html: '<details open><summary>s</summary><p>b</p></details>' },
  { name: '实体与文本转义', html: '<p>a &amp; b &lt;c&gt; &quot;d&quot;</p>' },
  { name: '注释被丢弃', html: '<p>a<!-- secret -->b</p>' },
  { name: '大写标签与属性', html: '<P CLASS="x">t</P><A HREF="/a">l</A>' },
  { name: '未知标签只剥标签保留子节点', html: '<section><p onclick="x()">c</p></section><custom-el>keep</custom-el>' },
  { name: 'svg/math 外来内容', html: '<div><svg><script>alert(1)</script></svg><math><mtext><p>x</p></mtext></math></div>' },
  { name: '媒体与 selectedcontent 容器子树', html: '<div><audio><p>a</p></audio><video><p>v</p></video><selectedcontent><p>s</p></selectedcontent></div>' },
  { name: '普通 id/name（不触发 clobbering）', html: '<p id="comment-12" name="n">t</p>' },
  { name: '嵌套列表与引用', html: '<blockquote><ul><li>a<ol start="3"><li>b</li></ol></li></ul></blockquote>' },
  { name: '链接目标与 rel', html: '<a href="https://e.test/x" target="_blank" rel="nofollow noopener noreferrer">l</a>' }
]

describe('sanitizeHtml 两端等价（同输入 → 同输出）', () => {
  test('每一份语料两端输出逐字相同', () => {
    const mismatches: Array<{ name: string, ssr: string, client: string }> = []

    for (const sample of EQUIVALENT_CORPUS) {
      const ssr = sanitizeHtmlServer(sample.html)
      const client = sanitizeHtmlClient(sample.html)
      if (ssr !== client) mismatches.push({ name: sample.name, ssr, client })
    }

    expect(mismatches).toEqual([])
  })

  test('空输入两端都是空串', () => {
    for (const value of ['', null, undefined]) {
      expect(sanitizeHtmlServer(value)).toBe('')
      expect(sanitizeHtmlClient(String(value ?? ''))).toBe('')
    }
  })
})

describe('唯一保留的差异：DOMPurify 的 SANITIZE_DOM', () => {
  /**
   * DOMPurify 默认开启 SANITIZE_DOM：剥掉与 document 属性同名的 id/name
   * （DOM clobbering 防护）。SSR 跑在没有 document 的环境里，无法复刻这份名单，
   * 所以这一条**允许不一致** —— 但方向必须是「客户端更严」：只删属性，不删内容，
   * 也不新增任何标签。改这里之前先读
   * `knowledge/decisions/2026-11-17-sanitize-single-whitelist.md`。
   */
  test('clobbering id 只在客户端被剥掉，且差异仅限该属性', () => {
    const html = '<p id="cookie" class="c">clobber</p>'

    expect(sanitizeHtmlServer(html)).toBe('<p id="cookie" class="c">clobber</p>')
    expect(sanitizeHtmlClient(html)).toBe('<p class="c">clobber</p>')
    // 差异只在这一个属性上：把 SSR 的 id 去掉后必须与客户端一致。
    expect(sanitizeHtmlServer(html).replace(' id="cookie"', '')).toBe(sanitizeHtmlClient(html))
  })

  test('普通 id 不受影响，两端都保留', () => {
    const html = '<p id="comment-12">t</p>'
    expect(sanitizeHtmlServer(html)).toBe('<p id="comment-12">t</p>')
    expect(sanitizeHtmlClient(html)).toBe('<p id="comment-12">t</p>')
  })
})

describe('已知的第二处差异：首节点为 <noscript> 时 DOMPurify 的位置怪癖', () => {
  /**
   * 实测（jsdom + DOMPurify 3.4.17）：当**输入的第一个顶层节点**就是 `<noscript>` 时，
   * DOMPurify 会把 fallback 子节点提到父级（等于不丢子树）；同样的内容一旦被外层容器
   * 包住，两端就一致丢弃。
   *
   * 这不是白名单差异（两端策略都把 noscript 记为「整棵丢弃」），而是 DOMPurify 在
   * 「输入以该标签开头」时的解析/初始化路径差异。生产输入是服务端渲染出的完整片段，
   * 且首屏权威是 SSR（会丢弃），所以这里选择**记录并断言**，不去为它放宽策略。
   */
  test('首节点 noscript 被客户端提升子节点，包一层后两端一致', () => {
    expect(sanitizeHtmlServer('<noscript>plain</noscript>')).toBe('')
    expect(sanitizeHtmlClient('<noscript>plain</noscript>')).toBe('plain')

    // 同样的内容包进容器后两端一致（这才是真实内容片段的形状）。
    expect(sanitizeHtmlServer('<div><noscript>plain</noscript></div>')).toBe('<div></div>')
    expect(sanitizeHtmlClient('<div><noscript>plain</noscript></div>')).toBe('<div></div>')
  })

  test('首节点 noscript 之外的其它「整棵丢弃」标签两端一致', () => {
    for (const tag of ['template', 'title', 'xmp', 'plaintext', 'iframe', 'object', 'embed']) {
      const html = `<${tag}><p>x</p></${tag}>`
      expect(sanitizeHtmlServer(html)).toBe(sanitizeHtmlClient(html))
    }
  })
})

describe('客户端配置是策略的投影，不是第二份白名单', () => {
  test('标签与属性集合直接来自 SF_SANITIZE_POLICY', () => {
    expect(SF_PURIFY_CONFIG.ALLOWED_TAGS).toEqual([...SF_SANITIZE_POLICY.allowedTags])
    expect(SF_PURIFY_CONFIG.ALLOWED_ATTR).toEqual([...SF_SANITIZE_POLICY.allowedAttributes])
    expect(SF_PURIFY_CONFIG.FORBID_TAGS).toEqual([...SF_SANITIZE_POLICY.forbiddenTags])
    expect(SF_PURIFY_CONFIG.FORBID_ATTR).toEqual([...SF_SANITIZE_POLICY.forbiddenAttributes])
  })

  test('DOMPurify 默认值与策略冲突的开关都被显式关掉', () => {
    expect(SF_PURIFY_CONFIG.ALLOW_DATA_ATTR).toBe(false)
    expect(SF_PURIFY_CONFIG.ALLOW_ARIA_ATTR).toBe(false)
    expect(SF_PURIFY_CONFIG.ADD_FORBID_CONTENTS).toEqual(['form', 'object', 'embed'])
    // 除 href/src 外的白名单属性不做 URI 过滤，否则 title="javascript:…" 会被误删。
    expect(SF_PURIFY_CONFIG.ADD_URI_SAFE_ATTR).not.toContain('href')
    expect(SF_PURIFY_CONFIG.ADD_URI_SAFE_ATTR).not.toContain('src')
    expect(SF_PURIFY_CONFIG.ADD_URI_SAFE_ATTR).toContain('title')
  })

  test('两端使用同一份「整棵丢弃」标签集合', () => {
    // SSR 直接用它，客户端用 ADD_FORBID_CONTENTS 补差额；集合本身必须覆盖所有禁止标签。
    for (const tag of SF_SANITIZE_POLICY.forbiddenTags) {
      expect(SF_DROP_SUBTREE_TAGS).toContain(tag)
    }
  })
})
