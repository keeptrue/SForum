<script setup lang="ts">
import type { AdminExtension } from '~/utils/admin/adminExtensions'
import { extensionUpdateBadge } from '~/utils/admin/extensions/updatePresentation'

defineOptions({ name: 'SFAdminExtensionUpdateState' })

const props = defineProps<{ extension: AdminExtension }>()

const { t } = useI18n()

// 无更新、制品缺失或版本非法时组件渲染空：调用方不需要再判断一次。
const badge = computed(() => extensionUpdateBadge(props.extension))
</script>

<template>
  <UBadge
    v-if="badge"
    :color="badge.color"
    :variant="badge.variant"
    :icon="badge.icon"
    :title="t(badge.titleKey, badge.titleParams)"
  >
    {{ t(badge.labelKey, badge.labelParams) }}
  </UBadge>
</template>
