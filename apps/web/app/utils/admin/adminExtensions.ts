// 本文件是扩展管理的前端契约中心：类型、纯函数与展示规则。为避免单文件越过
// architecture-boundaries 的 1000 行闸门，两组内聚的纯逻辑已抽到同目录模块，
// 并在此**原样 re-export**，因此所有既有 `~/utils/admin/adminExtensions` 的
// import 路径、导出名与类型形状都不变。
//
// - `./adminExtensionsLocalization`：manifest 文案的本地化合并（含 langs 解析）
// - `./adminExtensionsEvents`：事件/投递的合并与分页
//
// 两个模块只 `import type` 本文件，运行期依赖保持单向。
export * from './adminExtensionsLocalization'
export * from './adminExtensionsEvents'

// `export *` 只做再导出，不会在当前模块作用域里引入绑定。本文件内部仍要调用
// 其中的展示函数（extensionSettingDeclarations / extensionAdminPages），因此
// 显式再 import 一次。方向仍是单向：对方只 import type 本文件。
import { extensionDisplayName, extensionLocalizedDisplay } from './adminExtensionsLocalization'

export type AdminExtensionType = 'plugin' | 'theme'
export type AdminExtensionStatus = 'installed' | 'enabled' | 'disabled'
export type AdminExtensionSource = 'builtin' | 'uploaded'
export type AdminExtensionArtifactState = 'available' | 'missing'
export type AdminMissingArtifactDataMode = 'preserve' | 'discard_settings'
export type AdminThemeActionState = 'active' | 'activateDefault' | 'activate'
export type AdminRuntimeState = 'stopped' | 'starting' | 'running' | 'degraded' | 'failed'
export type AdminExtensionEventKind = 'observe' | 'validate' | 'filter'
export type AdminExtensionDeliveryStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'skipped'
export type AdminContributionPayloadType = 'extensionRoute'

export type AdminExtensionSetting = {
  key: string
  label: string
  description?: string
  type: string
  default?: string
  placeholder?: string
  recommendedValue?: string
  required?: boolean
  /** Schema UI control width: default (capped) or full (fill column). */
  width?: 'default' | 'full' | string
  group?: string
  groupId?: string
  column?: number
  options?: Array<{ value: string, label: string, description?: string }>
}

export type AdminExtensionAuthor = {
  name: string
  url?: string
  email?: string
}

// 可选本地化覆盖；字段有值才覆盖顶层默认英文。
export type AdminExtensionLocale = {
  name?: string
  description?: string
  url?: string
  author?: Partial<AdminExtensionAuthor>
}

export type AdminExtensionAdminPage = {
  path: string
  label: string
  description?: string
  icon?: string
  view?: 'about' | 'settings' | 'component'
  menu?: boolean
  order?: number
  permission?: string
  component?: {
    id: string
    apiVersion: number
    entry: string
    css?: string
  }
  /** 宿主为该管理页提供的动作 ID（首版仅 extension.install）。 */
  hostActions?: string[]
}

export type AdminExtensionAdmin = {
  entry?: string
  pages?: AdminExtensionAdminPage[]
}

export type AdminManifestSettingsDocument = {
  schemaVersion: number
  ui: {
    mode: 'schema' | 'component'
    layout: 'form' | 'tabs'
    component?: { id: string, apiVersion: number, entry?: string, css?: string }
  }
  fields: AdminExtensionSetting[]
  actions?: Array<{ id: string, kind: string }>
}

export type AdminExtensionManifest = {
  id: string
  name: string
  description: string
  url: string
  author: AdminExtensionAuthor
  version: string
  type: AdminExtensionType
  sforumVersion: string
  // 可选；未声明时直接使用顶层英文文案，无需实现翻译。
  langs?: Record<string, AdminExtensionLocale>
  permissions?: string[]
  /** Host capability keys declared by the plugin (F2.1). */
  capabilities?: string[]
  settings?: AdminExtensionSetting[] | AdminManifestSettingsDocument
  migrations?: Array<{ path: string }>
  backend?: { entry?: string, rpc?: string, protocolVersion?: number }
  lifecycle?: { contractVersion?: string }
  admin?: AdminExtensionAdmin
  adminPages?: AdminExtensionAdminPage[]
  routes?: Array<{ path: string, methods?: string[], access?: 'public' | 'login' | 'permission', permission?: string, timeoutMs?: number }>
  hooks?: Array<{ name: string }>
  events?: Array<{ name: string, kind?: AdminExtensionEventKind, timeoutMs?: number }>
  jobs?: Array<{ name: string }>
  providers?: Array<{ slot: string, label: string, timeoutMs?: number, multiInstance?: boolean }>
  contributions?: AdminManifestContribution[]
}

