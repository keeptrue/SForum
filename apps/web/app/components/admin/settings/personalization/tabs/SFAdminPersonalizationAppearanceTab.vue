<script setup lang="ts">
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { useAdminAppearancePreview } from '~/composables/admin/settings/useAdminAppearancePreview'
import {
  appearanceThemes,
  buildCustomAppearanceThemeValue,
  cloneFooterLink,
  cloneFooterLinks,
  customColorFromAppearanceTheme,
  defaultCustomThemeColor,
  lightBackgroundPalettes,
  lightBackgroundPresets,
  normalizeAppearanceThemeValue,
  normalizeHexColor,
  normalizeLightBackground,
  recommendedAppearanceTheme,
  recommendedLightBackground,
  recommendedFooterCopyright,
  recommendedFooterLinks,
  resolveAppearanceTheme,
  type AdminWebOption,
  type AppearanceThemePreset,
  type FooterLinkKey,
  type FooterLinkOption,
  type LightBackgroundPreset
} from '~/composables/useWebOptions'
import { useAdminOptionForm } from '~/composables/admin/settings/useAdminOptionForm'

type ThemePreview = { accent: string, hover: string, soft: string }
type AppearanceForm = {
  themeMode: AppearanceThemePreset | 'custom'
  customColor: string
  lightBackground: LightBackgroundPreset
  copyrightZh: string
  copyrightEn: string
  footerLinks: FooterLinkOption[]
}
const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const appearancePreview = useAdminAppearancePreview()
const previews: Record<AppearanceThemePreset, ThemePreview> = {
  pine_teal: { accent: '#0f766e', hover: '#0b5f59', soft: '#e6f4f1' },
  ocean_blue: { accent: '#2563eb', hover: '#1d4ed8', soft: '#eff6ff' },
  violet: { accent: '#7c3aed', hover: '#6d28d9', soft: '#f3e8ff' },
  rose: { accent: '#e11d48', hover: '#be123c', soft: '#fff1f2' },
  amber: { accent: '#d97706', hover: '#b45309', soft: '#fffbeb' }
}
const state = useAdminOptionForm<AppearanceForm>(
  toRef(props, 'items'),
  map => {
    const theme = normalizeAppearanceThemeValue(map['appearance.theme']?.value)
    const customColor = customColorFromAppearanceTheme(theme)
    return {
      themeMode: customColor ? 'custom' : theme as AppearanceThemePreset,
      customColor: customColor || defaultCustomThemeColor,
      lightBackground: normalizeLightBackground(map['appearance.light_background']?.value),
      copyrightZh: map['footer.copyright.zh-CN']?.value ?? recommendedFooterCopyright['zh-CN'],
      copyrightEn: map['footer.copyright.en-US']?.value ?? recommendedFooterCopyright['en-US'],
      footerLinks: parseFooterLinks(map['footer.links']?.value)
    }
  },
  value => {
    const color = normalizeHexColor(value.customColor) || defaultCustomThemeColor
    return [
      { name: 'appearance.theme', value: value.themeMode === 'custom' ? buildCustomAppearanceThemeValue(color) : value.themeMode },
      { name: 'appearance.light_background', value: value.lightBackground },
      { name: 'footer.copyright.zh-CN', value: value.copyrightZh },
      { name: 'footer.copyright.en-US', value: value.copyrightEn },
      { name: 'footer.links', value: JSON.stringify(value.footerLinks.map(link => ({ key: link.key, labels: { 'zh-CN': link.labels['zh-CN'].trim(), 'en-US': link.labels['en-US'].trim() }, url: link.url.trim() }))) }
    ]
  },
  recommended,
  items => emit('saved', items),
  { saved: t('admin.personalization.saved'), saveFailed: t('admin.personalization.saveFailed'), reset: t('admin.personalization.resetChanges'), restored: t('admin.personalization.restoreRecommended') }
)
const choices = computed(() => appearanceThemes.map(value => ({ value, label: t(`admin.personalization.themes.${value}.label`), description: t(`admin.personalization.themes.${value}.description`), preview: previews[value] })))
const backgroundChoices = computed(() => lightBackgroundPresets.map(value => ({
  value,
  label: t(`admin.personalization.lightBackground.presets.${value}.label`),
  description: t(`admin.personalization.lightBackground.presets.${value}.description`),
  preview: lightBackgroundPalettes[value]
})))
const customPreview = computed(() => {
  const vars = resolveAppearanceTheme(buildCustomAppearanceThemeValue(state.form.customColor)).cssVars
  return { accent: vars['--sf-accent'] || defaultCustomThemeColor, soft: vars['--sf-accent-soft'] || '#eff6ff' }
})
const previewTheme = computed(() => state.form.themeMode === 'custom'
  ? buildCustomAppearanceThemeValue(state.form.customColor)
  : state.form.themeMode)
