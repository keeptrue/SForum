<script setup lang="ts">
import type { AdminUserSummary, Role } from '~/utils/admin/adminUsers'
import {
  useAdminUserPreview,
  PREVIEW_SESSION_PAGE_SIZE,
  PREVIEW_AUTH_EVENT_PAGE_SIZE,
  PREVIEW_PERMISSION_PAGE_SIZE
} from '~/composables/admin/identity/useAdminUserPreview'
import { formatAdminDateTime } from '~/utils/admin/adminDateFormat'

defineProps<{
  roles: Role[]
}>()

const { t } = useI18n()
const emit = defineEmits<{
  manage: [user: AdminUserSummary]
}>()

const {
  previewUser, previewTargetId, previewOpen, previewPending, previewError,
  previewSessionsPage, previewAuthEventsPage, previewPermissionsPage,
  previewSessionsPageInfo, previewAuthEventsPageInfo, previewPermissionsPageInfo,
  openUserPreview, closeUserPreview, manageFromPreview, displayOrDash, authActionLabel
} = useAdminUserPreview({
  openUser: async user => { emit('manage', user) }
})

// 模板沿用抽取前的 formatter 名称，避免改写大段展示逻辑。
const formatDateTime = formatAdminDateTime

defineExpose({
  open: openUserPreview,
  pending: previewPending,
  targetId: previewTargetId
})
</script>