export type AdminManifestContribution = {
  point: string
  id: string
  order?: number
  label?: Record<string, string>
  icon?: string
  /** 非空时贡献受同扩展 boolean 设置门控（与宿主 EffectiveContributions 一致）。 */
  enabledBySetting?: string
  payload?: AdminContributionPayload
}

export type AdminContributionPayload = {
  type?: AdminContributionPayloadType | string
  method?: string
  path?: string
  confirm?: boolean
  [key: string]: unknown
}

export type AdminExtensionNavigationItem = {
  extensionId: string
  extensionName: string
  extensionType: AdminExtensionType
  extensionStatus: AdminExtensionStatus
  path: string
  label: string
  description: string
  icon: string
  view: 'about' | 'settings'
  /** 非空时，同组的页签在侧栏合并成一个独立的可展开一级菜单。 */
  group?: string
  order: number
}

/** 一个扩展用 `group` 声明出来的侧栏一级菜单。 */
export type AdminExtensionNavigationGroup = {
  extensionId: string
  label: string
  icon: string
  order: number
  pages: AdminExtensionNavigationItem[]
}

/**
 * 按 `group` 把扩展菜单页签收成一级菜单；未声明 group 的页签不参与分组，
 * 继续由调用方平铺到「扩展管理」里。组内顺序取 order 升序，组间同理。
 */
export function groupExtensionNavigation(items: AdminExtensionNavigationItem[]): AdminExtensionNavigationGroup[] {
  const groups = new Map<string, AdminExtensionNavigationGroup>()
  for (const item of items) {
    const label = (item.group || '').trim()
    if (!label) {
      continue
    }
    const key = `${item.extensionId}::${label}`
    const existing = groups.get(key)
    if (existing) {
      existing.pages.push(item)
      existing.order = Math.min(existing.order, item.order || 0)
      if (!existing.icon) {
        existing.icon = item.icon || ''
      }
      continue
    }
    groups.set(key, {
      extensionId: item.extensionId,
      label,
      icon: item.icon || '',
      order: item.order || 0,
      pages: [item]
    })
  }
  for (const group of groups.values()) {
    group.pages.sort((left, right) => ((left.order || 0) - (right.order || 0)) || left.path.localeCompare(right.path))
  }
  return [...groups.values()].sort((left, right) => (left.order - right.order) || left.label.localeCompare(right.label))
}

/** 没声明 group 的页签：保持旧的平铺行为。 */
export function ungroupedExtensionNavigation(items: AdminExtensionNavigationItem[]): AdminExtensionNavigationItem[] {
  return items.filter(item => !(item.group || '').trim())
}

/** 扩展页签在后台路由里的稳定 pageId 形状，供侧栏与页签共用。 */
export function adminExtensionNavPageId(item: Pick<AdminExtensionNavigationItem, 'extensionId' | 'path'>): string {
  return extensionAdminPageRoute(item.extensionId, item.path)
}

/** 声明了 group 的页由 Core 分区或自有分组承载，不再算「扩展管理」的子页。 */
export function extensionNavPageIdsWithOwnMenu(items: AdminExtensionNavigationItem[]): Set<string> {
  const ids = new Set<string>()
  for (const item of items) {
    // 判定必须与 groupExtensionNavigation 一致，避免两处漂移。
    if ((item.group || '').trim()) {
      ids.add(adminExtensionNavPageId(item))
    }
  }
  return ids
}

/** 该扩展页未声明 group，因此由「扩展管理」承载；group 页有自己的分区或分组。 */
export function extensionPageSitsUnderExtensionsFolder(
  items: AdminExtensionNavigationItem[],
  pageId?: string | null
): boolean {
  return !extensionNavPageIdsWithOwnMenu(items).has(`${pageId || ''}`.trim())
}

export type AdminExtensionSettingValue = {
  key: string
  label: string
  description?: string
  type: string
  default: string
  value: string
  secretSet?: boolean
  placeholder?: string
  recommendedValue?: string
  required?: boolean
  /** Schema UI control width: default (capped) or full (fill column). */
  width?: 'default' | 'full' | string
  group?: string
  groupId?: string
  column?: number
  options?: Array<{ value: string, label: string, description?: string }>
}

