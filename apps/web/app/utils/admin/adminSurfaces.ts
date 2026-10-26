import { adminSurfacePlacements, type AdminSurfacePlacement } from '~/config/adminSurfaceCatalog.gen'
import { HOST_ICON_NAME_PATTERN } from '../iconPicker'

export const adminSurfaceKinds = [
  'navigation',
  'dashboard',
  'list_column',
  'list_filter',
  'row_action',
  'bulk_action',
  'form',
  'notice',
  'editor_panel',
  'detail_region',
  'importer',
  'exporter'
] as const

export type AdminSurfaceKind = typeof adminSurfaceKinds[number]
export type AdminSurfaceAction = 'add' | 'before' | 'after' | 'wrap' | 'replace' | 'hide' | 'filter'
export type AdminSurfaceOperation = 'query' | 'command'
export type AdminSurfaceTone = 'neutral' | 'primary' | 'success' | 'warning' | 'error'
export type AdminSurfacePrimitive = string | number | boolean | null

export type AdminSurfaceContract = {
  id: string
  contractVersion: string
  extensionId: string
  extensionVersion: string
  kind: AdminSurfaceKind
  action: AdminSurfaceAction
  targetId?: string
  placementId?: string
  placementContractVersion?: string
  label: string
  propsSchema?: string
  propsSchemaDigest?: string
  resultSchema?: string
  resultSchemaDigest?: string
  operation: AdminSurfaceOperation
  schema?: string
  schemaDigest?: string
  priority: number
  invokable: boolean
}

export type AdminSurfaceCatalog = {
  revision: number
  surfaces: AdminSurfaceContract[]
}

/**
 * 列表行上的只读徽章：来自 `list_column` surface 的单元格，
 * 链接目标来自同一份输出的 `links[resourceId]`。
 */
export type AdminListBadge = {
  id: string
  label: string
  title: string
  tone: AdminSurfaceTone
  icon?: string
  pageId?: string
}

/** `resolveAdminListBadges` 只需要已解析的 surface + view，避免反向依赖 composable 类型。 */
export type AdminListBadgeSource = {
  surface: AdminSurfaceContract
  view: AdminSurfaceViewModel
}

export type AdminSurfaceInvocation = {
  surface: AdminSurfaceContract
  output: Record<string, unknown>
}

export type AdminSurfaceOption = {
  label: string
  value: string
}

export type AdminSurfaceField = {
  key: string
  label: string
  type: 'text' | 'textarea' | 'number' | 'boolean' | 'select'
  required: boolean
  placeholder?: string
  options: AdminSurfaceOption[]
}

export type AdminSurfaceViewModel = {
  title?: string
  description?: string
  message?: string
  value?: AdminSurfacePrimitive
  tone: AdminSurfaceTone
  icon?: string
  pageId?: string
  commandSurfaceId?: string
  items: Array<{ label: string, value: AdminSurfacePrimitive }>
  cells: Record<string, AdminSurfacePrimitive>
  /** resourceId → 后台相对页面路径；徽章/单元格可据此深链到宿主页面。 */
  links: Record<string, string>
  options: AdminSurfaceOption[]
  visibleResourceIds: string[]
  visibleResourceIdsDeclared: boolean
  fields: AdminSurfaceField[]
  values: Record<string, AdminSurfacePrimitive>
  refresh: boolean
  download?: { url: string, filename?: string }
}

// 图标名校验复用宿主唯一的一份语法，避免 surface、贡献点与插件 bridge 三处漂移。
const iconPattern = HOST_ICON_NAME_PATTERN
const fieldKeyPattern = /^[a-z0-9][a-z0-9._-]{0,79}$/
const surfaceIdPattern = /^[a-z0-9][a-z0-9._-]{1,160}$/
const tones = new Set<AdminSurfaceTone>(['neutral', 'primary', 'success', 'warning', 'error'])
const fieldTypes = new Set<AdminSurfaceField['type']>(['text', 'textarea', 'number', 'boolean', 'select'])

export function resolveAdminSurfacePlacement(pageId: string): AdminSurfacePlacement | undefined {
  const normalized = normalizeAdminSurfaceRoute(pageId)
  const placementRoutes = normalized === '/admin/attachments/manager'
    ? [normalized, '/admin/attachments']
    : [normalized]
  for (const route of placementRoutes) {
    const exact = adminSurfacePlacements.find(item => item.route === route)
    if (exact) return exact
  }

  return adminSurfacePlacements.find(item => routePatternMatches(item.route, normalized))
}

export function adminSurfacePlacementPageId(placementId?: string) {
  const placement = adminSurfacePlacements.find(item => item.id === placementId)
  if (!placement || placement.route.includes(':')) return undefined
  if (placement.id === 'core.component.page.admin.attachments') return '/attachments/manager'
  return placement.route === '/admin' ? '/' : placement.route.replace(/^\/admin/, '') || '/'
}

