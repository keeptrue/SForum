<script setup lang="ts">
/**
 * 某一档的**字段级**保存失败提示（P2）。
 *
 * 只做展示：错误由 `bucketSaveErrors` 按 rank / tierId 归位后传进来，这里不再判断归属
 * ——「哪条错误属于哪一档」是契约层的事，组件不该有第二套判据。
 *
 * 文案纪律：优先 `reasonCode` 的 i18n（en-US 站点不出现后端中文散文），
 * 取不到才回落后端 `reason`，再否则统一兜底句；字段名同样走 i18n。
 */
import {
  saveErrorFieldLabel,
  saveErrorText,
  type TrustLevelSaveError
} from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import { TRUST_LEVEL_METRIC_SUFFIX_BY_KEY, type TrustLevelMetricKey } from '~/composables/profile/useTrustLevel'

const props = defineProps<{
  errors: TrustLevelSaveError[]
  /** 行内紧凑模式（档位列表里用），默认 false 给较宽的行用。 */
  compact?: boolean
}>()

const { t, te } = useI18n()

const METRIC_I18N_KEY_BY_SUFFIX = Object.fromEntries(
  Object.entries(TRUST_LEVEL_METRIC_SUFFIX_BY_KEY).map(([key, suffix]) => [suffix, key])
) as Record<string, TrustLevelMetricKey>

function metricLabel(suffix: string) {
  const key = METRIC_I18N_KEY_BY_SUFFIX[suffix]
  return key
    ? t(`admin.forum.trustLevels.metric.${key}`)
    : t('admin.forum.trustLevels.errors.field.requirements')
}

function fieldLabel(field: string) {
  return saveErrorFieldLabel(field, t, te, metricLabel)
}

function text(error: TrustLevelSaveError) {
  return saveErrorText(error, (key, params) => t(key, params ?? {}), te)
}
</script>

<template>
  <ul v-if="props.errors.length > 0" class="sf-field-errors" :data-compact="compact ? 'true' : 'false'" data-sf-tier-errors>
    <li v-for="(error, index) in props.errors" :key="`${error.field}-${index}`" class="sf-field-errors__item">
      <UIcon name="i-lucide-circle-alert" class="sf-field-errors__icon" aria-hidden="true" />
      <span class="min-w-0">
        <span class="sf-field-errors__field">{{ fieldLabel(error.field) }}</span>
        <span class="sf-field-errors__text">{{ text(error) }}</span>
      </span>
    </li>
  </ul>
</template>

<style scoped>
.sf-field-errors {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
  display: grid;
  gap: 4px;
}

.sf-field-errors__item {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  color: var(--sf-danger, #dc2626);
  font-size: 12px;
  line-height: 18px;
}

.sf-field-errors[data-compact="true"] .sf-field-errors__item {
  font-size: 11px;
  line-height: 16px;
}

.sf-field-errors__icon {
  flex: 0 0 auto;
  width: 13px;
  height: 13px;
  margin-top: 2px;
}

.sf-field-errors__field {
  font-weight: 600;
}

.sf-field-errors__text::before {
  content: '：';
}

/* 行内提示贴着字段出现，所以不带背景色块：再叠一层底色会盖住输入框边框。 */
</style>
