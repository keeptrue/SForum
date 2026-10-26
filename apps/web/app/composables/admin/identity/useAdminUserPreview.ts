import type { Ref } from 'vue'
import type { AdminUserDetail, AdminUserSummary } from '~/utils/admin/adminUsers'
import { paginateItems } from '~/utils/admin/adminExtensions'

// 预览面板的分页大小。它们与详情抽屉的分页是独立的：预览用来快速核对一个账号，
// 不应该因为抽屉里的翻页而改变。
export const PREVIEW_SESSION_PAGE_SIZE = 5
export const PREVIEW_AUTH_EVENT_PAGE_SIZE = 10
export const PREVIEW_PERMISSION_PAGE_SIZE = 24

export type AdminUserPreviewDeps = {
  // openUser 由页面提供：从预览「转为管理」要进入详情抽屉，而那套状态属于页面。
  openUser: (user: AdminUserSummary) => Promise<void>
}

// 用户预览：列表里点一下就能看到账号全貌，不进入管理抽屉。
export function useAdminUserPreview(deps: AdminUserPreviewDeps) {
  const { request } = useApiClient()
  const { t } = useI18n()

  const previewUser: Ref<AdminUserDetail | null> = ref(null)
  const previewTargetId: Ref<number | null> = ref(null)
  const previewOpen = ref(false)
  const previewPending = ref(false)
  // 加载失败留在预览弹窗内：关窗会让操作者丢失「我刚才点了谁」的上下文。
  const previewError = ref('')
  const previewSessionsPage = ref(1)
  const previewAuthEventsPage = ref(1)
  const previewPermissionsPage = ref(1)

  const previewSessionsPageInfo = computed(() =>
    paginateItems(previewUser.value?.sessions || [], previewSessionsPage.value, PREVIEW_SESSION_PAGE_SIZE)
  )
  const previewAuthEventsPageInfo = computed(() =>
    paginateItems(previewUser.value?.recentAuthEvents || [], previewAuthEventsPage.value, PREVIEW_AUTH_EVENT_PAGE_SIZE)
  )
  const previewPermissionsPageInfo = computed(() =>
    paginateItems(previewUser.value?.permissions || [], previewPermissionsPage.value, PREVIEW_PERMISSION_PAGE_SIZE)
  )

  // 数据变少时把页码夹回有效范围，避免停在一个空页上。
  watch(() => previewSessionsPageInfo.value.page, next => { previewSessionsPage.value = next })
  watch(() => previewAuthEventsPageInfo.value.page, next => { previewAuthEventsPage.value = next })
  watch(() => previewPermissionsPageInfo.value.page, next => { previewPermissionsPage.value = next })

  function resetPreviewListPages() {
    previewSessionsPage.value = 1
    previewAuthEventsPage.value = 1
    previewPermissionsPage.value = 1
  }

  async function openUserPreview(user: AdminUserSummary) {
    previewPending.value = true
    previewTargetId.value = user.id
    previewOpen.value = true
    previewUser.value = null
    previewError.value = ''
    resetPreviewListPages()
    try {
      previewUser.value = await request<AdminUserDetail>(`/users/${user.id}`)
      // 加载完成后按实际条数夹紧页码（通常仍是第 1 页）。
      resetPreviewListPages()
    } catch (error) {
      previewError.value = apiErrorMessage(error) || t('admin.users.previewLoadFailed')
    } finally {
      previewPending.value = false
    }
  }

  function closeUserPreview() {
    previewOpen.value = false
    previewUser.value = null
    previewTargetId.value = null
    previewError.value = ''
    resetPreviewListPages()
  }

  async function manageFromPreview() {
    const user = previewUser.value
    closeUserPreview()
    if (user) {
      await deps.openUser(user)
    }
  }

  function displayOrDash(value?: string | null) {
    const text = (value || '').trim()
    return text || t('admin.users.previewEmptyValue')
  }

  function authActionLabel(action: string) {
    if (action === 'auth.login.success') {
      return t('admin.users.previewAuthLogin')
    }
    if (action === 'auth.register.success') {
      return t('admin.users.previewAuthRegister')
    }
    return action
  }

  return {
    previewUser,
    previewTargetId,
    previewOpen,
    previewPending,
    previewError,
    previewSessionsPage,
    previewAuthEventsPage,
    previewPermissionsPage,
    previewSessionsPageInfo,
    previewAuthEventsPageInfo,
    previewPermissionsPageInfo,
    openUserPreview,
    closeUserPreview,
    manageFromPreview,
    displayOrDash,
    authActionLabel
  }
}
