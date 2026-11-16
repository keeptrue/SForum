<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage } from '~/composables/useApiClient'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminWebhooks' })

type Endpoint = {
  id: number
  name: string
  targetUrl: string
  secretMasked?: string
  hasSecret: boolean
  events: string[]
  enabled: boolean
  description?: string
  createdAt: string
}

type Delivery = {
  id: number
  endpointId: number
  eventName: string
  status: string
  attemptCount: number
  httpStatus?: number
  reason?: string
  errorSummary?: string
  createdAt: string
}

type CatalogEvent = { name: string, description: string, kind: string }

const { t } = useI18n()
const { request } = useApiClient()
const adminPage = useAdminPage('/webhooks')
const toast = useToast()
const { can } = useAuthSession()
const canManage = computed(() => can('settings.manage') || can('settings.site.manage'))

const pending = ref(true)
const saving = ref(false)
const createModalOpen = ref(false)
// UX2-13：加载类错误留在页级；新建失败必须在模态内可见（模态捕获焦点，页级提示看不到）。
const loadError = ref('')
const modalError = ref('')
const endpoints = ref<Endpoint[]>([])
const deliveries = ref<Delivery[]>([])
const catalogEvents = ref<CatalogEvent[]>([])

const form = reactive({
  name: '',
  targetUrl: '',
  secret: '',
  events: [] as string[],
  description: ''
})

function resetForm() {
  form.name = ''
  form.targetUrl = ''
  form.secret = ''
  form.events = []
  form.description = ''
}

function openCreateModal() {
  resetForm()
  modalError.value = ''
  createModalOpen.value = true
}

function closeCreateModal() {
  createModalOpen.value = false
  modalError.value = ''
  resetForm()
}

async function load() {
  pending.value = true
  loadError.value = ''
  try {
    const [ep, del, events] = await Promise.all([
      request<{ items: Endpoint[] }>('/admin/webhooks/endpoints'),
      request<{ items: Delivery[] }>('/admin/webhooks/deliveries?limit=50'),
      request<{ items: CatalogEvent[] }>('/admin/webhooks/events')
    ])
    endpoints.value = ep.items || []
    deliveries.value = del.items || []
    catalogEvents.value = events.items || []
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.webhooks.loadFailed')
  } finally {
    pending.value = false
  }
}

async function createEndpoint() {
  if (!canManage.value) {
    return
  }
  saving.value = true
  modalError.value = ''
  try {
    await request('/admin/webhooks/endpoints', {
      method: 'POST',
      body: {
        name: form.name,
        targetUrl: form.targetUrl,
        secret: form.secret || undefined,
        events: form.events,
        description: form.description,
        enabled: true
      }
    })
    closeCreateModal()
    toast.add({ title: t('admin.webhooks.created'), color: 'primary', duration: 5000 })
    await load()
  } catch (error) {
    modalError.value = apiErrorMessage(error) || t('admin.webhooks.saveFailed')
  } finally {
    saving.value = false
  }
}

async function setEnabled(item: Endpoint, enabled: boolean) {
  if (!canManage.value) {
    return
  }
  try {
    await request(`/admin/webhooks/endpoints/${item.id}`, {
      method: 'PATCH',
      body: { enabled }
    })
    toast.add({
      title: enabled ? t('admin.webhooks.enabled') : t('admin.webhooks.disabled'),
      color: 'primary'
    })
    await load()
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.webhooks.saveFailed')
  }
}

async function removeEndpoint(item: Endpoint) {
  if (!canManage.value) {
    return
  }
  try {
    await request(`/admin/webhooks/endpoints/${item.id}`, { method: 'DELETE' })
    toast.add({ title: t('admin.webhooks.deleted'), color: 'primary' })
    await load()
  } catch (error) {
    loadError.value = apiErrorMessage(error) || t('admin.webhooks.saveFailed')
  }
}

function statusColor(status: string): 'success' | 'error' | 'warning' | 'neutral' | 'info' {
  if (status === 'sent') return 'success'
  if (status === 'failed' || status === 'dead') return 'error'
  if (status === 'queued' || status === 'sending') return 'warning'
  if (status === 'skipped') return 'neutral'
  return 'info'
}

