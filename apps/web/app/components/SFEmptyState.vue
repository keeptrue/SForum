<script setup lang="ts">
const props = withDefaults(defineProps<{
  title: string
  description?: string
  actionLabel?: string
  iconLabel?: string
  /** 默认图标可由调用方替换；`iconLabel` 存在时仍然优先渲染文字标记。 */
  icon?: string
}>(), {
  description: undefined,
  actionLabel: undefined,
  iconLabel: undefined,
  icon: 'i-lucide-inbox'
})

const emit = defineEmits<{
  action: []
}>()
</script>

<template>
  <div class="sf-empty-state">
    <div class="sf-empty-state__icon" aria-hidden="true">
      <span v-if="iconLabel">{{ iconLabel }}</span>
      <UIcon v-else :name="props.icon" class="size-5" />
    </div>
    <h3 class="sf-empty-state__title">
      {{ title }}
    </h3>
    <p v-if="description" class="sf-empty-state__description">
      {{ description }}
    </p>
    <SFButton v-if="actionLabel" size="sm" @click="emit('action')">
      {{ actionLabel }}
    </SFButton>
  </div>
</template>
