export type ProfileData = {
  userId: number
  bio: string
  signature: string
  location: string
  websiteUrl: string
  avatarAttachmentId?: number | null
  avatar: AvatarView
  createdAt: string
  updatedAt: string
}

export type AvatarView = {
  kind: 'uploaded' | 'initials' | 'gravatar' | 'static'
  url: string
  attachmentId?: number | null
  alt: string
}

export type ProfileExtensionTab = {
  extensionId: string
  id: string
  order: number
  label?: Record<string, string>
  icon?: string
  /**
   * widget 由宿主在资料页 tab 栏原地挂载 L2 组件（不发请求、不跳转）；
   * extensionRoute / hostLink 仍是右栏跳转入口。
   */
  kind: 'extensionRoute' | 'hostLink' | 'widget'
  /** 仅 widget 使用：本扩展 manifest 内声明的公开 L2 组件 id。 */
  componentId?: string
  method?: string
  /** widget 无跳转目标，后端按契约仍返回该字段（空串）。 */
  url: string
}

export type ProfileActivityTopic = {
  id: number
  slug: string
  title: string
  status: string
  categorySlug: string
  categoryName: string
  commentCount: number
  createdAt: string
  updatedAt: string
  lastActivityAt: string
}

export type ProfileActivity = {
  kind: 'topic' | 'comment'
  topic: ProfileActivityTopic
  commentId?: number | null
  /** 回复在主题 flat 分页中的页码；用于 /page/N#comment-{id} 深链。 */
  commentPage?: number | null
  excerpt: string
  createdAt: string
}

export type ProfileActivityKind = 'topic' | 'comment'

export type ProfileActivityPage = {
  items: ProfileActivity[]
  page: number
  perPage: number
  total: number
  hasMore: boolean
  kind: ProfileActivityKind
}

export type ListProfileActivitiesInput = {
  kind?: ProfileActivityKind
  page?: number
  perPage?: number
}

export type PublicProfile = {
  userId: number
  username: string
  displayName: string
  profile: ProfileData
  topicCount: number
  commentCount: number
  recentTopics: import('~/utils/forum/forumTaxonomy').ForumTopicSummary[]
  activities: ProfileActivity[]
  joinedAt: string
  extensionTabs?: ProfileExtensionTab[]
  /** 自助端点才有：昵称冷却与重名提示。 */
  nickname?: NicknameState
  /** 公开主页可选展示的最近一条曾用名。 */
  previousDisplayNames?: string[]
}

export type NicknameState = {
  displayName: string
  changedAt?: string | null
  nextChangeAt?: string | null
  canChange: boolean
  duplicates: number
}

export type NicknameAvailability = {
  available: boolean
  duplicates: number
  conflict?: string
  normalized?: string
}

export type UpdateProfileInput = {
  bio?: string
  signature?: string
  location?: string
  websiteUrl?: string
  avatarAttachmentId?: number | null
  displayName?: string
}

export function useProfileApi() {
  const { request } = useApiClient()

  function getPublicProfile(username: string) {
    return request<PublicProfile>(`/profiles/${encodeURIComponent(username)}`)
  }

  function listPublicActivities(username: string, input: ListProfileActivitiesInput = {}) {
    const query = new URLSearchParams()
    if (input.kind) {
      query.set('kind', input.kind)
    }
    if (input.page && input.page > 0) {
      query.set('page', String(input.page))
    }
    if (input.perPage && input.perPage > 0) {
      query.set('perPage', String(input.perPage))
    }
    const suffix = query.toString()
    return request<ProfileActivityPage>(
      `/profiles/${encodeURIComponent(username)}/activities${suffix ? `?${suffix}` : ''}`
    )
  }

  function getMyProfile() {
    return request<PublicProfile>('/profile')
  }

  function updateMyProfile(input: UpdateProfileInput) {
    // 返回完整 PublicProfile：改名后前端需要新的 displayName 与昵称状态，
    // 不应该再补一次 GET。
    return request<PublicProfile>('/profile', { method: 'PUT', body: input })
  }

  function checkNicknameAvailability(value: string) {
    const query = new URLSearchParams({ value })
    return request<NicknameAvailability>(`/profile/nickname/availability?${query.toString()}`)
  }

  function uploadAvatar(file: File) {
    const body = new FormData()
    body.append('file', file)
    return request<ProfileData>('/profile/avatar', { method: 'POST', body })
  }

  function deleteAvatar() {
    return request<ProfileData>('/profile/avatar', { method: 'DELETE' })
  }

  return {
    getPublicProfile,
    listPublicActivities,
    getMyProfile,
    updateMyProfile,
    checkNicknameAvailability,
    uploadAvatar,
    deleteAvatar
  }
}
