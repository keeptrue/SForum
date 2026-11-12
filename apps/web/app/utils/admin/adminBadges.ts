import type { BadgeTier } from '~/utils/forum/forumBadge'
import type { BadgeRule } from '~/utils/admin/badgeRule'

/** 授予方式：自动按统计阈值，手动由管理员授予。 */
export type BadgeGrantMode = 'automatic' | 'manual'

/** 公开说明的双语句子；两个键**必须成对写回**（服务端整体替换）。 */
export interface BadgeRequirementSummary {
  zh: string
  en: string
}

/** 后台列表行：徽章定义 + 已授予人数（后端聚合）。 */
export interface AdminBadgeRow {
  id: number
  slug: string
  name: string
  description: string
  icon: string
  tier: BadgeTier
  groupId: number | null
  groupName: string | null
  grantMode: BadgeGrantMode
  /** 旧口径字段（只读展示）：M1 规则引擎之后写路径不再维护，下一刀删列。 */
  metric: string | null
  threshold: number | null
  /**
   * 规则 AST。`undefined` = 列表接口还没带这个字段（M1.4 前的过渡态）；
   * `null` = 手动徽章（无规则）；对象 = 当前规则。
   */
  rule?: BadgeRule | null
  /**
   * 公开面「怎么获得」的双语说明，**服务端按规则生成**（`requirementText{zh,en}`）。
   * 后台只读展示，不再手填。
   */
  requirementText?: BadgeRequirementSummary | null
  /**
   * @deprecated 手填口径已废弃（`requirementSummary{zh,en}`）：服务端仍下发以兼容旧客户端，
   * 后台界面既不读也不写。新代码一律用 `requirementText`。
   */
  requirementSummary?: BadgeRequirementSummary | null
  grantedCount: number
  enabled: boolean
  position: number
}

/**
 * 分组创建/更新请求体：字段名与后端 `badgeGroupBody`（Badges/controller.go）逐字一致。
 * PATCH 是 merge 语义，四个字段都可选提交。
 */
export type AdminBadgeGroupInput = {
  key: string
  name: string
  description: string
  position: number
}

/**
 * 后台授予记录行：字段名与后端 `BadgeGrantRecord`（Models/Badges/types.go）json tag 逐字一致。
 * 该类型由 controller 直接透传（不经过 dto.go 的 admin 映射），改字段名前先读 types.go。
 */
export interface AdminBadgeGrantRow {
  userId: number
  username: string
  displayName: string
  badgeId: number
  badgeSlug: string
  grantedAt: string
  grantedBy: number | null
  grantReason: string
}

/** 授予来源三态，判别式与 knowledge/modules/badges.md「三来源判别式」一致。 */
export type AdminBadgeGrantSource = 'manual' | 'recompute' | 'automatic'

/**
 * 来源判别式：`grantedBy` 非空 = 管理员手动；`grantReason === 'recompute'` = 批量重算补授；
 * 其余（grantedBy 为空 + 无理由）= 规则自动授予。
 */
export function grantSourceOf(row: Pick<AdminBadgeGrantRow, 'grantedBy' | 'grantReason'>): AdminBadgeGrantSource {
  if (row.grantedBy !== null && row.grantedBy !== undefined) return 'manual'
  if (row.grantReason === 'recompute') return 'recompute'
  return 'automatic'
}

/** 只有手动授予行才允许收回：自动/重算来源收回会 422 forum.badge_manual_only。 */
export function grantRevocable(row: AdminBadgeGrantRow): boolean {
  return grantSourceOf(row) === 'manual'
}

/**
 * 规则列口径：手动徽章、或规则字段不全时返回 null，由调用方渲染占位符。
 * 不在这里编造 `metric ≥ 0` 这类看起来有、实际不存在的规则。
 */
export function adminBadgeRule(
  badge: Pick<AdminBadgeRow, 'grantMode' | 'metric' | 'threshold'>
): { metric: string, threshold: number } | null {
  if (badge.grantMode !== 'automatic' || !badge.metric || !badge.threshold) {
    return null
  }
  return { metric: badge.metric, threshold: badge.threshold }
}

