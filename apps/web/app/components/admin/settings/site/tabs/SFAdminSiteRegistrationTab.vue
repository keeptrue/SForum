<script setup lang="ts">
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { enabledOptionValue, normalizeEnabledOption } from '~/composables/useWebOptions'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'
import { resolveRegistrationMode, type RegistrationMode } from '~/utils/settings/registrationPolicy'
import { joinCooldownMinutes, splitCooldownMinutes } from '~/utils/settings/nicknamePolicy'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const toast = useToast()
const section = useSettingsSection()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))

const form = reactive({
  registrationMode: 'open' as RegistrationMode,
  requireEmailVerification: false,
  blockPostingUntilVerified: true,
  usernameMinLength: 3,
  usernameMaxLength: 20,
  usernameCharset: 'unicode_letters_numbers' as 'unicode_letters_numbers' | 'ascii',
  usernameReserved: '',
  nicknameAllowUserChange: true,
  nicknameCooldownDays: 7,
  nicknameCooldownHours: 0,
  nicknameCooldownMinutes: 0,
  nicknameMinLength: 2,
  nicknameMaxLength: 24,
  nicknameHistoryPublic: false
})
const initial = computed(() => {
  const rawMode = map.value['identity.registration.mode']?.value
  const enabled = normalizeEnabledOption(map.value['identity.registration.enabled']?.value, true)
  const registrationMode = resolveRegistrationMode(rawMode, enabled)
  // 冷却以分钟存储，后台按天/时/分三段编辑：运营不需要自己算 10080 是几天。
  const cooldown = splitCooldownMinutes(boundedInteger(map.value['profile.nickname.cooldown_minutes']?.value, 10080, 0, 525600))
  return {
    registrationMode,
    requireEmailVerification: normalizeEnabledOption(map.value['identity.registration.require_email_verification']?.value, false),
    blockPostingUntilVerified: normalizeEnabledOption(map.value['identity.registration.block_posting_until_verified']?.value, true),
    usernameMinLength: boundedInteger(map.value['identity.username.min_length']?.value, 3, 2, 32),
    usernameMaxLength: boundedInteger(map.value['identity.username.max_length']?.value, 20, 2, 64),
    usernameCharset: map.value['identity.username.charset']?.value === 'ascii' ? 'ascii' as const : 'unicode_letters_numbers' as const,
    usernameReserved: map.value['identity.username.reserved']?.value || '',
    nicknameAllowUserChange: normalizeEnabledOption(map.value['profile.nickname.allow_user_change']?.value, true),
    nicknameCooldownDays: cooldown.days,
    nicknameCooldownHours: cooldown.hours,
    nicknameCooldownMinutes: cooldown.minutes,
    nicknameMinLength: boundedInteger(map.value['profile.nickname.min_length']?.value, 2, 1, 32),
    nicknameMaxLength: boundedInteger(map.value['profile.nickname.max_length']?.value, 24, 2, 64),
    nicknameHistoryPublic: normalizeEnabledOption(map.value['profile.nickname.history_public']?.value, false)
  }
})
const hasChanges = computed(() => JSON.stringify(form) !== JSON.stringify(initial.value))

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  Object.assign(form, initial.value)
}

async function save() {
  await section.runSave({
    successTitle: t('admin.settings.saved'),
    failureTitle: t('admin.settings.saveFailed'),
    prepare: () => {
      form.usernameMinLength = boundedInteger(form.usernameMinLength, 3, 2, 32)
      form.usernameMaxLength = boundedInteger(form.usernameMaxLength, 20, 2, 64)
      if (form.usernameMaxLength < form.usernameMinLength) form.usernameMaxLength = form.usernameMinLength
      form.nicknameMinLength = boundedInteger(form.nicknameMinLength, 2, 1, 32)
      form.nicknameMaxLength = boundedInteger(form.nicknameMaxLength, 24, 2, 64)
      if (form.nicknameMaxLength < form.nicknameMinLength) form.nicknameMaxLength = form.nicknameMinLength
      form.nicknameCooldownDays = boundedInteger(form.nicknameCooldownDays, 0, 0, 365)
      form.nicknameCooldownHours = boundedInteger(form.nicknameCooldownHours, 0, 0, 23)
      form.nicknameCooldownMinutes = boundedInteger(form.nicknameCooldownMinutes, 0, 0, 59)
    },
    save: () => saveOptions([
      { name: 'identity.registration.enabled', value: enabledOptionValue(form.registrationMode === 'open') },
      { name: 'identity.registration.mode', value: form.registrationMode },
      { name: 'identity.registration.require_email_verification', value: enabledOptionValue(form.requireEmailVerification) },
      { name: 'identity.registration.block_posting_until_verified', value: enabledOptionValue(form.blockPostingUntilVerified) },
      { name: 'identity.username.min_length', value: String(form.usernameMinLength) },
      { name: 'identity.username.max_length', value: String(form.usernameMaxLength) },
      { name: 'identity.username.charset', value: form.usernameCharset },
      { name: 'identity.username.reserved', value: form.usernameReserved.trim() },
      { name: 'profile.nickname.allow_user_change', value: enabledOptionValue(form.nicknameAllowUserChange) },
      { name: 'profile.nickname.cooldown_minutes', value: String(joinCooldownMinutes({
        days: form.nicknameCooldownDays,
        hours: form.nicknameCooldownHours,
        minutes: form.nicknameCooldownMinutes
      })) },
      { name: 'profile.nickname.min_length', value: String(form.nicknameMinLength) },
      { name: 'profile.nickname.max_length', value: String(form.nicknameMaxLength) },
      { name: 'profile.nickname.history_public', value: enabledOptionValue(form.nicknameHistoryPublic) }
    ])
  })
}

