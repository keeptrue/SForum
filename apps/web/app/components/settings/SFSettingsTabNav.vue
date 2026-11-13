<script setup lang="ts">
/**
 * 账号设置类页面的共用 tab 栏（唯一的 tab 呈现所有者）。
 *
 * 两种激活模型共用同一套几何与样式（`.sforum-settings__tabs` / `.sforum-settings__tab`）：
 * - 路由模型（账号安全页）：item 带 `to`，渲染 NuxtLink + `aria-current="page"`；
 * - 面板模型（资料设置页）：item 不带 `to`，渲染 `role=tab`/`role=tablist`，
 *   支持 roving tabindex、←/→ 循环、Home/End，panel 用 `aria-controls` 关联。
 *
 * 不新造 tab 样式、不挂动态计数徽标；错误点只在提交后出现（`item.error`），
 * 且对读屏用户是 `aria-describedby` 指向的隐藏文案，不是图标本身。
 */
export type SettingsTabItem = {
  id: string
  label: string
  /** 路由驱动 tab：给出后用链接渲染，点击交给路由。 */
  to?: string
  /** 提交后该分区有字段错误（面板模型）。 */
  error?: boolean
  /** 插件命名可能很长：给完整文案做 title，样式侧按 max-width 截断。 */
  title?: string
}

const props = withDefaults(defineProps<{
  items: SettingsTabItem[]
  /** tab 栏的无障碍名称（渲染成 aria-label；aria-* 是保留属性，不能当 prop 名）。 */
  label: string
  /** 面板模型必填：tab id 前缀，用于 `${prefix}-tab-${id}` / `${prefix}-panel-${id}`。 */
  idPrefix?: string
  /** 面板模型的选中项。 */
  modelValue?: string
  /** 错误点的读屏文案（「（有错误）」）。 */
  errorLabel?: string
}>(), {
  idPrefix: 'settings',
  modelValue: '',
  errorLabel: ''
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

// 路由模型下没有任何 to 之外的状态；`route` 由调用方传入 to 决定高亮。
const linkMode = computed(() => props.items.some(item => Boolean(item.to)))
const tablist = ref<HTMLElement | null>(null)

function tabElementId(id: string) {
  return `${props.idPrefix}-tab-${id}`
}

function panelElementId(id: string) {
  return `${props.idPrefix}-panel-${id}`
}

function isActive(id: string) {
  return props.modelValue === id
}

function errorId(id: string) {
  return `${props.idPrefix}-tab-${id}-error`
}

function focusTab(id: string) {
  const container = tablist.value
  if (!container) return
  const target = Array.from(container.querySelectorAll<HTMLElement>('[role="tab"]'))
    .find(element => element.id === tabElementId(id))
  target?.focus()
}

function selectTab(id: string, options: { focus?: boolean } = {}) {
  if (!isActive(id)) {
    emit('update:modelValue', id)
  }
  if (options.focus) {
    focusTab(id)
  }
}

function onKeydown(event: KeyboardEvent) {
  // 只有面板模型接管键盘；链接模式的 tab 栏交给浏览器默认行为。
  if (linkMode.value) return
  const ids = props.items.map(item => item.id)
  if (ids.length === 0) return
  const current = Math.max(ids.indexOf(props.modelValue), 0)
  let next = current
  switch (event.key) {
    case 'ArrowRight':
      next = (current + 1) % ids.length
      break
    case 'ArrowLeft':
      next = (current - 1 + ids.length) % ids.length
      break
    case 'Home':
      next = 0
      break
    case 'End':
      next = ids.length - 1
      break
    default:
      return
  }
  event.preventDefault()
  selectTab(ids[next] as string, { focus: true })
}

// 切 tab 后让新选中项滚进可视区：窄屏横向滚动条上，选中的一定要露出来。
watch(() => props.modelValue, async (value) => {
  if (linkMode.value) return
  await nextTick()
  tablist.value
    ?.querySelector<HTMLElement>(`#${CSS.escape(tabElementId(value))}`)
    ?.scrollIntoView({ inline: 'nearest', block: 'nearest' })
})
</script>

<template>
  <nav
    v-if="linkMode"
    class="sforum-settings__tabs"
    :aria-label="label"
  >
    <NuxtLink
      v-for="tab in items"
      :key="tab.id"
      :to="tab.to"
      class="sforum-settings__tab"
      :class="{ 'is-active': isActive(tab.id) }"
      :aria-current="isActive(tab.id) ? 'page' : undefined"
    >
      <span class="sforum-settings__tab-label">{{ tab.label }}</span>
    </NuxtLink>
  </nav>

  <div
    v-else
    ref="tablist"
    class="sforum-settings__tabs"
    role="tablist"
    :aria-label="label"
    @keydown="onKeydown"
  >
    <button
      v-for="tab in items"
      :id="tabElementId(tab.id)"
      :key="tab.id"
      type="button"
      role="tab"
      class="sforum-settings__tab"
      :class="{ 'is-active': isActive(tab.id) }"
      :aria-selected="isActive(tab.id)"
      :aria-controls="panelElementId(tab.id)"
      :aria-describedby="tab.error ? errorId(tab.id) : undefined"
      :tabindex="isActive(tab.id) ? 0 : -1"
      :title="tab.title"
      @click="selectTab(tab.id)"
    >
      <span class="sforum-settings__tab-label">{{ tab.label }}</span>
      <span
        v-if="tab.error"
        class="sforum-settings__tab-error"
        aria-hidden="true"
      />
      <span
        v-if="tab.error"
        :id="errorId(tab.id)"
        class="sr-only"
      >{{ errorLabel }}</span>
    </button>
  </div>
</template>
