<script setup lang="ts">
import { useAdminSurfaces } from '~/composables/admin/useAdminSurfaces'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  adminSurfaceKindIcon,
  normalizeAdminSurfaceOutput,
  resolveAdminSurfacePlacement,
  type AdminSurfaceContract,
  type AdminSurfaceKind,
  type AdminSurfacePrimitive,
  type AdminSurfaceViewModel
} from '~/utils/admin/adminSurfaces'

type ResolvedSurface = {
  surface: AdminSurfaceContract
  view: AdminSurfaceViewModel
}

type SurfaceOutletData = {
  revision: number
  surfaces: AdminSurfaceContract[]
  resolved: ResolvedSurface[]
  failures: Array<{ id: string, label: string, message: string }>
}

const props = withDefaults(defineProps<{
  pageId: string
  kinds?: AdminSurfaceKind[]
  context?: Record<string, unknown>
}>(), {
  kinds: (): AdminSurfaceKind[] => ['notice', 'dashboard', 'form', 'importer', 'exporter'],
  context: () => ({})
})
const emit = defineEmits<{ refresh: [] }>()

const { t, locale } = useI18n()
const route = useRoute()
const adminRoutes = useAdminRoutes()
const toast = useToast()
const { list, invoke } = useAdminSurfaces()
const placement = computed(() => resolveAdminSurfacePlacement(props.pageId))
const contextKey = computed(() => JSON.stringify(props.context))
const kindsKey = computed(() => [...props.kinds].sort().join(','))
const busySurfaceId = ref('')
const formValues = reactive<Record<string, Record<string, AdminSurfacePrimitive>>>(Object.create(null))
const mounted = ref(false)

onMounted(() => {
  // 查询仅在客户端执行；挂载后再展示 loading，避免 SSR 空节点与 hydration 首帧不一致。
  mounted.value = true
})

const asyncKey = computed(() => `admin-surfaces:${placement.value?.id || 'none'}:${kindsKey.value}`)
const { data, pending, error, refresh } = await useAsyncData<SurfaceOutletData>(
  asyncKey,
  async () => loadSurfaces(),
  {
    default: () => ({ revision: 0, surfaces: [], resolved: [], failures: [] }),
    // Surface 查询通过带输入契约的 POST 调用；留到客户端执行，确保先取得 double-submit CSRF。
    server: false,
    watch: [contextKey]
  }
)

const notices = computed(() => resolvedByKind('notice'))
const dashboards = computed(() => resolvedByKind('dashboard'))
const details = computed(() => resolvedByKind('detail_region'))
const forms = computed(() => [
  ...resolvedByKind('form'),
  ...resolvedByKind('importer'),
  ...resolvedByKind('editor_panel')
])
const exporters = computed(() => resolvedByKind('exporter'))
const referencedCommands = computed(() => new Set(data.value.resolved.flatMap(item => item.view.commandSurfaceId || [])))
const standaloneCommands = computed(() => data.value.surfaces.filter(surface =>
  surface.action === 'add' && surface.operation === 'command' && surface.invokable &&
  !referencedCommands.value.has(surface.id) && !['row_action', 'bulk_action'].includes(surface.kind)
))
const hasContent = computed(() => Boolean(
  notices.value.length || dashboards.value.length || details.value.length || forms.value.length ||
  exporters.value.length || standaloneCommands.value.length || data.value.failures.length
))

watch(() => data.value.resolved, (resolved) => {
  for (const item of resolved) {
    if (item.view.fields.length === 0) continue
    const current = formValues[item.surface.id] || Object.create(null)
    for (const field of item.view.fields) {
      if (current[field.key] !== undefined) continue
      const preset = item.view.values[field.key]
      current[field.key] = preset !== undefined ? preset : field.type === 'boolean' ? false : field.type === 'number' ? 0 : ''
    }
    formValues[item.surface.id] = current
  }
}, { immediate: true, deep: true })

async function loadSurfaces(): Promise<SurfaceOutletData> {
  const target = placement.value
  if (!target) return { revision: 0, surfaces: [], resolved: [], failures: [] }
  const catalog = await list(target.id)
  const kinds = new Set(props.kinds)
  const surfaces = catalog.surfaces.filter(surface =>
    surface.placementId === target.id && surface.placementContractVersion === target.contractVersion &&
    kinds.has(surface.kind) && surface.action === 'add' && surface.invokable
  )
  const resolved: ResolvedSurface[] = []
  const failures: SurfaceOutletData['failures'] = []
  await Promise.all(surfaces.filter(surface => surface.operation === 'query').map(async (surface) => {
    try {
      const result = await invoke(surface, invocationInput(surface))
      const view = normalizeAdminSurfaceOutput(result.output)
      if (!view) throw new Error(t('admin.surfaces.invalidResult'))
      resolved.push({ surface: result.surface, view })
    } catch (current) {
      failures.push({ id: surface.id, label: surface.label, message: apiErrorMessage(current) || t('admin.surfaces.loadFailed') })
    }
  }))
  resolved.sort((left, right) => surfaceOrder(left.surface, right.surface))
  failures.sort((left, right) => left.id.localeCompare(right.id))
  return { revision: catalog.revision, surfaces, resolved, failures }
}

