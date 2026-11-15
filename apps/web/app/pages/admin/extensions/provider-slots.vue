<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminProviderSlots } from '~/composables/admin/useAdminProviderSlots'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import type { ProviderSlotCandidate, ProviderSlotItem, ProviderSlotProbe } from '~/composables/admin/useAdminProviderSlots'

definePageMeta({ middleware: 'admin', layout: 'admin' })
defineOptions({ name: 'AdminExtensionProviderSlots' })

const { t } = useI18n()
const { format: formatSiteDateTime } = useSiteDateTime()
const { user } = useAuthSession()
const toast = useToast()
const adminPage = useAdminPage('/extensions/provider-slots')
const { inspect, select, reset, probe } = useAdminProviderSlots()

const pending = ref(true)
const error = ref('')
const revision = ref(0)
const slots = ref<ProviderSlotItem[]>([])
const chosen = ref<Record<string, string>>({})
const busy = ref('')
const probes = ref<Record<string, ProviderSlotProbe>>({})

const isActiveSuperAdmin = computed(() =>
  user.value?.status === 'active' && user.value.roleKeys.includes('super_admin')
)
const selectedCount = computed(() => slots.value.filter(item => item.selectionStatus === 'selected').length)
const staleCount = computed(() => slots.value.filter(item => item.selectionStatus === 'stale').length)

function candidateOperationId(contractId: string, candidateId: string) {
  return `${contractId}\u0000${candidateId}`
}

function statusColor(status: ProviderSlotItem['selectionStatus']) {
  if (status === 'selected') return 'primary'
  if (status === 'stale') return 'error'
  return 'neutral'
}

function availabilityColor(availability: ProviderSlotCandidate['availability'] | ProviderSlotItem['availability']) {
  return availability === 'available' ? 'primary' : 'error'
}

function operationError(cause: unknown, fallback: string) {
  const reason = apiErrorReason(cause)
  if (reason === 'extensions.provider_slot_conflict') return t('admin.extensions.providerSlots.casConflict')
  if (reason === 'extensions.provider_slot_stale') return t('admin.extensions.providerSlots.staleOperation')
  return apiErrorMessage(cause) || t(fallback)
}

async function load() {
  pending.value = true
  error.value = ''
  try {
    const result = await inspect()
    revision.value = result.revision
    slots.value = result.slots
    const next = { ...chosen.value }
    for (const slot of result.slots) {
      if (slot.selection && slot.candidates.some(candidate => candidate.id === slot.selection?.candidateId)) {
        next[slot.contract.id] = slot.selection.candidateId
      }
    }
    chosen.value = next
  } catch (cause) {
    slots.value = []
    error.value = apiErrorMessage(cause) || t('admin.extensions.providerSlots.loadFailed')
  } finally {
    pending.value = false
  }
}

async function applySelection(slot: ProviderSlotItem) {
  const candidateId = chosen.value[slot.contract.id]
  if (!isActiveSuperAdmin.value || !candidateId) return
  busy.value = candidateOperationId(slot.contract.id, 'select')
  try {
    await select(slot.contract.id, candidateId, slot.selection?.revision ?? 0)
    toast.add({ color: 'primary', icon: 'i-lucide-check', title: t('admin.extensions.providerSlots.selected'), duration: 5000 })
    await load()
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-circle-alert', title: operationError(cause, 'admin.extensions.providerSlots.selectFailed') })
    if (apiErrorReason(cause) === 'extensions.provider_slot_conflict') await load()
  } finally {
    busy.value = ''
  }
}

async function restoreDefault(slot: ProviderSlotItem) {
  if (!isActiveSuperAdmin.value || !slot.selection) return
  busy.value = candidateOperationId(slot.contract.id, 'reset')
  try {
    await reset(slot.contract.id, slot.selection.revision)
    delete chosen.value[slot.contract.id]
    toast.add({ color: 'primary', icon: 'i-lucide-rotate-ccw', title: t('admin.extensions.providerSlots.reset'), duration: 5000 })
    await load()
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-circle-alert', title: operationError(cause, 'admin.extensions.providerSlots.resetFailed') })
    if (apiErrorReason(cause) === 'extensions.provider_slot_conflict') await load()
  } finally {
    busy.value = ''
  }
}

