import { provide, type ComputedRef, type InjectionKey, type Ref } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useForumApi } from '~/composables/forum/useForumApi'
import {
  topicCommentEditorContentIsMeaningful,
  useTopicCommentSubmission
} from '~/composables/forum/useTopicCommentSubmission'
import { sforumDocumentPublishIssue, sforumPublishFailureKey } from '~/utils/editor/shortcodes'
import {
  advancedReplyDraftStorageKey,
  flattenCommentTree,
  forumContentFromEditorPayload,
  forumEditorInitialContent,
  type ForumComment,
  type ForumEditableContentSource,
  type ForumTopicDetail
} from '~/utils/forum/forumTaxonomy'
import type { SFEditorContentPayload } from '~/utils/sfEditor'
import type { PostRiskVerificationController } from '~/utils/forum/postRiskVerification'

export type TopicCommentComposerMode = 'advanced' | 'reply' | 'edit'

/**
 * post_risk 人机验证控制器的注入键（P3-3）。
 *
 * 控制器由本 composable 持有（它同时是回帖提交的发起方，token 必须与请求同源），
 * 抽屉只负责渲染 widget。走 provide/inject 而不是 prop，是因为话题详情页
 * `SFTopicShowPage.vue` 是 legacy 行数上限文件（1118 行，棘轮只降不升），
 * 新增一行 prop 绑定就会顶破架构门；注入键把这份接线留在本文件里。
 */
export const TOPIC_COMMENT_POST_RISK_KEY: InjectionKey<PostRiskVerificationController> =
  Symbol('sforum.topic-comment.post-risk')

export type TopicCommentComposerContext = {
  author: string
  excerpt: string
  floorLabel?: string
  href?: string
}

type TopicCommentComposerDrawerOptions = {
  topic: Ref<ForumTopicDetail | null | undefined>
  comments: ComputedRef<ForumComment[]>
  currentUserId: ComputedRef<number | null | undefined>
  legacyParentId: ComputedRef<number>
  refreshComments: () => Promise<unknown>
  commentAuthorName: (comment: ForumComment) => string
  commentFloor: (comment: ForumComment) => string
  /** 回复成功后定位到新评论（时间流末尾或跨页），由详情页实现。 */
  focusCreatedComment?: (comment: ForumComment) => Promise<void> | void
}

export function useLegacyTopicCommentComposerParent() {
  const route = useRoute()
  return computed(() => {
    if (route.query.compose !== 'advanced') return 0
    const raw = Array.isArray(route.query.parent) ? route.query.parent[0] : route.query.parent
    const id = Number(raw)
    return Number.isInteger(id) && id > 0 ? id : 0
  })
}

