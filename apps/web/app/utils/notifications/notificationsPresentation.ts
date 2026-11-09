import type { NotificationActor, NotificationItem } from '~/composables/notifications/useNotifications'

export type NotificationFilter = 'all' | 'unread' | NotificationItem['type']
export type NotificationGroupKey = 'today' | 'earlier'

export type NotificationTarget = {
  path: string
  unavailable: boolean
}

export type NotificationPresentation = {
  id: number
  type: NotificationItem['type']
  read: boolean
  titleKey: string
  bodyKey: string
  bodyParams: Record<string, string | number>
  typeLabelKey: string
  targetLabel: string
  detailMeta: string
  icon: string
  actor?: NotificationActor
  target: NotificationTarget
  createdAt: string
  readAt?: string
}

export type NotificationDateGroup<T> = {
  key: NotificationGroupKey
  items: T[]
}

const notificationIcons: Record<NotificationItem['type'], string> = {
  reply: 'i-tabler-message-reply',
  mention: 'i-tabler-at',
  reaction: 'i-tabler-heart',
  moderation_pending: 'i-tabler-shield-exclamation',
  moderation_approved: 'i-tabler-shield-check',
  moderation_rejected: 'i-tabler-shield-x',
  admin_test: 'i-tabler-bell-ringing'
}

const userAuthoredTypes = new Set<NotificationItem['type']>(['reply', 'mention'])

export const notificationFilters: NotificationFilter[] = [
  'all',
  'unread',
  'reply',
  'mention',
  'reaction',
  'moderation_pending',
  'moderation_approved',
  'moderation_rejected',
  'admin_test'
]

export function notificationPresentation(item: NotificationItem): NotificationPresentation {
  const title = payloadString(item.payload, 'title')
  const reviewNote = payloadString(item.payload, 'reviewNote')
  const topicId = payloadNumber(item.payload, 'topicId')
  const commentId = payloadNumber(item.payload, 'commentId')
  const target = notificationTarget(item, topicId, commentId)
  const targetLabel = title || fallbackTargetLabel(item, topicId)
  const detailMeta = reviewNote || targetLabel

  return {
    id: item.id,
    type: item.type,
    read: Boolean(item.readAt),
    titleKey: `notifications.types.${item.type}`,
    bodyKey: bodyKeyForType(item.type, Boolean(targetLabel), Boolean(reviewNote)),
    bodyParams: {
      target: targetLabel,
      reviewNote
    },
    typeLabelKey: `notifications.filter.${item.type}`,
    targetLabel,
    detailMeta,
    icon: notificationIcons[item.type] || 'i-tabler-bell',
    actor: userAuthoredTypes.has(item.type) ? item.actor : undefined,
    target,
    createdAt: item.createdAt,
    readAt: item.readAt
  }
}

export function notificationTarget(item: NotificationItem, topicId = payloadNumber(item.payload, 'topicId'), commentId = payloadNumber(item.payload, 'commentId')): NotificationTarget {
  if (item.targetAvailable === false) {
    return { path: '/notifications', unavailable: true }
  }
  if (item.targetAvailable === true && item.targetPath?.startsWith('/')) {
    return { path: item.targetPath, unavailable: false }
  }
  if (topicId > 0) {
    return { path: `/t/${topicId}${commentId > 0 ? `#comment-${commentId}` : ''}`, unavailable: false }
  }
  if (item.targetType === 'topic' && item.targetId > 0) {
    return { path: `/t/${item.targetId}`, unavailable: false }
  }
  return { path: '/notifications', unavailable: true }
}

export function filterNotifications<T extends { type: NotificationItem['type'], read: boolean }>(items: T[], filter: NotificationFilter): T[] {
  if (filter === 'all') return items
  if (filter === 'unread') return items.filter(item => !item.read)
  return items.filter(item => item.type === filter)
}

export function groupNotificationsByDate<T extends { createdAt: string }>(
  items: T[],
  now: Date,
  timeZone: string,
  locale: string
): Array<NotificationDateGroup<T>> {
  const today = localDateKey(now, timeZone, locale)
  const grouped: Record<NotificationGroupKey, T[]> = {
    today: [],
    earlier: []
  }
  for (const item of items) {
    grouped[localDateKey(new Date(item.createdAt), timeZone, locale) === today ? 'today' : 'earlier'].push(item)
  }
  return (Object.entries(grouped) as Array<[NotificationGroupKey, T[]]>)
    .filter(([, groupItems]) => groupItems.length > 0)
    .map(([key, groupItems]) => ({ key, items: groupItems }))
}

export function unreadLoadedCount(items: Array<{ read: boolean }>) {
  return items.filter(item => !item.read).length
}

export function notificationFilterCounts(items: Array<{ type: NotificationItem['type'], read: boolean }>) {
  const counts = new Map<NotificationFilter, number>()
  counts.set('all', items.length)
  counts.set('unread', unreadLoadedCount(items))
  for (const item of items) {
    counts.set(item.type, (counts.get(item.type) || 0) + 1)
  }
  return counts
}

function bodyKeyForType(type: NotificationItem['type'], hasTarget: boolean, hasReviewNote: boolean) {
  if (type === 'admin_test') {
    return 'notifications.body.admin_test'
  }
  if (type === 'moderation_rejected' && hasReviewNote) {
    return 'notifications.body.moderation_rejected_note'
  }
  if (hasTarget) {
    return `notifications.body.${type}`
  }
  return 'notifications.body.noTarget'
}

function fallbackTargetLabel(item: NotificationItem, topicId: number) {
  if (topicId > 0) return `#${topicId}`
  if (item.targetType && item.targetId > 0) return `${item.targetType} #${item.targetId}`
  return ''
}

function payloadString(payload: Record<string, unknown>, key: string) {
  const value = payload[key]
  return typeof value === 'string' ? value.trim() : ''
}

function payloadNumber(payload: Record<string, unknown>, key: string) {
  const value = payload[key]
  const parsed = typeof value === 'number' ? value : Number.parseInt(String(value ?? ''), 10)
  return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : 0
}

function localDateKey(date: Date, timeZone: string, locale: string) {
  if (Number.isNaN(date.getTime())) {
    return ''
  }
  try {
    const parts = new Intl.DateTimeFormat(locale || 'zh-CN', {
      timeZone: timeZone || 'UTC',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit'
    }).formatToParts(date)
    const map = Object.fromEntries(parts.filter(part => part.type !== 'literal').map(part => [part.type, part.value]))
    return `${map.year}-${map.month}-${map.day}`
  } catch {
    return date.toISOString().slice(0, 10)
  }
}
