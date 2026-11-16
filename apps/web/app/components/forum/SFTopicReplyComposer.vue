<script setup lang="ts">
import type { AltchaWidgetElement } from 'altcha'
import { toValue } from 'vue'
import { useTopicCommentSubmission } from '~/composables/forum/useTopicCommentSubmission'
import { useForumContentLimits } from '~/composables/forum/useForumContentLimits'
import type { ForumComment, ForumTopicDetail } from '~/utils/forum/forumTaxonomy'

type AvatarView = {
  kind: 'uploaded' | 'initials' | 'gravatar' | 'static'
  url: string
  attachmentId?: number | null
  alt: string
}

const props = defineProps<{
  topic: ForumTopicDetail
  refreshComments: () => Promise<unknown>
  actorName?: string
  avatar?: AvatarView | null
  /** 回复成功后定位到新评论（时间流末尾或跨页），与高级回复共用详情页实现。 */
  focusCreatedComment?: (comment: ForumComment) => Promise<void> | void
}>()

const emit = defineEmits<{
  open: [draft: string]
}>()

const { t } = useI18n()
const { limits } = useForumContentLimits()
const replyingTo = ref<ForumComment | null>(null)
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
  topic: toRef(props, 'topic'),
  replyingTo,
  refreshComments: props.refreshComments,
  focusCreatedComment: comment => props.focusCreatedComment?.(comment)
})

// 快捷回复与高级回复抽屉共用同一套 post_risk 形态：只在开关开启（或服务端要求）时渲染。
// 控制器经 props 传入，用 toValue 读取：无论调用方传的是 ref 还是已被解包的值都成立。
const humanVerification = computed(() => ({
  visible: toValue(postRisk.enabled),
  message: toValue(postRisk.fieldMessage),
  challenge: toValue(postRisk.challengeUrl),
  configuration: toValue(postRisk.configuration),
  language: toValue(postRisk.language),
  settings: toValue(postRisk.widgetSettings)
}))

function setAltchaWidget(element: unknown) {
  postRisk.widget.value = (element as AltchaWidgetElement | null) ?? null
}

function cancelReply() {
  replyMarkdown.value = ''
  replyError.value = ''
  showReplyError.value = false
}
</script>

<template>
  <section id="topic-reply-editor" class="sforum-topic-comments__reply">
    <header class="sforum-topic-comments__reply-head">
      <SFAvatar v-if="actorName" :name="actorName" :avatar="avatar" size="sm" />
      <span class="sforum-topic-comments__reply-head-copy">
        <strong>{{ t('topicDetail.replyTitle') }}</strong>
        <small v-if="actorName">{{ t('topicDetail.replyAs', { name: actorName }) }}</small>
      </span>
      <button type="button" class="sforum-topic-comments__reply-advanced" @click="emit('open', replyMarkdown)">
        {{ t('topicDetail.advancedReply') }}
      </button>
    </header>

    <LazySFEditor
      image-surface="comment"
      v-model="replyMarkdown"
      compact
      :rows="5"
      :placeholder="t('topicDetail.replyPlaceholder')"
      :submit-label="replySubmitting ? t('topicDetail.submitting') : t('topicDetail.submitReply')"
      :cancel-label="t('topicDetail.cancel')"
      :support-label="t('topicDetail.markdownSupported')"
      :disabled="replySubmitting"
      :submit-disabled="commentCooldownActive"
      :enable-mentions="limits.mentionsEnabled"
      @cancel="cancelReply"
      @submit="submitReply"
    />

    <div v-if="humanVerification.visible" class="sforum-topic-comments__verification">
      <div class="sforum-topic-comments__verification-head">
        <label id="topic-reply-human-verification-label">{{ t('composer.humanVerificationLabel') }}</label>
        <small>{{ t('composer.humanVerificationHint') }}</small>
      </div>
      <ClientOnly>
        <altcha-widget
          :ref="setAltchaWidget"
          class="sf-forum-altcha"
          :challenge="humanVerification.challenge"
          :configuration="humanVerification.configuration"
          :auto="humanVerification.settings.auto"
          :display="humanVerification.settings.display"
          :language="humanVerification.language"
          :type="humanVerification.settings.type"
          :workers="humanVerification.settings.workers"
          :aria-invalid="humanVerification.message ? 'true' : undefined"
          :aria-describedby="humanVerification.message ? 'topic-reply-human-verification-error' : undefined"
          aria-labelledby="topic-reply-human-verification-label"
          @verified="postRisk.handleVerified"
          @expired="postRisk.reset"
          @statechange="postRisk.handleStateChange"
        />
        <template #fallback>
          <small class="sforum-topic-comments__verification-hint">{{ t('auth.humanVerificationLoading') }}</small>
        </template>
      </ClientOnly>
      <p
        v-if="humanVerification.message"
        id="topic-reply-human-verification-error"
        class="sforum-topic-comments__verification-error"
        role="alert"
      >
        {{ humanVerification.message }}
      </p>
    </div>

    <SFAlert
      v-if="showReplyError"
      variant="danger"
      :title="replyDisplayError"
      :closable="!commentCooldownActive"
      class="mx-3 mb-3"
      @close="showReplyError = false"
    />
  </section>
</template>

<style scoped>
.sforum-topic-comments__verification {
  display: grid;
  gap: 7px;
  margin: 0 13px 12px;
  border: 1px solid var(--sf-public-border, var(--sf-border));
  border-radius: 6px;
  padding: 10px 12px;
  background: var(--sf-public-surface-muted, var(--sf-muted));
}

.sforum-topic-comments__verification-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  justify-content: space-between;
  gap: 4px 10px;
}

.sforum-topic-comments__verification-head label {
  color: var(--sf-public-text, var(--sf-fg));
  font-size: 12px;
  font-weight: 700;
}

.sforum-topic-comments__verification-head small,
.sforum-topic-comments__verification-hint {
  color: var(--sf-public-text-muted, var(--sf-fg-tertiary));
  font-size: 11px;
  line-height: 1.5;
}

.sforum-topic-comments__verification-error {
  margin: 0;
  color: var(--sf-danger, #dc2626);
  font-size: 11px;
  font-weight: 650;
}
</style>
