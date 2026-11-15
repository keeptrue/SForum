import { CharacterCount } from '@tiptap/extension-character-count'
import { FileHandler } from '@tiptap/extension-file-handler'
import Image from '@tiptap/extension-image'
import Placeholder from '@tiptap/extension-placeholder'
import Underline from '@tiptap/extension-underline'
import { Markdown } from '@tiptap/markdown'
import StarterKit from '@tiptap/starter-kit'
import {
  createInlineMarkdownSpec,
  mergeAttributes,
  Node,
  type JSONContent
} from '@tiptap/vue-3'
import type { Editor } from '@tiptap/core'
import type { NodeViewRenderer } from '@tiptap/core'
import { createSForumClipboardExtensions } from '~/utils/editor/clipboard'
import { SForumCodeBlock } from '~/utils/editor/codeBlock'
import { createEditorImageUploadPlaceholderExtension } from '~/utils/editor/editorImageUpload'
import {
  editorImageRenderAttributes,
  normalizeEditorImageDimension,
  normalizeEditorImageDisplaySize
} from '~/utils/editor/editorImage'
import { SForumLink } from '~/utils/editor/link'
import {
  createSForumMentionExtensions,
  type SForumMentionOptions
} from '~/utils/editor/mentions'
import {
  createSForumShortcodeExtensions,
  type SFEditorTranslate,
  type SFShortcodeNodeViewHost,
  type SFShortcodeResourceKind
} from '~/utils/editor/shortcodes'
import { createSForumTableExtensions } from '~/utils/editor/table'

export type SForumEmojiItem = {
  name: string
  label: string
  native: string
}

/**
 * 表情节点的默认插入项。
 *
 * 模块级常量不能调 `t()`（会把首次加载的语言冻进闭包），所以这里只存文案
 * **键**，由插入点用 `sforumEmojiItem()` 在渲染期解析：`label` 会写进文档、
 * `title` 与 `data-label`，必须按插入当时的语言落地。
 */
export type SForumEmojiPreset = {
  name: string
  labelKey: string
  native: string
}

export type TiptapContentReader = {
  getHTML: () => string
  getMarkdown: () => string
  getJSON: () => JSONContent
  getText: () => string
  storage: {
    characterCount: {
      characters: () => number
      words: () => number
    }
  }
  isEmpty: boolean
}

export type SFEditorContentPayload = {
  html: string
  markdown: string
  native: JSONContent
  text: string
  characterCount: number
  wordCount: number
  isEmpty: boolean
  attachmentIds: number[]
  pendingUploadCount: number
}

// Markdown intentionally omits presentation-only image attributes. Use the
// native document when deciding whether an edit is semantically dirty.
export function editorDocumentSignature(native: unknown) {
  return JSON.stringify(native ?? null)
}

export function isExternalEditorMarkdownUpdate(
  incoming: string,
  lastEmitted: string,
  current: string
) {
  return incoming !== lastEmitted && incoming !== current
}

export const SForumImage = Image.extend({
  addAttributes() {
    return {
      ...this.parent?.(),
      attachmentId: {
        default: null,
        rendered: false
      },
      attachmentPublicId: {
        default: null,
        rendered: false
      },
      width: {
        default: null,
        rendered: false,
        parseHTML: element => normalizeEditorImageDimension(element.getAttribute('width'))
      },
      height: {
        default: null,
        rendered: false,
        parseHTML: element => normalizeEditorImageDimension(element.getAttribute('height'))
      },
      displaySize: {
        default: 'standard',
        rendered: false,
        parseHTML: element => normalizeEditorImageDisplaySize(element.getAttribute('data-sforum-image-size'))
      }
    }
  },

  renderHTML({ node, HTMLAttributes }) {
    return ['img', mergeAttributes(
      this.options.HTMLAttributes,
      HTMLAttributes,
      editorImageRenderAttributes(node.attrs)
    )]
  }
})

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    sforumEmoji: {
      insertSForumEmoji: (emoji: SForumEmojiItem) => ReturnType
    }
  }
}

