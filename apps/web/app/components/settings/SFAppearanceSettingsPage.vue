<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useUserAppearancePreference } from '~/composables/appearance/useUserAppearancePreference'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import SFSettingsShell from '~/components/settings/SFSettingsShell.vue'
import { radioGroupNextIndex, radioGroupTabIndex } from '~/utils/a11y/rovingRadioGroup'
import {
  appearanceThemes,
  buildCustomAppearanceThemeValue,
  customColorFromAppearanceTheme,
  defaultCustomThemeColor,
  lightBackgroundPalettes,
  lightBackgroundPresets,
  normalizeHexColor,
  resolveAppearanceTheme,
  type AppearanceTheme,
  type AppearanceThemePreset,
  type LightBackgroundPreset
} from '~/utils/settings/appearance'

type PreferenceMode = 'site' | 'custom'
type ThemeOptionValue = AppearanceThemePreset | 'custom'
type ThemePreview = { accent: string, soft: string }

const { t } = useI18n()
const toast = useToast()
const { appearanceTheme: siteTheme, lightBackground: siteLightBackground } = useWebOptions()
const appearance = useUserAppearancePreference()

useSForumSeo({
  title: () => t('userAppearanceSettings.metaTitle'),
  description: () => t('userAppearanceSettings.metaDescription'),
  type: 'website',
  noindex: true
})

const previews: Record<AppearanceThemePreset, ThemePreview> = {
  pine_teal: { accent: '#0f766e', soft: '#e6f4f1' },
  ocean_blue: { accent: '#2563eb', soft: '#eff6ff' },
  violet: { accent: '#7c3aed', soft: '#f3e8ff' },
  rose: { accent: '#e11d48', soft: '#fff1f2' },
  amber: { accent: '#d97706', soft: '#fffbeb' }
}
const sourceChoices: PreferenceMode[] = ['site', 'custom']
/** 「自定义配色」是组内第 6 个 radio：卡片本体是控件，取色器在组外（避免 nested-interactive）。 */
const themeOptionValues: ThemeOptionValue[] = [...appearanceThemes, 'custom']

const mode = ref<PreferenceMode>('site')
const themeMode = ref<ThemeOptionValue>('pine_teal')
const customColor = ref(defaultCustomThemeColor)
const lightBackground = ref<LightBackgroundPreset>('pure_white')
const saving = ref(false)
const actionError = ref('')
let previewActive = false

const sourceGroup = ref<HTMLElement | null>(null)
const themeGroup = ref<HTMLElement | null>(null)
const backgroundGroup = ref<HTMLElement | null>(null)

const themeChoices = computed(() => appearanceThemes.map(value => ({
  value,
  label: t(`admin.personalization.themes.${value}.label`),
  description: t(`admin.personalization.themes.${value}.description`),
  preview: previews[value]
})))
const backgroundChoices = computed(() => lightBackgroundPresets.map(value => ({
  value,
  label: t(`admin.personalization.lightBackground.presets.${value}.label`),
  description: t(`admin.personalization.lightBackground.presets.${value}.description`),
  preview: lightBackgroundPalettes[value]
})))
const draftTheme = computed<AppearanceTheme>(() => themeMode.value === 'custom'
  ? buildCustomAppearanceThemeValue(customColor.value)
  : themeMode.value)
const effectiveDraft = computed(() => mode.value === 'site'
  ? { theme: siteTheme.value, lightBackground: siteLightBackground.value }
  : { theme: draftTheme.value, lightBackground: lightBackground.value })
const customPreview = computed(() => resolveAppearanceTheme(buildCustomAppearanceThemeValue(customColor.value)).cssVars)
const hasChanges = computed(() => {
  const saved = appearance.saved.value
  if (mode.value === 'site') return saved !== null
  return !saved || saved.theme !== draftTheme.value || saved.lightBackground !== lightBackground.value
})
const sourceLabel = computed(() => appearance.saved.value
  ? t('userAppearanceSettings.status.personal')
  : t('userAppearanceSettings.status.site'))

