import { describe, expect, mock, test } from 'bun:test'

// 客户端分支的真实净化由浏览器里的 DOMPurify 完成。测试环境是 happy-dom，
// 它和 DOMPurify 3.4 的节点遍历（NodeIterator + 原型访问器）不兼容：首个顶层
// 元素会被误判为非白名单标签而剥掉。断言真实净化结果只会测出这个环境缺陷，
// 所以这里用记录型替身验证客户端分支的接线（HTML 与 SF_PURIFY_CONFIG 一起
// 交给 DOMPurify），净化策略本身由同构的 sanitizeHtmlServer 纯函数逐条断言。
const purifyCalls: Array<{ html: string, config: unknown }> = []

const domPurifyStub = {
  sanitize(html: string, config?: unknown) {
    purifyCalls.push({ html, config })
    return html
  }
}

mock.module('dompurify', () => ({ default: domPurifyStub }))

const { sanitizeHtml, sanitizeHtmlServer, SF_PURIFY_CONFIG, SF_SERVER_DROP_SUBTREE_TAGS } =
  await import('../../app/utils/sfSanitize')

describe('sanitizeHtmlServer：SSR 分支与客户端同构净化', () => {
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

  test('未列入 ALLOWED_ATTR 的属性与客户端一样被剥离', () => {
    expect(sanitizeHtmlServer('<time datetime="2020-01-01">d</time>')).toBe('<time>d</time>')
    expect(sanitizeHtmlServer('<p data-track="x">t</p>')).toBe('<p>t</p>')
  })

  test('整棵丢弃禁止子树（script/style/form/iframe/object/embed/template）', () => {
    for (const payload of [
      '<script>alert(1)</script>',
      '<style>p{}</style>',
      '<form><input type="text" value="x"></form>',
      '<iframe src="https://evil.example"></iframe>',
      '<object data="x"></object>',
      '<embed src="x">',
      '<template><p>hidden</p></template>'
    ]) {
      expect(sanitizeHtmlServer(payload)).toBe('')
    }
  })

  test('嵌套在非白名单容器里的脚本同样被剥离', () => {
    expect(sanitizeHtmlServer('<svg><script>alert(1)</script></svg>')).toBe('')
    // section 不在白名单：只剥标签、保留子节点（与 DOMPurify 的 KEEP_CONTENT 一致）。
    expect(sanitizeHtmlServer('<section><p onclick="x()">c</p></section>')).toBe('<p>c</p>')
  })

  test('空输入返回空串，且文本与属性值被重新转义', () => {
    expect(sanitizeHtmlServer('')).toBe('')
    expect(sanitizeHtmlServer(undefined)).toBe('')
    expect(sanitizeHtmlServer(null)).toBe('')
    expect(sanitizeHtmlServer('<p>a &amp; b &lt;c&gt;</p>')).toBe('<p>a &amp; b &lt;c&gt;</p>')
    expect(sanitizeHtmlServer('<a href="/x?a=1&amp;b=2" title="q&quot;t">y</a>'))
      .toBe('<a href="/x?a=1&amp;b=2" title="q&quot;t">y</a>')
  })
})

/**
 * SSR 与客户端的已知差异（用真实浏览器 DOMPurify 3.4 对拍得出）。
 * 两条路径共享白名单条目，但**不是等价结果**：SSR 严格更严，水合后客户端会补回
 * 一部分属性与子树。下面逐条把差异钉住 —— 谁改白名单导致差异扩大，测试会红。
 */
