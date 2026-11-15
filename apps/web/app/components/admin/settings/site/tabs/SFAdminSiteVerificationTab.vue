<script setup lang="ts">
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import type {
  AdminWebOption,
  AltchaWidgetAuto,
  AltchaWidgetDisplay,
  AltchaWidgetType,
  HumanVerificationScenario,
  WebOption
} from '~/composables/useWebOptions'
import {
  altchaWidgetAutoModes,
  altchaWidgetDisplays,
  altchaWidgetTypes,
  enabledOptionValue,
  humanVerificationScenarioOptionName,
  normalizeEnabledOption
} from '~/composables/useWebOptions'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const toast = useToast()
const section = useSettingsSection()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))
const showSecret = ref(false)

const scenarioFallbacks: Record<HumanVerificationScenario, boolean> = {
  register: true,
  password_reset: true,
  email_verification: true,
  login_risk: false,
  post_risk: false
}
const scenarios = computed(() => [
  { key: 'register' as const, label: t('admin.settings.verification.scenarios.register.label'), description: t('admin.settings.verification.scenarios.register.description'), icon: 'i-lucide-user-plus' },
  { key: 'password_reset' as const, label: t('admin.settings.verification.scenarios.passwordReset.label'), description: t('admin.settings.verification.scenarios.passwordReset.description'), icon: 'i-lucide-key-round' },
  { key: 'email_verification' as const, label: t('admin.settings.verification.scenarios.emailVerification.label'), description: t('admin.settings.verification.scenarios.emailVerification.description'), icon: 'i-lucide-mail-check' },
  { key: 'login_risk' as const, label: t('admin.settings.verification.scenarios.loginRisk.label'), description: t('admin.settings.verification.scenarios.loginRisk.description'), icon: 'i-lucide-radar' },
  { key: 'post_risk' as const, label: t('admin.settings.verification.scenarios.postRisk.label'), description: t('admin.settings.verification.scenarios.postRisk.description'), icon: 'i-lucide-message-square-warning' }
])
const typeOptions = computed(() => altchaWidgetTypes.map(value => ({ value, label: t(`admin.settings.verification.widget.typeOptions.${value}`) })))
const autoOptions = computed(() => altchaWidgetAutoModes.map(value => ({ value, label: t(`admin.settings.verification.widget.autoOptions.${value}`) })))
const displayOptions = computed(() => altchaWidgetDisplays.map(value => ({ value, label: t(`admin.settings.verification.widget.displayOptions.${value}`) })))
const loginTriggers = computed(() => [
  { key: 'always' as const, field: 'loginAlways' as const, label: t('admin.settings.verification.login.always.label'), description: t('admin.settings.verification.login.always.description'), icon: 'i-lucide-shield-check' },
  { key: 'new_device' as const, field: 'loginNewDevice' as const, label: t('admin.settings.verification.login.newDevice.label'), description: t('admin.settings.verification.login.newDevice.description'), icon: 'i-lucide-monitor-smartphone' },
  { key: 'privileged' as const, field: 'loginPrivileged' as const, label: t('admin.settings.verification.login.privileged.label'), description: t('admin.settings.verification.login.privileged.description'), icon: 'i-lucide-user-cog' }
])
const form = reactive({
  provider: 'disabled' as 'disabled' | 'altcha',
  scenarios: { ...scenarioFallbacks },
  loginAlways: false,
  loginNewDevice: false,
  loginPrivileged: false,
  rateLimit: 60,
  secret: '',
  secretSet: false,
  challengeTTLMinutes: 10,
  cost: 1000,
  widgetType: 'checkbox' as AltchaWidgetType,
  widgetAuto: 'off' as AltchaWidgetAuto,
  widgetDisplay: 'standard' as AltchaWidgetDisplay,
  widgetHideLogo: true,
  widgetHideFooter: true,
  widgetWorkers: 2,
  widgetMinDuration: 500
})
// 触发条件已开但总闸/provider 没开时，明确告诉运营「现在不会生效」，避免静默失效。
const loginTriggersInactive = computed(() => {
  const anyTrigger = form.loginAlways || form.loginNewDevice || form.loginPrivileged
  return anyTrigger && (form.provider !== 'altcha' || !form.scenarios.login_risk)
})
const initial = computed(() => ({
  provider: normalizeProvider(map.value['human_verification.provider']?.value),
  scenarios: readScenarioSettings(map.value),
  loginAlways: normalizeEnabledOption(map.value['human_verification.login_risk.always']?.value, false),
  loginNewDevice: normalizeEnabledOption(map.value['human_verification.login_risk.new_device']?.value, false),
  loginPrivileged: normalizeEnabledOption(map.value['human_verification.login_risk.privileged']?.value, false),
  rateLimit: boundedInteger(map.value['human_verification.rate_limit.per_minute']?.value, 60, 1, 600),
  secretSet: map.value['human_verification.altcha.secret']?.secretSet === true,
  challengeTTLMinutes: durationToMinutes(map.value['human_verification.altcha.challenge_ttl']?.value || '10m'),
  cost: normalizeCost(map.value['human_verification.altcha.cost']?.value),
  widgetType: normalizeChoice(map.value['human_verification.altcha.widget.type']?.value, altchaWidgetTypes, 'checkbox'),
  widgetAuto: normalizeChoice(map.value['human_verification.altcha.widget.auto']?.value, altchaWidgetAutoModes, 'off'),
  widgetDisplay: normalizeChoice(map.value['human_verification.altcha.widget.display']?.value, altchaWidgetDisplays, 'standard'),
  widgetHideLogo: normalizeEnabledOption(map.value['human_verification.altcha.widget.hide_logo']?.value, true),
  widgetHideFooter: normalizeEnabledOption(map.value['human_verification.altcha.widget.hide_footer']?.value, true),
  widgetWorkers: boundedInteger(map.value['human_verification.altcha.widget.workers']?.value, 2, 1, 16),
  widgetMinDuration: boundedInteger(map.value['human_verification.altcha.widget.min_duration_ms']?.value, 500, 0, 10000)
}))
const comparableForm = computed(() => ({ ...form, secret: undefined }))
const comparableInitial = computed(() => ({ ...initial.value, secret: undefined }))
const hasChanges = computed(() => form.secret.trim() !== '' || JSON.stringify(comparableForm.value) !== JSON.stringify(comparableInitial.value))
const secretPlaceholder = computed(() => form.secretSet ? t('admin.settings.verification.keepSecretPlaceholder') : t('admin.settings.verification.secretPlaceholder'))
const configRows = computed(() => [
  { label: t('admin.settings.verification.config.algorithm'), value: 'PBKDF2/SHA-256' },
  { label: t('admin.settings.verification.config.signature'), value: 'HMAC-SHA-256' },
  { label: t('admin.settings.verification.config.challengeEndpoint'), value: '/api/v1/human-verification/challenge?purpose={purpose}' },
  { label: t('admin.settings.verification.config.widgetType'), value: form.widgetType },
  { label: t('admin.settings.verification.config.widgetAuto'), value: form.widgetAuto },
  { label: t('admin.settings.verification.config.widgetDisplay'), value: form.widgetDisplay },
  { label: t('admin.settings.verification.config.replayProtection'), value: t('admin.settings.verification.config.replayProtectionValue') },
  { label: t('admin.settings.verification.config.rateLimit'), value: t('admin.settings.verification.config.rateLimitValue') },
  { label: t('admin.settings.verification.config.clientWidget'), value: 'ALTCHA widget v3' }
])

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  Object.assign(form, initial.value, { scenarios: { ...initial.value.scenarios }, secret: '' })
}

