<script setup lang="ts">
import { useTrustedEditorCatalog } from '~/composables/editor/useTrustedEditorCatalog'
import SFEditorToolbar, {
  type SFEditorBlockFormat,
  type SFEditorExtensionTool,
  type SFEditorToolbarAction,
  type SFEditorViewMode
} from '~/components/editor/SFEditorToolbar.vue'
import SFEditorImageUploadModal from '~/components/editor/SFEditorImageUploadModal.vue'
import SFEditorImageMenu from '~/components/editor/SFEditorImageMenu.vue'
import SFEditorTableMenu from '~/components/editor/SFEditorTableMenu.vue'
import SFShortcodeReferenceDialog from '~/components/editor/shortcodes/SFShortcodeReferenceDialog.vue'
import SFShortcodeReferenceNodeView from '~/components/editor/shortcodes/SFShortcodeReferenceNodeView.vue'
import SFShortcodeProtectedNodeView from '~/components/editor/shortcodes/SFShortcodeProtectedNodeView.vue'
import { Editor, EditorContent, VueNodeViewRenderer } from '@tiptap/vue-3'
import type { AnyExtension } from '@tiptap/core'
import type { AdmittedEditorCommand } from '~/runtime/editor-extensions/admit'
import type {
  EditorCatalogContribution,
  EditorL2CommandContextV1,
  EditorProtectedKind,
  EditorProtectedSelectionV1,
  EditorReferenceDialogResultV1,
  EditorReferenceKind,
  EditorReferenceSelectionV1,
  EditorShortcodeKind,
  EditorShortcodeSelectionV1
} from '~/runtime/editor-extensions/types'
import {
  collectEditorAttachmentIds,
  createSFEditorExtensions,
  escapeHtml,
  isExternalEditorMarkdownUpdate,
  normalizeUserUrl,
  type SFEditorContentPayload,
  type TiptapContentReader
} from '~/utils/sfEditor'
import { imageFilesFromList, useEditorImageUpload } from '~/composables/editor/useEditorImageUpload'
import { useMentionSuggestions } from '~/composables/editor/useMentionSuggestions'
import { insertSForumLink } from '~/utils/editor/link'
import {
  buildSForumProtectedTransaction,
  inspectSForumShortcodeMarkdown,
  prepareSForumShortcodeMarkdown,
  SF_SHORTCODE_BLOCK_NODE,
  SF_SHORTCODE_REF_NODE,
  sforumCanInsertReference,
  type SFProtectedInsertResult,
  type SFShortcodeReferencePreview,
  sforumProtectedNodeAttributes,
  sforumReferenceNodeAttributes,
  type SFShortcodeNodeViewHost
} from '~/utils/editor/shortcodes'
import { insertSForumTable, type SFEditorTableSize } from '~/utils/editor/table'

