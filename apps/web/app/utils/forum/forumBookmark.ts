import type { SiteDateTimeSettings } from '../siteDateTime'
import { formatSiteDateTime } from '../siteDateTime'

/**
 * 收藏的纯决策函数：可见性、提醒时间、到期判定、空状态与错误码文案选择。
 *
 * 组件只负责渲染与网络，状态计算留在这里，便于在没有 SFC 编译的环境里单测。
 * 所有时间在库内一律 UTC ISO；展示时才按站点时区/格式转成本地字符串。
 */

export type BookmarkVisibility = 'private' | 'public'
export type BookmarkTargetType = 'topic' | 'comment'
export type BookmarkRemindPreset = 'day1' | 'day3' | 'week1'

export const bookmarkVisibilities: BookmarkVisibility[] = ['private', 'public']

/** 收藏默认私密：公开是需要用户明确选择的动作，不做隐式公开。 */
export const recommendedBookmarkVisibility: BookmarkVisibility = 'private'

export const bookmarkRemindPresets: { id: BookmarkRemindPreset; days: number }[] = [
  { id: 'day1', days: 1 },
  { id: 'day3', days: 3 },
  { id: 'week1', days: 7 }
]

const MS_PER_DAY = 86400000

function trimmedLower(value: unknown): string {
  return typeof value === 'string' ? value.trim().toLowerCase() : ''
}

function parseDate(value: string | null | undefined): Date | null {
  if (!value) {
    return null
  }
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date
}

export function normalizeBookmarkVisibility(
  value: unknown,
  fallback: BookmarkVisibility = recommendedBookmarkVisibility
): BookmarkVisibility {
  const normalized = trimmedLower(value) as BookmarkVisibility
  return bookmarkVisibilities.includes(normalized) ? normalized : fallback
}

export function normalizeBookmarkTargetType(value: unknown): BookmarkTargetType | null {
  const normalized = trimmedLower(value)
  return normalized === 'topic' || normalized === 'comment' ? normalized : null
}

export function bookmarkVisibilityLabelKey(
  visibility: BookmarkVisibility
): 'bookmark.visibility.private' | 'bookmark.visibility.public' {
  return visibility === 'public' ? 'bookmark.visibility.public' : 'bookmark.visibility.private'
}

export function bookmarkVisibilityHintKey(
  visibility: BookmarkVisibility
): 'bookmark.visibility.privateHint' | 'bookmark.visibility.publicHint' {
  return visibility === 'public' ? 'bookmark.visibility.publicHint' : 'bookmark.visibility.privateHint'
}

export function bookmarkVisibilityIcon(
  visibility: BookmarkVisibility
): 'i-lucide-lock' | 'i-lucide-globe' {
  return visibility === 'public' ? 'i-lucide-globe' : 'i-lucide-lock'
}

export function bookmarkTargetTypeLabelKey(
  targetType: BookmarkTargetType
): 'bookmark.target.topic' | 'bookmark.target.comment' {
  return targetType === 'comment' ? 'bookmark.target.comment' : 'bookmark.target.topic'
}

export function bookmarkTargetTypeIcon(
  targetType: BookmarkTargetType
): 'i-lucide-file-text' | 'i-lucide-message-circle' {
  return targetType === 'comment' ? 'i-lucide-message-circle' : 'i-lucide-file-text'
}

/** 收藏按钮的 aria-label / title：未收藏时是「收藏」，已收藏时是「取消收藏」。 */
export function bookmarkActionLabelKey(
  bookmarked: boolean
): 'bookmark.action.add' | 'bookmark.action.remove' {
  return bookmarked ? 'bookmark.action.remove' : 'bookmark.action.add'
}

/** 预设提醒的相对天数；未知 id 回落到 1 天，避免算出 NaN 时间。 */
export function bookmarkRemindPresetDays(preset: BookmarkRemindPreset | string): number {
  return bookmarkRemindPresets.find(item => item.id === preset)?.days ?? 1
}

export function bookmarkRemindPresetLabelKey(preset: BookmarkRemindPreset | string): string {
  const id = bookmarkRemindPresets.find(item => item.id === preset)?.id ?? 'day1'
  return `bookmark.remind.preset.${id}`
}

/** 预设提醒 → UTC ISO。以传入的 now 为基准，便于测试固定时钟。 */
export function bookmarkRemindAtFromPreset(
  preset: BookmarkRemindPreset | string,
  now: Date = new Date()
): string {
  return new Date(now.getTime() + bookmarkRemindPresetDays(preset) * MS_PER_DAY).toISOString()
}

