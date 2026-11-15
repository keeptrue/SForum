<script setup lang="ts">
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'
import {
  commonSiteTimezones,
  normalizeSiteDateFormat,
  normalizeSiteStartOfWeek,
  normalizeSiteTimeFormat,
  normalizeSiteTimezone,
  previewSiteDateTime,
  recommendedSiteDateTimeSettings,
  siteDateFormats,
  siteTimeFormats,
  type SiteDateFormat,
  type SiteTimeFormat
} from '~/utils/siteDateTime'
import { normalizeSiteDomain, siteDomainFromUrl } from '~/utils/settings/siteDomain'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const toast = useToast()
const section = useSettingsSection()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))

const form = reactive({
  siteName: 'SForum',
  siteUrl: '',
  siteDomain: '',
  aboutUrl: '',
  aboutOpenInNewTab: false,
  defaultLocale: 'zh-CN',
  supportedLocales: ['zh-CN', 'en-US'],
  tagline: '',
  adminEmail: '',
  timezone: 'Asia/Shanghai',
  dateFormat: 'YYYY-MM-DD' as SiteDateFormat,
  timeFormat: 'HH:mm' as SiteTimeFormat,
  startOfWeek: 1
})

const localeChoices = computed(() => [
  { label: t('admin.settings.locale.zhCN'), value: 'zh-CN' },
  { label: t('admin.settings.locale.enUS'), value: 'en-US' }
])
const dateFormatChoices = computed(() => siteDateFormats.map(value => ({
  value,
  label: t(`admin.settings.basic.dateFormatOptions.${value}`)
})))
const timeFormatChoices = computed(() => siteTimeFormats.map(value => ({
  value,
  label: t(`admin.settings.basic.timeFormatOptions.${value}`)
})))
const startOfWeekChoices = computed(() => Array.from({ length: 7 }, (_, value) => ({
  value,
  label: t(`admin.settings.basic.weekdays.${value}`)
})))
const timezoneChoices = computed(() => {
  const values = [...commonSiteTimezones] as string[]
  if (form.timezone && !values.includes(form.timezone)) values.unshift(form.timezone)
  return values.map(value => ({
    value,
    label: value === 'UTC' ? t('admin.settings.basic.timezoneUtc') : value
  }))
})

const initial = computed(() => ({
  siteName: map.value['site.name']?.value || 'SForum',
  siteUrl: map.value['site.url']?.overrideValue ?? '',
  siteDomain: normalizeSiteDomain(map.value['site.domain']?.value),
  aboutUrl: (map.value['site.about_url']?.value || '').trim(),
  aboutOpenInNewTab: (map.value['site.about_open_in_new_tab']?.value || '').trim() === 'enabled',
  defaultLocale: map.value['site.default_locale']?.value || 'zh-CN',
  supportedLocales: parseLocaleList(map.value['site.supported_locales']?.value || 'zh-CN,en-US'),
  tagline: (map.value['site.tagline']?.value || '').trim(),
  adminEmail: (map.value['site.admin_email']?.value || '').trim(),
  timezone: normalizeSiteTimezone(map.value['site.timezone']?.value),
  dateFormat: normalizeSiteDateFormat(map.value['site.date_format']?.value),
  timeFormat: normalizeSiteTimeFormat(map.value['site.time_format']?.value),
  startOfWeek: normalizeSiteStartOfWeek(map.value['site.start_of_week']?.value)
}))
const siteUrlFallback = computed(() => {
  const option = map.value['site.url']
  return option?.fallbackValue || option?.value || 'http://127.0.0.1:3000'
})
const datetimePreview = computed(() => previewSiteDateTime({
  timezone: form.timezone,
  dateFormat: form.dateFormat,
  timeFormat: form.timeFormat,
  startOfWeek: form.startOfWeek
}, form.defaultLocale))
const hasChanges = computed(() => JSON.stringify(form) !== JSON.stringify(initial.value))

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  Object.assign(form, initial.value, { supportedLocales: [...initial.value.supportedLocales] })
}

async function save() {
  form.siteDomain = normalizeSiteDomain(form.siteDomain)
  await section.runSave({
    successTitle: t('admin.settings.saved'),
    failureTitle: t('admin.settings.saveFailed'),
    save: () => saveOptions([
      { name: 'site.name', value: form.siteName },
      { name: 'site.url', value: form.siteUrl.trim() },
      { name: 'site.domain', value: form.siteDomain },
      { name: 'site.about_url', value: form.aboutUrl.trim() },
      { name: 'site.about_open_in_new_tab', value: form.aboutOpenInNewTab ? 'enabled' : 'disabled' },
      { name: 'site.default_locale', value: form.defaultLocale },
      { name: 'site.supported_locales', value: form.supportedLocales.join(',') },
      { name: 'site.tagline', value: form.tagline.trim() },
      { name: 'site.admin_email', value: form.adminEmail.trim() },
      { name: 'site.timezone', value: form.timezone },
      { name: 'site.date_format', value: form.dateFormat },
      { name: 'site.time_format', value: form.timeFormat },
      { name: 'site.start_of_week', value: String(form.startOfWeek) }
    ])
  })
}