const props = withDefaults(defineProps<{
  modelValue?: string
  /** 首次挂载使用的 Markdown 或原生 Tiptap JSON；后续编辑仍通过 Markdown v-model 同步。 */
  initialContent?: string | Record<string, unknown>
  placeholder?: string
  rows?: number
  hint?: string
  disabled?: boolean
  submitDisabled?: boolean
  error?: string
  maxCharacters?: number
  submitLabel?: string
  /** 由抽屉等宿主提供统一底部操作区时，可隐藏编辑器内建提交按钮。 */
  submitVisible?: boolean
  compact?: boolean
  preset?: 'full' | 'basic-field'
  imageSurface?: 'topic' | 'comment'
  /** 论坛 @提及（候选浮层 + 高亮）；仅论坛编辑宿主在 mentionsEnabled 设置开启时传入。 */
  enableMentions?: boolean
  ariaLabel?: string
  cancelLabel?: string
  supportLabel?: string
  // Host-admitted trusted L2 Tiptap extensions (digest-verified before pass-in).
  trustedExtensions?: unknown[]
  // 默认从 Host editor-catalog 拉取并 digest-verify 准入 L2；失败 fail-closed。
  loadTrustedCatalog?: boolean
}>(), {
  modelValue: '',
  initialContent: undefined,
  placeholder: undefined,
  rows: 6,
  hint: undefined,
  disabled: false,
  submitDisabled: false,
  error: undefined,
  maxCharacters: 12000,
  submitLabel: undefined,
  submitVisible: true,
  compact: false,
  preset: 'full',
  imageSurface: 'topic',
  enableMentions: false,
  ariaLabel: undefined,
  cancelLabel: '',
  supportLabel: undefined,
  trustedExtensions: () => [],
  loadTrustedCatalog: true
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  'content-change': [payload: SFEditorContentPayload]
  submit: [payload: SFEditorContentPayload]
  cancel: []
}>()

const editor = shallowRef<Editor | null>(null)
const editorForContent = computed(() => editor.value || undefined)
const viewMode = ref<SFEditorViewMode>('write')
// modelValue 只承载 Markdown；原生 JSON 必须走 initialContent，避免把 rawContent 当正文。
const editorStateTick = ref(0)
const lastEmittedMarkdown = ref('')
const imageDialogOpen = ref(false)
const imageInsertPosition = ref<number | null>(null)
const toast = useToast()
const { t } = useI18n()

// 默认文案走 i18n：prop 默认值在 setup 之外求值，拿不到 t()，所以统一在这里兜底（RV-28）。
const resolvedPlaceholder = computed(() => props.placeholder ?? t('composer.editor.placeholder'))
const resolvedSubmitLabel = computed(() => props.submitLabel ?? t('composer.editor.submit'))
const resolvedAriaLabel = computed(() => props.ariaLabel ?? t('composer.editor.ariaLabel'))
const resolvedSupportLabel = computed(() => props.supportLabel ?? t('composer.editor.supportMarkdown'))

const { fetchMentionSuggestions } = useMentionSuggestions()
const { pendingUploadCount, uploadImages } = useEditorImageUpload({
  uploading: t('composer.imageUpload.uploading'),
  invalidType: t('composer.imageUpload.invalidType'),
  notAllowed: t('composer.imageUpload.notAllowed'),
  tooLarge: (fileName, maxSize) => t('composer.imageUpload.tooLarge', { fileName, maxSize }),
  uploaded: count => t('composer.imageUpload.uploaded', { count }),
  partiallyUploaded: (uploaded, failed) => t('composer.imageUpload.partiallyUploaded', { uploaded, failed }),
  failed: t('composer.imageUpload.failed'),
  positionLost: t('composer.imageUpload.positionLost')
})

const editorMinHeight = computed(() => `${Math.max(props.rows, 4) * 1.55 + 1.5}rem`)

const editorClass = computed(() => [
  'sf-editor',
  props.disabled ? 'sf-editor--disabled' : '',
  props.error ? 'sf-editor--invalid' : '',
  props.compact ? 'sf-editor--compact' : '',
  props.preset === 'basic-field' ? 'sf-editor--basic-field' : '',
  props.imageSurface === 'comment' ? 'sf-editor--image-comment' : ''
].filter(Boolean).join(' '))

const blockFormat = computed<SFEditorBlockFormat>(() => {
  if (isActive('heading', { level: 2 })) return 'heading-2'
  if (isActive('heading', { level: 3 })) return 'heading-3'
  return 'paragraph'
})

const toolbarActive = computed<Partial<Record<SFEditorToolbarAction, boolean>>>(() => ({
  bold: isActive('bold'),
  italic: isActive('italic'),
  strike: isActive('strike'),
  code: isActive('code'),
  bulletList: isActive('bulletList'),
  orderedList: isActive('orderedList'),
  blockquote: isActive('blockquote'),
  codeBlock: isActive('codeBlock'),
  link: isActive('link'),
  table: isActive('table')
}))

const currentPayload = computed<SFEditorContentPayload>(() => {
  void editorStateTick.value

  if (!editor.value) {
    return emptyPayload(props.modelValue)
  }

  const currentEditor = editor.value

  return {
    html: currentEditor.getHTML(),
    markdown: currentEditor.getMarkdown(),
    native: currentEditor.getJSON(),
    text: currentEditor.getText(),
    characterCount: currentEditor.storage.characterCount.characters(),
    wordCount: currentEditor.storage.characterCount.words(),
    isEmpty: currentEditor.isEmpty,
    attachmentIds: collectEditorAttachmentIds(currentEditor.getJSON()),
    pendingUploadCount: pendingUploadCount.value
  }
})

const footerText = computed(() => {
  if (props.error) {
    return props.error
  }

  if (props.hint) {
    return props.hint
  }

  const count = currentPayload.value.characterCount
  const suffix = props.maxCharacters ? ` / ${props.maxCharacters}` : ''
  return t('composer.editor.characterCount', { count: `${count}${suffix}` })
})

const shouldLoadTrustedCatalog = props.loadTrustedCatalog && props.preset === 'full'
const catalogReady = ref(!shouldLoadTrustedCatalog)
const admittedExtensions = shallowRef<unknown[]>(props.trustedExtensions || [])
const admittedToolbars = shallowRef<EditorCatalogContribution[]>([])
const admittedCommands = shallowRef<Record<string, AdmittedEditorCommand>>({})
const shortcodeInlineError = ref('')
const referenceCache = new Map<string, SFShortcodeReferencePreview | null>()
const referenceDialog = reactive<{
  open: boolean
  resources: EditorShortcodeKind[]
  selection?: EditorShortcodeSelectionV1
  resolve?: (result: EditorReferenceDialogResultV1) => void
  returnFocus?: HTMLElement
}>({ open: false, resources: [] })

const extensionTools = computed<SFEditorExtensionTool[]>(() => admittedToolbars.value
  .filter(item => Boolean(item.commandId && admittedCommands.value[item.commandId]))
  .map(item => ({
    id: item.commandId || item.id,
    label: item.artifact.extensionId === 'sforum-shortcodes'
      ? t('composer.shortcodes.toolbar')
      : (item.label || item.id),
    icon: item.icon || 'i-lucide-blocks',
    disabled: props.disabled
  })))

const shortcodeCommandID = computed(() => Object.entries(admittedCommands.value)
  .find(([, command]) => command.declaration.artifact.extensionId === 'sforum-shortcodes')?.[0] || '')

onMounted(async () => {
  let trusted = props.trustedExtensions || []
  if (shouldLoadTrustedCatalog) {
    try {
      const { loadAdmittedExtensions } = useTrustedEditorCatalog()
      const admitted = await loadAdmittedExtensions()
      // 父组件显式传入的扩展优先于 catalog 准入结果。
      trusted = [...admitted.extensions, ...trusted]
      admittedToolbars.value = admitted.toolbars
      admittedCommands.value = admitted.commands
    } catch {
      // fail-closed：catalog 失败时仅核心扩展
    }
  }
  admittedExtensions.value = trusted
  catalogReady.value = true
  // object → Tiptap JSON 文档；string → Markdown。禁止把 editor-document 的 raw JSON 字符串当 Markdown。
  const unpreparedInitialContent = props.initialContent !== undefined && props.initialContent !== null
    ? props.initialContent
    : (props.modelValue || '')
  const initialContent = typeof unpreparedInitialContent === 'string'
    ? prepareSForumShortcodeMarkdown(unpreparedInitialContent, props.imageSurface)
    : unpreparedInitialContent
  let nextEditor: Editor
  nextEditor = new Editor({
    content: initialContent,
    ...(typeof initialContent === 'string' ? { contentType: 'markdown' as const } : {}),
    editable: !props.disabled,
    extensions: createSFEditorExtensions({
      placeholder: resolvedPlaceholder.value,
      maxCharacters: props.maxCharacters,
      preset: props.preset,
      trustedExtensions: trusted,
      resourceKind: props.imageSurface,
      ...(props.enableMentions ? {
        mentions: {
          fetch: fetchMentionSuggestions,
          labels: { empty: t('composer.mention.empty') }
        }
      } : {}),
      ...(shortcodeCommandID.value ? {
        shortcodeReferenceNodeView: VueNodeViewRenderer(SFShortcodeReferenceNodeView),
        shortcodeProtectedNodeView: VueNodeViewRenderer(SFShortcodeProtectedNodeView),
        shortcodeNodeViewHost: createShortcodeNodeViewHost()
      } : {}),
      onImageDrop: (dropEditor, files, pos) => {
        void uploadImages(dropEditor, files, pos)
      }
    }) as AnyExtension[],
    onCreate: ({ editor: createdEditor }) => {
      syncFromEditor(createdEditor)
    },
    onUpdate: ({ editor: updatedEditor }) => {
      syncFromEditor(updatedEditor)
    },
    onSelectionUpdate: () => {
      editorStateTick.value += 1
    },
    editorProps: {
      attributes: {
        class: 'sf-editor__content',
        'aria-label': resolvedAriaLabel.value
      },
      handlePaste: (_view, event) => {
        const files = imageFilesFromList(event.clipboardData?.files)
        if (files.length === 0) {
          const markdown = event.clipboardData?.getData('text/plain') || ''
          const inspection = inspectSForumShortcodeMarkdown(markdown, props.imageSurface)
          if (!inspection.activated || !inspection.withinBudgets) return false

          event.preventDefault()
          return nextEditor.commands.insertContent(markdown, { contentType: 'markdown' })
        }

        event.preventDefault()
        void uploadImages(nextEditor, files, nextEditor.state.selection.from)
        return true
      }
    }
  })
  editor.value = nextEditor
})

onBeforeUnmount(() => {
  editor.value?.destroy()
})

watch(() => props.disabled, disabled => {
  editor.value?.setEditable(!disabled)
})

watch(resolvedPlaceholder, placeholder => {
  const placeholderExtension = editor.value?.extensionManager.extensions
    .find(extension => extension.name === 'placeholder')

  if (placeholderExtension) {
    placeholderExtension.options.placeholder = placeholder
  }
})

watch(pendingUploadCount, () => {
  if (editor.value) syncFromEditor(editor.value)
})

// 仅接受外部 Markdown 同步；跳过与自身 emit 相同的回写，以及与当前文档一致的值。
watch(() => props.modelValue, value => {
  const incomingMarkdown = value || ''
  const currentEditor = editor.value

  if (!currentEditor) {
    return
  }
  if (!isExternalEditorMarkdownUpdate(
    incomingMarkdown,
    lastEmittedMarkdown.value,
    currentPayload.value.markdown
  )) {
    return
  }

  const nextMarkdown = prepareSForumShortcodeMarkdown(incomingMarkdown, props.imageSurface)
  if (nextMarkdown === currentPayload.value.markdown) return

  lastEmittedMarkdown.value = nextMarkdown
  currentEditor.commands.setContent(nextMarkdown, {
    contentType: 'markdown',
    emitUpdate: false
  })
  editorStateTick.value += 1
})

function emptyPayload(markdown: string): SFEditorContentPayload {
  return {
    html: markdown ? `<p>${escapeHtml(markdown)}</p>` : '<p></p>',
    markdown,
    native: {
      type: 'doc',
      content: markdown
        ? [{ type: 'paragraph', content: [{ type: 'text', text: markdown }] }]
        : [{ type: 'paragraph' }]
    },
    text: markdown,
    characterCount: markdown.length,
    wordCount: markdown.trim() ? markdown.trim().split(/\s+/).length : 0,
    isEmpty: markdown.trim().length === 0,
    attachmentIds: [],
    pendingUploadCount: pendingUploadCount.value
  }
}

function syncFromEditor(sourceEditor: TiptapContentReader) {
  editorStateTick.value += 1

  const payload: SFEditorContentPayload = {
    html: sourceEditor.getHTML(),
    markdown: sourceEditor.getMarkdown(),
    native: sourceEditor.getJSON(),
    text: sourceEditor.getText(),
    characterCount: sourceEditor.storage.characterCount.characters(),
    wordCount: sourceEditor.storage.characterCount.words(),
    isEmpty: sourceEditor.isEmpty,
    attachmentIds: collectEditorAttachmentIds(sourceEditor.getJSON()),
    pendingUploadCount: pendingUploadCount.value
  }

  if (payload.markdown !== lastEmittedMarkdown.value) {
    lastEmittedMarkdown.value = payload.markdown
    emit('update:modelValue', payload.markdown)
  }

  emit('content-change', payload)
}

function runEditorCommand(command: (currentEditor: Editor) => boolean) {
  const currentEditor = editor.value

  if (!currentEditor || props.disabled) {
    return
  }

  command(currentEditor)
}

function isActive(name: string, attrs?: Record<string, unknown>) {
  void editorStateTick.value
  return editor.value?.isActive(name, attrs) || false
}

function canUndo() {
  void editorStateTick.value
  return editor.value?.can().undo() || false
}

function canRedo() {
  void editorStateTick.value
  return editor.value?.can().redo() || false
}

function setLink() {
  const currentEditor = editor.value

  if (!currentEditor || props.disabled) {
    return
  }
  // 代码块不允许 mark；光标或整块选中代码块时，链接按钮保持无操作。
  if (currentEditor.isActive('codeBlock')) {
    return
  }

  const previousUrl = currentEditor.getAttributes('link').href || ''
  const rawUrl = window.prompt(t('composer.editor.linkPrompt'), previousUrl)

  if (rawUrl === null) {
    return
  }

  const href = normalizeUserUrl(rawUrl)

  if (!href) {
    currentEditor.chain().focus().unsetLink().run()
    return
  }

  // 空选区不能 setMark：stored mark 会把之后输入的所有文字都变成链接。
  // 这里直接把 URL 作为链接文字插入，光标停在链接之后（link 不再 inclusive）。
  const { empty } = currentEditor.state.selection
  if (empty && !currentEditor.isActive('link')) {
    insertSForumLink(currentEditor, href)
    return
  }

  currentEditor.chain().focus().extendMarkRange('link').setLink({ href }).run()
}

function openImageDialog() {
  const currentEditor = editor.value

  if (!currentEditor || props.disabled) {
    return
  }
  imageInsertPosition.value = currentEditor.state.selection.from
  imageDialogOpen.value = true
}

function onImageDialogOpenChange(open: boolean) {
  imageDialogOpen.value = open
  if (!open) imageInsertPosition.value = null
}

function onImageFilesSelected(files: File[]) {
  const currentEditor = editor.value
  const position = imageInsertPosition.value
  imageInsertPosition.value = null

  if (!currentEditor || position == null || files.length === 0) return
  void uploadImages(currentEditor, files, position)
}

function runToolbarAction(action: SFEditorToolbarAction) {
  if (action === 'link') {
    setLink()
    return
  }
  if (action === 'image') {
    openImageDialog()
    return
  }
  // 表格尺寸由 SFEditorTablePicker 经 'insert-table' 单独上报，不经过通用 action。
  if (action === 'table') {
    return
  }

  const commands: Record<Exclude<SFEditorToolbarAction, 'link' | 'image' | 'table'>, (currentEditor: Editor) => boolean> = {
    undo: currentEditor => currentEditor.chain().focus().undo().run(),
    redo: currentEditor => currentEditor.chain().focus().redo().run(),
    bold: currentEditor => currentEditor.chain().focus().toggleBold().run(),
    italic: currentEditor => currentEditor.chain().focus().toggleItalic().run(),
    strike: currentEditor => currentEditor.chain().focus().toggleStrike().run(),
    code: currentEditor => currentEditor.chain().focus().toggleCode().run(),
    bulletList: currentEditor => currentEditor.chain().focus().toggleBulletList().run(),
    orderedList: currentEditor => currentEditor.chain().focus().toggleOrderedList().run(),
    blockquote: currentEditor => currentEditor.chain().focus().toggleBlockquote().run(),
    codeBlock: currentEditor => currentEditor.chain().focus().toggleCodeBlock().run()
  }
  runEditorCommand(commands[action])
}

function onInsertTable(size: SFEditorTableSize) {
  runEditorCommand(currentEditor => insertSForumTable(currentEditor, size))
}

function createShortcodeNodeViewHost(): SFShortcodeNodeViewHost {
  return {
    disabled: () => props.disabled || !Boolean(shortcodeCommandID.value),
    resolveReference,
    editReference: selection => void runExtensionAction(shortcodeCommandID.value, selection),
    deleteReference,
    editProtected: selection => void runExtensionAction(shortcodeCommandID.value, selection),
    deleteProtected,
    unwrapProtected
  }
}

async function resolveReference(kind: Exclude<EditorReferenceKind, 'friend-links'>, id: number) {
  const key = `${kind}:${id}`
  if (referenceCache.has(key)) return referenceCache.get(key) || null
  const { request } = useApiClient()
  const params = new URLSearchParams({ kind, selectedId: String(id), limit: '1' })
  const result = await request<{ items: Array<{ id: number } & SFShortcodeReferencePreview> }>(
    `/composer/references?${params}`
  )
  const item = result.items.find(candidate => candidate.id === id)
  const resolved = item
    ? {
        label: item.label,
        secondaryLabel: item.secondaryLabel,
        avatar: item.avatar,
        icon: item.icon,
        iconColor: item.iconColor
      }
    : null
  referenceCache.set(key, resolved)
  return resolved
}

function openReferenceDialog(
  resources: EditorShortcodeKind[],
  selection?: EditorShortcodeSelectionV1
) {
  const allowed: EditorShortcodeKind[] = props.imageSurface === 'comment'
    ? ['user', 'topic', 'comment', 'category', 'friend-links', 'login', 'reply', 'only-author']
    : ['user', 'topic', 'comment', 'category', 'friend-links', 'login', 'reply']
  const selectionAllowed: EditorShortcodeKind[] = selection && 'protected' in selection
    ? allowed.filter(kind => kind === 'login' || kind === 'reply' || kind === 'only-author')
    : selection
      ? allowed.filter(kind => kind !== 'login' && kind !== 'reply' && kind !== 'only-author')
      : allowed
  const normalized = [...new Set(resources.filter(kind => selectionAllowed.includes(kind)))]
  if (normalized.length === 0 || referenceDialog.open) {
    return Promise.resolve<EditorReferenceDialogResultV1>({ action: 'cancel' })
  }
  referenceDialog.resources = normalized
  referenceDialog.selection = selection
  referenceDialog.returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : undefined
  referenceDialog.open = true
  return new Promise<EditorReferenceDialogResultV1>(resolve => {
    referenceDialog.resolve = resolve
  })
}

function resolveReferenceDialog(result: EditorReferenceDialogResultV1) {
  const resolve = referenceDialog.resolve
  const returnFocus = referenceDialog.returnFocus
  referenceDialog.open = false
  referenceDialog.resolve = undefined
  referenceDialog.returnFocus = undefined
  resolve?.(result)
  if (result.action === 'cancel') nextTick(() => returnFocus?.focus())
}

function onReferenceDialogOpenChange(open: boolean) {
  referenceDialog.open = open
}

function upsertReference(
  kind: EditorReferenceKind,
  id: number | undefined,
  selection?: EditorReferenceSelectionV1
) {
  const currentEditor = editor.value
  const attrs = sforumReferenceNodeAttributes(kind, id)
  if (!currentEditor || !attrs || props.disabled) return false
  const content = { type: SF_SHORTCODE_REF_NODE, attrs }
  const selectedNode = selection ? currentEditor.state.doc.nodeAt(selection.position) : null
  // 只拦新插入：改型既有节点不改变其位置，不会引入新的位置违规。
  const isFreshInsert = !selection || selectedNode?.type.name !== SF_SHORTCODE_REF_NODE
  if (isFreshInsert && !sforumCanInsertReference(currentEditor.state)) {
    shortcodeInlineError.value = t('composer.shortcodes.invalidReferencePlacement')
    return false
  }
  const changed = selection && selectedNode?.type.name === SF_SHORTCODE_REF_NODE
    ? currentEditor.chain().focus().insertContentAt({
        from: selection.position,
        to: selection.position + selectedNode.nodeSize
      }, content).run()
    : currentEditor.chain().focus().insertContent(content).run()
  if (changed) {
    referenceCache.delete(`${kind}:${id || 0}`)
    toast.add({
      color: 'success', icon: 'i-lucide-check',
      title: t(selection ? 'composer.shortcodes.updated' : 'composer.shortcodes.inserted'), duration: 5000
    })
  }
  return changed
}

function deleteReference(selection: EditorReferenceSelectionV1) {
  const currentEditor = editor.value
  if (!currentEditor || props.disabled) return
  const node = currentEditor.state.doc.nodeAt(selection.position)
  if (!node || node.type.name !== SF_SHORTCODE_REF_NODE) return
  const changed = currentEditor.chain().focus().deleteRange({
    from: selection.position,
    to: selection.position + node.nodeSize
  }).run()
  if (changed) {
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('composer.shortcodes.deleted'), duration: 5000 })
  }
}

