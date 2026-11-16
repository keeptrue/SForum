<script setup lang="ts">
import type { AltchaWidgetElement } from 'altcha'
import { inject, toValue } from 'vue'
import { topicCommentEditorContentIsMeaningful } from '~/composables/forum/useTopicCommentSubmission'
import { useForumContentLimits } from '~/composables/forum/useForumContentLimits'
import { editorDocumentSignature, type SFEditorContentPayload } from '~/utils/sfEditor'
import type {
  TopicCommentComposerContext,
  TopicCommentComposerMode
} from '~/composables/forum/useTopicCommentComposerDrawer'
import { TOPIC_COMMENT_POST_RISK_KEY } from '~/composables/forum/useTopicCommentComposerDrawer'

type AvatarView = {
  kind: 'uploaded' | 'initials' | 'gravatar' | 'static'
  url: string
  attachmentId?: number | null
  alt: string
}

const props = withDefaults(defineProps<{
  open: boolean
  mode: TopicCommentComposerMode
  editorKey: string
  topicTitle: string
  topicExcerpt?: string
  actorName?: string
  avatar?: AvatarView | null
  context?: TopicCommentComposerContext | null
  modelValue: string
  initialContent?: string | Record<string, unknown>
  submitting?: boolean
  submitDisabled?: boolean
  error?: string
  errorClosable?: boolean
  reason?: string
  requireReason?: boolean
  reasonError?: string
}>(), {
  topicExcerpt: '',
  actorName: '',
  avatar: null,
  context: null,
  initialContent: undefined,
  submitting: false,
  submitDisabled: false,
  error: '',
  errorClosable: true,
  reason: '',
  requireReason: false,
  reasonError: ''
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  'update:modelValue': [value: string]
  'update:reason': [value: string]
  'submit': [payload: SFEditorContentPayload]
  'cancel': []
  'dismiss-error': []
}>()

const { t } = useI18n()
const toast = useToast()
const discardPromptOpen = ref(false)
const baselineMarkdown = ref('')
const baselineNativeSignature = ref('')
const baselineCaptured = ref(false)
const currentPayload = shallowRef<SFEditorContentPayload | null>(null)
const { limits } = useForumContentLimits()
const drawerHeight = ref<number | null>(null)
const localContentError = ref('')
const localReasonError = ref('')
let resizeSession: { pointerId: number, startY: number, startHeight: number, drawer: HTMLElement } | null = null

const title = computed(() => {
  if (props.mode === 'edit') return t('topicDetail.composerDrawer.editTitle')
  if (props.mode === 'reply' && props.context) {
    return t('topicDetail.composerDrawer.replyTitle', { name: props.context.author })
  }
  return t('topicDetail.advancedReply')
})

const subtitle = computed(() => {
  if (props.mode === 'edit') {
    return t('topicDetail.composerDrawer.editSubtitle', {
      floor: props.context?.floorLabel || t('topicDetail.composerDrawer.thisReply')
    })
  }
  if (props.mode === 'reply') {
    return t('topicDetail.composerDrawer.replySubtitle', {
      floor: props.context?.floorLabel || t('topicDetail.composerDrawer.thisReply')
    })
  }
  return t('topicDetail.advancedReplySubtitle', { title: props.topicTitle })
})

const icon = computed(() => props.mode === 'edit'
  ? 'i-lucide-pencil'
  : props.mode === 'reply'
    ? 'i-lucide-corner-up-left'
    : 'i-lucide-message-square-text')

const contextLabel = computed(() => {
  if (props.mode === 'edit') return t('topicDetail.composerDrawer.originalReply')
  if (props.mode === 'reply') return t('topicDetail.composerDrawer.replyingTo')
  return t('topicDetail.composerDrawer.topicContext')
})

const contextAuthor = computed(() => props.context?.author || t('topicDetail.authorLabel'))
const contextExcerpt = computed(() => props.context?.excerpt || props.topicExcerpt)
const submitLabel = computed(() => {
  if (props.submitting) {
    return props.mode === 'edit'
      ? t('topicDetail.composerDrawer.saving')
      : t('topicDetail.submitting')
  }
  return props.mode === 'edit' ? t('topicDetail.saveEdit') : t('topicDetail.submitReply')
})
const submitIcon = computed(() => props.mode === 'edit' ? 'i-lucide-save' : 'i-lucide-send')
const dirty = computed(() => baselineCaptured.value
  && currentPayload.value != null
  && (
    currentPayload.value.markdown !== baselineMarkdown.value
    || editorDocumentSignature(currentPayload.value.native) !== baselineNativeSignature.value
  ))
const reasonMissing = computed(() => props.requireReason && !props.reason.trim())
const canSubmit = computed(() => Boolean(
  currentPayload.value
  && topicCommentEditorContentIsMeaningful(currentPayload.value)
  && !props.submitting
  && !props.submitDisabled
  && currentPayload.value.pendingUploadCount === 0
  && (props.mode !== 'edit' || dirty.value)
  && !reasonMissing.value
))
const displayedReasonError = computed(() => props.reasonError || localReasonError.value)
// post_risk：控制器由 useTopicCommentComposerDrawer 注入（它同时是提交发起方，
// token 必须与请求同源）。编辑模式走 PATCH，不需要人机验证。
// toValue 让 ref 与已解包的值都能读。
const injectedPostRisk = inject(TOPIC_COMMENT_POST_RISK_KEY)
if (!injectedPostRisk) {
  // 抽屉只由话题详情页挂载；缺失说明提供方漏挂。宁可显式报错，也不要静默丢掉人机验证
  // ——那样运营会看到「开关已开但没人验证」的假象。
  throw new Error('SFTopicCommentComposerDrawer 需要 useTopicCommentComposerDrawer 提供 post_risk 控制器')
}
const postRisk = injectedPostRisk
const humanVerification = computed(() => ({
  visible: toValue(postRisk.enabled) && props.mode !== 'edit',
  message: toValue(postRisk.fieldMessage),
  challenge: toValue(postRisk.challengeUrl),
  configuration: toValue(postRisk.configuration),
  language: toValue(postRisk.language),
  settings: toValue(postRisk.widgetSettings)
}))

function setAltchaWidget(element: unknown) {
  postRisk.widget.value = (element as AltchaWidgetElement | null) ?? null
}

watch(
  () => [props.open, props.editorKey] as const,
  ([open]) => {
    if (!open) return
    discardPromptOpen.value = false
    baselineMarkdown.value = ''
    baselineNativeSignature.value = ''
    baselineCaptured.value = false
    currentPayload.value = null
    localContentError.value = ''
    localReasonError.value = ''
  }
)

function onOpenUpdate(value: boolean) {
  if (value) {
    emit('update:open', true)
    return
  }
  requestClose()
}

function onContentChange(payload: SFEditorContentPayload) {
  currentPayload.value = payload
  localContentError.value = ''
  if (!baselineCaptured.value) {
    baselineMarkdown.value = payload.markdown
    baselineNativeSignature.value = editorDocumentSignature(payload.native)
    baselineCaptured.value = true
  }
}

watch(() => props.reason, (reason) => {
  if (reason.trim()) localReasonError.value = ''
})

function requestClose() {
  if (props.submitting) return
  if (dirty.value) {
    discardPromptOpen.value = true
    return
  }
  close()
}

function close() {
  discardPromptOpen.value = false
  emit('cancel')
  emit('update:open', false)
}

function submit() {
  if (props.submitting || props.submitDisabled) return
  if (!canSubmit.value || !currentPayload.value) {
    let message = t('composer.editValidation.noChanges')
    if ((currentPayload.value?.pendingUploadCount || 0) > 0) {
      message = t('composer.imageUpload.wait')
    } else if (currentPayload.value?.isEmpty) {
      message = t('composer.checks.body.empty')
      localContentError.value = message
    } else if (props.mode === 'edit' && reasonMissing.value && dirty.value) {
      message = t('topicDetail.composerDrawer.editReasonRequired')
      localReasonError.value = message
    }
    toast.add({
      color: 'warning',
      icon: 'i-lucide-info',
      title: t('composer.editValidation.blocked'),
      description: message,
      duration: 5000
    })
    return
  }
  localContentError.value = ''
  localReasonError.value = ''
  emit('submit', currentPayload.value)
}

function resizeBounds() {
  const viewportHeight = window.innerHeight
  const min = Math.min(360, Math.max(280, Math.round(viewportHeight * 0.45)))
  return { min, max: Math.max(min, viewportHeight - 16) }
}

function setDrawerHeight(value: number, drawer: HTMLElement) {
  const { min, max } = resizeBounds()
  drawerHeight.value = Math.round(Math.min(max, Math.max(min, value)))
  drawer.style.height = `${drawerHeight.value}px`
}

function onResizePointerDown(event: PointerEvent) {
  if (event.button !== 0) return
  const handle = event.currentTarget as HTMLButtonElement
  const drawer = handle.closest('.sf-topic-composer-drawer') as HTMLElement | null
  if (!drawer) return

  resizeSession = {
    pointerId: event.pointerId,
    startY: event.clientY,
    startHeight: drawer.getBoundingClientRect().height,
    drawer
  }
  window.addEventListener('pointermove', onResizePointerMove)
  window.addEventListener('pointerup', onResizePointerEnd, { once: true })
  window.addEventListener('pointercancel', onResizePointerEnd, { once: true })
  event.preventDefault()
}

function onResizePointerMove(event: PointerEvent) {
  if (!resizeSession || resizeSession.pointerId !== event.pointerId) return
  setDrawerHeight(resizeSession.startHeight + resizeSession.startY - event.clientY, resizeSession.drawer)
}

function onResizePointerEnd(event: PointerEvent) {
  if (!resizeSession || resizeSession.pointerId !== event.pointerId) return
  window.removeEventListener('pointermove', onResizePointerMove)
  window.removeEventListener('pointerup', onResizePointerEnd)
  window.removeEventListener('pointercancel', onResizePointerEnd)
  resizeSession = null
}

function onResizeKeydown(event: KeyboardEvent) {
  const drawer = (event.currentTarget as HTMLButtonElement).closest('.sf-topic-composer-drawer') as HTMLElement | null
  if (!drawer) return

  const { min, max } = resizeBounds()
  const current = drawerHeight.value ?? drawer.getBoundingClientRect().height
  const next = event.key === 'ArrowUp'
    ? current + 32
    : event.key === 'ArrowDown'
      ? current - 32
      : event.key === 'Home'
        ? min
        : event.key === 'End'
          ? max
          : null
  if (next == null) return

  event.preventDefault()
  setDrawerHeight(next, drawer)
}

function restoreDrawerHeight() {
  if (drawerHeight.value == null) return
  const drawer = document.querySelector<HTMLElement>('.sf-topic-composer-drawer[data-state="open"]')
  if (drawer) setDrawerHeight(drawerHeight.value, drawer)
}

onBeforeUnmount(() => {
  window.removeEventListener('pointermove', onResizePointerMove)
  window.removeEventListener('pointerup', onResizePointerEnd)
  window.removeEventListener('pointercancel', onResizePointerEnd)
})
</script>

<template>
  <USlideover
    :open="open"
    side="bottom"
    :dismissible="false"
    :close="false"
    :title="title"
    :description="subtitle"
    :ui="{
      overlay: 'sf-topic-composer-drawer__overlay',
      content: 'sf-topic-composer-drawer',
      body: 'sf-topic-composer-drawer__body',
      footer: 'sf-topic-composer-drawer__footer'
    }"
    @update:open="onOpenUpdate"
    @close:prevent="requestClose"
    @after:enter="restoreDrawerHeight"
  >
    <template #title>
      <span class="sf-topic-composer-drawer__header-title">
        <span class="sf-topic-composer-drawer__icon" aria-hidden="true">
          <UIcon :name="icon" />
        </span>
        <span>{{ title }}</span>
      </span>
    </template>

    <template #description>
      {{ subtitle }}
    </template>

    <template #actions>
      <button
        type="button"
        class="sf-topic-composer-drawer__resize"
        :aria-label="t('topicDetail.composerDrawer.resizeHeight')"
        :title="t('topicDetail.composerDrawer.resizeHeightHint')"
        @keydown="onResizeKeydown"
        @pointerdown="onResizePointerDown"
      >
        <span aria-hidden="true" />
      </button>
    </template>

    <template #close>
      <button
        type="button"
        class="sf-topic-composer-drawer__close"
        :aria-label="t('topicDetail.composerDrawer.close')"
        :title="t('topicDetail.composerDrawer.close')"
        :disabled="submitting"
        @click="requestClose"
      >
        <UIcon name="i-lucide-x" aria-hidden="true" />
      </button>
    </template>

    <template #body>
      <div class="sf-topic-composer-drawer__context">
        <div class="sf-topic-composer-drawer__context-meta">
          <strong>{{ contextLabel }}</strong>
          <span>{{ contextAuthor }}</span>
          <a v-if="context?.href" :href="context.href">{{ t('topicDetail.composerDrawer.backToContent') }}</a>
        </div>
        <p>{{ contextExcerpt }}</p>
      </div>

      <SFAlert
        v-if="error"
        variant="danger"
        :title="error"
        :closable="errorClosable"
        @close="emit('dismiss-error')"
      />

      <LazySFEditor
        :key="editorKey"
        :model-value="modelValue"
        :initial-content="initialContent"
        :rows="6"
        image-surface="comment"
        :placeholder="mode === 'edit' ? t('topicDetail.editPlaceholder') : t('topicDetail.replyPlaceholder')"
        :submit-label="submitLabel"
        :submit-visible="false"
        :disabled="submitting"
        :submit-disabled="submitDisabled"
        :error="error || localContentError"
        :enable-mentions="limits.mentionsEnabled"
        @update:model-value="emit('update:modelValue', $event)"
        @content-change="onContentChange"
      />

      <div v-if="humanVerification.visible" class="sf-topic-composer-drawer__verification">
        <div class="sf-topic-composer-drawer__verification-head">
          <label id="drawer-human-verification-label">{{ t('composer.humanVerificationLabel') }}</label>
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
            :aria-describedby="humanVerification.message ? 'drawer-human-verification-error' : undefined"
            aria-labelledby="drawer-human-verification-label"
            @verified="postRisk.handleVerified"
            @expired="postRisk.reset"
            @statechange="postRisk.handleStateChange"
          />
          <template #fallback>
            <small class="sf-topic-composer-drawer__verification-hint">{{ t('auth.humanVerificationLoading') }}</small>
          </template>
        </ClientOnly>
        <p
          v-if="humanVerification.message"
          id="drawer-human-verification-error"
          class="sf-topic-composer-drawer__verification-error"
          role="alert"
        >
          {{ humanVerification.message }}
        </p>
      </div>

      <label v-if="requireReason" class="sf-topic-composer-drawer__reason">
        <span>{{ t('topicDetail.composerDrawer.editReason') }}</span>
        <textarea
          :value="reason"
          :placeholder="t('topicDetail.composerDrawer.editReasonPlaceholder')"
          :disabled="submitting"
          maxlength="500"
          rows="2"
          @input="emit('update:reason', ($event.target as HTMLTextAreaElement).value)"
        />
        <small v-if="displayedReasonError" role="alert">{{ displayedReasonError }}</small>
        <small v-else>{{ t('topicDetail.composerDrawer.editReasonHint') }}</small>
      </label>

      <div v-if="discardPromptOpen" class="sf-topic-composer-discard" role="alert">
        <div>
          <strong>{{ t('topicDetail.composerDrawer.discardTitle') }}</strong>
          <p>{{ t('topicDetail.composerDrawer.discardDescription') }}</p>
        </div>
        <div class="sf-topic-composer-discard__actions">
          <SFButton variant="ghost" @click="discardPromptOpen = false">
            {{ t('topicDetail.composerDrawer.keepEditing') }}
          </SFButton>
          <SFButton variant="danger" @click="close">
            {{ t('topicDetail.composerDrawer.discard') }}
          </SFButton>
        </div>
      </div>
    </template>

    <template #footer>
      <div class="sf-topic-composer-drawer__actor">
        <SFAvatar v-if="actorName" :name="actorName" :avatar="avatar" size="sm" />
        <span v-if="actorName">{{ t('topicDetail.replyAs', { name: actorName }) }}</span>
      </div>
      <div class="sf-topic-composer-drawer__actions">
        <SFButton variant="ghost" :disabled="submitting" @click="requestClose">
          {{ t('topicDetail.cancel') }}
        </SFButton>
        <SFButton
          :loading="submitting"
          :disabled="submitting || (mode !== 'edit' && !canSubmit)"
          :aria-disabled="mode === 'edit' && !canSubmit ? 'true' : undefined"
          @click="submit"
        >
          <template #leading>
            <UIcon :name="submitIcon" class="size-4" aria-hidden="true" />
          </template>
          {{ submitLabel }}
        </SFButton>
      </div>
    </template>

  </USlideover>
