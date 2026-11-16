<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useForumContentLimits } from '~/composables/forum/useForumContentLimits'
import { useForumCooldownError } from '~/composables/forum/useForumCooldownError'
import { useForumApi } from '~/composables/forum/useForumApi'
import { usePostRiskVerification } from '~/composables/forum/usePostRiskVerification'
import SFTopicComposerRightRail from '~/components/forum/SFTopicComposerRightRail.vue'
import SFTopicComposerLeftRail from '~/components/forum/SFTopicComposerLeftRail.vue'
import SFTagInput from '~/components/forum/SFTagInput.vue'
import SFHomeNavigation from '~/components/forum/SFHomeNavigation.vue'
import SFCategorySelect from '~/components/forum/SFCategorySelect.vue'
import SFResponsivePublicSidebar from '~/components/forum/navigation/SFResponsivePublicSidebar.vue'
import SFPublicMobileUserMenu from '~/components/navigation/SFPublicMobileUserMenu.vue'
import SFPublicMobileRightDrawerHeader from '~/components/navigation/SFPublicMobileRightDrawerHeader.vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
/**
 * 宿主 body 岛：forum.topic.create。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 */

import {
  forumContentFromEditorPayload,
  forumTopicPath,
  isForumTagSlug,
  type ForumCategoryGroup,
  type ForumTag
} from '~/utils/forum/forumTaxonomy'
import type { SFEditorContentPayload } from '~/utils/sfEditor'
import { sforumDocumentPublishIssue, sforumPublishFailureKey } from '~/utils/editor/shortcodes'
import { onBeforeRouteLeave } from 'vue-router'


const { t } = useI18n()
const localePath = useLocalePath()
const { seoSettings } = useWebOptions()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)
const forumApi = useForumApi()
const { can } = usePermissions()
const toast = useToast()
const { locale } = useI18n()
const { user } = useAuthSession()
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
const { closeDrawer: closeMobileMenu } = usePublicSidebarDrawer()
const topicDraftKey = computed(() => {
  const actorKey = user.value?.id || user.value?.username || 'authenticated'
  return `sforum.topic-composer.draft.v1:${actorKey}`
})

// F4.3：composer 工具栏扩展动作（登录后拉取；失败静默为空）。
const { data: composerToolbarActions } = await useAsyncData(
  'composer-toolbar-actions',
  async () => {
    try {
      return await forumApi.listComposerToolbarActions()
    } catch {
      return []
    }
  },
  { default: () => [] as import('~/utils/forum/forumTaxonomy').ForumComposerToolbarAction[] }
)

function composerToolbarLabel(action: import('~/utils/forum/forumTaxonomy').ForumComposerToolbarAction) {
  const labels = action.label || {}
  return labels[String(locale.value)] || labels['zh-CN'] || labels['en-US'] || Object.values(labels)[0] || action.id
}

async function runComposerToolbarAction(action: import('~/utils/forum/forumTaxonomy').ForumComposerToolbarAction) {
  if (action.confirm && !window.confirm(composerToolbarLabel(action))) {
    return
  }
  try {
    await forumApi.applyComposerToolbarAction(action)
    toast.add({
      color: 'primary',
      icon: action.icon || 'i-lucide-check',
      title: composerToolbarLabel(action),
      duration: 5000
    })
  } catch (error) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(error) || t('composer.submitFailed'),
      duration: 0
    })
  }
}

useSForumSeo({
  title: () => t('composer.metaTitle'),
  description: () => t('composer.metaDescription'),
  type: 'website',
  noindex: true
})

// 没有发帖权限直接给出提示。
const canCreate = computed(() => can(FORUM_PERMISSIONS.topicCreate))

const { data: categoryGroups, pending: categoriesPending } = await useAsyncData(
  'composer-category-groups',
  () => forumApi.listCategoryGroups(),
  { default: () => [] as ForumCategoryGroup[] }
)

const categories = computed(() => categoryGroups.value.flatMap((group) => group.categories || []))
const totalTopics = computed(() => categories.value.reduce((sum, category) => sum + category.topicCount, 0))

const { data: tagOptions, pending: tagsPending } = await useAsyncData(
  'composer-active-tags',
  async () => (await forumApi.listTags()).filter((tag) => tag.status === 'active'),
  { default: () => [] as ForumTag[] }
)