/**
 * 规则列的指标文案。
 *
 * 直接把 `rule.metric` 塞进插值会渲染出 `topic_count 达到 5 时授予` —— 后端枚举值不是人话。
 * 优先用 `admin.badges.metrics.<slug>`；缺键才回落原始 slug（保留信息，不静默变成空字符串）。
 * 做成独立函数是为了能被断言：组件里内联三元表达式只能靠读代码相信它。
 */
export function adminBadgeMetricLabel(
  metric: string | undefined,
  translate: (key: string) => string,
  has: (key: string) => boolean
): string {
  if (!metric) return ''
  const key = `admin.badges.metrics.${metric}`
  return has(key) ? translate(key) : metric
}

/** 后台可选图标白名单（规范 §4.4）：只允许 Tabler / Lucide，禁止 emoji 与手写 SVG。 */
export const BADGE_ICON_CHOICES: readonly string[] = [
  'i-lucide-medal',
  'i-lucide-award',
  'i-lucide-crown',
  'i-lucide-trophy',
  'i-lucide-star',
  'i-lucide-sparkles',
  'i-lucide-flame',
  'i-lucide-heart',
  'i-lucide-zap',
  'i-lucide-book-open',
  'i-lucide-message-square',
  'i-lucide-thumbs-up',
  'i-lucide-calendar-check',
  'i-lucide-gem',
  'i-lucide-target'
]

/** 自动徽章可用的统计指标；顺序与后端 ActivityMetric 一致。 */
export const BADGE_METRIC_OPTIONS: readonly string[] = [
  'topic_count',
  'comment_count',
  'like_received_count',
  'bookmark_count',
  'days_visited'
]

/** 编辑表单 → 请求体：手动徽章不带 metric/threshold，避免后端按自动规则校验。 */
export function toAdminBadgeInput(form: {
  slug: string
  name: string
  description: string
  groupId: number | null
  tier: string
  icon: string
  grantMode: string
  metric: string | null
  threshold: number | null
  enabled: boolean
  position: number
  /**
   * 规则三态：**缺省 = 不改**（键都不出现）、`null` = 清空、对象 = 替换。
   * PATCH 是 merge 语义，缺省时带一个 `rule` 键会把「只改名字」变成「替换规则」。
   */
  rule?: BadgeRule | null
}) {
  const automatic = form.grantMode === 'automatic'
  const body = {
    slug: form.slug.trim(),
    name: form.name.trim(),
    description: form.description.trim(),
    groupId: form.groupId,
    tier: form.tier,
    icon: form.icon,
    grantMode: form.grantMode,
    metric: automatic ? form.metric : null,
    threshold: automatic ? form.threshold : null,
    enabled: form.enabled,
    position: form.position
  }
  // 公开说明是派生数据：请求体永不携带 requirementSummary（服务端按规则生成）。
  return form.rule === undefined ? body : { ...body, rule: form.rule }
}

/** 徽章 tab 的筛选状态；空串表示「不过滤」。字段名与 URL query 键一致。 */
export interface AdminBadgeFilters {
  q: string
  /** 分组 id 的字符串形式；'' = 全部分组，'none' = 未分组。 */
  group: string
  tier: string
  grantMode: string
  /** 'true' | 'false' | ''（全部）。 */
  enabled: string
}

export const EMPTY_BADGE_FILTERS: AdminBadgeFilters = {
  q: '',
  group: '',
  tier: '',
  grantMode: '',
  enabled: ''
}

/** 筛选是否真的在起作用：区分「列表本来就空」与「筛选把结果筛空了」。 */
export function badgeFiltersActive(filters: AdminBadgeFilters): boolean {
  return filters.q.trim() !== '' || filters.group !== '' || filters.tier !== ''
    || filters.grantMode !== '' || filters.enabled !== ''
}

/**
 * 等级位次表 → 排序用的 `Record<key, position>`。
 *
 * 等级是运营可增删的数据，前端不许再写一份三档权重：位次只来自 `GET /admin/badges/tiers`
 * 的 `position`（升序 = 后台与公开面一致的展示顺序）。
 */
export function tierOrderMap(tiers: ReadonlyArray<{ key: string, position: number }>): Record<string, number> {
  const order: Record<string, number> = {}
  for (const tier of tiers) order[tier.key] = Number(tier.position)
  return order
}

