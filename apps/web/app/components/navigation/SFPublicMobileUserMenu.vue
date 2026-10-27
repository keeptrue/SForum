<script setup lang="ts">
import type { PublicUserMenuEntry } from '~/composables/navigation/usePublicUserMenu'
import { usePublicUserMenu } from '~/composables/navigation/usePublicUserMenu'

const { t } = useI18n()
const { user, displayName, menuGroups } = usePublicUserMenu()
const mobileInfoOpen = useState<boolean>('forum-mobile-info-open', () => false)
// 抽屉内本组件就是首屏唯一的操作区：默认折叠会呈现一块空面板，
// 所以默认展开，身份行仍可点击折叠（组件随抽屉 v-if 挂载，每次打开回到展开态）。
const menuOpen = ref(true)

function closeDrawer() {
  mobileInfoOpen.value = false
}

function selectAction(entry: PublicUserMenuEntry) {
  if (!entry.keepOpen) closeDrawer()
  entry.onSelect?.()
}
</script>

<template>
  <section v-if="user" class="sf-public-mobile-user-menu" :aria-label="t('nav.personalCenter')">
    <button
      type="button"
      class="sf-public-mobile-user-menu__identity"
      :aria-expanded="menuOpen"
      aria-controls="sf-public-mobile-user-menu-actions"
      @click="menuOpen = !menuOpen"
    >
      <SFAvatar :name="displayName" :avatar="user.avatar" size="md" shape="circle" />
      <span class="sf-public-mobile-user-menu__identity-copy">
        <strong>{{ displayName }}</strong>
        <span>@{{ user.username }}</span>
      </span>
      <UIcon
        :name="menuOpen ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
        class="sf-public-mobile-user-menu__toggle-icon"
        aria-hidden="true"
      />
    </button>

    <div v-if="menuOpen" id="sf-public-mobile-user-menu-actions">
      <div
        v-for="(group, groupIndex) in menuGroups"
        :key="groupIndex"
        class="sf-public-mobile-user-menu__group"
      >
        <template v-for="entry in group" :key="entry.key">
          <NuxtLink
            v-if="entry.to"
            :to="entry.to"
            class="sf-public-mobile-user-menu__item"
            @click="closeDrawer"
          >
            <UIcon :name="entry.icon" class="size-[18px]" aria-hidden="true" />
            <span>
              <strong>{{ entry.label }}</strong>
              <small v-if="entry.description">{{ entry.description }}</small>
            </span>
          </NuxtLink>
          <button
            v-else
            type="button"
            class="sf-public-mobile-user-menu__item"
            :class="{ 'sf-public-mobile-user-menu__item--danger': entry.tone === 'danger' }"
            :disabled="entry.disabled"
            @click="selectAction(entry)"
          >
            <UIcon :name="entry.icon" class="size-[18px]" aria-hidden="true" />
            <span>
              <strong>{{ entry.label }}</strong>
              <small v-if="entry.description">{{ entry.description }}</small>
            </span>
          </button>
        </template>
      </div>
    </div>
  </section>
</template>

<style scoped>
.sf-public-mobile-user-menu {
  margin: 0 0 16px;
  padding: 0 0 14px;
  border-bottom: 1px solid var(--sf-public-border);
}

.sf-public-mobile-user-menu__identity {
  width: 100%;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
  border: 0;
  border-radius: 6px;
  padding: 6px 8px 12px;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
}

.sf-public-mobile-user-menu__identity:hover {
  background: var(--sf-public-surface-muted);
}

.sf-public-mobile-user-menu__identity-copy {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.sf-public-mobile-user-menu__identity-copy strong,
.sf-public-mobile-user-menu__identity-copy span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-public-mobile-user-menu__identity-copy strong {
  color: var(--sf-public-text);
  font-size: 14px;
  font-weight: 700;
}

.sf-public-mobile-user-menu__identity-copy span {
  color: var(--sf-public-text-muted);
  font-size: 12px;
}

.sf-public-mobile-user-menu__toggle-icon {
  width: 18px;
  height: 18px;
  flex: 0 0 auto;
  margin-left: auto;
  color: var(--sf-public-text-muted);
}

.sf-public-mobile-user-menu__group {
  display: grid;
  gap: 2px;
  padding: 5px 0;
  border-top: 1px solid var(--sf-public-border);
}

.sf-public-mobile-user-menu__item {
  width: 100%;
  min-height: 42px;
  display: flex;
  align-items: center;
  gap: 10px;
  border: 0;
  border-radius: 6px;
  padding: 7px 8px;
  background: transparent;
  color: var(--sf-public-text-secondary);
  text-align: left;
  text-decoration: none;
  cursor: pointer;
}

.sf-public-mobile-user-menu__item:hover {
  background: var(--sf-public-surface-muted);
  color: var(--sf-public-text);
}

.sf-public-mobile-user-menu__item:disabled {
  cursor: wait;
  opacity: .58;
}

.sf-public-mobile-user-menu__item > span {
  min-width: 0;
  display: grid;
  gap: 2px;
}

.sf-public-mobile-user-menu__item strong {
  font-size: 13px;
  font-weight: 650;
}

.sf-public-mobile-user-menu__item small {
  color: var(--sf-public-text-muted);
  font-size: 11px;
  line-height: 1.4;
}

.sf-public-mobile-user-menu__item--danger {
  color: var(--ui-color-error-600, #dc2626);
}

.dark .sf-public-mobile-user-menu__item--danger {
  color: var(--ui-color-error-400, #f87171);
}
</style>