function protectedInsertFailureKey(outcome: SFProtectedInsertResult) {
  if (outcome.status === 'selection') return 'composer.shortcodes.selectProtectedBody'
  if (outcome.status === 'invalid') {
    // 空体不再是插入期失败：插入允许空脚手架，改由发布门负责。
    if (outcome.reasons.includes('placement')) {
      return outcome.hadPlacementViolation
        ? 'composer.shortcodes.invalidProtectedPlacementExisting'
        : 'composer.shortcodes.invalidProtectedPlacement'
    }
    if (outcome.reasons.includes('commentOnly')) return 'composer.shortcodes.invalidProtectedCommentOnly'
    if (outcome.reasons.includes('count')) return 'composer.shortcodes.invalidProtectedCount'
    if (outcome.reasons.includes('depth')) return 'composer.shortcodes.invalidProtectedDepth'
  }
  return 'composer.shortcodes.invalidProtectedBody'
}

function upsertProtected(kind: EditorProtectedKind, selection?: EditorShortcodeSelectionV1) {
  const currentEditor = editor.value
  const attrs = sforumProtectedNodeAttributes(kind, props.imageSurface)
  if (!currentEditor || !attrs || props.disabled) return false
  // 候选事务只有 valid 才派发；任何失败路径都不触碰文档与 v-model。
  const outcome = buildSForumProtectedTransaction({
    state: currentEditor.state,
    attrs,
    resourceKind: props.imageSurface,
    selection
  })
  if (outcome.status !== 'ready') {
    shortcodeInlineError.value = t(protectedInsertFailureKey(outcome))
    return false
  }
  currentEditor.view.dispatch(outcome.tr.scrollIntoView())
  toast.add({
    color: 'success', icon: 'i-lucide-check',
    title: t(selection ? 'composer.shortcodes.updated' : 'composer.shortcodes.inserted'), duration: 5000
  })
  return true
}

