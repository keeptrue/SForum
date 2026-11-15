import { afterEach, describe, expect, test } from 'bun:test'
import { Editor } from '@tiptap/core'
import { HAN_PATTERN, LOCALES, lookupMessage, translate, type LocaleName } from '../helpers/locale'
import {
  createSFEditorExtensions,
  sforumEditorEmojiPresets,
  sforumEmojiItem
} from '../../app/utils/sfEditor'
import {
  SF_SHORTCODE_PROTECTED_FALLBACK_LABEL,
  sforumProtectedFallbackLabel,
  sforumShortcodeFallbackKeys,
  sforumShortcodeFallbackLabel
} from '../../app/utils/editor/shortcodes'

/**
 * utils 层 i18n 回归（RV-28 扩口径）。
 *
 * `utils/editor/shortcodes.ts` 与 `utils/sfEditor.ts` 里的文案会进编辑器与正文：
 * 短代码兜底卡片（`renderHTML` / `renderText`）与表情节点的 `title` / `data-label`。
 * 它们在模块级常量里，**不能**调 `t()`——那会把首次加载的语言冻进闭包。所以本轮
 * 改成「常量只存键，渲染时解析」，这里钉住三件事：
 *
 * 1. 键在两侧都存在且英文是真英文（缺键时翻译器会抛错，不是静默回显）；
 * 2. 注入翻译器后渲染结果跟随语言（默认路径不许回到写死的中文）；
 * 3. 不注入翻译器时仍退到声明的最后字面量，纯工具调用不会渲染出空文案。
 */

const translateFor = (locale: LocaleName) => (key: string) => translate(locale, key)

let editor: Editor | undefined

afterEach(() => {
  editor?.destroy()
  editor = undefined
})

function openEditor(translateFn?: (key: string) => string) {
  return new Editor({
    extensions: createSFEditorExtensions({
      placeholder: 'body',
      maxCharacters: 10000,
      ...(translateFn ? { translate: translateFn } : {})
    }),
    content: ''
  })
}

const SHORTCODE_MARKDOWN = [
  '[topic topic_id="12"][/topic]',
  '',
  '[user user_id="34"][/user]',
  '',
  '[reply]',
  'secret body',
  '[/reply]'
].join('\n')

function fallbackLabels(html: string) {
  return Array.from(
    html.matchAll(/<div class="sf-editor-fallback"[^>]*data-fallback="([^"]+)"[^>]*>([^<]*)<\/div>/g),
    match => ({ code: match[1], label: match[2] })
  )
}

