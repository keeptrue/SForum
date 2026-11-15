import { describe, expect, test } from 'bun:test'
import { unlinkSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { defineComponent } from 'vue'
import { Editor, EditorContent, VueNodeViewRenderer } from '@tiptap/vue-3'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import { HAN_PATTERN, LOCALES, translate, type LocaleName, useI18nFor } from '../helpers/locale'
import { stripHtmlComments } from '../helpers/sourceText'
import {
  collectEditorAttachmentIds,
  createSFEditorExtensions,
  escapeHtml,
  isExternalEditorMarkdownUpdate,
  normalizeUserUrl
} from '../../app/utils/sfEditor'
import { imageFilesFromList } from '../../app/composables/editor/useEditorImageUpload'
import { insertSForumLink } from '../../app/utils/editor/link'
import {
  buildSForumProtectedTransaction,
  inspectSForumShortcodeMarkdown,
  prepareSForumShortcodeMarkdown,
  SF_SHORTCODE_BLOCK_NODE,
  SF_SHORTCODE_REF_NODE,
  sforumCanInsertReference,
  sforumProtectedNodeAttributes,
  sforumReferenceNodeAttributes
} from '../../app/utils/editor/shortcodes'
import { insertSForumTable } from '../../app/utils/editor/table'
import { sanitizeHtml } from '../../app/utils/sfSanitize'

/**
 * 前台共享组件的「渲染语义」回归（RV-28）。
 *
 * 审计口径的缺陷不是「源码里有中文」，而是**读屏名称与占位文案在生产里就是中文**：
 * `SFAlert` 的关闭按钮、`SFPagination` 的上/下一页、`SFSearch` 的占位与过滤组名、
 * `SFEditorToolbar` 的工具栏与按钮名。所以这里断言的是挂载后的 DOM：
 * 属性值、`placeholder`、可见文本，zh-CN 与 en-US 各挂一次。
 *
 * 只用真实 locale 做翻译器（`tests/helpers/locale.ts`），缺键会直接抛错 —— 这比
 * 「回显 key」的桩强：桩会让 `aria-label === key` 永远成立，替身替被断言的逻辑干活。
 */

let activeLocale: LocaleName = 'zh-CN'

const useI18nBinding = () => {
  const forLocale = useI18nFor(activeLocale)
  return forLocale()
}

const bindings = {
  computed: testVue.computed,
  ref: testVue.ref,
  watch: testVue.watch,
  useI18n: useI18nBinding
}

const Stub = { template: '<span />' }

interface MountOptions {
  props?: Record<string, unknown>
  stubs?: Record<string, unknown>
  directives?: Record<string, unknown>
  mocks?: Record<string, unknown>
}

function mountIn(locale: LocaleName, component: unknown, options: MountOptions = {}) {
  activeLocale = locale
  return mount(component as never, {
    ...(options.props ? { props: options.props } : {}),
    global: {
      stubs: {
        NuxtLink: { template: '<a><slot /></a>' },
        NuxtImg: { template: '<img />' },
        UIcon: true,
        SFAvatar: true,
        SFBadge: true,
        ...options.stubs
      },
      directives: options.directives ?? {},
      mocks: options.mocks ?? {}
    }
  } as never)
}

function expectNoHan(label: string, html: string) {
  // 模板注释不是渲染结果（`<!-- 上传头像由后端预处理 -->` 这类说明本来就用中文写）。
  const hits = stripHtmlComments(html).match(new RegExp(HAN_PATTERN.source, 'g'))
  expect({ label, hits: hits ?? [] }).toEqual({ label, hits: [] })
}

const alertPath = fileURLToPath(new URL('../../app/components/SFAlert.vue', import.meta.url))
const toastPath = fileURLToPath(new URL('../../app/components/SFToast.vue', import.meta.url))
const paginationPath = fileURLToPath(new URL('../../app/components/SFPagination.vue', import.meta.url))
const searchPath = fileURLToPath(new URL('../../app/components/SFSearch.vue', import.meta.url))
const tabsPath = fileURLToPath(new URL('../../app/components/SFTabs.vue', import.meta.url))
const feedRowPath = fileURLToPath(new URL('../../app/components/forum/SFFeedRow.vue', import.meta.url))
const toolbarPath = fileURLToPath(new URL('../../app/components/editor/SFEditorToolbar.vue', import.meta.url))
const avatarPath = fileURLToPath(new URL('../../app/components/SFAvatar.vue', import.meta.url))
const commentPath = fileURLToPath(new URL('../../app/components/forum/SFComment.vue', import.meta.url))

const SFAlert = await compileVueSfc(alertPath, 'sf-alert-i18n', bindings)
const SFToast = await compileVueSfc(toastPath, 'sf-toast-i18n', bindings)
const SFPagination = await compileVueSfc(paginationPath, 'sf-pagination-i18n', {
  ...bindings,
  buildPaginationWindow: (page: number, total: number) => [1, 2, 3, 4, 5].slice(0, Math.max(1, total))
})
const SFSearch = await compileVueSfc(searchPath, 'sf-search-i18n', bindings)
const SFTabs = await compileVueSfc(tabsPath, 'sf-tabs-i18n', bindings)
const SFFeedRow = await compileVueSfc(feedRowPath, 'sf-feed-row-i18n', bindings)
const SFEditorToolbar = await compileVueSfc(toolbarPath, 'sf-editor-toolbar-i18n', {
  ...bindings,
  SFEditorTablePicker: defineComponent({
    name: 'SFEditorTablePicker',
    props: { disabled: { type: Boolean, default: false } },
    template: '<button type="button" class="table-picker-stub" />'
  })
})
const SFAvatar = await compileVueSfc(avatarPath, 'sf-avatar-i18n', {
  ...bindings,
  forumAvatarToneClass: () => 'tone'
})

const editorPath = fileURLToPath(new URL('../../app/components/SFEditor.vue', import.meta.url))
// 真实 Tiptap + 真实工具函数：只有 i18n 与「不参与断言」的宿主能力用替身，
// 这样 placeholder / aria-label / 字数文案都经过产品代码本身。
const SFEditor = await compileVueSfc(editorPath, 'sf-editor-i18n', {
  ...bindings,
  shallowRef: testVue.shallowRef,
  reactive: testVue.reactive,
  onMounted: testVue.onMounted,
  onBeforeUnmount: testVue.onBeforeUnmount,
  nextTick: testVue.nextTick,
  useToast: () => ({ add: () => {} }),
  useMentionSuggestions: () => ({ fetchMentionSuggestions: async () => [] }),
  useEditorImageUpload: () => ({ pendingUploadCount: testVue.ref(0), uploadImages: async () => {} }),
  Editor,
  EditorContent,
  VueNodeViewRenderer,
  collectEditorAttachmentIds,
  createSFEditorExtensions,
  escapeHtml,
  isExternalEditorMarkdownUpdate,
  normalizeUserUrl,
  imageFilesFromList,
  insertSForumLink,
  insertSForumTable,
  buildSForumProtectedTransaction,
  inspectSForumShortcodeMarkdown,
  prepareSForumShortcodeMarkdown,
  SF_SHORTCODE_BLOCK_NODE,
  SF_SHORTCODE_REF_NODE,
  sforumCanInsertReference,
  sforumProtectedNodeAttributes,
  sforumReferenceNodeAttributes,
  SFEditorToolbar: Stub,
  SFEditorImageUploadModal: Stub,
  SFEditorImageMenu: Stub,
  SFEditorTableMenu: Stub,
  SFShortcodeReferenceDialog: Stub,
  SFShortcodeReferenceNodeView: Stub,
  SFShortcodeProtectedNodeView: Stub
})

describe('SFAlert close button name', () => {
  for (const locale of LOCALES) {
    test(`announces the dismiss action in ${locale}`, () => {
      activeLocale = locale
      const wrapper = mountIn(locale, SFAlert, { props: { title: 'Heads up', closable: true } })
      try {
        const close = wrapper.get('.sf-alert__close')
        expect(close.attributes('aria-label')).toBe(translate(locale, 'common.dismissAlert'))
        expect(close.attributes('title')).toBe(translate(locale, 'common.dismissAlert'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })
  }

  test('zh-CN 与 en-US 的关闭名称确实是两种语言', () => {
    activeLocale = 'zh-CN'
    const zh = mountIn('zh-CN', SFAlert, { props: { closable: true } })
    const en = mountIn('en-US', SFAlert, { props: { closable: true } })
    try {
      expect(zh.get('.sf-alert__close').attributes('aria-label')).toBe('关闭提示')
      expect(en.get('.sf-alert__close').attributes('aria-label')).toBe('Dismiss alert')
    } finally {
      zh.unmount()
      en.unmount()
    }
  })
})

describe('SFToast close button name', () => {
  test('renders the localized dismiss label, not a hardcoded string', () => {
    const zh = mountIn('zh-CN', SFToast, { props: { title: 'Saved', closable: true } })
    const en = mountIn('en-US', SFToast, { props: { title: 'Saved', closable: true } })
    try {
      expect(zh.get('.sf-toast__close').attributes('aria-label')).toBe('关闭通知')
      expect(en.get('.sf-toast__close').attributes('aria-label')).toBe('Dismiss notification')
      expectNoHan('en-US', en.html())
    } finally {
      zh.unmount()
      en.unmount()
    }
  })
})

describe('SFPagination control names', () => {
  for (const locale of LOCALES) {
    test(`labels the nav and both page controls in ${locale}`, () => {
      activeLocale = locale
      const wrapper = mountIn(locale, SFPagination, { props: { page: 2, totalPages: 5 } })
      try {
        expect(wrapper.get('nav').attributes('aria-label')).toBe(translate(locale, 'common.pagination.label'))
        const labels = wrapper.findAll('.sf-pagination__item').map(node => node.attributes('aria-label')).filter(Boolean)
        expect(labels).toContain(translate(locale, 'common.pagination.previous'))
        expect(labels).toContain(translate(locale, 'common.pagination.next'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })
  }

  test('crawler-visible links carry the same names as the buttons', () => {
    activeLocale = 'en-US'
    const wrapper = mountIn('en-US', SFPagination, {
      props: { page: 2, totalPages: 5, pageTo: (page: number) => `/topics?page=${page}` }
    })
    try {
      expect(wrapper.get('nav').attributes('aria-label')).toBe('Pagination')
      const links = wrapper.findAll('a').map(node => node.attributes('aria-label')).filter(Boolean)
      expect(links).toEqual(['Previous page', 'Next page'])
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFSearch placeholder and filter group name', () => {
  test('defaults the placeholder to locale copy', () => {
    const zh = mountIn('zh-CN', SFSearch, { props: { modelValue: '' } })
    const en = mountIn('en-US', SFSearch, { props: { modelValue: '' } })
    try {
      expect(zh.get('input[type="search"]').attributes('placeholder')).toBe('搜索主题、用户或标签')
      expect(zh.get('input[type="search"]').attributes('aria-label')).toBe('搜索主题、用户或标签')
      expect(en.get('input[type="search"]').attributes('placeholder')).toBe('Search topics, users, or tags')
      expect(en.get('button[type="button"]').attributes('aria-label')).toBe('Search topics, users, or tags')
      expectNoHan('en-US', en.html())
    } finally {
      zh.unmount()
      en.unmount()
    }
  })

  test('an explicit placeholder still wins over the locale default', () => {
    const wrapper = mountIn('en-US', SFSearch, { props: { modelValue: '', placeholder: 'Find a topic' } })
    try {
      expect(wrapper.get('input[type="search"]').attributes('placeholder')).toBe('Find a topic')
      expect(wrapper.get('input[type="search"]').attributes('aria-label')).toBe('Find a topic')
    } finally {
      wrapper.unmount()
    }
  })

  test('names the filter group in the active locale', () => {
    const filters = [{ label: 'Questions', value: 'questions' }]
    const zh = mountIn('zh-CN', SFSearch, { props: { modelValue: '', filters } })
    const en = mountIn('en-US', SFSearch, { props: { modelValue: '', filters } })
    try {
      expect(zh.get('.sf-search__filters').attributes('aria-label')).toBe('搜索过滤')
      expect(en.get('.sf-search__filters').attributes('aria-label')).toBe('Search filters')
      expect(en.text()).toContain('Questions')
    } finally {
      zh.unmount()
      en.unmount()
    }
  })
})

describe('SFTabs default tab list name', () => {
  test('falls back to locale copy and still honours an explicit ariaLabel', () => {
    const items = [{ label: 'Overview', value: 'overview' }]
    const zh = mountIn('zh-CN', SFTabs, { props: { items } })
    const en = mountIn('en-US', SFTabs, { props: { items } })
    const explicit = mountIn('en-US', SFTabs, { props: { items, ariaLabel: 'Topic sections' } })
    try {
      expect(zh.get('[role="tablist"]').attributes('aria-label')).toBe('分区切换')
      expect(en.get('[role="tablist"]').attributes('aria-label')).toBe('Section tabs')
      expect(explicit.get('[role="tablist"]').attributes('aria-label')).toBe('Topic sections')
    } finally {
      zh.unmount()
      en.unmount()
      explicit.unmount()
    }
  })
})

describe('SFFeedRow counters', () => {
  test('renders the table stats and the compact view count in the active locale', () => {
    const zhTable = mountIn('zh-CN', SFFeedRow, {
      props: { title: 'Topic', layout: 'table', replies: 3, views: 12 }
    })
    const enTable = mountIn('en-US', SFFeedRow, {
      props: { title: 'Topic', layout: 'table', replies: 3, views: 12 }
    })
    const enCompact = mountIn('en-US', SFFeedRow, {
      props: { title: 'Topic', layout: 'compact', views: 12 }
    })
    try {
      expect(zhTable.get('.sf-feed-row__stat--replies').attributes('aria-label')).toBe('回复数')
      expect(zhTable.get('.sf-feed-row__stat--replies').text()).toContain('回复')
      expect(zhTable.get('.sf-feed-row__stat--views').attributes('aria-label')).toBe('浏览数')
      // 投票按钮只在 compact 布局出现；table 布局断言的是统计格子。
      expect(zhTable.findAll('.sf-feed-row__vote-btn')).toEqual([])

      expect(enTable.get('.sf-feed-row__stat--replies').attributes('aria-label')).toBe('Replies')
      expect(enTable.get('.sf-feed-row__stat--replies').text()).toContain('Replies')
      expect(enTable.get('.sf-feed-row__stat--views').attributes('aria-label')).toBe('Views')
      expect(enCompact.findAll('.sf-feed-row__vote-btn').map(node => node.attributes('aria-label')))
        .toEqual(['Upvote', 'Downvote'])

      expect(enCompact.get('.sf-feed-row__views').text()).toContain('12 views')
      expectNoHan('en-US', enTable.html())
    } finally {
      zhTable.unmount()
      enTable.unmount()
      enCompact.unmount()
    }
  })
})

describe('SFEditorToolbar accessible names', () => {
  const toolbarProps = {
    preset: 'full' as const,
    disabled: false,
    canUndo: true,
    canRedo: true,
    active: {},
    blockFormat: 'paragraph' as const,
    viewMode: 'write' as const
  }

  for (const locale of LOCALES) {
    test(`names the toolbar, its commands and its view modes in ${locale}`, () => {
      activeLocale = locale
      const wrapper = mountIn(locale, SFEditorToolbar, { props: toolbarProps })
      try {
        expect(wrapper.get('.sf-editor__toolbar').attributes('aria-label'))
          .toBe(translate(locale, 'composer.editor.toolbarLabel'))
        expect(wrapper.findAll('.sf-editor__tool')[0]!.attributes('aria-label'))
          .toBe(translate(locale, 'composer.editor.undo'))
        expect(wrapper.findAll('.sf-editor__tool')[1]!.attributes('title'))
          .toBe(translate(locale, 'composer.editor.redo'))

        const select = wrapper.get('select.sf-editor__format')
        expect(select.attributes('aria-label')).toBe(translate(locale, 'composer.editor.formatLabel'))
        expect(wrapper.findAll('option').map(node => node.text()))
          .toEqual([
            translate(locale, 'composer.editor.format.paragraph'),
            translate(locale, 'composer.editor.format.heading2'),
            translate(locale, 'composer.editor.format.heading3')
          ])

        expect(wrapper.get('.sf-editor__modes').attributes('aria-label'))
          .toBe(translate(locale, 'composer.editor.viewLabel'))
        expect(wrapper.findAll('.sf-editor__mode').map(node => node.text()))
          .toEqual([translate(locale, 'composer.editor.mode.write'), translate(locale, 'composer.editor.mode.preview')])

        const labels = wrapper.findAll('.sf-editor__tool').map(node => node.attributes('aria-label'))
        expect(labels).toContain(translate(locale, 'composer.editor.action.bold'))
        expect(labels).toContain(translate(locale, 'composer.editor.action.bulletList'))
        expect(labels).toContain(translate(locale, 'composer.editor.action.link'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })
  }

  test('the basic-field preset still names its remaining commands', () => {
    activeLocale = 'en-US'
    const wrapper = mountIn('en-US', SFEditorToolbar, {
      props: { ...toolbarProps, preset: 'basic-field' }
    })
    try {
      const labels = wrapper.findAll('.sf-editor__tool').map(node => node.attributes('aria-label'))
      expect(labels).toEqual(['Undo', 'Redo', 'Bold', 'Italic', 'Bulleted list', 'Numbered list', 'Link'])
      expect(wrapper.find('select.sf-editor__format').exists()).toBe(false)
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFAvatar anonymous fallback', () => {
  test('resolves the default display name from the locale in both the alt text and initials', () => {
    const avatar = { kind: 'uploaded', url: '/media/avatars/pub-1' }
    const zh = mountIn('zh-CN', SFAvatar, { props: { avatar } })
    const en = mountIn('en-US', SFAvatar, { props: { avatar } })
    const zhInitials = mountIn('zh-CN', SFAvatar, {})
    try {
      expect(zh.get('img').attributes('alt')).toBe('匿名用户')
      expect(en.get('img').attributes('alt')).toBe('Anonymous user')
      // 首字母也从同一份文案来：中文取首字，英文取前两个词首字母。
      expect(zhInitials.get('span.sf-avatar span').text()).toBe('匿')
      expectNoHan('en-US', en.html())
    } finally {
      zh.unmount()
      en.unmount()
      zhInitials.unmount()
    }
  })
})

describe('SFEditor default copy', () => {
  // 只替换宿主能力：i18n 用真 locale，其余（Tiptap、工具函数）都是真模块。
  const editorStubs = {
    ClientOnly: { template: '<div><slot /></div>' },
    SFButton: { template: '<button class="sf-button-stub"><slot /></button>' },
    UIcon: true,
    UButton: { template: '<button><slot /></button>' }
  }

  async function mountEditor(locale: LocaleName, props: Record<string, unknown>) {
    const wrapper = mountIn(locale, SFEditor, {
      props: { loadTrustedCatalog: false, ...props },
      stubs: editorStubs,
      directives: { highlight: {} },
      mocks: { sanitizeHtml }
    })
    await flushPromises()
    return wrapper
  }

  for (const locale of LOCALES) {
    test(`renders the placeholder, editor name, support hint and submit label in ${locale}`, async () => {
      const wrapper = await mountEditor(locale, {
        preset: 'basic-field',
        compact: true,
        modelValue: ''
      })
      try {
        const content = wrapper.get('.sf-editor__content')
        // placeholder 由 Tiptap 渲染到空段落上，属性值来自 props/默认值 → 就是用户看到的那句。
        expect(content.get('p').attributes('data-placeholder')).toBe(translate(locale, 'composer.editor.placeholder'))
        expect(content.attributes('aria-label')).toBe(translate(locale, 'composer.editor.ariaLabel'))
        expect(wrapper.get('.sf-editor__compact-support').text()).toBe(translate(locale, 'composer.editor.supportMarkdown'))
        expect(wrapper.get('.sf-button-stub').text()).toBe(translate(locale, 'composer.editor.submit'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })

    test(`renders the character/word counters and the document hint in ${locale}`, async () => {
      const wrapper = await mountEditor(locale, { preset: 'full', modelValue: 'hello world' })
      try {
        // 「hello world」= 11 个字符 / 2 个词；上限后缀仍跟在计数后面。
        expect(wrapper.get('.sf-editor__status').text()).toBe(translate(locale, 'composer.editor.characterCount', { count: '11 / 12000' }))
        const meta = wrapper.get('.sf-editor__meta').text()
        expect(meta).toContain(translate(locale, 'composer.editor.wordCount', { count: 2 }))
        expect(meta).toContain(translate(locale, 'composer.editor.structuredDocument'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })
  }

  test('the client-only fallback announces the loading state in the active locale', async () => {
    const wrapper = mountIn('en-US', SFEditor, {
      props: { preset: 'basic-field', modelValue: '', loadTrustedCatalog: false },
      stubs: { ...editorStubs, ClientOnly: { template: '<div><slot name="fallback" /></div>' } },
      directives: { highlight: {} },
      mocks: { sanitizeHtml }
    })
    await flushPromises()
    try {
      expect(wrapper.get('.sf-editor__loading').text()).toBe('Loading editor')
    } finally {
      wrapper.unmount()
    }
  })

  test('explicit props still win over the locale defaults', async () => {
    const wrapper = await mountEditor('en-US', {
      preset: 'basic-field',
      compact: true,
      modelValue: '',
      placeholder: 'Reply to this topic',
      submitLabel: 'Send',
      supportLabel: 'Markdown',
      ariaLabel: 'Reply editor'
    })
    try {
      const content = wrapper.get('.sf-editor__content')
      expect(content.get('p').attributes('data-placeholder')).toBe('Reply to this topic')
      expect(content.attributes('aria-label')).toBe('Reply editor')
      expect(wrapper.get('.sf-editor__compact-support').text()).toBe('Markdown')
      expect(wrapper.get('.sf-button-stub').text()).toBe('Send')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('SFEditor shortcode fallback copy', () => {
  // utils 层的兜底文案只有挂在真编辑器里才算「用户看见的」：短代码扩展由
  // SFEditor.vue 创建，翻译器也在那里注入，所以这里挂整组件而不是单测扩展。
  const editorStubs = {
    ClientOnly: { template: '<div><slot /></div>' },
    SFButton: { template: '<button class="sf-button-stub"><slot /></button>' },
    UIcon: true,
    UButton: { template: '<button><slot /></button>' }
  }
  const shortcodeDocument = [
    '[topic topic_id="12"][/topic]',
    '',
    '[reply]',
    'gated body',
    '[/reply]'
  ].join('\n')

  for (const locale of LOCALES) {
    test(`renders the reference and protected fallbacks in ${locale}`, async () => {
      const wrapper = mountIn(locale, SFEditor, {
        props: { preset: 'full', loadTrustedCatalog: false, initialContent: shortcodeDocument },
        stubs: editorStubs,
        directives: { highlight: {} },
        mocks: { sanitizeHtml }
      })
      await flushPromises()
      try {
        // 写模式的可编辑面：没有 NodeView（未准入插件）时走 renderHTML 兜底卡片，
        // 这就是用户看到的文案。预览面走同一份 renderHTML 输出，另由末尾的整页
        // 零 CJK 断言覆盖。
        const fallbacks = wrapper.get('.sf-editor__content').findAll('.sf-editor-fallback')
        expect(fallbacks.map(node => node.attributes('data-fallback'))).toEqual([
          'shortcode.topic.unavailable',
          'shortcode.protected.unavailable'
        ])
        expect(fallbacks.map(node => node.text())).toEqual([
          translate(locale, 'composer.shortcodes.fallback.topic'),
          translate(locale, 'composer.shortcodes.fallback.protected')
        ])
        // zh 侧确实落地中文、en 侧零 CJK：两侧都不许停在「键」或「写死的中文」。
        expect({ locale, han: HAN_PATTERN.test(fallbacks.map(node => node.text()).join('')) })
          .toEqual({ locale, han: locale === 'zh-CN' })
        // 预览面吃的是同一份 renderHTML 输出（经净化），文案同样必须跟随语言。
        expect(wrapper.get('.sf-editor__preview').text())
          .toContain(translate(locale, 'composer.shortcodes.fallback.topic'))
        if (locale === 'en-US') expectNoHan(locale, wrapper.html())
      } finally {
        wrapper.unmount()
      }
    })
  }
})

// SFComment 另有一个普通 <script> 只放类型与 inject key；compileVueSfc 只吃单个
// script，这里在测试侧剥掉它（与 forumCommentActionRow.test.ts 同一手法）。
const plainScript = /<script lang="ts">[\s\S]*?<\/script>\s*/
const commentSource = (await Bun.file(commentPath).text()).replace(plainScript, '')
const scratchPath = `/tmp/sf-comment-i18n-${process.pid}.vue`
await Bun.write(scratchPath, commentSource)
const SFComment = await compileVueSfc(scratchPath, 'sf-comment-i18n', {
  ...bindings,
  inject: testVue.inject,
  onMounted: testVue.onMounted,
  onBeforeUnmount: testVue.onBeforeUnmount,
  nextTick: testVue.nextTick,
  useId: testVue.useId,
  useWebOptions: () => ({ reactionsEnabled: testVue.ref(false), bookmarksEnabled: testVue.ref(false) }),
  sanitizeHtml: (html: string) => html,
  forumAuthorName: (author: { displayName?: string } | undefined) => author?.displayName ?? '',
  userDisplayName: (author: { displayName?: string } | undefined) => author?.displayName ?? '',
  commentBranchPresentation: () => ({ collapsible: false }),
  COMMENT_EDITOR_RENDERER_KEY: 'sforum-comment-editor-renderer',
  SFCommentAuthorRow: Stub,
  SFCommentUserPreview: Stub,
  SFCommentAIActivity: Stub,
  SFMentionContent: Stub,
  SFReactionControl: Stub,
  SFBookmarkControl: Stub,
  SFUserIdentityName: Stub,
  SFBadgeChip: Stub,
  SFComment: Stub
})
unlinkSync(scratchPath)

describe('SFComment default reply action', () => {
  test('renders the reply action label from the locale when actions are omitted', () => {
    const comment = { id: 1, status: 'active', author: { id: 3, username: 'qa', displayName: 'QA' }, content: { htmlContent: '<p>hi</p>' } }
    const zh = mountIn('zh-CN', SFComment, {
      props: { comment, author: 'QA' },
      directives: { highlight: {} }
    })
    const en = mountIn('en-US', SFComment, {
      props: { comment, author: 'QA' },
      directives: { highlight: {} }
    })
    try {
      expect(zh.get('.sf-comment__action').text()).toBe('回复')
      expect(en.get('.sf-comment__action').text()).toBe('Reply')
      expectNoHan('en-US', en.html())
    } finally {
      zh.unmount()
      en.unmount()
    }
  })

  test('an explicitly empty action list still renders no action buttons', () => {
    const comment = { id: 2, status: 'active', author: { id: 3, username: 'qa', displayName: 'QA' }, content: { htmlContent: '<p>hi</p>' } }
    const wrapper = mountIn('en-US', SFComment, { props: { comment, author: 'QA', actions: [] } })
    try {
      expect(wrapper.findAll('.sf-comment__action')).toEqual([])
    } finally {
      wrapper.unmount()
    }
  })
})