function focusOption<T extends string>(group: HTMLElement | null, values: readonly T[], value: T) {
  const index = values.indexOf(value)
  if (index < 0) return
  group?.querySelectorAll<HTMLElement>('[data-radio-option]')[index]?.focus()
}

/** radiogroup 的方向键/Home/End：选中项跟着焦点走，与原生 radio 行为一致。 */
function handleRadioKeydown<T extends string>(
  group: HTMLElement | null,
  values: readonly T[],
  value: T,
  event: KeyboardEvent,
  select: (value: T) => void
) {
  const next = radioGroupNextIndex(values.length, values.indexOf(value), event.key)
  if (next === null) return
  event.preventDefault()
  const nextValue = values[next]
  if (nextValue === undefined) return
  select(nextValue)
  focusOption(group, values, nextValue)
}

function loadSavedPreference() {
  const saved = appearance.saved.value
  mode.value = saved ? 'custom' : 'site'
  const theme = saved?.theme || siteTheme.value
  const savedCustomColor = customColorFromAppearanceTheme(theme)
  themeMode.value = savedCustomColor ? 'custom' : theme as AppearanceThemePreset
  customColor.value = savedCustomColor || defaultCustomThemeColor
  lightBackground.value = saved?.lightBackground || siteLightBackground.value
  actionError.value = ''
}

function syncPreview() {
  if (previewActive) appearance.showPreview(effectiveDraft.value)
}

function selectTheme(value: ThemeOptionValue) {
  themeMode.value = value
}

function selectBackground(value: LightBackgroundPreset) {
  lightBackground.value = value
}

function selectMode(value: PreferenceMode) {
  mode.value = value
  if (value === 'custom' && !appearance.saved.value) {
    const siteCustomColor = customColorFromAppearanceTheme(siteTheme.value)
    themeMode.value = siteCustomColor ? 'custom' : siteTheme.value as AppearanceThemePreset
    customColor.value = siteCustomColor || defaultCustomThemeColor
    lightBackground.value = siteLightBackground.value
  }
}

function normalizeCustomColor() {
  customColor.value = normalizeHexColor(customColor.value) || defaultCustomThemeColor
}

async function save() {
  if (!hasChanges.value || saving.value) return
  saving.value = true
  actionError.value = ''
  try {
    await appearance.save(mode.value === 'site' ? null : effectiveDraft.value)
    loadSavedPreference()
    syncPreview()
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: mode.value === 'site' ? t('userAppearanceSettings.siteDefaultsSaved') : t('userAppearanceSettings.saved'),
      duration: 5000
    })
  } catch (error) {
    actionError.value = apiErrorMessage(error) || t('userAppearanceSettings.saveFailed')
  } finally {
    saving.value = false
  }
}

function resetDraft() {
  loadSavedPreference()
  syncPreview()
  toast.add({ color: 'neutral', icon: 'i-lucide-undo-2', title: t('userAppearanceSettings.reset'), duration: 5000 })
}

watch([mode, draftTheme, lightBackground, siteTheme, siteLightBackground], syncPreview, { flush: 'sync' })
onMounted(() => {
  loadSavedPreference()
  previewActive = true
  syncPreview()
})
onActivated(() => {
  previewActive = true
  syncPreview()
})
onDeactivated(() => {
  previewActive = false
  appearance.clearPreview()
})
onBeforeUnmount(() => {
  previewActive = false
  appearance.clearPreview()
})
</script>

