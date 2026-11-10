<script setup lang="ts">
/**
 * 收藏控件：书签按钮 + 可见性 / 提醒设置弹层。
 *
 * 状态策略与 SFReactionControl 一致：点击先乐观切换，服务端返回真值后回填；
 * 失败回滚并出错误 Toast（不自动关闭）。可见性切换与提醒设置复用同一个 PUT。
 * 产品开关关闭时整块不渲染：SSR 首屏即由 web-options 决定，避免水合后闪隐。
 */
import type { BookmarkTargetType, BookmarkVisibility } from '~/utils/forum/forumBookmark'
import type { BookmarkRemindIssue } from '~/utils/forum/forumBookmark'
import {
  bookmarkActionLabelKey,
  bookmarkRemindAtFromLocalInput,
  bookmarkRemindAtFromPreset,
  bookmarkRemindAtToLocalInput,
  bookmarkRemindBounds,
  bookmarkRemindIssue,
  bookmarkRemindIssueKey,
  bookmarkRemindPresets,
  bookmarkRemindPresetLabelKey,
  bookmarkRemindRelative,
  bookmarkRemindRelativeKey,
  bookmarkRemindRelativeParams,
  bookmarkRemindStatusKey,
  bookmarkVisibilityHintKey,
  bookmarkVisibilityIcon,
  bookmarkVisibilityLabelKey,
  bookmarkVisibilities,
  formatBookmarkRemindAt
} from '~/utils/forum/forumBookmark'
import { useBookmark, type BookmarkState } from '~/composables/forum/useBookmark'

const props = withDefaults(defineProps<{
  targetType: BookmarkTargetType
  targetId: number
  bookmarked?: boolean
  visibility?: BookmarkVisibility
  remindAt?: string | null
  /** 提醒已触发的时间；缺失表示「将提醒」。 */
  remindedAt?: string | null
  enabled?: boolean
  size?: 'sm' | 'md'
}>(), {
  bookmarked: false,
  visibility: 'private',
  remindAt: null,
  remindedAt: null,
  enabled: true,
  size: 'sm'
})

const emit = defineEmits<{ change: [state: BookmarkState] }>()

const { t, locale } = useI18n()
const { settings: dateTimeSettings } = useSiteDateTime()

const {
  bookmarked,
  visibility,
  remindAt,
  remindedAt,
  pending,
  toggle,
  setVisibility,
  setRemindAt
} = useBookmark({
  targetType: () => props.targetType,
  targetId: () => props.targetId,
  bookmarked: () => props.bookmarked,
  visibility: () => props.visibility,
  remindAt: () => props.remindAt,
  remindedAt: () => props.remindedAt,
  enabled: () => props.enabled,
  onState: state => emit('change', state)
})

const customRemind = ref(bookmarkRemindAtToLocalInput(props.remindAt))
// 输入框的 min/max 以「打开面板的时刻」为基准刷新，避免长开的弹层里 min 过期。
const boundsNow = ref(new Date())
const bounds = computed(() => bookmarkRemindBounds(boundsNow.value))
const customIssue = ref<BookmarkRemindIssue>(null)
const customIssueText = computed(() => {
  const key = bookmarkRemindIssueKey(customIssue.value)
  return key ? t(key) : ''
})

watch(remindAt, (value) => {
  customRemind.value = bookmarkRemindAtToLocalInput(value)
})

function onMenuOpenChange(open: boolean) {
  if (open) {
    boundsNow.value = new Date()
    customIssue.value = null
  }
}

const actionLabel = computed(() => t(bookmarkActionLabelKey(bookmarked.value)))
const visibilityLabel = computed(() => t(bookmarkVisibilityLabelKey(visibility.value)))
const visibilityHint = computed(() => t(bookmarkVisibilityHintKey(visibility.value)))
const remindText = computed(() => formatBookmarkRemindAt(
  remindAt.value,
  dateTimeSettings.value,
  String(locale.value || 'zh-CN')
))
const remindRelativeText = computed(() => {
  const relative = bookmarkRemindRelative(
    remindAt.value,
    new Date(),
    dateTimeSettings.value.timezone,
    String(locale.value || 'zh-CN')
  )
  return t(bookmarkRemindRelativeKey(relative), bookmarkRemindRelativeParams(relative))
})
// 已提醒 / 将提醒必须分开：同一个「提醒中」会让人分不清是否已经响过。
const remindStatusText = computed(() => t(bookmarkRemindStatusKey(remindedAt.value)))
const remindedText = computed(() => formatBookmarkRemindAt(
  remindedAt.value,
  dateTimeSettings.value,
  String(locale.value || 'zh-CN')
))

