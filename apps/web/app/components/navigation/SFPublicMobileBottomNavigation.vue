<script setup lang="ts">
import { useNotifications } from '~/composables/notifications/useNotifications'

const props = defineProps<{
  canCreateTopic: boolean
  authenticated: boolean
}>()

const { t } = useI18n()
const localePath = useLocalePath()
const route = useRoute()
const notifications = useNotifications()
</script>

<template>
  <nav class="sf-public-mobile-bottom-nav" :aria-label="t('nav.mainNav')">
    <NuxtLink
      :to="localePath('/')"
      class="sf-public-mobile-bottom-nav__link"
      :class="{ 'is-active': route.path === localePath('/') }"
    >
      <UIcon name="i-lucide-house" class="size-5" aria-hidden="true" />
      <span>{{ t('nav.home') }}</span>
    </NuxtLink>
    <NuxtLink
      :to="canCreateTopic ? localePath('/topics/new') : localePath('/login')"
      class="sf-public-mobile-bottom-nav__link"
      :class="{ 'is-active': route.path.startsWith(localePath('/topics/new')) }"
      :aria-label="t('nav.newTopic')"
    >
      <UIcon name="i-lucide-square-pen" class="size-5" aria-hidden="true" />
      <span>{{ t('nav.newTopic') }}</span>
    </NuxtLink>
    <NuxtLink
      :to="authenticated ? localePath('/notifications') : localePath('/login')"
      class="sf-public-mobile-bottom-nav__link sf-public-mobile-bottom-nav__notifications"
      :class="{ 'is-active': route.path.startsWith(localePath('/notifications')) }"
      :aria-label="t('nav.notifications')"
    >
      <span class="sf-public-mobile-bottom-nav__icon-wrap">
        <UIcon name="i-lucide-bell" class="size-5" aria-hidden="true" />
        <!-- 同 SFNotificationPreview：未读数在 SSR 时恒为 0，客户端首帧已有值会 node mismatch。 -->
        <ClientOnly>
          <span v-if="authenticated && notifications.unreadCount.value" class="sf-public-mobile-bottom-nav__badge">
            {{ notifications.unreadCount.value > 99 ? '99+' : notifications.unreadCount.value }}
          </span>
        </ClientOnly>
      </span>
      <span>{{ t('nav.notifications') }}</span>
    </NuxtLink>
  </nav>
</template>

<style scoped>
.sf-public-mobile-bottom-nav {
  display: none;
}

@media (max-width: 980px) {
  .sf-public-mobile-bottom-nav {
    position: fixed;
    right: 0;
    bottom: 0;
    left: 0;
    z-index: 40;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    height: 60px;
    padding: 8px 28px 7px;
    background: color-mix(in srgb, var(--sf-public-surface) 97%, transparent);
    box-shadow: 0 -2px 14px rgb(51 56 80 / 0.08);
    backdrop-filter: blur(12px);
  }

  .sf-public-mobile-bottom-nav__link {
    position: relative;
    display: flex;
    min-width: 0;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 2px;
    color: var(--sf-public-text-muted);
    font-size: 10px;
    font-weight: 650;
    text-decoration: none;
  }

  .sf-public-mobile-bottom-nav__link.is-active {
    color: var(--sf-accent);
  }

  .sf-public-mobile-bottom-nav__icon-wrap {
    position: relative;
    display: inline-flex;
  }

  .sf-public-mobile-bottom-nav__badge {
    position: absolute;
    top: -5px;
    left: 13px;
    min-width: 14px;
    height: 14px;
    display: grid;
    place-items: center;
    border-radius: 50%;
    padding: 0 3px;
    background: #e05260;
    color: #fff;
    font-size: 9px;
    font-style: normal;
    font-weight: 700;
    line-height: 1;
  }
}

@media (max-width: 520px) {
  .sf-public-mobile-bottom-nav {
    padding-right: 24px;
    padding-left: 24px;
  }
}
</style>
