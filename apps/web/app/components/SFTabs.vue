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

function select(item: TabItem) {
  if (!item.disabled) {
    emit('update:modelValue', item.value)
  }
}
</script>

<template>
  <div class="sf-tabs" role="tablist" :aria-label="ariaLabel || t('common.sectionTabs')">
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
