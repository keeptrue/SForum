<script setup lang="ts">
import {
  formatUploadLimit,
  replaceUploadPermissionOverride,
  uploadLimitMaxMB,
  uploadPermissionMode,
  type PermissionOverrides,
  type UploadPermissionMode
} from '~/components/admin/settings/attachments/uploadPolicyModel'
import { apiErrorMessage } from '~/composables/useApiClient'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'

type RolePolicy = {
  roleKey: string
  alias: string
  enabled: boolean
  grantsUpload: boolean
  protected: boolean
  configuredMaxFileSizeMb: number | null
  effectiveMaxFileSizeBytes: number
}

type RolePolicyCatalog = {
  uploadEnabled: boolean
  siteMaxFileSizeBytes: number
  transportMaxFileSizeBytes: number
  items: RolePolicy[]
}

type Role = {
  key: string
  permissionKeys: string[]
}

type UserSummary = {
  id: number
  username: string
  displayName: string
  status: string
  roleKeys: string[]
}

type UserList = {
  items: UserSummary[]
  total: number
}

type UserDetail = UserSummary & {
  permissionOverrides: PermissionOverrides
}

type UserPolicy = {
  userId: number
  username: string
  displayName: string
  status: string
  roleKeys: string[]
  canUpload: boolean
  protected: boolean
  configuredMaxFileSizeMb: number | null
  effectiveMaxFileSizeBytes: number
  source: 'site' | 'role' | 'user'
  reason: string
}

const { t } = useI18n()
const toast = useToast()
const { request } = useApiClient()
const { can } = usePermissions()

const roleDetails = ref<Role[]>([])
const roleDetailsLoaded = ref(false)
const roleDetailsError = ref('')
const roleDrafts = reactive<Record<string, number>>({})
const roleSaving = ref('')
const rolePermissionSaving = ref('')
const userQuery = ref('')
const userResults = ref<UserSummary[]>([])
const userSearching = ref(false)
const userSearchError = ref('')
const selectedUser = ref<UserPolicy | null>(null)
const selectedUserDetail = ref<UserDetail | null>(null)
const userLimitDraft = ref(1)
const userPermissionDraft = ref<UploadPermissionMode>('inherit')
const userLoading = ref(false)
const userLimitSaving = ref(false)
const userPermissionSaving = ref(false)

const canManageRolePermissions = computed(() => can(FORUM_PERMISSIONS.roleManage))
const canViewUsers = computed(() => can(FORUM_PERMISSIONS.userView))
const canManageUserOverrides = computed(() => can(FORUM_PERMISSIONS.userPermissionOverride))

const { data: catalog, pending, error, refresh } = await useAsyncData(
  'admin-attachment-upload-policies',
  () => request<RolePolicyCatalog>('/admin/attachment-upload-policies/roles')
)

const maximumMB = computed(() => catalog.value
  ? uploadLimitMaxMB(catalog.value.siteMaxFileSizeBytes, catalog.value.transportMaxFileSizeBytes)
  : 1)
const transportMismatch = computed(() => Boolean(catalog.value
  && catalog.value.transportMaxFileSizeBytes < catalog.value.siteMaxFileSizeBytes))

watch(catalog, value => {
  if (!value) return
  for (const item of value.items) {
    roleDrafts[item.roleKey] = item.configuredMaxFileSizeMb ?? maximumMB.value
  }
}, { immediate: true })

onMounted(async () => {
  if (!canManageRolePermissions.value) return
  try {
    roleDetails.value = await request<Role[]>('/roles')
  } catch (err) {
    roleDetails.value = []
    roleDetailsError.value = apiErrorMessage(err) || t('admin.attachments.uploadPolicies.rolePermissionsLoadFailed')
  } finally {
    roleDetailsLoaded.value = true
  }
})

defineExpose({ refresh, pending })

function rolePermissionEditable(item: RolePolicy) {
  return canManageRolePermissions.value
    && roleDetailsLoaded.value
    && !roleDetailsError.value
    && !item.protected
    && roleDetails.value.some(role => role.key === item.roleKey)
}

