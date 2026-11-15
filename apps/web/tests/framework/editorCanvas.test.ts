import { describe, expect, it } from 'bun:test'
import { readFileSync } from 'node:fs'

function source(path: string) {
  return readFileSync(new URL(path, import.meta.url), 'utf8')
}

describe('Forum Canvas editor surface', () => {
  const editor = source('../../app/components/SFEditor.vue')
  const toolbar = source('../../app/components/editor/SFEditorToolbar.vue')
  const styles = source('../../app/assets/css/sforum-components.css')
  const contentSemantics = source('../../app/assets/css/sforum-content-semantics.css')
  const mainStyles = source('../../app/assets/css/main.css')
  const editorExtensions = source('../../app/utils/sfEditor.ts')

  it('keeps write and preview while removing source modes and the Unicode emoji picker', () => {
    expect(editor).toContain('<SFEditorToolbar')
    expect(toolbar).toContain("{ action: 'strike' as const")

    expect(toolbar).toContain("export type SFEditorViewMode = 'write' | 'preview'")
    // 视图模式文案走 i18n（RV-28）：源码里必须是键，不是写死的中文。
    expect(toolbar).toContain("t('composer.editor.mode.write')")
    expect(toolbar).toContain("t('composer.editor.mode.preview')")
    expect(toolbar).not.toContain("value: 'markdown'")
    expect(toolbar).not.toContain("value: 'native'")
    expect(editor).toContain('sf-editor__preview')
    expect(editor).not.toContain('sf-editor__source')
    expect(editor).not.toContain('sf-editor__native')
    expect(editor).not.toContain('showEmojiPanel')
    expect(editor).not.toContain('sforumEditorEmojiItems')
    expect(toolbar).not.toContain('i-lucide-smile')
  })

  it('keeps quiet editor focus and the approved responsive canvas geometry', () => {
    expect(styles).not.toContain('.sf-editor:focus-within')
    expect(styles).toContain('padding: 42px 52px 64px;')
    expect(styles).toContain('padding: 28px 22px 52px;')
    expect(styles).toContain('.sf-editor__toolbar')
    expect(styles).toContain('overflow-x: auto;')
  })

  it('preserves historical sforumEmoji documents and trusted L2 loading', () => {
    expect(editorExtensions).toContain("name: 'sforumEmoji'")
    expect(editorExtensions).toContain('SForumEmoji')
    expect(editor).toContain('useTrustedEditorCatalog()')
    expect(editor).toContain('trustedExtensions: trusted')
  })

  it('guarantees visible list markers in write/preview against Tailwind Preflight', () => {
    // Tailwind Preflight resets ol/ul/menu to list-style:none; the shared
    // content-semantics contract must explicitly restore disc/decimal on every
    // content surface so markers stay visible in both write and preview modes.
    expect(contentSemantics).toContain('list-style-type: disc;')
    expect(contentSemantics).toContain('list-style-type: decimal;')
    expect(contentSemantics).toContain('padding-left: 0.375em;')

    // The contract covers the contenteditable write surface, the client
    // preview surface, and the formal .sf-prose body surface in one source of
    // truth (sf-prose keeps :where() zero specificity so theme overrides win).
    expect(contentSemantics).toContain('.sf-editor__content ul,')
    expect(contentSemantics).toContain('.sf-editor__preview ul,')
    expect(contentSemantics).toContain('.sf-prose :where(ul)')
    expect(contentSemantics).toContain('.sf-editor__content ol,')
    expect(contentSemantics).toContain('.sf-editor__preview ol,')
    expect(contentSemantics).toContain('.sf-prose :where(ol)')

    // Marker presentation must match the formal body's accent markers
    // (main.css prose-li:marker:text-[color:var(--sf-accent)]) and hr must
    // mirror .sf-prose :where(hr) spacing instead of Preflight's zeroed box.
    expect(contentSemantics).toContain('li::marker')
    expect(contentSemantics).toContain('color: var(--sf-accent);')
    expect(contentSemantics).toContain('.sf-editor__content hr,')
    expect(contentSemantics).toContain('.sf-editor__preview hr')
    expect(contentSemantics).toContain('.sf-editor__content h2,')
    expect(contentSemantics).toContain('.sf-editor__preview blockquote,')
    expect(contentSemantics).toContain('.sf-prose :where(pre)')
    expect(contentSemantics).toContain('ul:has(input[type="checkbox"])')
    expect(contentSemantics).toContain('.sf-prose :where(hr)')
    expect(mainStyles).toContain('prose-li:marker:text-[color:var(--sf-accent)]')
  })

  it('keeps editor container geometry and editable-state concerns out of the semantics contract', () => {
    // The shared contract must not absorb editor geometry, focus styling, or
    // theme overrides; those stay in sforum-components.css / theme CSS.
    expect(contentSemantics).not.toContain('padding: 42px 52px 64px;')
    expect(contentSemantics).not.toContain('--sf-editor-min-height')
    expect(contentSemantics).not.toContain('focus-within')
    expect(contentSemantics).not.toContain('ProseMirror-selectednode')
  })

  it('dispatches protected shortcode inserts only for a valid candidate transaction', () => {
    // Tiptap 对非链式 commands.command 无条件 view.dispatch(tr)，校验前置必须落在
    // 独立事务上：upsertProtected 只在 buildSForumProtectedTransaction 返回 ready
    // 后才允许 dispatch，任何失败分支都不触碰文档。
    expect(editor).toContain('buildSForumProtectedTransaction')
    expect(editor).toContain("outcome.status !== 'ready'")
    expect(editor).toContain('view.dispatch(outcome.tr.scrollIntoView())')
    // 新插入引用须经容器前置校验（改型既有节点除外），切断引用落进列表项的
    // 投毒口。
    expect(editor).toContain('sforumCanInsertReference(currentEditor.state)')
  })
})
