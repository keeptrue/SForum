import { describe, expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'
import {
  SF_DROP_SUBTREE_TAGS,
  SF_PURIFY_CONFIG,
  SF_SANITIZE_POLICY,
  sanitizeHtml,
  sanitizeHtmlServer
} from '../../app/utils/sfSanitize'

/**
 * SSR 分支（parse5）逐条行为 + 客户端分支的真实净化结果。
 *
 * 历史：这一族断言曾经把 `dompurify` 换成记录型替身，因为 happy-dom 下 DOMPurify
 * 3.4 的节点遍历不工作（输入会被原样返回，`<script>` 都不剥），真实净化只能靠浏览器。
 * 现在用 `jsdom` 绑定一个独立实例来跑真实 DOMPurify —— 注意 `mock.module('dompurify')`
 * 是**整轮测试共享**的，用替身会连带把别的测试文件也污染成假绿。
 *
 * 两端等价性（同输入 → 同输出）在 `sanitizeEquivalence.test.ts` 里断言；
 * 这里只钉 SSR 自己的行为与白名单语义。
 */
// `mock.module('dompurify', …)` 是整轮测试共享的（tests/content/richContentMermaid.test.ts
// 会把**裸模块名**换成记录型替身），按裸名导入会拿到替身 → 断言变假绿。
// 这里用包的 UMD 子路径导入真实实现（exports 里公开声明，不受裸名替身影响）。
const createDOMPurify = ((await import('dompurify/dist/purify.js')) as unknown as {
  default: typeof import('dompurify').default
}).default

const { window } = new JSDOM('')
const purify = createDOMPurify(window as unknown as Parameters<typeof createDOMPurify>[0])
const sanitizeHtmlClient = (html: string) => String(purify.sanitize(html, SF_PURIFY_CONFIG))

describe('sanitizeHtmlServer：SSR 分支（首屏权威实现）', () => {
  test('剥离 <script> 及其内容', () => {
    const out = sanitizeHtmlServer('<p>ok</p><script>alert(1)</script>')

    expect(out).toContain('<p>ok</p>')
    expect(out).not.toContain('<script')
    expect(out).not.toContain('alert(1)')
  })

  test('剥离 <img onerror> 的事件处理器，保留 src 与 alt', () => {
    const out = sanitizeHtmlServer('<img src="/a.png" alt="示意图" onerror="alert(1)">')

    expect(out).toContain('<img')
    expect(out).toContain('src="/a.png"')
    expect(out).toContain('alt="示意图"')
    expect(out).not.toContain('onerror')
    expect(out).not.toContain('alert(1)')
  })

  test('处理 javascript: 协议的 href（大小写、实体编码、控制字符绕过）', () => {
    const payloads = [
      '<a href="javascript:alert(1)">x</a>',
      '<a href="JaVaScRiPt:alert(1)">x</a>',
      '<a href="&#106;avascript:alert(1)">x</a>',
      '<a href="java\tscript:alert(1)">x</a>',
      '<a href="vbscript:msgbox(1)">x</a>'
    ]

    for (const payload of payloads) {
      const out = sanitizeHtmlServer(payload)

      expect(out).not.toContain('javascript')
      expect(out).not.toContain('vbscript')
      expect(out).not.toContain('alert(1)')
      expect(out).toContain('>x</a>')
    }
  })

  test('剥离 style 属性与 <style> 子树', () => {
    expect(sanitizeHtmlServer('<p style="color:red">t</p>')).toBe('<p>t</p>')
    expect(sanitizeHtmlServer('<style>p{color:red}</style>')).toBe('')
  })

  test('保留白名单标签、表格对齐属性与图片查看器属性', () => {
    const out = sanitizeHtmlServer(
      '<article><h2>标题</h2><time>2020</time>'
      + '<p><strong>粗</strong><em>斜</em></p>'
      + '<table><tbody><tr><td align="center" colspan="2">1</td></tr></tbody></table>'
      + '<img src="/b.png" data-sforum-image-viewer="1" data-sforum-image-size="large" data-sforum-image-long="0">'
      + '</article>'
    )

    expect(out).toContain('<article>')
    expect(out).toContain('<time>')
    expect(out).toContain('<strong>粗</strong>')
    expect(out).toContain('<em>斜</em>')
    expect(out).toContain('<td align="center" colspan="2">')
    expect(out).toContain('data-sforum-image-viewer="1"')
    expect(out).toContain('data-sforum-image-size="large"')
    expect(out).toContain('data-sforum-image-long="0"')
  })

  test('剥掉白名单外的属性与任意 data-/aria- 属性', () => {
    expect(sanitizeHtmlServer('<time datetime="2020-01-01">d</time>')).toBe('<time>d</time>')
    expect(sanitizeHtmlServer('<p data-track="x">t</p>')).toBe('<p>t</p>')
    expect(sanitizeHtmlServer('<p aria-label="x">t</p>')).toBe('<p>t</p>')
  })

  test('丢弃禁止容器与外来内容的整棵子树', () => {
    const payloads = [
      '<form><input type="text"><p>inside</p></form>',
      '<object><p>inside</p></object>',
      '<iframe src="/x"><p>inside</p></iframe>',
      // 外来内容：HTML 解析器遇到 <p> 会先跳出 svg/math，所以这里用留在外来内容里的
      // 子节点形状（真实攻击面也是这种：<svg><script>）。
      '<svg><script>alert(1)</script></svg>',
      '<math><mtext><script>alert(1)</script></mtext></math>',
      '<template><p>inside</p></template>'
    ]

    for (const payload of payloads) {
      expect(sanitizeHtmlServer(payload)).toBe('')
    }
  })

  test('非白名单标签只剥标签、保留子节点与文本', () => {
    expect(sanitizeHtmlServer('<section><p onclick="x()">c</p></section>')).toBe('<p>c</p>')
    expect(sanitizeHtmlServer('<custom-el>keep</custom-el>')).toBe('keep')
  })

  test('空输入与实体转义', () => {
    expect(sanitizeHtmlServer('')).toBe('')
    expect(sanitizeHtmlServer(undefined)).toBe('')
    expect(sanitizeHtmlServer(null)).toBe('')
    expect(sanitizeHtmlServer('<p>a &amp; b &lt;c&gt;</p>')).toBe('<p>a &amp; b &lt;c&gt;</p>')
    expect(sanitizeHtmlServer('<a href="/x?a=1&amp;b=2" title="q&quot;t">y</a>'))
      .toBe('<a href="/x?a=1&amp;b=2" title="q&quot;t">y</a>')
  })
})

describe('客户端分支：真实 DOMPurify 的结果', () => {
  test('与 SSR 同口径地剥离脚本、事件处理器与任意 data-/aria- 属性', () => {
    expect(sanitizeHtmlClient('<p>ok</p><script>alert(1)</script>')).toBe('<p>ok</p>')
    expect(sanitizeHtmlClient('<p data-x="1" aria-label="a">t</p>')).toBe('<p>t</p>')
    expect(sanitizeHtmlClient('<img src="/a.png" onerror="alert(1)">')).toBe('<img src="/a.png">')
  })

  test('白名单内的 data-sforum-image-* 仍然保留（编辑器与图片查看器依赖）', () => {
    expect(sanitizeHtmlClient('<img src="/a.png" data-sforum-image-viewer="1">'))
      .toBe('<img src="/a.png" data-sforum-image-viewer="1">')
  })

  test('非 URL 属性的协议文案不会被误删（SSR 只校验 href/src）', () => {
    expect(sanitizeHtmlClient('<p title="javascript:alert(1)">t</p>')).toBe('<p title="javascript:alert(1)">t</p>')
  })
})

describe('sanitizeHtml：分支选择与短路', () => {
  test('空输入短路，不进入任何分支', () => {
    expect(sanitizeHtml('')).toBe('')
    expect(sanitizeHtml(undefined)).toBe('')
    expect(sanitizeHtml(null)).toBe('')
  })

  test('SSR 上下文走 parse5 分支（与 sanitizeHtmlServer 同一实现）', () => {
    // bun test 里 import.meta.server 为 false，这里直接比对 SSR 纯函数，
    // 保证「服务端渲染走的那条路」就是被断言的那条路。
    expect(sanitizeHtmlServer('<p>a</p><script>x</script>')).toBe('<p>a</p>')
  })
})

/**
 * 伴生硬编码集合是唯一会静默漂移的地方：改白名单时容易忘。
 * 这里把「与策略的派生关系」写成断言。
 */
describe('整棵丢弃集合与策略的派生关系', () => {
  test('丢弃集合覆盖全部 forbiddenTags，且无重复', () => {
    for (const tag of SF_SANITIZE_POLICY.forbiddenTags) {
      expect(SF_DROP_SUBTREE_TAGS).toContain(tag)
    }
    expect(new Set(SF_DROP_SUBTREE_TAGS).size).toBe(SF_DROP_SUBTREE_TAGS.length)
  })

  test('允许的标签不得出现在丢弃集合里（否则表格结构会被整块删掉）', () => {
    for (const tag of SF_SANITIZE_POLICY.allowedTags) {
      expect(SF_DROP_SUBTREE_TAGS).not.toContain(tag)
    }
  })

  test('每个非 void 的丢弃标签都真的丢掉了子树内容', () => {
    // embed 是 void 元素，parse5 会把后续节点当成兄弟。
    const voidLike = new Set(['embed'])
    // 外来内容族：parse5 遇到 HTML 标签会先跳出 svg/math（`<svg><p>x</p></svg>` 解析成
    // 空的 svg + 兄弟 p），所以「用 <p> 当子节点」测不出丢弃行为；它们的覆盖在
    // sanitizeEquivalence.test.ts 的外来内容用例里（<svg><script>、<math><mtext>）。
    const foreignContent = new Set([
      'svg', 'math', 'annotation-xml', 'desc', 'foreignobject', 'mi', 'mn', 'mo', 'ms', 'mtext',
      // head 同理：parse5 会把 body 内容移出 <head>，`<head><p>x</p></head>` 解析成
      // 空的 head + 兄弟 p，用 <p> 当子节点测不出丢弃行为。
      'head'
    ])
    for (const tag of SF_DROP_SUBTREE_TAGS) {
      if (voidLike.has(tag) || foreignContent.has(tag)) {
        continue
      }
      expect(sanitizeHtmlServer(`<${tag}><p>inside</p></${tag}>`)).toBe('')
    }
  })

  test('SF_PURIFY_CONFIG 白名单语义未被 SSR 分支改动', () => {
    expect(SF_PURIFY_CONFIG.ALLOWED_TAGS).toEqual(
      expect.arrayContaining(['article', 'time', 'table', 'p', 'strong'])
    )
    expect(SF_PURIFY_CONFIG.ALLOWED_ATTR).toEqual(
      expect.arrayContaining([
        'align',
        'data-sforum-image-viewer',
        'data-sforum-image-size',
        'data-sforum-image-long'
      ])
    )
    expect(SF_PURIFY_CONFIG.FORBID_TAGS).toEqual(
      expect.arrayContaining(['script', 'style', 'form', 'iframe', 'object', 'embed'])
    )
    expect(SF_PURIFY_CONFIG.FORBID_ATTR).toEqual(
      expect.arrayContaining(['onerror', 'onload', 'onclick', 'onmouseover', 'style'])
    )
    expect(SF_PURIFY_CONFIG.RETURN_TRUSTED_TYPE).toBe(false)
  })
})