/** 按筛选条件过滤徽章列表；q 同时匹配名称、slug 与说明（大小写不敏感）。 */
export function filterBadges(badges: AdminBadgeRow[], filters: AdminBadgeFilters): AdminBadgeRow[] {
  const q = filters.q.trim().toLowerCase()
  return badges.filter((badge) => {
    if (q && !`${badge.name} ${badge.slug} ${badge.description}`.toLowerCase().includes(q)) return false
    if (filters.group === 'none' && badge.groupId !== null) return false
    if (filters.group !== '' && filters.group !== 'none' && String(badge.groupId) !== filters.group) return false
    if (filters.tier !== '' && badge.tier !== filters.tier) return false
    if (filters.grantMode !== '' && badge.grantMode !== filters.grantMode) return false
    if (filters.enabled === 'true' && !badge.enabled) return false
    if (filters.enabled === 'false' && badge.enabled) return false
    return true
  })
}

export type AdminBadgeSortKey = 'name' | 'tier' | 'grantedCount'
export type AdminBadgeSortDir = 'asc' | 'desc'

/** 可排序列集合：表格列头的排序按钮与 URL query 白名单共用这份枚举。 */
export const BADGE_SORT_KEYS: readonly AdminBadgeSortKey[] = ['name', 'tier', 'grantedCount']

/**
 * 排序为稳定排序（同键保持后端原有次序）：后端列表已按分组 position → 徽章 position
 * 排好，前端排序只在其上重排，不越权重排分组。
 */
export function sortBadges(
  badges: AdminBadgeRow[],
  key: AdminBadgeSortKey | '',
  dir: AdminBadgeSortDir,
  tierOrder: Record<string, number> = {}
): AdminBadgeRow[] {
  if (!key) return badges
  const factor = dir === 'desc' ? -1 : 1
  // 未知等级（等级被删但行还没刷新）排在所有已知等级之后，不丢行、不猜位次。
  const tierWeight = (tier: string) => tierOrder[tier] ?? Number.MAX_SAFE_INTEGER
  return [...badges].sort((left, right) => {
    if (key === 'tier') return (tierWeight(left.tier) - tierWeight(right.tier)) * factor
    if (key === 'grantedCount') return (left.grantedCount - right.grantedCount) * factor
    return left.name.localeCompare(right.name) * factor
  })
}

/** 分组表单校验：错误码由 tab 组件翻译成文案。 */
export function validateBadgeGroupForm(form: Pick<AdminBadgeGroupInput, 'key' | 'name'>): {
  key?: 'required', name?: 'required'
} {
  const errors: { key?: 'required', name?: 'required' } = {}
  if (!form.key.trim()) errors.key = 'required'
  if (!form.name.trim()) errors.name = 'required'
  return errors
}

/**
 * 重算 toast 副标题的**唯一**文案决策点。
 *
 * 三个数语义不同（granted = 新插入授予行数，refreshed = top badge 变化的用户数，
 * queued = 转入异步评估的徽章数），所以分两句说，绝不加总：
 * - `queued > 0`：必须出现「{count} 枚徽章已转入后台评估」——后台还在跑，不能被摘要吃掉；
 * - `granted + refreshed > 0`：保留「本次影响 {count} 个用户」；
 * - 都为 0：`undefined`（幂等结果是正常状态，不编一句假成功）。
 *
 * 参数形状与 `useAdminBadges` 的 `AdminBadgeRecomputeResult` 同形；这里不反向 import
 * composable（utils 是叶子层），缺字段按 0 处理。
 */
/**
 * 重算之后是否值得重拉回扫进度。
 *
 * 只有真的把徽章转入后台评估（`queued > 0`）才可能新增 run；`0` 或字段缺失（老后端）
 * 时重拉等于替一次空操作打一次请求。`undefined` 的既有语义见
 * `recomputeToastDescription`：三数全 0 时不编副标题，所以这里也必须当 0 处理，
 * 不能靠 `Number(undefined)` 之类的技巧把「没这个字段」当成「有值」。
 */
export function shouldRefreshBackfillAfterRecompute(
  result: { queued?: number | null } | null | undefined
): boolean {
  return Number(result?.queued ?? 0) > 0
}

