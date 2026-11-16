/**
 * 后台高级设置偏好（系统高级设置 Modal）。
 * 默认全部关闭；仅影响侧栏展示，不改服务端权限。
 * cookie 存 '1'/'0'，避免布尔序列化失真；SSR/客户端一致，避免水合闪烁。
 */
const COOKIE_MAX_AGE = 60 * 60 * 24 * 365

const PROFESSIONAL_MODE_COOKIE = 'sforum-admin-professional-mode'
const OPERATIONS_MODE_COOKIE = 'sforum-admin-operations-mode'

function parseFlagCookie(value: string | null | undefined) {
  return value === '1' || value === 'true'
}

function useAdminFlagCookie(key: string) {
  const cookie = useCookie<string>(key, {
    default: () => '0',
    maxAge: COOKIE_MAX_AGE,
    sameSite: 'lax',
    watch: true
  })

  return computed({
    get: () => parseFlagCookie(cookie.value),
    set: (value: boolean) => {
      cookie.value = value ? '1' : '0'
    }
  })
}

export function useAdminAdvancedSettings() {
  const professionalMode = useAdminFlagCookie(PROFESSIONAL_MODE_COOKIE)
  const operationsMode = useAdminFlagCookie(OPERATIONS_MODE_COOKIE)

  return {
    professionalMode,
    operationsMode
  }
}