function selectUserPermissionMode(mode: UploadPermissionMode) {
  userPermissionDraft.value = mode
}

async function setRoleUploadPermission(item: RolePolicy, enabled: boolean) {
  const detail = roleDetails.value.find(role => role.key === item.roleKey)
  if (!detail || !rolePermissionEditable(item)) return
  rolePermissionSaving.value = item.roleKey
  try {
    const permissions = new Set(detail.permissionKeys)
    if (enabled) permissions.add(FORUM_PERMISSIONS.attachmentUpload)
    else permissions.delete(FORUM_PERMISSIONS.attachmentUpload)
    await request(`/roles/${encodeURIComponent(item.roleKey)}/permissions`, {
      method: 'PUT',
      body: { permissions: [...permissions] }
    })
    detail.permissionKeys = [...permissions]
    await refresh()
    showSuccess(t('admin.attachments.uploadPolicies.permissionSaved'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.permissionSaveFailed')
  } finally {
    rolePermissionSaving.value = ''
  }
}

async function saveRoleLimit(item: RolePolicy) {
  roleSaving.value = item.roleKey
  try {
    await request(`/admin/attachment-upload-policies/roles/${encodeURIComponent(item.roleKey)}`, {
      method: 'PUT',
      body: { maxFileSizeMb: roleDrafts[item.roleKey] }
    })
    await refresh()
    showSuccess(t('admin.attachments.uploadPolicies.limitSaved'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.limitSaveFailed')
  } finally {
    roleSaving.value = ''
  }
}

async function restoreRoleLimit(item: RolePolicy) {
  roleSaving.value = item.roleKey
  try {
    await request(`/admin/attachment-upload-policies/roles/${encodeURIComponent(item.roleKey)}`, { method: 'DELETE' })
    await refresh()
    showSuccess(t('admin.attachments.uploadPolicies.limitRestored'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.limitSaveFailed')
  } finally {
    roleSaving.value = ''
  }
}

async function searchUsers() {
  if (!canViewUsers.value) return
  userSearching.value = true
  userSearchError.value = ''
  try {
    const params = new URLSearchParams({ page: '1', perPage: '10' })
    if (userQuery.value.trim()) params.set('query', userQuery.value.trim())
    const result = await request<UserList>(`/users?${params.toString()}`)
    userResults.value = result.items
  } catch (err) {
    userSearchError.value = apiErrorMessage(err) || t('admin.attachments.uploadPolicies.userSearchFailed')
  } finally {
    userSearching.value = false
  }
}

async function selectUser(user: UserSummary) {
  userLoading.value = true
  try {
    const [policy, detail] = await Promise.all([
      request<UserPolicy>(`/admin/attachment-upload-policies/users/${user.id}`),
      request<UserDetail>(`/users/${user.id}`)
    ])
    selectedUser.value = policy
    selectedUserDetail.value = detail
    userLimitDraft.value = policy.configuredMaxFileSizeMb ?? maximumMB.value
    userPermissionDraft.value = uploadPermissionMode(detail.permissionOverrides)
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.userLoadFailed')
  } finally {
    userLoading.value = false
  }
}

async function saveUserLimit() {
  if (!selectedUser.value) return
  userLimitSaving.value = true
  try {
    selectedUser.value = await request<UserPolicy>(
      `/admin/attachment-upload-policies/users/${selectedUser.value.userId}`,
      { method: 'PUT', body: { maxFileSizeMb: userLimitDraft.value } }
    )
    showSuccess(t('admin.attachments.uploadPolicies.userLimitSaved'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.limitSaveFailed')
  } finally {
    userLimitSaving.value = false
  }
}

async function restoreUserLimit() {
  if (!selectedUser.value) return
  userLimitSaving.value = true
  try {
    selectedUser.value = await request<UserPolicy>(
      `/admin/attachment-upload-policies/users/${selectedUser.value.userId}`,
      { method: 'DELETE' }
    )
    userLimitDraft.value = maximumMB.value
    showSuccess(t('admin.attachments.uploadPolicies.userLimitRestored'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.limitSaveFailed')
  } finally {
    userLimitSaving.value = false
  }
}

async function saveUserPermission() {
  if (!selectedUser.value || !selectedUserDetail.value || !canManageUserOverrides.value) return
  userPermissionSaving.value = true
  try {
    const overrides = replaceUploadPermissionOverride(
      selectedUserDetail.value.permissionOverrides,
      userPermissionDraft.value
    )
    selectedUserDetail.value = await request<UserDetail>(
      `/users/${selectedUser.value.userId}/permission-overrides`,
      { method: 'PUT', body: overrides }
    )
    selectedUser.value = await request<UserPolicy>(`/admin/attachment-upload-policies/users/${selectedUser.value.userId}`)
    showSuccess(t('admin.attachments.uploadPolicies.permissionSaved'))
  } catch (err) {
    showError(err, 'admin.attachments.uploadPolicies.permissionSaveFailed')
  } finally {
    userPermissionSaving.value = false
  }
}

function showSuccess(title: string) {
  toast.add({ color: 'success', icon: 'i-lucide-check', title, duration: 5000 })
}

function showError(error: unknown, fallbackKey: string) {
  toast.add({ color: 'error', duration: 0, icon: 'i-lucide-triangle-alert', title: apiErrorMessage(error) || t(fallbackKey) })
}
</script>

<template>
  <div class="min-w-0 space-y-8">
    <UAlert
      v-if="error"
      color="error"
      variant="soft"
      icon="i-lucide-triangle-alert"
      :title="t('admin.attachments.uploadPolicies.loadFailed')"
    />
    <UAlert
      v-else-if="catalog && !catalog.uploadEnabled"
      color="warning"
      variant="soft"
      icon="i-lucide-upload-cloud"
      :title="t('admin.attachments.uploadPolicies.siteDisabled')"
    />
    <UAlert
      v-else-if="transportMismatch"
      color="warning"
      variant="soft"
      icon="i-lucide-server-cog"
      :title="t('admin.attachments.uploadPolicies.transportMismatch')"
      :description="t('admin.attachments.uploadPolicies.transportMismatchHelp', { limit: formatUploadLimit(catalog?.transportMaxFileSizeBytes || 0) })"
    />
    <UAlert
      v-if="roleDetailsError"
      color="warning"
      variant="soft"
      icon="i-lucide-shield-alert"
      :title="roleDetailsError"
      :description="t('admin.attachments.uploadPolicies.rolePermissionsLoadFailedHelp')"
    />

    <section aria-labelledby="role-upload-policy-title">
      <div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h3 id="role-upload-policy-title" class="text-base font-bold text-slate-900 dark:text-zinc-100">
            {{ t('admin.attachments.uploadPolicies.rolesTitle') }}
          </h3>
          <p class="mt-1 max-w-3xl text-sm text-slate-500 dark:text-zinc-400">
            {{ t('admin.attachments.uploadPolicies.rolesDescription') }}
          </p>
        </div>
        <div v-if="catalog" class="flex flex-wrap gap-2 text-xs">
          <UBadge color="neutral" variant="soft">
            {{ t('admin.attachments.uploadPolicies.siteLimit', { limit: formatUploadLimit(catalog.siteMaxFileSizeBytes) }) }}
          </UBadge>
          <UBadge color="neutral" variant="soft">
            {{ t('admin.attachments.uploadPolicies.transportLimit', { limit: formatUploadLimit(catalog.transportMaxFileSizeBytes) }) }}
          </UBadge>
        </div>
      </div>

      <div class="divide-y divide-slate-200 overflow-hidden rounded-md border border-slate-200 bg-white dark:divide-zinc-800 dark:border-zinc-800 dark:bg-zinc-950">
        <div v-if="pending" class="p-5 text-sm text-slate-500 dark:text-zinc-400">
          {{ t('admin.attachments.uploadPolicies.loading') }}
        </div>
        <div
          v-for="item in catalog?.items || []"
          :key="item.roleKey"
          class="grid min-h-20 gap-4 p-4 lg:grid-cols-[minmax(12rem,1fr)_minmax(12rem,1fr)_auto] lg:items-center"
        >
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-semibold text-slate-900 dark:text-zinc-100">{{ item.alias }}</span>
              <code class="text-xs text-slate-500 dark:text-zinc-400">{{ item.roleKey }}</code>
              <UBadge v-if="item.protected" color="neutral" variant="soft">{{ t('admin.attachments.uploadPolicies.protected') }}</UBadge>
            </div>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.attachments.uploadPolicies.effectiveLimit', { limit: formatUploadLimit(item.effectiveMaxFileSizeBytes) }) }}
            </p>
          </div>

          <div class="flex min-w-0 flex-col gap-3 sm:flex-row sm:items-center">
            <div class="flex min-w-32 items-center gap-2">
              <USwitch
                :model-value="item.grantsUpload"
                :disabled="!rolePermissionEditable(item) || rolePermissionSaving === item.roleKey"
                :aria-label="t('admin.attachments.uploadPolicies.uploadPermissionFor', { role: item.alias })"
                @update:model-value="setRoleUploadPermission(item, Boolean($event))"
              />
              <span class="text-sm">{{ item.grantsUpload ? t('admin.attachments.uploadPolicies.permissionEnabled') : t('admin.attachments.uploadPolicies.permissionDisabled') }}</span>
            </div>
            <UInput
              v-model.number="roleDrafts[item.roleKey]"
              type="number"
              min="1"
              :max="maximumMB"
              :disabled="item.protected"
              class="w-full sm:max-w-36"
            >
              <template #trailing><span class="text-xs text-slate-500">MB</span></template>
            </UInput>
          </div>

          <div class="flex items-center justify-end gap-2">
            <UButton
              color="neutral"
              variant="ghost"
              icon="i-lucide-rotate-ccw"
              :aria-label="t('admin.attachments.uploadPolicies.restoreInheritance')"
              :title="t('admin.attachments.uploadPolicies.restoreInheritance')"
              :disabled="item.protected || item.configuredMaxFileSizeMb === null"
              :loading="roleSaving === item.roleKey"
              @click="restoreRoleLimit(item)"
            />
            <UButton
              color="primary"
              icon="i-lucide-save"
              :aria-label="t('admin.attachments.uploadPolicies.saveRoleLimit')"
              :title="t('admin.attachments.uploadPolicies.saveRoleLimit')"
              :disabled="item.protected"
              :loading="roleSaving === item.roleKey"
              @click="saveRoleLimit(item)"
            />
          </div>
        </div>
      </div>
    </section>

    <section class="border-t border-slate-200 pt-8 dark:border-zinc-800" aria-labelledby="user-upload-policy-title">
      <h3 id="user-upload-policy-title" class="text-base font-bold text-slate-900 dark:text-zinc-100">
        {{ t('admin.attachments.uploadPolicies.usersTitle') }}
      </h3>
      <p class="mt-1 max-w-3xl text-sm text-slate-500 dark:text-zinc-400">
        {{ t('admin.attachments.uploadPolicies.usersDescription') }}
      </p>

      <UAlert
        v-if="!canViewUsers"
        class="mt-4"
        color="neutral"
        variant="soft"
        icon="i-lucide-lock-keyhole"
        :title="t('admin.attachments.uploadPolicies.userViewRequired')"
      />
      <template v-else>
        <form class="mt-4 flex max-w-2xl gap-2" @submit.prevent="searchUsers">
          <UInput
            v-model="userQuery"
            class="min-w-0 flex-1"
            icon="i-lucide-search"
            :placeholder="t('admin.attachments.uploadPolicies.userSearchPlaceholder')"
          />
          <UButton type="submit" color="neutral" variant="outline" icon="i-lucide-search" :loading="userSearching">
            {{ t('admin.attachments.uploadPolicies.search') }}
          </UButton>
        </form>
        <p v-if="userSearchError" class="mt-2 text-sm text-error" role="alert">{{ userSearchError }}</p>

        <div v-if="userResults.length" class="mt-3 flex max-w-3xl flex-wrap gap-2">
          <UButton
            v-for="user in userResults"
            :key="user.id"
            color="neutral"
            :variant="selectedUser?.userId === user.id ? 'solid' : 'outline'"
            @click="selectUser(user)"
          >
            {{ user.displayName || user.username }}
            <span class="ml-1 opacity-70">@{{ user.username }}</span>
          </UButton>
        </div>

        <div v-if="userLoading" class="mt-5 text-sm text-slate-500 dark:text-zinc-400">
          {{ t('admin.attachments.uploadPolicies.loadingUser') }}
        </div>
        <div v-else-if="selectedUser" class="mt-5 border-y border-slate-200 py-5 dark:border-zinc-800">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-semibold text-slate-900 dark:text-zinc-100">{{ selectedUser.displayName || selectedUser.username }}</span>
                <code class="text-xs text-slate-500">@{{ selectedUser.username }}</code>
                <UBadge :color="selectedUser.canUpload ? 'success' : 'warning'" variant="soft">
                  {{ selectedUser.canUpload ? t('admin.attachments.uploadPolicies.canUpload') : t('admin.attachments.uploadPolicies.cannotUpload') }}
                </UBadge>
              </div>
              <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.attachments.uploadPolicies.userEffective', { limit: formatUploadLimit(selectedUser.effectiveMaxFileSizeBytes), source: t(`admin.attachments.uploadPolicies.source.${selectedUser.source}`) }) }}
              </p>
            </div>
            <UBadge v-if="selectedUser.protected" color="neutral" variant="soft">{{ t('admin.attachments.uploadPolicies.protected') }}</UBadge>
          </div>

          <div class="mt-5 grid gap-6 lg:grid-cols-2">
            <div>
              <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.uploadPolicies.userPermission') }}</h4>
              <div class="mt-3 flex flex-wrap gap-2">
                <UButton
                  v-for="mode in (['inherit', 'allow', 'deny'] as UploadPermissionMode[])"
                  :key="mode"
                  color="neutral"
                  :variant="userPermissionDraft === mode ? 'solid' : 'outline'"
                  :disabled="selectedUser.protected || !canManageUserOverrides"
                  @click="selectUserPermissionMode(mode)"
                >
                  {{ t(`admin.attachments.uploadPolicies.permissionMode.${mode}`) }}
                </UButton>
              </div>
              <UButton
                class="mt-3"
                color="primary"
                leading-icon="i-lucide-save"
                :disabled="selectedUser.protected || !canManageUserOverrides"
                :loading="userPermissionSaving"
                @click="saveUserPermission"
              >
                {{ t('admin.attachments.uploadPolicies.savePermission') }}
              </UButton>
              <p v-if="!canManageUserOverrides" class="mt-2 text-xs text-slate-500 dark:text-zinc-400">
                {{ t('admin.attachments.uploadPolicies.overridePermissionRequired') }}
              </p>
            </div>

            <div>
              <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.attachments.uploadPolicies.userLimit') }}</h4>
              <div class="mt-3 flex max-w-sm items-center gap-2">
                <UInput v-model.number="userLimitDraft" type="number" min="1" :max="maximumMB" :disabled="selectedUser.protected" class="min-w-0 flex-1">
                  <template #trailing><span class="text-xs text-slate-500">MB</span></template>
                </UInput>
                <UButton
                  color="primary"
                  icon="i-lucide-save"
                  :title="t('admin.attachments.uploadPolicies.saveUserLimit')"
                  :aria-label="t('admin.attachments.uploadPolicies.saveUserLimit')"
                  :loading="userLimitSaving"
                  :disabled="selectedUser.protected"
                  @click="saveUserLimit"
                />
                <UButton
                  color="neutral"
                  variant="outline"
                  icon="i-lucide-rotate-ccw"
                  :title="t('admin.attachments.uploadPolicies.restoreInheritance')"
                  :aria-label="t('admin.attachments.uploadPolicies.restoreInheritance')"
                  :loading="userLimitSaving"
                  :disabled="selectedUser.protected || selectedUser.configuredMaxFileSizeMb === null"
                  @click="restoreUserLimit"
                />
              </div>
            </div>
          </div>
        </div>
      </template>
    </section>
  </div>
</template>
