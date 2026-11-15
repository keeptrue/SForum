<script setup lang="ts">
export type AdminFixedTabItem = {
  id: string
  label: string
  icon: string
  disabled?: boolean
}

defineProps<{
  items: AdminFixedTabItem[]
  modelValue: string
  ariaLabel: string
  /**
   * 可选：传入时给每个页签输出稳定的 `id` 与 `aria-controls`，与页面里
   * `role="tabpanel"` 的容器形成闭环。不传时渲染结果与旧版逐字节一致
   * （现有 settings 页不受影响）。
   */
  panelId?: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()
</script>

<template>
  <div
    role="tablist"
    :aria-label="ariaLabel"
    class="relative z-0 mb-4 flex flex-wrap gap-2 border-b border-slate-200 pb-3 dark:border-zinc-800"
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
      :id="panelId ? `${panelId}-tab-${item.id}` : undefined"
      :aria-controls="panelId || undefined"
      :aria-selected="modelValue === item.id"
      @click="emit('update:modelValue', item.id)"
    >
      {{ item.label }}
    </UButton>
  </div>
</template>
