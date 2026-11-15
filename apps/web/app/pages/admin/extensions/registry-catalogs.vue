<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  type ContentCatalogView,
  type EntityCatalogView,
  type EntityImportExportDryRunView,
  type MediaCatalogView,
  useAdminRegistryCatalogs
} from '~/composables/admin/useAdminRegistryCatalogs'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminExtensionRegistryCatalogs' })

const { t } = useI18n()
const toast = useToast()
const adminRoutes = useAdminRoutes()
const adminPage = useAdminPage('/extensions/registry-catalogs')
const {
  loadContentCatalog,
  loadEntityCatalog,
  loadMediaCatalog,
  dryRunEntityImportExport
} = useAdminRegistryCatalogs()

const tab = ref<'content' | 'entity' | 'media'>('entity')
const pending = ref(true)
const error = ref('')
const content = ref<ContentCatalogView | null>(null)
const entities = ref<EntityCatalogView | null>(null)
const media = ref<MediaCatalogView | null>(null)

const dryRunEntityId = ref('')
const dryRunAction = ref<'import' | 'export'>('export')
const dryRunPending = ref(false)
const dryRunError = ref('')
const dryRunResult = ref<EntityImportExportDryRunView | null>(null)

const entityOptions = computed(() => {
  const rows = entities.value?.entities || []
  return rows.map(row => ({
    label: row.id,
    value: row.id
  }))
})

const actionItems = computed(() => [
  { label: t('admin.extensions.registryCatalogs.actionExport'), value: 'export' as const },
  { label: t('admin.extensions.registryCatalogs.actionImport'), value: 'import' as const }
])

async function loadAll() {
  pending.value = true
  error.value = ''
  try {
    const [contentCatalog, entityCatalog, mediaCatalog] = await Promise.all([
      loadContentCatalog(),
      loadEntityCatalog(),
      loadMediaCatalog()
    ])
    content.value = contentCatalog
    entities.value = entityCatalog
    media.value = mediaCatalog
    if (!dryRunEntityId.value && entityCatalog.entities.length > 0) {
      dryRunEntityId.value = entityCatalog.entities[0]?.id ?? ''
    }
  } catch (err) {
    error.value = apiErrorMessage(err)
    content.value = null
    entities.value = null
    media.value = null
  } finally {
    pending.value = false
  }
}

async function refresh() {
  await loadAll()
  if (!error.value) {
    toast.add({ title: t('admin.extensions.registryCatalogs.refreshed'), color: 'primary' })
  }
}

async function runDryRun() {
  dryRunPending.value = true
  dryRunError.value = ''
  dryRunResult.value = null
  try {
    dryRunResult.value = await dryRunEntityImportExport(dryRunEntityId.value, dryRunAction.value)
    toast.add({
      title: dryRunResult.value.allowed
        ? t('admin.extensions.registryCatalogs.dryRunAllowed')
        : t('admin.extensions.registryCatalogs.dryRunDenied'),
      color: 'primary'
    })
  } catch (err) {
    dryRunError.value = apiErrorMessage(err)
  } finally {
    dryRunPending.value = false
  }
}

onMounted(loadAll)
useSeoMeta({ title: t('admin.extensions.registryCatalogs.metaTitle') })
</script>