export function normalizeAdminSurfaceOutput(output: unknown): AdminSurfaceViewModel | null {
  if (!isRecord(output)) return null
  const tone = typeof output.tone === 'string' && tones.has(output.tone as AdminSurfaceTone)
    ? output.tone as AdminSurfaceTone
    : 'neutral'
  const icon = boundedString(output.icon, 80)
  const pageId = safeAdminPageId(output.pageId)
  const result: AdminSurfaceViewModel = {
    title: boundedString(output.title, 160),
    description: boundedString(output.description, 1000),
    message: boundedString(output.message, 1000),
    value: primitive(output.value),
    tone,
    icon: iconPattern.test(icon || '') ? icon : undefined,
    pageId,
    commandSurfaceId: safeSurfaceId(output.commandSurfaceId),
    items: normalizeItems(output.items),
    cells: normalizePrimitiveRecord(output.cells, 1000),
    links: normalizeResourceLinks(output.links),
    options: normalizeOptions(output.options),
    visibleResourceIds: normalizeResourceIds(output.visibleResourceIds),
    visibleResourceIdsDeclared: Array.isArray(output.visibleResourceIds),
    fields: normalizeFields(output.fields),
    values: normalizePrimitiveRecord(output.values, 100),
    refresh: output.refresh === true,
    download: normalizeDownload(output.download)
  }
  if (result.title === undefined && result.description === undefined && result.message === undefined && result.value === undefined &&
    result.icon === undefined && result.pageId === undefined && result.commandSurfaceId === undefined &&
    result.items.length === 0 && Object.keys(result.cells).length === 0 && Object.keys(result.links).length === 0 &&
    result.options.length === 0 && !result.visibleResourceIdsDeclared && result.fields.length === 0 &&
    Object.keys(result.values).length === 0 && !result.refresh && result.download === undefined) {
    return null
  }
  return result
}

export function adminSurfaceIdempotencyKey(surfaceId: string) {
  const prefix = surfaceId.replace(/[^a-zA-Z0-9._-]/g, '').slice(0, 48) || 'admin-surface'
  const cryptoValue = globalThis.crypto?.randomUUID?.()
  if (cryptoValue) return `${prefix}:${cryptoValue}`.slice(0, 128)
  return `${prefix}:${Date.now().toString(36)}:${Math.random().toString(36).slice(2)}`.slice(0, 128)
}

export function adminSurfaceKindIcon(kind: AdminSurfaceKind) {
  const icons: Record<AdminSurfaceKind, string> = {
    navigation: 'i-lucide-panel-left',
    dashboard: 'i-lucide-layout-dashboard',
    list_column: 'i-lucide-columns-3',
    list_filter: 'i-lucide-list-filter',
    row_action: 'i-lucide-mouse-pointer-click',
    bulk_action: 'i-lucide-list-checks',
    form: 'i-lucide-notebook-pen',
    notice: 'i-lucide-info',
    editor_panel: 'i-lucide-panel-right',
    detail_region: 'i-lucide-rows-3',
    importer: 'i-lucide-file-up',
    exporter: 'i-lucide-file-down'
  }
  return icons[kind]
}

/**
 * 把 `list_column` surface 在某个资源上的单元格折叠成徽章。
 * 只接受精确放置的 add + invokable 列；空单元格、不可见资源直接跳过，
 * 因为宿主不能替扩展把“没有值”渲染成“没有更新”。
 */
export function resolveAdminListBadges(
  resolved: readonly AdminListBadgeSource[],
  resourceId: string
): AdminListBadge[] {
  const id = String(resourceId || '').trim()
  if (!id) return []
  const result: AdminListBadge[] = []
  for (const item of resolved) {
    const { surface, view } = item
    if (surface.kind !== 'list_column' || surface.action !== 'add' || !surface.invokable) continue
    if (view.visibleResourceIdsDeclared && !view.visibleResourceIds.includes(id)) continue
    const cell = view.cells[id]
    if (cell === undefined || cell === null || cell === '') continue
    const label = String(cell).trim()
    if (!label) continue
    // 资源 ID 是不透明字符串，可能撞上 Object.prototype 的键（constructor/toString）。
    // 只认自有属性，否则 pageId 会拿到函数并一路炸到 adminRoutes.path()。
    const linked = Object.hasOwn(view.links, id) ? view.links[id] : undefined
    result.push({
      id: surface.id,
      label,
      title: view.title || surface.label || label,
      tone: view.tone,
      icon: view.icon,
      pageId: typeof linked === 'string' ? linked : undefined
    })
  }
  return result
}

