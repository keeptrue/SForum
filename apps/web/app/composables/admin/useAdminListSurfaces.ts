import { useAdminSurfaces } from '~/composables/admin/useAdminSurfaces'
import { computed, reactive, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  adminSurfaceKindIcon,
  normalizeAdminSurfaceOutput,
  resolveAdminSurfacePlacement,
  type AdminSurfaceContract,
  type AdminSurfaceTone,
  type AdminSurfaceViewModel
} from '~/utils/admin/adminSurfaces'

export type AdminListSurfaceResource = {
  id: string
  attributes: Record<string, unknown>
}

export type ResolvedAdminListSurface = {
  surface: AdminSurfaceContract
  view: AdminSurfaceViewModel
}

export type AdminListSurfaceAction = {
  surface: AdminSurfaceContract
  source: AdminSurfaceContract
  label: string
  description?: string
  icon: string
  tone: AdminSurfaceTone
}

type AdminListSurfaceOptions = {
  pageId: MaybeRefOrGetter<string>
  resources: MaybeRefOrGetter<AdminListSurfaceResource[]>
  context?: MaybeRefOrGetter<Record<string, unknown>>
  refreshHost?: () => Promise<void> | void
}

const listKinds = new Set(['list_column', 'list_filter', 'row_action', 'bulk_action'])

