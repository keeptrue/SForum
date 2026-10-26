import type { Ref } from 'vue'
import type { AdminUserDetail, AdminUserSummary, UserStatus } from '~/utils/admin/adminUsers'
import { apiErrorMessage } from '~/composables/useApiClient'
import {
  ADMIN_USER_FIELD_LABEL_KEYS,
  adminUserErrorFieldNames,
  firstAdminUserErrorField,
  resolveAdminUserError
} from '~/utils/admin/adminUserFieldErrors'

export type AdminUserEditorForm = {
  username: string
  email: string
  displayName: string
  locale: string
  status: UserStatus
  kind: 'human' | 'bot'
  bio: string
  signature: string
  location: string
  websiteUrl: string
  newPassword: string
  confirmPassword: string
}

export type AdminUserDetailEditorDeps = {
  /** 保存成功走 Toast：抽屉里不再放成功横幅。 */
  notifySaved: (message: string) => void
  /** 账户/用户组/权限改动会影响列表行，保存后让页面刷新。 */
  refreshList: () => Promise<void>
}

export type AdminUserDetailEditor = ReturnType<typeof useAdminUserDetailEditor>

/**
 * 详情抽屉的编辑状态：选中用户、表单草稿、保存动作，以及「错误进抽屉」的统一出口。
 *
 * 错误策略：字段级原因只认后端 `data.fields`（已本地化），前端不做长度/格式的镜像校验，
 * 否则两套阈值迟早漂移。这里的 summary 只是摘要与兜底。
 */
