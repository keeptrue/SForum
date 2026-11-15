<script setup lang="ts">
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useAdminRouteProviders } from '~/composables/admin/useAdminRouteProviders'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'
import {
  routeProviderConflictId,
  routeProviderRisk,
  type RouteProviderCandidate,
  type RouteProviderConflict
} from '~/composables/admin/useAdminRouteProviders'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminExtensionRouteProviders'
})

const { t } = useI18n()
const { format: formatSiteDateTime } = useSiteDateTime()
const toast = useToast()
const adminPage = useAdminPage('/extensions/route-providers')
const { user } = useAuthSession()
const { listConflicts, selectProvider, resetProvider } = useAdminRouteProviders()

const pending = ref(true)
const error = ref('')
const conflicts = ref<RouteProviderConflict[]>([])
const selectedCandidates = ref<Record<string, string>>({})
const busyConflict = ref('')

const isActiveSuperAdmin = computed(() =>
  user.value?.status === 'active' && user.value.roleKeys.includes('super_admin')
)
const selectedCount = computed(() => conflicts.value.filter(item => item.selectionStatus === 'selected').length)
const unresolvedCount = computed(() => conflicts.value.length - selectedCount.value)

function candidateKey(candidate: RouteProviderCandidate) {
  const artifact = candidate.artifact
  return [candidate.routeId, candidate.contractVersion, artifact?.extensionId, artifact?.extensionVersion, artifact?.packageDigest, artifact?.runtimeInstanceId].join('\u0000')
}

function chosenCandidate(conflict: RouteProviderConflict) {
  const selectedKey = selectedCandidates.value[routeProviderConflictId(conflict.key)]
  return conflict.candidates.find(candidate => candidate.artifact && candidate.action === 'replace' && candidateKey(candidate) === selectedKey)
}

function canChooseCandidate(candidate: RouteProviderCandidate) {
  return candidate.providerKind === 'plugin' && candidate.action === 'replace' && Boolean(candidate.artifact)
}

function statusColor(status: RouteProviderConflict['selectionStatus']) {
  if (status === 'selected') return 'primary'
  if (status === 'stale') return 'error'
  return 'warning'
}

function riskLabels(candidate: RouteProviderCandidate) {
  const risk = routeProviderRisk(candidate)
  const labels: string[] = []
  if (risk.rawRequest) labels.push(t('admin.extensions.routeProviders.risks.rawRequest'))
  if (risk.customGuard) labels.push(t('admin.extensions.routeProviders.risks.customGuard'))
  if (risk.replacementHandler) labels.push(t('admin.extensions.routeProviders.risks.replacementHandler'))
  return labels
}

function operationError(cause: unknown, fallbackKey: string) {
  const reason = apiErrorReason(cause)
  if (reason === 'extensions.route_provider_conflict') {
    return t('admin.extensions.routeProviders.casConflict')
  }
  if (reason === 'extensions.route_provider_stale') {
    return t('admin.extensions.routeProviders.staleOperation')
  }
  return apiErrorMessage(cause) || t(fallbackKey)
}

async function load() {
  pending.value = true
  error.value = ''
  try {
    const result = await listConflicts()
    conflicts.value = result
    const nextSelections = { ...selectedCandidates.value }
    for (const conflict of result) {
      const id = routeProviderConflictId(conflict.key)
      const current = conflict.candidates.find(candidate => candidate.routeId === conflict.selection?.routeId
        && candidate.contractVersion === conflict.selection?.contractVersion
        && candidate.artifact?.packageDigest === conflict.selection?.packageDigest)
      if (current && !nextSelections[id]) nextSelections[id] = candidateKey(current)
    }
    selectedCandidates.value = nextSelections
  } catch (cause) {
    conflicts.value = []
    error.value = apiErrorMessage(cause) || t('admin.extensions.routeProviders.loadFailed')
  } finally {
    pending.value = false
  }
}

async function applySelection(conflict: RouteProviderConflict) {
  const candidate = chosenCandidate(conflict)
  if (!isActiveSuperAdmin.value || !candidate?.artifact) return
  const id = routeProviderConflictId(conflict.key)
  busyConflict.value = id
  try {
    await selectProvider({
      ...conflict.key,
      providerRouteId: candidate.routeId,
      providerContractVersion: candidate.contractVersion,
      providerArtifact: candidate.artifact,
      expectedRevision: conflict.selection?.revision ?? 0
    })
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: t('admin.extensions.routeProviders.selected'),
      duration: 5000
    })
    await load()
  } catch (cause) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-alert-circle',
      title: operationError(cause, 'admin.extensions.routeProviders.selectFailed')
    })
    if (apiErrorReason(cause) === 'extensions.route_provider_conflict') await load()
  } finally {
    busyConflict.value = ''
  }
}

