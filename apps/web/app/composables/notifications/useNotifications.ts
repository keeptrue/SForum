import { createNotificationRealtimeClient, type NotificationRevisionRefresh } from './notificationRealtime'

export type NotificationActor = {
  id: number
  username: string
  displayName: string
  avatar: {
    kind: 'uploaded' | 'initials' | 'gravatar' | 'static'
    url: string
    attachmentId?: number | null
    alt: string
  }
}

export type NotificationItem = {
  id: number
  type: string
  category?: string
  typeVersion?: number
  payloadVersion?: number
  actorUserId?: number
  actor?: NotificationActor
  targetType: string
  targetId: number
  targetAvailable?: boolean
  targetPath?: string
  payload: Record<string, unknown>
  readAt?: string
  createdAt: string
}

export type NotificationPreviewAuthor = NotificationActor

export type NotificationPreviewContent = {
  type: 'topic' | 'comment'
  id: number
  excerpt: string
  author?: NotificationPreviewAuthor
}

export type NotificationDetail = NotificationItem & {
  preview?: {
    topicId: number
    topicTitle: string
    content: NotificationPreviewContent
    context?: NotificationPreviewContent
  }
}

export type NotificationListFilters = {
  category?: string
  type?: string
  unread?: boolean
}

const notificationRealtime = createNotificationRealtimeClient()

export function useNotifications() {
  const { apiBaseUrl, request } = useApiClient()
  const unreadCount = useState<number>('notification-unread-count', () => 0)
  const revision = useState<number>('notification-recipient-revision', () => 0)
  // RV-33：预览弹层与详情页会对同一条通知各调一次 markRead。服务端 PATCH 本身幂等，
  // 本地角标也必须幂等——按 id 记账，同一 id 只递减一次（跨组件共享同一份 state）。
  const locallyReadIds = useState<number[]>('notification-locally-read-ids', () => [])
  async function refreshUnreadCount() { const data = await request<{ count: number }>('/notifications/unread-count'); unreadCount.value = data.count; return data.count }
  async function list(beforeId = 0, limit = 20, filters: NotificationListFilters = {}) {
    const params = new URLSearchParams()
    if (beforeId > 0) params.set('beforeId', String(beforeId))
    if (limit > 0) params.set('limit', String(limit))
    if (filters.category) params.set('category', filters.category)
    if (filters.type) params.set('type', filters.type)
    if (filters.unread !== undefined) params.set('unread', String(filters.unread))
    const query = params.toString()
    return request<{ items: NotificationItem[], hasMore: boolean }>(`/notifications${query ? `?${query}` : ''}`)
  }
  async function get(id: number) {
    return request<NotificationDetail>(`/notifications/${id}`, { serverInternal: import.meta.server })
  }
  async function markRead(id: number) {
    await request(`/notifications/${id}/read`, { method: 'PATCH' })
    if (locallyReadIds.value.includes(id)) return
    locallyReadIds.value = [...locallyReadIds.value, id]
    if (unreadCount.value > 0) unreadCount.value--
  }
  async function markAllRead() { const data = await request<{ updated: number }>('/notifications/read-all', { method: 'POST' }); unreadCount.value = 0; return data.updated }

  function startRealtime(actorUserId: number, refresh: NotificationRevisionRefresh) {
    if (import.meta.server) return () => {}
    return notificationRealtime.subscribe({ actorUserId, apiBaseUrl, revision, refresh })
  }

  function stopRealtime() {
    notificationRealtime.stopAll()
  }

  return { unreadCount, revision, refreshUnreadCount, list, get, markRead, markAllRead, startRealtime, stopRealtime }
}

if (import.meta.hot) {
  import.meta.hot.dispose(() => notificationRealtime.stopAll())
}