export function useTopicCommentComposerDrawer(options: TopicCommentComposerDrawerOptions) {
  const route = useRoute()
  const router = useRouter()
  const { t } = useI18n()
  const forumApi = useForumApi()
  const toast = useToast()

  const mode = ref<TopicCommentComposerMode | null>(null)
  const editorVersion = ref(0)
  const replyingTo = ref<ForumComment | null>(null)
  const replyParentId = ref<number | null>(null)
  const editingComment = ref<ForumComment | null>(null)
  const editingMarkdown = ref('')
  // editor-document 的 rawContent 是 Tiptap JSON，只能经 initialContent 加载。
  const editingInitialContent = ref<string | Record<string, unknown>>('')
  const editingRevision = ref(0)
  const editingSourceLoading = ref(false)
  const editingSubmitting = ref(false)
  const editingError = ref('')
  const editingReason = ref('')
  const editingReasonError = ref('')

  const {
    replyMarkdown,
    replySubmitting,
    replyError,
    showReplyError,
    commentCooldownActive,
    replyDisplayError,
    postRisk,
    submitReply
  } = useTopicCommentSubmission({
    topic: options.topic,
    replyingTo,
    replyParentId,
    refreshComments: options.refreshComments,
    focusCreatedComment: options.focusCreatedComment
  })

  // 抽屉（SFTopicCommentComposerDrawer）用同一份控制器渲染 widget，见 TOPIC_COMMENT_POST_RISK_KEY。
  provide(TOPIC_COMMENT_POST_RISK_KEY, postRisk)

  const open = computed(() => mode.value != null)
  const editingAnotherAuthor = computed(() => Boolean(
    editingComment.value
    && options.currentUserId.value
    && editingComment.value.authorUserId !== options.currentUserId.value
  ))
  const context = computed<TopicCommentComposerContext | null>(() => {
    const comment = mode.value === 'edit' ? editingComment.value : replyingTo.value
    if (comment) {
      return {
        author: options.commentAuthorName(comment),
        excerpt: comment.content.excerpt,
        floorLabel: options.commentFloor(comment),
        href: `#comment-${comment.id}`
      }
    }
    if (mode.value === 'reply' && replyParentId.value) {
      return {
        author: `#${replyParentId.value}`,
        excerpt: t('topicDetail.advancedReplyParent', { id: replyParentId.value }),
        href: `#comment-${replyParentId.value}`
      }
    }
    return null
  })
  const modelValue = computed(() => mode.value === 'edit' ? editingMarkdown.value : replyMarkdown.value)
  const initialContent = computed(() => mode.value === 'edit' ? editingInitialContent.value : undefined)
  const submitting = computed(() => mode.value === 'edit' ? editingSubmitting.value : replySubmitting.value)
  const error = computed(() => mode.value === 'edit'
    ? editingError.value
    : (showReplyError.value ? replyDisplayError.value : ''))
  const editorKey = computed(() => `${mode.value || 'closed'}-${editingComment.value?.id || replyParentId.value || 0}-${editorVersion.value}`)

  function updateModelValue(value: string) {
    if (mode.value === 'edit') {
      editingMarkdown.value = value
    } else {
      replyMarkdown.value = value
    }
  }

  function updateReason(value: string) {
    editingReason.value = value
    editingReasonError.value = ''
  }

  function removeReplyDraft() {
    if (!import.meta.client || !options.topic.value) return
    try {
      sessionStorage.removeItem(advancedReplyDraftStorageKey(options.topic.value.id))
    } catch {
      // sessionStorage 不可用时忽略。
    }
  }

  watch(replyMarkdown, (draft) => {
    if (!import.meta.client || !options.topic.value || mode.value === 'edit') return
    try {
      const key = advancedReplyDraftStorageKey(options.topic.value.id)
      if (draft) sessionStorage.setItem(key, draft)
      else sessionStorage.removeItem(key)
    } catch {
      // 草稿持久化失败不阻断编辑。
    }
  })

  function resetEditingState() {
    editingComment.value = null
    editingMarkdown.value = ''
    editingInitialContent.value = ''
    editingRevision.value = 0
    editingError.value = ''
    editingReason.value = ''
    editingReasonError.value = ''
  }

  async function startEdit(comment: ForumComment) {
    if (editingSourceLoading.value) return
    editingSourceLoading.value = true
    try {
      const source: ForumEditableContentSource = await forumApi.getCommentEditSource(comment.id)
      replyingTo.value = null
      replyParentId.value = null
      editingComment.value = comment
      editingMarkdown.value = ''
      editingInitialContent.value = forumEditorInitialContent(source)
      editingRevision.value = source.currentRevision
      editingError.value = ''
      editingReason.value = ''
      editingReasonError.value = ''
      mode.value = 'edit'
      editorVersion.value += 1
    } catch (cause) {
      toast.add({
        color: 'error', duration: 0,
        icon: 'i-lucide-circle-alert',
        title: apiErrorMessage(cause) || t('topicDetail.editFailed')
      })
    } finally {
      editingSourceLoading.value = false
    }
  }

  function startReply(comment: ForumComment, initialDraft = '') {
    resetEditingState()
    replyingTo.value = comment
    replyParentId.value = comment.id
    replyMarkdown.value = initialDraft
    replyError.value = ''
    showReplyError.value = false
    mode.value = 'reply'
    editorVersion.value += 1
  }

  function openAdvancedReply(initialDraft?: string) {
    resetEditingState()
    replyingTo.value = null
    replyParentId.value = null
    replyError.value = ''
    showReplyError.value = false
    if (typeof initialDraft === 'string') {
      replyMarkdown.value = initialDraft
    } else if (import.meta.client && options.topic.value) {
      try {
        replyMarkdown.value = sessionStorage.getItem(advancedReplyDraftStorageKey(options.topic.value.id)) || replyMarkdown.value
      } catch {
        // sessionStorage 不可用时保留当前内存草稿。
      }
    }
    mode.value = 'advanced'
    editorVersion.value += 1
  }

  function cancelReply() {
    replyingTo.value = null
    replyParentId.value = null
    replyMarkdown.value = ''
    replyError.value = ''
    showReplyError.value = false
    removeReplyDraft()
  }

  function close() {
    if (mode.value === 'edit') resetEditingState()
    else cancelReply()
    mode.value = null
  }

  function updateOpen(value: boolean) {
    if (!value) close()
  }

  function dismissError() {
    if (mode.value === 'edit') editingError.value = ''
    else showReplyError.value = false
  }

  async function saveCommentEdit(comment: ForumComment, payload: SFEditorContentPayload) {
    if (payload.pendingUploadCount > 0) return
    const reason = editingReason.value.trim()
    if (editingAnotherAuthor.value && !reason) {
      editingReasonError.value = t('topicDetail.composerDrawer.editReasonRequired')
      return
    }
    const markdown = payload.markdown ?? editingMarkdown.value
    const text = payload.text ?? markdown
    if (!topicCommentEditorContentIsMeaningful(payload, markdown) || editingSubmitting.value) return
    // 空保护块可以插入，但发布前必须有内容（Host 会拒绝空保护体）。
    const protectedIssue = sforumDocumentPublishIssue(payload.native, 'comment')
    if (protectedIssue) {
      editingError.value = t(sforumPublishFailureKey(protectedIssue))
      return
    }

    editingSubmitting.value = true
    editingError.value = ''
    editingReasonError.value = ''
    try {
      await forumApi.updateComment(
        comment.id,
        forumContentFromEditorPayload({
          markdown,
          native: payload.native,
          text,
          attachmentIds: payload.attachmentIds
        }),
        editingRevision.value,
        reason || undefined
      )
      mode.value = null
      resetEditingState()
      await options.refreshComments()
      toast.add({ color: 'success', icon: 'i-lucide-check', title: t('topicDetail.commentUpdated'), duration: 5000 })
    } catch (cause) {
      editingError.value = apiErrorMessage(cause) || t('topicDetail.editFailed')
    } finally {
      editingSubmitting.value = false
    }
  }

  async function submit(payload: SFEditorContentPayload) {
    if (mode.value === 'edit') {
      if (editingComment.value) await saveCommentEdit(editingComment.value, payload)
      return
    }
    const created = await submitReply(payload)
    if (created) {
      mode.value = null
      removeReplyDraft()
    }
  }

  const legacyComposerOpened = ref(false)
  watch(
    [() => options.topic.value?.id || 0, options.comments],
    async ([loadedTopicId]) => {
      if (!import.meta.client || !loadedTopicId || route.query.compose !== 'advanced' || legacyComposerOpened.value) return

      legacyComposerOpened.value = true
      const parentId = options.legacyParentId.value
      if (parentId > 0) {
        const parent = flattenCommentTree(options.comments.value).find(comment => comment.id === parentId)
        if (parent) {
          startReply(parent)
        } else {
          resetEditingState()
          replyingTo.value = null
          replyParentId.value = parentId
          replyMarkdown.value = ''
          mode.value = 'reply'
          editorVersion.value += 1
        }
      } else {
        openAdvancedReply()
      }

      const query = { ...route.query }
      delete query.compose
      delete query.parent
      await router.replace({ path: route.path, query, hash: route.hash })
    },
    { immediate: true, flush: 'post' }
  )

  return {
    mode,
    open,
    context,
    modelValue,
    initialContent,
    submitting,
    error,
    editorKey,
    editingReason,
    editingReasonError,
    editingAnotherAuthor,
    commentCooldownActive,
    replyError,
    showReplyError,
    // post_risk 人机验证状态：由抽屉渲染 widget，token 由 submitReply 带进请求体。
    postRisk,
    startEdit,
    startReply,
    openAdvancedReply,
    updateOpen,
    updateModelValue,
    updateReason,
    dismissError,
    submit
  }
}