<template>
  <div data-testid="admin-registry-catalogs" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.registryCatalogs.title') }}
      </h2>
      <p class="max-w-4xl text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.registryCatalogs.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-5 rounded-lg border border-slate-200 bg-white px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
      <template #left>
        <div class="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-zinc-300" data-testid="registry-catalogs-summary">
          <UIcon name="i-lucide-library" class="size-4" />
          <span class="truncate">{{ t('admin.extensions.registryCatalogs.contentCount') }} {{ content?.entryCount ?? 0 }}</span>
          <span class="text-slate-300 dark:text-zinc-700">/</span>
          <span class="truncate">{{ t('admin.extensions.registryCatalogs.entityCount') }} {{ entities?.entryCount ?? 0 }}</span>
          <span class="text-slate-300 dark:text-zinc-700">/</span>
          <span class="truncate">{{ t('admin.extensions.registryCatalogs.mediaCount') }} {{ media?.entryCount ?? 0 }}</span>
        </div>
      </template>
      <template #right>
        <div class="flex shrink-0 items-center gap-2">
          <UButton
            icon="i-lucide-arrow-left"
            color="neutral"
            variant="subtle"
            :to="adminRoutes.path('/extensions/plugins')"
          >
            {{ t('admin.extensions.registryCatalogs.back') }}
          </UButton>
          <UButton
            icon="i-lucide-rotate-cw"
            color="neutral"
            variant="subtle"
            :loading="pending"
            data-testid="registry-catalogs-refresh"
            @click="refresh"
          >
            {{ t('admin.extensions.registryCatalogs.refresh') }}
          </UButton>
        </div>
      </template>
    </UDashboardToolbar>

    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="error"
      class="mb-6"
      data-testid="registry-catalogs-error"
      :close-button="{ icon: 'i-lucide-x', color: 'neutral', variant: 'link' }"
      @close="error = ''"
    />

    <div
      v-if="pending && !content && !entities && !media"
      class="space-y-3"
      data-testid="registry-catalogs-loading"
      aria-busy="true"
    >
      <USkeleton class="h-32 w-full rounded-lg" />
      <span class="sr-only">{{ t('admin.extensions.registryCatalogs.loading') }}</span>
    </div>

    <template v-if="content || entities || media">
      <div class="mb-4 flex flex-wrap gap-2" data-testid="registry-catalogs-tabs">
        <UButton
          :variant="tab === 'entity' ? 'solid' : 'soft'"
          color="neutral"
          icon="i-lucide-database"
          size="sm"
          data-testid="registry-catalogs-tab-entity"
          @click="() => { tab = 'entity' }"
        >
          {{ t('admin.extensions.registryCatalogs.tabEntity') }}
        </UButton>
        <UButton
          :variant="tab === 'content' ? 'solid' : 'soft'"
          color="neutral"
          icon="i-lucide-file-text"
          size="sm"
          data-testid="registry-catalogs-tab-content"
          @click="() => { tab = 'content' }"
        >
          {{ t('admin.extensions.registryCatalogs.tabContent') }}
        </UButton>
        <UButton
          :variant="tab === 'media' ? 'solid' : 'soft'"
          color="neutral"
          icon="i-lucide-image"
          size="sm"
          data-testid="registry-catalogs-tab-media"
          @click="() => { tab = 'media' }"
        >
          {{ t('admin.extensions.registryCatalogs.tabMedia') }}
        </UButton>
      </div>

      <div
        v-if="tab === 'entity' && entities"
        class="space-y-4"
        data-testid="registry-catalogs-entity"
      >
        <section class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
          <div class="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
            <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
              {{ t('admin.extensions.registryCatalogs.dryRunTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.registryCatalogs.dryRunIntro') }}
            </p>
          </div>
          <div class="px-4 py-4">
            <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_180px_auto] lg:items-end">
              <div class="min-w-0">
                <label for="registry-catalogs-dry-run-entity" class="mb-1 block text-xs font-medium text-slate-600 dark:text-zinc-300">
                  {{ t('admin.extensions.registryCatalogs.entityId') }}
                </label>
                <USelect
                  id="registry-catalogs-dry-run-entity"
                  v-model="dryRunEntityId"
                  :items="entityOptions"
                  value-key="value"
                  class="w-full"
                  data-testid="registry-catalogs-dry-run-entity"
                />
              </div>
              <div class="min-w-0">
                <label for="registry-catalogs-dry-run-action" class="mb-1 block text-xs font-medium text-slate-600 dark:text-zinc-300">
                  {{ t('admin.extensions.registryCatalogs.action') }}
                </label>
                <USelect
                  id="registry-catalogs-dry-run-action"
                  v-model="dryRunAction"
                  :items="actionItems"
                  value-key="value"
                  class="w-full"
                  data-testid="registry-catalogs-dry-run-action"
                />
              </div>
              <UButton
                icon="i-lucide-play"
                color="primary"
                class="justify-center"
                :loading="dryRunPending"
                :disabled="!dryRunEntityId"
                data-testid="registry-catalogs-dry-run-submit"
                @click="runDryRun"
              >
                {{ t('admin.extensions.registryCatalogs.runDryRun') }}
              </UButton>
            </div>
            <UAlert
              v-if="dryRunError"
              color="error"
              variant="subtle"
              icon="i-lucide-triangle-alert"
              class="mt-3"
              :title="dryRunError"
              data-testid="registry-catalogs-dry-run-error"
              :close-button="{ icon: 'i-lucide-x', color: 'neutral', variant: 'link' }"
              @close="dryRunError = ''"
            />
            <div
              v-if="dryRunResult"
              class="mt-3 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950"
              data-testid="registry-catalogs-dry-run-result"
            >
              <div class="font-medium text-slate-900 dark:text-zinc-100">
                {{ dryRunResult.allowed
                  ? t('admin.extensions.registryCatalogs.dryRunAllowed')
                  : t('admin.extensions.registryCatalogs.dryRunDenied') }}
              </div>
              <div class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.extensions.registryCatalogs.executes') }}:
                {{ dryRunResult.executes
                  ? t('admin.extensions.registryCatalogs.yes')
                  : t('admin.extensions.registryCatalogs.no') }}
                · {{ dryRunResult.action }}
                <span v-if="dryRunResult.permissionKey"> · {{ dryRunResult.permissionKey }}</span>
              </div>
              <div v-if="dryRunResult.reason" class="mt-1 text-xs text-slate-600 dark:text-zinc-300">
                {{ dryRunResult.reason }}
              </div>
            </div>
          </div>
        </section>

        <section class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
          <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
            <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
              {{ t('admin.extensions.registryCatalogs.entityCount') }}
            </h3>
            <UBadge color="neutral" variant="outline">
              {{ entities.entryCount }}
            </UBadge>
          </div>
          <div
            v-if="entities.entities.length === 0"
            class="p-10"
            data-testid="registry-catalogs-entity-empty"
          >
            <SFEmptyState icon-label="ENT" :title="t('admin.extensions.registryCatalogs.emptyEntity')" />
          </div>
          <ul v-else class="divide-y divide-slate-200 dark:divide-zinc-800" data-testid="registry-catalogs-entity-list">
            <li
              v-for="row in entities.entities"
              :key="row.id"
              class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_220px]"
            >
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <UIcon name="i-lucide-database" class="size-4 text-[var(--sf-accent)]" />
                  <h3 class="min-w-0 break-all font-mono text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ row.id }}
                  </h3>
                  <UBadge v-if="row.importExportPolicy" color="neutral" variant="subtle">
                    {{ row.importExportPolicy }}
                  </UBadge>
                </div>
              </div>
              <div class="flex flex-col gap-1 break-all text-xs text-slate-500 md:items-end dark:text-zinc-400">
                <span>{{ row.extensionId }}</span>
                <span>{{ row.kind }}</span>
              </div>
            </li>
          </ul>
        </section>
      </div>

      <section
        v-else-if="tab === 'content' && content"
        class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
        data-testid="registry-catalogs-content"
      >
        <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.registryCatalogs.contentCount') }}
          </h3>
          <UBadge color="neutral" variant="outline">
            {{ content.entryCount }}
          </UBadge>
        </div>
        <div
          v-if="content.content.length === 0"
          class="p-10"
          data-testid="registry-catalogs-content-empty"
        >
          <SFEmptyState icon-label="CNT" :title="t('admin.extensions.registryCatalogs.emptyContent')" />
        </div>
        <ul v-else class="divide-y divide-slate-200 dark:divide-zinc-800" data-testid="registry-catalogs-content-list">
          <li
            v-for="row in content.content"
            :key="row.id"
            class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_220px]"
          >
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <UIcon name="i-lucide-file-text" class="size-4 text-[var(--sf-accent)]" />
                <h3 class="min-w-0 break-all font-mono text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ row.id }}
                </h3>
                <UBadge color="neutral" variant="subtle">
                  {{ row.kind }}
                </UBadge>
              </div>
            </div>
            <div class="flex flex-col gap-1 break-all text-xs text-slate-500 md:items-end dark:text-zinc-400">
              <span>{{ row.extensionId }}</span>
            </div>
          </li>
        </ul>
      </section>

      <section
        v-else-if="tab === 'media' && media"
        class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
        data-testid="registry-catalogs-media"
      >
        <div class="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
          <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.registryCatalogs.mediaCount') }}
          </h3>
          <UBadge color="neutral" variant="outline">
            {{ media.entryCount }}
          </UBadge>
        </div>
        <div
          v-if="media.entryCount === 0"
          class="p-10"
          data-testid="registry-catalogs-media-empty"
        >
          <SFEmptyState icon-label="MED" :title="t('admin.extensions.registryCatalogs.emptyMedia')" />
        </div>
        <div v-else>
          <div class="border-b border-slate-200 px-4 py-2 text-xs font-semibold text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
            {{ t('admin.extensions.registryCatalogs.policies') }}
          </div>
          <ul class="divide-y divide-slate-200 dark:divide-zinc-800" data-testid="registry-catalogs-media-policies">
            <li
              v-for="row in media.policies"
              :key="row.id"
              class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_220px]"
            >
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <UIcon name="i-lucide-shield-check" class="size-4 text-[var(--sf-accent)]" />
                  <h3 class="min-w-0 break-all font-mono text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ row.id }}
                  </h3>
                  <UBadge color="neutral" variant="subtle">
                    {{ row.purpose }}
                  </UBadge>
                </div>
              </div>
              <div class="flex flex-col gap-1 break-all text-xs text-slate-500 md:items-end dark:text-zinc-400">
                <span>{{ row.extensionId }}</span>
              </div>
            </li>
          </ul>
          <div class="border-y border-slate-200 px-4 py-2 text-xs font-semibold text-slate-500 dark:border-zinc-800 dark:text-zinc-400">
            {{ t('admin.extensions.registryCatalogs.processors') }}
          </div>
          <ul class="divide-y divide-slate-200 dark:divide-zinc-800" data-testid="registry-catalogs-media-processors">
            <li
              v-for="row in media.processors"
              :key="row.id"
              class="grid gap-3 px-4 py-4 md:grid-cols-[minmax(0,1fr)_220px]"
            >
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <UIcon name="i-lucide-activity" class="size-4 text-[var(--sf-accent)]" />
                  <h3 class="min-w-0 break-all font-mono text-sm font-semibold text-slate-900 dark:text-zinc-100">
                    {{ row.id }}
                  </h3>
                  <UBadge color="neutral" variant="subtle">
                    {{ row.stage }}
                  </UBadge>
                </div>
              </div>
              <div class="flex flex-col gap-1 break-all text-xs text-slate-500 md:items-end dark:text-zinc-400">
                <span>{{ row.extensionId }}</span>
              </div>
            </li>
          </ul>
        </div>
      </section>
    </template>
  </div>
</template>