describe('SSR 与客户端的已知差异（SSR 更严，非等价）', () => {
  test('① 禁止容器：客户端 KEEP_CONTENT 保留子节点，SSR 整棵丢弃', () => {
    // 客户端等价输出：<input type="text"><p>inside</p>
    expect(sanitizeHtmlServer('<form><input type="text"><p>inside</p></form>')).toBe('')
    expect(sanitizeHtmlServer('<object><p>inside</p></object>')).toBe('')
  })

  test('② 非白名单容器里的文本：客户端连文本一起丢，SSR 保留文本', () => {
    // 客户端等价输出：''
    expect(sanitizeHtmlServer('<svg>hello</svg>')).toBe('hello')
  })

  test('③ data-* / aria-*：客户端默认放行任意，SSR 只认 ALLOWED_ATTR', () => {
    // 客户端等价输出：<p data-foo="1" aria-label="x">t</p>
    expect(sanitizeHtmlServer('<p data-foo="1" aria-label="x">t</p>')).toBe('<p>t</p>')
    // 白名单内的 data-sforum-image-* 必须继续保留（编辑器与图片查看器依赖它）。
    expect(sanitizeHtmlServer('<img src="/a.png" data-sforum-image-viewer="1">'))
      .toBe('<img src="/a.png" data-sforum-image-viewer="1">')
  })

  test('④ URL scheme：客户端还放行 xmpp/sms/callto/cid，SSR 只放行 6 种', () => {
    for (const scheme of ['xmpp:', 'sms:', 'callto:', 'cid:']) {
      expect(sanitizeHtmlServer(`<a href="${scheme}x">y</a>`)).toBe('<a>y</a>')
    }
    expect(sanitizeHtmlServer('<a href="https://sforum.example/x">y</a>'))
      .toBe('<a href="https://sforum.example/x">y</a>')
  })

  test('⑤ DOM clobbering：客户端 SANITIZE_DOM 会剥同名 id，SSR 原样保留', () => {
    // 客户端等价输出：<p>x</p>
    expect(sanitizeHtmlServer('<p id="document">x</p>')).toBe('<p id="document">x</p>')
  })
})

/**
 * 伴生硬编码集合是唯一会静默漂移的地方：它们不在 SF_PURIFY_CONFIG 里，
 * 改白名单时容易忘。这里把「与配置的派生关系」写成断言。
 */
describe('伴生硬编码集合与 SF_PURIFY_CONFIG 的派生关系', () => {
  test('SSR 丢弃集合覆盖全部 FORBID_TAGS，且无重复', () => {
    for (const tag of SF_PURIFY_CONFIG.FORBID_TAGS) {
      expect(SF_SERVER_DROP_SUBTREE_TAGS).toContain(tag)
    }
    expect(new Set(SF_SERVER_DROP_SUBTREE_TAGS).size).toBe(SF_SERVER_DROP_SUBTREE_TAGS.length)
  })

  test('每个非 void 的丢弃标签都真的丢掉了子树内容', () => {
    // embed 是 void 元素，parse5 会把后续节点当成兄弟，单独由上方 ① 覆盖。
    const voidLike = new Set(['embed'])
    for (const tag of SF_SERVER_DROP_SUBTREE_TAGS) {
      if (voidLike.has(tag)) {
        continue
      }
      expect(sanitizeHtmlServer(`<${tag}><p>inside</p></${tag}>`)).toBe('')
    }
  })
})

describe('sanitizeHtml：客户端分支接线', () => {
  test('把原始 HTML 与 SF_PURIFY_CONFIG 一起交给 DOMPurify', () => {
    purifyCalls.length = 0

    const out = sanitizeHtml('<p>hi</p>')

    expect(purifyCalls).toHaveLength(1)
    expect(purifyCalls[0]!.html).toBe('<p>hi</p>')
    expect(purifyCalls[0]!.config).toBe(SF_PURIFY_CONFIG)
    expect(out).toBe('<p>hi</p>')
  })

  test('空输入短路，不触碰 DOMPurify', () => {
    purifyCalls.length = 0

    expect(sanitizeHtml('')).toBe('')
    expect(sanitizeHtml(undefined)).toBe('')
    expect(sanitizeHtml(null)).toBe('')
    expect(purifyCalls).toHaveLength(0)
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