export function useAdminListSurfaces(options: AdminListSurfaceOptions) {
  const { t, locale } = useI18n()
  const route = useRoute()
  const toast = useToast()
  const { list, invoke } = useAdminSurfaces()
  const placement = computed(() => resolveAdminSurfacePlacement(toValue(options.pageId)))
  const selectedFilters = reactive<Record<string, string>>(Object.create(null))
  const selectedResourceIds = ref<string[]>([])
  const surfaces = ref<AdminSurfaceContract[]>([])
  const resolved = ref<ResolvedAdminListSurface[]>([])
  const failures = ref<Array<{ id: string, label: string, message: string }>>([])
  const pending = ref(false)
  const busySurfaceId = ref('')
  let requestGeneration = 0

  const resources = computed(() => normalizeResources(toValue(options.resources)))
  const resourceKey = computed(() => stableJSON(resources.value))
  const filterKey = computed(() => stableJSON(Object.entries(selectedFilters).sort(([left], [right]) => left.localeCompare(right))))
  const context = computed(() => toValue(options.context) || {})
  const contextKey = computed(() => stableJSON(context.value))

  const columns = computed(() => resolved.value.filter(item => item.surface.kind === 'list_column'))
  const filters = computed(() => resolved.value.filter(item => item.surface.kind === 'list_filter'))
  const failureMessage = computed(() => failures.value.map(item => `${item.label}: ${item.message}`).join('\n'))
  const hasBulkActions = computed(() => surfaces.value.some(surface => surface.kind === 'bulk_action'))
  const visibleResourceIds = computed(() => resources.value
    .map(resource => resource.id)
    .filter(isResourceVisible))
  const allVisibleSelected = computed(() => visibleResourceIds.value.length > 0 &&
    visibleResourceIds.value.every(id => selectedResourceIds.value.includes(id)))
  const bulkActions = computed(() => actionsFor('bulk_action', selectedResourceIds.value))

  watch([resourceKey, filterKey, contextKey, () => String(locale.value)], () => {
    if (import.meta.client) void refreshSurfaces()
  }, { immediate: true })

  watch(visibleResourceIds, (ids) => {
    const visible = new Set(ids)
    selectedResourceIds.value = selectedResourceIds.value.filter(id => visible.has(id))
  })

  async function refreshSurfaces() {
    const target = placement.value
    const generation = ++requestGeneration
    if (!target) {
      surfaces.value = []
      resolved.value = []
      failures.value = []
      return
    }

    pending.value = true
    try {
      const catalog = await list(target.id)
      if (generation !== requestGeneration) return
      const active = catalog.surfaces
        .filter(surface => surface.placementId === target.id &&
          surface.placementContractVersion === target.contractVersion &&
          surface.action === 'add' && surface.invokable && listKinds.has(surface.kind))
        .sort(surfaceOrder)
      const nextResolved: ResolvedAdminListSurface[] = []
      const nextFailures: Array<{ id: string, label: string, message: string }> = []

      await Promise.all(active.filter(surface => surface.operation === 'query').map(async (surface) => {
        try {
          const result = await invoke(surface, queryInput(surface))
          const view = normalizeAdminSurfaceOutput(result.output)
          if (!view) throw new Error(t('admin.surfaces.invalidResult'))
          nextResolved.push({ surface: result.surface, view })
        } catch (current) {
          if (surface.kind === 'list_filter') selectedFilters[surface.id] = ''
          nextFailures.push({
            id: surface.id,
            label: surface.label,
            message: apiErrorMessage(current) || t('admin.surfaces.loadFailed')
          })
        }
      }))
      if (generation !== requestGeneration) return
      surfaces.value = active
      resolved.value = nextResolved.sort((left, right) => surfaceOrder(left.surface, right.surface))
      failures.value = nextFailures.sort((left, right) => left.id.localeCompare(right.id))
      clearUnavailableFilterValues()
    } catch (current) {
      if (generation !== requestGeneration) return
      surfaces.value = []
      resolved.value = []
      failures.value = [{ id: 'catalog', label: t('admin.surfaces.extensions'), message: apiErrorMessage(current) || t('admin.surfaces.loadFailed') }]
    } finally {
      if (generation === requestGeneration) pending.value = false
    }
  }

  function queryInput(surface: AdminSurfaceContract) {
    return {
      placementId: placement.value?.id,
      placementContractVersion: placement.value?.contractVersion,
      kind: surface.kind,
      locale: String(locale.value),
      route: { path: route.path, params: route.params, query: route.query },
      resources: resources.value,
      filters: currentFilters(),
      selection: selectedFilters[surface.id] || '',
      context: context.value
    }
  }

  function currentFilters() {
    return Object.fromEntries(Object.entries(selectedFilters).filter(([, value]) => Boolean(value)))
  }

  function clearUnavailableFilterValues() {
    const available = new Map(filters.value.map(item => [
      item.surface.id,
      new Set(item.view.options.map(option => option.value))
    ]))
    for (const [surfaceId, value] of Object.entries(selectedFilters)) {
      if (value && !available.get(surfaceId)?.has(value)) selectedFilters[surfaceId] = ''
    }
  }

  function setFilter(surfaceId: string, value: string) {
    const filter = filters.value.find(item => item.surface.id === surfaceId)
    if (!filter || (value && !filter.view.options.some(option => option.value === value))) return
    selectedFilters[surfaceId] = value
  }

  function filterValue(surfaceId: string) {
    return selectedFilters[surfaceId] || ''
  }

  function isResourceVisible(resourceId: string) {
    const active = filters.value.filter(item => Boolean(selectedFilters[item.surface.id]))
    if (active.length === 0) return true
    return active.every((item) => {
      if (!item.view.visibleResourceIdsDeclared) return false
      return item.view.visibleResourceIds.includes(resourceId)
    })
  }

  function columnValue(item: ResolvedAdminListSurface, resourceId: string) {
    const value = item.view.cells[resourceId]
    if (value === undefined || value === null || value === '') return t('admin.surfaces.emptyValue')
    if (typeof value === 'boolean') return value ? t('admin.surfaces.yes') : t('admin.surfaces.no')
    return String(value)
  }

  function rowActionsFor(resourceId: string) {
    return actionsFor('row_action', [resourceId])
  }

  function actionsFor(kind: 'row_action' | 'bulk_action', resourceIds: string[]) {
    const descriptors = resolved.value.filter(item => item.surface.kind === kind)
    const referenced = new Set(descriptors.flatMap(item => item.view.commandSurfaceId || []))
    const queryOwners = new Set(surfaces.value
      .filter(surface => surface.kind === kind && surface.operation === 'query')
      .map(surface => surface.extensionId))
    const result: AdminListSurfaceAction[] = []

    for (const item of descriptors) {
      const command = pairedCommand(item)
      if (!command || !resourcesAreVisible(item.view, resourceIds)) continue
      result.push({
        surface: command,
        source: item.surface,
        label: item.view.title || item.surface.label,
        description: item.view.description,
        icon: item.view.icon || adminSurfaceKindIcon(kind),
        tone: item.view.tone
      })
    }
    for (const command of surfaces.value) {
      if (command.kind !== kind || command.operation !== 'command' || referenced.has(command.id) || queryOwners.has(command.extensionId)) continue
      result.push({
        surface: command,
        source: command,
        label: command.label,
        icon: adminSurfaceKindIcon(kind),
        tone: 'neutral'
      })
    }
    const seen = new Set<string>()
    return result
      .filter((item) => {
        if (seen.has(item.surface.id)) return false
        seen.add(item.surface.id)
        return true
      })
      .sort((left, right) => surfaceOrder(left.source, right.source))
  }

  function pairedCommand(item: ResolvedAdminListSurface) {
    const id = item.view.commandSurfaceId
    if (!id) return undefined
    return surfaces.value.find(candidate => candidate.id === id && candidate.operation === 'command' &&
      candidate.extensionId === item.surface.extensionId && candidate.kind === item.surface.kind &&
      candidate.placementId === item.surface.placementId &&
      candidate.placementContractVersion === item.surface.placementContractVersion)
  }

  function resourcesAreVisible(view: AdminSurfaceViewModel, resourceIds: string[]) {
    if (!view.visibleResourceIdsDeclared) return true
    const visible = new Set(view.visibleResourceIds)
    return resourceIds.every(id => visible.has(id))
  }

  function toggleResource(resourceId: string, selected: boolean) {
    const next = new Set(selectedResourceIds.value)
    if (selected) next.add(resourceId)
    else next.delete(resourceId)
    selectedResourceIds.value = [...next].sort()
  }

  function selectAllVisible(selected: boolean) {
    const visible = new Set(visibleResourceIds.value)
    if (selected) selectedResourceIds.value = [...visible].sort()
    else selectedResourceIds.value = selectedResourceIds.value.filter(id => !visible.has(id))
  }

  async function executeAction(action: AdminListSurfaceAction, resourceIds: string[]) {
    if (busySurfaceId.value || resourceIds.length === 0) return
    const selected = new Set(resourceIds)
    busySurfaceId.value = action.surface.id
    try {
      const result = await invoke(action.surface, {
        placementId: placement.value?.id,
        placementContractVersion: placement.value?.contractVersion,
        kind: action.surface.kind,
        locale: String(locale.value),
        route: { path: route.path, params: route.params, query: route.query },
        resourceIds,
        resources: resources.value.filter(resource => selected.has(resource.id)),
        filters: currentFilters(),
        context: context.value
      })
      const view = normalizeAdminSurfaceOutput(result.output)
      toast.add({
        color: 'primary',
        icon: 'i-lucide-check',
        title: view?.message || t('admin.surfaces.completed', { label: action.label }),
        duration: 5000
      })
      if (view?.refresh) {
        await options.refreshHost?.()
        await refreshSurfaces()
      }
      if (action.surface.kind === 'bulk_action') selectedResourceIds.value = []
    } catch (current) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: apiErrorMessage(current) || t('admin.surfaces.failed', { label: action.label }),
        // 错误提示不自动消失，操作者要能读到原因。
        duration: 0
      })
    } finally {
      busySurfaceId.value = ''
    }
  }

  return {
    columns,
    filters,
    pending,
    busySurfaceId,
    failureMessage,
    hasBulkActions,
    bulkActions,
    selectedResourceIds,
    visibleResourceIds,
    allVisibleSelected,
    setFilter,
    filterValue,
    isResourceVisible,
    columnValue,
    rowActionsFor,
    toggleResource,
    selectAllVisible,
    executeAction,
    refreshSurfaces
  }
}

function normalizeResources(resources: AdminListSurfaceResource[] | undefined) {
  if (!Array.isArray(resources)) return []
  const seen = new Set<string>()
  return resources.slice(0, 1000).flatMap((resource) => {
    const id = String(resource?.id || '').trim()
    if (!id || id.length > 160 || seen.has(id) || !resource.attributes ||
      typeof resource.attributes !== 'object' || Array.isArray(resource.attributes)) return []
    seen.add(id)
    return [{ id, attributes: { ...resource.attributes } }]
  })
}

function stableJSON(value: unknown) {
  try {
    return JSON.stringify(value)
  } catch {
    return ''
  }
}

function surfaceOrder(left: AdminSurfaceContract, right: AdminSurfaceContract) {
  if (left.priority !== right.priority) return right.priority - left.priority
  if (left.extensionId !== right.extensionId) return left.extensionId.localeCompare(right.extensionId)
  return left.id.localeCompare(right.id)
}
