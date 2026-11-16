<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useSiteChromeApi, type SiteAnnouncement, type SiteAnnouncementStyle } from '~/composables/admin/useSiteChromeApi'
import { sanitizeHtml } from '~/utils/sfSanitize'

const { t, locale } = useI18n()
const toast = useToast()
const api = useSiteChromeApi()
const loading = ref(false)
const creating = ref(false)
const items = ref<SiteAnnouncement[]>([])

function defaultDraft() {
  return {
    titleZhCN: '',
    titleEnUS: '',
    bodyZhCN: '',
    bodyEnUS: '',
    style: 'info' as SiteAnnouncementStyle,
    href: '',
    dismissible: true,
    position: 0,
    enabled: true,
    startsAtLocal: '',
    endsAtLocal: ''
  }
}

const draft = reactive(defaultDraft())
const errors = reactive(defaultErrors())
const styles = computed(() => (['info', 'success', 'warning', 'danger'] as SiteAnnouncementStyle[]).map(value => ({ value, label: t(`admin.siteChrome.announcements.styles.${value}`) })))

defineExpose({ refresh: load, loading })
onMounted(load)

async function load() {
  loading.value = true
  try {
    items.value = sort(await api.listAdminAnnouncements())
  } catch (error) {
    failure(error)
  } finally {
    loading.value = false
  }
}

async function add() {
  if (!validate()) {
    return
  }

  creating.value = true
  try {
    const created = await api.createAnnouncement({
      titleZhCN: draft.titleZhCN.trim(),
      titleEnUS: draft.titleEnUS.trim(),
      bodyZhCN: draft.bodyZhCN.trim(),
      bodyEnUS: draft.bodyEnUS.trim(),
      style: draft.style,
      href: draft.href.trim(),
      dismissible: draft.dismissible,
      position: draft.position,
      enabled: draft.enabled,
      startsAt: localDateTimeToISO(draft.startsAtLocal),
      endsAt: localDateTimeToISO(draft.endsAtLocal)
    })
    items.value = sort([...items.value, created])
    Object.assign(draft, defaultDraft())
    clearErrors()
    success(t('admin.siteChrome.announcements.created'))
  } catch (error) {
    failure(error)
  } finally {
    creating.value = false
  }
}

async function toggle(item: SiteAnnouncement) {
  try {
    const updated = await api.updateAnnouncement(item.id, { enabled: !item.enabled })
    items.value = sort(items.value.map(row => row.id === updated.id ? updated : row))
    success(t('admin.siteChrome.announcements.updated'))
  } catch (error) {
    failure(error)
  }
}

async function remove(item: SiteAnnouncement) {
  try {
    await api.deleteAnnouncement(item.id)
    items.value = items.value.filter(row => row.id !== item.id)
    success(t('admin.siteChrome.announcements.deleted'))
  } catch (error) {
    failure(error)
  }
}

function validate() {
  clearErrors()
  const titleZh = draft.titleZhCN.trim()
  const titleEn = draft.titleEnUS.trim()
  const bodyZh = draft.bodyZhCN.trim()
  const bodyEn = draft.bodyEnUS.trim()

  if (!titleZh && !titleEn && !bodyZh && !bodyEn) {
    errors.content = t('admin.siteChrome.announcements.validation.contentRequired')
  }
  if (runeCount(titleZh) > 120) errors.titleZhCN = t('admin.siteChrome.announcements.validation.titleTooLong')
  if (runeCount(titleEn) > 120) errors.titleEnUS = t('admin.siteChrome.announcements.validation.titleTooLong')
  if (runeCount(bodyZh) > 2000) errors.bodyZhCN = t('admin.siteChrome.announcements.validation.bodyTooLong')
  if (runeCount(bodyEn) > 2000) errors.bodyEnUS = t('admin.siteChrome.announcements.validation.bodyTooLong')
  if (!validHref(draft.href)) errors.href = t('admin.siteChrome.announcements.validation.hrefInvalid')
  if (!Number.isInteger(draft.position) || draft.position < -100000 || draft.position > 100000) {
    errors.position = t('admin.siteChrome.announcements.validation.positionInvalid')
  }

  const startsAt = parseLocalDateTime(draft.startsAtLocal)
  const endsAt = parseLocalDateTime(draft.endsAtLocal)
  if (draft.startsAtLocal && !startsAt) errors.startsAt = t('admin.siteChrome.announcements.validation.dateInvalid')
  if (draft.endsAtLocal && !endsAt) errors.endsAt = t('admin.siteChrome.announcements.validation.dateInvalid')
  if (startsAt && endsAt && endsAt.getTime() < startsAt.getTime()) {
    errors.endsAt = t('admin.siteChrome.announcements.validation.endBeforeStart')
  }

  return Object.values(errors).every(value => !value)
}

function clearErrors() {
  Object.assign(errors, defaultErrors())
}

