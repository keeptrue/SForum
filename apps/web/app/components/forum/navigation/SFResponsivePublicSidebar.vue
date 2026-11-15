<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, useAttrs, useId, watch } from 'vue'
import { usePublicSidebarDrawer } from '~/composables/navigation/usePublicSidebarDrawer'
import { createFocusTrap } from '~/utils/a11y/focusTrap'

defineOptions({ inheritAttrs: false })

const props = defineProps<{
  ownerId: string
  title: string
}>()

const attrs = useAttrs()
const { t } = useI18n()
const ownerToken = useId()
const { open, claimOwner, closeDrawer, releaseOwner } = usePublicSidebarDrawer()

claimOwner(props.ownerId, ownerToken)

watch(() => props.ownerId, (nextOwnerId) => {
  claimOwner(nextOwnerId, ownerToken)
})

const surfaceElement = ref<HTMLElement | null>(null)

/**
 * 移动端抽屉的键盘契约（RV-24）：抽屉打开时对读屏是 dialog（role/aria-modal/名称），
 * 初始焦点落在抽屉本身，Tab 在抽屉内循环，Esc 关闭，关闭后焦点回到汉堡按钮。
 * 关闭态下这块就是桌面端普通侧栏，不能带 dialog 语义，所以语义全部跟着 `open` 走。
 */
const focusTrap = createFocusTrap({
  container: () => surfaceElement.value,
  onEscape: () => closeDrawer(),
  resolveInitialFocus: container => container
})

watch(open, async (isOpen) => {
  if (!isOpen) {
    focusTrap.deactivate()
    return
  }
  await nextTick()
  focusTrap.activate()
})

onBeforeUnmount(() => {
  focusTrap.deactivate({ restoreFocus: false })
  releaseOwner(ownerToken)
})

function handleContentClick(event: MouseEvent) {
  if (!open.value) return
  const target = event.target
  if (target instanceof Element && target.closest('a')) {
    closeDrawer()
  }
}
</script>

<template>
  <div
    v-bind="attrs"
    class="sf-responsive-public-sidebar"
    :class="{ 'is-open': open }"
    :data-sidebar-owner="ownerId"
  >
    <button
      v-if="open"
      type="button"
      class="sforum-mobile-drawer__backdrop sf-responsive-public-sidebar__backdrop"
      :aria-label="t('common.close')"
      @click="closeDrawer"
    />
    <div
      ref="surfaceElement"
      class="sf-responsive-public-sidebar__surface"
      :class="{ 'sforum-mobile-drawer sforum-mobile-drawer--left': open }"
      :role="open ? 'dialog' : undefined"
      :aria-modal="open ? 'true' : undefined"
      :aria-label="open ? title : undefined"
      :tabindex="open ? -1 : undefined"
      data-navigation-location="public.sidebar.primary"
      :data-navigation-viewport="open ? 'mobile' : 'desktop'"
      @click="handleContentClick"
    >
      <header class="sforum-mobile-drawer__head sf-responsive-public-sidebar__head">
        <strong>{{ title }}</strong>
        <button type="button" :aria-label="t('common.close')" @click="closeDrawer">
          <UIcon name="i-lucide-x" class="size-5" aria-hidden="true" />
        </button>
      </header>
      <slot />
    </div>
  </div>
</template>

<style scoped>
.sf-responsive-public-sidebar__surface {
  min-height: 100%;
}

.sf-responsive-public-sidebar__head {
  display: none;
}

@media (max-width: 980px) {
  :global(.sforum-home__sidebar:has(> .sf-responsive-public-sidebar)) {
    display: contents !important;
  }

  .sf-responsive-public-sidebar {
    display: contents !important;
  }

  .sf-responsive-public-sidebar__surface {
    display: none;
  }

  .sf-responsive-public-sidebar__surface.sforum-mobile-drawer {
    display: block;
  }

  .sf-responsive-public-sidebar__head {
    display: flex;
  }

  .sf-responsive-public-sidebar__backdrop {
    z-index: 90;
  }

  .sf-responsive-public-sidebar__surface {
    z-index: 91;
  }
}

@media (min-width: 981px) {
  .sf-responsive-public-sidebar__surface.sforum-mobile-drawer {
    position: static;
    display: block;
    width: auto;
    overflow: visible;
    padding: 0;
    border: 0;
    background: transparent;
    box-shadow: none;
  }
}
</style>