async function applyCustomRemind() {
  const iso = bookmarkRemindAtFromLocalInput(customRemind.value)
  const issue = bookmarkRemindIssue(iso)
  customIssue.value = issue
  if (issue) {
    return
  }
  const saved = await setRemindAt(iso)
  if (!saved) {
    customRemind.value = bookmarkRemindAtToLocalInput(remindAt.value)
  }
}

function clearRemind() {
  customRemind.value = ''
  customIssue.value = null
  void setRemindAt(null)
}
</script>

<template>
  <div v-if="props.enabled" class="sf-bookmark" :class="`sf-bookmark--${size}`">
    <button
      type="button"
      class="sf-bookmark__button"
      :class="{ 'sf-bookmark__button--active': bookmarked }"
      :aria-pressed="bookmarked"
      :aria-busy="pending"
      :aria-label="actionLabel"
      :title="actionLabel"
      :disabled="pending"
      @click="toggle"
    >
      <UIcon
        :name="bookmarked ? 'i-tabler-bookmark-filled' : 'i-lucide-bookmark'"
        class="sf-bookmark__icon"
        aria-hidden="true"
      />
    </button>

    <UPopover :content="{ align: 'end', sideOffset: 6, collisionPadding: 12 }" @update:open="onMenuOpenChange">
      <button
        type="button"
        class="sf-bookmark__menu"
        :aria-label="t('bookmark.controlLabel')"
        :title="t('bookmark.controlLabel')"
        :aria-busy="pending"
      >
        <UIcon name="i-lucide-chevron-down" class="sf-bookmark__chevron" aria-hidden="true" />
      </button>

      <template #content>
        <div class="sf-bookmark__panel" role="group" :aria-label="t('bookmark.controlLabel')">
          <p class="sf-bookmark__section">{{ t('bookmark.visibility.label') }}</p>
          <button
            v-for="option in bookmarkVisibilities"
            :key="option"
            type="button"
            class="sf-bookmark__option"
            :class="{ 'is-active': visibility === option }"
            :aria-pressed="visibility === option"
            :disabled="pending"
            @click="setVisibility(option)"
          >
            <UIcon :name="bookmarkVisibilityIcon(option)" class="size-3.5" aria-hidden="true" />
            <span class="sf-bookmark__option-label">{{ t(bookmarkVisibilityLabelKey(option)) }}</span>
            <UIcon v-if="visibility === option" name="i-lucide-check" class="size-3.5" aria-hidden="true" />
          </button>
          <p class="sf-bookmark__hint">{{ visibilityHint }}</p>

          <p class="sf-bookmark__section">{{ t('bookmark.remind.label') }}</p>
          <button
            type="button"
            class="sf-bookmark__option"
            :class="{ 'is-active': !remindAt }"
            :aria-pressed="!remindAt"
            :disabled="pending"
            @click="clearRemind"
          >
            <UIcon name="i-lucide-bell-off" class="size-3.5" aria-hidden="true" />
            <span class="sf-bookmark__option-label">{{ t('bookmark.remind.none') }}</span>
            <UIcon v-if="!remindAt" name="i-lucide-check" class="size-3.5" aria-hidden="true" />
          </button>
          <button
            v-for="preset in bookmarkRemindPresets"
            :key="preset.id"
            type="button"
            class="sf-bookmark__option"
            :disabled="pending"
            @click="setRemindAt(bookmarkRemindAtFromPreset(preset.id))"
          >
            <UIcon name="i-lucide-bell" class="size-3.5" aria-hidden="true" />
            <span class="sf-bookmark__option-label">{{ t(bookmarkRemindPresetLabelKey(preset.id)) }}</span>
          </button>

          <label class="sf-bookmark__custom">
            <span>{{ t('bookmark.remind.customLabel') }}</span>
            <input
              v-model="customRemind"
              type="datetime-local"
              class="sf-bookmark__input"
              :min="bounds.minInput"
              :max="bounds.maxInput"
              :aria-invalid="customIssue ? 'true' : undefined"
              :aria-describedby="customIssue ? 'sf-bookmark-remind-error' : undefined"
              :disabled="pending"
              @change="applyCustomRemind"
            >
          </label>
          <!-- 字段级校验留在输入框旁：用户改的就是这里，不用去别处找原因 -->
          <p v-if="customIssueText" id="sf-bookmark-remind-error" class="sf-bookmark__error" role="alert">
            {{ customIssueText }}
          </p>

          <p v-if="remindedAt" class="sf-bookmark__hint">
            {{ remindStatusText }} · {{ remindedText }}
          </p>
          <p v-else-if="remindAt" class="sf-bookmark__hint">
            {{ remindStatusText }} · {{ t('bookmark.remind.at', { time: remindText }) }} · {{ remindRelativeText }}
          </p>
        </div>
      </template>
    </UPopover>
  </div>