export function recomputeToastDescription(
  result: { granted?: number | null, refreshed?: number | null, queued?: number | null } | null | undefined,
  translate: (key: string, params?: Record<string, unknown>) => string
): string | undefined {
  const queued = Number(result?.queued ?? 0)
  const affected = Number(result?.granted ?? 0) + Number(result?.refreshed ?? 0)
  const sentences: string[] = []
  if (queued > 0) {
    sentences.push(translate('admin.badges.toast.recomputeQueued', { count: queued }))
  }
  if (affected > 0) {
    sentences.push(translate('admin.badges.toast.recomputeChanged', { count: affected }))
  }
  return sentences.length > 0 ? sentences.join(' ') : undefined
}

/**
 * 回扫进度（`GET /admin/badges/backfill-status`，形状冻结于计划 §13#15 的后续刀）。
 *
 * 读侧语义，别自行加码：
 * - 数字只来自 job 写的 run 行（`lastUserId` = 最后一个成功页的游标，
 *   `failedCursorUserId` = 失败页的 `afterUserID`），**不从 `user_badges` 反推**；
 * - `completed` 只是「刚生效，值得看一眼」，24h 后收起（见 `BACKFILL_PROGRESS_TTL_MS`）；
 * - 行内文案绝不出现 `cmp`/`params`/`children`/`op`/`rule` 这类内部字段名。
 */
export type AdminBadgeBackfillStatus = 'running' | 'completed' | 'failed'

export interface AdminBadgeBackfillRun {
  badgeId: number
  slug: string
  name: string
  status: AdminBadgeBackfillStatus
  /** 最后一个成功页的游标用户 id（0 = 还没推进过）。 */
  lastUserId: number
  /** 失败页的 `afterUserID`（非失败为 0）。 */
  failedCursorUserId: number
  startedAt: string
  updatedAt: string
  finishedAt: string
  error: string
}

export interface AdminBadgeBackfillSummary {
  status: AdminBadgeBackfillStatus
  /** 当前状态下的徽章名，最多 `MAX_BACKFILL_BADGE_NAMES` 条。 */
  badgeNames: string[]
  /** 未展示的同状态徽章数；0 = 全部展示了。 */
  hiddenCount: number
  /** running = 已扫描游标，failed = 失败游标，completed = 0。 */
  userId: number
}

/** completed 行的保留窗口：刚跑完值得确认一次，过期就收起，不长期占位。 */
export const BACKFILL_PROGRESS_TTL_MS = 24 * 60 * 60 * 1000
export const MAX_BACKFILL_BADGE_NAMES = 3

const BACKFILL_STATUSES: readonly AdminBadgeBackfillStatus[] = ['running', 'completed', 'failed']
/** 聚合优先级：还在跑 > 卡住了 > 刚跑完。 */
const BACKFILL_STATUS_ORDER: readonly AdminBadgeBackfillStatus[] = ['running', 'failed', 'completed']