function resetChanges() {
  resetFromItems()
  toast.add({ color: 'neutral', icon: 'i-lucide-rotate-ccw', title: t('admin.settings.registration.resetChanges'), duration: 5000 })
}

function restoreRecommended() {
  section.runRestore({
    title: t('admin.settings.registration.restoreDefaults'),
    apply: () => Object.assign(form, {
      registrationMode: 'open',
      requireEmailVerification: false,
      blockPostingUntilVerified: true,
      usernameMinLength: 3,
      usernameMaxLength: 20,
      usernameCharset: 'unicode_letters_numbers',
      usernameReserved: 'admin,administrator,system,sforum,root,support,moderator,mod,official,null,undefined',
      nicknameAllowUserChange: true,
      nicknameCooldownDays: 7,
      nicknameCooldownHours: 0,
      nicknameCooldownMinutes: 0,
      nicknameMinLength: 2,
      nicknameMaxLength: 24,
      nicknameHistoryPublic: false
    })
  })
}

// 预设只是把三段输入填好；「不限」= 0 分钟，与服务端语义一致。
function applyCooldownPreset(totalMinutes: number) {
  const parts = splitCooldownMinutes(totalMinutes)
  form.nicknameCooldownDays = parts.days
  form.nicknameCooldownHours = parts.hours
  form.nicknameCooldownMinutes = parts.minutes
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  const normalized = Math.trunc(parsed)
  return normalized >= min && normalized <= max ? normalized : fallback
}

