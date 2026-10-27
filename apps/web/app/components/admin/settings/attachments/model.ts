export type AttachmentOwner = {
  id: number
  username: string
  displayName: string
}

export type AdminAttachment = {
  id: number
  publicId: string
  owner?: AttachmentOwner
  provider: string
  objectKey: string
  name: string
  contentType: string
  extension: string
  size: number
  sha256: string
  imageWidth?: number
  imageHeight?: number
  visibility: string
  status: string
  referenceCount: number
  url: string
  createdAt: string
  deletedAt?: string
}

export type AttachmentReference = {
  id: number
  attachmentId: number
  resourceType: string
  resourceId: number
  context: string
  createdAt: string
}

export type AttachmentDetail = AdminAttachment & {
  references: AttachmentReference[]
}

export type AttachmentList = {
  items: AdminAttachment[]
  total: number
  page: number
  perPage: number
}

export type AttachmentCleanupResult = {
  deleted: number
  failed: number
  /** 宽限期内未处理、到期后自动清理的孤儿附件数；旧帧无此字段。 */
  skipped?: number
  /** 宽限期天数，随清理结果一起返回；旧帧无此字段。 */
  retentionDays?: number
}

export type AttachmentRecountResult = {
  lowered: number
  raised: number
  unchanged: number
  applied: boolean
  /** 受影响附件主键示例（最多 20 条）。 */
  samples: number[]
}

export type AttachmentFilters = {
  query: string
  provider: string
  status: string
  contentType: string
  referenceStatus: string
}

export function buildAttachmentListQuery(list: Pick<AttachmentList, 'page' | 'perPage'>, filters: AttachmentFilters) {
  const params = new URLSearchParams({
    page: String(list.page || 1),
    perPage: String(list.perPage || 20)
  })
  for (const [key, value] of Object.entries(filters)) {
    if (value) params.set(key, value)
  }
  return params.toString()
}

export function humanFileSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

export function attachmentStatusColor(status: string): 'success' | 'warning' | 'neutral' {
  if (status === 'active') return 'success'
  if (status === 'disabled') return 'warning'
  return 'neutral'
}
