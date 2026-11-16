<script setup lang="ts">
/**
 * 后台固定页签条（Core 自有设置页的几何基准）。
 *
 * 键盘契约（WAI-ARIA tabs，自动激活模式）：
 * - `tabindex` roving：只有当前页签是 `0`，其余 `-1`，Tab 只进出一次；
 * - `←/→` 循环切换、`Home/End` 跳到首尾，激活即跟随焦点；
 * - 方向键只在焦点落在 `[role="tab"]` 上时接管，不劫持页签条里的其它控件；
 * - 禁用的页签不参与方向键序列（`disabled` 按钮本来就不可聚焦）。
 * 与后台多页签条 `SFAdminTabStrip` 共用 `nextTabIndex()`，两处行为不会漂移。
 */
import { nextTick, ref } from 'vue'
import { nextTabIndex } from '~/utils/admin/adminTabStrip'

export type AdminFixedTabItem = {
  id: string
  label: string
  icon: string
  disabled?: boolean
}

const props = defineProps<{
  items: AdminFixedTabItem[]
  modelValue: string
  ariaLabel: string
  /**
   * 可选：传入时给每个页签输出稳定的 `id` 与 `aria-controls`，与页面里
   * `role="tabpanel"` 的容器形成闭环。不传时渲染结构不变（现有 settings 页不受影响）。
   */
  panelId?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const tablist = ref<HTMLElement | null>(null)

/** 焦点搬运：按 `data-sf-fixed-tab-id` 找按钮，避免依赖组件实例 ref。 */
function focusTab(id: string) {
  const target = Array.from(tablist.value?.querySelectorAll<HTMLElement>('[role="tab"]') ?? [])
    .find(element => element.dataset.sfFixedTabId === id)
  target?.focus()
}

function onKeydown(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  if (!target || target.getAttribute('role') !== 'tab') {
    return
  }
  const enabled = props.items.filter(item => !item.disabled)
  if (!enabled.length) {
    return
  }
  const current = enabled.findIndex(item => item.id === props.modelValue)
  const next = nextTabIndex(current, enabled.length, event.key)
  if (next === null) {
    return
  }
  event.preventDefault()
  const nextItem = enabled[next]
  if (!nextItem) {
    return
  }
  emit('update:modelValue', nextItem.id)
  // 激活后 tabindex 换位，焦点必须一起搬过去，否则下一次 Tab 会从旧位置继续。
  void nextTick(() => focusTab(nextItem.id))
}
</script>

<template>
  <div
    ref="tablist"
    role="tablist"
    :id="panelId ? `${panelId}-tablist` : undefined"
    :aria-label="ariaLabel"
    class="relative z-0 mb-4 flex flex-wrap gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
    @keydown="onKeydown"
  >
    <UButton
      v-for="item in items"
      :key="item.id"
      type="button"
      role="tab"
      :icon="item.icon"
      :color="modelValue === item.id ? 'primary' : 'neutral'"
      :variant="modelValue === item.id ? 'solid' : 'ghost'"
      :disabled="item.disabled"
      :data-sf-fixed-tab-id="item.id"
      :id="panelId ? `${panelId}-tab-${item.id}` : undefined"
      :aria-controls="panelId || undefined"
      :aria-selected="modelValue === item.id"
      :tabindex="modelValue === item.id ? 0 : -1"
      @click="emit('update:modelValue', item.id)"
    >
      {{ item.label }}
    </UButton>
  </div>
</template>