async function save() {
  await section.runSave({
    successTitle: t('admin.settings.saved'),
    failureTitle: t('admin.settings.saveFailed'),
    prepare: () => {
      form.challengeTTLMinutes = positiveInteger(form.challengeTTLMinutes, 10)
      form.cost = normalizeCost(form.cost)
      form.widgetWorkers = boundedInteger(form.widgetWorkers, 2, 1, 16)
      form.widgetMinDuration = boundedInteger(form.widgetMinDuration, 500, 0, 10000)
      form.rateLimit = boundedInteger(form.rateLimit, 60, 1, 600)
    },
    save: async () => {
      const payload: WebOption[] = [
        { name: 'human_verification.provider', value: form.provider },
        ...scenarios.value.map(scenario => ({
          name: humanVerificationScenarioOptionName(scenario.key),
          value: enabledOptionValue(form.scenarios[scenario.key])
        })),
        { name: 'human_verification.login_risk.always', value: enabledOptionValue(form.loginAlways) },
        { name: 'human_verification.login_risk.new_device', value: enabledOptionValue(form.loginNewDevice) },
        { name: 'human_verification.login_risk.privileged', value: enabledOptionValue(form.loginPrivileged) },
        { name: 'human_verification.rate_limit.per_minute', value: String(form.rateLimit) },
        { name: 'human_verification.altcha.challenge_ttl', value: `${form.challengeTTLMinutes}m` },
        { name: 'human_verification.altcha.cost', value: String(form.cost) },
        { name: 'human_verification.altcha.widget.type', value: form.widgetType },
        { name: 'human_verification.altcha.widget.auto', value: form.widgetAuto },
        { name: 'human_verification.altcha.widget.display', value: form.widgetDisplay },
        { name: 'human_verification.altcha.widget.hide_logo', value: enabledOptionValue(form.widgetHideLogo) },
        { name: 'human_verification.altcha.widget.hide_footer', value: enabledOptionValue(form.widgetHideFooter) },
        { name: 'human_verification.altcha.widget.workers', value: String(form.widgetWorkers) },
        { name: 'human_verification.altcha.widget.min_duration_ms', value: String(form.widgetMinDuration) }
      ]
      if (form.secret.trim()) payload.push({ name: 'human_verification.altcha.secret', value: form.secret })
      await saveOptions(payload)
    }
  })
}

