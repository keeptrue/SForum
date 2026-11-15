<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { useAccountSecurityApi } from '~/composables/identity/useAccountSecurityApi'
import type { LoginSession, LoginSessionList } from '~/composables/identity/useAccountSecurityApi'
import SFSettingsShell from '~/components/settings/SFSettingsShell.vue'
import SFSettingsSecurityTabNav from '~/components/settings/SFSettingsSecurityTabNav.vue'
/**
 * 宿主 body 岛：forum.settings.security。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 * 三栏 chrome 由 SFSettingsShell 提供；本页是「账号安全」的默认 tab（设备与会话），
 * 登录方式、密码是 /settings/security 下的兄弟 tab 路由。
 */

const { t } = useI18n()
const toast = useToast()
const sessionsApi = useAccountSecurityApi()

useSForumSeo({
  title: () => t('accountSecurity.metaTitle'),
  description: () => t('accountSecurity.metaDescription'),
  type: 'website',
  noindex: true
})

const emptySessionList = (): LoginSessionList => ({
  items: [],
  total: 0,
  page: 1,
  perPage: 20
})
const HISTORY_PAGE_SIZE = 10
const emptyHistorySessionList = (): LoginSessionList => ({
  items: [],
  total: 0,
  page: 1,
  perPage: HISTORY_PAGE_SIZE
})

const { data: sessions, pending, refresh } = await useAsyncData(
  'account-security-sessions',
  () => sessionsApi.listSessions(),
  { default: emptySessionList }
)
const activeSessions = computed(() => sessions.value?.items || [])
const currentSession = computed(() => activeSessions.value.find(session => session.isCurrent) || null)

// 登录历史默认显示，并通过后端分页读取，避免一次性拉取较长历史。
const historyPage = ref(1)
const { data: historyList, pending: historyLoading, refresh: refreshHistory } = await useAsyncData(
  'account-security-session-history',
  () => sessionsApi.listSessions({ includeHistory: true, page: historyPage.value, perPage: HISTORY_PAGE_SIZE }),
  { default: emptyHistorySessionList, watch: [historyPage] }
)
const historySessions = computed(() => historyList.value?.items || [])
const historyTotalPages = computed(() => Math.max(1, Math.ceil((historyList.value?.total || 0) / Math.max(historyList.value?.perPage || HISTORY_PAGE_SIZE, 1))))

function selectHistoryPage(page: number) {
  historyPage.value = Math.min(historyTotalPages.value, Math.max(1, page))
}

async function refreshSessionLists() {
  historyPage.value = 1
  await Promise.all([refresh(), refreshHistory()])
}

async function revokeDevice(session: LoginSession) {
  if (session.isCurrent) {
    return
  }
  try {
    await sessionsApi.revokeSession(session.id)
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('accountSecurity.deviceRevoked') })
    await refreshSessionLists()
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accountSecurity.revokeFailed') })
  }
}

const revokingOthers = ref(false)
async function revokeOthers() {
  if (revokingOthers.value) {
    return
  }
  revokingOthers.value = true
  try {
    const result = await sessionsApi.revokeOtherSessions()
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('accountSecurity.othersRevoked', { count: result.revoked })
    })
    await refreshSessionLists()
  } catch (error) {
    toast.add({ color: 'error', duration: 0, icon: 'i-lucide-alert-triangle', title: apiErrorMessage(error) || t('accountSecurity.revokeFailed') })
  } finally {
    revokingOthers.value = false
  }
}

const { format: formatSiteDateTime } = useSiteDateTime()

function formatTime(iso: string): string {
  return formatSiteDateTime(iso)
}
</script>

