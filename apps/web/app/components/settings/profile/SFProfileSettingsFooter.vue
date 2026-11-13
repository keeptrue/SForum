<script setup lang="ts">
/**
 * 资料设置页脚：统一保存 / 撤销 + 提交后的错误汇总。
 *
 * 必须留在 `<form>` 内（保存按钮是 submit），窄屏时吸底。错误汇总不是一个
 * 新提示系统：它只是把「哪个分区还有几处待修正」摊开，点它或点 tab 上的错误点
 * 都会切到那个分区，错误文案本身仍挨着字段显示。
 */
defineProps<{
  dirty: boolean
  saving: boolean
  canSave: boolean
  /** 保存被禁用时的原因（插件组件分区 / 正在保存等）。 */
  saveDisabledReason?: string
  /** 提交后才发现的服务端错误：第一个待修正的分区 + 该分区错误数。 */
  errorSummary?: { label: string, count: number } | null
  /** 不属于任何分区的错误条数（后端新键 / 运营手建字段）：只说明，不跳转。 */
  globalErrorCount?: number
}>()

const emit = defineEmits<{
  reset: []
  'jump-error': []
}>()

const { t } = useI18n()

const reasonId = 'profile-settings-save-disabled'
</script>

<template>
  <footer class="sforum-settings-profile__footer">
    <div class="sforum-settings-profile__footer-copy">
      <p>{{ dirty ? t('profileSettings.unsavedFooter') : t('profileSettings.savedFooter') }}</p>
      <p v-if="saveDisabledReason" :id="reasonId" class="sforum-settings-profile__footer-hint">
        {{ saveDisabledReason }}
      </p>
      <button
        v-if="errorSummary"
        type="button"
        class="sforum-settings-profile__footer-error"
        @click="emit('jump-error')"
      >
        <UIcon name="i-lucide-circle-alert" class="size-4" aria-hidden="true" />
        {{ t('profileSettings.tabs.errorSummary', { tab: errorSummary.label, count: errorSummary.count }) }}
      </button>
      <p
        v-if="globalErrorCount"
        class="sforum-settings-profile__footer-global-error"
        role="status"
      >
        {{ t('profileSettings.tabs.errorGlobal', { count: globalErrorCount }) }}
      </p>
    </div>
    <div class="sforum-settings-profile__footer-actions">
      <SFButton
        variant="ghost"
        type="button"
        :disabled="!dirty || saving"
        @click="emit('reset')"
      >
        <template #leading>
          <UIcon name="i-lucide-rotate-ccw" />
        </template>
        {{ t('profileSettings.resetChanges') }}
      </SFButton>
      <SFButton
        variant="primary"
        type="submit"
        :disabled="!canSave"
        :loading="saving"
        :aria-describedby="saveDisabledReason ? reasonId : undefined"
        :title="saveDisabledReason || undefined"
      >
        <template #leading>
          <UIcon name="i-lucide-save" />
        </template>
        {{ saving ? t('profileSettings.saving') : t('profileSettings.save') }}
      </SFButton>
    </div>
  </footer>
</template>
