/**
 * 搜索命中高亮：把文本切成 `{ text, hit }` 分段，交给模板渲染 `<mark>`。
 *
 * 为什么返回分段而不是 HTML 字符串：调用方要么 `v-html`（XSS 面 + 无法约束标签），
 * 要么自己拼字符串（同样危险）。分段渲染让 Vue 只输出 `<mark>`/`<span>` 两种元素，
 * 文本始终走插值，天然转义。
 */

export type HighlightSegment = { text: string, hit: boolean }

/**
 * 停用词：只有停用词的查询不做高亮（否则搜「的」会满屏 `<mark>`）。
 * 中英各一小份，够覆盖论坛标题的常见虚词即可，不要做成搜索引擎级别的词表。
 */
const STOP_WORDS = new Set([
  '的', '了', '和', '是', '在', '也', '就', '都', '而', '及', '与', '着', '过', '吗', '呢', '吧',
  'the', 'a', 'an', 'of', 'and', 'or', 'to', 'in', 'on', 'for', 'is', 'are', 'be', 'with'
])

/** 查询分词：按空白切分、小写、去停用词、去重。 */
export function tokenizeQuery(query: string): string[] {
  const seen = new Set<string>()
  const terms: string[] = []
  for (const raw of String(query || '').trim().toLowerCase().split(/\s+/)) {
    if (!raw || STOP_WORDS.has(raw) || seen.has(raw)) {
      continue
    }
    seen.add(raw)
    terms.push(raw)
  }
  return terms
}

/**
 * 把 `text` 按 `terms` 切成命中/未命中的连续段。
 * - 大小写不敏感；长词优先，避免短词先命中把长词切断。
 * - 无词、纯空白、只有停用词 → 返回单段 `hit: false`（原样输出）。
 */
export function highlightSegments(text: string, terms: readonly string[]): HighlightSegment[] {
  const source = String(text ?? '')
  if (!source) {
    return []
  }
  const usable = terms.filter(term => term.length > 0)
  if (!usable.length) {
    return [{ text: source, hit: false }]
  }

  const lower = source.toLowerCase()
  // 少数语言小写化会改变长度（如 'İ'），此时无法安全按索引映射回原文，退化为不高亮。
  if (lower.length !== source.length) {
    return [{ text: source, hit: false }]
  }

  const marks = new Array<boolean>(source.length).fill(false)
  for (const term of [...usable].sort((left, right) => right.length - left.length)) {
    let from = 0
    while (from <= lower.length - term.length) {
      const at = lower.indexOf(term, from)
      if (at === -1) {
        break
      }
      for (let i = at; i < at + term.length; i += 1) {
        marks[i] = true
      }
      from = at + term.length
    }
  }

  const segments: HighlightSegment[] = []
  let index = 0
  while (index < source.length) {
    const hit = marks[index] === true
    let end = index
    while (end < source.length && marks[end] === hit) {
      end += 1
    }
    segments.push({ text: source.slice(index, end), hit })
    index = end
  }
  return segments
}