function deleteProtected(selection: EditorProtectedSelectionV1) {
  const currentEditor = editor.value
  if (!currentEditor || props.disabled) return
  const node = currentEditor.state.doc.nodeAt(selection.position)
  if (!node || node.type.name !== SF_SHORTCODE_BLOCK_NODE) return
  const changed = currentEditor.chain().focus().deleteRange({
    from: selection.position,
    to: selection.position + node.nodeSize
  }).run()
  if (changed) toast.add({ color: 'success', icon: 'i-lucide-check', title: t('composer.shortcodes.deleted'), duration: 5000 })
}

function unwrapProtected(selection: EditorProtectedSelectionV1) {
  const currentEditor = editor.value
  if (!currentEditor || props.disabled) return
  const changed = currentEditor.commands.command(({ tr, dispatch }) => {
    const node = tr.doc.nodeAt(selection.position)
    if (!node || node.type.name !== SF_SHORTCODE_BLOCK_NODE) return false
    tr.replaceWith(selection.position, selection.position + node.nodeSize, node.content)
    dispatch?.(tr.scrollIntoView())
    return true
  })
  if (changed) toast.add({ color: 'success', icon: 'i-lucide-unlock', title: t('composer.shortcodes.unwrapped'), duration: 5000 })
}

async function runExtensionAction(commandID: string, selection?: EditorShortcodeSelectionV1) {
  const currentEditor = editor.value
  const admitted = admittedCommands.value[commandID]
  if (!currentEditor || !admitted || props.disabled) return
  shortcodeInlineError.value = ''
  const context: EditorL2CommandContextV1 = {
    editor: currentEditor,
    disabled: props.disabled,
    resourceKind: props.imageSurface,
    selection,
    host: {
      openReferenceDialog: resources => openReferenceDialog(resources, selection),
      upsertReference: (kind, id) => upsertReference(
        kind,
        id,
        selection && !('protected' in selection) ? selection : undefined
      ),
      upsertProtected: kind => upsertProtected(kind, selection),
      deleteReference: () => {
        if (!selection) return false
        if ('protected' in selection) deleteProtected(selection)
        else deleteReference(selection)
        return true
      },
      unwrapProtected: () => {
        if (!selection || !('protected' in selection)) return false
        unwrapProtected(selection)
        return true
      },
      focusEditor: () => currentEditor.commands.focus()
    }
  }
  try {
    await admitted.handler(context)
  } catch {
    shortcodeInlineError.value = t('composer.shortcodes.loadError')
  }
}

