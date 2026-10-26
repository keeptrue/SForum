import { apiErrorFields, apiErrorMessage, apiErrorReason } from '~/composables/useApiClient'

/**
 * 详情抽屉保存失败时展示给操作者的错误形态。
 * `summary` 是抽屉顶部的摘要，`fields` 直接喂给各字段的 UFormField :error。
 */
export interface AdminUserErrorPresentation {
  summary: string
  fields: Record<string, string[]>
}

export interface AdminUserErrorCopy {
  /** 后端既没有 fields 也没有可用 message 时的兜底文案。 */
  fallback: string
  /** 有字段级错误时的摘要：点名出错的字段，具体原因仍落在各字段下面。 */
  fieldsSummary: (fields: Record<string, string[]>) => string
  /** reason=user.update_empty：什么都没改。 */
  noChanges: string
}

/** 字段名 → admin.users 里的标签 key，供摘要点名使用。 */
export const ADMIN_USER_FIELD_LABEL_KEYS: Record<string, string> = {
  username: 'admin.users.usernameField',
  email: 'admin.users.email',
  displayName: 'admin.users.displayName',
  locale: 'admin.users.locale',
  status: 'admin.users.status',
  kind: 'admin.users.kind',
  bio: 'admin.users.bio',
  signature: 'admin.users.signature',
  location: 'admin.users.location',
  websiteUrl: 'admin.users.websiteUrl',
  password: 'admin.users.newPassword',
  confirmPassword: 'admin.users.confirmPassword',
  roleKeys: 'admin.users.roles'
}

/**
 * 后端字段名 → 抽屉里的表单字段。顺序即「第一个出错字段」的优先级：
 * 与保存按钮下方字段的视觉顺序一致，聚焦时不会跳到视野外的字段。
 */
export const ADMIN_USER_FIELD_ORDER = [
  'username',
  'email',
  'displayName',
  'locale',
  'status',
  'kind',
  'bio',
  'signature',
  'location',
  'websiteUrl',
  'password',
  'confirmPassword',
  'roleKeys'
] as const

/**
 * 把 API 错误折叠成「摘要 + 字段级消息」。
 * 字段名与文案的唯一事实来源是后端 `data.fields`，前端不做长度/格式的镜像校验，
 * 否则两套阈值迟早漂移。
 */
export function resolveAdminUserError(error: unknown, copy: AdminUserErrorCopy): AdminUserErrorPresentation {
  const fields = apiErrorFields(error)
  if (Object.keys(fields).length > 0) {
    return { summary: copy.fieldsSummary(fields), fields }
  }
  if (apiErrorReason(error) === 'user.update_empty') {
    return { summary: copy.noChanges, fields: {} }
  }
  return { summary: apiErrorMessage(error) || copy.fallback, fields: {} }
}

/**
 * 摘要里点名的字段：按表单视觉顺序取前 `limit` 个，其余折叠成「等 N 项」。
 * 抽屉是长表单，错误分散在四个分区，只说「请按字段提示修改」等于没说。
 */
export function adminUserErrorFieldNames(
  fields: Record<string, string[]>,
  limit = 3
): { names: string[], extra: number } {
  const ordered: string[] = []
  for (const name of ADMIN_USER_FIELD_ORDER) {
    if ((fields[name]?.length ?? 0) > 0) {
      ordered.push(name)
    }
  }
  for (const name of Object.keys(fields)) {
    if (!ordered.includes(name)) {
      ordered.push(name)
    }
  }
  return { names: ordered.slice(0, limit), extra: Math.max(0, ordered.length - limit) }
}

/** 按表单视觉顺序取第一个出错字段，用于保存失败后滚动并聚焦。 */
export function firstAdminUserErrorField(fields: Record<string, string[]>): string | null {
  for (const name of ADMIN_USER_FIELD_ORDER) {
    if ((fields[name]?.length ?? 0) > 0) {
      return name
    }
  }
  const [fallback] = Object.keys(fields)
  return fallback ?? null
}