async function resetSelection(conflict: RouteProviderConflict) {
  if (!isActiveSuperAdmin.value || !conflict.selection) return
  const id = routeProviderConflictId(conflict.key)
  busyConflict.value = id
  try {
    await resetProvider({
      ...conflict.selection.key,
      expectedRevision: conflict.selection.revision,
      reasonCode: 'operator_reset'
    })
    toast.add({
      color: 'primary',
      icon: 'i-lucide-rotate-ccw',
      title: t('admin.extensions.routeProviders.reset'),
      duration: 5000
    })
    delete selectedCandidates.value[id]
    await load()
  } catch (cause) {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-alert-circle',
      title: operationError(cause, 'admin.extensions.routeProviders.resetFailed')
    })
    if (apiErrorReason(cause) === 'extensions.route_provider_conflict') await load()
  } finally {
    busyConflict.value = ''
  }
}

useSeoMeta({ title: t('admin.extensions.routeProviders.metaTitle') })
void load()
</script>

<template>
  <div data-testid="admin-route-providers" class="min-w-0 shrink-0">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.extensions.routeProviders.title') }}
      </h2>
      <p class="max-w-4xl text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.extensions.routeProviders.intro') }}
      </p>
    </div>

    <UDashboardToolbar class="mb-5 rounded-lg border border-slate-200 bg-white px-4 py-2.5 dark:border-zinc-800 dark:bg-zinc-900">
      <template #left>
        <div class="flex min-w-0 flex-wrap items-center gap-2 text-sm text-slate-600 dark:text-zinc-300">
          <span>{{ t('admin.extensions.routeProviders.count', { count: conflicts.length }) }}</span>
          <UBadge color="primary" variant="subtle">{{ t('admin.extensions.routeProviders.selectedCount', { count: selectedCount }) }}</UBadge>
          <UBadge v-if="unresolvedCount" color="warning" variant="subtle">{{ t('admin.extensions.routeProviders.unresolvedCount', { count: unresolvedCount }) }}</UBadge>
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
      :title="t('admin.extensions.routeProviders.readOnlyTitle')"
      :description="t('admin.extensions.routeProviders.readOnlyDescription')"
      class="mb-4"
    />

    <div v-if="pending" class="space-y-3" aria-busy="true">
      <USkeleton v-for="index in 3" :key="index" class="h-36 w-full rounded-lg" />
    </div>

    <SFEmptyState
      v-else-if="!error && conflicts.length === 0"
      icon-label="RTE"
      :title="t('admin.extensions.routeProviders.emptyTitle')"
      :description="t('admin.extensions.routeProviders.emptyDescription')"
    />

    <div v-else-if="!error" class="space-y-4">
      <article
        v-for="conflict in conflicts"
        :key="routeProviderConflictId(conflict.key)"
        class="overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
      >
        <header class="flex flex-col gap-3 border-b border-slate-200 px-4 py-4 dark:border-zinc-800 sm:flex-row sm:items-start sm:justify-between">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="rounded bg-slate-100 px-2 py-1 font-mono text-xs font-semibold text-slate-700 dark:bg-zinc-800 dark:text-zinc-200">{{ conflict.key.method }}</span>
              <code class="break-all text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ conflict.key.pathSignature }}</code>
              <UBadge :color="statusColor(conflict.selectionStatus)" variant="subtle">
                {{ t(`admin.extensions.routeProviders.status.${conflict.selectionStatus}`) }}
              </UBadge>
            </div>
            <p class="mt-2 break-all font-mono text-xs text-slate-500 dark:text-zinc-400">
              {{ conflict.key.targetRouteId }} · {{ conflict.key.targetContractVersion }}
            </p>
            <p v-if="conflict.selection" class="mt-2 break-words text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.routeProviders.auditEvidence', {
                revision: conflict.selection.revision,
                actor: conflict.selection.selectedByUserId,
                audit: conflict.selection.selectionAuditEventId,
                updatedAt: formatSiteDateTime(conflict.selection.updatedAt)
              }) }}
            </p>
          </div>
          <UButton
            v-if="conflict.selection"
            icon="i-lucide-rotate-ccw"
            color="neutral"
            variant="soft"
            size="sm"
            :disabled="!isActiveSuperAdmin"
            :loading="busyConflict === routeProviderConflictId(conflict.key)"
            @click="resetSelection(conflict)"
          >
            {{ t('admin.extensions.routeProviders.resetAction') }}
          </UButton>
        </header>

        <SFAlert
          v-if="conflict.selectionStatus === 'stale'"
          variant="danger"
          :title="t('admin.extensions.routeProviders.staleTitle')"
          :description="t('admin.extensions.routeProviders.staleDescription')"
          class="m-4"
        />

        <fieldset class="space-y-3 p-4" :disabled="!isActiveSuperAdmin || busyConflict === routeProviderConflictId(conflict.key)">
          <legend class="mb-3 text-sm font-semibold text-slate-900 dark:text-zinc-100">
            {{ t('admin.extensions.routeProviders.chooseProvider') }}
          </legend>
          <label
            v-for="candidate in conflict.candidates"
            :key="candidateKey(candidate)"
            class="block cursor-pointer rounded-md border border-slate-200 p-3 transition-colors has-[:checked]:border-[var(--sf-accent)] has-[:checked]:bg-slate-50 dark:border-zinc-700 dark:has-[:checked]:border-[var(--sf-accent-dark)] dark:has-[:checked]:bg-zinc-950"
          >
            <div class="flex items-start gap-3">
              <input
                v-if="canChooseCandidate(candidate)"
                v-model="selectedCandidates[routeProviderConflictId(conflict.key)]"
                type="radio"
                :name="`provider-${routeProviderConflictId(conflict.key)}`"
                :value="candidateKey(candidate)"
                class="mt-1 size-4 shrink-0 accent-[var(--sf-accent)]"
              >
              <UIcon v-else name="i-lucide-shield" class="mt-0.5 size-5 shrink-0 text-slate-400" />
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <strong class="break-all text-sm text-slate-900 dark:text-zinc-100">{{ candidate.artifact?.extensionId || candidate.providerKind }}</strong>
                  <UBadge v-if="conflict.selection?.routeId === candidate.routeId && conflict.selection?.packageDigest === candidate.artifact?.packageDigest" color="primary" variant="subtle" size="xs">
                    {{ t('admin.extensions.routeProviders.current') }}
                  </UBadge>
                  <UBadge v-if="!canChooseCandidate(candidate)" color="neutral" variant="subtle" size="xs">
                    {{ t('admin.extensions.routeProviders.coreBaseline') }}
                  </UBadge>
                  <span class="break-all font-mono text-xs text-slate-500">{{ candidate.routeId }} @ {{ candidate.contractVersion }}</span>
                </div>

                <dl class="mt-3 grid min-w-0 gap-x-5 gap-y-2 text-xs sm:grid-cols-2 lg:grid-cols-3">
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.version') }}</dt><dd class="break-all font-mono">{{ candidate.artifact?.extensionVersion || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.digest') }}</dt><dd class="break-all font-mono">{{ candidate.artifact?.packageDigest || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.runtime') }}</dt><dd class="break-all font-mono">{{ candidate.artifact?.runtimeInstanceId || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.guard') }}</dt><dd class="break-all font-mono">{{ candidate.guard || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.permission') }}</dt><dd class="break-all font-mono">{{ candidate.permission || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.handler') }}</dt><dd class="break-all font-mono">{{ candidate.handler || candidate.destination || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.fallback') }}</dt><dd>{{ candidate.fallback || '—' }} · {{ candidate.mode || '—' }}</dd></div>
                  <div><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.timeout') }}</dt><dd>{{ candidate.timeoutMs ?? '—' }}</dd></div>
                  <div class="sm:col-span-2 lg:col-span-3"><dt class="text-slate-500">{{ t('admin.extensions.routeProviders.fields.contracts') }}</dt><dd class="break-all font-mono">{{ candidate.requestSchema || '—' }} → {{ candidate.responseSchema || '—' }}</dd></div>
                </dl>

                <div v-if="riskLabels(candidate).length" class="mt-3 border-l-2 border-red-500 pl-3 text-xs text-red-700 dark:text-red-300">
                  <p class="font-semibold">{{ t('admin.extensions.routeProviders.riskTitle') }}</p>
                  <ul class="mt-1 list-disc space-y-1 pl-4">
                    <li v-for="risk in riskLabels(candidate)" :key="risk">{{ risk }}</li>
                  </ul>
                </div>
              </div>
            </div>
          </label>

          <div class="flex flex-col gap-2 pt-1 sm:flex-row sm:items-center sm:justify-between">
            <p class="text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.extensions.routeProviders.casHint', { revision: conflict.selection?.revision ?? 0 }) }}
            </p>
            <UButton
              icon="i-lucide-shield-check"
              color="primary"
              :disabled="!chosenCandidate(conflict)?.artifact || !isActiveSuperAdmin || (conflict.selectionStatus === 'stale' && !conflict.selection)"
              :loading="busyConflict === routeProviderConflictId(conflict.key)"
              @click="applySelection(conflict)"
            >
              {{ t('admin.extensions.routeProviders.selectAction') }}
            </UButton>
          </div>
        </fieldset>
      </article>
    </div>
  </div>
</template>
