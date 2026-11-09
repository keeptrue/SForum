/**
 * 论坛正文的渲染内容与编辑器输入。
 *
 * 从 forumTaxonomy 抽出：这些类型只回答「正文怎么存、怎么还原、怎么提交」，
 * 与分类 / 标签 / 排序等站点级分类学无关。forumTaxonomy 用 `export *` 再导出，
 * 既有 `~/utils/forum/forumTaxonomy` import 路径全部保持不变。
 */

export type ForumRenderedContent = {
  id: number
  htmlContent: string
  plainText: string
  excerpt: string
  renderVersion: string
}

export type ForumEditableContentSource = {
  rawContent: string
  sourceFormat: 'markdown' | 'html' | 'editor-document'
  editorType: string
  editorVersion: string
  contentHash: string
  attachmentIds: number[]
  currentRevision: number
}

export type ForumProtectedRenderedContent = ForumRenderedContent
  & Pick<ForumEditableContentSource, 'rawContent' | 'sourceFormat' | 'editorType' | 'editorVersion' | 'contentHash'>

/**
 * 将 API 保存格式还原为 Tiptap 可接受的初始内容。
 * editor-document 必须按 JSON 文档加载，不能作为 Markdown 字符串显示或回存。
 */
export function forumEditorInitialContent(
  content: Pick<ForumEditableContentSource, 'rawContent' | 'sourceFormat'>
): string | Record<string, unknown> {
  if (content.sourceFormat !== 'editor-document') {
    return content.rawContent
  }

  try {
    const parsed = JSON.parse(content.rawContent)
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // 非法历史数据保留原文，避免静默清空；服务端正常数据不会走到这里。
  }

  return content.rawContent
}

// 编辑器提交给后端的正文输入。
// markdown/html：经典路径；editor-document：native Tiptap JSON 经 Host Accept 管线。
export type ForumContentInput = {
  rawContent: string
  sourceFormat?: 'markdown' | 'html' | 'editor-document'
  editorType?: string
  editorVersion?: string
  attachmentIds?: number[]
}

/** 从 SFEditor payload 构造 Host 可验收的正文输入（优先 editor-document）。 */
export function forumContentFromEditorPayload(payload: {
  markdown?: string
  native?: unknown
  text?: string
  attachmentIds?: number[]
}): ForumContentInput {
  if (payload.native && typeof payload.native === 'object') {
    return {
      rawContent: JSON.stringify(payload.native),
      sourceFormat: 'editor-document',
      editorType: 'tiptap',
      editorVersion: 'sf-editor-v1',
      attachmentIds: payload.attachmentIds || []
    }
  }
  return {
    rawContent: payload.markdown || payload.text || '',
    sourceFormat: 'markdown',
    editorType: 'tiptap',
    editorVersion: 'sf-editor-v1'
  }
}