describe('shortcode fallback copy', () => {
  test('every rendered fallback key exists in both locales and the English copy is English', () => {
    const keys = sforumShortcodeFallbackKeys()
    // 反真空绿：声明整族被删空时下面两个循环会静默通过。
    expect(keys).toEqual([
      'composer.shortcodes.fallback.category',
      'composer.shortcodes.fallback.comment',
      'composer.shortcodes.fallback.protected',
      'composer.shortcodes.fallback.topic',
      'composer.shortcodes.fallback.user'
    ])
    for (const key of keys) {
      for (const locale of LOCALES) {
        expect({ key, locale, value: typeof lookupMessage(locale, key) }).toEqual({ key, locale, value: 'string' })
      }
      expect({ key, han: HAN_PATTERN.test(lookupMessage('en-US', key) || '') }).toEqual({ key, han: false })
    }
  })

  test('the resolver follows the injected translator and keeps the literal only as last resort', () => {
    const en = translateFor('en-US')
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.topic', en)).toBe('Topic reference unavailable')
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.topic', translateFor('zh-CN'))).toBe('主题引用暂不可用')
    // 没有翻译器（纯工具调用、扩展在 Vue 之外被创建）才用声明里的字面量。
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.topic')).toBe('主题引用暂不可用')
    // friend-links 静默消失：任何语言都不产生兜底文案。
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.friend-links', en)).toBe('')
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.unknown', en)).toBe('')
    expect(sforumProtectedFallbackLabel(en)).toBe('Protected content unavailable')
    expect(sforumProtectedFallbackLabel()).toBe(SF_SHORTCODE_PROTECTED_FALLBACK_LABEL)
  })

  test('a translator that only echoes the key degrades to the literal, never to a raw key', () => {
    expect(sforumShortcodeFallbackLabel('sforum-shortcodes.topic', key => key)).toBe('主题引用暂不可用')
    expect(sforumProtectedFallbackLabel(key => key)).toBe(SF_SHORTCODE_PROTECTED_FALLBACK_LABEL)
  })

  test('editor HTML and text fallbacks are rendered in the active language', () => {
    for (const locale of LOCALES) {
      editor = new Editor({
        extensions: createSFEditorExtensions({
          placeholder: 'body',
          maxCharacters: 10000,
          translate: translateFor(locale)
        }),
        content: SHORTCODE_MARKDOWN,
        contentType: 'markdown'
      })
      const html = editor.getHTML()
      const rendered = fallbackLabels(html)
      // `data-fallback` 是与插件/宿主共享的稳定契约，语言切换不许改它。
      expect(rendered.map(item => item.code)).toEqual([
        'shortcode.topic.unavailable',
        'shortcode.user.unavailable',
        'shortcode.protected.unavailable'
      ])
      expect(rendered.map(item => item.label)).toEqual([
        translate(locale, 'composer.shortcodes.fallback.topic'),
        translate(locale, 'composer.shortcodes.fallback.user'),
        translate(locale, 'composer.shortcodes.fallback.protected')
      ])
      const allLabels = rendered.map(item => item.label).join('')
      expect({ locale, han: HAN_PATTERN.test(allLabels) }).toEqual({ locale, han: locale === 'zh-CN' })
      // 正文里的保护内容不许因为换语言漏进 HTML。
      expect(html).not.toContain('secret body')
    }
  })

  test('the plain-text export carries the same localized fallback', () => {
    for (const locale of LOCALES) {
      editor = openEditor(translateFor(locale))
      editor.commands.setContent(SHORTCODE_MARKDOWN, { contentType: 'markdown' })
      const text = editor.getText()
      expect(text).toContain(translate(locale, 'composer.shortcodes.fallback.topic'))
      expect({ locale, han: HAN_PATTERN.test(text) }).toEqual({ locale, han: locale === 'zh-CN' })
    }
  })
})

describe('emoji preset labels', () => {
  test('presets store copy keys, and both locales resolve them as real translations', () => {
    expect(sforumEditorEmojiPresets.map(item => item.name))
      .toEqual(['sparkles', 'clap', 'thinking', 'rocket', 'eyes', 'party'])
    for (const preset of sforumEditorEmojiPresets) {
      expect({ name: preset.name, key: preset.labelKey }).toEqual({
        name: preset.name,
        key: `composer.emoji.${preset.name}`
      })
      for (const locale of LOCALES) {
        const value = lookupMessage(locale, preset.labelKey)
        expect({ name: preset.name, locale, value: typeof value }).toEqual({ name: preset.name, locale, value: 'string' })
      }
      expect({ name: preset.name, han: HAN_PATTERN.test(lookupMessage('en-US', preset.labelKey) || '') })
        .toEqual({ name: preset.name, han: false })
    }
  })

  test('an inserted emoji carries the active language into title and data-label', () => {
    const preset = sforumEditorEmojiPresets[0]!
    for (const locale of LOCALES) {
      const translateFn = translateFor(locale)
      editor = openEditor(translateFn)
      editor.commands.insertSForumEmoji(sforumEmojiItem(preset, translateFn))
      const html = editor.getHTML()
      const label = translate(locale, preset.labelKey)
      expect(html).toContain(`title="${label}"`)
      expect(html).toContain(`data-label="${label}"`)
      expect({ locale, han: HAN_PATTERN.test(html) }).toEqual({ locale, han: locale === 'zh-CN' })
    }
  })

  test('a missing emoji translation falls back to the node name, not to a raw key', () => {
    editor = openEditor(key => key)
    editor.commands.insertSForumEmoji(sforumEmojiItem(sforumEditorEmojiPresets[0]!, key => key))
    const html = editor.getHTML()
    expect(html).toContain('data-label="sparkles"')
    expect(html).not.toContain('composer.emoji.sparkles')
  })
})