function resetChanges() {
  resetFromItems()
  toast.add({ color: 'neutral', icon: 'i-lucide-rotate-ccw', title: t('admin.settings.verification.resetChanges'), duration: 5000 })
}

function restoreRecommended() {
  section.runRestore({
    title: t('admin.settings.verification.restoreDefaults'),
    apply: () => Object.assign(form, {
      provider: 'disabled',
      scenarios: { ...scenarioFallbacks },
      loginAlways: false,
      loginNewDevice: false,
      loginPrivileged: false,
      rateLimit: 60,
      secret: '',
      challengeTTLMinutes: 10,
      cost: 1000,
      widgetType: 'checkbox',
      widgetAuto: 'off',
      widgetDisplay: 'standard',
      widgetHideLogo: true,
      widgetHideFooter: true,
      widgetWorkers: 2,
      widgetMinDuration: 500
    })
  })
}

function generateSecret() {
  if (!globalThis.crypto?.getRandomValues) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: t('admin.settings.verification.secretGenerateUnavailable') })
    return
  }
  const bytes = new Uint8Array(32)
  globalThis.crypto.getRandomValues(bytes)
  form.secret = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
  toast.add({ color: 'success', icon: 'i-lucide-key-round', title: t('admin.settings.verification.secretGenerated'), duration: 5000 })
}

function toggleSecretVisibility() {
  showSecret.value = !showSecret.value
}

function readScenarioSettings(options: Record<string, AdminWebOption>) {
  return Object.fromEntries((Object.keys(scenarioFallbacks) as HumanVerificationScenario[]).map(scenario => [
    scenario,
    normalizeEnabledOption(options[humanVerificationScenarioOptionName(scenario)]?.value, scenarioFallbacks[scenario])
  ])) as Record<HumanVerificationScenario, boolean>
}

function durationToMinutes(value: string) {
  const raw = value.trim().toLowerCase()
  if (/^\d+$/.test(raw)) return positiveInteger(Number(raw), 10)
  const seconds = Number(raw.match(/(\d+)h/)?.[1] || 0) * 3600 + Number(raw.match(/(\d+)m/)?.[1] || 0) * 60 + Number(raw.match(/(\d+)s/)?.[1] || 0)
  return seconds > 0 ? Math.max(1, Math.ceil(seconds / 60)) : 10
}

function positiveInteger(value: unknown, fallback: number) {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : fallback
}

function boundedInteger(value: unknown, fallback: number, min: number, max: number) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return fallback
  const normalized = Math.trunc(parsed)
  return normalized >= min && normalized <= max ? normalized : fallback
}

function normalizeCost(value: unknown) {
  return positiveInteger(value, 1000)
}

