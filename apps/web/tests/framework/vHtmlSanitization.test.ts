import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { stripHtmlComments } from '../helpers/sourceText'

/**
 * `v-html` 净化的常驻棘轮（P3-12 的收口点）。
 *
 * 缺陷形态：`v-html` 直接渲染后端下发的 HTML，前端没有二次净化，唯一防线是服务端
 * `markdown.RenderSafe`（bluemonday 白名单）。后端一旦回归，就是全站存储型 XSS。
 *
 * 判定顺序（每处绑定必须至少命中一条）：
 * 1. **inline**：表达式自己就是 `sanitizeHtml(...)`；
 * 2. **derived**：表达式是本地 accessor `foo(item)`，且同文件里 `foo` 返回
 *    `CACHE.value.get(`、`CACHE = computed(...)` 体内**真的调了** `sanitizeHtml(取值)`；
 * 3. **exception**：显式白名单，每条必须带理由 + **结构化证据**（见 `checkException`）。
 *
 * 为什么证据必须结构化：上一版把「已净化」和「合法绕过」塞进同一张白名单，证据是裸
 * token（`'sanitizeHtml('`），跑在**未剥注释**的原文上，结果
 * - 删掉净化、只留一句提到 `sanitizeHtml(` 的注释 → 仍绿；
 * - 新增 `v-html="rawHtml"` 再补一条 `{ proof: 'const ' }` → 仍绿（任何 .vue 都有 `const `）。
 * 所以：所有证据都跑剥注释后的源码，且 file 域证据必须同时点名「被渲染的标识符」与
 * 「白名单渲染器调用点」；props 域证据逐个宿主标签断言。校验逻辑抽成纯函数并自带
 * 反向自测，绕过路径本身也被覆盖。
 *
 * 扫描口径：`app/**` 下的 `.vue`，只认 `v-html=` 绑定，先剥 HTML 注释。
 */

const APP_ROOT = fileURLToPath(new URL('../../app/', import.meta.url))

/** `v-html="..."` / `v-html='...'` / 无引号写法；值里允许换行。 */
const V_HTML_PATTERN = /\bv-html\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))/g

