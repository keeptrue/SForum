<script setup lang="ts">
import { useProfileApi } from '~/composables/profile/useProfileApi'
import type { NicknameAvailability, NicknameState } from '~/composables/profile/useProfileApi'
import type { NicknameSettings } from '~/composables/useWebOptions'
import {
  nicknameCountdown,
  nicknameDraftConflict,
  nicknameRemainingMs,
  normalizeNicknameDraft
} from '~/utils/settings/nicknamePolicy'

/**
 * 昵称输入：本地预检 + 服务端预检 + 冷却倒计时。
 *
 * 组件只负责「这个值看起来行不行」，保存仍由资料页统一提交；保留词、撞用户名
 * 与冷却的最终裁决在服务端。
 */
const props = withDefaults(defineProps<{
  modelValue: string
  username: string
  policy: NicknameSettings
  state?: NicknameState | null
  busy?: boolean
  serverError?: string | null
}>(), {
  state: null,
  busy: false,
  serverError: null
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const { t } = useI18n()
const { format } = useSiteDateTime()
const profileApi = useProfileApi()

const now = ref(Date.now())
const availability = ref<NicknameAvailability | null>(null)
let clockTimer: ReturnType<typeof setInterval> | undefined
let debounceTimer: ReturnType<typeof setTimeout> | undefined

onMounted(() => {
  // 分钟级精度：30 秒刷新一次足够，倒计时不会显示成过期时间。
  clockTimer = setInterval(() => {
    now.value = Date.now()
  }, 30_000)
})

onBeforeUnmount(() => {
  if (clockTimer) {
    clearInterval(clockTimer)
  }
  if (debounceTimer) {
    clearTimeout(debounceTimer)
  }
})

const localConflict = computed(() => nicknameDraftConflict(props.modelValue, props.policy))
const remainingMs = computed(() => nicknameRemainingMs(props.state?.nextChangeAt, now.value))
const inCooldown = computed(() => remainingMs.value > 0)
const policyLocked = computed(() => !props.policy.allowUserChange)
const editable = computed(() => !policyLocked.value && !inCooldown.value && !props.busy)

const nicknameLength = computed(() => Array.from(normalizeNicknameDraft(props.modelValue)).length)

const countdownText = computed(() => {
  const parts = nicknameCountdown(remainingMs.value)
  const chunks: string[] = []
  if (parts.days > 0) {
    chunks.push(t('profileSettings.nicknameCooldownDays', { days: parts.days }))
  }
  if (parts.hours > 0) {
    chunks.push(t('profileSettings.nicknameCooldownHours', { hours: parts.hours }))
  }
  if (parts.minutes > 0 || chunks.length === 0) {
    chunks.push(t('profileSettings.nicknameCooldownMinutes', { minutes: parts.minutes }))
  }
  return chunks.join(' ')
})

const lockedReason = computed(() => {
  if (policyLocked.value) {
    return t('profileSettings.nicknameLocked')
  }
  if (inCooldown.value) {
    return t('profileSettings.nicknameCooldown', { duration: countdownText.value })
  }
  return ''
})

const cooldownUntil = computed(() => {
  if (!inCooldown.value || !props.state?.nextChangeAt) {
    return ''
  }
  return t('profileSettings.nicknameCooldownUntil', { date: format(props.state.nextChangeAt) })
})

const conflictMessage = computed(() => {
  if (props.serverError) {
    return props.serverError
  }
  if (localConflict.value) {
    return t(`profile.nickname_${localConflict.value}`)
  }
  if (availability.value && !availability.value.available && availability.value.conflict) {
    // 服务端返回的是与 API 消息包同名的 i18n key（profile.nickname_*），直接翻译。
    return t(availability.value.conflict)
  }
  return ''
})

const duplicateMessage = computed(() => {
  const count = availability.value?.duplicates || 0
  if (count <= 0 || localConflict.value || props.serverError) {
    return ''
  }
  return t('profileSettings.nicknameDuplicates', { count })
})

watch(
  () => [props.modelValue, props.policy.maxLength, props.policy.minLength, editable.value] as const,
  () => {
    availability.value = null
    if (debounceTimer) {
      clearTimeout(debounceTimer)
    }
    if (!editable.value || localConflict.value) {
      return
    }
    const normalized = normalizeNicknameDraft(props.modelValue)
    if (normalized === (props.state?.displayName || '')) {
      return
    }
    debounceTimer = setTimeout(async () => {
      try {
        availability.value = await profileApi.checkNicknameAvailability(normalized)
      } catch {
        // 预检失败不该阻塞保存：服务端写入时仍会给出权威结论。
        availability.value = null
      }
    }, 400)
  }
)

function onInput(event: Event) {
  emit('update:modelValue', (event.target as HTMLInputElement).value)
}
</script>

<template>
  <div class="sforum-settings-profile__field">
    <div class="sforum-settings-profile__field-topline">
      <label for="profile-settings-nickname">{{ t('profileSettings.nickname') }}</label>
      <span>{{ nicknameLength }} / {{ policy.maxLength }}</span>
    </div>
    <input
      id="profile-settings-nickname"
      :value="modelValue"
      type="text"
      autocomplete="nickname"
      :disabled="!editable"
      :class="{ 'is-invalid': Boolean(conflictMessage) }"
      :placeholder="t('profileSettings.nicknamePlaceholder')"
      :aria-invalid="conflictMessage ? 'true' : undefined"
      aria-describedby="profile-settings-nickname-message"
      @input="onInput"
    >
    <div id="profile-settings-nickname-message">
      <p v-if="lockedReason" class="sforum-settings-profile__field-hint">
        {{ cooldownUntil ? `${lockedReason} · ${cooldownUntil}` : lockedReason }}
      </p>
      <p v-else class="sforum-settings-profile__field-hint">
        {{ nicknameLength === 0
          ? t('profileSettings.nicknameEmptyHint', { username })
          : t('profileSettings.nicknameHint', { min: policy.minLength, max: policy.maxLength }) }}
      </p>
      <p v-if="conflictMessage" class="sforum-settings-profile__field-error">
        {{ conflictMessage }}
      </p>
      <p v-if="duplicateMessage" class="sforum-settings-profile__field-warning">
        {{ duplicateMessage }}
      </p>
    </div>
  </div>
</template>