export type AdminExtensionSettingsRenderer = {
  mode: 'schema' | 'component'
  layout: 'form' | 'tabs'
	source: 'document' | 'legacy_array'
  fallback: 'schema'
  component?: {
    id: string
		kind: 'prebuilt'
    apiVersion: number
    entry?: string
    css?: string
  }
}

export type AdminExtensionSettingsTab = {
  id: string
  label: string
  description?: string
  groups?: string[]
}

export type AdminExtensionSettingsGroup = {
  id: string
  label: string
  description?: string
  columns?: number
}

export type AdminExtensionSettingsCallout = {
  id: string
  tone: string
  title: string
  body?: string
  linkLabel?: string
  linkUrl?: string
  tab?: string
  group?: string
}

export type AdminExtensionSettingsAction = {
  id: string
  kind: 'provider_probe' | 'ai_tool_probe'
  label: string
  description?: string
  placement: 'header' | 'footer'
  useDraftValues: boolean
  fields?: string[]
  /** ai_tool_probe 调用的 AI 工具声明 id；其它 kind 不返回。 */
  tool?: string
  available: boolean
  unavailableReason?: string
}

export type AdminExtensionSettingsActionResult = {
  success: boolean
  reason: string
  message: string
  details?: Record<string, string>
  suggestions?: string[]
  durationMs: number
}

export type AdminExtensionSettings = {
  extensionId: string
  extensionType: AdminExtensionType
  extensionVersion: string
  extensionStatus: AdminExtensionStatus
  renderer: AdminExtensionSettingsRenderer
  tabs?: AdminExtensionSettingsTab[]
  groups?: AdminExtensionSettingsGroup[]
  callouts?: AdminExtensionSettingsCallout[]
  items: AdminExtensionSettingValue[]
  actions?: AdminExtensionSettingsAction[]
}

export function recommendedExtensionSettingValues(items: AdminExtensionSettingValue[]) {
  return Object.fromEntries(items
    .filter(item => item.type !== 'secret')
    .map(item => [item.key, item.recommendedValue ?? item.default]))
}

export type AdminExtensionRuntime = {
  state: AdminRuntimeState
  lastError?: string
  startedAt?: string
  routeCount: number
  hookCount: number
  eventCount?: number
  providerCount: number
  /** F2.3 resilience observability */
  circuitOpen?: boolean
  circuitOpenUntil?: string
  consecutiveFailures?: number
  lastFailureReason?: string
  lastFailureAt?: string
  activeRpcCalls?: number
  maxConcurrentRpc?: number
  /** P3 protocol migration telemetry */
  protocolVersion?: 1 | 2
  protocolTransport?: 'net/rpc' | 'grpc'
  protocolDeprecated?: boolean
  protocolStartCount?: number
  protocolCallCount?: number
  protocolLastCallAt?: string
  /** 拥有 backend 子进程时的 OS RSS（字节）；未采样或无进程时省略 */
  memoryBytes?: number
}

/** 插件进程 RSS 展示：不足 1 MiB 用 KiB，否则整数 MiB。 */
export function formatPluginMemoryBytes(bytes: number) {
  const value = Math.max(0, Number(bytes) || 0)
  if (value < 1024 * 1024) {
    return `${Math.max(1, Math.round(value / 1024))} KiB`
  }
  return `${Math.round(value / (1024 * 1024))} MiB`
}

export type AdminCapabilityRisk = 'low' | 'medium' | 'high'

/** 启用审查用的有效 Host 能力（显式 + 宿主推断）。 */
export type AdminCapabilityGrant = {
  key: string
  risk: AdminCapabilityRisk
  labelZh: string
  labelEn: string
  description: string
  implied?: boolean
}

export type AdminExtensionVersion = {
  version: string
  manifest: AdminExtensionManifest
  artifactState?: AdminExtensionArtifactState
  packageDigest: string
  adminFrontendDigest: string
  packagePath: string
  installedAt: string
}

export type AdminExtension = {
  id: string
  name: string
  version: string
  type: AdminExtensionType
  status: AdminExtensionStatus
  source?: AdminExtensionSource
  isSystem?: boolean
  isDeletable?: boolean
  manifest: AdminExtensionManifest
  /** F2.1 有效能力列表，启用前需运营确认。 */
  capabilityGrants?: AdminCapabilityGrant[]
  runtime?: AdminExtensionRuntime
  artifactState?: AdminExtensionArtifactState
  /** 存在「台账已 succeeded 但宿主物理清理未完成」的 pending 卸载 tombstone。 */
  pendingUninstallCleanup?: boolean
  packageDigest: string
  adminFrontendDigest?: string
  packagePath: string
  stagedVersion?: AdminExtensionVersion
  installedAt: string
  updatedAt: string
}