async function probeCandidate(slot: ProviderSlotItem, candidate: ProviderSlotCandidate) {
  if (!isActiveSuperAdmin.value) return
  const id = candidateOperationId(slot.contract.id, candidate.id)
  busy.value = id
  try {
    const result = await probe(slot.contract.id, candidate.id)
    probes.value[id] = result
    toast.add({
      color: result.ok ? 'primary' : 'error',
      icon: result.ok ? 'i-lucide-circle-check' : 'i-lucide-circle-alert',
      title: result.ok ? t('admin.extensions.providerSlots.probePassed') : t('admin.extensions.providerSlots.probeFailed')
    })
  } catch (cause) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-circle-alert', title: operationError(cause, 'admin.extensions.providerSlots.probeFailed') })
  } finally {
    busy.value = ''
  }
}

useSeoMeta({ title: t('admin.extensions.providerSlots.metaTitle') })
void load()
</script>

<template>
  <div data-testid="admin-provider-slots" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.providerSlots.title') }}
      </h2>
      <p class="max-w-4xl text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.providerSlots.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-5 rounded-lg border border-slate-200 bg-white px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
      <template #left>
        <div class="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-zinc-300">
          <span>{{ t('admin.extensions.providerSlots.count', { count: slots.length }) }}</span>
          <UBadge color="neutral" variant="subtle">{{ t('admin.extensions.providerSlots.revision', { revision }) }}</UBadge>
          <UBadge color="primary" variant="subtle">{{ t('admin.extensions.providerSlots.selectedCount', { count: selectedCount }) }}</UBadge>
          <UBadge v-if="staleCount" color="error" variant="subtle">{{ t('admin.extensions.providerSlots.staleCount', { count: staleCount }) }}</UBadge>
        </div>
      </template>
      <template #right>
        <UButton icon="i-lucide-rotate-cw" color="neutral" variant="subtle" :loading="pending" @click="load">
          {{ t('admin.extensions.refresh') }}
        </UButton>
      </template>
    </UDashboardToolbar>

    <SFAlert v-if="error" variant="danger" :title="error" class="mb-4" />
    <SFAlert
      v-else-if="!isActiveSuperAdmin && !pending"
      variant="warning"
      :title="t('admin.extensions.providerSlots.readOnlyTitle')"
      :description="t('admin.extensions.providerSlots.readOnlyDescription')"
      class="mb-4"
    />

    <div v-if="pending" class="space-y-3" aria-busy="true">
      <USkeleton v-for="index in 3" :key="index" class="h-40 w-full rounded-lg" />
    </div>

    <SFEmptyState
      v-else-if="!error && slots.length === 0"
      icon-label="PRV"
      :title="t('admin.extensions.providerSlots.emptyTitle')"
      :description="t('admin.extensions.providerSlots.emptyDescription')"
    />

    <div v-else-if="!error" class="space-y-4">
      <article
        v-for="slot in slots"
        :key="slot.contract.id"
        class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
      >
        <header class="flex flex-col gap-3 border-b border-slate-200 px-4 py-4 dark:border-zinc-800 sm:flex-row sm:items-start sm:justify-between">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <strong class="break-all text-sm text-slate-900 dark:text-zinc-100">{{ slot.contract.slot }}</strong>
              <UBadge :color="statusColor(slot.selectionStatus)" variant="subtle">
                {{ t(`admin.extensions.providerSlots.status.${slot.selectionStatus}`) }}
              </UBadge>
              <UBadge :color="availabilityColor(slot.availability)" variant="subtle">
                {{ t(`admin.extensions.providerSlots.availability.${slot.availability}`) }}
              </UBadge>
            </div>
            <p class="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
              {{ slot.contract.id }} · {{ slot.contract.contractVersion }}
            </p>
            <p v-if="slot.selection" class="mt-2 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.providerSlots.auditEvidence', {
                revision: slot.selection.revision,
                actor: slot.selection.selectedByUserId,
                audit: slot.selection.selectionAuditEventId,
                updatedAt: formatSiteDateTime(slot.selection.updatedAt)
              }) }}
            </p>
          </div>
          <UButton
            v-if="slot.selection"
            icon="i-lucide-rotate-ccw"
            color="neutral"
            variant="soft"
            size="sm"
            :disabled="!isActiveSuperAdmin"
            :loading="busy === candidateOperationId(slot.contract.id, 'reset')"
            @click="restoreDefault(slot)"
          >
            {{ t('admin.extensions.providerSlots.restoreDefault') }}
          </UButton>
        </header>

        <SFAlert
          v-if="slot.selectionStatus === 'stale'"
          variant="danger"
          :title="t('admin.extensions.providerSlots.staleTitle')"
          :description="t('admin.extensions.providerSlots.staleDescription')"
          class="m-4"
        />

        <dl class="grid gap-x-6 gap-y-3 border-b border-slate-200 px-4 py-4 text-xs dark:border-zinc-800 sm:grid-cols-2 lg:grid-cols-4">
          <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.owner') }}</dt><dd class="break-all font-mono">{{ slot.contract.artifact.extensionId }} @ {{ slot.contract.artifact.extensionVersion }}</dd></div>
          <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.fallback') }}</dt><dd>{{ t(`admin.extensions.providerSlots.fallback.${slot.contract.fallback}`) }}</dd></div>
          <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.timeout') }}</dt><dd>{{ slot.contract.timeoutMs }} ms</dd></div>
          <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.schemas') }}</dt><dd class="break-all font-mono">{{ slot.contract.requestSchema }} → {{ slot.contract.responseSchema }}</dd></div>
        </dl>

        <fieldset class="space-y-0" :disabled="!isActiveSuperAdmin">
          <legend class="sr-only">{{ t('admin.extensions.providerSlots.chooseProvider') }}</legend>
          <div
            v-for="candidate in slot.candidates"
            :key="candidate.id"
            class="border-b border-slate-100 px-4 py-4 last:border-b-0 dark:border-zinc-800"
          >
            <div class="flex min-w-0 items-start gap-3">
              <input
                v-model="chosen[slot.contract.id]"
                type="radio"
                :name="`provider-slot-${slot.contract.id}`"
                :value="candidate.id"
                class="mt-1 size-4 shrink-0 accent-[var(--sf-accent)]"
              >
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <strong class="break-all text-sm text-slate-900 dark:text-zinc-100">{{ candidate.label || candidate.id }}</strong>
                  <UBadge color="neutral" variant="subtle" size="xs">#{{ candidate.rank }} · P{{ candidate.priority }}</UBadge>
                  <UBadge :color="availabilityColor(candidate.availability)" variant="subtle" size="xs">
                    {{ t(`admin.extensions.providerSlots.availability.${candidate.availability}`) }}
                  </UBadge>
                  <UBadge v-if="slot.selection?.candidateId === candidate.id" color="primary" variant="subtle" size="xs">
                    {{ t('admin.extensions.providerSlots.current') }}
                  </UBadge>
                </div>
                <p class="mt-1 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">{{ candidate.id }}</p>
                <dl class="mt-3 grid gap-x-5 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-4">
                  <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.extension') }}</dt><dd class="break-all font-mono">{{ candidate.artifact.extensionId }} @ {{ candidate.artifact.extensionVersion }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.digest') }}</dt><dd class="break-all font-mono">{{ candidate.artifact.packageDigest }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.runtime') }}</dt><dd class="break-all font-mono">{{ candidate.artifact.runtimeInstanceId }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.providerSlots.fields.handler') }}</dt><dd class="break-all font-mono">{{ candidate.handler }}</dd></div>
                </dl>
                <SFAlert
                  v-if="probes[candidateOperationId(slot.contract.id, candidate.id)]"
                  :variant="probes[candidateOperationId(slot.contract.id, candidate.id)]?.ok ? 'success' : 'danger'"
                  :title="probes[candidateOperationId(slot.contract.id, candidate.id)]?.reason"
                  :description="probes[candidateOperationId(slot.contract.id, candidate.id)]?.message"
                  class="mt-3"
                />
              </div>
              <UButton
                icon="i-lucide-activity"
                color="neutral"
                variant="soft"
                size="sm"
                :disabled="candidate.availability !== 'available' || !isActiveSuperAdmin"
                :loading="busy === candidateOperationId(slot.contract.id, candidate.id)"
                @click="probeCandidate(slot, candidate)"
              >
                {{ t('admin.extensions.providerSlots.probe') }}
              </UButton>
            </div>
          </div>
        </fieldset>

        <footer class="flex flex-col gap-2 bg-slate-50 px-4 py-3 dark:bg-zinc-950 sm:flex-row sm:items-center sm:justify-between">
          <p class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.extensions.providerSlots.casHint', { revision: slot.selection?.revision ?? 0 }) }}
          </p>
          <UButton
            icon="i-lucide-shield-check"
            color="primary"
            :disabled="!chosen[slot.contract.id] || !isActiveSuperAdmin"
            :loading="busy === candidateOperationId(slot.contract.id, 'select')"
            @click="applySelection(slot)"
          >
            {{ t('admin.extensions.providerSlots.selectAction') }}
          </UButton>
        </footer>
      </article>
    </div>
  </div>
</template>
