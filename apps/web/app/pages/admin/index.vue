<script setup lang="ts">
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { apiErrorMessage } from '~/composables/useApiClient'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import SFAdminOverviewResourceCards from '~/components/admin/SFAdminOverviewResourceCards.vue'
import SFAdminOverviewRuntimeCard from '~/components/admin/SFAdminOverviewRuntimeCard.vue'
import SFAdminOverviewTrendTrio from '~/components/admin/SFAdminOverviewTrendTrio.vue'
import { useAdminOverviewLive } from '~/composables/admin/useAdminOverviewLive'
import {
  formatOverviewBytes,
  formatOverviewCount,
  formatOverviewDate,
  overviewActionTone,
  overviewActionsTone,
  overviewActionTotal,
  overviewAdvisoryTotal,
  overviewPercent,
  type AdminOverview,
  type AdminOverviewAction,
  type AdminOverviewExtensionWidget
} from '~/utils/admin/adminOverview'

definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

// 明确声明组件名用于 KeepAlive 匹配
defineOptions({
  name: 'AdminIndex'
})

const { t, te } = useI18n()
const { request } = useApiClient()
const adminRoutes = useAdminRoutes()
const adminPage = useAdminPage('/')

const {
  data: overview,
  pending,
  error,
  refresh
} = await useAsyncData<AdminOverview | null>(
  'admin-overview',
  () => request<AdminOverview>('/admin/overview'),
  { default: () => null }
)

// 资源 5s + KPI 30s；标签页隐藏 / KeepAlive 停用时自动停止。
useAdminOverviewLive({ overview, request })

const kpiCards = computed(() => {
  const data = overview.value
  if (!data) return []
  const actions = data.actions || []
  // 「待处理」只算真实待办；孤儿附件是 advisory，只进副标题说明。
  const actionCount = overviewActionTotal(actions)
  const advisoryCount = overviewAdvisoryTotal(actions)
  const reports = formatOverviewCount(data.moderation.openCount + data.moderation.reviewingCount)
  const actionsMeta = advisoryCount > 0
    ? `${t('admin.home.kpi.actions.meta', { reports })} · ${t('admin.home.kpi.actions.metaAdvisory', { count: formatOverviewCount(advisoryCount) })}`
    : t('admin.home.kpi.actions.meta', { reports })
  return [
    {
      key: 'topics',
      label: t('admin.home.kpi.topics.label'),
      value: formatOverviewCount(data.community.postCount),
      meta: t('admin.home.kpi.topics.meta', {
        topics: formatOverviewCount(data.community.topicCount),
        comments: formatOverviewCount(data.community.commentCount)
      }),
      icon: 'i-lucide-message-square-text',
      tone: 'text-blue-600 dark:text-blue-400',
      to: ''
    },
    {
      key: 'users',
      label: t('admin.home.kpi.users.label'),
      value: formatOverviewCount(data.community.userCount),
      meta: t('admin.home.kpi.users.meta', { active: formatOverviewCount(data.community.activeUserCount) }),
      icon: 'i-lucide-users-round',
      tone: 'text-green-600 dark:text-green-400',
      to: ''
    },
    {
      key: 'actions',
      label: t('admin.home.kpi.actions.label'),
      value: formatOverviewCount(actionCount),
      meta: actionsMeta,
      icon: 'i-lucide-list-checks',
      tone: overviewActionsTone(actions) === 'warning'
        ? 'text-amber-600 dark:text-amber-400'
        : 'text-emerald-600 dark:text-emerald-400',
      to: '#admin-todo'
    }
  ]
})

const healthRows = computed(() => {
  const data = overview.value
  if (!data) return []
  return [
    {
      label: t('admin.home.health.activeUsers'),
      value: `${overviewPercent(data.community.activeUserCount, data.community.userCount)}%`,
      sub: t('admin.home.health.activeUsersSub', {
        active: formatOverviewCount(data.community.activeUserCount),
        total: formatOverviewCount(data.community.userCount)
      }),
      percent: overviewPercent(data.community.activeUserCount, data.community.userCount)
    },
    {
      label: t('admin.home.health.activeTopics'),
      value: `${overviewPercent(data.community.activeTopicCount + data.community.lockedTopicCount, data.community.topicCount)}%`,
      sub: t('admin.home.health.activeTopicsSub', {
        active: formatOverviewCount(data.community.activeTopicCount + data.community.lockedTopicCount),
        total: formatOverviewCount(data.community.topicCount)
      }),
      percent: overviewPercent(data.community.activeTopicCount + data.community.lockedTopicCount, data.community.topicCount)
    },
    {
      label: t('admin.home.health.attachments'),
      value: `${overviewPercent(data.attachments.activeCount, data.attachments.totalCount)}%`,
      sub: t('admin.home.health.attachmentsSub', {
        orphan: formatOverviewCount(data.attachments.orphanCount),
        size: formatOverviewBytes(data.attachments.totalBytes)
      }),
      percent: overviewPercent(data.attachments.activeCount, data.attachments.totalCount)
    }
  ]
})