function invocationInput(surface: AdminSurfaceContract, extra: Record<string, unknown> = {}) {
  return {
    placementId: placement.value?.id,
    placementContractVersion: placement.value?.contractVersion,
    kind: surface.kind,
    locale: String(locale.value),
    route: {
      path: route.path,
      params: route.params,
      query: route.query
    },
    context: props.context,
    ...extra
  }
}

function resolvedByKind(kind: AdminSurfaceKind) {
  return data.value.resolved.filter(item => item.surface.kind === kind)
}

function pairedCommand(item: ResolvedSurface) {
  const id = item.view.commandSurfaceId
  if (!id) return undefined
  return data.value.surfaces.find(candidate =>
    candidate.id === id && candidate.operation === 'command' && candidate.invokable &&
    candidate.extensionId === item.surface.extensionId && candidate.kind === item.surface.kind &&
    candidate.placementId === item.surface.placementId &&
    candidate.placementContractVersion === item.surface.placementContractVersion
  )
}

async function execute(surface: AdminSurfaceContract, extra: Record<string, unknown> = {}) {
  if (busySurfaceId.value) return
  busySurfaceId.value = surface.id
  try {
    const result = await invoke(surface, invocationInput(surface, extra))
    const view = normalizeAdminSurfaceOutput(result.output)
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: view?.message || t('admin.surfaces.completed', { label: surface.label }),
      duration: 5000
    })
    if (view?.download && import.meta.client) window.location.assign(view.download.url)
    if (view?.refresh) {
      await refresh()
      emit('refresh')
    }
  } catch (current) {
    toast.add({
      color: 'error',
      icon: 'i-lucide-triangle-alert',
      title: apiErrorMessage(current) || t('admin.surfaces.failed', { label: surface.label }),
      // 错误提示不自动消失，操作者要能读到原因。
      duration: 0
    })
  } finally {
    busySurfaceId.value = ''
  }
}

function submitForm(item: ResolvedSurface) {
  const command = pairedCommand(item)
  if (!command) return
  return execute(command, {
    sourceSurfaceId: item.surface.id,
    values: { ...(formValues[item.surface.id] || {}) }
  })
}

function setFieldValue(surfaceId: string, key: string, value: AdminSurfacePrimitive) {
  const values = formValues[surfaceId] || Object.create(null)
  values[key] = value
  formValues[surfaceId] = values
}

function surfaceOrder(left: AdminSurfaceContract, right: AdminSurfaceContract) {
  if (left.priority !== right.priority) return right.priority - left.priority
  if (left.extensionId !== right.extensionId) return left.extensionId.localeCompare(right.extensionId)
  return left.id.localeCompare(right.id)
}

function toneColor(tone: AdminSurfaceViewModel['tone']) {
  return tone === 'neutral' ? 'neutral' : tone
}

function displayValue(value: AdminSurfacePrimitive) {
  if (value === null || value === '') return t('admin.surfaces.emptyValue')
  if (typeof value === 'boolean') return value ? t('admin.surfaces.yes') : t('admin.surfaces.no')
  return String(value)
}
</script>

