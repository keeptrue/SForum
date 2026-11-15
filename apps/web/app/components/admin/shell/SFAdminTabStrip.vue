<script setup lang="ts">
/**
 * 后台多页签条（唯一的页签呈现所有者）。
 *
 * ARIA 结构约束：`role="tablist"` 的直接子元素只能是 `role="tab"`。
 * 所以页签内的 X 是**装饰元素**（`span` + `aria-hidden`，无 tabindex、不可聚焦），
 * 只承担鼠标路径；键盘路径是 tablist 之外那个可聚焦的「关闭当前页签」按钮。
 * 两者并存：鼠标点哪个 X 关哪个页签，键盘用外置按钮关当前页签。
 *
 * 焦点契约：←/→/Home/End 激活后、关闭页签后，焦点都跟到页签按钮上；
 * 否则按钮离开 DOM 后 activeElement 落回 body，键盘用户要从头 Tab 过侧栏。
 */
import { computed, nextTick, ref } from 'vue'
import { adminTabElementId, nextTabIndex } from '~/utils/admin/adminTabStrip'

export interface AdminTabStripItem {
  id: string
  /** 已解析的显示文案（父级负责 i18n）。 */
  label: string
  icon: string
  closable: boolean
}

const props = defineProps<{
  tabs: AdminTabStripItem[]
  activeId: string
  /** 页签条的无障碍名称。 */
  label: string
  /** 「关闭当前页签」按钮的无障碍名称。 */
  closeLabel: string
}>()

const emit = defineEmits<{
  navigate: [id: string]
  close: [id: string]
}>()

const tablist = ref<HTMLElement | null>(null)

const activeTab = computed(() => props.tabs.find(tab => tab.id === props.activeId))
const canCloseActive = computed(() => Boolean(activeTab.value?.closable))

function focusTab(id: string) {
  // 用 dataset 比对而不是 CSS.escape：页签 id 是路由路径（含 `/`），
  // 拼选择器既要在浏览器里转义，也依赖 CSS.escape 在测试环境里可用。
  const target = Array.from(tablist.value?.querySelectorAll<HTMLElement>('[role="tab"]') ?? [])
    .find(element => element.dataset.adminTabId === id)
  target?.focus()
}

function activate(id: string, options: { focus?: boolean } = {}) {
  emit('navigate', id)
  if (options.focus) {
    // 激活后 tabindex 换位，焦点必须一起搬过去。
    void nextTick(() => focusTab(id))
  }
}

function onKeydown(event: KeyboardEvent) {
  // 只有焦点在页签本体上才接管方向键：焦点在关闭控件或别处时不能劫持。
  const target = event.target as HTMLElement | null
  if (!target || target.getAttribute('role') !== 'tab') {
    return
  }

  const next = nextTabIndex(
    props.tabs.findIndex(tab => tab.id === props.activeId),
    props.tabs.length,
    event.key
  )
  if (next === null) {
    return
  }

  event.preventDefault()
  const tab = props.tabs[next]
  if (tab) {
    activate(tab.id, { focus: true })
  }
}

function requestClose(id: string) {
  const tab = props.tabs.find(item => item.id === id)
  if (!tab?.closable) {
    return
  }

  emit('close', id)
  // 关闭后页签从 DOM 消失：焦点跟到（可能已变成激活项的）页签上，不回 body。
  void nextTick(() => focusTab(props.activeId))
}
</script>

<template>
  <div class="sforum-admin-tabs flex min-w-0 shrink-0 items-end h-[52px] px-3 gap-1.5 bg-[var(--bg-admin-card)] border-b border-[var(--border-admin)] z-15">
    <div
      ref="tablist"
      role="tablist"
      :aria-label="label"
      class="flex min-w-0 flex-1 items-end gap-1.5 overflow-x-auto overflow-y-hidden select-none no-scrollbar"
      @keydown="onKeydown"
    >
      <button
        v-for="tab in tabs"
        :id="adminTabElementId(tab.id)"
        :key="tab.id"
        type="button"
        role="tab"
        :data-admin-tab-id="tab.id"
        :aria-selected="activeId === tab.id"
        aria-controls="sforum-admin-main"
        :tabindex="activeId === tab.id ? 0 : -1"
        class="group relative z-10 mb-[-1px] inline-flex h-[44px] max-w-[12.5rem] shrink-0 cursor-pointer items-center gap-2 whitespace-nowrap rounded-t-lg border border-b-0 px-3 text-sm font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--sf-accent)] sm:px-4"
        :class="activeId === tab.id
          ? 'bg-[var(--bg-admin-app)] text-[var(--sf-accent)] border-[var(--border-admin)]'
          : 'bg-transparent text-slate-500 dark:text-zinc-400 border-transparent hover:text-[var(--text-admin-main)]'"
        @click="activate(tab.id)"
      >
        <UIcon :name="tab.icon" class="size-4.5 shrink-0" />
        <span class="min-w-0 truncate">{{ tab.label }}</span>

        <!--
          鼠标路径的逐页签关闭：装饰元素，不进 Tab 序列、不进无障碍树。
          `aria-hidden` + 无 tabindex 是刻意的 —— 加 tabindex="-1" 会让它成为
          可编程聚焦元素，axe 的 aria-hidden-focus 会把它算成违规。
          `.stop` 保证点 X 不触发页签导航。
        -->
        <span
          v-if="tab.closable"
          aria-hidden="true"
          class="inline-flex size-4.5 shrink-0 items-center justify-center rounded-full text-slate-500 transition-colors hover:bg-red-500/20 hover:text-red-500 dark:text-zinc-400"
          @click.stop="requestClose(tab.id)"
        >
          <UIcon name="i-lucide-x" class="size-3" />
        </span>
      </button>
    </div>

    <button
      v-if="canCloseActive"
      type="button"
      class="mb-2 inline-flex size-7 shrink-0 cursor-pointer items-center justify-center rounded-full bg-transparent text-slate-500 transition-colors hover:bg-red-500/20 hover:text-red-500 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--sf-accent)] dark:text-zinc-400"
      :aria-label="closeLabel"
      @click="requestClose(activeId)"
    >
      <UIcon name="i-lucide-x" class="size-4" />
    </button>
  </div>
</template>