const sforumEmojiMarkdown = createInlineMarkdownSpec({
  nodeName: 'sforumEmoji',
  name: 'emoji',
  selfClosing: true,
  allowedAttributes: ['name', 'label', 'native']
})

export const SForumEmoji = Node.create({
  name: 'sforumEmoji',
  group: 'inline',
  inline: true,
  atom: true,
  selectable: false,

  addAttributes() {
    return {
      name: {
        default: ''
      },
      label: {
        default: ''
      },
      native: {
        default: ''
      }
    }
  },

  parseHTML() {
    return [
      {
        tag: 'span[data-sforum-emoji]'
      }
    ]
  },

  renderHTML({ node, HTMLAttributes }) {
    const name = String(node.attrs.name || '')
    const label = String(node.attrs.label || name)
    const native = String(node.attrs.native || `:${name}:`)

    return [
      'span',
      mergeAttributes(HTMLAttributes, {
        class: 'sf-editor-emoji-node',
        'data-sforum-emoji': name,
        'data-label': label,
        title: label
      }),
      native
    ]
  },

  renderText({ node }) {
    const name = String(node.attrs.name || '')
    return String(node.attrs.native || `:${name}:`)
  },

  addCommands() {
    return {
      insertSForumEmoji:
        emoji =>
        ({ commands }) => commands.insertContent({
          type: this.name,
          attrs: emoji
        })
    }
  },

  ...sforumEmojiMarkdown
})

export const sforumEditorEmojiPresets: SForumEmojiPreset[] = [
  { name: 'sparkles', labelKey: 'composer.emoji.sparkles', native: '✨' },
  { name: 'clap', labelKey: 'composer.emoji.clap', native: '👏' },
  { name: 'thinking', labelKey: 'composer.emoji.thinking', native: '🤔' },
  { name: 'rocket', labelKey: 'composer.emoji.rocket', native: '🚀' },
  { name: 'eyes', labelKey: 'composer.emoji.eyes', native: '👀' },
  { name: 'party', labelKey: 'composer.emoji.party', native: '🎉' }
]

/** 把预设解析成节点属性：`label` 走 i18n；缺键时留空，渲染侧会回落到 `name`。 */
export function sforumEmojiItem(
  preset: SForumEmojiPreset,
  translate: SFEditorTranslate
): SForumEmojiItem {
  const label = translate(preset.labelKey)
  return {
    name: preset.name,
    label: label === preset.labelKey ? '' : label,
    native: preset.native
  }
}