<template>
  <div v-if="mounted && placement && (pending || error || hasContent)" class="mb-5 flex min-w-0 flex-col gap-4" data-testid="admin-surface-outlet">
    <div v-if="pending && !hasContent" class="rounded-md border border-slate-200 bg-white p-4 dark:border-zinc-800 dark:bg-zinc-900">
      <SFSkeleton :lines="2" />
    </div>

    <UAlert
      v-if="error"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="apiErrorMessage(error) || t('admin.surfaces.loadFailed')"
    />

    <UAlert
      v-for="failure in data.failures"
      :key="failure.id"
      color="error"
      variant="subtle"
      icon="i-lucide-triangle-alert"
      :title="failure.label"
      :description="failure.message"
    />

    <UAlert
      v-for="item in notices"
      :key="item.surface.id"
      :color="toneColor(item.view.tone)"
      variant="subtle"
      :icon="item.view.icon || adminSurfaceKindIcon(item.surface.kind)"
      :title="item.view.title || item.surface.label"
      :description="item.view.message || item.view.description"
    />

    <div v-if="dashboards.length" class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <UCard
        v-for="item in dashboards"
        :key="item.surface.id"
        class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900"
      >
        <div class="flex min-w-0 items-start gap-3">
          <UIcon :name="item.view.icon || adminSurfaceKindIcon(item.surface.kind)" class="mt-0.5 size-5 shrink-0 text-[var(--sf-accent)]" />
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ item.view.title || item.surface.label }}</p>
            <p v-if="item.view.value !== undefined" class="mt-2 break-words text-2xl font-bold text-slate-900 dark:text-white">{{ displayValue(item.view.value) }}</p>
            <p v-if="item.view.description || item.view.message" class="mt-1 text-sm text-slate-500 dark:text-zinc-400">{{ item.view.description || item.view.message }}</p>
            <UButton v-if="item.view.pageId" class="mt-3" size="sm" variant="subtle" :to="adminRoutes.path(item.view.pageId)" trailing-icon="i-lucide-arrow-right">
              {{ t('admin.surfaces.open') }}
            </UButton>
          </div>
        </div>
      </UCard>
    </div>

    <section v-for="item in details" :key="item.surface.id" class="border-y border-slate-200 py-4 dark:border-zinc-800">
      <div class="mb-3 flex items-center gap-2">
        <UIcon :name="item.view.icon || adminSurfaceKindIcon(item.surface.kind)" class="size-4 text-[var(--sf-accent)]" />
        <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ item.view.title || item.surface.label }}</h3>
      </div>
      <dl class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <div v-for="entry in item.view.items" :key="entry.label" class="min-w-0">
          <dt class="text-xs font-medium text-slate-500 dark:text-zinc-400">{{ entry.label }}</dt>
          <dd class="mt-1 break-words text-sm text-slate-900 dark:text-zinc-100">{{ displayValue(entry.value) }}</dd>
        </div>
      </dl>
    </section>

    <div v-if="forms.length" class="grid gap-4 xl:grid-cols-2">
      <UCard v-for="item in forms" :key="item.surface.id" class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
        <form class="flex flex-col gap-4" @submit.prevent="submitForm(item)">
          <div class="flex items-center gap-2">
            <UIcon :name="item.view.icon || adminSurfaceKindIcon(item.surface.kind)" class="size-4 text-[var(--sf-accent)]" />
            <h3 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ item.view.title || item.surface.label }}</h3>
          </div>
          <p v-if="item.view.description" class="text-sm text-slate-500 dark:text-zinc-400">{{ item.view.description }}</p>
          <UFormField v-for="field in item.view.fields" :key="field.key" :label="field.label" :required="field.required">
            <UTextarea
              v-if="field.type === 'textarea'"
              :model-value="String(formValues[item.surface.id]?.[field.key] ?? '')"
              :placeholder="field.placeholder"
              :rows="4"
              class="w-full"
              @update:model-value="setFieldValue(item.surface.id, field.key, String($event))"
            />
            <UCheckbox
              v-else-if="field.type === 'boolean'"
              :model-value="formValues[item.surface.id]?.[field.key] === true"
              @update:model-value="setFieldValue(item.surface.id, field.key, $event === true)"
            />
            <select
              v-else-if="field.type === 'select'"
              :value="String(formValues[item.surface.id]?.[field.key] ?? '')"
              class="h-9 w-full rounded-md border border-slate-200 bg-white px-3 text-sm text-slate-800 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-100"
              @change="setFieldValue(item.surface.id, field.key, ($event.target as HTMLSelectElement).value)"
            >
              <option v-for="option in field.options" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <UInput
              v-else
              :type="field.type === 'number' ? 'number' : 'text'"
              :model-value="String(formValues[item.surface.id]?.[field.key] ?? '')"
              :placeholder="field.placeholder"
              class="w-full"
              @update:model-value="setFieldValue(item.surface.id, field.key, field.type === 'number' ? Number($event) : String($event))"
            />
          </UFormField>
          <div v-if="pairedCommand(item)" class="flex justify-end">
            <UButton type="submit" icon="i-lucide-send" :loading="busySurfaceId === pairedCommand(item)?.id">
              {{ t('admin.surfaces.submit') }}
            </UButton>
          </div>
        </form>
      </UCard>
    </div>

    <div v-if="exporters.length || standaloneCommands.length" class="flex flex-wrap items-center gap-2 border-t border-slate-200 pt-4 dark:border-zinc-800">
      <template v-for="item in exporters" :key="item.surface.id">
        <UButton
          v-if="item.view.download"
          color="neutral"
          variant="outline"
          :icon="item.view.icon || adminSurfaceKindIcon(item.surface.kind)"
          :href="item.view.download.url"
          download
        >
          {{ item.view.title || item.surface.label }}
        </UButton>
      </template>
      <UButton
        v-for="surface in standaloneCommands"
        :key="surface.id"
        color="neutral"
        variant="outline"
        :icon="adminSurfaceKindIcon(surface.kind)"
        :loading="busySurfaceId === surface.id"
        @click="execute(surface)"
      >
        {{ surface.label }}
      </UButton>
    </div>
  </div>
</template>
