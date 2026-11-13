<script setup lang="ts">
import SFDeclarativeSettingsField from '~/components/settings/shared/SFDeclarativeSettingsField.vue'
import SFExtensionWidget from '~/components/SFExtensionWidget.vue'
import type { ProfileSettingsDraftValue, ProfileSettingsTabView } from '~/utils/settings/profileSettings'
/**
 * 资料设置 · 插件分区面板。两种形态共用同一个 panel：
 * - `fields`：服务端把插件字段当成声明式字段下发，用中性渲染器画，随页脚统一保存；
 * - `component`：扩展自带 L2 组件，原地挂载；保存权归插件自己（页脚保存按钮禁用）。
 * 插件分区没有“空分区”形态：缺字段/缺组件 id 的分区在 tab 合成阶段就被丢弃。
 */
const props = defineProps<{
  section: ProfileSettingsTabView
  values: Record<string, ProfileSettingsDraftValue>
  fieldErrors: Record<string, string[]>
  busy: boolean
}>()

const emit = defineEmits<{
  'update:value': [fieldKey: string, value: ProfileSettingsDraftValue]
  change: [fieldKey: string]
}>()

const { t } = useI18n()

const isComponent = computed(() => props.section.type === 'component')

function valueOf(fieldKey: string): ProfileSettingsDraftValue {
  return props.values[fieldKey] ?? ''
}
</script>

<template>
  <div class="sforum-settings-profile__panel-body">
    <p v-if="section.description" class="sforum-settings-profile__panel-description">
      {{ section.description }}
    </p>

    <template v-if="isComponent">
      <SFAlert
        variant="info"
        compact
        :title="t('profileSettings.extension.componentNotice')"
        class="sforum-settings-profile__extension-notice"
      />
      <SFExtensionWidget
        :extension-id="section.extensionId || ''"
        :component-id="section.componentId || ''"
        :component-props="{ scope: 'profile.settings', sectionId: section.id }"
      >
        <p class="sforum-settings-profile__extension-placeholder">
          {{ t('profileSettings.extension.componentLoading') }}
        </p>
      </SFExtensionWidget>
    </template>

    <template v-else>
      <SFDeclarativeSettingsField
        v-for="field in section.fields"
        :key="field.fieldKey"
        :field="field"
        :model-value="valueOf(field.fieldKey)"
        :errors="fieldErrors[field.fieldKey] || []"
        :required-label="t('profileSettings.extension.required')"
        :disabled="busy"
        class="sforum-settings-profile__field"
        @update:model-value="emit('update:value', field.fieldKey, $event)"
        @change="emit('change', field.fieldKey)"
      />
    </template>
  </div>
</template>