function resetChanges() {
  resetFromItems()
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.settings.basic.resetChanges'),
    duration: 5000
  })
}

function restoreRecommendedDateTimeSettings() {
  section.runRestore({
    title: t('admin.settings.basic.restoreDateTimeDefaults'),
    apply: () => Object.assign(form, recommendedSiteDateTimeSettings)
  })
}

function onLocaleToggle(locale: string, event: Event) {
  const checked = event.target instanceof HTMLInputElement && event.target.checked
  const selected = new Set(form.supportedLocales)
  if (checked) selected.add(locale)
  else if (selected.size > 1) selected.delete(locale)
  form.supportedLocales = localeChoices.value.map(choice => choice.value).filter(value => selected.has(value))
  if (!form.supportedLocales.includes(form.defaultLocale)) {
    form.defaultLocale = form.supportedLocales[0] || 'zh-CN'
  }
}

function parseLocaleList(value: string) {
  const locales = value.split(',').map(item => item.trim()).filter(Boolean)
  return locales.length > 0 ? locales : ['zh-CN', 'en-US']
}

function useEnvironmentSiteUrl() {
  form.siteUrl = ''
}

function useSiteUrlDomain() {
  form.siteDomain = siteDomainFromUrl(form.siteUrl || siteUrlFallback.value)
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="save">
    <UCard
      class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100"
      :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }"
    >
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('admin.settings.basic.title') }}</h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.settings.basic.description') }}</p>
          </div>
          <UBadge color="neutral" variant="soft" class="border border-slate-200 font-mono dark:border-zinc-800">site.*</UBadge>
        </div>
      </template>

      <div class="grid max-w-5xl gap-5">
        <UFormField :label="t('admin.settings.siteName')" name="site-name">
          <UInput v-model="form.siteName" size="lg" icon="i-lucide-message-square-text" :placeholder="t('admin.settings.siteNamePlaceholder')" maxlength="80" required class="w-full" />
        </UFormField>
        <UFormField :label="t('admin.settings.siteUrl')" name="site-url">
          <UInput v-model="form.siteUrl" size="lg" icon="i-lucide-link" type="url" :placeholder="t('admin.settings.siteUrlPlaceholder')" class="w-full" />
          <div class="mt-2 flex flex-wrap items-center justify-between gap-2">
            <p class="text-xs text-muted">{{ t('admin.settings.siteUrlHint', { url: siteUrlFallback }) }}</p>
            <UButton type="button" color="neutral" variant="ghost" size="xs" icon="i-lucide-rotate-ccw" :disabled="!form.siteUrl" @click="useEnvironmentSiteUrl">
              {{ t('admin.settings.siteUrlUseEnvironment') }}
            </UButton>
          </div>
        </UFormField>
        <UFormField :label="t('admin.settings.siteDomain')" name="site-domain">
          <UInput v-model="form.siteDomain" size="lg" icon="i-lucide-globe-2" type="text" inputmode="url" :placeholder="t('admin.settings.siteDomainPlaceholder')" maxlength="253" required class="w-full" />
          <div class="mt-2 flex flex-wrap items-center justify-between gap-2">
            <p class="text-xs text-muted">{{ t('admin.settings.siteDomainHint') }}</p>
            <UButton type="button" color="neutral" variant="ghost" size="xs" icon="i-lucide-rotate-ccw" @click="useSiteUrlDomain">
              {{ t('admin.settings.siteDomainUseSiteUrl') }}
            </UButton>
          </div>
        </UFormField>
        <UFormField :label="t('admin.settings.siteTagline')" name="site-tagline">
          <UInput v-model="form.tagline" size="lg" icon="i-lucide-quote" :placeholder="t('admin.settings.siteTaglinePlaceholder')" maxlength="160" class="w-full" />
          <template #hint>{{ t('admin.settings.siteTaglineHint') }}</template>
        </UFormField>
        <UFormField :label="t('admin.settings.siteAboutUrl')" name="site-about-url">
          <UInput v-model="form.aboutUrl" size="lg" icon="i-lucide-info" type="text" inputmode="url" :placeholder="t('admin.settings.siteAboutUrlPlaceholder')" maxlength="2048" class="w-full" />
          <template #hint>{{ t('admin.settings.siteAboutUrlHint') }}</template>
        </UFormField>
        <label class="flex w-fit items-center gap-2 rounded-md border border-slate-200 bg-white px-3 py-2 text-sm dark:border-zinc-700 dark:bg-zinc-950">
          <input v-model="form.aboutOpenInNewTab" type="checkbox" class="size-4 rounded border-slate-300 text-[var(--sf-accent)] focus:ring-[var(--sf-accent)]">
          <span>{{ t('admin.settings.siteAboutOpenInNewTab') }}</span>
        </label>
        <UFormField :label="t('admin.settings.adminEmail')" name="site-admin-email">
          <UInput v-model="form.adminEmail" size="lg" icon="i-lucide-mail" type="email" :placeholder="t('admin.settings.adminEmailPlaceholder')" maxlength="254" class="w-full" />
          <template #hint>{{ t('admin.settings.adminEmailHint') }}</template>
        </UFormField>
        <UFormField :label="t('admin.settings.defaultLocale')" name="default-locale">
          <SFNativeSelect v-model="form.defaultLocale" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base outline-none focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950">
            <option v-for="choice in localeChoices.filter(choice => form.supportedLocales.includes(choice.value))" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
          </SFNativeSelect>
        </UFormField>
        <UFormField :label="t('admin.settings.supportedLocales')" name="supported-locales">
          <div class="flex flex-wrap gap-3">
            <label v-for="choice in localeChoices" :key="choice.value" class="flex h-10 items-center gap-2 rounded-md border border-slate-200 bg-white px-3 text-sm dark:border-zinc-700 dark:bg-zinc-950">
              <input type="checkbox" class="size-4 rounded border-slate-300 text-[var(--sf-accent)] focus:ring-[var(--sf-accent)]" :checked="form.supportedLocales.includes(choice.value)" @change="onLocaleToggle(choice.value, $event)">
              <span>{{ choice.label }}</span>
            </label>
          </div>
        </UFormField>

        <section class="border-t border-slate-200 pt-4 dark:border-zinc-800">
          <div class="mb-3 flex flex-wrap items-start justify-between gap-3">
            <div>
              <h3 class="text-sm font-semibold">{{ t('admin.settings.basic.datetimeTitle') }}</h3>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.settings.basic.datetimeDescription') }}</p>
            </div>
            <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" @click="restoreRecommendedDateTimeSettings">
              {{ t('admin.settings.basic.restoreDateTimeDefaults') }}
            </UButton>
          </div>
          <div class="grid gap-4 md:grid-cols-2">
            <UFormField :label="t('admin.settings.basic.timezone')" name="site-timezone" class="md:col-span-2">
              <SFNativeSelect v-model="form.timezone" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base outline-none focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950">
                <option v-for="choice in timezoneChoices" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
              </SFNativeSelect>
              <template #hint>{{ t('admin.settings.basic.timezoneHint') }}</template>
            </UFormField>
            <UFormField :label="t('admin.settings.basic.dateFormat')" name="site-date-format">
              <SFNativeSelect v-model="form.dateFormat" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base outline-none focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950">
                <option v-for="choice in dateFormatChoices" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
              </SFNativeSelect>
            </UFormField>
            <UFormField :label="t('admin.settings.basic.timeFormat')" name="site-time-format">
              <SFNativeSelect v-model="form.timeFormat" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base outline-none focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950">
                <option v-for="choice in timeFormatChoices" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
              </SFNativeSelect>
            </UFormField>
            <UFormField :label="t('admin.settings.basic.startOfWeek')" name="site-start-of-week" class="md:col-span-2">
              <SFNativeSelect v-model.number="form.startOfWeek" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 text-base outline-none focus:border-[var(--sf-accent)] focus:ring-2 focus:ring-[var(--sf-accent-focus)] dark:border-zinc-700 dark:bg-zinc-950">
                <option v-for="choice in startOfWeekChoices" :key="choice.value" :value="choice.value">{{ choice.label }}</option>
              </SFNativeSelect>
              <template #hint>{{ t('admin.settings.basic.startOfWeekHint') }}</template>
            </UFormField>
          </div>
          <UAlert class="mt-4" color="neutral" variant="soft" icon="i-lucide-clock-3" :title="t('admin.settings.basic.datetimePreviewTitle')" :description="datetimePreview" />
        </section>
      </div>

      <template #footer>
        <SFAdminFormFooter :saving="section.saving.value" :show-unsaved-alert="hasChanges" :submit-text="t('admin.settings.save')" @reset="resetChanges" />
      </template>
    </UCard>
  </form>
</template>
