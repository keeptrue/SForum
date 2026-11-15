<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { apiErrorMessage } from '~/composables/useApiClient'
import { createAdminForumApi } from '~/utils/admin/adminForum'
import type { SearchProviderItem, SearchProvidersState } from '~/utils/admin/adminForum'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const forumApi = createAdminForumApi(request)
const adminRoutes = useAdminRoutes()
const { can } = usePermissions()
const canManage = computed(() => can('search.manage'))
const loading = ref(false)
const saving = ref(false)
const reindexing = ref(false)
const loadError = ref('')
const providers = ref<SearchProviderItem[]>([])
const selected = ref('')
const pinned = ref(false)
const resolved = ref<SearchProviderItem | null>(null)
const providerItems = computed(() => providers.value.map(item => ({
  label: `${item.label}${item.healthy ? '' : ` (${t('admin.forum.settings.search.unhealthy')})`}${item.isDefault ? ` · ${t('admin.forum.settings.search.defaultBadge')}` : ''}`,
  value: item.extensionId
})))
const dirty = computed(() => Boolean(selected.value) && selected.value !== (resolved.value?.extensionId || ''))

onMounted(load)

function applyState(state: SearchProvidersState) {
  providers.value = state.items || []
  resolved.value = state.selected || null
  selected.value = state.selected?.extensionId || state.defaultExtensionId || ''
  pinned.value = Boolean(state.pinned)
}

async function load() {
  if (!canManage.value) return
  loading.value = true
  loadError.value = ''
  try {
    applyState(await forumApi.listSearchProviders())
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.forum.settings.search.loadFailed')
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!canManage.value || !selected.value) return
  saving.value = true
  loadError.value = ''
  try {
    await forumApi.selectSearchProvider(selected.value)
    await load()
    success(t('admin.forum.settings.search.saved'))
    toast.add({ color: 'primary', icon: 'i-lucide-info', title: t('admin.forum.settings.search.switchHint'), duration: 5000 })
  } catch (error) {
    failure(error, t('admin.forum.settings.search.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function restore() {
  if (!canManage.value) return
  saving.value = true
  loadError.value = ''
  try {
    await forumApi.resetSearchProvider()
    await load()
    success(t('admin.forum.settings.search.resetDone'))
  } catch (error) {
    failure(error, t('admin.forum.settings.search.resetFailed'))
  } finally {
    saving.value = false
  }
}

async function reindex() {
  if (!canManage.value) return
  reindexing.value = true
  try {
    await forumApi.reindexSearch()
    success(t('admin.forum.settings.search.reindexStarted'))
  } catch (error) {
    const message = apiErrorMessage(error)
    if (message?.includes('reindex_running')) {
      toast.add({ color: 'warning', icon: 'i-lucide-alert-triangle', title: t('admin.forum.settings.search.reindexAlreadyRunning'), duration: 5000 })
    } else {
      failure(error, t('admin.forum.settings.search.reindexFailed'))
    }
  } finally {
    reindexing.value = false
  }
}

function success(title: string) {
  toast.add({ color: 'success', icon: 'i-lucide-check', title, duration: 5000 })
}

function failure(error: unknown, fallback: string) {
  toast.add({ color: 'error', icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || fallback })
}
</script>

<template>
  <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <template #header>
      <div class="flex items-center justify-between gap-3">
        <div><h3 class="text-base font-bold">{{ t('admin.forum.settings.sections.search.title') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.forum.settings.sections.search.description') }}</p></div>
        <UBadge color="neutral" variant="soft" class="font-mono">search.provider</UBadge>
      </div>
    </template>
    <div class="grid max-w-5xl gap-6">
      <UAlert v-if="!canManage" color="warning" variant="soft" icon="i-lucide-lock" :title="t('admin.forum.settings.search.noPermission')" />
      <UAlert v-else-if="loadError" color="error" variant="soft" icon="i-lucide-triangle-alert" :title="loadError" />
      <template v-if="canManage">
        <section class="space-y-4">
          <div class="flex flex-wrap items-center gap-2 text-sm">
            <span class="text-muted">{{ t('admin.forum.settings.search.current') }}:</span>
            <strong>{{ resolved?.label || '—' }}</strong>
            <UBadge :color="pinned ? 'primary' : 'neutral'" variant="soft">{{ pinned ? t('admin.forum.settings.search.pinned') : t('admin.forum.settings.search.resolvedDefault') }}</UBadge>
          </div>
          <p class="text-xs text-muted">{{ t('admin.forum.settings.search.providerHelp') }}</p>
          <SFEmptyState v-if="!loading && providers.length === 0" icon-label="SRC" :title="t('admin.forum.settings.search.emptyProviders')" :description="t('admin.forum.settings.search.emptyProvidersHelp')" />
          <template v-else>
            <UFormField :label="t('admin.forum.settings.search.provider')" name="search-provider">
              <SFNativeSelect v-model="selected" :disabled="loading || saving" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option v-for="item in providerItems" :key="item.value" :value="item.value">{{ item.label }}</option></SFNativeSelect>
            </UFormField>
            <div class="flex flex-wrap gap-2">
              <UButton type="button" leading-icon="i-lucide-check" :loading="saving" :disabled="!dirty" @click="save">{{ t('admin.forum.settings.search.selectAction') }}</UButton>
              <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" :loading="saving" :disabled="!pinned" @click="restore">{{ t('admin.forum.settings.search.resetAction') }}</UButton>
              <UButton type="button" color="neutral" variant="ghost" leading-icon="i-lucide-refresh-cw" :loading="loading" @click="load">{{ t('admin.common.refresh') }}</UButton>
            </div>
          </template>
        </section>
        <section class="space-y-3 border-t border-slate-200 pt-5 dark:border-zinc-800">
          <div><h3 class="text-sm font-semibold">{{ t('admin.forum.settings.search.reindexTitle') }}</h3><p class="mt-1 text-xs text-muted">{{ t('admin.forum.settings.search.reindexHelp') }}</p></div>
          <div class="flex flex-wrap gap-2">
            <UButton type="button" color="primary" variant="soft" leading-icon="i-lucide-database-zap" :loading="reindexing" @click="reindex">{{ t('admin.forum.settings.search.reindexAction') }}</UButton>
            <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-external-link" :to="adminRoutes.path('/search')">{{ t('admin.forum.settings.search.reindexPage') }}</UButton>
          </div>
        </section>
      </template>
    </div>
  </UCard>
</template>