function backfillText(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function backfillCount(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0
}

function normalizeBackfillRun(row: unknown): AdminBadgeBackfillRun | null {
  if (!row || typeof row !== 'object') return null
  const raw = row as Record<string, unknown>
  const badgeId = Number(raw.badgeId)
  if (!Number.isFinite(badgeId) || badgeId <= 0) return null
  const status = raw.status
  if (typeof status !== 'string' || !BACKFILL_STATUSES.includes(status as AdminBadgeBackfillStatus)) return null
  const slug = backfillText(raw.slug)
  return {
    badgeId,
    slug,
    // 徽章名没给就退回 slug，再没有就退回 id：状态行不能因为一个空名字变空白。
    name: backfillText(raw.name) || slug || `#${badgeId}`,
    status: status as AdminBadgeBackfillStatus,
    lastUserId: backfillCount(raw.lastUserId),
    failedCursorUserId: backfillCount(raw.failedCursorUserId),
    startedAt: backfillText(raw.startedAt),
    updatedAt: backfillText(raw.updatedAt),
    finishedAt: backfillText(raw.finishedAt),
    error: backfillText(raw.error)
  }
}

/**
 * `{ items: [...] }` 是这一刀的冻结形状，**不接受裸数组**：新端点没有历史包袱，
 * 两种形状都认会让后端改响应形状时前端安静地继续工作，契约漂移永远不红。
 *
 * 单行不合格只丢那一行（一条脏数据不该让整行进度消失）；整个 payload 不合格返回
 * `null`，由调用方降级成空清单 + 一条可见提示。
 */
export function normalizeBackfillStatusPayload(payload: unknown): AdminBadgeBackfillRun[] | null {
  const items = (payload as { items?: unknown } | null)?.items
  if (!Array.isArray(items)) return null
  return items.flatMap((row) => {
    const run = normalizeBackfillRun(row)
    return run ? [run] : []
  })
}

/** 这一行最近一次有动静的时间：完成时间优先，失败行没有 finishedAt 时用 updatedAt。 */
function backfillActivityMs(run: AdminBadgeBackfillRun): number {
  for (const value of [run.finishedAt, run.updatedAt, run.startedAt]) {
    const parsed = Date.parse(value)
    if (Number.isFinite(parsed)) return parsed
  }
  return 0
}

function summarizeBackfillGroup(
  status: AdminBadgeBackfillStatus,
  runs: readonly AdminBadgeBackfillRun[],
  userId: number
): AdminBadgeBackfillSummary {
  const badgeNames = runs.slice(0, MAX_BACKFILL_BADGE_NAMES).map(run => run.name)
  return {
    status,
    badgeNames,
    hiddenCount: Math.max(0, runs.length - badgeNames.length),
    userId
  }
}

/**
 * 把多条 run 收成**一行**状态（不是每枚徽章一行：这是进度提示，不是清单）。
 *
 * `null` 表示这一行不该渲染：没有任何 run，或者只有超过 24h 的 completed。
 */
export function summarizeBackfillRuns(
  runs: readonly AdminBadgeBackfillRun[],
  nowMs: number = Date.now()
): AdminBadgeBackfillSummary | null {
  if (runs.length === 0) return null

  // 最近有动静的排前面：失败行要展示「眼下卡在哪」，刚跑完的要展示刚跑完的那些。
  const byStatus = (status: AdminBadgeBackfillStatus) => runs
    .filter(run => run.status === status)
    .slice()
    .sort((left, right) => backfillActivityMs(right) - backfillActivityMs(left) || left.badgeId - right.badgeId)

  for (const status of BACKFILL_STATUS_ORDER) {
    const group = byStatus(status)
    if (group.length === 0) continue
    if (status === 'completed') {
      const finishedMs = backfillActivityMs(group[0]!)
      // 时间戳缺失（0）也算过期：渲染一条无法判断新鲜度的「已完成」不如不渲染。
      if (nowMs - finishedMs > BACKFILL_PROGRESS_TTL_MS) return null
      return summarizeBackfillGroup('completed', group, 0)
    }
    // 多条在跑时展示推进得最远的游标 —— 这一行回答「已经扫到哪」。
    const userId = status === 'running'
      ? Math.max(...group.map(run => run.lastUserId))
      : group[0]!.failedCursorUserId
    return summarizeBackfillGroup(status, group, userId)
  }
  return null
}

/**
 * 进度行文案的**唯一**决策点（与 `recomputeToastDescription` 同一套路）：
 * 组件只负责摆位，测试可以直接喂真实 locale 断言最终句子。
 */
export function backfillProgressCopy(
  summary: AdminBadgeBackfillSummary,
  translate: (key: string, params?: Record<string, unknown>) => string
): { title: string, description: string } {
  const titleKey = {
    running: 'admin.badges.backfill.running',
    completed: 'admin.badges.backfill.completed',
    failed: 'admin.badges.backfill.failed'
  }[summary.status]
  const title = translate(titleKey, { userId: summary.userId })
  const separator = translate('admin.badges.backfill.listSeparator')
  const names = summary.badgeNames.join(separator)
  if (names === '') return { title, description: '' }
  const suffix = summary.hiddenCount > 0
    ? ` ${translate('admin.badges.backfill.more', { count: summary.hiddenCount })}`
    : ''
  return { title, description: translate('admin.badges.backfill.detail', { names: `${names}${suffix}` }) }
}