const title = ref('')
const selectedCategorySlug = ref('')
const tagDraft = ref<string[]>([])
const bodyMarkdown = ref('')
const editorPayload = ref<SFEditorContentPayload | null>(null)
const {
  limits,
  runeLength,
  validateTopicTitle,
  validateTopicBody,
  validateTagCount
} = useForumContentLimits()

// 表单状态。
type SubmitState = 'idle' | 'submitting' | 'error' | 'success'
const submitState = ref<SubmitState>('idle')
const errorMessage = ref('')
const {
  active: topicCooldownActive,
  message: topicCooldownMessage,
  capture: captureTopicCooldown
} = useForumCooldownError('topic')
const displayErrorMessage = computed(() => topicCooldownMessage.value || errorMessage.value)
// post_risk 人机验证（P3-3）：开关开启时在正文下方渲染 ALTCHA，token 随发帖请求提交。
const {
  enabled: postRiskEnabled,
  widget: postRiskWidget,
  fieldMessage: postRiskMessage,
  widgetSettings: postRiskWidgetSettings,
  challengeUrl: postRiskChallengeUrl,
  configuration: postRiskConfiguration,
  language: postRiskLanguage,
  handleVerified: onPostRiskVerified,
  handleStateChange: onPostRiskStateChange,
  reset: resetPostRisk,
  field: postRiskField,
  capture: capturePostRisk,
  settle: settlePostRisk
} = usePostRiskVerification()
const fieldErrors = ref<Record<string, string[]>>({})
const draftSavedAt = ref('')
const draftSaving = ref(false)
const draftError = ref('')
const lastPersistedDraftSignature = ref('')

const submitLabel = computed(() => {
  if (submitState.value === 'submitting') {
    return t('composer.submitting')
  }
  return t('composer.submit')
})

const titleCount = computed(() => runeLength(title.value))
const bodyCount = computed(() => runeLength(bodyMarkdown.value))
const titleHint = computed(() => t('composer.titleHintWithLimit', {
  min: limits.value.topicTitleMinRunes,
  max: limits.value.topicTitleMaxRunes
}))
const bodyHint = computed(() => t('composer.bodyHintWithLimit', {
  max: limits.value.topicContentMaxRunes
}))

const canSubmit = computed(() => {
  if (
    !canCreate.value
    || submitState.value === 'submitting'
    || topicCooldownActive.value
    || (editorPayload.value?.pendingUploadCount || 0) > 0
  ) {
    return false
  }
  // 允许 contentMin=0 时正文可为空字符串以外的空白由后端 RenderContent 再判。
  if (title.value.trim() === '') {
    return false
  }
  if (limits.value.topicContentMinRunes > 0 && bodyMarkdown.value.trim() === '') {
    return false
  }
  return !validateTopicTitle(title.value) && !validateTopicBody(bodyMarkdown.value) && !validateTagCount(tagDraft.value.length)
})

const actorName = computed(() => userDisplayName(user.value, t('composer.currentAccount')))
const selectedCategory = computed(() => categories.value.find(category => category.slug === selectedCategorySlug.value))
const defaultCategory = computed(() => categories.value.find(category => category.slug === limits.value.defaultCategorySlug))
const summaryCategory = computed(() => selectedCategory.value || defaultCategory.value || null)
const effectiveText = computed(() => editorPayload.value?.text ?? bodyMarkdown.value)
const currentDraftSignature = computed(() => JSON.stringify({
  title: title.value,
  selectedCategorySlug: selectedCategorySlug.value,
  tagDraft: tagDraft.value,
  bodyMarkdown: bodyMarkdown.value
}))
const hasComposerContent = computed(() => Boolean(
  title.value.trim()
  || tagDraft.value.length
  || bodyMarkdown.value.trim()
))
const hasUnsavedChanges = computed(() => (
  submitState.value !== 'success'
  && hasComposerContent.value
  && currentDraftSignature.value !== lastPersistedDraftSignature.value
))

const validationState = computed(() => {
  const titleError = validateTopicTitle(title.value)
  const bodyError = validateTopicBody(effectiveText.value)
  const tagError = validateTagCount(tagDraft.value.length)
  return { titleError, bodyError, tagError }
})

