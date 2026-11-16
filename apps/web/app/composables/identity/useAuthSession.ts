import { apiErrorMessage, apiErrorReason } from '../useApiClient'
import { canWithUser } from '~/utils/identity/permissionCheck'
import type { AvatarView } from '../profile/useProfileApi'
import type { AppearanceTheme, LightBackgroundPreset } from '~/utils/settings/appearance'

export type CurrentUser = {
  id: number
  username: string
  displayName: string
  locale: string
  appearance: {
    theme: AppearanceTheme
    lightBackground: LightBackgroundPreset
  } | null
  status: 'active' | 'disabled' | 'banned'
  emailVerified: boolean
  isInitialSuperAdmin: boolean
  avatar: AvatarView
  roleKeys: string[]
  permissions: string[]
}

export type AuthSessionStatus = 'unknown' | 'authenticated' | 'guest' | 'unavailable'

export type AuthRefreshError = {
  message: string
  reason: string
  statusCode: number | null
}

type AuthRefreshOptions = {
  timeout?: number
  serverInternal?: boolean
}

export const useAuthSession = () => {
  const user = useState<CurrentUser | null>('auth:user', () => null)
  const pending = useState<boolean>('auth:pending', () => false)
  const status = useState<AuthSessionStatus>('auth:status', () => 'unknown')
  const lastRefreshError = useState<AuthRefreshError | null>('auth:last-refresh-error', () => null)
  const { request } = useApiClient()

  const refresh = async (options: AuthRefreshOptions = {}) => {
    pending.value = true
    try {
      user.value = await request<CurrentUser>('/auth/session', {
        timeout: options.timeout,
        serverInternal: options.serverInternal
      })
      status.value = 'authenticated'
      lastRefreshError.value = null
    } catch (error) {
      if (isUnauthenticatedAuthError(error)) {
        user.value = null
        status.value = 'guest'
        lastRefreshError.value = null
      } else {
        // API 重启或编译中的瞬时失败不等于退出登录，保留已有用户状态。
        status.value = 'unavailable'
        lastRefreshError.value = serializeAuthRefreshError(error)
      }
    } finally {
      pending.value = false
    }

    return user.value
  }

  const setUser = (currentUser: CurrentUser | null) => {
    user.value = currentUser
    status.value = currentUser ? 'authenticated' : 'guest'
    lastRefreshError.value = null
  }

  // 只查后端下发的已展开、已扣除集合；不得在此展开父权限（F-1，见 permissionCheck）。
  const can = (permission: string) => canWithUser(user.value, permission)

  return { user, pending, status, lastRefreshError, refresh, setUser, can }
}

export function isUnauthenticatedAuthError(error: unknown) {
  return authErrorStatusCode(error) === 401 || apiErrorReason(error) === 'auth.required'
}

function authErrorStatusCode(error: unknown) {
  const direct = asRecord(error)
  const response = asRecord(direct?.response)
  const data = asRecord(direct?.data)
  const responseData = asRecord(response?._data)
  const candidates = [
    direct?.status,
    direct?.statusCode,
    response?.status,
    data?.code,
    responseData?.code
  ]

  for (const candidate of candidates) {
    if (typeof candidate === 'number') {
      return candidate
    }
  }

  return undefined
}

function serializeAuthRefreshError(error: unknown): AuthRefreshError {
  const fallbackMessage = error instanceof Error ? error.message : ''

  return {
    message: apiErrorMessage(error) || fallbackMessage || 'auth.session_refresh_failed',
    reason: apiErrorReason(error) || '',
    statusCode: authErrorStatusCode(error) ?? null
  }
}

function asRecord(value: unknown) {
  return value && typeof value === 'object' ? value as Record<string, unknown> : undefined
}