</template>

<style scoped>
.sf-bookmark {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--sf-public-muted, #64748b);
}

.sf-bookmark__button,
.sf-bookmark__menu {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  border-radius: 0.375rem;
  transition: color 120ms ease, background-color 120ms ease;
  min-width: 2.375rem;
  min-height: 2.375rem;
}

.sf-bookmark__menu {
  min-width: 1.5rem;
}

.sf-bookmark__button:hover,
.sf-bookmark__menu:hover {
  background: color-mix(in srgb, currentColor 8%, transparent);
}

.sf-bookmark__button--active {
  color: var(--sf-public-accent, #e11d48);
}

.sf-bookmark__button:disabled,
.sf-bookmark__menu:disabled,
.sf-bookmark__option:disabled {
  cursor: default;
  opacity: 0.6;
}

.sf-bookmark--sm { font-size: 0.8125rem; }
.sf-bookmark--md { font-size: 0.875rem; }
.sf-bookmark__icon { width: 1rem; height: 1rem; }
.sf-bookmark__chevron { width: 0.75rem; height: 0.75rem; }

.sf-bookmark__panel {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 0.5rem;
  width: min(16rem, calc(100vw - 2rem));
  color: var(--sf-public-fg, #0f172a);
}

.sf-bookmark__section {
  margin: 0.25rem 0 0.125rem;
  font-size: 0.6875rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--sf-public-muted, #64748b);
}

.sf-bookmark__option {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  width: 100%;
  padding: 0.375rem 0.5rem;
  border: 0;
  border-radius: 0.375rem;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}

.sf-bookmark__option:hover { background: color-mix(in srgb, currentColor 8%, transparent); }
.sf-bookmark__option.is-active { color: var(--sf-public-accent, #e11d48); }
.sf-bookmark__option-label { flex: 1 1 auto; }

.sf-bookmark__error {
  margin: 0.125rem 0.5rem 0.25rem;
  font-size: 0.75rem;
  line-height: 1.35;
  color: var(--sf-danger, #dc2626);
}

.sf-bookmark__hint {
  margin: 0.125rem 0.5rem 0.25rem;
  font-size: 0.75rem;
  line-height: 1.35;
  color: var(--sf-public-muted, #64748b);
}

.sf-bookmark__custom {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  padding: 0.25rem 0.5rem 0.125rem;
  font-size: 0.75rem;
  color: var(--sf-public-muted, #64748b);
}

.sf-bookmark__input {
  width: 100%;
  min-height: 2rem;
  padding: 0.25rem 0.375rem;
  border: 1px solid color-mix(in srgb, currentColor 24%, transparent);
  border-radius: 0.375rem;
  background: transparent;
  color: var(--sf-public-fg, #0f172a);
  font: inherit;
}

@media (prefers-reduced-motion: reduce) {
  .sf-bookmark,
  .sf-bookmark__button,
  .sf-bookmark__menu { transition: none; }
}
</style>