const titleCheckText = computed(() => {
  if (!title.value.trim()) return t('composer.checks.title.empty')
  if (validationState.value.titleError === 'titleTooShort') {
    return t('composer.titleTooShort', { min: limits.value.topicTitleMinRunes })
  }
  if (validationState.value.titleError === 'titleTooLong') {
    return t('composer.titleTooLong', { max: limits.value.topicTitleMaxRunes })
  }
  return t('composer.checks.title.ready')
})

const bodyCheckText = computed(() => {
  if (!effectiveText.value.trim()) return t('composer.checks.body.empty')
  if (validationState.value.bodyError === 'contentTooShort') {
    return t('composer.contentTooShort', { min: limits.value.topicContentMinRunes })
  }
  if (validationState.value.bodyError === 'contentTooLong') {
    return t('composer.contentTooLong', { max: limits.value.topicContentMaxRunes })
  }
  return t('composer.checks.body.ready')
})

const categoryCheckText = computed(() => {
  if (selectedCategory.value) return t('composer.checks.category.selected', { category: selectedCategory.value.name })
  if (defaultCategory.value) return t('composer.checks.category.default', { category: defaultCategory.value.name })
  return t('composer.checks.category.serverDefault')
})

const tagCheckText = computed(() => {
  if (validationState.value.tagError === 'tagMin') {
    return t('composer.tagMinRequired', { min: limits.value.tagMinPerTopic })
  }
  if (validationState.value.tagError === 'tagMax') {
    return t('composer.tagLimit')
  }
  if (tagDraft.value.length) {
    return t('composer.checks.tags.selected', { count: tagDraft.value.length, max: limits.value.tagMaxPerTopic })
  }
  return limits.value.tagMinPerTopic > 0
    ? t('composer.checks.tags.emptyRequired', { min: limits.value.tagMinPerTopic })
    : t('composer.checks.tags.optional', { max: limits.value.tagMaxPerTopic })
})

const prePublishChecks = computed(() => [
  {
    key: 'title',
    ok: Boolean(title.value.trim()) && !validationState.value.titleError && !fieldErrors.value.title,
    icon: 'i-lucide-heading-1',
    label: t('composer.checks.title.label'),
    text: titleCheckText.value
  },
  {
    key: 'body',
    ok: Boolean(effectiveText.value.trim()) && !validationState.value.bodyError && !fieldErrors.value.content,
    icon: 'i-lucide-file-text',
    label: t('composer.checks.body.label'),
    text: bodyCheckText.value
  },
  {
    key: 'category',
    ok: Boolean(summaryCategory.value || limits.value.defaultCategorySlug),
    icon: 'i-lucide-folder',
    label: t('composer.checks.category.label'),
    text: categoryCheckText.value
  },
  {
    key: 'tags',
    ok: !validationState.value.tagError && !fieldErrors.value.tagSlugs,
    icon: 'i-lucide-tags',
    label: t('composer.checks.tags.label'),
    text: tagCheckText.value
  }
])

const draftStatusLabel = computed(() => {
  if (draftSaving.value) return t('composer.draft.saving')
  if (draftError.value) return draftError.value
  if (draftSavedAt.value) return t('composer.draft.savedAt', { time: draftSavedAt.value })
  if (hasUnsavedChanges.value) return t('composer.draft.unsaved')
  return t('composer.draft.ready')
})

const publishVisibilityLabel = computed(() => {
  if (submitState.value === 'success') return t('composer.publishState.success')
  if (submitState.value === 'error') return t('composer.publishState.error')
  return t('composer.publishState.public')
})

const tagPolicyLabel = computed(() => t(`composer.tagPolicy.${limits.value.tagCreationMode}`))

watch([categories, limits], () => {
  if (selectedCategorySlug.value || !limits.value.defaultCategorySlug) {
    return
  }
  if (categories.value.some(category => category.slug === limits.value.defaultCategorySlug)) {
    selectedCategorySlug.value = limits.value.defaultCategorySlug
  }
}, { immediate: true })

watch(title, () => {
  if (!validateTopicTitle(title.value)) {
    delete fieldErrors.value.title
  }
})

watch(bodyMarkdown, () => {
  if (!validateTopicBody(effectiveText.value)) {
    delete fieldErrors.value.content
  }
})

watch(selectedCategorySlug, () => {
  delete fieldErrors.value.categorySlug
})

