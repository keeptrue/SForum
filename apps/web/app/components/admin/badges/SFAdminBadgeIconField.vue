<script setup lang="ts">
/**
 * 徽章图标字段：15 个快捷图标 + 「更多图标」内联展开 `SFIconPicker`。
 *
 * 为什么不直接放 picker：后台编辑弹窗里最常见的动作是「从这十几个里挑一个」，
 * 让运营每次都先翻 60 个一页的图标墙是把高频路径做成了低频路径。
 * picker 自己带分页与搜索，这里**不再套第二层固定高滚动区**（套了会出现双滚动条）。
 */
import { BADGE_ICON_CHOICES } from '~/utils/admin/adminBadges'

const props = withDefaults(defineProps<{
  modelValue: string
  label: string
  moreLabel: string
  lessLabel: string
  hint?: string
  error?: string
  disabled?: boolean
}>(), {
  hint: '',
  error: '',
  disabled: false
})

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const expanded = ref(false)

function selectIcon(value: string) {
  if (props.disabled) return
  emit('update:modelValue', value)
}

function toggleExpanded() {
  expanded.value = !expanded.value
}
</script>

<template>
  <div class="min-w-0">
    <p class="mb-1.5 text-sm font-medium text-slate-700 dark:text-zinc-200">{{ props.label }}</p>

    <div class="flex flex-wrap gap-2">
      <UButton
        v-for="choice in BADGE_ICON_CHOICES"
        :key="choice"
        type="button"
        size="xs"
        :color="props.modelValue === choice ? 'primary' : 'neutral'"
        :variant="props.modelValue === choice ? 'solid' : 'outline'"
        :icon="choice"
        :aria-label="choice"
        :aria-pressed="props.modelValue === choice"
        :title="choice"
        :disabled="props.disabled"
        class="justify-center"
        @click="selectIcon(choice)"
      />
    </div>

    <UButton
      type="button"
      color="neutral"
      variant="ghost"
      size="xs"
      class="mt-2"
      :icon="expanded ? 'i-lucide-chevron-up' : 'i-lucide-chevron-down'"
      :aria-expanded="expanded"
      :disabled="props.disabled"
      @click="toggleExpanded"
    >
      {{ expanded ? props.lessLabel : props.moreLabel }}
    </UButton>

    <!-- picker 自带分页；这里只负责「展开 / 收起」，不限制高度。 -->
    <div v-if="expanded" class="mt-2 rounded-lg border border-slate-200 p-3 dark:border-zinc-800">
      <SFIconPicker
        :model-value="props.modelValue"
        :label="props.label"
        :disabled="props.disabled"
        @update:model-value="selectIcon"
      />
    </div>

    <p v-if="props.error" class="mt-1.5 text-xs text-red-600 dark:text-red-400">{{ props.error }}</p>
    <p v-else-if="props.hint" class="mt-1.5 text-xs text-slate-500 dark:text-zinc-400">{{ props.hint }}</p>
  </div>
</template>
