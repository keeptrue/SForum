import {
  buildForumCommentQuery,
  buildForumSearchQuery,
  buildForumTopicQuery,
  forumCommentExtensionActionRequest,
  forumTopicExtensionActionRequest,
  type ForumCategoryGroup,
  type ForumComment,
  type ForumCommentExtensionAction,
  type ForumCommentList,
  type ForumCommentListQuery,
  type ForumCommentPage,
  type ForumContentInput,
  type ForumEditableContentSource,
  type ForumTag,
  type ForumTopicAction,
  type ForumTopicActionKey,
  type ForumTopicContributionTimeline,
  type ForumTopicDetail,
  type ForumTopicExtensionAction,
  type ForumComposerToolbarAction,
  type ForumTopicFilters,
  type ForumTopicCreateInput,
  type ForumTopicList,
  type ForumTopicSearchFilters,
  type ForumTopicUpdateInput,
  forumTopicExtensionActionRequestPath
} from '~/utils/forum/forumTaxonomy'
import type { ForumCommentRevisionState } from '~/utils/forum/commentLiveDecision'
import type { ForumReactionRoster, ForumReactionState } from '~/utils/forum/forumReaction'

export function useForumApi() {
  const { request } = useApiClient()
  const serverReadOptions = { serverInternal: import.meta.server }

  type ReadOptions = {
    timeout?: number
    serverInternal?: boolean
  }

  function listCategoryGroups(options: ReadOptions = {}) {
    return request<ForumCategoryGroup[]>('/category-groups', { ...serverReadOptions, ...options })
  }

  function listTags(options: ReadOptions = {}) {
    return request<ForumTag[]>('/tags', { ...serverReadOptions, ...options })
  }

  function listTopics(filters: ForumTopicFilters = {}, options: ReadOptions = {}) {
    return request<ForumTopicList>(
      pathWithQuery('/topics', buildForumTopicQuery(filters)),
      { ...serverReadOptions, ...options }
    )
  }

  // 关键词检索走选定 search.provider，默认站内引擎支持中英文全文与模糊搜索。
  function searchTopics(filters: ForumTopicSearchFilters) {
    return request<ForumTopicList>(pathWithQuery('/search', buildForumSearchQuery(filters)), serverReadOptions)
  }

  function getTopic(topicId: number) {
    return request<ForumTopicDetail>(`/topics/${topicId}`, serverReadOptions)
  }

  function getTopicEditSource(topicId: number) {
    return request<ForumEditableContentSource>(`/topics/${topicId}/edit-source`, serverReadOptions)
  }

  // 按 slug 查询主题：仅 "纯 slug" URL 模式使用，对应后端 GET /topics/by-slug/:slug。
  function getTopicBySlug(slug: string) {
    return request<ForumTopicDetail>(`/topics/by-slug/${encodeURIComponent(slug)}`, serverReadOptions)
  }

  // 公开贡献时间线：作者/编辑/恢复 header，不含正文与 reason。
  function listTopicContributionTimeline(
    topicId: number,
    query: { after?: string, perPage?: number } = {}
  ) {
    const params: Record<string, string> = {}
    if (query.after?.trim()) {
      params.after = query.after.trim()
    }
    if (query.perPage && query.perPage > 0) {
      params.perPage = String(query.perPage)
    }
    return request<ForumTopicContributionTimeline>(
      pathWithQuery(`/topics/${topicId}/contribution-timeline`, params),
      serverReadOptions
    )
  }

  function listTopicComments(topicId: number, query: ForumCommentListQuery = {}) {
    return request<ForumCommentList>(
      pathWithQuery(`/topics/${topicId}/comments`, buildForumCommentQuery(query)),
      serverReadOptions
    )
  }

function getCommentEditSource(commentId: number) {
    return request<ForumEditableContentSource>(`/comments/${commentId}/edit-source`, serverReadOptions)
  }

  // 评论区修订状态：SSE 信号的对账事实来源，同时是 SSE 不可用时的轮询降级端点。
  // 与列表同口径（不可见主题 404），且不走评论列表缓存，因此不会返回过期修订号。
  function getCommentRevision(topicId: number) {
    return request<ForumCommentRevisionState>(`/topics/${topicId}/comments/revision`, serverReadOptions)
  }

  // 反查 commentId 在 flat 视图分页下所属的页码；SSR 阶段调用以零闪屏定位 #comment-{id}。
  // 评论不存在/软删/跨主题统一 404，调用方捕获后降级到 page=1。
  function resolveCommentPage(topicId: number, commentId: number) {
    return request<ForumCommentPage>(
      `/topics/${topicId}/comments/${commentId}/page`,
      serverReadOptions
    )
  }

  function createTopicComment(topicId: number, content: ForumContentInput, parentId?: number | null) {
    return request<ForumComment>(`/topics/${topicId}/comments`, {
      method: 'POST',
      body: { parentId: parentId ?? null, content }
    })
  }

  function updateComment(commentId: number, content: ForumContentInput, expectedRevision: number, reason?: string) {
	return request<ForumComment>(`/comments/${commentId}`, {
	  method: 'PATCH',
	  body: { content, expectedRevision, ...(reason ? { reason } : {}) }
    })
  }

  function deleteComment(commentId: number) {
    return request<ForumComment>(`/comments/${commentId}`, { method: 'DELETE' })
  }

  function listCommentReplies(commentId: number) {
    return request<ForumComment[]>(`/comments/${commentId}/replies`, serverReadOptions)
  }

  function createTopic(input: ForumTopicCreateInput) {
    const {
      rawContent,
      sourceFormat,
      editorType,
      editorVersion,
      attachmentIds,
      ...topicInput
    } = input

    return request<ForumTopicDetail>('/topics', {
      method: 'POST',
      body: {
        ...topicInput,
        content: {
          rawContent,
          sourceFormat,
          editorType,
          editorVersion,
          attachmentIds
        }
      }
    })
  }

  function updateTopic(topicId: number, input: ForumTopicUpdateInput) {
    return request<ForumTopicDetail>(`/topics/${topicId}`, {
      method: 'PATCH',
      body: input
    })
  }

  function deleteTopic(topicId: number) {
    return request<ForumTopicDetail>(`/topics/${topicId}`, { method: 'DELETE' })
  }

  function applyTopicAction(topicId: number, action: ForumTopicActionKey) {
    return request<ForumTopicAction>(`/topics/${topicId}/${action}`, { method: 'POST' })
  }

  // 点赞：四个切换端点 + 两个公开名单端点。
  function likeTopic(topicId: number) {
    return request<ForumReactionState>(`/topics/${topicId}/like`, { method: 'POST' })
  }

  function unlikeTopic(topicId: number) {
    return request<ForumReactionState>(`/topics/${topicId}/like`, { method: 'DELETE' })
  }

  function likeComment(commentId: number) {
    return request<ForumReactionState>(`/comments/${commentId}/like`, { method: 'POST' })
  }

  function unlikeComment(commentId: number) {
    return request<ForumReactionState>(`/comments/${commentId}/like`, { method: 'DELETE' })
  }

  function listTopicLikes(topicId: number, limit = 20, offset = 0) {
    return request<ForumReactionRoster>(`/topics/${topicId}/likes?limit=${limit}&offset=${offset}`, serverReadOptions)
  }

  function listCommentLikes(commentId: number, limit = 20, offset = 0) {
    return request<ForumReactionRoster>(`/comments/${commentId}/likes?limit=${limit}&offset=${offset}`, serverReadOptions)
  }

  function applyTopicExtensionAction(topicId: number, action: ForumTopicExtensionAction) {
    const input = forumTopicExtensionActionRequest(action, topicId)
    if (!input) {
      throw new Error('Invalid topic extension action')
    }
    return request<unknown>(input.path, {
      method: input.method,
      body: input.body
    })
  }

  function applyCommentExtensionAction(
    topicId: number,
    commentId: number,
    action: ForumCommentExtensionAction
  ) {
    const input = forumCommentExtensionActionRequest(action, topicId, commentId)
    if (!input) {
      throw new Error('Invalid comment extension action')
    }
    return request<unknown>(input.path, {
      method: input.method,
      body: input.body
    })
  }

  function listComposerToolbarActions() {
    return request<ForumComposerToolbarAction[]>('/composer/toolbar')
  }

  function applyComposerToolbarAction(action: ForumComposerToolbarAction) {
    const path = forumTopicExtensionActionRequestPath(action)
    if (!path) {
      throw new Error('Invalid composer toolbar action')
    }
    return request<unknown>(path, {
      method: action.method,
      body: {}
    })
  }

  return {
    listCategoryGroups,
    listTags,
    listTopics,
    searchTopics,
    getTopic,
    getTopicEditSource,
    getTopicBySlug,
    listTopicContributionTimeline,
    listTopicComments,
    getCommentEditSource,
    getCommentRevision,
    resolveCommentPage,
    createTopicComment,
    updateComment,
    deleteComment,
    listCommentReplies,
    createTopic,
    updateTopic,
    deleteTopic,
    applyTopicAction,
    applyTopicExtensionAction,
    likeTopic,
    unlikeTopic,
    likeComment,
    unlikeComment,
    listTopicLikes,
    listCommentLikes,
    applyCommentExtensionAction,
    listComposerToolbarActions,
    applyComposerToolbarAction
  }
}

function pathWithQuery(path: string, query: Record<string, string>) {
  const params = new URLSearchParams(query)
  const queryString = params.toString()
  return queryString ? `${path}?${queryString}` : path
}