function normalizeProvider(value: string | undefined) {
  return value?.trim().toLowerCase() === 'altcha' ? 'altcha' as const : 'disabled' as const
}

function normalizeChoice<T extends string>(value: string | undefined, choices: readonly T[], fallback: T): T {
  const normalized = value?.trim().toLowerCase()
  return choices.find(choice => choice === normalized) || fallback
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
          <div><h2 class="text-base font-bold">{{ t('admin.settings.verification.title') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.settings.verification.description') }}</p></div>
          <UBadge color="neutral" variant="soft" class="font-mono">ALTCHA</UBadge>
        </div>
      </template>

      <div class="grid max-w-5xl gap-6">
        <div class="flex justify-end">
          <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" @click="restoreRecommended">{{ t('admin.settings.verification.restoreDefaults') }}</UButton>
        </div>
        <UFormField :label="t('admin.settings.verification.provider')" name="verification-provider">
          <SFNativeSelect v-model="form.provider" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950">
            <option value="disabled">{{ t('admin.settings.verification.disabled') }}</option>
            <option value="altcha">{{ t('admin.settings.verification.altcha') }}</option>
          </SFNativeSelect>
        </UFormField>

        <section class="space-y-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div><h3 class="text-sm font-semibold">{{ t('admin.settings.verification.scenarios.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.settings.verification.scenarios.description') }}</p></div>
          <div class="grid gap-3 md:grid-cols-2">
            <label v-for="scenario in scenarios" :key="scenario.key" class="flex cursor-pointer gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
              <input v-model="form.scenarios[scenario.key]" type="checkbox" class="mt-1 size-4">
              <span><strong class="flex items-center gap-2"><UIcon :name="scenario.icon" class="size-4" />{{ scenario.label }}</strong><span class="mt-1 block text-xs text-muted">{{ scenario.description }}</span></span>
            </label>
          </div>
        </section>

        <section class="space-y-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div><h3 class="text-sm font-semibold">{{ t('admin.settings.verification.login.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.settings.verification.login.description') }}</p></div>
          <div class="grid gap-3 md:grid-cols-3">
            <label v-for="trigger in loginTriggers" :key="trigger.key" class="flex cursor-pointer gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950/60">
              <input v-model="form[trigger.field]" type="checkbox" class="mt-1 size-4">
              <span><strong class="flex items-center gap-2"><UIcon :name="trigger.icon" class="size-4" />{{ trigger.label }}</strong><span class="mt-1 block text-xs text-muted">{{ trigger.description }}</span></span>
            </label>
          </div>
          <UFormField :label="t('admin.settings.verification.login.rateLimit')" name="human-verification-rate-limit">
            <UInput v-model.number="form.rateLimit" type="number" min="1" max="600" required class="w-40" @keydown="blockNonIntegerKey" />
            <p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.login.rateLimitHint') }}</p>
          </UFormField>
          <p v-if="loginTriggersInactive" class="text-xs text-amber-600 dark:text-amber-400">{{ t('admin.settings.verification.login.inactiveHint') }}</p>
        </section>

        <UFormField :label="t('admin.settings.verification.altchaSecret')" name="altcha-secret" class="border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div class="flex flex-wrap items-center gap-2">
            <UInput v-model="form.secret" icon="i-lucide-key-round" :type="showSecret ? 'text' : 'password'" :placeholder="secretPlaceholder" class="min-w-[200px] flex-1" />
            <UButton type="button" color="neutral" variant="outline" :icon="showSecret ? 'i-lucide-eye-off' : 'i-lucide-eye'" :aria-label="showSecret ? t('admin.settings.verification.hideSecret') : t('admin.settings.verification.showSecret')" @click="toggleSecretVisibility" />
            <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-key-round" @click="generateSecret">{{ t('admin.settings.verification.generateSecret') }}</UButton>
            <UBadge :color="form.secretSet ? 'success' : 'neutral'" variant="soft">{{ form.secretSet ? t('admin.settings.verification.secretConfigured') : t('admin.settings.verification.secretMissing') }}</UBadge>
          </div>
          <p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.secretHint') }}</p>
        </UFormField>

        <section class="space-y-4 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div><h3 class="text-sm font-semibold">{{ t('admin.settings.verification.widget.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.settings.verification.widget.description') }}</p></div>
          <div class="grid gap-4 md:grid-cols-3">
            <UFormField :label="t('admin.settings.verification.widget.type')" name="widget-type">
              <SFNativeSelect v-model="form.widgetType" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option v-for="item in typeOptions" :key="item.value" :value="item.value">{{ item.label }}</option></SFNativeSelect>
              <p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.widget.typeHint') }}</p>
            </UFormField>
            <UFormField :label="t('admin.settings.verification.widget.auto')" name="widget-auto">
              <SFNativeSelect v-model="form.widgetAuto" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option v-for="item in autoOptions" :key="item.value" :value="item.value">{{ item.label }}</option></SFNativeSelect>
              <p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.widget.autoHint') }}</p>
            </UFormField>
            <UFormField :label="t('admin.settings.verification.widget.display')" name="widget-display">
              <SFNativeSelect v-model="form.widgetDisplay" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option v-for="item in displayOptions" :key="item.value" :value="item.value">{{ item.label }}</option></SFNativeSelect>
              <p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.widget.displayHint') }}</p>
            </UFormField>
          </div>
          <div class="grid gap-3 md:grid-cols-2">
            <label class="flex items-start gap-3 rounded-md border border-slate-200 p-3 text-sm dark:border-zinc-800"><input v-model="form.widgetHideLogo" type="checkbox" class="mt-1 size-4"><span><strong class="block">{{ t('admin.settings.verification.widget.hideLogo') }}</strong><span class="text-xs text-muted">{{ t('admin.settings.verification.widget.hideLogoHint') }}</span></span></label>
            <label class="flex items-start gap-3 rounded-md border border-slate-200 p-3 text-sm dark:border-zinc-800"><input v-model="form.widgetHideFooter" type="checkbox" class="mt-1 size-4"><span><strong class="block">{{ t('admin.settings.verification.widget.hideFooter') }}</strong><span class="text-xs text-muted">{{ t('admin.settings.verification.widget.hideFooterHint') }}</span></span></label>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.settings.verification.widget.workers')" name="widget-workers"><UInput v-model.number="form.widgetWorkers" type="number" min="1" max="16" required class="w-full" @keydown="blockNonIntegerKey" /><p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.widget.workersHint') }}</p></UFormField>
            <UFormField :label="t('admin.settings.verification.widget.minDuration')" name="widget-min-duration"><UInput v-model.number="form.widgetMinDuration" type="number" min="0" max="10000" step="100" required class="w-full" @keydown="blockNonIntegerKey" /><p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.widget.minDurationHint') }}</p></UFormField>
          </div>
        </section>

        <section class="grid gap-4 border-t border-slate-200 pt-4 dark:border-zinc-800 md:grid-cols-2">
          <UFormField :label="t('admin.settings.verification.challengeTTL')" name="challenge-ttl"><UInput v-model.number="form.challengeTTLMinutes" type="number" min="1" required class="w-full" @keydown="blockNonIntegerKey" /><p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.challengeTTLHint') }}</p></UFormField>
          <UFormField :label="t('admin.settings.verification.cost')" name="altcha-cost"><UInput v-model.number="form.cost" type="number" min="1" step="1" required class="w-full" @keydown="blockNonIntegerKey" /><p class="mt-2 text-xs text-muted">{{ t('admin.settings.verification.costHint') }}</p></UFormField>
        </section>

        <section class="space-y-3 border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div><h3 class="text-sm font-semibold">{{ t('admin.settings.verification.config.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.settings.verification.config.description') }}</p></div>
          <dl class="grid gap-3 text-sm md:grid-cols-2"><div v-for="row in configRows" :key="row.label" class="border-b border-slate-100 pb-2 dark:border-zinc-800"><dt class="text-xs text-muted">{{ row.label }}</dt><dd class="break-words font-mono text-xs">{{ row.value }}</dd></div></dl>
        </section>
      </div>

      <template #footer>
        <SFAdminFormFooter :saving="section.saving.value" :show-unsaved-alert="hasChanges" :submit-text="t('admin.settings.save')" @reset="resetChanges" />
      </template>
    </UCard>
  </form>
</template>
