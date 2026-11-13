<script setup lang="ts">
import { notificationFilters, notificationTypeIcon, type NotificationFilter } from '~/utils/notifications/notificationsPresentation'

const props = defineProps<{
  activeFilter: NotificationFilter
  counts: ReadonlyMap<NotificationFilter, number>
  loadedCount: number
}>()

const emit = defineEmits<{
  select: [filter: NotificationFilter]
}>()

const { t } = useI18n()

// 只有「全部 / 未读」是范围而不是类型，其余类型的图标来自共享目录，
// 本地不再维护第二份映射（漏项时会静默退化成铃铛，两处还会各自漂移）。
const filterIcons: Record<string, string> = {
  all: 'i-tabler-bell',
  unread: 'i-tabler-point-filled'
}

function filterIcon(filter: NotificationFilter) {
  return filterIcons[filter] || notificationTypeIcon(filter)
}

function filterCount(filter: NotificationFilter) {
  return props.counts.get(filter) || 0
}
</script>

<template>
  <nav class="sforum-notification-type-nav" :aria-label="t('notifications.filter.aria')">
    <div class="sforum-notification-type-nav__label">{{ t('notifications.filter.title') }}</div>
    <button
      v-for="filter in notificationFilters"
      :key="filter"
      type="button"
      class="sforum-notification-type-nav__link"
      :class="{ 'is-active': activeFilter === filter }"
      :aria-pressed="activeFilter === filter"
      @click="emit('select', filter)"
    >
      <span class="sforum-notification-type-nav__link-main">
        <UIcon :name="filterIcon(filter)" class="size-[18px]" aria-hidden="true" />
        {{ t(`notifications.filter.${filter}`) }}
      </span>
      <span class="sforum-notification-type-nav__count">{{ filterCount(filter) }}</span>
    </button>
    <p class="sforum-notification-type-nav__scope">
      {{ t('notifications.filter.loadedScope', { count: loadedCount }) }}
    </p>
  </nav>
</template>

<style scoped>
.sforum-notification-type-nav {
  padding: 0;
}

.sforum-notification-type-nav__label {
  padding: 10px 16px 6px;
  color: var(--sf-public-text-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0;
}

.sforum-notification-type-nav__link {
  width: 100%;
  min-height: 34px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  border: 0;
  border-left: 2px solid transparent;
  border-radius: 0;
  padding: 7px 16px;
  background: transparent;
  color: var(--sf-public-text-secondary);
  font-size: 13px;
  font-weight: 400;
  text-align: left;
  cursor: pointer;
}

.sforum-notification-type-nav__link:hover {
  background: var(--sf-public-surface-muted);
  color: var(--sf-public-text);
}

.sforum-notification-type-nav__link.is-active {
  position: relative;
  border-left-color: var(--sf-accent);
  background: var(--sf-accent-soft);
  color: var(--sf-accent);
  font-weight: 600;
}

:global(.dark) .sforum-notification-type-nav__link.is-active {
  color: var(--sf-accent-dark, var(--sf-accent));
}

.sforum-notification-type-nav__link-main {
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: 11px;
}

.sforum-notification-type-nav__count {
  color: var(--sf-public-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.sforum-notification-type-nav__link.is-active .sforum-notification-type-nav__count {
  color: inherit;
}

.sforum-notification-type-nav__scope {
  margin: 6px 16px 0;
  color: var(--sf-public-text-muted);
  font-size: 12px;
  line-height: 1.6;
}
</style>