function onTagDraftUpdate(next: string[]) {
  tagDraft.value = next
  // 成功变更芯片后清掉上一次格式/策略错误；数量下限仍在提交时校验。
  delete fieldErrors.value.tagSlugs
}

function onTagInputInvalid(key: 'tagInvalid' | 'tagUnknownControlled' | 'tagLimit') {
  fieldErrors.value.tagSlugs = [t(`composer.${key}`)]
}

function onEditorContentChange(payload: SFEditorContentPayload) {
  editorPayload.value = payload
}

function closeMobileDrawers() {
  closeMobileMenu()
  mobileInfoOpen.value = false
}

/** 左栏进度项：滚到主列对应字段并尝试 focus。 */
function focusComposerField(field: 'title' | 'body' | 'category' | 'tags') {
  if (!import.meta.client) {
    return
  }
  const idByField: Record<typeof field, string> = {
    title: 'topic-composer-title-input',
    body: 'topic-composer-body',
    category: 'topic-composer-category',
    tags: 'topic-composer-tags'
  }
  const target = document.getElementById(idByField[field])
  if (!target) {
    return
  }
  target.scrollIntoView({ behavior: 'smooth', block: 'center' })
  // 编辑器容器可能不可 focus，退而求其次找内部可编辑节点。
  const focusable = target.matches('input, select, textarea, button, [contenteditable="true"]')
    ? target
    : target.querySelector<HTMLElement>('input, select, textarea, button, [contenteditable="true"], .ProseMirror')
  if (focusable && typeof focusable.focus === 'function') {
    window.setTimeout(() => focusable.focus(), 180)
  }
  closeMobileDrawers()
}

function onLeftRailSaveDraft() {
  saveDraft()
  closeMobileDrawers()
}

async function submit(payload?: Pick<SFEditorContentPayload, 'markdown' | 'native' | 'text' | 'attachmentIds' | 'pendingUploadCount'>) {
  if (
    !canCreate.value
    || submitState.value === 'submitting'
    || topicCooldownActive.value
    || (payload?.pendingUploadCount ?? editorPayload.value?.pendingUploadCount ?? 0) > 0
  ) {
    return
  }
  const markdown = payload?.markdown ?? bodyMarkdown.value
  const content = forumContentFromEditorPayload({
    markdown,
    native: payload?.native,
    text: payload?.text,
    attachmentIds: payload?.attachmentIds
  })
  const nextErrors: Record<string, string[]> = {}
  const titleError = validateTopicTitle(title.value)
  if (titleError === 'titleTooShort') {
    nextErrors.title = [t('composer.titleTooShort', { min: limits.value.topicTitleMinRunes })]
  } else if (titleError === 'titleTooLong') {
    nextErrors.title = [t('composer.titleTooLong', { max: limits.value.topicTitleMaxRunes })]
  }
  // 字数校验仍按 markdown/text 预估；最终权威在 Host Accept/Render。
  const bodyError = validateTopicBody(payload?.text || markdown)
  // 空保护块在编辑期合法，但发布前必须有内容：比字数校验更具体，优先呈现。
  const protectedIssue = sforumDocumentPublishIssue(payload?.native ?? editorPayload.value?.native, 'topic')
  if (protectedIssue) {
    nextErrors.content = [t(sforumPublishFailureKey(protectedIssue))]
  } else if (bodyError === 'contentTooShort') {
    nextErrors.content = [t('composer.contentTooShort', { min: limits.value.topicContentMinRunes })]
  } else if (bodyError === 'contentTooLong') {
    nextErrors.content = [t('composer.contentTooLong', { max: limits.value.topicContentMaxRunes })]
  }
  const tagError = validateTagCount(tagDraft.value.length)
  if (tagError === 'tagMin') {
    nextErrors.tagSlugs = [t('composer.tagMinRequired', { min: limits.value.tagMinPerTopic })]
  } else if (tagError === 'tagMax') {
    nextErrors.tagSlugs = [t('composer.tagLimit')]
  }
  if (Object.keys(nextErrors).length) {
    fieldErrors.value = nextErrors
    submitState.value = 'error'
    return
  }

  submitState.value = 'submitting'
  errorMessage.value = ''
  fieldErrors.value = {}

  try {
    const created = await forumApi.createTopic({
      title: title.value.trim(),
      categorySlug: selectedCategorySlug.value || undefined,
      tagSlugs: tagDraft.value.length ? tagDraft.value : undefined,
      ...content,
      // 开关关闭时 postRiskField() 是空对象：请求体里连 humanVerification 键都没有。
      ...postRiskField()
    })
    settlePostRisk()
    submitState.value = 'success'
    clearDraft()
    if (created.status === 'pending') {
      // 待审主题无公开落地页；Toast 提示后回首页，审核结果走通知。
      toast.add({ color: 'primary', icon: 'i-lucide-clock-3', title: t('composer.submittedForReview'), duration: 5000 })
      await navigateTo(localePath('/'))
      return
    }
    await navigateTo(localePath(forumTopicPath(created, topicUrlMode.value)))
  } catch (error) {
    submitState.value = 'error'
    // 人机验证失败：提示挂在 widget 旁（capture 已本地化并重置已消费的 token）。
    if (capturePostRisk(error)) {
      errorMessage.value = ''
      fieldErrors.value = {}
      return
    }
    errorMessage.value = captureTopicCooldown(error)
      ? ''
      : apiErrorMessage(error) || t('composer.submitFailed')
    fieldErrors.value = apiErrorFields(error)
  }
}

