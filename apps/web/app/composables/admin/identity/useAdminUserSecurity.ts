import type { AdminUserEditorForm } from '~/composables/admin/identity/useAdminUserDetailEditor'

export type AdminUserSecurityDeps = {
  form: AdminUserEditorForm
  userId: () => number | null
  /** 保存类错误统一回抽屉顶部 + 字段位；这里不碰页面级红条与 error toast。 */
  presentError: (error: unknown, fallback: string) => void
  /** 纯 UX 判断（必填/两次一致）走字段位。 */
  setFieldError: (field: string, message: string) => void
  clearErrors: () => void
  notifySaved: (message: string) => void
}

/** 账号安全动作：重置密码、强制下线、清空真实 IP。 */
export function useAdminUserSecurity(deps: AdminUserSecurityDeps) {
  const { request } = useApiClient()
  const { t } = useI18n()

  const resettingPassword = ref(false)
  const revokingSessions = ref(false)
  const clearingClientIPs = ref(false)

  // 管理员直接设置目标用户密码（user.manage）；成功后目标全部设备会被下线。
  async function resetUserPassword() {
    const userId = deps.userId()
    if (!userId || resettingPassword.value) {
      return
    }
    const password = deps.form.newPassword
    if (!password) {
      deps.setFieldError('password', t('admin.users.passwordRequired'))
      return
    }
    if (password !== deps.form.confirmPassword) {
      deps.setFieldError('confirmPassword', t('admin.users.passwordMismatch'))
      return
    }
    resettingPassword.value = true
    deps.clearErrors()
    try {
      const result = await request<{ revokedSessions: number }>(`/users/${userId}/password`, {
        method: 'POST',
        body: { password }
      })
      deps.form.newPassword = ''
      deps.form.confirmPassword = ''
      deps.notifySaved(t('admin.users.passwordResetSuccess', { count: result.revokedSessions ?? 0 }))
    } catch (error) {
      deps.presentError(error, t('admin.users.passwordResetFailed'))
    } finally {
      resettingPassword.value = false
    }
  }

  // 管理员强制下线目标用户的全部设备（user.manage；禁止对自己操作）。
  async function revokeUserSessions() {
    const userId = deps.userId()
    if (!userId || revokingSessions.value) {
      return
    }
    revokingSessions.value = true
    deps.clearErrors()
    try {
      const result = await request<{ revoked: number }>(`/users/${userId}/sessions/revoke`, {
        method: 'POST'
      })
      deps.notifySaved(t('admin.users.sessionsRevoked', { count: result.revoked }))
    } catch (error) {
      deps.presentError(error, t('admin.users.revokeSessionsFailed'))
    } finally {
      revokingSessions.value = false
    }
  }

  // 隐私合规：清空该用户相关真实客户端 IP。
  async function clearUserClientIPs() {
    const userId = deps.userId()
    if (!userId || clearingClientIPs.value) {
      return
    }
    clearingClientIPs.value = true
    deps.clearErrors()
    try {
      await request(`/users/${userId}/client-ips/clear`, {
        method: 'POST'
      })
      deps.notifySaved(t('admin.users.clientIPsCleared'))
    } catch (error) {
      deps.presentError(error, t('admin.users.clearClientIPsFailed'))
    } finally {
      clearingClientIPs.value = false
    }
  }

  return {
    resettingPassword,
    revokingSessions,
    clearingClientIPs,
    resetUserPassword,
    revokeUserSessions,
    clearUserClientIPs
  }
}
