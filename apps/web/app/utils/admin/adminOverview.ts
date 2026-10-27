export type AdminOverview = {
  generatedAt: string
  windowDays: number
  runtime: AdminOverviewRuntime
  community: AdminOverviewCommunity
  attachments: AdminOverviewAttachments
  moderation: AdminOverviewModeration
  extensions: AdminOverviewExtensions
  trends: {
    days: AdminOverviewTrendDay[]
  }
  topCategories: AdminOverviewCategoryActivity[]
  actions: AdminOverviewAction[]
  extensionWidgets?: AdminOverviewExtensionWidget[]
}

/** 轻量资源快照：内存/CPU/磁盘/系统负载，供高频轮询。 */
export type AdminOverviewResources = {
  generatedAt: string
  resources?: AdminOverviewRuntimeResources
  disk?: AdminOverviewDiskRuntime
  loadAverage?: AdminOverviewSystemLoadAverage
}

/**
 * 将资源快照合并进总览 runtime。
 * 采样失败字段省略时保留上一帧，避免卡片闪成「暂无数据」。
 */
export function applyOverviewResources(
  overview: AdminOverview,
  patch: AdminOverviewResources
): AdminOverview {
  return {
    ...overview,
    runtime: {
      ...overview.runtime,
      ...(patch.resources !== undefined ? { resources: patch.resources } : {}),
      ...(patch.disk !== undefined ? { disk: patch.disk } : {}),
      ...(patch.loadAverage !== undefined ? { loadAverage: patch.loadAverage } : {})
    }
  }
}

export const ADMIN_OVERVIEW_RESOURCE_POLL_MS = 5000
export const ADMIN_OVERVIEW_KPI_POLL_MS = 30_000

export type AdminOverviewExtensionWidget = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  route: string
  severity: string
}

export type AdminOverviewWorkerRuntime = {
  lastSeenAt?: string
  ageSeconds?: number
  stale: boolean
  status: 'ok' | 'stale' | 'unknown'
}

export type AdminOverviewQueueLag = {
  waiting: number
  running: number
  failed: number
}

export type AdminOverviewRuntime = {
  startedAt: string
  uptimeSeconds: number
  build: AdminOverviewBuildInfo
  /** Primary KPI: API process RSS (bytes), not Go MemStats.Sys. */
  memoryBytes: number
  heapAllocBytes: number
  heapSysBytes: number
  /** Diagnostic: Go runtime.MemStats.Sys. */
  sysBytes: number
  /** API RSS + owned backend plugin children; omitted when sampling fails. */
  familyMemoryBytes?: number
  pluginChildCount: number
  resources?: AdminOverviewRuntimeResources
  disk?: AdminOverviewDiskRuntime
  loadAverage?: AdminOverviewSystemLoadAverage
  goroutineCount: number
  gcCount: number
  lastGcPauseNs: number
  database: {
    maxConnections: number
    totalConnections: number
    acquiredConnections: number
    idleConnections: number
  }
  worker?: AdminOverviewWorkerRuntime
  queueLag?: AdminOverviewQueueLag
}

export type AdminOverviewRuntimeResources = {
  sampledAt?: string
  apiMemoryBytes: number
  workerMemoryBytes: number
  pluginMemoryBytes: number
  totalMemoryBytes: number
  apiPssBytes?: number
  workerPssBytes?: number
  pluginPssBytes?: number
  totalPssBytes?: number
  apiAnonHugePagesBytes?: number
  workerAnonHugePagesBytes?: number
  pluginAnonHugePagesBytes?: number
  totalAnonHugePagesBytes?: number
  apiMemoryMedianBytes: number
  workerMemoryMedianBytes: number
  pluginMemoryMedianBytes: number
  totalMemoryMedianBytes: number
  apiPssMedianBytes?: number
  workerPssMedianBytes?: number
  pluginPssMedianBytes?: number
  totalPssMedianBytes?: number
  memorySampleCount: number
  pssSampleCount?: number
  memoryWindowSeconds: number
  apiCpuPercent: number
  workerCpuPercent: number
  pluginCpuPercent: number
  totalCpuPercent: number
  pluginChildCount: number
  pluginOverlapCount: number
  plugins?: AdminOverviewPluginRuntimeUsage[]
  workerFound: boolean
  workerEmbedded: boolean
  workerConcurrency: number
}

export type AdminOverviewPluginRuntimeUsage = {
  extensionId: string
  memoryBytes: number
  pssBytes?: number
  pssMedianBytes?: number
  pssSampleCount?: number
  anonHugePagesBytes?: number
  cpuPercent: number
  processCount: number
  apiOwnedProcessCount: number
  workerOwnedProcessCount: number
}