function onEditorSubmit(payload: SFEditorContentPayload) {
  submit(payload)
}

function submitCurrentDraft() {
  void submit(editorPayload.value || undefined)
}

function formatDraftTime(timestamp: number) {
  return new Date(timestamp).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
}

function saveDraft({ quiet = false } = {}) {
  if (!import.meta.client || draftSaving.value) {
    return
  }
  draftSaving.value = true
  draftError.value = ''
  try {
    const savedAt = Date.now()
    sessionStorage.setItem(topicDraftKey.value, JSON.stringify({
      title: title.value,
      selectedCategorySlug: selectedCategorySlug.value,
      tagDraft: tagDraft.value,
      bodyMarkdown: bodyMarkdown.value,
      savedAt
    }))
    lastPersistedDraftSignature.value = currentDraftSignature.value
    draftSavedAt.value = formatDraftTime(savedAt)
    if (!quiet) {
      toast.add({
        color: 'primary',
        icon: 'i-lucide-save',
        title: t('composer.draft.saved'),
        duration: 5000
      })
    }
  } catch {
    draftError.value = t('composer.draft.saveFailed')
    if (!quiet) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: draftError.value,
        duration: 0
      })
    }
  } finally {
    draftSaving.value = false
  }
}

function clearDraft() {
  lastPersistedDraftSignature.value = currentDraftSignature.value
  if (!import.meta.client) {
    return
  }
  try {
    sessionStorage.removeItem(topicDraftKey.value)
  } catch {
    // 草稿清理失败不影响已提交的正式发布。
  }
}

function restoreDraft() {
  if (!import.meta.client) {
    return
  }
  try {
    const raw = sessionStorage.getItem(topicDraftKey.value)
    if (!raw) {
      lastPersistedDraftSignature.value = currentDraftSignature.value
      return
    }
    const draft = JSON.parse(raw) as {
      title?: string
      selectedCategorySlug?: string
      tagDraft?: string[]
      bodyMarkdown?: string
      savedAt?: number
    }
    title.value = draft.title || ''
    selectedCategorySlug.value = draft.selectedCategorySlug || selectedCategorySlug.value
    tagDraft.value = Array.isArray(draft.tagDraft) ? draft.tagDraft.filter(isForumTagSlug) : []
    bodyMarkdown.value = draft.bodyMarkdown || ''
    lastPersistedDraftSignature.value = currentDraftSignature.value
    if (draft.savedAt) {
      draftSavedAt.value = formatDraftTime(draft.savedAt)
    }
  } catch {
    draftError.value = t('composer.draft.restoreFailed')
  }
}

let beforeUnloadHandler: ((event: BeforeUnloadEvent) => void) | null = null

onMounted(() => {
  restoreDraft()
  beforeUnloadHandler = (event: BeforeUnloadEvent) => {
    if (!hasUnsavedChanges.value) {
      return
    }
    event.preventDefault()
    event.returnValue = ''
  }
  window.addEventListener('beforeunload', beforeUnloadHandler)
})

onBeforeUnmount(() => {
  if (beforeUnloadHandler) {
    window.removeEventListener('beforeunload', beforeUnloadHandler)
  }
})

