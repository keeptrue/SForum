<script setup lang="ts">
type TabItem = {
  label: string
  value: string
  badge?: string | number
  disabled?: boolean
}

const props = withDefaults(defineProps<{
  items: TabItem[]
  modelValue?: string
  ariaLabel?: string
}>(), {
  modelValue: undefined,
  ariaLabel: undefined
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const activeValue = computed(() => props.modelValue || props.items[0]?.value || '')

const { t } = useI18n()

/**
 * 页签条的可访问名：`undefined` 与**空串/纯空白**都回落到本地化默认文案。
 *
 * 空串不等于「不要名字」—— 一个没有可访问名的 tablist 对读屏只是噪声；
 * 想表达「没有页签」应当不渲染组件，而不是传空串。这条约定与 `SFAvatar.alt`
 * 相反（那里 `alt=""` 有明确的装饰语义），因此单独写测试钉住。
 */
const resolvedAriaLabel = computed(() => props.ariaLabel?.trim() || t('common.sectionTabs'))

function select(item: TabItem) {
  if (!item.disabled) {
    emit('update:modelValue', item.value)
  }
}
</script>

<template>
  <div class="sf-tabs" role="tablist" :aria-label="resolvedAriaLabel">
    <button
      v-for="item in items"
      :key="item.value"
      type="button"
      class="sf-tabs__item"
      role="tab"
      :disabled="item.disabled"
      :aria-selected="activeValue === item.value ? 'true' : 'false'"
      @click="select(item)"
    >
      <span>{{ item.label }}</span>
      <SFBadge v-if="item.badge" variant="neutral">
        {{ item.badge }}
      </SFBadge>
    </button>
  </div>
</template>