</template>

<style>
.sf-topic-composer-drawer__overlay { background: rgb(15 23 42 / 0.34); }

.sf-topic-composer-drawer {
  width: 100%;
  height: min(76dvh, 780px);
  min-height: 280px;
  max-width: none;
  max-height: calc(100dvh - 16px);
  border-top: 1px solid var(--sf-public-border, var(--sf-border));
  border-radius: 8px 8px 0 0;
  background: var(--sf-public-surface, var(--sf-card));
  color: var(--sf-public-text, var(--sf-fg));
}

.sf-topic-composer-drawer > [data-slot="header"] {
  position: relative;
  min-height: 76px;
  justify-content: space-between;
  border-bottom: 1px solid var(--sf-public-border, var(--sf-border));
  padding: 20px max(20px, calc((100vw - 1180px) / 2)) 12px;
}

.sf-topic-composer-drawer__resize { position: absolute; top: 0; left: 50%; display: grid; width: 88px; height: 18px; place-items: center; border: 0; padding: 0; transform: translateX(-50%); touch-action: none; background: transparent; cursor: ns-resize; }
.sf-topic-composer-drawer__resize span { width: 42px; height: 4px; border-radius: 999px; background: var(--sf-public-border-strong, var(--sf-border)); transition: width 0.15s ease, background-color 0.15s ease; }
.sf-topic-composer-drawer__resize:hover span, .sf-topic-composer-drawer__resize:focus-visible span { width: 52px; background: var(--sf-accent); }
.sf-topic-composer-drawer__resize:focus-visible { outline: 2px solid var(--sf-accent); outline-offset: -2px; }
.sf-topic-composer-drawer__header-title { display: flex; min-width: 0; align-items: center; gap: 12px; font-size: 16px; font-weight: 750; line-height: 1.35; }
.sf-topic-composer-drawer [data-slot="description"] { max-width: 470px; margin: 3px 48px 0; overflow: hidden; color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.sf-topic-composer-drawer__icon { display: grid; width: 36px; height: 36px; flex: 0 0 36px; place-items: center; border-radius: 6px; background: var(--sf-accent-soft); color: var(--sf-accent); }
.sf-topic-composer-drawer__icon svg { width: 18px; height: 18px; }
.sf-topic-composer-drawer__close { display: grid; width: 36px; height: 36px; flex: 0 0 36px; place-items: center; border: 0; border-radius: 5px; background: transparent; color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); cursor: pointer; }
.sf-topic-composer-drawer__close:hover { background: var(--sf-public-surface-muted, var(--sf-muted)); color: var(--sf-public-text, var(--sf-fg)); }

