/**
 * 作者侧「我的内容 → 待审 / 被退回」的纯逻辑。
 *
 * 事实来源：`GET /api/v1/me/content-review`（`listAuthorContentReview`）。
 * 后端按作者 UNION 查询 `status IN ('pending','rejected')` 的主题与评论，并用
 * LATERAL 取该目标最近一条 `pre_publish` 审核决定的 `review_note`。
 *
 * 这里只做展示侧派生：分桶、链接目标、审核意见可见性。不做任何改写或补齐，
 * 因为 `reviewNote` 为空本身是事实（待审通常还没有决定，被退回也可能没写理由）。
 */

export type AuthorReviewTargetType = 'topic' | 'comment'
export type AuthorReviewStatus = 'pending' | 'rejected'

export type AuthorReviewItem = {
  targetType: AuthorReviewTargetType
  targetId: number
  /** 评论项带所属主题 id；主题项与 targetId 相同（后端 omitempty）。 */
  topicId?: number
  title: string
  excerpt: string
  status: AuthorReviewStatus
  reviewNote: string
  createdAt: string
}

export type AuthorReviewSections = {
  pending: AuthorReviewItem[]
  rejected: AuthorReviewItem[]
}

/**
 * 按状态分成两个列表。契约里的 status 只有 pending / rejected，
 * 未知状态不猜、不并入任何一桶（宁可少显示，也不把状态标错）。
 */
export function authorReviewSections(items: readonly AuthorReviewItem[] | undefined | null): AuthorReviewSections {
  const sections: AuthorReviewSections = { pending: [], rejected: [] }
  for (const item of items || []) {
    if (item?.status === 'pending') sections.pending.push(item)
    else if (item?.status === 'rejected') sections.rejected.push(item)
  }
  return sections
}

/**
 * 内容在站内的目标地址。评论落到主题的评论锚点，与通知中心的目标规则一致。
 * 返回空串表示无法安全定位（例如评论缺所属主题），调用方应渲染纯文本而不是死链。
 */
export function authorReviewTargetPath(item: Pick<AuthorReviewItem, 'targetType' | 'targetId' | 'topicId'>): string {
  if (item.targetType === 'topic') {
    return item.targetId > 0 ? `/t/${item.targetId}` : ''
  }
  if (item.targetType === 'comment') {
    if (!(item.topicId && item.topicId > 0) || !(item.targetId > 0)) return ''
    return `/t/${item.topicId}#comment-${item.targetId}`
  }
  return ''
}

/** 被退回项必须给出审核意见位置；没有意见时也要说明「没有」，而不是留空白。 */
export function authorReviewReviewNote(item: Pick<AuthorReviewItem, 'reviewNote'>): string {
  return (item.reviewNote || '').trim()
}
