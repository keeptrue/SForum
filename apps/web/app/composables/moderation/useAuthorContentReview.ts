import { useApiClient } from '~/composables/useApiClient'
import type { AuthorReviewItem } from '~/utils/moderation/authorReview'

export type AuthorReviewList = {
  items: AuthorReviewItem[]
}

/**
 * 作者侧内容审核列表。
 *
 * 只读：后端只有 `GET /me/content-review`（登录态）。作者不能自行撤审或重提，
 * 所以这里不提供写操作，避免在客户端造出后端没有的语义。
 */
export function useAuthorContentReview() {
  const { request } = useApiClient()

  function list() {
    return request<AuthorReviewList>('/me/content-review')
  }

  return { list }
}
