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
  /** 有字段级错误时的固定摘要：具体原因在各字段下面。 */
  fieldsSummary: string
  /** reason=user.update_empty：什么都没改。 */
  noChanges: string
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
  'confirmPassword'
] as const

/**
 * 把 API 错误折叠成「摘要 + 字段级消息」。
 * 字段名与文案的唯一事实来源是后端 `data.fields`，前端不做长度/格式的镜像校验，
 * 否则两套阈值迟早漂移。
 */
export function resolveAdminUserError(error: unknown, copy: AdminUserErrorCopy): AdminUserErrorPresentation {
  const fields = apiErrorFields(error)
  if (Object.keys(fields).length > 0) {
    return { summary: copy.fieldsSummary, fields }
  }
  if (apiErrorReason(error) === 'user.update_empty') {
    return { summary: copy.noChanges, fields: {} }
  }
  return { summary: apiErrorMessage(error) || copy.fallback, fields: {} }
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