onBeforeRouteLeave(() => {
  if (!import.meta.client || !hasUnsavedChanges.value) {
    return true
  }
  return window.confirm(t('composer.draft.leaveConfirm'))
})
</script>

<template>
  <main
    class="sforum-home sforum-topic-composer"
    data-sforum-island-body="forum.component.topic_composer"
    data-layout="fullwidth-3col"
  >
    <div
      class="sforum-home__layout"
      :class="{ 'sforum-home__layout--with-right': canCreate }"
    >
      <!-- 左栏：通知页模式 — 无类别列表，挂发帖实用工具 -->
      <SFResponsivePublicSidebar
        owner-id="forum.topic.composer"
        :title="t('home.sidebar.drawerTitle')"
        class="sforum-home__sidebar"
      >
        <SFHomeNavigation
          desktop-only
          navigation-mode="route"
          :categories="categories"
          selected-category-slug=""
          :total-topics="totalTopics"
          :pending="categoriesPending"
          :can-create-topic="canCreate"
          :show-categories="false"
        >
          <template #after-navigation>
            <SFTopicComposerLeftRail
              :checks="prePublishChecks"
              :draft-saving="draftSaving"
              :draft-status-label="draftStatusLabel"
              :can-create="canCreate"
              @focus-field="focusComposerField"
              @save-draft="onLeftRailSaveDraft"
            />
          </template>
        </SFHomeNavigation>
      </SFResponsivePublicSidebar>

      <section
        class="sforum-home__main sforum-topic-composer__main"
        :aria-labelledby="canCreate ? 'topic-composer-title' : undefined"
      >
        <!-- 无权限提示 -->
        <SFCard v-if="!canCreate" class="p-8">
          <SFEmptyState
            icon-label="LOCK"
            :title="t('composer.permissionDenied.title')"
            :description="t('composer.permissionDenied.description')"
          />
        </SFCard>

        <template v-else>
          <div class="sforum-topic-composer__inner">
            <nav class="sforum-topic-composer__breadcrumbs" :aria-label="t('composer.breadcrumbLabel')">
              <NuxtLink :to="localePath('/')" class="hover:text-[color:var(--sf-accent)]">
                {{ t('composer.breadcrumbHome') }}
              </NuxtLink>
              <UIcon name="i-lucide-chevron-right" class="size-3" aria-hidden="true" />
              <span>{{ t('composer.title') }}</span>
            </nav>

            <header class="sforum-topic-composer__head">
              <div class="sforum-topic-composer__head-copy">
                <h1 id="topic-composer-title">{{ t('composer.title') }}</h1>
                <p>{{ t('composer.subtitle') }}</p>
              </div>
              <div class="sforum-topic-composer__head-actions">
                <span class="sforum-topic-composer__draft-state" :class="{ 'is-error': draftError }">
                  <UIcon :name="draftError ? 'i-lucide-cloud-off' : 'i-lucide-cloud-check'" class="size-4" aria-hidden="true" />
                  {{ draftStatusLabel }}
                </span>
                <!-- 中窄屏：打开完整右栏（与通知页 panel-right 一致） -->
                <button
                  type="button"
                  class="sforum-topic-composer__icon-button sforum-topic-composer__desktop-hidden"
                  :aria-label="t('composer.rightRail.open')"
                  @click="mobileInfoOpen = true"
                >
                  <UIcon name="i-lucide-panel-right" class="size-[18px]" aria-hidden="true" />
                </button>
              </div>
            </header>

            <!-- 全局错误（不自动消失） -->
            <SFAlert
              v-if="displayErrorMessage"
              variant="danger"
              :title="displayErrorMessage"
              :closable="!topicCooldownActive"
              class="sforum-topic-composer__alert"
              @close="errorMessage = ''"
            />

            <SFRegionOutlet page="forum.topic.create" region="content_before" />

            <form class="sforum-topic-composer__form" @submit.prevent="submitCurrentDraft">
              <!-- 分类 / 标签：自定义控件，展示 taxonomy icon + 颜色 -->
              <div class="sforum-topic-composer__taxonomy">
                <div class="sforum-topic-composer__field">
                  <div class="sforum-topic-composer__field-head">
                    <span class="sforum-topic-composer__field-label">{{ t('composer.categoryLabel') }}</span>
                    <span>{{ t('composer.categoryOptionalDefault') }}</span>
                  </div>
                  <SFCategorySelect
                    id="topic-composer-category"
                    v-model="selectedCategorySlug"
                    :categories="categories"
                    :empty-label="t('composer.categoryDefault')"
                    :hint="t('composer.categoryHint')"
                    :error="fieldErrors.categorySlug?.join(', ')"
                    :disabled="submitState === 'submitting'"
                    :pending="categoriesPending"
                  />
                </div>

                <div class="sforum-topic-composer__field">
                  <div class="sforum-topic-composer__field-head">
                    <span class="sforum-topic-composer__field-label">{{ t('composer.tagsLabel') }}</span>
                    <span>{{ t('composer.tagLimitSummary', { min: limits.tagMinPerTopic, max: limits.tagMaxPerTopic }) }}</span>
                  </div>
                  <SFTagInput
                    id="topic-composer-tags"
                    :model-value="tagDraft"
                    :options="tagOptions"
                    :hint="tagsPending ? t('composer.tagsLoading') : t('composer.tagsHint')"
                    :error="fieldErrors.tagSlugs?.join(', ')"
                    :disabled="submitState === 'submitting'"
                    :max="limits.tagMaxPerTopic"
                    :creation-mode="limits.tagCreationMode"
                    @update:model-value="onTagDraftUpdate"
                    @invalid="onTagInputInvalid"
                  />
                </div>
              </div>

              <!-- 标题 -->
              <div id="topic-composer-title-field" class="sforum-topic-composer__field">
                <SFInput
                  id="topic-composer-title-input"
                  v-model="title"
                  :label="t('composer.titleLabel')"
                  :placeholder="t('composer.titlePlaceholder')"
                  :hint="`${titleHint} (${t('composer.charCount', { count: titleCount, max: limits.topicTitleMaxRunes })})`"
                  :error="fieldErrors.title?.join(', ')"
                  :disabled="submitState === 'submitting'"
                  required
                />
              </div>

              <!-- 正文编辑器 -->
              <div id="topic-composer-body" class="sforum-topic-composer__field">
                <div class="sforum-topic-composer__field-head">
                  <label>{{ t('composer.bodyLabel') }}</label>
                  <span>{{ t('composer.charCount', { count: bodyCount, max: limits.topicContentMaxRunes }) }}</span>
                </div>
                <p class="sforum-topic-composer__hint">{{ bodyHint }}</p>
                <!-- F4.3：扩展 composer 工具栏（宿主渲染按钮，执行走扩展路由） -->
                <div
                  v-if="composerToolbarActions.length"
                  class="sforum-topic-composer__extension-toolbar"
                >
                  <SFButton
                    v-for="action in composerToolbarActions"
                    :key="`${action.extensionId}:${action.id}`"
                    type="button"
                    size="sm"
                    variant="ghost"
                    :disabled="submitState === 'submitting'"
                    @click="runComposerToolbarAction(action)"
                  >
                    <UIcon v-if="action.icon" :name="action.icon" class="size-4" />
                    <span>{{ composerToolbarLabel(action) }}</span>
                  </SFButton>
                </div>
                <LazySFEditor
                  v-model="bodyMarkdown"
                  :placeholder="t('composer.bodyPlaceholder')"
                  :submit-label="submitLabel"
                  :submit-visible="false"
                  :disabled="submitState === 'submitting'"
                  :submit-disabled="topicCooldownActive"
                  :max-characters="limits.topicContentMaxRunes"
                  :error="fieldErrors.content?.join(', ')"
                  :rows="14"
                  :enable-mentions="limits.mentionsEnabled"
                  @content-change="onEditorContentChange"
                  @submit="onEditorSubmit"
                />
                <p v-if="fieldErrors.content" class="sforum-topic-composer__error">
                  {{ fieldErrors.content.join(', ') }}
                </p>
              </div>

              <!-- post_risk 人机验证：只在运营开关开启（或服务端 422 要求）时出现 -->
              <div
                v-if="postRiskEnabled"
                id="topic-composer-human-verification"
                class="sforum-topic-composer__field sforum-topic-composer__verification"
              >
                <div class="sforum-topic-composer__field-head">
                  <label id="topic-composer-human-verification-label">{{ t('composer.humanVerificationLabel') }}</label>
                  <span>{{ t('composer.humanVerificationHint') }}</span>
                </div>
                <ClientOnly>
                  <altcha-widget
                    ref="postRiskWidget"
                    class="sf-forum-altcha"
                    :challenge="postRiskChallengeUrl"
                    :configuration="postRiskConfiguration"
                    :auto="postRiskWidgetSettings.auto"
                    :display="postRiskWidgetSettings.display"
                    :language="postRiskLanguage"
                    :type="postRiskWidgetSettings.type"
                    :workers="postRiskWidgetSettings.workers"
                    :aria-invalid="postRiskMessage ? 'true' : undefined"
                    :aria-describedby="postRiskMessage ? 'topic-composer-human-verification-error' : undefined"
                    aria-labelledby="topic-composer-human-verification-label"
                    @verified="onPostRiskVerified"
                    @expired="resetPostRisk"
                    @statechange="onPostRiskStateChange"
                  />
                  <template #fallback>
                    <p class="sforum-topic-composer__hint">{{ t('auth.humanVerificationLoading') }}</p>
                  </template>
                </ClientOnly>
                <p
                  v-if="postRiskMessage"
                  id="topic-composer-human-verification-error"
                  class="sforum-topic-composer__error"
                  role="alert"
                >
                  {{ postRiskMessage }}
                </p>
              </div>
            </form>

            <SFRegionOutlet page="forum.topic.create" region="content_after" />
          </div>
        </template>

        <footer v-if="canCreate" class="sforum-topic-composer__dock" aria-live="polite">
          <div class="sforum-topic-composer__dock-status">
            <SFAvatar size="sm" :name="actorName" :avatar="user?.avatar" />
            <span>
              <strong>{{ t('composer.publishAs', { name: actorName }) }}</strong>
              {{ publishVisibilityLabel }} · {{ draftStatusLabel }}
            </span>
          </div>
          <div class="sforum-topic-composer__dock-actions">
            <SFButton
              type="button"
              variant="ghost"
              :disabled="draftSaving || submitState === 'submitting'"
              @click="saveDraft()"
            >
              <UIcon name="i-lucide-save" class="size-4" aria-hidden="true" />
              {{ draftSaving ? t('composer.draft.saving') : t('composer.draft.save') }}
            </SFButton>
            <SFButton
              type="button"
              :disabled="!canSubmit"
              @click="submitCurrentDraft"
            >
              <UIcon name="i-lucide-send" class="size-4" aria-hidden="true" />
              {{ submitLabel }}
            </SFButton>
          </div>
        </footer>
      </section>

      <aside
        v-if="canCreate"
        class="sforum-home__right"
        :aria-label="t('composer.rightRail.label')"
      >
        <SFTopicComposerRightRail
          :category-name="summaryCategory?.name"
          :title-max="limits.topicTitleMaxRunes"
          :tag-policy-label="tagPolicyLabel"
          :actor-name="actorName"
          :publish-visibility-label="publishVisibilityLabel"
          :checks="prePublishChecks"
          :body-max="limits.topicContentMaxRunes"
        />
      </aside>
    </div>

    <button
      v-if="mobileInfoOpen"
      type="button"
      class="sforum-mobile-drawer__backdrop"
      :aria-label="t('common.close')"
      @click="closeMobileDrawers"
    />

    <aside v-if="mobileInfoOpen && canCreate" class="sforum-mobile-drawer sforum-mobile-drawer--right">
      <SFPublicMobileRightDrawerHeader
        :title="t('composer.rightRail.label')"
        :close-label="t('common.close')"
        @close="closeMobileDrawers"
      />
      <SFPublicMobileUserMenu />
      <!-- 抽屉内复用完整右栏（与标签索引 / 首页一致） -->
      <aside class="sforum-home__right" :aria-label="t('composer.rightRail.label')">
        <SFTopicComposerRightRail
          :category-name="summaryCategory?.name"
          :title-max="limits.topicTitleMaxRunes"
          :tag-policy-label="tagPolicyLabel"
          :actor-name="actorName"
          :publish-visibility-label="publishVisibilityLabel"
          :checks="prePublishChecks"
          :body-max="limits.topicContentMaxRunes"
        />
      </aside>
    </aside>
  </main>
</template>

<style scoped src="./SFTopicComposerPage.css"></style>