<template>
  <SFSettingsShell
    class="sforum-settings-security"
    data-sforum-island-body="identity.component.security_settings"
    active="security"
    title-id="security-settings-title"
    :title="t('accountSecurity.title')"
    :description="t('accountSecurity.intro')"
    :rail-label="t('accountSecurity.rail.ariaLabel')"
  >
    <template #head-actions>
      <SFButton
        variant="secondary"
        size="sm"
        :disabled="revokingOthers || activeSessions.length <= 1"
        @click="revokeOthers"
      >
        <UIcon name="i-lucide-log-out" class="mr-1" />
        {{ t('accountSecurity.revokeOthers') }}
      </SFButton>
    </template>

    <SFSettingsSecurityTabNav />

    <h2 class="text-lg font-semibold text-slate-900 dark:text-zinc-50">
      {{ t('accountSecurity.devicesTitle') }}
    </h2>
    <p class="mt-1 text-sm text-slate-500 dark:text-zinc-400">
      {{ t('accountSecurity.devicesIntro') }}
    </p>

    <SFCard class="mt-4 p-0 overflow-hidden">
      <div v-if="pending && activeSessions.length === 0" class="divide-y divide-slate-100 dark:divide-zinc-800">
        <div v-for="i in 3" :key="i" class="p-4">
          <SFSkeleton class="h-4 w-1/3 mb-2" />
          <SFSkeleton class="h-3 w-1/2" />
        </div>
      </div>

      <ul v-else-if="activeSessions.length > 0" class="divide-y divide-slate-100 dark:divide-zinc-800">
        <li
          v-for="session in activeSessions"
          :key="session.id"
          class="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between"
        >
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <UIcon
                :name="session.isCurrent ? 'i-lucide-monitor-check' : 'i-lucide-monitor'"
                class="text-slate-400 shrink-0"
              />
              <span class="font-medium text-slate-900 dark:text-zinc-100 truncate">
                {{ session.deviceName || t('accountSecurity.unknownDevice') }}
              </span>
              <span
                v-if="session.isCurrent"
                class="inline-flex items-center rounded-full bg-teal-50 px-2 py-0.5 text-xs font-medium text-teal-700 dark:bg-teal-900/30 dark:text-teal-300"
              >
                {{ t('accountSecurity.currentDevice') }}
              </span>
            </div>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('accountSecurity.ip') }}: {{ session.ipPrefix || t('accountSecurity.unknown') }}
              · {{ t('accountSecurity.loggedIn') }}: {{ formatTime(session.createdAt) }}
              · {{ t('accountSecurity.lastActive') }}: {{ formatTime(session.lastSeenAt) }}
            </p>
          </div>
          <div class="shrink-0">
            <SFButton
              variant="ghost"
              size="sm"
              :disabled="session.isCurrent"
              @click="revokeDevice(session)"
            >
              <UIcon name="i-lucide-x" class="mr-1" />
              {{ t('accountSecurity.revoke') }}
            </SFButton>
          </div>
        </li>
      </ul>

      <SFEmptyState
        v-else
        :title="t('accountSecurity.emptyTitle')"
        :description="t('accountSecurity.emptyDescription')"
      />
    </SFCard>

    <section class="mt-6">
      <h2 class="flex items-center gap-2 text-lg font-semibold text-slate-900 dark:text-zinc-50">
        <UIcon name="i-lucide-history" class="text-slate-400" />
        {{ t('accountSecurity.showHistory') }}
      </h2>

      <SFCard class="p-0 mt-3 overflow-hidden">
        <div v-if="historyLoading" class="p-4">
          <SFSkeleton class="h-4 w-1/2 mb-2" />
          <SFSkeleton class="h-3 w-1/3" />
        </div>
        <ul v-else-if="historySessions.length > 0" class="divide-y divide-slate-100 dark:divide-zinc-800">
          <li
            v-for="session in historySessions"
            :key="session.id"
            class="p-4"
          >
            <div class="flex items-center gap-2">
              <UIcon
                :name="session.revokedAt ? 'i-lucide-monitor-off' : 'i-lucide-monitor'"
                class="text-slate-400 shrink-0"
              />
              <span class="font-medium text-slate-700 dark:text-zinc-300 truncate">
                {{ session.deviceName || t('accountSecurity.unknownDevice') }}
              </span>
            </div>
            <p class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
              {{ t('accountSecurity.loggedIn') }}: {{ formatTime(session.createdAt) }}
              <template v-if="session.revokedAt">
                · {{ t('accountSecurity.revokedAt') }}: {{ formatTime(session.revokedAt) }}
              </template>
              <template v-else>
                · {{ t('accountSecurity.lastActive') }}: {{ formatTime(session.lastSeenAt) }}
              </template>
            </p>
          </li>
        </ul>
        <p v-else class="p-4 text-sm text-slate-400 dark:text-zinc-500">
          {{ t('accountSecurity.noHistory') }}
        </p>
        <div
          v-if="historyTotalPages > 1"
          class="border-t border-slate-100 px-4 py-3 dark:border-zinc-800"
        >
          <SFPagination
            :page="historyList?.page || historyPage"
            :total-pages="historyTotalPages"
            @update:page="selectHistoryPage"
          />
        </div>
      </SFCard>
    </section>

    <template #rail>
      <section class="sforum-settings__rail-section">
        <div class="sforum-settings__rail-head">
          <h2>{{ t('accountSecurity.rail.devicesTitle') }}</h2>
          <span>{{ t('accountSecurity.rail.live') }}</span>
        </div>
        <div class="sforum-settings__summary">
          <strong>{{ activeSessions.length }}</strong>
          <span>{{ t('accountSecurity.rail.devicesLabel') }}</span>
        </div>
        <p class="sforum-settings__rail-help">{{ t('accountSecurity.rail.devicesHelp') }}</p>
      </section>

      <section class="sforum-settings__rail-section">
        <div class="sforum-settings__rail-head">
          <h2>{{ t('accountSecurity.rail.overviewTitle') }}</h2>
          <span>{{ t('accountSecurity.rail.overviewHint') }}</span>
        </div>
        <dl class="sforum-settings__stats">
          <div>
            <dt>{{ t('accountSecurity.rail.currentDevice') }}</dt>
            <dd>{{ currentSession?.deviceName || t('accountSecurity.unknownDevice') }}</dd>
          </div>
        </dl>
      </section>
    </template>
  </SFSettingsShell>
</template>
