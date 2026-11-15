<script setup lang="ts">
type ToastVariant = 'primary' | 'success' | 'warning' | 'danger'

const props = withDefaults(defineProps<{
  title?: string
  description?: string
  variant?: ToastVariant
  actionLabel?: string
  closable?: boolean
}>(), {
  title: undefined,
  description: undefined,
  variant: 'primary',
  actionLabel: undefined,
  closable: false
})

const emit = defineEmits<{
  action: []
  close: []
}>()

const { t } = useI18n()

const toastClass = computed(() => [
  'sf-toast',
  `sf-toast--${props.variant}`
].join(' '))
</script>

<template>
  <div :class="toastClass" role="status">
    <div class="sf-toast__content">
      <p v-if="title" class="sf-toast__title">
        {{ title }}
      </p>
      <p v-if="description" class="sf-toast__description">
        {{ description }}
      </p>
      <slot />
    </div>
    <button
      v-if="actionLabel"
      type="button"
      class="sf-toast__action"
      @click="emit('action')"
    >
      {{ actionLabel }}
    </button>
    <button
      v-if="closable"
      type="button"
      class="sf-toast__close"
      :aria-label="t('common.dismissNotification')"
      :title="t('common.dismissNotification')"
      @click="emit('close')"
    >
      ×
    </button>
  </div>
</template>