function defaultErrors() {
  return { content: '', titleZhCN: '', titleEnUS: '', bodyZhCN: '', bodyEnUS: '', href: '', position: '', startsAt: '', endsAt: '' }
}

function runeCount(value: string) {
  return [...value].length
}

function validHref(value: string) {
  const normalized = value.trim()
  if (!normalized) return true
  if (runeCount(normalized) > 500) return false
  if (normalized.startsWith('/') && !normalized.startsWith('//')) return true
  try {
    const parsed = new URL(normalized)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

function parseLocalDateTime(value: string) {
  if (!value) return null
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? null : parsed
}

function localDateTimeToISO(value: string) {
  return parseLocalDateTime(value)?.toISOString() || null
}

function sort(rows: SiteAnnouncement[]) {
  return [...rows].sort((a, b) => a.position - b.position || a.id - b.id)
}

function english() {
  return String(locale.value).toLowerCase().startsWith('en')
}

function title(item: SiteAnnouncement) {
  return english() ? item.titleEnUS || item.titleZhCN : item.titleZhCN || item.titleEnUS
}

function bodyHTML(item: SiteAnnouncement) {
  return english() ? item.bodyHtmlEnUS || item.bodyHtmlZhCN : item.bodyHtmlZhCN || item.bodyHtmlEnUS
}

function bodyText(item: SiteAnnouncement) {
  return english() ? item.bodyEnUS || item.bodyZhCN : item.bodyZhCN || item.bodyEnUS
}

// 列表预览是同一段后端 HTML：管理员能输入富文本，渲染时同样必须走前端二次净化
// （后端 markdown.RenderSafe 之外的第二道防线）。按 id 预先净化，避免重复解析。
const sanitizedBodyHTML = computed(() => {
  const result = new Map<number, string>()
  for (const item of items.value) {
    result.set(item.id, sanitizeHtml(bodyHTML(item)))
  }
  return result
})

function sanitizedBodyHTMLOf(item: SiteAnnouncement) {
  return sanitizedBodyHTML.value.get(item.id) || ''
}

function success(title: string) {
  toast.add({ color: 'success', icon: 'i-lucide-check', title, duration: 5000 })
}

function failure(error: unknown) {
  toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.siteChrome.announcements.saveFailed') })
}
</script>

<template>
  <form @submit.prevent="add">
    <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
      <template #header>
        <div>
          <h3 class="text-base font-bold">{{ t('admin.siteChrome.announcements.title') }}</h3>
          <p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.announcements.description') }}</p>
        </div>
      </template>

      <div class="space-y-5">
        <UAlert v-if="errors.content" color="error" variant="soft" icon="i-lucide-circle-alert" :title="errors.content" />

        <section class="space-y-4" :aria-label="t('admin.siteChrome.announcements.contentSection')">
          <div>
            <h4 class="text-sm font-semibold">{{ t('admin.siteChrome.announcements.contentSection') }}</h4>
            <p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.announcements.localeFallbackHelp') }}</p>
          </div>
          <div class="grid gap-4 lg:grid-cols-2">
            <UFormField :label="t('admin.siteChrome.announcements.titleZh')" :description="t('admin.siteChrome.announcements.titleHelp')" :error="errors.titleZhCN" name="announcement-title-zh">
              <UInput v-model="draft.titleZhCN" maxlength="120" class="w-full" :placeholder="t('admin.siteChrome.announcements.titleZhPlaceholder')" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.titleEn')" :description="t('admin.siteChrome.announcements.titleHelp')" :error="errors.titleEnUS" name="announcement-title-en">
              <UInput v-model="draft.titleEnUS" maxlength="120" class="w-full" :placeholder="t('admin.siteChrome.announcements.titleEnPlaceholder')" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.bodyZh')" :description="t('admin.siteChrome.announcements.bodyHelp')" :error="errors.bodyZhCN" name="announcement-body-zh">
              <LazySFEditor v-model="draft.bodyZhCN" preset="basic-field" :load-trusted-catalog="false" :rows="4" :max-characters="2000" :disabled="creating" :error="errors.bodyZhCN || undefined" :aria-label="t('admin.siteChrome.announcements.bodyZh')" :placeholder="t('admin.siteChrome.announcements.bodyZhPlaceholder')" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.bodyEn')" :description="t('admin.siteChrome.announcements.bodyHelp')" :error="errors.bodyEnUS" name="announcement-body-en">
              <LazySFEditor v-model="draft.bodyEnUS" preset="basic-field" :load-trusted-catalog="false" :rows="4" :max-characters="2000" :disabled="creating" :error="errors.bodyEnUS || undefined" :aria-label="t('admin.siteChrome.announcements.bodyEn')" :placeholder="t('admin.siteChrome.announcements.bodyEnPlaceholder')" />
            </UFormField>
          </div>
        </section>

        <section class="space-y-4 border-t border-slate-200 pt-5 dark:border-zinc-800" :aria-label="t('admin.siteChrome.announcements.deliverySection')">
          <div>
            <h4 class="text-sm font-semibold">{{ t('admin.siteChrome.announcements.deliverySection') }}</h4>
            <p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.announcements.deliveryHelp') }}</p>
          </div>
          <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
            <UFormField :label="t('admin.siteChrome.announcements.styleLabel')" :description="t('admin.siteChrome.announcements.styleHelp')" name="announcement-style">
              <USelect v-model="draft.style" :items="styles" value-key="value" label-key="label" class="w-full" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.hrefLabel')" :description="t('admin.siteChrome.announcements.hrefHelp')" :error="errors.href" name="announcement-href">
              <UInput v-model="draft.href" icon="i-lucide-link" maxlength="500" class="w-full" :placeholder="t('admin.siteChrome.announcements.hrefPlaceholder')" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.position')" :description="t('admin.siteChrome.announcements.positionHelp')" :error="errors.position" name="announcement-position">
              <UInput v-model.number="draft.position" type="number" :min="-100000" :max="100000" class="w-full" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.startsAt')" :description="t('admin.siteChrome.announcements.startsAtHelp')" :error="errors.startsAt" name="announcement-starts-at">
              <UInput v-model="draft.startsAtLocal" type="datetime-local" class="w-full" :disabled="creating" />
            </UFormField>
            <UFormField :label="t('admin.siteChrome.announcements.endsAt')" :description="t('admin.siteChrome.announcements.endsAtHelp')" :error="errors.endsAt" name="announcement-ends-at">
              <UInput v-model="draft.endsAtLocal" type="datetime-local" class="w-full" :disabled="creating" />
            </UFormField>
          </div>

          <div class="grid gap-3 md:grid-cols-2">
            <div class="flex items-center justify-between gap-4 rounded-md border border-slate-200 p-3 dark:border-zinc-800">
              <div><p class="text-sm font-medium">{{ t('admin.siteChrome.announcements.dismissible') }}</p><p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.announcements.dismissibleHelp') }}</p></div>
              <USwitch v-model="draft.dismissible" :disabled="creating" :aria-label="t('admin.siteChrome.announcements.dismissible')" />
            </div>
            <div class="flex items-center justify-between gap-4 rounded-md border border-slate-200 p-3 dark:border-zinc-800">
              <div><p class="text-sm font-medium">{{ t('admin.siteChrome.announcements.enabledOnCreate') }}</p><p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.announcements.enabledOnCreateHelp') }}</p></div>
              <USwitch v-model="draft.enabled" :disabled="creating" :aria-label="t('admin.siteChrome.announcements.enabledOnCreate')" />
            </div>
          </div>
        </section>

        <div class="flex justify-end border-t border-slate-200 pt-4 dark:border-zinc-800">
          <UButton type="submit" leading-icon="i-lucide-plus" :loading="creating" :disabled="creating">{{ t('admin.siteChrome.announcements.add') }}</UButton>
        </div>

        <div v-if="loading" class="py-8 text-center text-sm text-muted">{{ t('admin.common.loading') }}</div>
        <div v-else-if="items.length === 0" class="py-8 text-center text-sm text-muted">{{ t('admin.siteChrome.announcements.empty') }}</div>
        <ul v-else class="divide-y divide-slate-200 border-t border-slate-200 dark:divide-zinc-800 dark:border-zinc-800">
          <li v-for="item in items" :key="item.id" class="flex items-start justify-between gap-3 py-4">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2"><strong>{{ title(item) }}</strong><UBadge color="neutral" variant="soft">{{ t(`admin.siteChrome.announcements.styles.${item.style}`) }}</UBadge></div>
              <div v-if="sanitizedBodyHTMLOf(item)" class="sf-announcement-admin-preview mt-1 text-sm text-muted" v-html="sanitizedBodyHTMLOf(item)" />
              <p v-else-if="bodyText(item)" class="mt-1 text-sm text-muted">{{ bodyText(item) }}</p>
            </div>
            <div class="flex shrink-0 gap-2"><UButton type="button" size="sm" color="neutral" variant="outline" @click="toggle(item)">{{ item.enabled ? t('admin.siteChrome.disable') : t('admin.siteChrome.enable') }}</UButton><UButton type="button" size="sm" color="error" variant="soft" icon="i-lucide-trash-2" :aria-label="t('admin.siteChrome.delete')" :title="t('admin.siteChrome.delete')" @click="remove(item)" /></div>
          </li>
        </ul>
      </div>
    </UCard>
  </form>
</template>

<style scoped>
.sf-announcement-admin-preview :deep(p) {
  margin: 0.2rem 0 0;
}

.sf-announcement-admin-preview :deep(ul),
.sf-announcement-admin-preview :deep(ol) {
  margin: 0.35rem 0 0;
  padding-left: 1.25rem;
}

.sf-announcement-admin-preview :deep(a) {
  color: var(--sf-accent);
  text-decoration: underline;
  text-underline-offset: 0.15em;
}
</style>
