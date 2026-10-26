<script setup lang="ts">
import type { AdminUserSummary, Permission, Role } from '~/utils/admin/adminUsers'
import { useAdminUserDetailEditor } from '~/composables/admin/identity/useAdminUserDetailEditor'
import { useAdminUserSecurity } from '~/composables/admin/identity/useAdminUserSecurity'
import SFAdminUserAccountSection from '~/components/admin/identity/users/SFAdminUserAccountSection.vue'
import SFAdminUserProfileSection from '~/components/admin/identity/users/SFAdminUserProfileSection.vue'
import SFAdminUserSecuritySection from '~/components/admin/identity/users/SFAdminUserSecuritySection.vue'
import SFAdminUserRolesSection from '~/components/admin/identity/users/SFAdminUserRolesSection.vue'
import SFAdminUserPermissionsSection from '~/components/admin/identity/users/SFAdminUserPermissionsSection.vue'
import SFAdminUserNicknameHistory from '~/components/admin/identity/users/SFAdminUserNicknameHistory.vue'
import SFAdminSurfaceOutlet from '~/components/admin/SFAdminSurfaceOutlet.vue'
import { formatAdminDateTime } from '~/utils/admin/adminDateFormat'

const props = defineProps<{
  roles: Role[]
  permissions: Permission[]
}>()

const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const toast = useToast()

function notifySaved(message: string) {
  toast.add({
    title: message,
    color: 'success',
    icon: 'i-lucide-check-circle',
    duration: 5000
  })
}

const editor = useAdminUserDetailEditor({
  notifySaved,
  refreshList: async () => { emit('changed') }
})

const security = useAdminUserSecurity({
  form: editor.form,
  userId: () => editor.selectedUser.value?.id ?? null,
  presentError: editor.presentError,
  setFieldError: editor.setFieldError,
  clearErrors: editor.clearErrors,
  notifySaved
})

const {
  drawerOpen,
  detailPending,
  detailError,
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
  clearFieldError,
  applyDetailToForm,
  retryOpenUser,
  closeUser,
  refreshSelectedUser,
  toggleRole,
  setPermissionMode,
  saveAccount,
  saveProfile,
  saveRoles,
  savePermissionOverrides,
  firstErrorField
} = editor

const drawerEl = ref<HTMLElement | null>(null)

// 保存失败后把视野带到第一个出错字段：滚动 + 聚焦，键盘操作者不用自己找。
watch(errorFocusRequest, async () => {
  await nextTick()
  const name = firstErrorField(fieldErrors.value)
  if (!name) {
    return
  }
  const field = drawerEl.value?.querySelector<HTMLElement>(`[data-user-field="${name}"]`)
  const target = field?.querySelector<HTMLElement>('input, select, textarea, button[role="combobox"]')
  target?.scrollIntoView?.({ block: 'center' })
  target?.focus?.()
})

defineExpose({
  open: (user: AdminUserSummary) => editor.openUser(user),
  close: closeUser,
  pending: detailPending,
  targetId: computed(() => editor.detailTarget.value?.id ?? null)
})
</script>

<template>
  <USlideover
    :open="drawerOpen"
    side="right"
    :title="selectedUser?.displayName || t('admin.users.manage')"
    :description="t('admin.users.drawerDescription')"
    :close="false"
    :ui="{ content: 'w-full max-w-3xl' }"
    @update:open="value => !value && closeUser()"
  >
    <template #content>
      <div ref="drawerEl" class="flex h-full min-w-0 flex-col bg-white dark:bg-zinc-950">
        <header class="flex items-start justify-between gap-4 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-semibold text-slate-950 dark:text-zinc-50">
              {{ selectedUser?.displayName || t('admin.users.manage') }}
            </h3>
            <p v-if="selectedUser" class="text-xs text-slate-500 dark:text-zinc-400">
              {{ selectedUser.username }} · {{ selectedUser.email }}
            </p>
            <p v-if="selectedUser" class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
              {{ t('admin.users.userId') }}: {{ selectedUser.id }}
              · {{ t('admin.users.createdAt') }}: {{ formatAdminDateTime(selectedUser.createdAt) }}
              · {{ t('admin.users.updatedAt') }}: {{ formatAdminDateTime(selectedUser.updatedAt) }}
            </p>
          </div>
          <UButton color="neutral" variant="ghost" icon="i-lucide-x" :aria-label="t('admin.users.close')" @click="closeUser" />
        </header>

        <!-- 保存失败摘要钉在头部下方，不随内容滚走；错误提示不自动消失。 -->
        <div v-if="drawerError" class="border-b border-slate-200 px-5 py-3 dark:border-zinc-800" role="alert">
          <UAlert
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="t('admin.users.drawerErrorTitle')"
            :description="drawerError"
          />
        </div>

        <div class="min-h-0 flex-1 overflow-y-auto px-5 py-5">
          <div v-if="detailPending" class="space-y-4">
            <USkeleton class="h-6 w-1/2" />
            <USkeleton class="h-40 w-full" />
            <USkeleton class="h-40 w-full" />
          </div>

          <div v-else-if="detailError" class="space-y-3">
            <UAlert
              color="error"
              variant="soft"
              icon="i-lucide-triangle-alert"
              :title="t('admin.users.detailLoadFailed')"
              :description="detailError"
            />
            <UButton color="primary" leading-icon="i-lucide-refresh-cw" @click="retryOpenUser">
              {{ t('admin.users.detailRetry') }}
            </UButton>
          </div>

          <div v-else-if="selectedUser" class="space-y-5">
            <SFAdminSurfaceOutlet
              page-id="/users"
              :kinds="['detail_region', 'editor_panel']"
              :context="{ resourceType: 'user', resource: selectedUser }"
              @refresh="refreshSelectedUser"
            />

            <SFAdminUserAccountSection
              :user="selectedUser"
              :form="form"
              :errors="fieldErrors"
              :saving="savingAccount"
              :dirty="accountDirty"
              :clear-error="clearFieldError"
              @save="saveAccount"
              @refreshed="applyDetailToForm"
            />

            <SFAdminUserNicknameHistory :changes="selectedUser.nicknameHistory" />

            <SFAdminUserProfileSection
              :form="form"
              :errors="fieldErrors"
              :saving="savingProfile"
              :dirty="profileDirty"
              :clear-error="clearFieldError"
              @save="saveProfile"
            />

            <SFAdminUserSecuritySection
              :form="form"
              :errors="fieldErrors"
              :resetting="security.resettingPassword.value"
              :revoking="security.revokingSessions.value"
              :clearing="security.clearingClientIPs.value"
              :clear-error="clearFieldError"
              @reset="security.resetUserPassword"
              @revoke="security.revokeUserSessions"
              @clear-ips="security.clearUserClientIPs"
            />

            <SFAdminUserRolesSection
              :roles="props.roles"
              :selected-keys="selectedRoleKeys"
              :saving="savingRoles"
              :dirty="rolesDirty"
              :errors="fieldErrors"
              @toggle="toggleRole"
              @save="saveRoles"
            />

            <SFAdminUserPermissionsSection
              :permissions="props.permissions"
              :allow="allowOverrides"
              :deny="denyOverrides"
              :saving="savingOverrides"
              :locked="selectedUserIsSuperAdmin"
              :dirty="overridesDirty"
              @set-mode="setPermissionMode"
              @save="savePermissionOverrides"
            />
          </div>
        </div>
      </div>
    </template>
  </USlideover>
</template>