export type AdminExtensionInstallResult = {
  extension: AdminExtension
  upgraded: boolean
  previousVersion?: string
  previousDigest?: string
  trustRevoked?: boolean
  requiredReEnable?: boolean
  activationPending?: boolean
}

export type AdminMissingArtifactCleanupResult = {
  removed: Array<{
    extensionId: string
    name: string
    type: AdminExtensionType
    version: string
    packageDigest: string
    dataMode: AdminMissingArtifactDataMode
    retainSettings: boolean
    businessDataKept: boolean
  }>
}

/** 与宿主 ManifestAffectsPublicSurface 对齐：设置保存后应提示刷新帖子页并 bump 缓存键。 */
const publicForumContributionPoints = new Set([
  'forum.topic.badges',
  'forum.topic.sidebar',
  'forum.topic.list.badges',
  'forum.nav.items'
])

export function extensionAffectsPublicSurface(extension: Pick<AdminExtension, 'manifest'> | null | undefined): boolean {
  const contributions = extension?.manifest?.contributions
  if (!contributions?.length) {
    return false
  }
  return contributions.some((item) => {
    if (String(item.enabledBySetting || '').trim()) {
      return true
    }
    return publicForumContributionPoints.has(String(item.point || '').trim())
  })
}

/** 与宿主 FrontendStatus 对齐：预构建管理组件的精确 digest 与信任结论。 */
export type AdminFrontendStatus = {
  extensionId: string
  kind: 'none' | 'prebuilt_component'
  trustState: string
  digest?: string
  component?: { id: string, apiVersion: number, entry: string, css?: string }
  components?: Array<{ id: string, apiVersion: number, entry: string, css?: string }>
}

export type AdminExtensionPageBootstrap = {
  extension: AdminExtension
  page: AdminExtensionAdminPage | null
  settings: AdminExtensionSettings | null
  /**
   * 视图声明为 component/settings 时随 bootstrap 一并返回的信任结论。
   * 管理端是 SSR，缺少它时"信任状态未知"会被首帧渲染成"未授权"。
   */
  frontend: AdminFrontendStatus | null
  /**
   * 宿主构建标识。coreVersion 是兼容判定唯一权威；buildVersion 只用于
   * 识别开发版与排障。旧宿主缺该字段，插件必须按「未知」处理。
   */
  host?: {
    coreVersion: string
    buildVersion: string
    valid: boolean
  } | null
}

export type AdminLifecycleRemovalMode = 'preserve' | 'export_then_remove' | 'complete_removal'
export const RECOMMENDED_LIFECYCLE_REMOVAL_MODE: AdminLifecycleRemovalMode = 'preserve'
export type AdminLifecycleRecoveryDecision = 'retry' | 'skip_step'
export type AdminLifecycleTerminalResult = 'succeeded' | 'failed' | 'cancelled' | 'skipped'

export type AdminLifecyclePublicError = {
  code?: string
  reason?: string
  message?: string
  retryable: boolean
  retryAfter?: string
}

export type AdminLifecycleOperation = {
  id: number
  extensionId: string
  extensionVersion: string
  packageDigest: string
  operation: 'install' | 'enable' | 'disable' | 'upgrade' | 'rollback' | 'uninstall'
  state: 'planned' | 'migrating' | 'starting' | 'healthy' | 'registering' | 'enabled' | 'draining' | 'uninstalling' | 'failed' | 'recovery'
  planVersion: string
  removalMode?: AdminLifecycleRemovalMode
  forced: boolean
  attemptCount: number
  revision: number
  currentStepId?: string
  terminalResult?: AdminLifecycleTerminalResult
  closed: boolean
  requestedByUserId?: number
  auditEventId?: number
  recoveryActorUserId?: number
  recoveryAuditEventId?: number
  error: AdminLifecyclePublicError
  createdAt: string
  updatedAt: string
  startedAt?: string
  completedAt?: string
}

