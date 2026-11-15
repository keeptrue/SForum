// H3: 前端二次净化 HTML 内容，作为服务端 bluemonday 之外的纵深防御。
// 客户端走 DOMPurify（依赖 DOM）；SSR 走 parse5 白名单序列化（无 DOM 依赖），
// 首屏不再直接透传上游 HTML。两条路径共用同一份标签/属性白名单，但**不是同一份
// 结果**：SSR 侧严格更严，水合后客户端会补回一部分属性与子树。已知差异逐条钉在
// tests/security/sfSanitize.test.ts 的「SSR 与客户端的已知差异」小节里。
import DOMPurify from 'dompurify'
import { parseFragment, type DefaultTreeAdapterTypes } from 'parse5'

// 与服务端 bluemonday UGCPolicy 大致对齐的白名单：
// 允许常见富文本标签，禁用脚本/事件处理器/危险属性。
export const SF_PURIFY_CONFIG = {
  ALLOWED_TAGS: [
    'a', 'abbr', 'b', 'blockquote', 'br', 'caption', 'cite', 'code', 'col',
    'colgroup', 'dd', 'del', 'details', 'div', 'dl', 'dt', 'em', 'figcaption',
    'figure', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'hr', 'i', 'img', 'ins',
    'kbd', 'li', 'mark', 'ol', 'p', 'pre', 'q', 's', 'samp', 'small', 'span',
    'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'tfoot', 'th',
    'thead', 'tr', 'u', 'ul', 'var', 'input',
    // 服务端 shortcode 渲染输出的语义标签，剥掉后 CSS 选择器全部失效。
    'article', 'time'
  ],
  ALLOWED_ATTR: [
    'href', 'title', 'alt', 'src', 'width', 'height', 'class', 'id',
    'target', 'rel', 'colspan', 'rowspan', 'start', 'type', 'checked',
    'disabled', 'open', 'data-sforum-image-viewer', 'data-sforum-image-size',
    'data-sforum-image-long',
    // 表格单元格对齐：服务端 bluemonday 与前端 DOMPurify 都保留 align 属性，
    // 内联 style 会被两端剥掉，因此对齐只能走属性。
    'align'
  ],
  // 禁止任何脚本/样式/表单提交相关内容。
  FORBID_TAGS: ['script', 'style', 'form', 'iframe', 'object', 'embed'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'style'],
  RETURN_TRUSTED_TYPE: false
}

// SSR 分支的白名单集合直接由 SF_PURIFY_CONFIG 派生，避免两套规则漂移。
const ALLOWED_TAG_SET = new Set<string>(SF_PURIFY_CONFIG.ALLOWED_TAGS)
const ALLOWED_ATTR_SET = new Set<string>(SF_PURIFY_CONFIG.ALLOWED_ATTR)
const FORBID_ATTR_SET = new Set<string>(SF_PURIFY_CONFIG.FORBID_ATTR)

// 这些容器一旦出现，整棵子树都不属于正文：script/style 在解析器里是原始文本，
// template 的子节点挂在 content 上，其余是表单与嵌入对象。
// 维护提示：往 ALLOWED_TAGS 加 void 元素要同步 VOID_TAGS；往 FORBID_TAGS 加
// 「内容本身不算正文」的标签要同步这里，否则 SSR 会漏掉子树（客户端 DOMPurify
// 对 form/object 走 KEEP_CONTENT，会保留子节点——这是有意的严格化，不是等价）。
// 导出仅用于测试断言「必须覆盖全部 FORBID_TAGS」，防止两处静默漂移。
export const SF_SERVER_DROP_SUBTREE_TAGS: readonly string[] = [
  ...SF_PURIFY_CONFIG.FORBID_TAGS,
  'template', 'noscript', 'title', 'noembed', 'noframes', 'plaintext', 'xmp'
]
const DROP_SUBTREE_TAGS = new Set<string>(SF_SERVER_DROP_SUBTREE_TAGS)

// 以下四个集合是 parse5 / URL 语义的伴生硬编码，不在 SF_PURIFY_CONFIG 里：
// 改白名单（新增 void 元素、新增可携带 URL 的属性）时必须一起检查。
const VOID_TAGS = new Set<string>(['br', 'hr', 'img', 'input', 'col'])
const URL_ATTRS = new Set<string>(['href', 'src'])
// 只有 img 允许 data: URI（DOMPurify 默认的 DATA_URI_TAGS 语义）。
const DATA_URI_TAGS = new Set<string>(['img'])
const SAFE_URL_SCHEMES = new Set<string>(['http:', 'https:', 'mailto:', 'tel:', 'ftp:', 'ftps:'])

/**
 * 净化 HTML 字符串。client 端用 DOMPurify，SSR 端用同构的白名单序列化。
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
 * 白名单条目与 SF_PURIFY_CONFIG 同源，但净化强度严格不低于客户端：
 * - 禁止容器的整棵子树丢弃（DOMPurify 对 form/object 走 KEEP_CONTENT，保留子节点）；
 * - 属性只认 ALLOWED_ATTR（DOMPurify 默认还放行任意 data-* / aria-*）；
 * - URL scheme 只放行 6 种（DOMPurify 还放行 xmpp:/sms:/callto:/cid:）；
 * - 不做 SANITIZE_DOM 的 id 去混淆（客户端会剥掉与 document 属性同名的 id）。
 * 所以首屏与水合后的结果可能不同：SSR 更严，客户端随后补回一部分。
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
  // 禁止容器已在上面整棵丢弃，这是 SSR 比客户端更严的地方）。
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
    // 事件处理器按前缀整类拒绝，其余只认白名单与 FORBID_ATTR。
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