export function useAdminUserDetailEditor(deps: AdminUserDetailEditorDeps) {
  const { request } = useApiClient()
  const { t } = useI18n()

  const drawerOpen = ref(false)
  const detailPending = ref(false)
  const detailError = ref('')
  const detailTarget = ref<AdminUserSummary | null>(null)
  const selectedUser = ref<AdminUserDetail | null>(null)
  const selectedRoleKeys = ref<string[]>([])
  const allowOverrides = ref<string[]>([])
  const denyOverrides = ref<string[]>([])

  const form = reactive<AdminUserEditorForm>({
    username: '',
    email: '',
    displayName: '',
    locale: 'zh-CN',
    status: 'active',
    kind: 'human',
    bio: '',
    signature: '',
    location: '',
    websiteUrl: '',
    newPassword: '',
    confirmPassword: ''
  })

  const savingAccount = ref(false)
  const savingProfile = ref(false)
  const savingRoles = ref(false)
  const savingOverrides = ref(false)
  const drawerError = ref('')
  const fieldErrors = ref<Record<string, string[]>>({})
  // 递增即请求「聚焦第一个出错字段」，实际滚动/聚焦由抽屉组件做（它持有 DOM）。
  const errorFocusRequest = ref(0)

  let detailRequestToken = 0

  const selectedUserIsSuperAdmin = computed(() => {
    return selectedUser.value?.roleKeys.includes('super_admin') ?? false
  })

  function sameStringSet(left: string[], right: string[]) {
    if (left.length !== right.length) {
      return false
    }
    const rightSet = new Set(right)
    return left.every(item => rightSet.has(item))
  }

  // 未改动就不发请求：后端把「全量提交但值没变」判成 user.update_empty，
  // 而抽屉每次都提交整组字段，所以「有没有改」只能在前端按载入值比对。
  const accountDirty = computed(() => {
    const detail = selectedUser.value
    if (!detail) {
      return false
    }
    return form.username.trim() !== detail.username
      || form.email.trim() !== detail.email
      || form.displayName.trim() !== detail.displayName
      || form.locale !== (detail.locale || 'zh-CN')
      || form.status !== detail.status
      || form.kind !== (detail.kind === 'bot' ? 'bot' : 'human')
  })

  const profileDirty = computed(() => {
    const detail = selectedUser.value
    if (!detail) {
      return false
    }
    return form.bio !== (detail.profile?.bio ?? '')
      || form.signature !== (detail.profile?.signature ?? '')
      || form.location !== (detail.profile?.location ?? '')
      || form.websiteUrl.trim() !== (detail.profile?.websiteUrl ?? '')
  })

  const rolesDirty = computed(() => {
    const detail = selectedUser.value
    return detail ? !sameStringSet(selectedRoleKeys.value, detail.roleKeys) : false
  })

  const overridesDirty = computed(() => {
    const detail = selectedUser.value
    if (!detail) {
      return false
    }
    return !sameStringSet(allowOverrides.value, detail.permissionOverrides.allow)
      || !sameStringSet(denyOverrides.value, detail.permissionOverrides.deny)
  })

  function clearErrors() {
    drawerError.value = ''
    fieldErrors.value = {}
  }

  function clearFieldError(name: string) {
    if (!fieldErrors.value[name]) {
      return
    }
    const next = { ...fieldErrors.value }
    delete next[name]
    fieldErrors.value = next
  }

  function setFieldError(name: string, message: string) {
    drawerError.value = ''
    fieldErrors.value = { [name]: [message] }
    errorFocusRequest.value += 1
  }

  // 摘要点名出错字段：抽屉是长表单，只说「请按字段提示修改」等于没说。
  function describeFieldErrors(fields: Record<string, string[]>) {
    const { names, extra } = adminUserErrorFieldNames(fields)
    const labels = names.map(name => t(ADMIN_USER_FIELD_LABEL_KEYS[name] ?? name))
    const list = labels.join(t('admin.users.fieldListSeparator'))
    return extra > 0 ? `${list}${t('admin.users.fieldListMore', { count: extra })}` : list
  }

  function presentError(error: unknown, fallback: string) {
    const presentation = resolveAdminUserError(error, {
      fallback,
      fieldsSummary: fields => t('admin.users.fieldFallback', { fields: describeFieldErrors(fields) }),
      noChanges: t('admin.users.noChanges')
    })
    drawerError.value = presentation.summary
    fieldErrors.value = presentation.fields
    if (Object.keys(presentation.fields).length > 0) {
      errorFocusRequest.value += 1
    }
  }

  function applyDetailToForm(detail: AdminUserDetail) {
    selectedUser.value = detail
    selectedRoleKeys.value = [...detail.roleKeys]
    allowOverrides.value = [...detail.permissionOverrides.allow]
    denyOverrides.value = [...detail.permissionOverrides.deny]
    form.username = detail.username
    form.email = detail.email
    form.displayName = detail.displayName
    form.locale = detail.locale || 'zh-CN'
    form.status = detail.status
    form.kind = detail.kind === 'bot' ? 'bot' : 'human'
    form.bio = detail.profile?.bio ?? ''
    form.signature = detail.profile?.signature ?? ''
    form.location = detail.profile?.location ?? ''
    form.websiteUrl = detail.profile?.websiteUrl ?? ''
    // 切换用户时清空密码表单，避免误提交到下一位。
    form.newPassword = ''
    form.confirmPassword = ''
  }

  // 详情加载失败也要开抽屉：错误、重试都在抽屉里，不再落到列表区红条。
  async function openUser(user: AdminUserSummary) {
    const token = ++detailRequestToken
    drawerOpen.value = true
    detailTarget.value = user
    detailPending.value = true
    detailError.value = ''
    clearErrors()
    try {
      const detail = await request<AdminUserDetail>(`/users/${user.id}`)
      if (token !== detailRequestToken) {
        return
      }
      applyDetailToForm(detail)
    } catch (error) {
      if (token !== detailRequestToken) {
        return
      }
      selectedUser.value = null
      detailError.value = apiErrorMessage(error) || t('admin.users.detailLoadFailed')
    } finally {
      if (token === detailRequestToken) {
        detailPending.value = false
      }
    }
  }

  async function retryOpenUser() {
    if (detailTarget.value) {
      await openUser(detailTarget.value)
    }
  }

  // 插件 surface 触发的刷新不该把抽屉打回骨架屏，静默重取即可。
  // await 期间可能已经切到另一个用户：回来必须先校验 id，否则会把旧详情
  // 写进新用户的表单，失败也会把错误挂到新用户抽屉上。
  async function refreshSelectedUser() {
    const current = selectedUser.value
    if (!current) {
      return
    }
    try {
      const detail = await request<AdminUserDetail>(`/users/${current.id}`)
      if (selectedUser.value?.id !== current.id) {
        return
      }
      applyDetailToForm(detail)
    } catch (error) {
      if (selectedUser.value?.id !== current.id) {
        return
      }
      presentError(error, t('admin.users.detailLoadFailed'))
    }
  }

  function closeUser() {
    detailRequestToken += 1
    drawerOpen.value = false
    detailPending.value = false
    detailError.value = ''
    detailTarget.value = null
    selectedUser.value = null
    selectedRoleKeys.value = []
    allowOverrides.value = []
    denyOverrides.value = []
    form.newPassword = ''
    form.confirmPassword = ''
    clearErrors()
  }

  function toggleRole(key: string) {
    if (selectedRoleKeys.value.includes(key)) {
      selectedRoleKeys.value = selectedRoleKeys.value.filter(item => item !== key)
      return
    }
    selectedRoleKeys.value = [...selectedRoleKeys.value, key].sort()
  }

  function permissionMode(key: string) {
    if (allowOverrides.value.includes(key)) {
      return 'allow'
    }
    if (denyOverrides.value.includes(key)) {
      return 'deny'
    }
    return 'inherit'
  }

  function setPermissionMode(key: string, mode: 'inherit' | 'allow' | 'deny') {
    allowOverrides.value = allowOverrides.value.filter(item => item !== key)
    denyOverrides.value = denyOverrides.value.filter(item => item !== key)
    if (mode === 'allow') {
      allowOverrides.value = [...allowOverrides.value, key].sort()
    }
    if (mode === 'deny') {
      denyOverrides.value = [...denyOverrides.value, key].sort()
    }
  }

  async function saveAccount() {
    if (!selectedUser.value || !accountDirty.value) {
      return
    }
    savingAccount.value = true
    clearErrors()
    try {
      const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}`, {
        method: 'PATCH',
        body: {
          username: form.username.trim(),
          email: form.email.trim(),
          displayName: form.displayName.trim(),
          locale: form.locale,
          status: form.status,
          kind: form.kind
        }
      })
      applyDetailToForm(detail)
      await deps.refreshList()
      deps.notifySaved(t('admin.users.accountSaved'))
    } catch (error) {
      presentError(error, t('admin.users.saveAccountFailed'))
    } finally {
      savingAccount.value = false
    }
  }

  async function saveProfile() {
    if (!selectedUser.value || !profileDirty.value) {
      return
    }
    savingProfile.value = true
    clearErrors()
    try {
      const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}`, {
        method: 'PATCH',
        body: {
          bio: form.bio,
          signature: form.signature,
          location: form.location,
          websiteUrl: form.websiteUrl.trim()
        }
      })
      applyDetailToForm(detail)
      deps.notifySaved(t('admin.users.profileSaved'))
    } catch (error) {
      presentError(error, t('admin.users.saveProfileFailed'))
    } finally {
      savingProfile.value = false
    }
  }

  async function saveRoles() {
    if (!selectedUser.value || !rolesDirty.value) {
      return
    }
    savingRoles.value = true
    clearErrors()
    try {
      const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}/roles`, {
        method: 'PUT',
        body: {
          roleKeys: selectedRoleKeys.value
        }
      })
      applyDetailToForm(detail)
      await deps.refreshList()
      deps.notifySaved(t('admin.users.rolesSaved'))
    } catch (error) {
      presentError(error, t('admin.users.saveRolesFailed'))
    } finally {
      savingRoles.value = false
    }
  }

  async function savePermissionOverrides() {
    if (!selectedUser.value || !overridesDirty.value) {
      return
    }
    savingOverrides.value = true
    clearErrors()
    try {
      const detail = await request<AdminUserDetail>(`/users/${selectedUser.value.id}/permission-overrides`, {
        method: 'PUT',
        body: {
          allow: allowOverrides.value,
          deny: denyOverrides.value
        }
      })
      applyDetailToForm(detail)
      await deps.refreshList()
      deps.notifySaved(t('admin.users.permissionsSaved'))
    } catch (error) {
      presentError(error, t('admin.users.savePermissionsFailed'))
    } finally {
      savingOverrides.value = false
    }
  }

  return {
    drawerOpen,
    detailPending,
    detailError,
    detailTarget,
    selectedUser,
    selectedRoleKeys,
    allowOverrides,
    denyOverrides,
    form,
    savingAccount,
    savingProfile,
    savingRoles,
    savingOverrides,
    drawerError,
    fieldErrors,
    errorFocusRequest,
    selectedUserIsSuperAdmin,
    accountDirty,
    profileDirty,
    rolesDirty,
    overridesDirty,
    clearErrors,
    clearFieldError,
    setFieldError,
    presentError,
    applyDetailToForm,
    openUser,
    retryOpenUser,
    refreshSelectedUser,
    closeUser,
    toggleRole,
    permissionMode,
    setPermissionMode,
    saveAccount,
    saveProfile,
    saveRoles,
    savePermissionOverrides,
    firstErrorField: (fields: Record<string, string[]>) => firstAdminUserErrorField(fields)
  }
}
