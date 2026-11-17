<script setup lang="ts">
/**
 * 账号设置左栏 after-nav：资料 / 账号安全（及可选公开主页）。
 * 「账号安全」是合并后的单一入口：/settings/security 下的三个子路由
 * （设备与会话 / 登录方式 / 登录密码）都把 active 传为 security，
 * 共享 tab 导航见 SFSettingsSecurityTabNav。
 * 样式复用 sf-home-navigation 链接语言，与通知/审核 after-nav 一致。
 */
import type { AccountSettingsNavItem } from '~/composables/settings/useAccountSettingsNavigation'

const props = withDefaults(defineProps<{
  active: string
  publicProfilePath?: string
  extensionItems?: AccountSettingsNavItem[]
}>(), {
  extensionItems: () => []
})

const emit = defineEmits<{
  navigate: []
}>()

const { t } = useI18n()
const localePath = useLocalePath()

function itemPath(item: AccountSettingsNavItem) {
  return item.href.startsWith('/') ? item.href : '/'
}
</script>

<template>
  <nav class="sforum-settings__account-nav" :aria-label="t('profileSettings.nav.ariaLabel')">
    <div class="sf-home-navigation__label">{{ t('profileSettings.nav.account') }}</div>
    <NuxtLink
      :to="localePath('/settings/profile')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'profile' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-user-round" class="size-[18px]" aria-hidden="true" />
        {{ t('profileSettings.title') }}
      </span>
    </NuxtLink>
    <NuxtLink
      :to="localePath('/settings/appearance')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'appearance' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-palette" class="size-[18px]" aria-hidden="true" />
        {{ t('userAppearanceSettings.title') }}
      </span>
    </NuxtLink>
    <NuxtLink
      :to="localePath('/settings/security')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'security' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-shield-check" class="size-[18px]" aria-hidden="true" />
        {{ t('accountSecurity.title') }}
      </span>
    </NuxtLink>
    <NuxtLink
      :to="localePath('/settings/tokens')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'tokens' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-key-round" class="size-[18px]" aria-hidden="true" />
        {{ t('accessTokensSettings.title') }}
      </span>
    </NuxtLink>
    <NuxtLink
      :to="localePath('/settings/notifications')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'notifications' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-bell" class="size-[18px]" aria-hidden="true" />
        {{ t('notificationSettings.title') }}
      </span>
    </NuxtLink>
    <!--
      我的内容（待审 / 被退回）：作者侧审核状态入口。
      挂在账号设置下而不是复活 /my/content-review（该第一方页面 2026-07-21 有意删除）。
    -->
    <NuxtLink
      :to="localePath('/settings/content')"
      class="sf-home-navigation__link"
      :class="{ 'is-active': active === 'content' }"
      @click="emit('navigate')"
    >
      <span class="sf-home-navigation__link-main">
        <UIcon name="i-lucide-file-clock" class="size-[18px]" aria-hidden="true" />
        {{ t('moderation.authorStatus.nav') }}
      </span>
    </NuxtLink>

    <template v-if="props.extensionItems.length">
      <div class="sf-home-navigation__label">{{ t('profileSettings.nav.extensions') }}</div>
      <NuxtLink
        v-for="item in props.extensionItems"
        :key="item.id"
        :to="localePath(itemPath(item))"
        class="sf-home-navigation__link"
        :class="{ 'is-active': props.active === item.id }"
        @click="emit('navigate')"
      >
        <span class="sf-home-navigation__link-main">
          <UIcon :name="item.icon || 'i-lucide-puzzle'" class="size-[18px]" aria-hidden="true" />
          {{ item.label }}
        </span>
      </NuxtLink>
    </template>

    <template v-if="publicProfilePath">
      <div class="sf-home-navigation__label">{{ t('profileSettings.nav.space') }}</div>
      <NuxtLink
        :to="publicProfilePath"
        class="sf-home-navigation__link"
        @click="emit('navigate')"
      >
        <span class="sf-home-navigation__link-main">
          <UIcon name="i-lucide-external-link" class="size-[18px]" aria-hidden="true" />
          {{ t('profileSettings.viewPublicProfile') }}
        </span>
      </NuxtLink>
    </template>
  </nav>
</template>