export function createSFEditorExtensions(options: {
  placeholder: string
  maxCharacters: number
  preset?: 'full' | 'basic-field'
  // Trusted L2 plugin extensions already digest-verified by Host loader.
  // Failures must be filtered before calling this helper so core stays usable.
  trustedExtensions?: unknown[]
  onImageDrop?: (editor: Editor, files: File[], pos: number) => void
  resourceKind?: SFShortcodeResourceKind
  shortcodeReferenceNodeView?: NodeViewRenderer
  shortcodeProtectedNodeView?: NodeViewRenderer
  shortcodeNodeViewHost?: SFShortcodeNodeViewHost
  /** 论坛 @提及（候选 + 高亮）；不传则完全不注册，基础场景不受影响。 */
  mentions?: SForumMentionOptions
  /**
   * 渲染期文案翻译器（组件里传 `useI18n().t`）。
   *
   * 短代码/表情这类扩展在模块级定义，只能持有文案键；界面文案在这一层注入，
   * 不传时退到模块内声明的最后兜底字面量（纯工具调用、单测）。
   */
  translate?: SFEditorTranslate
}) {
  const full = options.preset !== 'basic-field'
  const trusted = Array.isArray(options.trustedExtensions)
    ? options.trustedExtensions.filter(Boolean)
    : []
  return [
    StarterKit.configure({
      link: false,
      underline: false,
      codeBlock: false,
      ...(full ? {} : {
        blockquote: false,
        code: false,
        heading: false,
        horizontalRule: false,
        strike: false
      })
    }),
    ...(full ? [SForumCodeBlock] : []),
    // 剪贴板归一化必须先于 Link/短代码的 paste handler，保证代码块内始终字面粘贴。
    ...createSForumClipboardExtensions(),
    ...(full ? [Underline] : []),
    SForumLink.configure({
      autolink: true,
      linkOnPaste: true,
      openOnClick: false,
      defaultProtocol: 'https',
      protocols: ['http', 'https', 'mailto'],
      HTMLAttributes: {
        rel: 'noopener noreferrer nofollow ugc',
        target: '_blank'
      },
      isAllowedUri: allowedLinkUri
    }),
    ...(full ? [
      ...createSForumShortcodeExtensions(
        options.resourceKind || 'topic',
        options.shortcodeReferenceNodeView,
        options.shortcodeProtectedNodeView,
        options.shortcodeNodeViewHost,
        { translate: options.translate }
      ),
      SForumImage.configure({
        allowBase64: false,
        HTMLAttributes: {
          loading: 'lazy',
          decoding: 'async',
          referrerpolicy: 'no-referrer'
        }
      }),
      FileHandler.configure({
        onDrop: options.onImageDrop
      }),
      createEditorImageUploadPlaceholderExtension(),
      SForumEmoji,
      // 表格只在完整撰写场景注册；basic-field 保持纯文本 schema。
      ...createSForumTableExtensions()
    ] : []),
    Placeholder.configure({
      placeholder: options.placeholder
    }),
    CharacterCount.configure({
      limit: options.maxCharacters,
      wordCounter: text => text.trim().split(/\s+/).filter(Boolean).length
    }),
    Markdown.configure({
      indentation: { style: 'space', size: 2 },
      markedOptions: {
        gfm: true,
        breaks: false
      }
    }),
    ...(full && options.mentions ? createSForumMentionExtensions(options.mentions) : []),
    // Plugin L2 extensions append after Host core so core marks/nodes win on
    // name conflicts inside Tiptap's extension manager.
    ...(full ? trusted : [])
  ]
}

export function collectEditorAttachmentIds(document: JSONContent) {
  const ids: number[] = []
  const seen = new Set<number>()

  function walk(node: JSONContent) {
    if (node.type === 'image') {
      const id = Number(node.attrs?.attachmentId)
      if (Number.isSafeInteger(id) && id > 0 && !seen.has(id)) {
        seen.add(id)
        ids.push(id)
      }
    }
    for (const child of node.content || []) {
      walk(child)
    }
  }

  walk(document)
  return ids
}

// 客户端只做交互层的基础限制；服务端仍必须重新生成并净化 HTML 后才能入库。
function allowedLinkUri(url: string, context: {
  defaultValidate: (url: string) => boolean
}) {
  const value = url.trim()

  if ((value.startsWith('/') && !value.startsWith('//')) || value.startsWith('#')) {
    return true
  }

  const normalized = normalizeUserUrl(url)
  return Boolean(normalized && context.defaultValidate(normalized))
}

export function normalizeUserUrl(url: string) {
  const value = url.trim()

  if (!value) {
    return ''
  }

  if ((value.startsWith('/') && !value.startsWith('//')) || value.startsWith('#')) {
    return value
  }

  try {
    const parsed = new URL(value)
    return ['http:', 'https:', 'mailto:'].includes(parsed.protocol) ? parsed.href : ''
  } catch {
    try {
      return new URL(`https://${value}`).href
    } catch {
      return ''
    }
  }
}

export function normalizeImageUrl(url: string) {
  const value = normalizeUserUrl(url)

  if (!value || value.startsWith('mailto:') || value.startsWith('#')) {
    return ''
  }

  return value
}

export function escapeHtml(value: string) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;')
}
