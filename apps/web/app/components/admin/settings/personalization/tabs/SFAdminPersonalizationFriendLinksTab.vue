<script setup lang="ts">
import { apiErrorMessage } from '~/composables/useApiClient'
import { useSiteChromeApi, type SiteFriendLink } from '~/composables/admin/useSiteChromeApi'

const { t } = useI18n()
const toast = useToast()
const api = useSiteChromeApi()
const loading = ref(false)
const items = ref<SiteFriendLink[]>([])
const draft = reactive({ name: '', url: 'https://', description: '', logoUrl: '', position: 0, enabled: true })
defineExpose({ refresh: load, loading })
onMounted(load)

async function load() {
  loading.value = true
  try { items.value = sort(await api.listAdminFriendLinks()) } catch (error) { failure(error) } finally { loading.value = false }
}
async function add() {
  try {
    const created = await api.createFriendLink({ ...draft, name: draft.name.trim(), url: draft.url.trim(), description: draft.description.trim(), logoUrl: draft.logoUrl.trim() })
    items.value = sort([...items.value, created])
    Object.assign(draft, { name: '', url: 'https://', description: '', logoUrl: '' })
    success(t('admin.siteChrome.friendLinks.created'))
  } catch (error) { failure(error) }
}
async function toggle(item: SiteFriendLink) {
  try { const updated = await api.updateFriendLink(item.id, { enabled: !item.enabled }); items.value = sort(items.value.map(row => row.id === updated.id ? updated : row)); success(t('admin.siteChrome.friendLinks.updated')) } catch (error) { failure(error) }
}
async function remove(item: SiteFriendLink) {
  try { await api.deleteFriendLink(item.id); items.value = items.value.filter(row => row.id !== item.id); success(t('admin.siteChrome.friendLinks.deleted')) } catch (error) { failure(error) }
}
function sort(rows: SiteFriendLink[]) { return [...rows].sort((a, b) => a.position - b.position || a.id - b.id) }
function success(title: string) { toast.add({ color: 'success', icon: 'i-lucide-check', title, duration: 5000 }) }
function failure(error: unknown) { toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t('admin.siteChrome.friendLinks.saveFailed') }) }
</script>

<template>
  <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <template #header><div><h3 class="text-base font-bold">{{ t('admin.siteChrome.friendLinks.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.siteChrome.friendLinks.description') }}</p></div></template>
    <div class="mb-4 grid gap-3 rounded-md border border-slate-200 bg-slate-50 p-3 dark:border-zinc-800 dark:bg-zinc-950/60 lg:grid-cols-[1fr_1.2fr_1fr_auto]">
      <UInput v-model="draft.name" :placeholder="t('admin.siteChrome.friendLinks.name')" /><UInput v-model="draft.url" icon="i-lucide-link" /><UInput v-model="draft.description" :placeholder="t('admin.siteChrome.friendLinks.descriptionField')" /><UButton leading-icon="i-lucide-plus" @click="add">{{ t('admin.siteChrome.friendLinks.add') }}</UButton>
    </div>
    <div v-if="loading" class="py-8 text-center text-sm text-muted">{{ t('admin.common.loading') }}</div>
    <div v-else-if="items.length === 0" class="py-8 text-center text-sm text-muted">{{ t('admin.siteChrome.friendLinks.empty') }}</div>
    <ul v-else class="divide-y divide-slate-200 dark:divide-zinc-800">
      <li v-for="item in items" :key="item.id" class="flex items-center justify-between gap-3 py-3">
        <div class="min-w-0"><strong>{{ item.name }}</strong><p class="truncate font-mono text-xs text-muted">{{ item.url }}</p></div>
        <div class="flex gap-2"><UButton size="sm" color="neutral" variant="outline" @click="toggle(item)">{{ item.enabled ? t('admin.siteChrome.disable') : t('admin.siteChrome.enable') }}</UButton><UButton size="sm" color="error" variant="soft" icon="i-lucide-trash-2" :aria-label="t('admin.siteChrome.delete')" @click="remove(item)" /></div>
      </li>
    </ul>
  </UCard>
</template>
