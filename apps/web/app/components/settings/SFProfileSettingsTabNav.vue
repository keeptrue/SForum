<script setup lang="ts">
import SFSettingsTabNav, { type SettingsTabItem } from '~/components/settings/SFSettingsTabNav.vue'
import { PROFILE_SETTINGS_ID_PREFIX, type ProfileSettingsTabView } from '~/utils/settings/profileSettings'
/**
 * 资料设置页的 tab 栏：核心三区（身份 / 介绍 / 地点与链接）+ 插件分区。
 *
 * 只把「分区清单 + 错误计数」翻译成 tab 条目，呈现与键盘语义仍在 SFSettingsTabNav。
 * 错误点是提交后的状态提示，不是徽标：不显示计数、不打断输入，点 tab 才切过去。
 */
const props = defineProps<{
  tabs: ProfileSettingsTabView[]
  modelValue: string
  errorCounts: Record<string, number>
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()

const items = computed<SettingsTabItem[]>(() => props.tabs.map(tab => ({
  id: tab.id,
  label: tab.label,
  error: (props.errorCounts[tab.id] || 0) > 0,
  // 插件分区名可能很长：窄屏按 max-width 截断，title 留完整文案。
  title: tab.source === 'extension' ? tab.label : undefined
})))

const ariaLabel = computed(() => t('profileSettings.tabs.ariaLabel'))
</script>

<template>
  <SFSettingsTabNav
    :items="items"
    :label="ariaLabel"
    :id-prefix="PROFILE_SETTINGS_ID_PREFIX"
    :model-value="modelValue"
    :error-label="t('profileSettings.tabs.errorDot')"
    @update:model-value="emit('update:modelValue', $event)"
  />
</template>
