export type UserStatus = 'active' | 'disabled' | 'banned'
export type AdminUserSortField = 'createdAt' | 'updatedAt' | 'username' | 'displayName' | 'email' | 'status'
export type AdminUserSortOrder = 'asc' | 'desc'

export type Role = {
  id: number
  key: string
  alias: string
  description: string
  isSystem: boolean
  isDefault: boolean
  isDeletable: boolean
  isEnabled: boolean
  permissionKeys: string[]
}

export type Permission = {
  key: string
  module: string
  label: string
  description: string
}

export type AdminUserProfile = {
  bio: string
  signature: string
  location: string
  websiteUrl: string
}

export type AdminUserSummary = {
  id: number
  username: string
  email: string
  emailVerified: boolean
  displayName: string
  locale: string
  status: UserStatus
  isInitialSuperAdmin: boolean
  roleKeys: string[]
  createdAt?: string
  updatedAt?: string
}

export type PermissionOverrides = {
  allow: string[]
  deny: string[]
}

export type AdminUserActivity = {
  topicCount: number
  commentCount: number
  activeSessionCount: number
  totalSessionCount: number
  lastLoginAt?: string | null
  lastLoginIP: string
  lastLoginUserAgent: string
  lastSeenAt?: string | null
}

export type AdminSessionInspect = {
  id: string
  deviceName: string
  browser: string
  os: string
  ipPrefix: string
  ipAddress: string
  userAgent: string
  createdAt: string
  lastSeenAt: string
  isActive: boolean
  revokedAt?: string | null
  revokeReason: string
}

export type AdminAuthEvent = {
  id: number
  action: string
  ipAddress: string
  userAgent: string
  sessionHash: string
  createdAt: string
}

export type AdminUserDetail = AdminUserSummary & {
  /** 账号类型：human 参与论坛；bot 由系统驱动、不能登录。 */
  kind: 'human' | 'bot'
  permissions: string[]
  permissionOverrides: PermissionOverrides
  profile: AdminUserProfile
  /** 管理端预览：活跃度与最近登录线索（含完整 IP / UA） */
  activity?: AdminUserActivity
  /** 管理端预览：最近会话（完整 IP + 原始 UA） */
  sessions?: AdminSessionInspect[]
  /** 管理端预览：最近登录/注册审计 */
  recentAuthEvents?: AdminAuthEvent[]
  passwordChangedAt?: string | null
  /** 改名历史（新→旧），后台可见；历史表本身永久保留。 */
  nicknameHistory?: AdminNicknameChange[]
}

export type AdminNicknameChange = {
  id: number
  oldName: string
  newName: string
  source: 'self' | 'admin'
  changedBy?: number | null
  createdAt: string
}

export type AdminUserList = {
  items: AdminUserSummary[]
  total: number
  page: number
  perPage: number
}

/** 管理端创建用户的结果；generatedPassword 只在随机生成模式下出现一次。 */
export type AdminCreateUserResult = {
  user: AdminUserDetail
  generatedPassword?: string
}