function setBlockFormat(format: SFEditorBlockFormat) {
  runEditorCommand(currentEditor => {
    if (format === 'heading-2') return currentEditor.chain().focus().setHeading({ level: 2 }).run()
    if (format === 'heading-3') return currentEditor.chain().focus().setHeading({ level: 3 }).run()
    return currentEditor.chain().focus().setParagraph().run()
  })
}

function submitContent() {
  if (props.disabled || props.submitDisabled || pendingUploadCount.value > 0) {
    return
  }
  emit('submit', currentPayload.value)
}
</script>

<template>
  <div
    :class="editorClass"
    :style="{ '--sf-editor-min-height': editorMinHeight }"
  >
    <SFEditorToolbar
      v-if="!compact"
      :preset="preset"
      :disabled="disabled"
      :can-undo="canUndo()"
      :can-redo="canRedo()"
      :active="toolbarActive"
      :block-format="blockFormat"
      :view-mode="viewMode"
      :extension-tools="extensionTools"
      @action="runToolbarAction"
      @extension-action="runExtensionAction"
      @insert-table="onInsertTable"
      @block-format="setBlockFormat"
      @view-mode="viewMode = $event"
    />

    <SFEditorImageUploadModal
      :open="imageDialogOpen"
      :disabled="disabled"
      @update:open="onImageDialogOpenChange"
      @select="onImageFilesSelected"
    />

    <SFShortcodeReferenceDialog
      :open="referenceDialog.open"
      :resources="referenceDialog.resources"
      :selection="referenceDialog.selection"
      :disabled="disabled"
      @update:open="onReferenceDialogOpenChange"
      @resolve="resolveReferenceDialog"
    />

    <SFEditorImageMenu
      v-if="editor && preset === 'full'"
      :editor="editor"
      :disabled="disabled"
    />

    <SFEditorTableMenu
      v-if="editor && preset === 'full'"
      :editor="editor"
      :disabled="disabled"
      :enabled="viewMode === 'write'"
    />

    <div
      v-if="shortcodeInlineError"
      class="mx-3 mt-3 flex items-start gap-2 rounded-md border border-error/30 bg-error/5 px-3 py-2 text-sm text-error"
      role="alert"
    >
      <UIcon name="i-lucide-triangle-alert" class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>{{ shortcodeInlineError }}</span>
      <UButton
        type="button"
        color="error"
        variant="ghost"
        size="xs"
        icon="i-lucide-x"
        class="ml-auto shrink-0"
        :aria-label="t('common.close')"
        :title="t('common.close')"
        @click="() => { shortcodeInlineError = '' }"
      />
    </div>

    <div class="sf-editor__body">
      <ClientOnly>
        <EditorContent
          v-show="viewMode === 'write'"
          :editor="editorForContent"
        />
        <template #fallback>
          <div class="sf-editor__loading">
            {{ t('composer.editor.loading') }}
          </div>
        </template>
      </ClientOnly>

      <div
        v-show="viewMode === 'preview'"
        class="sf-editor__preview"
        v-highlight
        v-html="sanitizeHtml(currentPayload.html)"
      />
    </div>

    <div class="sf-editor__footer">
      <template v-if="compact">
        <span class="sf-editor__compact-support">
          <UIcon name="i-lucide-file-code-2" class="size-3.5" aria-hidden="true" />
          {{ resolvedSupportLabel }}
        </span>
        <div class="sf-editor__compact-actions">
          <SFButton
            v-if="cancelLabel"
            type="button"
            variant="ghost"
            size="sm"
            :disabled="disabled"
            @click="emit('cancel')"
          >
            {{ cancelLabel }}
          </SFButton>
          <SFButton
            size="sm"
            :disabled="disabled || submitDisabled || currentPayload.isEmpty || currentPayload.pendingUploadCount > 0"
            @click="submitContent"
          >
            <template #leading>
              <UIcon name="i-lucide-send" class="size-3.5" aria-hidden="true" />
            </template>
            {{ resolvedSubmitLabel }}
          </SFButton>
        </div>
      </template>
      <template v-else-if="preset === 'basic-field'">
        <span
          class="sf-editor__status"
          :class="{ 'sf-editor__status--error': error }"
        >
          {{ footerText }}
        </span>
      </template>
      <template v-else>
        <span
          class="sf-editor__status"
          :class="{ 'sf-editor__status--error': error }"
        >
          {{ footerText }}
        </span>
        <div class="sf-editor__meta">
          <span>{{ t('composer.editor.wordCount', { count: currentPayload.wordCount }) }}</span>
          <span>{{ t('composer.editor.structuredDocument') }}</span>
        </div>
        <SFButton
          v-if="submitVisible"
          size="sm"
          :disabled="disabled || submitDisabled || currentPayload.isEmpty || currentPayload.pendingUploadCount > 0"
          @click="submitContent"
        >
          {{ resolvedSubmitLabel }}
        </SFButton>
      </template>
    </div>
  </div>
</template>