let previewActive = false

function syncAppearancePreview() {
  if (!previewActive) return
  appearancePreview.show({
    theme: previewTheme.value,
    lightBackground: state.form.lightBackground
  })
}

watch([previewTheme, () => state.form.lightBackground], syncAppearancePreview, { flush: 'sync' })
onMounted(() => {
  previewActive = true
  syncAppearancePreview()
})
onActivated(() => {
  previewActive = true
  syncAppearancePreview()
})
onDeactivated(() => {
  previewActive = false
  appearancePreview.clear()
})
onBeforeUnmount(() => {
  previewActive = false
  appearancePreview.clear()
})

function recommended(): AppearanceForm {
  return { themeMode: recommendedAppearanceTheme, customColor: defaultCustomThemeColor, lightBackground: recommendedLightBackground, copyrightZh: recommendedFooterCopyright['zh-CN'], copyrightEn: recommendedFooterCopyright['en-US'], footerLinks: cloneFooterLinks(recommendedFooterLinks) }
}
function parseFooterLinks(value: string | undefined) {
  if (!value) return cloneFooterLinks(recommendedFooterLinks)
  try {
    const parsed = JSON.parse(value)
    if (!Array.isArray(parsed)) return cloneFooterLinks(recommendedFooterLinks)
    const byKey = new Map<FooterLinkKey, FooterLinkOption>()
    for (const item of parsed) {
      if (!item || !isKey(item.key) || !item.labels) continue
      const zh = String(item.labels['zh-CN'] || '').trim()
      const en = String(item.labels['en-US'] || '').trim()
      if (zh && en) byKey.set(item.key, { key: item.key, labels: { 'zh-CN': zh, 'en-US': en }, url: String(item.url || '').trim() })
    }
    return recommendedFooterLinks.map(link => byKey.get(link.key) || cloneFooterLink(link))
  } catch { return cloneFooterLinks(recommendedFooterLinks) }
}
function isKey(value: unknown): value is FooterLinkKey { return value === 'terms' || value === 'privacy' || value === 'guidelines' }
function normalizeColor() { state.form.customColor = normalizeHexColor(state.form.customColor) || defaultCustomThemeColor }
function selectTheme(value: AppearanceThemePreset) { state.form.themeMode = value }
function selectLightBackground(value: LightBackgroundPreset) { state.form.lightBackground = value }
</script>

