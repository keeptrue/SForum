import type { Ref } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useForumApi } from '~/composables/forum/useForumApi'
import { useForumCooldownError } from '~/composables/forum/useForumCooldownError'
import { usePostRiskVerification } from '~/composables/forum/usePostRiskVerification'
import {
  forumContentFromEditorPayload,
  type ForumComment,
  type ForumTopicDetail
} from '~/utils/forum/forumTaxonomy'
import { sforumDocumentPublishIssue, sforumPublishFailureKey } from '~/utils/editor/shortcodes'

type TopicCommentSubmissionOptions = {
  topic: Ref<ForumTopicDetail | null | undefined>
  replyingTo: Ref<ForumComment | null>
  /** 兼容旧高级回复链接：目标评论不在当前页时仍保留服务端 parentId。 */
  replyParentId?: Ref<number | null>
  refreshComments: () => Promise<unknown>
  /**
   * 回复成功后的定位回调。flat 评论流是时间流：新回复永远落在列表末尾，
   * 不再插在被回复评论下面，所以必须由页面把视口带到新评论（跨页时先跳页）。
   */
  focusCreatedComment?: (comment: ForumComment) => Promise<void> | void
}

export type TopicCommentEditorContent = {
  markdown?: string
  native?: unknown
  text?: string
  attachmentIds?: number[]
  pendingUploadCount?: number
}

export function topicCommentEditorContentIsMeaningful(
  payload?: TopicCommentEditorContent,
  fallbackMarkdown = ''
) {
  if ([payload?.text, payload?.markdown, fallbackMarkdown]
    .some(value => typeof value === 'string' && value.trim() !== '')) {
    return true
  }

  function containsImage(node: unknown): boolean {
    if (!node || typeof node !== 'object') return false
    const candidate = node as { type?: unknown, content?: unknown }
    if (candidate.type === 'image') return true
    return Array.isArray(candidate.content) && candidate.content.some(containsImage)
  }

  return containsImage(payload?.native)
}

export function useTopicCommentSubmission(options: TopicCommentSubmissionOptions) {
  const { t } = useI18n()
  const forumApi = useForumApi()
  const toast = useToast()
  const replyMarkdown = ref('')
  const replySubmitting = ref(false)
  const replyError = ref('')
  const showReplyError = ref(false)
  const {
    active: commentCooldownActive,
    message: commentCooldownMessage,
    capture: captureCommentCooldown
  } = useForumCooldownError('comment')
  // post_risk 人机验证（P3-3）：回帖与发帖同口径，token 随请求体一起提交。
  const postRisk = usePostRiskVerification()
  const replyDisplayError = computed(() => commentCooldownMessage.value || replyError.value)

  async function submitReply(payload?: TopicCommentEditorContent) {
    if (
      !options.topic.value
      || replySubmitting.value
      || commentCooldownActive.value
      || (payload?.pendingUploadCount || 0) > 0
    ) return
    const markdown = payload?.markdown ?? replyMarkdown.value
    if (!topicCommentEditorContentIsMeaningful(payload, markdown)) return
    // 空保护块在编辑期合法（脚手架），但发布前必须有内容：Host 会拒绝空保护体。
    const protectedIssue = sforumDocumentPublishIssue(payload?.native, 'comment')
    if (protectedIssue) {
      replyError.value = t(sforumPublishFailureKey(protectedIssue))
      showReplyError.value = true
      return
    }

    replySubmitting.value = true
    replyError.value = ''
    showReplyError.value = false
    try {
      const created = await forumApi.createTopicComment(
        options.topic.value.id,
        forumContentFromEditorPayload({
          markdown,
          native: payload?.native,
          text: payload?.text,
          attachmentIds: payload?.attachmentIds
        }),
        options.replyParentId?.value ?? options.replyingTo.value?.id,
        postRisk.payload()
      )
      postRisk.settle()
      replyMarkdown.value = ''
      options.replyingTo.value = null
      if (options.replyParentId) {
        options.replyParentId.value = null
      }
      if (created.status === 'pending') {
        toast.add({ color: 'primary', icon: 'i-lucide-clock-3', title: t('topicDetail.replySubmittedForReview'), duration: 5000 })
      } else {
        await options.refreshComments()
        toast.add({ color: 'success', icon: 'i-lucide-check', title: t('topicDetail.replyPosted'), duration: 5000 })
        // 时间流下新回复在列表末尾：滚动与跨页跳转由页面注入的 focusCreatedComment 承担。
        // 定位只是 UX 增强，失败不能反过来把已成功的发布报成失败。
        try {
          await options.focusCreatedComment?.(created)
        } catch {
          // 忽略定位失败。
        }
      }
      return created
    } catch (error) {
      // 人机验证失败：提示挂在 widget 旁（capture 已本地化），不再叠加通用错误。
      if (postRisk.capture(error)) {
        replyError.value = ''
        showReplyError.value = false
        return null
      }
      replyError.value = captureCommentCooldown(error) ? '' : apiErrorMessage(error) || t('topicDetail.replyFailed')
      showReplyError.value = true
      return null
    } finally {
      replySubmitting.value = false
    }
  }

  return {
    replyMarkdown,
    replySubmitting,
    replyError,
    showReplyError,
    commentCooldownActive,
    replyDisplayError,
    postRisk,
    submitReply
  }
}