function normalizeAdminSurfaceRoute(pageId: string) {
  const path = String(pageId || '').trim().split(/[?#]/, 1)[0] || '/'
  if (path === '/admin' || path.startsWith('/admin/')) return path.replace(/\/$/, '') || '/admin'
  const suffix = path === '/' ? '' : `/${path.replace(/^\/+|\/+$/g, '')}`
  return `/admin${suffix}`
}

function routePatternMatches(pattern: string, route: string) {
  if (!pattern.includes(':')) return false
  const expression = pattern
    .split('/')
    .map((segment) => {
      if (segment.startsWith(':') && segment.endsWith('*')) return '.+'
      if (segment.startsWith(':')) return '[^/]+'
      return segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    })
    .join('/')
  return new RegExp(`^${expression}$`).test(route)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function boundedString(value: unknown, maximum: number) {
  if (typeof value !== 'string') return undefined
  const normalized = value.trim()
  return normalized && normalized.length <= maximum ? normalized : undefined
}

function primitive(value: unknown): AdminSurfacePrimitive | undefined {
  if (value === null || typeof value === 'boolean') return value
  if (typeof value === 'number' && Number.isFinite(value)) return value
  return boundedString(value, 1000)
}

function safeAdminPageId(value: unknown) {
  const pageId = boundedString(value, 512)
  if (!pageId || !pageId.startsWith('/') || pageId.startsWith('//') || pageId.includes('\\') || /[\u0000-\u001f]/.test(pageId)) {
    return undefined
  }
  return pageId
}

function safeSurfaceId(value: unknown) {
  const id = boundedString(value, 160)
  return id && surfaceIdPattern.test(id) ? id : undefined
}

function normalizeItems(value: unknown) {
  if (!Array.isArray(value)) return []
  return value.slice(0, 100).flatMap((item) => {
    if (!isRecord(item)) return []
    const label = boundedString(item.label, 160)
    const itemValue = primitive(item.value)
    return label && itemValue !== undefined ? [{ label, value: itemValue }] : []
  })
}

function normalizePrimitiveRecord(value: unknown, maximum: number) {
  if (!isRecord(value)) return {}
  const result: Record<string, AdminSurfacePrimitive> = {}
  for (const [key, item] of Object.entries(value).slice(0, maximum)) {
    const normalized = primitive(item)
    if (fieldKeyPattern.test(key) && normalized !== undefined) result[key] = normalized
  }
  return result
}

function normalizeOptions(value: unknown): AdminSurfaceOption[] {
  if (!Array.isArray(value)) return []
  return value.slice(0, 100).flatMap((item) => {
    if (!isRecord(item)) return []
    const label = boundedString(item.label, 160)
    const optionValue = boundedString(item.value, 160)
    return label && optionValue ? [{ label, value: optionValue }] : []
  })
}

function normalizeResourceIds(value: unknown) {
  if (!Array.isArray(value)) return []
  return [...new Set(value.slice(0, 1000).flatMap(item => boundedString(item, 160) || []))]
}

// 资源深链和 pageId 同源：必须是后台相对路径，任何协议、协议相对地址或
// 控制字符都在宿主边界丢弃，扩展不能借徽章把管理员带去站外。
function normalizeResourceLinks(value: unknown) {
  if (!isRecord(value)) return {}
  const result: Record<string, string> = {}
  for (const [key, item] of Object.entries(value).slice(0, 1000)) {
    const resourceId = boundedString(key, 160)
    if (!resourceId) continue
    const pageId = safeAdminPageId(item)
    if (!pageId) continue
    result[resourceId] = pageId
  }
  return result
}

function normalizeFields(value: unknown): AdminSurfaceField[] {
  if (!Array.isArray(value)) return []
  const seen = new Set<string>()
  return value.slice(0, 50).flatMap((item) => {
    if (!isRecord(item)) return []
    const key = boundedString(item.key, 80)
    const label = boundedString(item.label, 160)
    const type = boundedString(item.type, 20) as AdminSurfaceField['type'] | undefined
    if (!key || !fieldKeyPattern.test(key) || seen.has(key) || !label || !type || !fieldTypes.has(type)) return []
    seen.add(key)
    return [{
      key,
      label,
      type,
      required: item.required === true,
      placeholder: boundedString(item.placeholder, 240),
      options: type === 'select' ? normalizeOptions(item.options) : []
    }]
  })
}

function normalizeDownload(value: unknown) {
  if (!isRecord(value)) return undefined
  const url = boundedString(value.url, 2048)
  if (!url || !url.startsWith('/') || url.startsWith('//') || url.includes('\\') || /[\u0000-\u001f]/.test(url)) return undefined
  return { url, filename: boundedString(value.filename, 255) }
}