function blockNonIntegerKey(event: KeyboardEvent) {
  const allowed = ['Backspace', 'Delete', 'Tab', 'Escape', 'Enter', 'ArrowLeft', 'ArrowRight', 'Home', 'End']
  if (!allowed.includes(event.key) && !event.metaKey && !event.ctrlKey && !/^\d$/.test(event.key)) event.preventDefault()
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="save">
    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold">{{ t('admin.settings.registration.title') }}</h2>
            <p class="mt-1 text-xs text-muted">{{ t('admin.settings.registration.description') }}</p>
          </div>
          <UBadge color="neutral" variant="soft" class="font-mono">identity.registration.* / identity.username.* / profile.nickname.*</UBadge>
        </div>
      </template>

      <div class="space-y-5">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <UAlert color="neutral" variant="soft" icon="i-lucide-info" :title="t('admin.settings.registration.recommended')" class="flex-1" />
          <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" @click="restoreRecommended">{{ t('admin.settings.registration.restoreDefaults') }}</UButton>
        </div>
        <UFormField :label="t('admin.settings.registration.mode')" name="registration-mode">
          <SFNativeSelect v-model="form.registrationMode" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950">
            <option value="open">{{ t('admin.settings.registration.modes.open') }}</option>
            <option value="invite">{{ t('admin.settings.registration.modes.invite') }}</option>
            <option value="approval">{{ t('admin.settings.registration.modes.approval') }}</option>
            <option value="closed">{{ t('admin.settings.registration.modes.closed') }}</option>
          </SFNativeSelect>
          <p class="mt-2 text-xs text-muted">{{ t('admin.settings.registration.modeHint') }}</p>
        </UFormField>
        <div class="grid gap-3 md:grid-cols-2">
          <label class="flex cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
            <input v-model="form.requireEmailVerification" type="checkbox" class="mt-1 size-4">
            <span><strong class="block">{{ t('admin.settings.registration.requireEmailVerification') }}</strong><span class="mt-1 block text-xs text-muted">{{ t('admin.settings.registration.requireEmailVerificationHint') }}</span></span>
          </label>
          <label class="flex cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
            <input v-model="form.blockPostingUntilVerified" type="checkbox" class="mt-1 size-4">
            <span><strong class="block">{{ t('admin.settings.registration.blockPostingUntilVerified') }}</strong><span class="mt-1 block text-xs text-muted">{{ t('admin.settings.registration.blockPostingUntilVerifiedHint') }}</span></span>
          </label>
        </div>
        <section class="space-y-4 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div>
            <h3 class="text-sm font-semibold">{{ t('admin.settings.registration.usernameTitle') }}</h3>
            <p class="mt-1 text-xs text-muted">{{ t('admin.settings.registration.usernameDescription') }}</p>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.settings.registration.usernameMinLength')" name="username-min">
              <UInput v-model.number="form.usernameMinLength" type="number" min="2" max="32" required class="w-full" @keydown="blockNonIntegerKey" />
            </UFormField>
            <UFormField :label="t('admin.settings.registration.usernameMaxLength')" name="username-max">
              <UInput v-model.number="form.usernameMaxLength" type="number" min="2" max="64" required class="w-full" @keydown="blockNonIntegerKey" />
            </UFormField>
          </div>
          <UFormField :label="t('admin.settings.registration.usernameCharset')" name="username-charset">
            <SFNativeSelect v-model="form.usernameCharset" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950">
              <option value="unicode_letters_numbers">{{ t('admin.settings.registration.charsetUnicode') }}</option>
              <option value="ascii">{{ t('admin.settings.registration.charsetAscii') }}</option>
            </SFNativeSelect>
          </UFormField>
          <UFormField :label="t('admin.settings.registration.usernameReserved')" :description="t('admin.settings.registration.usernameReservedHint')" name="username-reserved">
            <UTextarea v-model="form.usernameReserved" :rows="3" class="w-full" :placeholder="t('admin.settings.registration.usernameReservedPlaceholder')" />
          </UFormField>
        </section>

        <section class="space-y-4 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div>
            <h3 class="text-sm font-semibold">{{ t('admin.settings.registration.nicknameTitle') }}</h3>
            <p class="mt-1 text-xs text-muted">{{ t('admin.settings.registration.nicknameDescription') }}</p>
          </div>
          <label class="flex cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
            <input v-model="form.nicknameAllowUserChange" type="checkbox" class="mt-1 size-4">
            <span><strong class="block">{{ t('admin.settings.registration.nicknameAllowUserChange') }}</strong><span class="mt-1 block text-xs text-muted">{{ t('admin.settings.registration.nicknameAllowUserChangeHint') }}</span></span>
          </label>
          <div>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <h4 class="text-xs font-semibold uppercase tracking-wide text-muted">{{ t('admin.settings.registration.nicknameCooldown') }}</h4>
              <div class="flex flex-wrap gap-2">
                <UButton type="button" size="xs" color="neutral" variant="outline" @click="applyCooldownPreset(0)">{{ t('admin.settings.registration.nicknameCooldownUnlimited') }}</UButton>
                <UButton type="button" size="xs" color="neutral" variant="outline" @click="applyCooldownPreset(1440)">{{ t('admin.settings.registration.nicknameCooldownOneDay') }}</UButton>
                <UButton type="button" size="xs" color="neutral" variant="outline" @click="applyCooldownPreset(10080)">{{ t('admin.settings.registration.nicknameCooldownSevenDays') }}</UButton>
                <UButton type="button" size="xs" color="neutral" variant="outline" @click="applyCooldownPreset(43200)">{{ t('admin.settings.registration.nicknameCooldownThirtyDays') }}</UButton>
              </div>
            </div>
            <div class="mt-2 grid gap-4 md:grid-cols-3">
              <UFormField :label="t('admin.settings.registration.nicknameCooldownDaysLabel')" name="nickname-cooldown-days">
                <UInput v-model.number="form.nicknameCooldownDays" type="number" min="0" max="365" class="w-full" @keydown="blockNonIntegerKey" />
              </UFormField>
              <UFormField :label="t('admin.settings.registration.nicknameCooldownHoursLabel')" name="nickname-cooldown-hours">
                <UInput v-model.number="form.nicknameCooldownHours" type="number" min="0" max="23" class="w-full" @keydown="blockNonIntegerKey" />
              </UFormField>
              <UFormField :label="t('admin.settings.registration.nicknameCooldownMinutesLabel')" name="nickname-cooldown-minutes">
                <UInput v-model.number="form.nicknameCooldownMinutes" type="number" min="0" max="59" class="w-full" @keydown="blockNonIntegerKey" />
              </UFormField>
            </div>
            <p class="mt-2 text-xs text-muted">{{ t('admin.settings.registration.nicknameCooldownHint') }}</p>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.settings.registration.nicknameMinLength')" name="nickname-min">
              <UInput v-model.number="form.nicknameMinLength" type="number" min="1" max="32" class="w-full" @keydown="blockNonIntegerKey" />
            </UFormField>
            <UFormField :label="t('admin.settings.registration.nicknameMaxLength')" name="nickname-max">
              <UInput v-model.number="form.nicknameMaxLength" type="number" min="2" max="64" class="w-full" @keydown="blockNonIntegerKey" />
            </UFormField>
          </div>
          <label class="flex cursor-pointer items-start gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
            <input v-model="form.nicknameHistoryPublic" type="checkbox" class="mt-1 size-4">
            <span><strong class="block">{{ t('admin.settings.registration.nicknameHistoryPublic') }}</strong><span class="mt-1 block text-xs text-muted">{{ t('admin.settings.registration.nicknameHistoryPublicHint') }}</span></span>
          </label>
          <p class="text-xs text-muted">{{ t('admin.settings.registration.nicknameReservedHint') }}</p>
        </section>
      </div>

      <template #footer>
        <SFAdminFormFooter :saving="section.saving.value" :show-unsaved-alert="hasChanges" :submit-text="t('admin.settings.save')" @reset="resetChanges" />
      </template>
    </UCard>
  </form>
</template>