<template>
  <!-- 用户信息预览（只读，含完整 IP / UA） -->
  <UModal
    v-model:open="previewOpen"
    :ui="{ content: 'sm:max-w-4xl' }"
    @update:open="(open) => { if (!open) closeUserPreview() }"
  >
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.users.previewTitle') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.users.previewIntro') }}
            </p>
          </div>
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.users.close')"
            @click="closeUserPreview"
          />
        </div>

        <div class="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          <UAlert
            v-if="previewError"
            color="error"
            variant="soft"
            icon="i-lucide-triangle-alert"
            :title="t('admin.users.previewLoadFailed')"
            :description="previewError"
          />

          <div v-if="previewPending" class="flex min-h-40 items-center justify-center text-sm text-slate-500">
            <UIcon name="i-lucide-loader-circle" class="mr-2 size-4 animate-spin" />
            {{ t('admin.common.loading') }}
          </div>

          <template v-else-if="previewUser">
            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-3 flex flex-wrap items-center gap-2">
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.users.previewSectionAccount') }}
                </h4>
                <UBadge :color="previewUser.status === 'active' ? 'success' : previewUser.status === 'banned' ? 'error' : 'neutral'" variant="soft">
                  {{ t(`admin.users.statusMap.${previewUser.status}`) }}
                </UBadge>
                <UBadge v-if="previewUser.isInitialSuperAdmin" color="warning" variant="soft">
                  {{ t('admin.users.initialSuperAdmin') }}
                </UBadge>
              </div>
              <dl class="grid gap-3 text-sm sm:grid-cols-2">
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.userId') }}</dt>
                  <dd class="mt-0.5 font-mono text-slate-900 dark:text-zinc-100">{{ previewUser.id }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.username') }}</dt>
                  <dd class="mt-0.5 font-semibold text-slate-900 dark:text-zinc-100">{{ previewUser.username }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.displayName') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.displayName) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.email') }}</dt>
                  <dd class="mt-0.5 break-all text-slate-900 dark:text-zinc-100">{{ previewUser.email }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.locale') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ previewUser.locale }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.createdAt') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ formatDateTime(previewUser.createdAt) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.updatedAt') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ formatDateTime(previewUser.updatedAt) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewPasswordChangedAt') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ formatDateTime(previewUser.passwordChangedAt || undefined) }}</dd>
                </div>
              </dl>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <h4 class="mb-3 text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.users.previewSectionProfile') }}
              </h4>
              <dl class="grid gap-3 text-sm sm:grid-cols-2">
                <div class="sm:col-span-2">
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.bio') }}</dt>
                  <dd class="mt-0.5 whitespace-pre-wrap text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.profile?.bio) }}</dd>
                </div>
                <div class="sm:col-span-2">
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.signature') }}</dt>
                  <dd class="mt-0.5 whitespace-pre-wrap text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.profile?.signature) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.location') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.profile?.location) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.websiteUrl') }}</dt>
                  <dd class="mt-0.5 break-all text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.profile?.websiteUrl) }}</dd>
                </div>
              </dl>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <h4 class="mb-3 text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.users.previewSectionActivity') }}
              </h4>
              <dl class="grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewTopicCount') }}</dt>
                  <dd class="mt-0.5 tabular-nums text-slate-900 dark:text-zinc-100">{{ previewUser.activity?.topicCount ?? 0 }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewCommentCount') }}</dt>
                  <dd class="mt-0.5 tabular-nums text-slate-900 dark:text-zinc-100">{{ previewUser.activity?.commentCount ?? 0 }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewActiveSessions') }}</dt>
                  <dd class="mt-0.5 tabular-nums text-slate-900 dark:text-zinc-100">{{ previewUser.activity?.activeSessionCount ?? 0 }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewTotalSessions') }}</dt>
                  <dd class="mt-0.5 tabular-nums text-slate-900 dark:text-zinc-100">{{ previewUser.activity?.totalSessionCount ?? 0 }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewLastLoginAt') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ formatDateTime(previewUser.activity?.lastLoginAt || undefined) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewLastSeenAt') }}</dt>
                  <dd class="mt-0.5 text-slate-900 dark:text-zinc-100">{{ formatDateTime(previewUser.activity?.lastSeenAt || undefined) }}</dd>
                </div>
                <div>
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewLastLoginIP') }}</dt>
                  <dd class="mt-0.5 font-mono text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.activity?.lastLoginIP) }}</dd>
                </div>
                <div class="sm:col-span-2 lg:col-span-3">
                  <dt class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.users.previewLastLoginUA') }}</dt>
                  <dd class="mt-0.5 break-all font-mono text-xs text-slate-900 dark:text-zinc-100">{{ displayOrDash(previewUser.activity?.lastLoginUserAgent) }}</dd>
                </div>
              </dl>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.users.previewSectionSessions') }}
                </h4>
                <div class="flex flex-wrap items-center gap-2">
                  <UBadge color="neutral" variant="soft">
                    {{ previewSessionsPageInfo.total }}
                  </UBadge>
                  <p v-if="previewSessionsPageInfo.total > 0" class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.previewListRange', {
                      start: previewSessionsPageInfo.start,
                      end: previewSessionsPageInfo.end,
                      total: previewSessionsPageInfo.total
                    }) }}
                  </p>
                </div>
              </div>
              <p v-if="!previewSessionsPageInfo.total" class="text-sm text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.previewNoSessions') }}
              </p>
              <div v-else class="space-y-3">
                <article
                  v-for="session in previewSessionsPageInfo.items"
                  :key="session.id"
                  class="rounded-md border border-slate-200 bg-white p-3 text-sm dark:border-zinc-800 dark:bg-zinc-950"
                >
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="font-semibold text-slate-900 dark:text-zinc-100">
                      {{ displayOrDash(session.deviceName || `${session.browser} / ${session.os}`) }}
                    </span>
                    <UBadge :color="session.isActive ? 'success' : 'neutral'" variant="soft">
                      {{ session.isActive ? t('admin.users.previewSessionActive') : t('admin.users.previewSessionRevoked') }}
                    </UBadge>
                    <code class="text-[11px] text-slate-400">{{ session.id }}</code>
                  </div>
                  <dl class="mt-2 grid gap-2 sm:grid-cols-2">
                    <div>
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionIP') }}</dt>
                      <dd class="font-mono text-xs">{{ displayOrDash(session.ipAddress || session.ipPrefix) }}</dd>
                    </div>
                    <div>
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionIPPrefix') }}</dt>
                      <dd class="font-mono text-xs">{{ displayOrDash(session.ipPrefix) }}</dd>
                    </div>
                    <div>
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionCreated') }}</dt>
                      <dd class="text-xs">{{ formatDateTime(session.createdAt) }}</dd>
                    </div>
                    <div>
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionLastSeen') }}</dt>
                      <dd class="text-xs">{{ formatDateTime(session.lastSeenAt) }}</dd>
                    </div>
                    <div v-if="!session.isActive && session.revokeReason" class="sm:col-span-2">
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionRevokeReason') }}</dt>
                      <dd class="font-mono text-xs">{{ session.revokeReason }} · {{ formatDateTime(session.revokedAt || undefined) }}</dd>
                    </div>
                    <div class="sm:col-span-2">
                      <dt class="text-xs text-slate-500">{{ t('admin.users.previewSessionUA') }}</dt>
                      <dd class="break-all font-mono text-[11px] text-slate-700 dark:text-zinc-300">{{ displayOrDash(session.userAgent) }}</dd>
                    </div>
                  </dl>
                </article>
                <div
                  v-if="previewSessionsPageInfo.totalPages > 1"
                  class="flex flex-col gap-2 border-t border-slate-200 pt-3 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
                >
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.pagination', {
                      page: previewSessionsPageInfo.page,
                      pages: previewSessionsPageInfo.totalPages
                    }) }}
                  </p>
                  <UPagination
                    v-model:page="previewSessionsPage"
                    :total="previewSessionsPageInfo.total"
                    :items-per-page="PREVIEW_SESSION_PAGE_SIZE"
                    size="sm"
                    class="justify-end"
                  />
                </div>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.users.previewSectionAuthEvents') }}
                </h4>
                <div class="flex flex-wrap items-center gap-2">
                  <UBadge color="neutral" variant="soft">
                    {{ previewAuthEventsPageInfo.total }}
                  </UBadge>
                  <p v-if="previewAuthEventsPageInfo.total > 0" class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.previewListRange', {
                      start: previewAuthEventsPageInfo.start,
                      end: previewAuthEventsPageInfo.end,
                      total: previewAuthEventsPageInfo.total
                    }) }}
                  </p>
                </div>
              </div>
              <p v-if="!previewAuthEventsPageInfo.total" class="text-sm text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.previewNoAuthEvents') }}
              </p>
              <div v-else class="space-y-3">
                <div class="overflow-x-auto">
                  <table class="min-w-full text-left text-sm">
                    <thead class="text-xs text-slate-500">
                      <tr>
                        <th class="px-2 py-2">{{ t('admin.users.previewAuthAction') }}</th>
                        <th class="px-2 py-2">{{ t('admin.users.previewAuthIP') }}</th>
                        <th class="px-2 py-2">{{ t('admin.users.previewAuthTime') }}</th>
                        <th class="px-2 py-2">{{ t('admin.users.previewAuthUA') }}</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-slate-200 dark:divide-zinc-800">
                      <tr v-for="event in previewAuthEventsPageInfo.items" :key="event.id">
                        <td class="px-2 py-2 align-top">
                          <UBadge color="neutral" variant="soft">{{ authActionLabel(event.action) }}</UBadge>
                          <p v-if="event.sessionHash" class="mt-1 font-mono text-[10px] text-slate-400">
                            {{ t('admin.users.previewAuthSessionHash') }}: {{ event.sessionHash.slice(0, 16) }}…
                          </p>
                        </td>
                        <td class="px-2 py-2 align-top font-mono text-xs">{{ displayOrDash(event.ipAddress) }}</td>
                        <td class="px-2 py-2 align-top text-xs tabular-nums">{{ formatDateTime(event.createdAt) }}</td>
                        <td class="px-2 py-2 align-top break-all font-mono text-[11px]">{{ displayOrDash(event.userAgent) }}</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
                <div
                  v-if="previewAuthEventsPageInfo.totalPages > 1"
                  class="flex flex-col gap-2 border-t border-slate-200 pt-3 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
                >
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.pagination', {
                      page: previewAuthEventsPageInfo.page,
                      pages: previewAuthEventsPageInfo.totalPages
                    }) }}
                  </p>
                  <UPagination
                    v-model:page="previewAuthEventsPage"
                    :total="previewAuthEventsPageInfo.total"
                    :items-per-page="PREVIEW_AUTH_EVENT_PAGE_SIZE"
                    size="sm"
                    class="justify-end"
                  />
                </div>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <h4 class="mb-3 text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.users.previewSectionRoles') }}
              </h4>
              <div class="flex flex-wrap gap-1.5">
                <UBadge
                  v-for="key in previewUser.roleKeys"
                  :key="key"
                  color="neutral"
                  variant="outline"
                >
                  {{ roles.find(role => role.key === key)?.alias || key }}
                </UBadge>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
                <h4 class="text-sm font-semibold text-slate-900 dark:text-zinc-100">
                  {{ t('admin.users.previewSectionPermissions') }}
                </h4>
                <div class="flex flex-wrap items-center gap-2">
                  <UBadge color="neutral" variant="soft">
                    {{ t('admin.users.previewPermissionCount', { count: previewPermissionsPageInfo.total }) }}
                  </UBadge>
                  <p v-if="previewPermissionsPageInfo.total > 0" class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.previewListRange', {
                      start: previewPermissionsPageInfo.start,
                      end: previewPermissionsPageInfo.end,
                      total: previewPermissionsPageInfo.total
                    }) }}
                  </p>
                </div>
              </div>
              <div v-if="!previewPermissionsPageInfo.total" class="text-sm text-slate-500 dark:text-zinc-400">
                {{ t('admin.users.previewEmptyValue') }}
              </div>
              <div v-else class="space-y-3">
                <div class="flex flex-wrap gap-1.5">
                  <code
                    v-for="key in previewPermissionsPageInfo.items"
                    :key="key"
                    class="rounded bg-white px-1.5 py-0.5 text-[11px] text-slate-700 dark:bg-zinc-950 dark:text-zinc-300"
                  >
                    {{ key }}
                  </code>
                </div>
                <div
                  v-if="previewPermissionsPageInfo.totalPages > 1"
                  class="flex flex-col gap-2 border-t border-slate-200 pt-3 sm:flex-row sm:items-center sm:justify-between dark:border-zinc-800"
                >
                  <p class="text-xs text-slate-500 dark:text-zinc-400">
                    {{ t('admin.users.pagination', {
                      page: previewPermissionsPageInfo.page,
                      pages: previewPermissionsPageInfo.totalPages
                    }) }}
                  </p>
                  <UPagination
                    v-model:page="previewPermissionsPage"
                    :total="previewPermissionsPageInfo.total"
                    :items-per-page="PREVIEW_PERMISSION_PAGE_SIZE"
                    size="sm"
                    class="justify-end"
                  />
                </div>
              </div>
            </section>

            <section class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-zinc-800 dark:bg-zinc-900/60">
              <h4 class="mb-3 text-sm font-semibold text-slate-900 dark:text-zinc-100">
                {{ t('admin.users.previewSectionOverrides') }}
              </h4>
              <div
                v-if="!(previewUser.permissionOverrides?.allow?.length || previewUser.permissionOverrides?.deny?.length)"
                class="text-sm text-slate-500 dark:text-zinc-400"
              >
                {{ t('admin.users.previewNoOverrides') }}
              </div>
              <div v-else class="grid gap-3 sm:grid-cols-2">
                <div>
                  <p class="mb-1 text-xs font-medium text-emerald-700 dark:text-emerald-300">
                    {{ t('admin.users.previewAllowOverrides') }}
                  </p>
                  <div class="flex flex-wrap gap-1">
                    <code
                      v-for="key in previewUser.permissionOverrides.allow"
                      :key="`allow-${key}`"
                      class="rounded bg-white px-1.5 py-0.5 text-[11px] dark:bg-zinc-950"
                    >{{ key }}</code>
                  </div>
                </div>
                <div>
                  <p class="mb-1 text-xs font-medium text-rose-700 dark:text-rose-300">
                    {{ t('admin.users.previewDenyOverrides') }}
                  </p>
                  <div class="flex flex-wrap gap-1">
                    <code
                      v-for="key in previewUser.permissionOverrides.deny"
                      :key="`deny-${key}`"
                      class="rounded bg-white px-1.5 py-0.5 text-[11px] dark:bg-zinc-950"
                    >{{ key }}</code>
                  </div>
                </div>
              </div>
            </section>
          </template>
        </div>

        <div class="flex flex-wrap justify-end gap-2 border-t border-slate-200 px-5 py-4 dark:border-zinc-800">
          <UButton
            v-if="previewUser"
            color="primary"
            variant="soft"
            leading-icon="i-lucide-panel-right-open"
            @click="manageFromPreview"
          >
            {{ t('admin.users.manage') }}
          </UButton>
          <UButton color="neutral" variant="ghost" @click="closeUserPreview">
            {{ t('admin.users.close') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
