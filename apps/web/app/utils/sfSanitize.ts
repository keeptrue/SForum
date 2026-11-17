// H3: 前端二次净化 HTML 内容，作为服务端 bluemonday 之外的纵深防御。
//
// **首屏权威是 SSR**：Vue 3.4+ 把 `v-html` 编译成 `innerHTML` prop，水合阶段跳过子节点
// 比对，所以首屏实际落 DOM 的是 SSR 分支的结果。因此两条分支必须给出同一份结果 ——
// 否则「首屏看到的」和「水合后的」就是两套净化口径，而用户先看到的是其中较松的那套。
//
// 单一来源是 `SF_SANITIZE_POLICY`（下面这一份定义）：
// - SSR 分支（parse5，无 DOM 依赖）直接消费它；
// - 客户端分支（DOMPurify）由 `SF_PURIFY_CONFIG` 把它投影成 DOMPurify 的配置项，
//   包括那些**默认值与本策略不同**的开关（任意 data-*/aria-*、禁止容器保留子节点、
//   协议白名单、非 URL 属性也做 URI 过滤）。
//
// 等价性由 `tests/security/sanitizeEquivalence.test.ts` 用同一语料逐条断言
// （SSR 输出 === 客户端输出）。唯一保留的差异是 DOMPurify 的 `SANITIZE_DOM`：
// 客户端会额外剥掉与 document 属性同名的 id/name，SSR 没有 document 无法复刻；
// 该差异只删属性、不删内容，且方向是「客户端更严」，测试与决策记录都已点名。
// 见 `knowledge/decisions/2026-11-17-sanitize-single-whitelist.md`。
import DOMPurify from 'dompurify'
import { parseFragment, type DefaultTreeAdapterTypes } from 'parse5'

/**
 * 净化策略的唯一定义。改这里就是改两端 —— 不要在别处再写一份白名单。
 *
 * 白名单本身与服务端 bluemonday UGCPolicy 大致对齐：允许常见富文本标签，
 * 禁用脚本/事件处理器/危险属性。
 */
export const SF_SANITIZE_POLICY = {
  allowedTags: [
    'a', 'abbr', 'b', 'blockquote', 'br', 'caption', 'cite', 'code', 'col',
    'colgroup', 'dd', 'del', 'details', 'div', 'dl', 'dt', 'em', 'figcaption',
    'figure', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'i', 'img', 'ins',
    'kbd', 'li', 'mark', 'ol', 'p', 'pre', 'q', 's', 'samp', 'small', 'span',
    'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'tfoot', 'th',
    'thead', 'tr', 'u', 'ul', 'var', 'input',
    // 服务端 shortcode 渲染输出的语义标签，剥掉后 CSS 选择器全部失效。
    'article', 'time'
  ],
  allowedAttributes: [
    'href', 'title', 'alt', 'src', 'width', 'height', 'class', 'id',
    'target', 'rel', 'colspan', 'rowspan', 'start', 'type', 'checked',
    'disabled', 'open', 'data-sforum-image-viewer', 'data-sforum-image-size',
    'data-sforum-image-long',
    // 表格单元格对齐：服务端 bluemonday 与前端 DOMPurify 都保留 align 属性，
    // 内联 style 会被两端剥掉，因此对齐只能走属性。
    'align'
  ],
  /** 事件处理器按前缀整类拒绝；这里是其余必须拒绝的属性。 */
  forbiddenAttributes: ['onerror', 'onload', 'onclick', 'onmouseover', 'style'],
  /**
   * 禁止出现的容器：标签本身被剥掉。
   * `dropSubtreeTags` 里的这些标签在 SSR 侧连子节点一起丢，客户端由
   * `ADD_FORBID_CONTENTS` 补齐同样的语义。
   */
  forbiddenTags: ['script', 'style', 'form', 'iframe', 'object', 'embed'],
  /**
   * 一旦出现，整棵子树都不属于正文：script/style 在解析器里是原始文本，
   * template 的子节点挂在 content 上，其余是表单与嵌入对象。
   * 维护提示：往 allowedTags 加 void 元素要同步 voidTags；往 forbiddenTags 加
   * 「内容本身不算正文」的标签要同步这里。
   */
  dropSubtreeTags: [
    'script', 'style', 'form', 'iframe', 'object', 'embed',
    'template', 'noscript', 'title', 'noembed', 'noframes', 'plaintext', 'xmp',
    // 外来内容（svg/math）与媒体容器：DOMPurify 的 DEFAULT_FORBID_CONTENTS 默认整棵丢弃，
    // SSR 侧必须同口径，否则首屏会把 DOMPurify 水合后会丢掉的内容先画出来。
    // 注意 colgroup / thead 虽在 DOMPurify 的默认名单里，但它们在 allowedTags 内，
    // 允许的标签不会被移除，因此不能进本名单（否则 SSR 会把表格结构整块删掉）。
    'svg', 'math', 'annotation-xml', 'desc', 'foreignobject',
    'mi', 'mn', 'mo', 'ms', 'mtext', 'audio', 'video', 'head', 'selectedcontent'
  ],
  /** 自闭合（void）元素：序列化时不写结束标签。 */
  voidTags: ['br', 'hr', 'img', 'input', 'col'],
  /** 只有这些属性按 URL 语义校验协议（其余白名单属性不做 URI 过滤）。 */
  urlAttributes: ['href', 'src'],
  /** 只有 img 允许 data: URI。 */
  dataUriTags: ['img'],
  /** 允许的协议。相对路径、锚点、协议相对地址没有 scheme，交给浏览器按同源处理。 */
  safeUrlSchemes: ['http:', 'https:', 'mailto:', 'tel:', 'ftp:', 'ftps:']
} as const

