<script setup lang="ts">
import type { ConfirmDialogRequest } from '~/composables/forum/useConfirmDialog'

/**
 * 站内通用确认弹窗：替代浏览器原生 window.confirm。
 *
 * - 只负责渲染与回传结果，状态由 `useConfirmDialog` 持有（request 来自父级）。
 * - ESC / 点遮罩 / 右上角关闭按钮统一按「取消」结算（settle(false)）。
 * - 底部顺序固定：取消（neutral/ghost）在前，确认（error）在后；组件不给任何元素设置
 *   自动聚焦，确认按钮又排在取消之后，因此破坏性按钮不会成为默认焦点。
 */
const props = defineProps<{
  request: ConfirmDialogRequest
}>()

const emit = defineEmits<{
  settle: [value: boolean]
}>()

function handleOpenUpdate(open: boolean) {
  if (!open) {
    emit('settle', false)
  }
}
</script>

<template>
  <UModal
    :open="props.request.open"
    :title="props.request.title"
    :description="props.request.description"
    :ui="{ content: 'sm:max-w-md' }"
    @update:open="handleOpenUpdate"
  >
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton
          color="neutral"
          variant="ghost"
          @click="emit('settle', false)"
        >
          {{ props.request.cancelLabel }}
        </UButton>
        <UButton
          color="error"
          variant="solid"
          @click="emit('settle', true)"
        >
          {{ props.request.confirmLabel }}
        </UButton>
      </div>
    </template>
  </UModal>
</template>
