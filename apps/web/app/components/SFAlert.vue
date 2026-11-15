<script setup lang="ts">
type AlertVariant = 'primary' | 'info' | 'success' | 'warning' | 'danger'

const props = withDefaults(defineProps<{
  title?: string
  description?: string
  variant?: AlertVariant
  compact?: boolean
  closable?: boolean
}>(), {
  title: undefined,
  description: undefined,
  variant: 'primary',
  compact: false,
  closable: false
})

const emit = defineEmits<{
  close: []
}>()

/** 关闭按钮的可读名称：en-US 下不能仍是中文（RV-28）。 */
const { t } = useI18n()

const alertClass = computed(() => [
  'sf-alert',
  `sf-alert--${props.variant}`,
  props.compact ? 'sf-alert--compact' : ''
].filter(Boolean).join(' '))

const alertRole = computed(() => props.variant === 'danger' ? 'alert' : 'status')
</script>

<template>
  <div :class="alertClass" :role="alertRole">
    <div v-if="$slots.icon" class="sf-alert__icon" aria-hidden="true">
      <slot name="icon" />
    </div>
    <div class="sf-alert__content">
      <p v-if="title" class="sf-alert__title">
        {{ title }}
      </p>
      <p v-if="description" class="sf-alert__description">
        {{ description }}
      </p>
      <slot />
    </div>
    <button
      v-if="closable"
      type="button"
      class="sf-alert__close"
      :aria-label="t('common.dismissAlert')"
      :title="t('common.dismissAlert')"
      @click="emit('close')"
    >
      ×
    </button>
  </div>
</template>