function toggleEvent(eventName: string) {
  form.events = form.events.includes(eventName)
    ? form.events.filter(name => name !== eventName)
    : [...form.events, eventName]
}

onMounted(load)
</script>

<template>
  <div class="mb-5 flex flex-wrap items-start justify-between gap-3">
    <div>
      <h2 class="flex items-center gap-2 text-xl font-bold">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)]" />
        {{ t('admin.webhooks.title') }}
      </h2>
      <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.webhooks.intro') }}
      </p>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <UButton
        icon="i-lucide-rotate-cw"
        color="neutral"
        variant="subtle"
        :loading="pending"
        @click="load"
      >
        {{ t('admin.extensions.refresh') }}
      </UButton>
      <UButton
        v-if="canManage"
        icon="i-lucide-plus"
        color="primary"
        @click="openCreateModal"
      >
        {{ t('admin.webhooks.create') }}
      </UButton>
    </div>
  </div>

  <UAlert
    class="mb-5"
    color="neutral"
    variant="subtle"
    icon="i-lucide-info"
    :title="t('admin.webhooks.hintTitle')"
    :description="t('admin.webhooks.hintBody')"
  />

  <UAlert
    v-if="loadError"
    class="mb-5"
    color="error"
    variant="subtle"
    icon="i-lucide-circle-alert"
    :title="loadError"
  />

  <section class="mb-5 border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <div class="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
      <p class="text-sm font-semibold">
        {{ t('admin.webhooks.endpointsTitle') }}
      </p>
    </div>
    <div v-if="pending" class="px-4 py-8" role="status" :aria-label="t('admin.webhooks.loading')">
      <SFSkeleton :lines="4" />
    </div>
    <div v-else-if="!endpoints.length" class="px-4 py-6">
      <SFEmptyState :title="t('admin.webhooks.emptyEndpoints')" />
    </div>
    <div v-else class="overflow-x-auto">
      <table class="min-w-full text-left text-sm">
        <thead class="bg-slate-50 text-xs text-slate-500 dark:bg-zinc-950">
          <tr>
            <th class="px-3 py-3">{{ t('admin.webhooks.colName') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colUrl') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colEvents') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colStatus') }}</th>
            <th class="px-3 py-3 text-right">{{ t('admin.webhooks.colActions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-200 dark:divide-zinc-800">
          <tr v-for="item in endpoints" :key="item.id">
            <td class="px-3 py-3 align-top">
              <p class="font-medium">{{ item.name }}</p>
              <p v-if="item.hasSecret" class="mt-1 font-mono text-[11px] text-slate-400">
                {{ item.secretMasked }}
              </p>
            </td>
            <td class="px-3 py-3 align-top break-all font-mono text-xs">
              {{ item.targetUrl }}
            </td>
            <td class="px-3 py-3 align-top text-xs">
              <span v-if="!item.events?.length">{{ t('admin.webhooks.allEvents') }}</span>
              <span v-else>{{ item.events.join(', ') }}</span>
            </td>
            <td class="px-3 py-3 align-top">
              <UBadge :color="item.enabled ? 'success' : 'neutral'" variant="subtle">
                {{ item.enabled ? t('admin.webhooks.statusOn') : t('admin.webhooks.statusOff') }}
              </UBadge>
            </td>
            <td class="px-3 py-3 align-top">
              <div class="flex flex-wrap justify-end gap-1">
                <UButton
                  v-if="canManage && item.enabled"
                  size="xs"
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-pause"
                  @click="setEnabled(item, false)"
                >
                  {{ t('admin.webhooks.disable') }}
                </UButton>
                <UButton
                  v-if="canManage && !item.enabled"
                  size="xs"
                  color="primary"
                  variant="subtle"
                  icon="i-lucide-play"
                  @click="setEnabled(item, true)"
                >
                  {{ t('admin.webhooks.enable') }}
                </UButton>
                <UButton
                  v-if="canManage"
                  size="xs"
                  color="error"
                  variant="ghost"
                  icon="i-lucide-trash-2"
                  @click="removeEndpoint(item)"
                >
                  {{ t('admin.webhooks.delete') }}
                </UButton>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <section class="border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <div class="border-b border-slate-200 px-4 py-3 dark:border-zinc-800">
      <p class="text-sm font-semibold">
        {{ t('admin.webhooks.deliveriesTitle') }}
      </p>
    </div>
    <div v-if="!deliveries.length" class="px-4 py-6">
      <SFEmptyState :title="t('admin.webhooks.emptyDeliveries')" />
    </div>
    <div v-else class="overflow-x-auto">
      <table class="min-w-full text-left text-sm">
        <thead class="bg-slate-50 text-xs text-slate-500 dark:bg-zinc-950">
          <tr>
            <th class="px-3 py-3">ID</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colEvent') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colStatus') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colAttempts') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colReason') }}</th>
            <th class="px-3 py-3">{{ t('admin.webhooks.colCreated') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-200 dark:divide-zinc-800">
          <tr v-for="item in deliveries" :key="item.id">
            <td class="px-3 py-3 font-mono text-xs">{{ item.id }}</td>
            <td class="px-3 py-3 font-mono text-xs">{{ item.eventName }}</td>
            <td class="px-3 py-3">
              <UBadge :color="statusColor(item.status)" variant="subtle">
                {{ item.status }}
              </UBadge>
              <span v-if="item.httpStatus" class="ml-1 text-xs text-slate-400">HTTP {{ item.httpStatus }}</span>
            </td>
            <td class="px-3 py-3 tabular-nums">{{ item.attemptCount }}</td>
            <td class="px-3 py-3 text-xs text-slate-500">
              {{ item.reason || item.errorSummary || '-' }}
            </td>
            <td class="px-3 py-3 text-xs tabular-nums">{{ item.createdAt }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>

  <!-- 新建出站端点 -->
  <UModal
    v-model:open="createModalOpen"
    :ui="{ content: 'sm:max-w-2xl' }"
    @update:open="(open) => { if (!open) closeCreateModal() }"
  >
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.webhooks.createTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.webhooks.createIntro') }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.common.cancel')"
            @click="closeCreateModal"
          />
        </div>

        <div class="grid flex-1 gap-4 overflow-y-auto px-5 py-4">
          <UAlert
            v-if="modalError"
            data-testid="admin-webhooks-create-error"
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="modalError"
          />
          <div class="grid gap-3 md:grid-cols-2">
            <UFormField :label="t('admin.webhooks.fieldName')">
              <UInput v-model="form.name" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.webhooks.fieldUrl')">
              <UInput v-model="form.targetUrl" class="w-full" placeholder="https://example.com/hooks/sforum" />
            </UFormField>
            <UFormField :label="t('admin.webhooks.fieldSecret')">
              <UInput v-model="form.secret" class="w-full" type="password" autocomplete="new-password" />
            </UFormField>
            <UFormField :label="t('admin.webhooks.fieldDescription')">
              <UInput v-model="form.description" class="w-full" />
            </UFormField>
          </div>
          <div>
            <p class="mb-2 text-xs font-medium text-slate-600 dark:text-zinc-300">
              {{ t('admin.webhooks.fieldEvents') }}
            </p>
            <p class="mb-2 text-xs text-slate-500">
              {{ t('admin.webhooks.eventsHint') }}
            </p>
            <div class="flex flex-wrap gap-2">
              <UButton
                v-for="event in catalogEvents"
                :key="event.name"
                size="xs"
                :color="form.events.includes(event.name) ? 'primary' : 'neutral'"
                :variant="form.events.includes(event.name) ? 'solid' : 'subtle'"
                @click="toggleEvent(event.name)"
              >
                {{ event.name }}
              </UButton>
            </div>
          </div>
        </div>

        <div class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <UButton type="button" color="neutral" variant="ghost" @click="closeCreateModal">
            {{ t('admin.common.cancel') }}
          </UButton>
          <UButton
            icon="i-lucide-plus"
            color="primary"
            :loading="saving"
            :disabled="!form.name || !form.targetUrl"
            @click="createEndpoint"
          >
            {{ t('admin.webhooks.create') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