<template>
  <form class="flex flex-col gap-5" @submit.prevent="state.save">
    <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <template #header><div><h2 class="text-base font-bold">{{ t('admin.personalization.theme.title') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.personalization.theme.description') }}</p></div></template>
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3" role="radiogroup" :aria-label="t('admin.personalization.theme.title')">
        <button v-for="choice in choices" :key="choice.value" type="button" role="radio" :aria-checked="state.form.themeMode === choice.value" class="flex min-h-32 cursor-pointer flex-col justify-between rounded-md border p-4 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]" :class="state.form.themeMode === choice.value ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 dark:border-zinc-800'" @click="selectTheme(choice.value)">
          <span class="flex justify-between gap-3"><strong>{{ choice.label }}</strong><span class="flex gap-1"><i class="size-4 rounded-full" :style="{ background: choice.preview.soft }" /><i class="size-4 rounded-full" :style="{ background: choice.preview.accent }" /></span></span><span class="text-xs text-muted">{{ choice.description }}</span>
        </button>
        <div class="flex min-h-32 cursor-pointer flex-col justify-between rounded-md border p-4 transition-colors" :class="state.form.themeMode === 'custom' ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 dark:border-zinc-800'" @click="state.form.themeMode = 'custom'">
          <span class="flex justify-between gap-3"><strong>{{ t('admin.personalization.themes.custom.label') }}</strong><i class="size-4 rounded-full" :style="{ background: customPreview.accent }" /></span>
          <div class="flex gap-2" @click.stop><input v-model="state.form.customColor" type="color" class="h-10 w-12" @input="state.form.themeMode = 'custom'"><UInput v-model="state.form.customColor" class="w-full font-mono" @focus="state.form.themeMode = 'custom'" @blur="normalizeColor" /></div>
        </div>
      </div>
      <USeparator class="my-6" />
      <div class="mb-3"><h3 class="text-sm font-bold">{{ t('admin.personalization.lightBackground.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.personalization.lightBackground.description') }}</p></div>
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4" role="radiogroup" :aria-label="t('admin.personalization.lightBackground.title')">
        <button v-for="choice in backgroundChoices" :key="choice.value" type="button" role="radio" :aria-checked="state.form.lightBackground === choice.value" class="flex min-h-40 cursor-pointer flex-col rounded-md border p-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]" :class="state.form.lightBackground === choice.value ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 dark:border-zinc-800'" @click="selectLightBackground(choice.value)">
          <span class="mb-3 block h-16 overflow-hidden rounded border" :style="{ background: choice.preview.background, borderColor: choice.preview.border }">
            <span class="grid h-full grid-cols-[0.42fr_1fr] gap-1.5 p-2">
              <i class="rounded-sm" :style="{ background: choice.preview.muted }" />
              <i class="rounded-sm border" :style="{ background: choice.preview.surface, borderColor: choice.preview.border }" />
            </span>
          </span>
          <strong class="text-sm">{{ choice.label }}</strong>
          <span class="mt-1 text-xs text-muted">{{ choice.description }}</span>
        </button>
      </div>
    </UCard>
    <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 border-t p-4' }">
      <template #header><div><h2 class="text-base font-bold">{{ t('admin.personalization.footer.title') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.personalization.footer.description') }}</p></div></template>
      <div class="grid max-w-5xl gap-5">
        <div class="grid gap-4 lg:grid-cols-2"><UFormField :label="t('admin.personalization.footer.copyrightZh')"><UTextarea v-model="state.form.copyrightZh" :rows="3" class="w-full" /></UFormField><UFormField :label="t('admin.personalization.footer.copyrightEn')"><UTextarea v-model="state.form.copyrightEn" :rows="3" class="w-full" /></UFormField></div>
        <div v-for="(link, linkIndex) in state.form.footerLinks" :key="link.key" class="grid gap-3 rounded-md border border-slate-200 p-3 dark:border-zinc-800 lg:grid-cols-[10rem_1fr_1fr_1.2fr]"><strong>{{ t(`admin.personalization.footer.linkKeys.${link.key}`) }}</strong><UInput v-model="link.labels['zh-CN']" :aria-label="t('admin.personalization.footer.linkFieldZh', { index: linkIndex + 1, key: t(`admin.personalization.footer.linkKeys.${link.key}`) })" /><UInput v-model="link.labels['en-US']" :aria-label="t('admin.personalization.footer.linkFieldEn', { index: linkIndex + 1, key: t(`admin.personalization.footer.linkKeys.${link.key}`) })" /><UInput v-model="link.url" icon="i-lucide-link" :aria-label="t('admin.personalization.footer.linkFieldUrl', { index: linkIndex + 1, key: t(`admin.personalization.footer.linkKeys.${link.key}`) })" /></div>
      </div>
      <template #footer><SFAdminFormFooter :saving="state.saving.value" :show-unsaved-alert="state.hasChanges.value" :submit-text="t('admin.personalization.save')" :reset-text="t('admin.personalization.restoreRecommended')" @reset="state.restoreRecommended" /></template>
    </UCard>
  </form>
</template>