.sf-topic-composer-drawer__body { display: flex; width: min(100%, 1180px); min-height: 0; flex-direction: column; gap: 12px; margin: 0 auto; overflow-y: auto; padding: 14px 20px 12px; }
.sf-topic-composer-drawer__context { flex: 0 0 auto; border-left: 3px solid var(--sf-accent); border-radius: 0 6px 6px 0; padding: 10px 12px; background: var(--sf-public-surface-muted, var(--sf-muted)); }
.sf-topic-composer-drawer__context-meta { display: flex; align-items: center; gap: 6px; color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 11px; }
.sf-topic-composer-drawer__context-meta strong { color: var(--sf-public-text-secondary, var(--sf-fg-secondary)); }
.sf-topic-composer-drawer__context-meta a { margin-left: auto; color: var(--sf-accent); font-weight: 650; }
.sf-topic-composer-drawer__context p { display: -webkit-box; margin: 6px 0 0; overflow: hidden; color: var(--sf-public-text-secondary, var(--sf-fg-secondary)); font-size: 12px; line-height: 1.55; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.sf-topic-composer-drawer .sf-editor {
  display: grid;
  min-height: 268px;
  flex: 1 0 268px;
  grid-template-rows: auto minmax(180px, 1fr) auto;
  border-radius: 6px;
}
.sf-topic-composer-drawer .sf-editor__body { min-height: 0; overflow-y: auto; overscroll-behavior: contain; }
.sf-topic-composer-drawer .sf-editor__loading,
.sf-topic-composer-drawer .sf-editor__content,
.sf-topic-composer-drawer .sf-editor__preview { min-height: 100%; }
.sf-topic-composer-drawer__verification { display: grid; flex: 0 0 auto; gap: 7px; border: 1px solid var(--sf-public-border, var(--sf-border)); border-radius: 6px; padding: 10px 12px; background: var(--sf-public-surface-muted, var(--sf-muted)); }
.sf-topic-composer-drawer__verification-head { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 4px 10px; }
.sf-topic-composer-drawer__verification-head label { color: var(--sf-public-text, var(--sf-fg)); font-size: 12px; font-weight: 700; }
.sf-topic-composer-drawer__verification-head small,
.sf-topic-composer-drawer__verification-hint { color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 11px; line-height: 1.5; }
.sf-topic-composer-drawer__verification-error { margin: 0; color: var(--sf-danger, #dc2626); font-size: 11px; font-weight: 650; }
.sf-topic-composer-drawer__reason { display: grid; flex: 0 0 auto; gap: 6px; }
.sf-topic-composer-drawer__reason > span { font-size: 12px; font-weight: 700; }
.sf-topic-composer-drawer__reason textarea { width: 100%; resize: vertical; border: 1px solid var(--sf-public-border, var(--sf-border)); border-radius: 6px; padding: 9px 10px; outline: 0; background: var(--sf-public-surface, var(--sf-card)); color: var(--sf-public-text, var(--sf-fg)); font-size: 12px; line-height: 1.5; }
.sf-topic-composer-drawer__reason textarea:focus { border-color: var(--sf-accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--sf-accent) 14%, transparent); }
.sf-topic-composer-drawer__reason small { color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 11px; }
.sf-topic-composer-drawer__reason small[role="alert"] { color: var(--sf-danger, #dc2626); }

.sf-topic-composer-drawer__footer { min-height: 68px; justify-content: space-between; border-top: 1px solid var(--sf-public-border, var(--sf-border)); padding: 11px max(20px, calc((100vw - 1180px) / 2)); }
.sf-topic-composer-drawer__actor, .sf-topic-composer-drawer__actions { display: flex; align-items: center; gap: 8px; }
.sf-topic-composer-drawer__actor { min-width: 0; color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 11px; }

.sf-topic-composer-discard { position: fixed; bottom: 78px; left: 50%; z-index: 90; display: flex; width: min(760px, calc(100vw - 40px)); align-items: center; justify-content: space-between; gap: 18px; border: 1px solid color-mix(in srgb, var(--sf-danger, #dc2626) 35%, var(--sf-border)); border-radius: 6px; padding: 13px 14px; transform: translateX(-50%); background: var(--sf-public-surface, var(--sf-card)); color: var(--sf-public-text, var(--sf-fg)); box-shadow: 0 14px 36px rgb(15 23 42 / 0.2); }
.sf-topic-composer-discard strong { font-size: 12px; }
.sf-topic-composer-discard p { margin: 3px 0 0; color: var(--sf-public-text-muted, var(--sf-fg-tertiary)); font-size: 11px; }
.sf-topic-composer-discard__actions { display: flex; flex: 0 0 auto; gap: 7px; }

@media (max-width: 640px) {
  .sf-topic-composer-drawer { height: min(90dvh, 780px); }
  .sf-topic-composer-drawer > [data-slot="header"] { min-height: 66px; padding: 18px 14px 8px; }
  .sf-topic-composer-drawer__icon { width: 32px; height: 32px; flex-basis: 32px; }
  .sf-topic-composer-drawer__header-title { font-size: 15px; }
  .sf-topic-composer-drawer [data-slot="description"] { max-width: calc(100vw - 128px); margin-left: 44px; }
  .sf-topic-composer-drawer__body { gap: 10px; padding: 10px 14px; }
  .sf-topic-composer-drawer__context { padding: 8px 10px; }
  .sf-topic-composer-drawer__context p { -webkit-line-clamp: 1; }
  .sf-topic-composer-drawer .sf-editor { min-height: 328px; flex-basis: 328px; grid-template-rows: auto minmax(190px, 1fr) auto; }
  .sf-topic-composer-drawer__footer { min-height: 64px; padding: 9px 14px; }
  .sf-topic-composer-drawer__actor { display: none; }
  .sf-topic-composer-drawer__actions { width: 100%; }
  .sf-topic-composer-drawer__actions .sf-button { flex: 1; }
  .sf-topic-composer-discard { bottom: 72px; width: calc(100vw - 20px); align-items: flex-start; flex-direction: column; }
  .sf-topic-composer-discard__actions { width: 100%; }
  .sf-topic-composer-discard__actions .sf-button { flex: 1; }
}

@media (max-width: 640px) and (max-height: 680px) {
  .sf-topic-composer-drawer { height: 96dvh; }
  .sf-topic-composer-drawer__context { display: none; }
}
</style>