export type AdminOverviewMemoryBucket = 'api' | 'worker' | 'plugin' | 'total'

/** 优先显示滚动中位数；兼容尚未返回窗口字段的旧 API 帧。 */
export function overviewMemoryDisplayBytes(
  resources: AdminOverviewRuntimeResources,
  bucket: AdminOverviewMemoryBucket
) {
  const medianKeys: Record<AdminOverviewMemoryBucket, keyof AdminOverviewRuntimeResources> = {
    api: 'apiMemoryMedianBytes',
    worker: 'workerMemoryMedianBytes',
    plugin: 'pluginMemoryMedianBytes',
    total: 'totalMemoryMedianBytes'
  }
  const instantKeys: Record<AdminOverviewMemoryBucket, keyof AdminOverviewRuntimeResources> = {
    api: 'apiMemoryBytes',
    worker: 'workerMemoryBytes',
    plugin: 'pluginMemoryBytes',
    total: 'totalMemoryBytes'
  }
  const median = Number(resources[medianKeys[bucket]])
  if (Number.isFinite(median) && median >= 0 && Number(resources.memorySampleCount) > 0) {
    return median
  }
  return Math.max(0, Number(resources[instantKeys[bucket]]) || 0)
}

export function overviewPSSDisplayBytes(
  resources: AdminOverviewRuntimeResources,
  bucket: AdminOverviewMemoryBucket
) {
  const medianKeys: Record<AdminOverviewMemoryBucket, keyof AdminOverviewRuntimeResources> = {
    api: 'apiPssMedianBytes',
    worker: 'workerPssMedianBytes',
    plugin: 'pluginPssMedianBytes',
    total: 'totalPssMedianBytes'
  }
  const instantKeys: Record<AdminOverviewMemoryBucket, keyof AdminOverviewRuntimeResources> = {
    api: 'apiPssBytes',
    worker: 'workerPssBytes',
    plugin: 'pluginPssBytes',
    total: 'totalPssBytes'
  }
  const median = Number(resources[medianKeys[bucket]])
  if (Number.isFinite(median) && median > 0 && Number(resources.pssSampleCount) > 0) {
    return median
  }
  return Math.max(0, Number(resources[instantKeys[bucket]]) || 0)
}

export type AdminOverviewDiskRuntime = {
  totalBytes: number
  usedBytes: number
  freeBytes: number
  usedPercent: number
}

export type AdminOverviewSystemLoadAverage = {
  oneMinute: number
  fiveMinutes: number
  fifteenMinutes: number
}

export type AdminOverviewBuildInfo = {
  name: string
  version: string
  commit?: string
  builtAt?: string
  goVersion: string
  /** Build target OS/arch (GOOS/GOARCH), such as linux/amd64. */
  platform: string
  dirty: boolean
  sourceUrl: string
}

export type AdminOverviewCommunity = {
  userCount: number
  activeUserCount: number
  disabledUserCount: number
  bannedUserCount: number
  topicCount: number
  activeTopicCount: number
  lockedTopicCount: number
  hiddenTopicCount: number
  deletedTopicCount: number
  commentCount: number
  postCount: number
  categoryCount: number
  tagCount: number
  pendingTagCount: number
  totalViews: number
}

export type AdminOverviewAttachments = {
  totalCount: number
  activeCount: number
  disabledCount: number
  deletedCount: number
  orphanCount: number
  totalBytes: number
}

export type AdminOverviewModeration = {
  openCount: number
  reviewingCount: number
  resolvedCount: number
  rejectedCount: number
}

export type AdminOverviewExtensions = {
  totalCount: number
  enabledCount: number
  pluginCount: number
  themeCount: number
  installedPluginRuntimeCount: number
  failedEventCount: number
}

export type AdminOverviewTrendDay = {
  date: string
  topicCount: number
  commentCount: number
  userCount: number
}

/** 趋势三列迷你图使用的字段 */
export type AdminOverviewTrendField = 'topicCount' | 'commentCount' | 'userCount'

export type AdminOverviewTrendSpark = {
  line: string
  area: string
  points: Array<{ x: number, y: number }>
}

export type AdminOverviewCategoryActivity = {
  id: number
  slug: string
  name: string
  topicCount: number
  commentCount: number
}

export type AdminOverviewAction = {
  key: string
  count: number
  severity: string
  route: string
  /**
   * 信息性指标（当前为孤儿附件）：值得让运营知道，但不是需要人工处理的待办。
   * advisory 项不计入「待处理」KPI，也不点亮告警色。
   */
  advisory?: boolean
}