const ALLOWED_TAG_SET = new Set<string>(SF_SANITIZE_POLICY.allowedTags)
const ALLOWED_ATTR_SET = new Set<string>(SF_SANITIZE_POLICY.allowedAttributes)
const FORBID_ATTR_SET = new Set<string>(SF_SANITIZE_POLICY.forbiddenAttributes)
const VOID_TAGS = new Set<string>(SF_SANITIZE_POLICY.voidTags)
const URL_ATTRS = new Set<string>(SF_SANITIZE_POLICY.urlAttributes)
const DATA_URI_TAGS = new Set<string>(SF_SANITIZE_POLICY.dataUriTags)
const SAFE_URL_SCHEMES = new Set<string>(SF_SANITIZE_POLICY.safeUrlSchemes)

/** 导出供测试断言「必须覆盖全部 forbiddenTags」，防止两处静默漂移。 */
export const SF_DROP_SUBTREE_TAGS: readonly string[] = SF_SANITIZE_POLICY.dropSubtreeTags
const DROP_SUBTREE_TAGS = new Set<string>(SF_DROP_SUBTREE_TAGS)

/**
 * DOMPurify 的默认 `FORBID_CONTENTS` 已含 script/style/iframe/template/noscript/
 * title/noembed/noframes/plaintext/xmp（以及 svg/math 等外来内容族），
 * 与 `dropSubtreeTags` 相比只差这三个。用 `ADD_FORBID_CONTENTS` 只补差额，
 * 不要改成 `FORBID_CONTENTS`：那会整份覆盖默认值，反而放开 svg/math 的子树丢弃。
 */
const PURIFY_EXTRA_DROP_CONTENTS = ['form', 'object', 'embed'] as const

/**
 * 与 SSR 同口径的协议白名单，供 DOMPurify 的 `ALLOWED_URI_REGEXP` 使用。
 *
 * 结构照 DOMPurify 文档推荐的「safe URI」正则，只把协议名换成策略里的集合：
 * 前一段放行 `http:/https:/mailto:/tel:/ftp:/ftps:`，后两段放行相对路径、锚点、
 * 协议相对地址与无 scheme 的 `foo/bar`。`javascript:`/`vbscript:`/`data:` 都不会命中，
 * 其中 `data:` 由 DOMPurify 的 `DATA_URI_TAGS` 分支单独按标签放行（只 img）。
 */
const SF_ALLOWED_URI_PATTERN = (() => {
  const schemes = SF_SANITIZE_POLICY.safeUrlSchemes.map(scheme => scheme.replace(':', '')).join('|')
  return new RegExp(`^(?:(?:${schemes}):|[^a-z]|[a-z+.\\-]+(?:[^a-z+.\\-:]|$))`, 'i')
})()

/**
 * 客户端分支的 DOMPurify 配置 —— 由 `SF_SANITIZE_POLICY` 投影而来，不是第二份白名单。
 *
 * 下面这些开关都是「DOMPurify 默认值与 SSR 不同」的地方，缺一个就会重新出现口径漂移：
 * - `ALLOW_DATA_ATTR` / `ALLOW_ARIA_ATTR`：默认放行任意 data- 与 aria- 属性，SSR 只认白名单；
 * - `ADD_FORBID_CONTENTS`：默认对 form/object/embed 保留子节点，SSR 整棵丢弃；
 * - `ADD_URI_SAFE_ATTR`：默认对所有属性值做 URI 过滤，SSR 只校验 href/src，
 *   不补这条会把 `title="javascript:…"` 这类无害文案也删掉；
 * - `ALLOWED_URI_REGEXP`：默认还放行 xmpp:/sms:/callto:/cid:，SSR 只认 6 种。
 */