<template>
  <SFSettingsShell
    class="sforum-settings-appearance"
    data-sforum-island-body="identity.component.appearance_settings"
    active="appearance"
    title-id="appearance-settings-title"
    :title="t('userAppearanceSettings.title')"
    :description="t('userAppearanceSettings.intro')"
    :rail-label="t('userAppearanceSettings.rail.ariaLabel')"
  >
    <template #head-actions>
      <SFButton
        variant="secondary"
        size="sm"
        :disabled="!hasChanges || saving"
        :aria-label="t('userAppearanceSettings.resetChanges')"
        :title="t('userAppearanceSettings.resetChanges')"
        @click="resetDraft"
      >
        <UIcon name="i-lucide-rotate-ccw" class="mr-1 size-4" aria-hidden="true" />
        <span class="hidden sm:inline">{{ t('userAppearanceSettings.resetChanges') }}</span>
      </SFButton>
    </template>

    <UAlert
      v-if="actionError"
      class="mt-4"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="actionError"
    />

    <section class="mt-5 border-b border-slate-200 pb-5 dark:border-zinc-800">
      <div class="mb-3">
        <h2 class="text-base font-semibold">{{ t('userAppearanceSettings.source.title') }}</h2>
        <p class="mt-1 text-sm text-muted">{{ t('userAppearanceSettings.source.description') }}</p>
      </div>
      <div ref="sourceGroup" class="grid gap-2 sm:grid-cols-2" role="radiogroup" :aria-label="t('userAppearanceSettings.source.title')">
        <button
          v-for="value in sourceChoices"
          :key="value"
          type="button"
          role="radio"
          data-radio-option
          :aria-checked="mode === value"
          :tabindex="radioGroupTabIndex(sourceChoices, mode, value)"
          class="flex min-h-20 items-start gap-3 rounded-md border p-3 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]"
          :class="mode === value ? 'border-[var(--sf-accent)] bg-[var(--sf-accent-soft)]' : 'border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900'"
          @click="selectMode(value)"
          @keydown="handleRadioKeydown(sourceGroup, sourceChoices, value, $event, selectMode)"
        >
          <UIcon :name="value === 'site' ? 'i-lucide-building-2' : 'i-lucide-palette'" class="mt-0.5 size-5 shrink-0" aria-hidden="true" />
          <span><strong class="block text-sm">{{ t(`userAppearanceSettings.source.${value}.label`) }}</strong><span class="mt-1 block text-xs text-muted">{{ t(`userAppearanceSettings.source.${value}.description`) }}</span></span>
        </button>
      </div>
    </section>

    <fieldset class="mt-5" :disabled="mode === 'site'">
      <legend class="text-base font-semibold">{{ t('admin.personalization.theme.title') }}</legend>
      <p class="mt-1 text-sm text-muted">{{ t('userAppearanceSettings.themeDescription') }}</p>
      <div ref="themeGroup" class="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3" role="radiogroup" :aria-label="t('admin.personalization.theme.title')">
        <button
          v-for="choice in themeChoices"
          :key="choice.value"
          type="button"
          role="radio"
          data-radio-option
          :aria-checked="themeMode === choice.value"
          :tabindex="radioGroupTabIndex(themeOptionValues, themeMode, choice.value)"
          class="flex min-h-28 flex-col justify-between rounded-md border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-55 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]"
          :class="themeMode === choice.value ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900'"
          @click="themeMode = choice.value"
          @keydown="handleRadioKeydown(themeGroup, themeOptionValues, choice.value, $event, selectTheme)"
        >
          <span class="flex w-full justify-between gap-3"><strong class="text-sm">{{ choice.label }}</strong><span class="flex gap-1"><i class="size-4 rounded-full" :style="{ background: choice.preview.soft }" /><i class="size-4 rounded-full" :style="{ background: choice.preview.accent }" /></span></span>
          <span class="mt-3 text-xs text-muted">{{ choice.description }}</span>
        </button>
        <button
          type="button"
          role="radio"
          data-radio-option
          data-theme-option="custom"
          :aria-checked="themeMode === 'custom'"
          :tabindex="radioGroupTabIndex(themeOptionValues, themeMode, 'custom')"
          class="flex min-h-28 flex-col justify-between rounded-md border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-55 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]"
          :class="themeMode === 'custom' ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900'"
          @click="themeMode = 'custom'"
          @keydown="handleRadioKeydown(themeGroup, themeOptionValues, 'custom', $event, selectTheme)"
        >
          <span class="flex w-full justify-between gap-3"><strong class="text-sm">{{ t('admin.personalization.themes.custom.label') }}</strong><i class="size-4 rounded-full" :style="{ background: customPreview['--sf-accent'] }" /></span>
          <span class="mt-3 font-mono text-xs text-muted">{{ customColor }}</span>
        </button>
      </div>

      <div v-if="themeMode === 'custom'" class="mt-3 flex max-w-md items-center gap-2" data-custom-theme-controls>
        <input v-model="customColor" type="color" class="h-10 w-12 shrink-0" :disabled="mode === 'site'" :aria-label="t('userAppearanceSettings.customColor')">
        <UInput v-model="customColor" class="min-w-0 flex-1 font-mono" :disabled="mode === 'site'" :aria-label="t('userAppearanceSettings.customColor')" @blur="normalizeCustomColor" />
      </div>

      <div class="mt-6 border-t border-slate-200 pt-5 dark:border-zinc-800">
        <h2 class="text-base font-semibold">{{ t('admin.personalization.lightBackground.title') }}</h2>
        <p class="mt-1 text-sm text-muted">{{ t('userAppearanceSettings.backgroundDescription') }}</p>
        <div ref="backgroundGroup" class="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3" role="radiogroup" :aria-label="t('admin.personalization.lightBackground.title')">
          <button
            v-for="choice in backgroundChoices"
            :key="choice.value"
            type="button"
            role="radio"
            data-radio-option
            :aria-checked="lightBackground === choice.value"
            :tabindex="radioGroupTabIndex(lightBackgroundPresets, lightBackground, choice.value)"
            class="flex min-h-36 flex-col rounded-md border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-55 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent-focus)]"
            :class="lightBackground === choice.value ? 'border-[var(--sf-accent)] ring-1 ring-[var(--sf-accent)]' : 'border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900'"
            @click="lightBackground = choice.value"
            @keydown="handleRadioKeydown(backgroundGroup, lightBackgroundPresets, choice.value, $event, selectBackground)"
          >
            <span class="mb-3 block h-14 overflow-hidden rounded border" :style="{ background: choice.preview.background, borderColor: choice.preview.border }"><span class="grid h-full grid-cols-[0.42fr_1fr] gap-1.5 p-2"><i class="rounded-sm" :style="{ background: choice.preview.muted }" /><i class="rounded-sm border" :style="{ background: choice.preview.surface, borderColor: choice.preview.border }" /></span></span>
            <strong class="text-sm">{{ choice.label }}</strong>
            <span class="mt-1 text-xs text-muted">{{ choice.description }}</span>
          </button>
        </div>
      </div>
    </fieldset>

    <div class="mt-6 flex flex-col gap-3 border-t border-slate-200 py-5 dark:border-zinc-800 sm:flex-row sm:items-center sm:justify-between">
      <p class="text-xs text-muted">{{ hasChanges ? t('userAppearanceSettings.unsaved') : t('userAppearanceSettings.savedState') }}</p>
      <SFButton :loading="saving" :disabled="!hasChanges" @click="save">
        <UIcon name="i-lucide-save" class="mr-1 size-4" aria-hidden="true" />
        {{ t('userAppearanceSettings.save') }}
      </SFButton>
    </div>

    <template #rail>
      <section class="space-y-3">
        <div class="flex items-center gap-2">
          <UIcon name="i-lucide-palette" class="size-5 text-[var(--sf-accent)]" aria-hidden="true" />
          <h2 class="text-sm font-semibold">{{ t('userAppearanceSettings.rail.title') }}</h2>
        </div>
        <p class="text-sm text-muted">{{ t('userAppearanceSettings.rail.description') }}</p>
        <div class="rounded-md border border-slate-200 bg-white p-3 text-sm dark:border-zinc-800 dark:bg-zinc-900">
          <span class="text-xs text-muted">{{ t('userAppearanceSettings.rail.currentSource') }}</span>
          <strong class="mt-1 block">{{ sourceLabel }}</strong>
        </div>
      </section>
    </template>
  </SFSettingsShell>
</template>