export type OverviewTone = 'success' | 'info' | 'warning' | 'danger' | 'neutral'

export function formatOverviewBytes(bytes: number) {
  const value = Math.max(0, Number(bytes) || 0)
  const mib = Math.round(value / 1024 / 1024)
  return `${formatInteger(mib)} MiB`
}

export function formatOverviewStorage(bytes: number) {
  const value = Math.max(0, Number(bytes) || 0)
  if (value < 1024) {
    return `${formatInteger(value)} B`
  }
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let scaled = value / 1024
  let unitIndex = 0
  while (scaled >= 1024 && unitIndex < units.length - 1) {
    scaled /= 1024
    unitIndex += 1
  }
  return `${trimOneDecimal(scaled)} ${units[unitIndex]}`
}

export function formatOverviewPercent(value: number) {
  const normalized = Math.max(0, Number(value) || 0)
  return `${normalized.toFixed(1).replace(/\.0$/, '')}%`
}

export function formatOverviewLoad(value: number) {
  const normalized = Math.max(0, Number(value) || 0)
  return normalized.toFixed(2)
}

export function formatOverviewCount(value: number) {
  const count = Math.max(0, Number(value) || 0)
  if (count >= 1_000_000) {
    return `${trimOneDecimal(count / 1_000_000)}m`
  }
  if (count >= 1_000) {
    return `${trimOneDecimal(count / 1_000)}k`
  }
  return formatInteger(count)
}

/**
 * 趋势图日粒度数字：尽量短、不出现「128.2k」再被 CSS 截成 128…
 * 1_000–9_999 → 1.2k；≥10_000 → 12k / 128k
 */
export function formatOverviewTrendDayCount(value: number) {
  const count = Math.max(0, Math.round(Number(value) || 0))
  if (count < 1_000) {
    return String(count)
  }
  if (count < 10_000) {
    return `${trimOneDecimal(count / 1_000)}k`
  }
  if (count < 1_000_000) {
    return `${Math.round(count / 1_000)}k`
  }
  return `${trimOneDecimal(count / 1_000_000)}m`
}

export function overviewPercent(value: number, total: number) {
  const normalizedTotal = Number(total) || 0
  if (normalizedTotal <= 0) {
    return 0
  }
  return Math.round((Math.max(0, Number(value) || 0) / normalizedTotal) * 100)
}

export function overviewTrendMax(days: AdminOverviewTrendDay[]) {
  const max = days.reduce((current, day) => {
    return Math.max(current, day.topicCount + day.commentCount + day.userCount)
  }, 0)
  return Math.max(max, 1)
}

/** 单系列最大值，供独立 sparkline 刻度使用 */
export function overviewTrendFieldMax(days: AdminOverviewTrendDay[], field: AdminOverviewTrendField) {
  if (!days.length) {
    return 1
  }
  return Math.max(1, ...days.map(day => Math.max(0, Number(day[field]) || 0)))
}

export function overviewTrendSum(days: AdminOverviewTrendDay[], field: AdminOverviewTrendField) {
  return days.reduce((total, day) => total + Math.max(0, Number(day[field]) || 0), 0)
}

/** 较前一日变化百分比；prev 为 0 且 today > 0 时视为 +100% */
export function overviewTrendDeltaPercent(today: number, previous: number) {
  const current = Math.max(0, Number(today) || 0)
  const prev = Math.max(0, Number(previous) || 0)
  if (prev === 0) {
    return current > 0 ? 100 : 0
  }
  return Math.round(((current - prev) / prev) * 100)
}

export type OverviewTrendDeltaKind = 'flat' | 'up' | 'down' | 'none'

/**
 * 环比展示语义：两端都为 0 时不展示百分比（避免「▲ 0%」误导）。
 */
export function overviewTrendDeltaKind(today: number, previous: number): OverviewTrendDeltaKind {
  const current = Math.max(0, Number(today) || 0)
  const prev = Math.max(0, Number(previous) || 0)
  if (current === 0 && prev === 0) {
    return 'none'
  }
  if (current === prev) {
    return 'flat'
  }
  return current > prev ? 'up' : 'down'
}

/** 单日柱高（px），用于迷你柱图；0 也给 2px 基线，避免完全消失 */
export function overviewTrendBarHeightPx(value: number, max: number, chartHeight = 72) {
  const safeMax = Math.max(1, Number(max) || 1)
  const safeValue = Math.max(0, Number(value) || 0)
  if (safeValue <= 0) {
    return 2
  }
  return Math.max(4, Math.round((safeValue / safeMax) * chartHeight))
}