const quickLinks = computed(() => {
  const data = overview.value
  return [
    {
      title: t('admin.home.quick.users.title'),
      description: t('admin.home.quick.users.description', { count: formatOverviewCount(data?.community.userCount || 0) }),
      icon: 'i-lucide-contact',
      to: adminRoutes.path('/users')
    },
    {
      title: t('admin.home.quick.forum.title'),
      description: t('admin.home.quick.forum.description', { count: formatOverviewCount(data?.community.categoryCount || 0) }),
      icon: 'i-lucide-folder-tree',
      to: adminRoutes.path('/forum/categories')
    },
    {
      title: t('admin.home.quick.moderation.title'),
      description: t('admin.home.quick.moderation.description', { count: formatOverviewCount((data?.moderation.openCount || 0) + (data?.moderation.reviewingCount || 0)) }),
      icon: 'i-lucide-shield-alert',
      to: adminRoutes.path('/moderation')
    },
    {
      title: t('admin.home.quick.extensions.title'),
      description: t('admin.home.quick.extensions.description', { count: formatOverviewCount(data?.extensions.totalCount || 0) }),
      icon: 'i-lucide-blocks',
      to: adminRoutes.path('/extensions')
    }
  ]
})

useSeoMeta({
  title: t('admin.home.metaTitle')
})

function actionLabel(action: AdminOverviewAction) {
  return t(`admin.home.actions.${action.key}.title`)
}

function actionDescription(action: AdminOverviewAction) {
  return t(`admin.home.actions.${action.key}.description`, { count: formatOverviewCount(action.count) })
}

function actionRoute(action: AdminOverviewAction) {
  return adminRoutes.path(action.route)
}

function actionColor(action: AdminOverviewAction) {
  // advisory 行只是信息提示，用中性徽章，避免和真实待办抢告警色。
  if (action.advisory) return 'neutral'
  const tone = overviewActionTone(action.severity)
  return tone === 'danger' ? 'error' : tone
}

function actionAdvisoryHint(action: AdminOverviewAction) {
  const key = `admin.home.actions.${action.key}.advisoryHint`
  return te(key) ? t(key) : ''
}

const { locale } = useI18n()

const extensionWidgets = computed(() => overview.value?.extensionWidgets || [])

function extensionWidgetLabel(widget: AdminOverviewExtensionWidget) {
  const labels = widget.label || {}
  return labels[String(locale.value)] || labels['zh-CN'] || labels['en-US'] || Object.values(labels)[0] || widget.id
}

function extensionWidgetRoute(widget: AdminOverviewExtensionWidget) {
  return adminRoutes.path(widget.route)
}

function extensionWidgetColor(widget: AdminOverviewExtensionWidget) {
  const tone = overviewActionTone(widget.severity)
  return tone === 'danger' ? 'error' : tone
}
</script>