export type AdminLifecycleStep = {
  id: number
  stepId: string
  lifecycleAction: string
  planVersion: string
  attempt: number
  status: 'planned' | 'running' | 'waiting' | 'succeeded' | 'failed' | 'cancelled' | 'skipped'
  completedUnits: number
  totalUnits: number
  skipReason?: string
  forced: boolean
  actorUserId?: number
  auditEventId?: number
  error: AdminLifecyclePublicError
  createdAt: string
  updatedAt: string
  startedAt?: string
  completedAt?: string
}

export type AdminLifecycleRecovery = {
  id: number
  operationAttempt: number
  decision: AdminLifecycleRecoveryDecision
  escalateForced: boolean
  reason?: string
  actorUserId: number
  auditEventId: number
  createdAt: string
}

export type AdminLifecycleOperationDetail = AdminLifecycleOperation & {
  steps: AdminLifecycleStep[]
  recoveries: AdminLifecycleRecovery[]
}

export type AdminLifecycleRecoveryInput = {
  decision: AdminLifecycleRecoveryDecision
  reason: string
  escalateForced: boolean
  residualRiskAcknowledged: boolean
}

export function isLifecycleV2Plugin(extension: AdminExtension) {
  return extension.type === 'plugin'
    && extension.manifest.backend?.protocolVersion === 2
    && Boolean(extension.manifest.lifecycle?.contractVersion?.trim())
}

export function canRecoverLifecycleOperation(operation?: AdminLifecycleOperation | null) {
  return operation?.terminalResult === 'failed' || operation?.terminalResult === 'cancelled'
}

export function lifecycleRecoveryRequiresReason(input: Pick<AdminLifecycleRecoveryInput, 'decision' | 'escalateForced'>) {
  return input.decision === 'skip_step' || input.escalateForced
}

export function canSubmitLifecycleRecovery(
  operation: AdminLifecycleOperation | null | undefined,
  input: AdminLifecycleRecoveryInput,
  isSuperAdmin: boolean
) {
  if (!canRecoverLifecycleOperation(operation)) return false
  if (input.escalateForced) {
    if (operation?.operation !== 'uninstall' || !isSuperAdmin || !input.residualRiskAcknowledged) return false
  }
  return !lifecycleRecoveryRequiresReason(input) || input.reason.trim().length > 0
}

export type AdminExtensionSettingsProfile = 'none' | 'schema' | 'actions' | 'prebuilt'

export function extensionSettingsProfile(extension: AdminExtension): AdminExtensionSettingsProfile {
  const settings = extension.manifest.settings
  if (settings && !Array.isArray(settings)) {
    if (settings.ui?.mode === 'component' && settings.ui.component?.entry) return 'prebuilt'
    if ((settings.actions?.length ?? 0) > 0) return 'actions'
    return 'schema'
  }
  if (Array.isArray(settings) && settings.length > 0) return 'schema'
  return 'none'
}

const extensionSettingsProfileMeta = {
  none: { color: 'neutral', icon: 'i-lucide-minus', labelKey: 'admin.extensions.settingsProfiles.none' },
  schema: { color: 'primary', icon: 'i-lucide-layout-template', labelKey: 'admin.extensions.settingsProfiles.schema' },
  actions: { color: 'info', icon: 'i-lucide-activity', labelKey: 'admin.extensions.settingsProfiles.actions' },
	prebuilt: { color: 'warning', icon: 'i-lucide-shield-alert', labelKey: 'admin.extensions.settingsProfiles.prebuilt' }
} as const

export function extensionSettingsPresentation(extension: AdminExtension) {
  const profile = extensionSettingsProfile(extension)
  return { profile, ...extensionSettingsProfileMeta[profile] }
}

function manifestSettingFields(manifest: AdminExtensionManifest) {
  const settings = manifest.settings
  return Array.isArray(settings) ? settings : settings?.fields || []
}

export type AdminExtensionEvent = {
  id: number
  extensionId: string
  actorUserId: number
  action: string
  message: string
  createdAt: string
}

export type AdminExtensionEventDefinition = {
  name: string
  kind: AdminExtensionEventKind
  description: string
  payloadFields?: string[]
  patchFields?: string[]
  timeoutMs: number
}

export type AdminExtensionEventDelivery = {
  id: number
  extensionId: string
  eventName: string
  eventKind: AdminExtensionEventKind
  status: AdminExtensionDeliveryStatus
  reason: string
  message: string
  correlationId: string
  attemptCount: number
  createdAt: string
  updatedAt: string
  completedAt?: string
}