/** `<input type="datetime-local">` 的本地墙钟 → UTC ISO；空值/非法值返回 null。 */
export function bookmarkRemindAtFromLocalInput(value: string | null | undefined): string | null {
  const raw = (value || '').trim()
  if (!raw) {
    return null
  }
  const date = parseDate(raw)
  return date ? date.toISOString() : null
}

/** UTC ISO → `<input type="datetime-local">` 的本地墙钟值（浏览器时区）。 */
export function bookmarkRemindAtToLocalInput(remindAt: string | null | undefined): string {
  const date = parseDate(remindAt)
  if (!date) {
    return ''
  }
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

/** 提醒时间按站点时区/格式展示；空值或非法值返回空串。 */
export function formatBookmarkRemindAt(
  remindAt: string | null | undefined,
  settings: SiteDateTimeSettings,
  locale = 'zh-CN'
): string {
  if (!remindAt) {
    return ''
  }
  return formatSiteDateTime(remindAt, { settings, locale })
}

/** 到期判定：提醒时刻已到或已过。没有提醒不算到期。 */
export function isBookmarkRemindDue(
  remindAt: string | null | undefined,
  now: Date = new Date()
): boolean {
  const date = parseDate(remindAt)
  return date ? date.getTime() <= now.getTime() : false
}

export type BookmarkRemindRelative =
  | { kind: 'none' }
  | { kind: 'due' }
  | { kind: 'today' }
  | { kind: 'tomorrow' }
  | { kind: 'inDays'; days: number }

/** 站点时区下的日历日序号；时区非法时返回 null（调用方回落到「今天」）。 */
function calendarDay(date: Date, timeZone: string, locale: string): number | null {
  try {
    const parts = new Intl.DateTimeFormat(locale || 'en-US', {
      timeZone: timeZone || 'UTC',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit'
    }).formatToParts(date)
    const map = Object.fromEntries(
      parts.filter(part => part.type !== 'literal').map(part => [part.type, part.value])
    )
    const year = Number(map.year)
    const month = Number(map.month)
    const day = Number(map.day)
    if (!Number.isFinite(year) || !Number.isFinite(month) || !Number.isFinite(day)) {
      return null
    }
    return Math.floor(Date.UTC(year, month - 1, day) / MS_PER_DAY)
  } catch {
    return null
  }
}

/**
 * 提醒的相对描述：已到期 / 今天 / 明天 / 还有 N 天。
 * 按站点时区的日历日比较，而不是 UTC 日，否则跨时区会把「明天」说成「今天」。
 */
export function bookmarkRemindRelative(
  remindAt: string | null | undefined,
  now: Date,
  timeZone: string,
  locale = 'zh-CN'
): BookmarkRemindRelative {
  const date = parseDate(remindAt)
  if (!date) {
    return { kind: 'none' }
  }
  if (date.getTime() <= now.getTime()) {
    return { kind: 'due' }
  }
  const target = calendarDay(date, timeZone, locale)
  const base = calendarDay(now, timeZone, locale)
  if (target === null || base === null) {
    return { kind: 'today' }
  }
  const diff = target - base
  if (diff <= 0) {
    return { kind: 'today' }
  }
  if (diff === 1) {
    return { kind: 'tomorrow' }
  }
  return { kind: 'inDays', days: diff }
}

export function bookmarkRemindRelativeKey(relative: BookmarkRemindRelative): string {
  switch (relative.kind) {
    case 'due':
      return 'bookmark.remind.relative.due'
    case 'today':
      return 'bookmark.remind.relative.today'
    case 'tomorrow':
      return 'bookmark.remind.relative.tomorrow'
    case 'inDays':
      return 'bookmark.remind.relative.inDays'
    default:
      return 'bookmark.remind.none'
  }
}

export function bookmarkRemindRelativeParams(relative: BookmarkRemindRelative): Record<string, number> {
  return relative.kind === 'inDays' ? { days: relative.days } : {}
}

export type BookmarkEmptyScope = 'self' | 'public'

export type BookmarkEmptyStateCopy = {
  title: string
  description: string
  icon: 'i-lucide-bookmark'
}

/** 空状态文案：本人视角是「去收藏」，他人视角是「对方还没公开」。 */
export function bookmarkEmptyStateCopy(scope: BookmarkEmptyScope): BookmarkEmptyStateCopy {
  return scope === 'public'
    ? {
        title: 'bookmark.empty.publicTitle',
        description: 'bookmark.empty.publicDescription',
        icon: 'i-lucide-bookmark'
      }
    : {
        title: 'bookmark.empty.selfTitle',
        description: 'bookmark.empty.selfDescription',
        icon: 'i-lucide-bookmark'
      }
}

/** 收藏接口错误码 → i18n key。status 缺失按 unknown 处理。 */
export function bookmarkErrorKey(status: number | null | undefined): string {
  if (status === 401 || status === 403) {
    return 'bookmark.error.forbidden'
  }
  if (status === 404) {
    return 'bookmark.error.notFound'
  }
  if (status === 429) {
    return 'bookmark.error.rateLimited'
  }
  if (status === 501 || status === 503) {
    return 'bookmark.error.unavailable'
  }
  return 'bookmark.error.unknown'
}

/** 自定义提醒的下界：必须晚于「现在 +1 分钟」，避免刚提交就过期。 */
export const BOOKMARK_REMIND_MIN_LEAD_MS = 60_000

/**
 * 自定义提醒的上界：now + 729 天。
 *
 * 后端上限是 730 个日历天，且以服务端 now 判定；前端只能按 24h×天算，
 * 跨闰年（日历天 ≠ 24h×天）加上提交延迟，恰好「730 天」就可能被判成 422 too_far。
 * 这里少给 1 天是给服务端时钟与日历差留的余量，不是产品规则变更。
 */
export const BOOKMARK_REMIND_MAX_AHEAD_MS = 729 * 86400000

export type BookmarkRemindBounds = {
  /** `<input type="datetime-local">` 需要的本地墙钟值。 */
  minInput: string
  maxInput: string
}

/** 输入框的 min/max：随 now 变化，调用方在打开设置面板时刷新一次即可。 */
export function bookmarkRemindBounds(now: Date = new Date()): BookmarkRemindBounds {
  return {
    minInput: bookmarkRemindAtToLocalInput(new Date(now.getTime() + BOOKMARK_REMIND_MIN_LEAD_MS).toISOString()),
    maxInput: bookmarkRemindAtToLocalInput(new Date(now.getTime() + BOOKMARK_REMIND_MAX_AHEAD_MS).toISOString())
  }
}

export type BookmarkRemindIssue = 'in_past' | 'too_far' | null

/**
 * 提交前本地校验：过去时间与超上限都在前端挡掉，不打到后端吃 422。
 * 空值不是错误——它表示「清除提醒」，走 clearReminder 语义。
 */
export function bookmarkRemindIssue(
  value: string | null | undefined,
  now: Date = new Date()
): BookmarkRemindIssue {
  const date = parseDate(value)
  if (!date) {
    return null
  }
  if (date.getTime() < now.getTime() + BOOKMARK_REMIND_MIN_LEAD_MS) {
    return 'in_past'
  }
  if (date.getTime() > now.getTime() + BOOKMARK_REMIND_MAX_AHEAD_MS) {
    return 'too_far'
  }
  return null
}

/** 本地校验结果 → 字段级文案 key；无问题返回空串。 */
export function bookmarkRemindIssueKey(issue: BookmarkRemindIssue): string {
  switch (issue) {
    case 'in_past':
      return 'bookmark.error.inPast'
    case 'too_far':
      return 'bookmark.error.tooFar'
    default:
      return ''
  }
}

/**
 * 后端具名原因 → i18n key。未知原因返回空串，调用方回落到 HTTP 状态码映射。
 *
 * 后端 422 用 `*_in_past` / `*_too_far` / `*_limit` 这类后缀表达时间边界，
 * 这里按后缀匹配，避免把完整 reason 常量抄到前端再和它失联。
 */
export function bookmarkReasonKey(reason: string | null | undefined): string {
  const normalized = (reason || '').trim().toLowerCase()
  if (!normalized) {
    return ''
  }
  if (normalized.includes('in_past')) {
    return 'bookmark.error.inPast'
  }
  if (normalized.includes('too_far')) {
    return 'bookmark.error.tooFar'
  }
  if (normalized.includes('limit')) {
    return 'bookmark.error.limit'
  }
  if (normalized.includes('bookmark_target_invalid')) {
    return 'bookmark.error.notFound'
  }
  if (normalized.includes('bookmarks_disabled') || normalized.includes('bookmarks_unavailable')) {
    return 'bookmark.error.unavailable'
  }
  return ''
}

/**
 * 提醒状态文案：`remindedAt` 有值 = 已提醒；缺失 = 仍在等待（将提醒）。
 * 两者不能共用「提醒中」，否则用户分不清「已经响过」和「还没到点」。
 */
export function bookmarkRemindStatusKey(
  remindedAt: string | null | undefined
): 'bookmark.remind.status.done' | 'bookmark.remind.status.pending' {
  return remindedAt ? 'bookmark.remind.status.done' : 'bookmark.remind.status.pending'
}
