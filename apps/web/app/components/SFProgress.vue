<script setup lang="ts">
type ProgressVariant = 'primary' | 'info' | 'success' | 'warning' | 'danger'

const props = withDefaults(defineProps<{
  value?: number
  max?: number
  label?: string
  variant?: ProgressVariant
  showValue?: boolean
}>(), {
  value: 0,
  max: 100,
  label: undefined,
  variant: 'primary',
  showValue: false
})

const percent = computed(() => {
  if (props.max <= 0) {
    return 0
  }
  return Math.min(100, Math.max(0, (props.value / props.max) * 100))
})

const progressClass = computed(() => [
  'sf-progress',
  `sf-progress--${props.variant}`
].join(' '))
</script>

<template>
  <div :class="progressClass">
    <div v-if="label || showValue" class="sf-progress__header">
      <span>{{ label }}</span>
      <span v-if="showValue">{{ Math.round(percent) }}%</span>
    </div>
    <div
      class="sf-progress__track"
      role="progressbar"
      :aria-label="label || undefined"
      :aria-valuenow="value"
      aria-valuemin="0"
      :aria-valuemax="max"
    >
      <div class="sf-progress__bar" :style="{ width: `${percent}%` }" />
    </div>
  </div>
</template>