export type AdminContributionPointDefinition = {
  id: string
  owner: string
  kind: string
  description: string
  payloadType: string
}

export type AdminEffectiveContribution = {
  extensionId: string
  extensionName: string
  extensionType: AdminExtensionType
  point: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  payload?: AdminContributionPayload
}

export type AdminExtensionStats = {
  pluginCount: number
  themeCount: number
  enabledPluginCount: number
  activeThemeId: string
}

export const EXTENSION_EVENT_FETCH_LIMIT = 100
export const EXTENSION_DELIVERY_FETCH_LIMIT = 100

export type AdminExtensionSettingDeclaration = {
  extensionId: string
  extensionName: string
  extensionType: AdminExtensionType
  setting: AdminExtensionSetting
}

export function filterExtensionsByType(items: AdminExtension[], type: AdminExtensionType) {
  return items.filter(item => item.type === type)
}

export function isExtensionArtifactAvailable(item: AdminExtension) {
  return item.artifactState !== 'missing'
}

export function canRequestExtensionUninstall(item: AdminExtension, isSuperAdmin: boolean) {
  if (!item.isDeletable || item.source === 'builtin' || item.isSystem) {
    return false
  }
  if (!isExtensionArtifactAvailable(item)) {
    return isSuperAdmin && item.status !== 'enabled'
  }
  return item.status !== 'enabled' || isLifecycleV2Plugin(item)
}

export function missingArtifactCleanupCandidates(items: AdminExtension[]) {
  return items.filter(item =>
    !isExtensionArtifactAvailable(item)
    && item.source === 'uploaded'
    && item.isDeletable
    && !item.isSystem
    && item.status !== 'enabled')
}

export function selectMissingArtifactCleanupTargets(items: AdminExtension[], extensionId = '') {
  const candidates = missingArtifactCleanupCandidates(items)
  const targetId = extensionId.trim()
  return targetId ? candidates.filter(item => item.id === targetId) : candidates
}

export function extensionStats(items: AdminExtension[]): AdminExtensionStats {
  return {
    pluginCount: filterExtensionsByType(items, 'plugin').length,
    themeCount: filterExtensionsByType(items, 'theme').length,
    enabledPluginCount: items.filter(item => item.type === 'plugin' && item.status === 'enabled').length,
    activeThemeId: activeTheme(items)?.id || ''
  }
}

export function activeTheme(items: AdminExtension[]) {
  return items.find(item => item.type === 'theme' && item.status === 'enabled')
}

export function themeStatusLabelKey(item: AdminExtension) {
  return item.type === 'theme' && item.status === 'enabled'
    ? 'admin.extensions.status.activeTheme'
    : `admin.extensions.status.${item.status}`
}

export function themeActionState(item: AdminExtension): AdminThemeActionState {
  if (item.status === 'enabled') {
    return 'active'
  }
  if (item.id === 'sforum.default-theme' && item.source === 'builtin') {
    return 'activateDefault'
  }
	return 'activate'
}

export function capabilityCount(item: AdminExtension) {
  const manifest = item.manifest
  return [
    manifest.permissions?.length || 0,
    manifestSettingFields(manifest).length,
    manifest.migrations?.length || 0,
    effectiveManifestAdminPages(manifest).length,
    manifest.routes?.length || 0,
    manifest.hooks?.length || 0,
    manifest.events?.length || 0,
    manifest.jobs?.length || 0,
    manifest.providers?.length || 0,
    manifest.contributions?.length || 0
  ].reduce((total, count) => total + count, 0)
}

function effectiveManifestAdminPages(manifest: AdminExtensionManifest) {
  return manifest.admin?.pages?.length ? manifest.admin.pages : manifest.adminPages || []
}


export function runtimeStatusLabelKey(item: AdminExtension) {
  return `admin.extensions.runtime.${item.runtime?.state || 'stopped'}`
}

export function runtimeCapabilitySummary(item: AdminExtension) {
  return {
    routes: item.runtime?.routeCount ?? item.manifest.routes?.length ?? 0,
    hooks: item.runtime?.hookCount ?? declaredEventCount(item),
    events: item.runtime?.eventCount ?? declaredEventCount(item),
    providers: item.runtime?.providerCount ?? item.manifest.providers?.length ?? 0
  }
}

export function canRestartPlugin(item: AdminExtension) {
  const recoverable = item.status === 'enabled' || (item.status === 'disabled' && Boolean(item.stagedVersion))
  return item.type === 'plugin' && isExtensionArtifactAvailable(item) && recoverable && Boolean(item.runtime)
}