export const SF_PURIFY_CONFIG = {
  ALLOWED_TAGS: [...SF_SANITIZE_POLICY.allowedTags],
  ALLOWED_ATTR: [...SF_SANITIZE_POLICY.allowedAttributes],
  FORBID_TAGS: [...SF_SANITIZE_POLICY.forbiddenTags],
  FORBID_ATTR: [...SF_SANITIZE_POLICY.forbiddenAttributes],
  ALLOW_DATA_ATTR: false,
  ALLOW_ARIA_ATTR: false,
  ADD_FORBID_CONTENTS: [...PURIFY_EXTRA_DROP_CONTENTS],
  ADD_URI_SAFE_ATTR: SF_SANITIZE_POLICY.allowedAttributes.filter(name => !URL_ATTRS.has(name)),
  ALLOWED_URI_REGEXP: SF_ALLOWED_URI_PATTERN,
  RETURN_TRUSTED_TYPE: false
}

/**
 * 净化 HTML 字符串。两条分支共用 `SF_SANITIZE_POLICY`，同一输入应得到同一输出。
 * 用法：v-html="sanitizeHtml(content)"
 */
export function sanitizeHtml(html: string | undefined | null): string {
  if (!html) {
    return ''
  }
  if (import.meta.server) {
    return sanitizeHtmlServer(html)
  }
  return String(DOMPurify.sanitize(html, SF_PURIFY_CONFIG))
}

/**
 * SSR 分支的纯函数版本：不依赖 window/DOM，可单独导入与测试。
 *
 * 这是首屏权威实现（见文件头）。它与客户端分支的等价性由
 * `tests/security/sanitizeEquivalence.test.ts` 断言，唯一保留差异是 DOMPurify
 * 的 `SANITIZE_DOM` id/name 剥离（客户端更严，只删属性不删内容）。
 */
export function sanitizeHtmlServer(html: string | undefined | null): string {
  if (!html) {
    return ''
  }
  return serializeSanitizedNodes(parseFragment(String(html)).childNodes)
}

function serializeSanitizedNodes(nodes: DefaultTreeAdapterTypes.ChildNode[]): string {
  let out = ''
  for (const node of nodes) {
    out += serializeSanitizedNode(node)
  }
  return out
}

function serializeSanitizedNode(node: DefaultTreeAdapterTypes.ChildNode): string {
  if (node.nodeName === '#text' && 'value' in node) {
    return escapeHtmlText(node.value)
  }
  if (node.nodeName === '#comment' || node.nodeName === '#documentType') {
    return ''
  }
  if (!('tagName' in node) || !('attrs' in node) || !('childNodes' in node)) {
    return ''
  }

  const tag = node.tagName.toLowerCase()
  if (DROP_SUBTREE_TAGS.has(tag)) {
    return ''
  }

  // 非白名单标签只剥标签、保留子节点（与 DOMPurify 的 KEEP_CONTENT 一致；
  // 禁止容器已在上面整棵丢弃）。
  const children = serializeSanitizedNodes(node.childNodes)
  if (!ALLOWED_TAG_SET.has(tag)) {
    return children
  }

  const attrs = sanitizeServerAttributes(tag, node.attrs)
  const serializedAttrs = Object.entries(attrs)
    .map(([name, value]) => ` ${name}="${escapeHtmlAttribute(value)}"`)
    .join('')

  if (VOID_TAGS.has(tag)) {
    return `<${tag}${serializedAttrs}>`
  }
  return `<${tag}${serializedAttrs}>${children}</${tag}>`
}

function sanitizeServerAttributes(
  tag: string,
  attrs: DefaultTreeAdapterTypes.Element['attrs']
): Record<string, string> {
  const result: Record<string, string> = {}

  for (const attr of attrs) {
    const name = attr.name.toLowerCase()
    // 事件处理器按前缀整类拒绝，其余只认白名单与 forbiddenAttributes。
    if (!name || name.startsWith('on') || FORBID_ATTR_SET.has(name) || !ALLOWED_ATTR_SET.has(name)) {
      continue
    }
    if (URL_ATTRS.has(name) && !isSafeUrl(tag, attr.value)) {
      continue
    }
    result[name] = attr.value
  }

  return result
}

function isSafeUrl(tag: string, value: string): boolean {
  // 先去掉控制字符与空白再判协议，`java\tscript:` 这类变形绕不过去。
  const canonical = value.replace(/[\u0000-\u0020\u007f]/g, '').toLowerCase()
  const scheme = /^([a-z][a-z0-9+.-]*):/.exec(canonical)
  if (!scheme) {
    // 相对路径、锚点、协议相对地址没有 scheme，交给浏览器按同源处理。
    return true
  }

  const name = `${scheme[1]}:`
  if (name === 'data:') {
    return DATA_URI_TAGS.has(tag)
  }
  return SAFE_URL_SCHEMES.has(name)
}

function escapeHtmlText(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

function escapeHtmlAttribute(value: string): string {
  return escapeHtmlText(value).replaceAll('"', '&quot;')
}