<template>
  <div class="mb-4 flex flex-col gap-1">
    <h2 class="text-xl font-bold flex items-center gap-2 text-slate-900 dark:text-zinc-100">
      <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
      {{ t('admin.home.title') }}
    </h2>
    <p class="text-sm text-slate-500 dark:text-zinc-400">
      {{ t('admin.home.intro') }}
    </p>
  </div>

  <UDashboardToolbar class="max-sm:flex-col max-sm:items-stretch max-sm:justify-start max-sm:gap-2 border border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 rounded-lg px-4 py-2.5 mb-6 text-slate-500 dark:text-zinc-400">
    <template #left>
      <div class="flex min-w-0 flex-col gap-0.5 text-sm sm:flex-row sm:items-center sm:gap-2">
        <div class="flex min-w-0 items-center gap-2">
          <UIcon name="i-lucide-activity" class="size-4 shrink-0" />
          <span class="truncate">
            {{ overview ? t('admin.home.toolbar.generatedAt', { time: formatOverviewDate(overview.generatedAt), days: overview.windowDays }) : t('admin.home.toolbar.loading') }}
          </span>
        </div>
        <span class="hidden text-xs text-slate-400 dark:text-zinc-500 sm:inline">
          {{ t('admin.home.toolbar.live') }}
        </span>
      </div>
    </template>
    <template #right>
      <div class="flex items-center justify-end gap-3 max-sm:w-full">
        <UButton
          icon="i-lucide-rotate-cw"
          color="neutral"
          variant="subtle"
          class="shrink-0 whitespace-nowrap"
          :aria-label="t('admin.home.refresh')"
          :title="t('admin.home.refresh')"
          :loading="pending"
          @click="refresh()"
        >
          <span class="hidden sm:inline">{{ t('admin.home.refresh') }}</span>
        </UButton>
        <UBadge color="neutral" variant="soft" class="hidden border border-slate-200 font-mono dark:border-zinc-800 sm:inline-flex">
          {{ adminRoutes.prefix }}
        </UBadge>
      </div>
    </template>
  </UDashboardToolbar>

  <UAlert
    v-if="error"
    color="error"
    icon="i-lucide-triangle-alert"
    variant="subtle"
    :title="apiErrorMessage(error) || t('admin.home.loadFailed')"
    class="mb-6"
  />

  <div v-if="pending && !overview" class="grid gap-5 lg:grid-cols-3">
    <UCard v-for="index in 3" :key="index" class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900">
      <SFSkeleton :lines="3" />
    </UCard>
  </div>

  <div v-else-if="overview" class="flex flex-col gap-6">
    <SFAdminOverviewResourceCards :runtime="overview.runtime" />

    <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
      <component
        :is="card.to ? 'NuxtLink' : 'div'"
        v-for="card in kpiCards"
        :key="card.key"
        :to="card.to || undefined"
        class="group block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] focus-visible:ring-offset-2 dark:focus-visible:ring-offset-zinc-950"
      >
        <UCard
          class="elegant-card h-full border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100"
          :class="card.to ? 'transition group-hover:border-[var(--sf-accent-soft-border)] dark:group-hover:border-[rgb(var(--sf-accent-rgb)/0.35)]' : ''"
        >
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <p class="text-xs font-semibold text-slate-500 dark:text-zinc-400 uppercase">
                {{ card.label }}
              </p>
              <p class="mt-2.5 truncate text-3xl font-black text-slate-900 dark:text-white">
                {{ card.value }}
              </p>
              <p class="mt-2 line-clamp-2 text-xs text-slate-500 dark:text-zinc-400">
                {{ card.meta }}
              </p>
            </div>
            <span class="icon-glass-box shrink-0" :class="card.tone">
              <UIcon :name="card.icon" class="size-5 z-10" />
            </span>
          </div>
        </UCard>
      </component>
    </div>

    <!-- 社区趋势整行：横向三系列，不与旁侧卡抢高度 -->
    <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h3 class="text-base font-bold text-slate-900 dark:text-white">
              {{ t('admin.home.trend.title') }}
            </h3>
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400">
              {{ t('admin.home.trend.description', { days: overview.windowDays }) }}
            </p>
          </div>
          <UIcon name="i-lucide-chart-no-axes-combined" class="size-5 text-slate-400 dark:text-zinc-500" />
        </div>
      </template>

      <SFAdminOverviewTrendTrio
        :days="overview.trends.days"
        :window-days="overview.windowDays"
      />
    </UCard>

    <div class="grid gap-6 xl:grid-cols-3">
      <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
        <template #header>
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.health.title') }}
          </h3>
        </template>
        <div class="space-y-4">
          <div v-for="row in healthRows" :key="row.label">
            <div class="mb-2 flex items-center justify-between gap-3">
              <div class="min-w-0">
                <p class="truncate text-sm font-semibold text-slate-900 dark:text-white">
                  {{ row.label }}
                </p>
                <p class="truncate text-xs text-slate-500 dark:text-zinc-400">
                  {{ row.sub }}
                </p>
              </div>
              <span class="shrink-0 font-mono text-sm font-bold text-slate-700 dark:text-zinc-200">{{ row.value }}</span>
            </div>
            <div class="h-2 overflow-hidden rounded-full bg-slate-100 dark:bg-zinc-800">
              <span class="block h-full rounded-full bg-[var(--sf-accent)] dark:bg-[var(--sf-accent-dark)]" :style="{ width: `${row.percent}%` }" />
            </div>
          </div>
        </div>
      </UCard>

      <UCard id="admin-todo" class="elegant-card scroll-mt-24 border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
        <template #header>
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.actionsTitle') }}
          </h3>
        </template>
        <div class="divide-y divide-slate-100 dark:divide-zinc-800/60">
          <NuxtLink
            v-for="action in overview.actions"
            :key="action.key"
            :to="actionRoute(action)"
            class="flex items-center gap-3 rounded-md px-2 py-3 transition hover:bg-slate-50 dark:hover:bg-zinc-800/40"
          >
            <UBadge :color="actionColor(action)" variant="soft" class="shrink-0 font-mono">
              {{ formatOverviewCount(action.count) }}
            </UBadge>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-semibold text-slate-900 dark:text-white">
                {{ actionLabel(action) }}
              </span>
              <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">
                {{ actionDescription(action) }}
              </span>
              <span v-if="action.advisory && actionAdvisoryHint(action)" class="block truncate text-xs text-slate-400 dark:text-zinc-500">
                {{ actionAdvisoryHint(action) }}
              </span>
            </span>
            <UIcon name="i-lucide-arrow-right" class="size-4 shrink-0 text-slate-400 dark:text-zinc-500" />
          </NuxtLink>
        </div>
      </UCard>

      <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
        <template #header>
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.topCategories.title') }}
          </h3>
        </template>
        <div v-if="overview.topCategories.length" class="space-y-3">
          <NuxtLink
            v-for="category in overview.topCategories"
            :key="category.id"
            :to="adminRoutes.path('/forum/categories')"
            class="flex items-center justify-between gap-3 rounded-md border border-slate-100 px-3 py-3 transition hover:bg-slate-50 dark:border-zinc-800 dark:hover:bg-zinc-800/40"
          >
            <span class="min-w-0">
              <span class="block truncate text-sm font-semibold text-slate-900 dark:text-white">{{ category.name }}</span>
              <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">{{ category.slug }}</span>
            </span>
            <span class="shrink-0 text-right text-xs text-slate-500 dark:text-zinc-400">
              <strong class="block text-sm text-slate-900 dark:text-white">{{ formatOverviewCount(category.topicCount) }}</strong>
              {{ t('admin.home.topCategories.topicUnit') }}
            </span>
          </NuxtLink>
        </div>
        <SFEmptyState
          v-else
          icon-label="DB"
          :title="t('admin.home.topCategories.emptyTitle')"
          :description="t('admin.home.topCategories.emptyDescription')"
        />
      </UCard>
    </div>

    <UCard
      v-if="extensionWidgets.length"
      class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100"
    >
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.extensionWidgetsTitle') }}
          </h3>
          <UIcon name="i-lucide-puzzle" class="size-5 text-slate-400 dark:text-zinc-500" />
        </div>
      </template>
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        <NuxtLink
          v-for="widget in extensionWidgets"
          :key="`${widget.extensionId}:${widget.id}`"
          :to="extensionWidgetRoute(widget)"
          class="group flex min-w-0 items-center gap-3 rounded-md border border-slate-100 px-3 py-3 transition hover:bg-slate-50 dark:border-zinc-800 dark:hover:bg-zinc-800/40"
        >
          <span class="icon-glass-box shrink-0 text-slate-600 dark:text-zinc-300">
            <UIcon :name="widget.icon || 'i-lucide-blocks'" class="size-5 z-10" />
          </span>
          <span class="min-w-0 flex-1">
            <span class="block truncate text-sm font-semibold text-slate-900 dark:text-white">
              {{ extensionWidgetLabel(widget) }}
            </span>
            <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">
              {{ widget.extensionId }}
            </span>
          </span>
          <UBadge :color="extensionWidgetColor(widget)" variant="soft" class="shrink-0">
            {{ widget.severity }}
          </UBadge>
        </NuxtLink>
      </div>
    </UCard>

    <UCard class="elegant-card border-slate-200 dark:border-zinc-800 bg-white dark:bg-zinc-900 text-slate-900 dark:text-zinc-100">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('admin.home.quickTitle') }}
          </h3>
          <UIcon name="i-lucide-route" class="size-5 text-slate-400 dark:text-zinc-500" />
        </div>
      </template>
      <div class="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
        <NuxtLink
          v-for="link in quickLinks"
          :key="link.to"
          :to="link.to"
          class="group flex min-w-0 items-center gap-3 rounded-md border border-slate-100 px-3 py-3 transition hover:border-[var(--sf-accent-soft-border)] hover:bg-slate-50 dark:border-zinc-800 dark:hover:border-[rgb(var(--sf-accent-rgb)/0.35)] dark:hover:bg-zinc-800/40"
        >
          <span class="icon-glass-box shrink-0 text-slate-600 dark:text-zinc-300">
            <UIcon :name="link.icon" class="size-5 z-10" />
          </span>
          <span class="min-w-0 flex-1">
            <span class="block truncate text-sm font-semibold text-slate-900 dark:text-white">
              {{ link.title }}
            </span>
            <span class="block truncate text-xs text-slate-500 dark:text-zinc-400">
              {{ link.description }}
            </span>
          </span>
          <UIcon name="i-lucide-arrow-right" class="size-4 shrink-0 text-slate-400 transition group-hover:text-[var(--sf-accent)] dark:text-zinc-500" />
        </NuxtLink>
      </div>
    </UCard>

    <SFAdminOverviewRuntimeCard :runtime="overview.runtime" />
  </div>
</template>