/** 表达式自己就是净化调用（容许 `sanitizeHtml (` 这种空格写法）。 */
const INLINE_SANITIZED = /^\s*sanitizeHtml\s*\(/

/** 本地 accessor 形态：`foo(item)`。 */
const ACCESSOR_CALL = /^([A-Za-z_$][\w$]*)\(\s*[A-Za-z_$][\w$]*\s*\)$/

/** 白名单渲染器：证据必须指向其中之一，别的函数名不算数。 */
const SAFE_RENDERERS = ['sanitizeHtml', 'renderLegalMarkdown'] as const
/** 证据里的调用点允许写成正则源码形态（`sanitizeHtml\(`），所以括号前的转义是可选的。 */
const RENDERER_CALL = new RegExp(`(?:${SAFE_RENDERERS.join('|')})\\\\?\\(`)

type Binding = {
  /** `app/...` 形式的相对路径，报错时直接可点。 */
  file: string
  expression: string
}

type ExceptionProof =
  | { proof: RegExp }
  | { callers: { component: string, requires: RegExp } }

type Exception = {
  file: string
  expression: string
  reason: string
} & ExceptionProof

type ExceptionCheck = { ok: boolean, detail: string }

/**
 * 只保留两处**结构性**真例外：它们既不走 `sanitizeHtml`，也不是本地 accessor。
 * 公告两处（P3-12）不在这里——它们走净化，必须由 derived 规则接住，否则就是
 * 「用白名单把已修复的回归又放回去」。
 */
const EXCEPTIONS: Exception[] = [
  {
    file: 'app/components/legal/SFLegalDocumentPage.vue',
    expression: 'body',
    reason: 'body 由 renderLegalMarkdown 产出：marked 渲染后走 parse5 白名单序列化，'
      + '剥离原始 HTML 与危险 URL，且输入是站内法律文案（legalBody），不是用户内容。',
    proof: /const body = computed\(\(\) => renderLegalMarkdown\(/
  },
  {
    file: 'app/components/forum/SFMentionContent.vue',
    expression: 'html',
    reason: '组件只做 @提及 链接化与外链装饰，html 是 props；净化责任在宿主，'
      + '每个宿主调用点都必须传 sanitizeHtml(...) 的结果（逐个标签断言，缺一个就红）。',
    callers: { component: 'SFMentionContent', requires: /sanitizeHtml\(/ }
  }
]

function vueFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = `${dir}${name}`
      if (statSync(full).isDirectory()) walk(`${full}/`)
      else if (name.endsWith('.vue')) out.push(full)
    }
  }
  walk(APP_ROOT)
  return out.sort()
}

function appRelative(full: string): string {
  return `app/${full.slice(APP_ROOT.length)}`
}

/**
 * 证据一律跑在「没有注释的代码」上：HTML 注释整段删除，`/* *\/` 块注释替换成空格，
 * `//` 行注释连同行尾一起剪掉（`://` 的 URL 不误伤）。
 * 注释不能单独充当净化证据——包括贴在代码行尾的那种。
 */
function stripComments(source: string): string {
  return stripHtmlComments(source)
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .split('\n')
    .map(line => line.replace(/(^|[^:])\/\/.*$/, '$1'))
    .join('\n')
}

/** 从 `{` 开始做花括号配平，取回整段代码块；配平失败返回 null（fail-closed）。 */
function balancedBody(source: string, marker: string): string | null {
  const start = source.indexOf(marker)
  if (start === -1) {
    return null
  }
  const open = source.indexOf('{', start)
  if (open === -1) {
    return null
  }
  let depth = 0
  for (let cursor = open; cursor < source.length; cursor += 1) {
    const char = source[cursor]
    if (char === '{') {
      depth += 1
    } else if (char === '}') {
      depth -= 1
      if (depth === 0) {
        return source.slice(open, cursor + 1)
      }
    }
  }
  return null
}

/** 纯函数版扫描：注释已剥，返回每处绑定的表达式。 */
export function findVHtmlBindings(source: string): string[] {
  const expressions: string[] = []
  for (const match of stripHtmlComments(source).matchAll(V_HTML_PATTERN)) {
    expressions.push((match[1] ?? match[2] ?? match[3] ?? '').trim())
  }
  return expressions
}

/**
 * derived 规则：`v-html="foo(item)"` 只有在同文件里能追到净化链时才成立——
 * `function foo(...) { return CACHE.value.get(...) }` +
 * `const CACHE = computed(() => { ... .set(..., sanitizeHtml(<取值>)) ... })`。
 * 只认「拿真实取值函数净化」，`sanitizeHtml('字面量')` 不算。
 */
export function isSanitizedDerivedAccessor(source: string, expression: string): boolean {
  const call = ACCESSOR_CALL.exec(expression)
  if (!call) {
    return false
  }
  const code = stripComments(source)

  const accessorBody = balancedBody(code, `function ${call[1]}(`)
  if (!accessorBody) {
    return false
  }
  const read = /return\s+([A-Za-z_$][\w$]*)\.value\.get\(/.exec(accessorBody)
  if (!read) {
    return false
  }

  const cacheBody = balancedBody(code, `const ${read[1]} = computed(`)
  if (!cacheBody) {
    return false
  }
  if (!/\.set\(/.test(cacheBody)) {
    return false
  }
  const sanitize = /sanitizeHtml\(\s*([^)]*)\)/.exec(cacheBody)
  if (!sanitize) {
    return false
  }
  // `sanitizeHtml('...')` 只是把字面量过一遍，等于没净化。
  return !/^\s*['"`]/.test(sanitize[1] ?? '')
}

/** 从被渲染表达式里取出「真正被渲染的标识符」，用于要求证据点名它。 */
function renderedIdentifier(expression: string): string {
  const call = /^([A-Za-z_$][\w$]*)\(/.exec(expression)
  if (call) {
    return call[1]!
  }
  return /^[A-Za-z_$][\w$]*$/.test(expression) ? expression : ''
}

/**
 * 校验一条白名单是否成立（导出给自测：合成弱证据必须不成立）。
 * 成立条件：文件存在 → 绑定仍存在（死条目要红）→ 理由够长 →
 * file 域证据同时点名被渲染标识符与白名单渲染器调用点且真的命中；
 * 或 props 域证据在每个宿主标签上都命中。
 */
export function checkException(input: unknown, sources: Map<string, string>): ExceptionCheck {
  const fail = (detail: string): ExceptionCheck => ({ ok: false, detail })
  if (!input || typeof input !== 'object') {
    return fail('白名单条目必须是对象')
  }

  const entry = input as Record<string, unknown>
  const file = entry.file
  const expression = entry.expression
  const reason = entry.reason
  if (typeof file !== 'string' || typeof expression !== 'string' || typeof reason !== 'string') {
    return fail('file / expression / reason 必须是字符串')
  }
  if (reason.trim().length < 20) {
    return fail(`${file}: 理由太短，写不清「为什么这处 HTML 天生可信」`)
  }

  const raw = sources.get(file)
  if (raw === undefined) {
    return fail(`${file}: 白名单指向的文件不存在`)
  }
  const source = stripComments(raw)
  if (!findVHtmlBindings(source).includes(expression)) {
    return fail(`${file}: 白名单里的 v-html="${expression}" 已不存在（死条目）`)
  }

  const callers = entry.callers
  if (callers !== undefined) {
    if (!callers || typeof callers !== 'object') {
      return fail(`${file}: callers 必须是 { component, requires }`)
    }
    const { component, requires } = callers as Record<string, unknown>
    if (typeof component !== 'string' || !/^[A-Za-z_$][\w$]*$/.test(component)) {
      return fail(`${file}: callers.component 必须是组件名`)
    }
    if (!(requires instanceof RegExp)) {
      return fail(`${file}: callers.requires 必须是 RegExp`)
    }
    if (!RENDERER_CALL.test(requires.source)) {
      return fail(`${file}: callers.requires 必须指向白名单渲染器（${SAFE_RENDERERS.join(' / ')}）调用`)
    }
    const tagPattern = new RegExp(`<(?:Lazy)?${component}\\b[^>]*>`, 'g')
    const tags = [...sources].flatMap(([tagFile, content]) =>
      [...stripComments(content).matchAll(tagPattern)]
        .map(match => ({ file: tagFile, tag: match[0].replace(/\s+/g, ' ') }))
    )
    if (tags.length === 0) {
      return fail(`${file}: 全仓找不到任何 <${component}> 宿主标签，props 型例外无从复核`)
    }
    const unsanitized = tags.filter(tag => !requires.test(tag.tag))
    if (unsanitized.length > 0) {
      return fail(`${file}: ${unsanitized.length} 个 <${component}> 宿主没有传净化结果，例如 ${unsanitized[0]!.file} :: ${unsanitized[0]!.tag}`)
    }
    return { ok: true, detail: '' }
  }

  const proof = entry.proof
  if (!(proof instanceof RegExp)) {
    return fail(`${file}: proof 必须是 RegExp；裸字符串 token 与被渲染值没有绑定，不算证据`)
  }
  const identifier = renderedIdentifier(expression)
  if (!identifier) {
    return fail(`${file}: 无法从表达式 "${expression}" 取出被渲染的标识符`)
  }
  if (!proof.source.includes(identifier)) {
    return fail(`${file}: proof 没有点名被渲染的 ${identifier}，证据与表达式无绑定`)
  }
  if (!RENDERER_CALL.test(proof.source)) {
    return fail(`${file}: proof 必须指向白名单渲染器（${SAFE_RENDERERS.join(' / ')}）的调用点`)
  }
  if (!proof.test(source)) {
    return fail(`${file}: proof 未在当前源码中命中`)
  }
  return { ok: true, detail: '' }
}

const files = vueFiles()
const sources = new Map(files.map(file => [appRelative(file), readFileSync(file, 'utf8')]))

const bindings: Binding[] = []
for (const [file, source] of sources) {
  for (const expression of findVHtmlBindings(source)) {
    bindings.push({ file, expression })
  }
}

type Verdict = 'inline' | 'derived' | 'exception' | 'offender'

function classify(binding: Binding): Verdict {
  if (INLINE_SANITIZED.test(binding.expression)) {
    return 'inline'
  }
  if (isSanitizedDerivedAccessor(sources.get(binding.file) ?? '', binding.expression)) {
    return 'derived'
  }
  const exception = EXCEPTIONS.find(item => item.file === binding.file && item.expression === binding.expression)
  if (exception && checkException(exception, sources).ok) {
    return 'exception'
  }
  return 'offender'
}

describe('v-html 二次净化棘轮', () => {
  test('扫描器能识别绑定，也不会被注释或非 v-html 文本骗过', () => {
    expect(findVHtmlBindings('<div v-html="item.rawHtml" />')).toEqual(['item.rawHtml'])
    expect(findVHtmlBindings("<div v-html='item.rawHtml' />")).toEqual(['item.rawHtml'])
    expect(findVHtmlBindings('<div v-html="sanitizeHtml(item.rawHtml)" />')).toEqual(['sanitizeHtml(item.rawHtml)'])
    expect(findVHtmlBindings('<!-- <div v-html="legacy" /> -->')).toEqual([])
    expect(findVHtmlBindings('// 永远不 v-html 渲染用户正文')).toEqual([])
  })

  test('derived 规则：能追到净化链才成立，注释与字面量都不算', () => {
    const source = [
      'const sanitizedBodyHTML = computed(() => {',
      '  const result = new Map<number, string>()',
      '  for (const item of list.value) {',
      '    result.set(item.id, sanitizeHtml(bodyHTMLOf(item)))',
      '  }',
      '  return result',
      '})',
      '',
      'function sanitizedBodyHTMLOf(item: SiteAnnouncement) {',
      "  return sanitizedBodyHTML.value.get(item.id) || ''",
      '}'
    ].join('\n')

    expect(isSanitizedDerivedAccessor(source, 'sanitizedBodyHTMLOf(item)')).toBe(true)
    // 去掉净化调用 → 不成立
    expect(isSanitizedDerivedAccessor(source.replace('sanitizeHtml(bodyHTMLOf(item))', 'bodyHTMLOf(item)'), 'sanitizedBodyHTMLOf(item)')).toBe(false)
    // 只留一句提到 sanitizeHtml( 的注释 → 不成立
    expect(isSanitizedDerivedAccessor(source.replace('sanitizeHtml(bodyHTMLOf(item))', 'bodyHTMLOf(item) // sanitizeHtml('), 'sanitizedBodyHTMLOf(item)')).toBe(false)
    expect(isSanitizedDerivedAccessor(source.replace('sanitizeHtml(bodyHTMLOf(item))', 'bodyHTMLOf(item) /* sanitizeHtml( */'), 'sanitizedBodyHTMLOf(item)')).toBe(false)
    // 只净化字面量 → 不成立
    expect(isSanitizedDerivedAccessor(source.replace('sanitizeHtml(bodyHTMLOf(item))', "sanitizeHtml('')"), 'sanitizedBodyHTMLOf(item)')).toBe(false)
    // accessor 不读缓存 / 不是 accessor 形态 → 不成立
    expect(isSanitizedDerivedAccessor(source, 'rawHtml')).toBe(false)
    expect(isSanitizedDerivedAccessor(source, 'item.rawHtml')).toBe(false)
  })

  test('白名单校验：弱证据必须不成立（绕过路径本身被覆盖）', () => {
    const probe = [
      '<script setup lang="ts">',
      'const rawHtml = computed(() => renderLegalMarkdown(source))',
      '</script>',
      '<template><div v-html="rawHtml" /></template>'
    ].join('\n')
    const fake = new Map([['app/components/fake/SFProbe.vue', probe]])
    const base = { file: 'app/components/fake/SFProbe.vue', expression: 'rawHtml', reason: '一条足够长的理由，用来通过长度门槛检查。' }

    // 裸字符串 token：任何 .vue 都有 `const `，绝不能算证据
    expect(checkException({ ...base, proof: 'const ' }, fake).ok).toBe(false)
    // RegExp 但没有点名被渲染值，也没有白名单渲染器
    expect(checkException({ ...base, proof: /const / }, fake).ok).toBe(false)
    // 点名了渲染器，却没点名被渲染的 rawHtml
    expect(checkException({ ...base, proof: /sanitizeHtml\(/ }, fake).ok).toBe(false)
    // 理由太短
    expect(checkException({ ...base, reason: '短', proof: /const rawHtml = computed\(\(\) => renderLegalMarkdown\(/ }, fake).ok).toBe(false)
    // 指向不存在的文件 / 死条目
    expect(checkException({ ...base, file: 'app/components/fake/Nope.vue', proof: /const rawHtml = computed\(\(\) => renderLegalMarkdown\(/ }, fake).ok).toBe(false)
    expect(checkException({ ...base, expression: 'gone', proof: /const rawHtml = computed\(\(\) => renderLegalMarkdown\(/ }, fake).ok).toBe(false)
    // 结构性成立：点名 rawHtml + 白名单渲染器调用点，且真的命中
    expect(checkException({ ...base, proof: /const rawHtml = computed\(\(\) => renderLegalMarkdown\(/ }, fake)).toEqual({ ok: true, detail: '' })

    // props 域：宿主没传净化结果就不成立；传了才成立
    const mention = '<template><div v-html="html" /></template>'
    const weakHost = new Map([
      ['app/components/fake/SFMentionContent.vue', mention],
      ['app/components/fake/SFHost.vue', '<template><SFMentionContent :html="raw" /></template>']
    ])
    const strongHost = new Map([
      ['app/components/fake/SFMentionContent.vue', mention],
      ['app/components/fake/SFHost.vue', '<template><SFMentionContent :html="sanitizeHtml(raw)" /></template>']
    ])
    const callers = { component: 'SFMentionContent', requires: /sanitizeHtml\(/ }
    const mentionBase = { file: 'app/components/fake/SFMentionContent.vue', expression: 'html', reason: '一条足够长的理由，用来通过长度门槛检查。' }
    expect(checkException({ ...mentionBase, callers }, weakHost).ok).toBe(false)
    expect(checkException({ ...mentionBase, callers: { component: 'SFMentionContent', requires: /const / } }, strongHost).ok).toBe(false)
    expect(checkException({ ...mentionBase, callers }, strongHost)).toEqual({ ok: true, detail: '' })
  })

  test('扫描覆盖到已知的 v-html 面（防止扫描口径静默失效）', () => {
    const scanned = new Set(bindings.map(binding => binding.file))
    for (const file of [
      'app/components/SFEditor.vue',
      'app/components/moderation/ModerationContextPanel.vue',
      'app/components/moderation/ModerationReviewReader.vue',
      'app/components/forum/SFMentionContent.vue',
      'app/components/legal/SFLegalDocumentPage.vue',
      'app/components/notifications/SFAnnouncementBanner.vue',
      'app/components/admin/settings/personalization/tabs/SFAdminPersonalizationAnnouncementsTab.vue',
      'app/pages/admin/forum/content.vue'
    ]) {
      expect(scanned.has(file)).toBe(true)
    }
  })

  test('每处 v-html 要么走 sanitizeHtml，要么是可追到净化链的 accessor，要么在成立的白名单里', () => {
    const offenders = bindings
      .filter(binding => classify(binding) === 'offender')
      .map(binding => `${binding.file} :: v-html="${binding.expression}"`)
    expect(offenders).toEqual([])
  })

  test('白名单只保留结构性真例外，且每条都成立', () => {
    expect(EXCEPTIONS.length).toBe(2)
    for (const exception of EXCEPTIONS) {
      expect(checkException(exception, sources)).toEqual({ ok: true, detail: '' })
    }
  })

  test('公告两处 v-html（P3-12）必须由净化链接住，不许退回白名单或原始值', () => {
    for (const file of [
      'app/components/notifications/SFAnnouncementBanner.vue',
      'app/components/admin/settings/personalization/tabs/SFAdminPersonalizationAnnouncementsTab.vue'
    ]) {
      const fileBindings = bindings.filter(binding => binding.file === file)
      expect(fileBindings.length).toBe(1)
      expect(classify(fileBindings[0]!)).toBe('derived')
      expect(EXCEPTIONS.some(exception => exception.file === file)).toBe(false)
    }
  })
})