export function canUpgradePlugin(item: AdminExtension) {
  return Boolean(item.stagedVersion) && canRestartPlugin(item)
}

export function extensionUpgradeLifecycleAction(item: AdminExtension): 'upgrade' | 'restart' {
  const staged = item.stagedVersion
  const stagedUsesLifecycleV2 = staged?.manifest.backend?.protocolVersion === 2
    && Boolean(staged.manifest.lifecycle?.contractVersion?.trim())
  return item.status === 'enabled' && isLifecycleV2Plugin(item) && stagedUsesLifecycleV2
    ? 'upgrade'
    : 'restart'
}

export function extensionUpgradeCandidates(items: AdminExtension[]) {
  return items.filter(canUpgradePlugin)
}


export function extensionSettingDeclarations(items: AdminExtension[], locale?: string | null) {
  return items
    .filter(isExtensionArtifactAvailable)
    .flatMap((item): AdminExtensionSettingDeclaration[] => manifestSettingFields(item.manifest).map(setting => ({
      extensionId: item.id,
      extensionName: extensionDisplayName(item, locale),
      extensionType: item.type,
      setting
    })))
}

export function defaultExtensionIcon(type: AdminExtensionType) {
  return type === 'theme' ? 'i-lucide-palette' : 'i-lucide-plug'
}

export function extensionAdminPages(item: AdminExtension, locale?: string | null): AdminExtensionAdminPage[] {
  const display = extensionLocalizedDisplay(item, locale)
  const pages: AdminExtensionAdminPage[] = [
    {
      path: '/about',
      label: display.name,
      description: display.description,
      icon: defaultExtensionIcon(item.type),
      view: 'about',
      order: 0
    }
  ]

  for (const page of effectiveManifestAdminPages(item.manifest)) {
    pages.push({
      path: normalizeExtensionPagePath(page.path),
      label: page.label,
      description: page.description || display.description,
      icon: page.icon || defaultExtensionIcon(item.type),
      view: page.view || 'about',
      menu: page.menu === true,
      order: page.order || 0,
      permission: page.permission,
      component: page.component,
      ...(page.hostActions?.length ? { hostActions: page.hostActions } : {})
    })
  }

  return pages.sort((left, right) => {
    const orderDiff = (left.order || 0) - (right.order || 0)
    return orderDiff || left.path.localeCompare(right.path)
  })
}

export function findExtensionAdminPage(item: AdminExtension, path: string, locale?: string | null) {
  const normalized = normalizeExtensionPagePath(path)
  return extensionAdminPages(item, locale).find(page => normalizeExtensionPagePath(page.path) === normalized)
}

export function extensionManagePagePath(item: AdminExtension, locale?: string | null) {
  const pages = extensionAdminPages(item, locale)
  const entry = normalizeExtensionPagePath(item.manifest.admin?.entry)
  if (item.manifest.admin?.entry && pages.some(page => normalizeExtensionPagePath(page.path) === entry)) {
    return entry
  }

  const settings = pages.find(page => normalizeExtensionPagePath(page.path) === '/settings')
  if (settings) {
    return '/settings'
  }

  const declared = pages.find(page => normalizeExtensionPagePath(page.path) !== '/about')
  return declared?.path || '/about'
}

export function extensionManageRoute(item: AdminExtension) {
  return extensionAdminPageRoute(item.id, extensionManagePagePath(item))
}

export function extensionAdminPageRoute(extensionId: string, pagePath = '/about') {
  return `/extensions/${extensionId}/pages${normalizeExtensionPagePath(pagePath)}`
}

export function normalizeExtensionPagePath(path?: string | string[] | null) {
  const raw = Array.isArray(path) ? path.join('/') : `${path || ''}`
  const normalized = raw
    .trim()
    .replace(/\\/g, '/')
    .replace(/^\/+|\/+$/g, '')

  if (!normalized) {
    return '/about'
  }

  return `/${normalized.split('/').filter(Boolean).join('/')}`
}

export function declaredEventCount(item: AdminExtension) {
  const names = new Set<string>()
  for (const event of item.manifest.events || []) {
    names.add(`${event.name}:${event.kind || ''}`)
  }
  for (const hook of item.manifest.hooks || []) {
    names.add(`${hook.name}:hook`)
  }
  return names.size
}
