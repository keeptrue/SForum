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
 * 所以断言分四层，缺一层就有假绿：
 * 1. 扫描器必须真的扫到绑定——空结果不能算通过（`v-html` 改个写法就静默失效）；
 * 2. 每处 `v-html` 要么在表达式里调 `sanitizeHtml(...)`，要么命中显式白名单；
 * 3. 白名单每条都必须写明理由，且理由要能被证据复核：本文件（或全部调用方标签）
 *    里真的存在净化调用 / 白名单渲染实现，否则就是死条目，必须红；
 * 4. 白名单里的 props 消费者，逐个宿主标签断言传进来的就是净化结果。
 *
 * 扫描口径：`app/**` 下的 `.vue`。只认 `v-html=` 绑定（`// v-html 渲染` 这类注释
 * 不匹配），并先剥掉 HTML 注释，避免注释里的示例被当成真实绑定。
 */

const APP_ROOT = fileURLToPath(new URL('../../app/', import.meta.url))

/** `v-html="..."` / `v-html='...'` / 无引号写法；值里允许换行。 */
const V_HTML_PATTERN = /\bv-html\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>"']+))/g

/** 表达式自己就是净化调用（容许 `sanitizeHtml (` 这种空格写法）。 */
const INLINE_SANITIZED = /^\s*sanitizeHtml\s*\(/

type Binding = {
  /** `app/...` 形式的相对路径，报错时直接可点。 */
  file: string
  expression: string
}

type Exception = {
  file: string
  expression: string
  reason: string
  /** 复核证据：必须在本文件里出现（或 `proofScope: 'callers'` 时出现在每个宿主标签里）。 */
  proof: string
  proofScope?: 'file' | 'callers'
  /** `proofScope: 'callers'` 时被逐个标签检查的组件名。 */
  component?: string
}

/**
 * 白名单不是免死金牌：每条都要写清「为什么这处 HTML 天生可信」，
 * 并且 `proof` 必须能在代码里找到——理由被推翻时测试会先红。
 */
const EXCEPTIONS: Exception[] = [
  {
    file: 'app/components/legal/SFLegalDocumentPage.vue',
    expression: 'body',
    reason: 'body 由 renderLegalMarkdown 产出：marked 渲染后走 parse5 白名单序列化，'
      + '剥离原始 HTML 与危险 URL，且输入是站内法律文案（legalBody），不是用户内容。',
    proof: 'renderLegalMarkdown('
  },
  {
    file: 'app/components/forum/SFMentionContent.vue',
    expression: 'html',
    reason: '组件只做 @提及 链接化与外链装饰，html 是 props；净化责任在宿主，'
      + '每个宿主调用点都必须传 sanitizeHtml(...) 的结果（本测试逐个标签断言）。',
    proof: 'sanitizeHtml(',
    proofScope: 'callers',
    component: 'SFMentionContent'
  },
  {
    file: 'app/components/notifications/SFAnnouncementBanner.vue',
    expression: 'sanitizedBodyHTMLOf(item)',
    reason: '同文件 sanitizedBodyHTML computed 对每条公告调 sanitizeHtml 后按 id 缓存，'
      + '模板只读缓存结果（避免 v-if 与 v-html 各解析一遍）。',
    proof: 'sanitizeHtml('
  },
  {
    file: 'app/components/admin/settings/personalization/tabs/SFAdminPersonalizationAnnouncementsTab.vue',
    expression: 'sanitizedBodyHTMLOf(item)',
    reason: '同文件 sanitizedBodyHTML computed 对每条公告调 sanitizeHtml 后按 id 缓存，'
      + '管理端预览与前台公告共用同一套净化口径。',
    proof: 'sanitizeHtml('
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

/** 纯函数版扫描：注释已剥，返回每处绑定的表达式。 */
export function findVHtmlBindings(source: string): string[] {
  const expressions: string[] = []
  for (const match of stripHtmlComments(source).matchAll(V_HTML_PATTERN)) {
    expressions.push((match[1] ?? match[2] ?? match[3] ?? '').trim())
  }
  return expressions
}

function isInlineSanitized(expression: string): boolean {
  return INLINE_SANITIZED.test(expression)
}

const files = vueFiles()
const sources = new Map(files.map(file => [appRelative(file), readFileSync(file, 'utf8')]))

const bindings: Binding[] = []
for (const [file, source] of sources) {
  for (const expression of findVHtmlBindings(source)) {
    bindings.push({ file, expression })
  }
}

function exceptionFor(binding: Binding): Exception | undefined {
  return EXCEPTIONS.find(item => item.file === binding.file && item.expression === binding.expression)
}

describe('v-html 二次净化棘轮', () => {
  test('扫描器能识别绑定，也不会被注释或非 v-html 文本骗过', () => {
    expect(findVHtmlBindings('<div v-html="item.rawHtml" />')).toEqual(['item.rawHtml'])
    expect(findVHtmlBindings("<div v-html='item.rawHtml' />")).toEqual(['item.rawHtml'])
    expect(findVHtmlBindings('<div v-html="sanitizeHtml(item.rawHtml)" />')).toEqual(['sanitizeHtml(item.rawHtml)'])
    // 注释里的示例不算绑定；`v-html` 出现在普通注释文字里也不算。
    expect(findVHtmlBindings('<!-- <div v-html="legacy" /> -->')).toEqual([])
    expect(findVHtmlBindings('// 永远不 v-html 渲染用户正文')).toEqual([])
    expect(isInlineSanitized('item.rawHtml')).toBe(false)
    expect(isInlineSanitized('sanitizeHtml(item.rawHtml)')).toBe(true)
    expect(isInlineSanitized('sanitizeHtml (item.rawHtml)')).toBe(true)
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
    expect(bindings.length).toBeGreaterThanOrEqual(8)
  })

  test('每处 v-html 要么走 sanitizeHtml，要么在显式白名单里', () => {
    const offenders = bindings
      .filter(binding => !isInlineSanitized(binding.expression) && !exceptionFor(binding))
      .map(binding => `${binding.file} :: v-html="${binding.expression}"`)
    expect(offenders).toEqual([])
  })

  test('白名单每条都带理由，且证据仍在（无死条目）', () => {
    for (const exception of EXCEPTIONS) {
      expect(exception.reason.trim().length).toBeGreaterThan(20)
      expect(exception.proof.length).toBeGreaterThan(0)

      const source = sources.get(exception.file)
      expect(source).toBeDefined()
      expect(findVHtmlBindings(source!)).toContain(exception.expression)

      if (exception.proofScope === 'callers') {
        const tags = [...sources].flatMap(([file, content]) =>
          [...content.matchAll(new RegExp(`<(?:Lazy)?${exception.component}\\b[^>]*>`, 'g'))]
            .map(match => `${file} :: ${match[0].replace(/\s+/g, ' ')}`)
        )
        expect(tags.length).toBeGreaterThan(0)
        const unsanitized = tags.filter(tag => !tag.includes(exception.proof))
        expect(unsanitized).toEqual([])
      } else {
        expect(source!).toContain(exception.proof)
      }
    }
  })

  test('公告两处 v-html（P3-12）绑定的必须是净化后的缓存值', () => {
    for (const file of [
      'app/components/notifications/SFAnnouncementBanner.vue',
      'app/components/admin/settings/personalization/tabs/SFAdminPersonalizationAnnouncementsTab.vue'
    ]) {
      const source = sources.get(file)!
      // 净化发生在 computed 里：拿原始值 → sanitizeHtml → 按 id 缓存。
      expect(source).toMatch(/const sanitizedBodyHTML = computed\(\(\) => \{[\s\S]*?sanitizeHtml\(/)
      // 模板只读缓存结果。
      expect(source).toContain('v-html="sanitizedBodyHTMLOf(item)"')
      // 原始 HTML 直出必须彻底消失（防止回退）。
      expect(source).not.toContain('v-html="bodyHTMLOf(item)"')
      expect(source).not.toContain('v-html="bodyHTML(item)"')
    }
  })
})
