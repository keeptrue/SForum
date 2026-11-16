export type ModerationTargetType = 'topic' | 'comment'
export type ModerationReasonCode = 'spam' | 'abuse' | 'illegal' | 'off_topic' | 'other'
export type ModerationReportStatus = 'open' | 'reviewing' | 'resolved' | 'rejected'
export type ModerationMode = 'off' | 'rules' | 'all'
export type ModerationSource = 'pre_publish' | 'report'
export type ModerationAction = 'approve' | 'reject' | 'keep_and_close' | 'hide_and_close' | 'delete_and_close'

export type ModerationSettings = {
  mode: ModerationMode
  reviewNewUsers: boolean
  newUserMaxAgeDays: number
  reviewExternalLinks: boolean
  updatedByUserId?: number | null
  updatedAt?: string
}

export type ModerationReport = {
  id: number
  reporterUserId: number
  reporterName?: string
  targetType: ModerationTargetType
  targetId: number
  reasonCode: ModerationReasonCode
  body: string
  status: ModerationReportStatus
  /** 乐观锁版本：每次成功更新 +1，PATCH 时必须原样回传。 */
  revision: number
  reviewerUserId?: number | null
  reviewerName?: string
  reviewNote: string
  createdAt: string
  updatedAt: string
  resolvedAt?: string | null
}

export type ModerationQueueCounts = {
  pendingContent: number
  openReports: number
  processedToday: number
  /** 处理记录全量条数；历史 tab 徽章用这个，不要用 processedToday。 */
  historyTotal: number
}
export type ModerationPendingItem = {
  targetType: ModerationTargetType
  targetId: number
  topicId?: number
  title: string
  excerpt: string
  authorId: number
  authorName: string
  category: string
  triggers: string[]
  createdAt: string
  /** 仅当当前用户持有 moderation.view_ip 时由 API 返回全文 IP。 */
  ipAddress?: string
  lastEditIp?: string
}
export type ModerationReportItem = ModerationReport & {
  title: string
  excerpt: string
  targetAuthorId: number
  targetAuthorName: string
  category: string
  targetStatus: string
  targetTopicId?: number
  ipAddress?: string
  lastEditIp?: string
}
export type ModerationDecision = {
  id: number
  source: ModerationSource
  targetType: ModerationTargetType
  targetId: number
  reportId?: number | null
  action: ModerationAction
  reviewerUserId: number
  reviewerName: string
  reviewNote: string
  triggers: string[]
  createdAt: string
}
export type ModerationReviewContext = {
  source: ModerationSource
  targetType: ModerationTargetType
  targetId: number
  topicId: number
  reportId?: number
  title: string
  html: string
  authorId: number
  authorName: string
  category: string
  status: string
  triggers: string[]
  parentTopic?: string
  createdAt: string
  /** 仅当当前用户持有 moderation.view_ip 时由 API 返回全文 IP。 */
  ipAddress?: string
  lastEditIp?: string
}
export type PagedModerationList<T> = { items: T[]; total: number; page: number; perPage: number }
export type ModerationReportList = PagedModerationList<ModerationReport>
export type ModerationWorkbenchFilters = { targetType?: ModerationTargetType; page?: number; perPage?: number }
export type ModerationHistoryFilters = ModerationWorkbenchFilters & { action?: ModerationAction; reviewerId?: number }

function queryString(filters: Record<string, string | number | undefined>) {
  const query = new URLSearchParams()
  for (const [key, value] of Object.entries(filters)) {
    if (value !== undefined && value !== '') query.set(key, String(value))
  }
  const value = query.toString()
  return value ? `?${value}` : ''
}

export function useModerationApi() {
  const { request } = useApiClient()

  const createReport = (input: { targetType: ModerationTargetType; targetId: number; reasonCode: ModerationReasonCode; body?: string }) =>
    request<ModerationReport>('/moderation/reports', { method: 'POST', body: input })
  const listReports = (filters: ModerationWorkbenchFilters & { status?: ModerationReportStatus; reporterId?: number } = {}) =>
    request<ModerationReportList>(`/admin/moderation/reports${queryString(filters)}`)
  // NF-02：`expectedRevision` 是必填的——缺省会让 API 走「不做 CAS」分支，
  // 乐观锁就白装了（上一轮实测：函数零调用点 + 不发版本号 = 并发保护空转）。
  const updateReport = (reportId: number, input: { status: ModerationReportStatus; reviewNote?: string; expectedRevision: number }) =>
    request<ModerationReport>(`/admin/moderation/reports/${reportId}`, { method: 'PATCH', body: input })

  const getSettings = () => request<ModerationSettings>('/admin/moderation/settings')
  const updateSettings = (input: ModerationSettings) => request<ModerationSettings>('/admin/moderation/settings', { method: 'PUT', body: input })
  const resetSettings = () => request<ModerationSettings>('/admin/moderation/settings/reset', { method: 'POST' })
  const getCounts = () => request<ModerationQueueCounts>('/moderation/workbench/counts')
  const listPending = (filters: ModerationWorkbenchFilters = {}) => request<PagedModerationList<ModerationPendingItem>>(`/moderation/workbench/pending${queryString(filters)}`)
  const listReportItems = (filters: ModerationWorkbenchFilters = {}) => request<PagedModerationList<ModerationReportItem>>(`/moderation/workbench/reports${queryString(filters)}`)
  const listHistory = (filters: ModerationHistoryFilters = {}, admin = false) => request<PagedModerationList<ModerationDecision>>(`${admin ? '/admin/moderation/decisions' : '/moderation/workbench/history'}${queryString(filters)}`)
  const getContext = (source: ModerationSource, targetType: ModerationTargetType, targetId: number, reportId?: number) => request<ModerationReviewContext>(`/moderation/workbench/context/${targetType}/${targetId}${queryString({ source, reportId })}`)
  const submitDecision = (input: { source: ModerationSource; targetType: ModerationTargetType; targetId: number; reportId?: number; action: ModerationAction; reviewNote?: string }) => request<ModerationDecision>('/moderation/workbench/decisions', { method: 'POST', body: input })

  return { createReport, listReports, updateReport, getSettings, updateSettings, resetSettings, getCounts, listPending, listReportItems, listHistory, getContext, submitDecision }
}