export function overviewTrendPeakDate(days: AdminOverviewTrendDay[], field: AdminOverviewTrendField) {
  if (!days.length) {
    return ''
  }
  return days.reduce((best, day) => (
    (Number(day[field]) || 0) >= (Number(best[field]) || 0) ? day : best
  )).date
}

export function overviewTrendDateLabel(date: string) {
  const value = `${date || ''}`.trim()
  if (value.length >= 10) {
    return value.slice(5, 10)
  }
  return value
}

/**
 * 生成平滑 sparkline 的 line / area path 与数据点坐标。
 * 各系列独立 max，避免用户被回复数量级压扁。
 */
export function overviewTrendSparkPath(
  values: number[],
  width = 280,
  height = 72,
  pad = 8
): AdminOverviewTrendSpark {
  const safe = values.map(value => Math.max(0, Number(value) || 0))
  if (safe.length === 0) {
    return { line: '', area: '', points: [] }
  }

  const max = Math.max(1, ...safe)
  const lastIndex = Math.max(safe.length - 1, 1)
  const points = safe.map((value, index) => {
    const x = pad + (index / lastIndex) * (width - pad * 2)
    const y = pad + (1 - value / max) * (height - pad * 2)
    return { x, y }
  })
  const first = points[0]!

  let line = `M ${first.x} ${first.y}`
  for (let index = 1; index < points.length; index += 1) {
    const prev = points[index - 1]!
    const curr = points[index]!
    const midX = (prev.x + curr.x) / 2
    line += ` C ${midX} ${prev.y}, ${midX} ${curr.y}, ${curr.x} ${curr.y}`
  }

  const bottom = height - 2
  const last = points[points.length - 1]!
  const area = safe.length === 1
    ? `${line} L ${first.x} ${bottom} Z`
    : `${line} L ${last.x} ${bottom} L ${first.x} ${bottom} Z`

  return { line, area, points }
}

export function overviewActionTone(severity: string): OverviewTone {
  switch (severity) {
    case 'success':
    case 'info':
    case 'warning':
    case 'danger':
      return severity
    default:
      return 'neutral'
  }
}

/** 「待处理」KPI 求和：跳过 advisory 项，信息性指标不算待办。 */
export function overviewActionTotal(actions: AdminOverviewAction[]) {
  return (actions || []).reduce((total, action) => (
    action.advisory ? total : total + Math.max(0, Number(action.count) || 0)
  ), 0)
}

/** advisory 项合计，供 KPI 副标题补充说明（当前为孤儿附件）。 */
export function overviewAdvisoryTotal(actions: AdminOverviewAction[]) {
  return (actions || []).reduce((total, action) => (
    action.advisory ? total + Math.max(0, Number(action.count) || 0) : total
  ), 0)
}

/** KPI 色调只看真实待办量：纯 advisory 时保持 success。 */
export function overviewActionsTone(actions: AdminOverviewAction[]): OverviewTone {
  return overviewActionTotal(actions) > 0 ? 'warning' : 'success'
}

export function formatOverviewUptime(seconds: number) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0))
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days > 0) {
    return `${days}d ${hours}h`
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`
  }
  return `${minutes}m`
}

export function formatOverviewDate(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  const year = date.getFullYear()
  const month = padDatePart(date.getMonth() + 1)
  const day = padDatePart(date.getDate())
  const hours = padDatePart(date.getHours())
  const minutes = padDatePart(date.getMinutes())
  const seconds = padDatePart(date.getSeconds())
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

export function formatOverviewCommit(value: string) {
  const commit = String(value || '').trim()
  return commit.length > 12 ? commit.slice(0, 12) : commit
}

export function formatOverviewVersion(name: string, version: string, commit = '') {
  const product = String(name || '').trim()
  const release = String(version || '').trim() || 'dev'
  const revision = String(commit || '').trim()
  const displayVersion = release === 'dev' && revision
    ? `dev-${revision.slice(0, 5)}`
    : release
  const prefix = /^\d+\.\d+\.\d+(?:-|$)/.test(release) ? 'v' : ''
  return `${product} ${prefix}${displayVersion}`.trim()
}

function trimOneDecimal(value: number) {
  return value.toFixed(1).replace(/\.0$/, '')
}

function formatInteger(value: number) {
  return Math.round(value).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

function padDatePart(value: number) {
  return value.toString().padStart(2, '0')
}
