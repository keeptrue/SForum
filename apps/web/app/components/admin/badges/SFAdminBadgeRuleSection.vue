<script setup lang="ts">
/**
 * 徽章弹窗里的「自动授予」区（legacy 只读两栏 + 健康清单 + 规则编辑器 + 规则字段错误）。
 *
 * 从 `SFAdminBadgeEditDialog.vue` 抽出来的纯展示段：**不持有状态**，规则草稿与
 * `useBadgeRuleDraft` 仍归弹窗（草稿的 reset/validate/payload 与保存流程绑在一起，
 * 搬进来只会造出第二套生命周期）。这里只把弹窗算好的值渲染出来，并把编辑器的
 * `update:modelValue` 原样往上抛。
 */
import { BADGE_METRIC_OPTIONS } from '~/utils/admin/adminBadges'
import { badgeRuleNeedsSummary } from '~/utils/admin/badgeRule'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRule } from '~/utils/admin/badgeRule'
import type { BadgeRuleHealthIssue } from '~/composables/admin/badges/useAdminBadgeRuleHealth'
import SFAdminBadgeRuleEditor from '~/components/admin/badges/rule/SFAdminBadgeRuleEditor.vue'

const props = withDefaults(defineProps<{
  /** legacy 指标/阈值：规则接管判定后这两列只读，值仍来自弹窗的 form。 */
  metric: string
  threshold: number | null
  fieldErrors: Record<string, string[]>
  catalog: BadgeConditionCatalogEntry[]
  catalogFailed: boolean
  /** 插件事实候选（`factCandidates`）与它自己的失败标志：与目录降级互相独立。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  draftRule: BadgeRule | null
  ruleMissing: boolean
  healthIssues: BadgeRuleHealthIssue[]
  healthFailed: boolean
}>(), {
  fieldErrors: () => ({}),
  catalog: () => [],
  catalogFailed: false,
  factCandidates: () => [],
  factCandidatesFailed: false,
  draftRule: null,
  ruleMissing: false,
  healthIssues: () => [],
  healthFailed: false
})

const emit = defineEmits<{ 'update:rule': [value: BadgeRule] }>()

/**
 * 公开说明的双语字段。用 model 而不是内部 copy：弹窗持有 form，这里不许出现第二套状态。
 */
const summaryZh = defineModel<string>('requirementSummaryZh', { default: '' })
const summaryEn = defineModel<string>('requirementSummaryEn', { default: '' })

/**
 * 只在「服务端不会自动生成 + 两侧都空」时提示（判据镜像服务端谓词）。
 * 只是提醒：不进 `fieldErrors`、不禁用保存按钮。
 */
const summaryMissing = computed(() => badgeRuleNeedsSummary(props.draftRule, {
  zh: summaryZh.value ?? '',
  en: summaryEn.value ?? ''
}))

const { t, te } = useI18n()

const metricItems = computed(() => BADGE_METRIC_OPTIONS.map((metric: string) => ({
  value: metric,
  label: t(`admin.badges.metrics.${metric}`)
})))

/**
 * 引擎错误码的查找链：`errors.engine.<ref>` → `errors.<ref>` → `errors.generic`（带码）。
 * 两套词表长期并存（引擎描述「看到了什么」，草稿码描述「哪里不对」），只有同名码才复用。
 */
function engineIssueText(ref: string) {
  const key = [`admin.badges.rule.errors.engine.${ref}`, `admin.badges.rule.errors.${ref}`]
    .find(candidate => te(candidate))
  return key ? t(key) : t('admin.badges.rule.errors.generic', { code: ref })
}

/** 健康问题文案：`invalid_rule_shape` 用 `ref`（引擎错误码）查文案，后端不出文案。 */
function issueText(issue: BadgeRuleHealthIssue) {
  if (issue.kind === 'invalid_rule_shape') return engineIssueText(issue.ref)
  return `${issue.label}（${t(`admin.badges.rule.health.${issue.kind}`)}）`
}
</script>

<template>
  <div class="space-y-3" data-rule-section>
    <!-- legacy 只读：规则接管判定后这两列不再有写路径，下一刀删列时一起删。 -->
    <div class="grid gap-3 sm:grid-cols-2">
      <UFormField :label="t('admin.badges.form.metric')" :error="fieldErrors.metric?.[0]">
        <USelect :model-value="props.metric" :items="metricItems" class="w-full" disabled data-field="metric" />
      </UFormField>
      <UFormField :label="t('admin.badges.form.threshold')" :error="fieldErrors.threshold?.[0]">
        <UInput
          :model-value="props.threshold ?? 0"
          type="number"
          min="1"
          class="w-full"
          disabled
          data-field="threshold"
        />
      </UFormField>
    </div>
    <p class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.form.legacyRuleHint') }}</p>

    <!-- 健康清单只报「引用/条件失效」，不报「暂时没人达标」：后者是正常状态。 -->
    <p v-if="healthFailed" class="text-sm text-amber-600 dark:text-amber-400" data-rule-health-failed>
      {{ t('admin.badges.rule.healthUnavailable') }}
    </p>
    <ul v-else-if="healthIssues.length > 0" class="space-y-1" data-rule-health>
      <li
        v-for="issue in healthIssues"
        :key="`${issue.kind}:${issue.path}:${issue.ref}`"
        class="text-sm text-amber-600 dark:text-amber-400"
        :title="issue.path"
      >
        {{ issueText(issue) }}
      </li>
    </ul>

    <div class="min-w-0">
      <p class="mb-1.5 text-sm font-medium text-slate-700 dark:text-zinc-200">
        {{ t('admin.badges.rule.title') }}
      </p>
      <p v-if="ruleMissing" class="mb-1.5 text-sm text-amber-600 dark:text-amber-400" data-rule-missing>
        {{ t('admin.badges.rule.missingHint') }}
      </p>
      <SFAdminBadgeRuleEditor
        v-if="draftRule"
        :model-value="draftRule"
        :catalog="catalog"
        :fact-candidates="factCandidates ?? []"
        :fact-candidates-failed="factCandidatesFailed === true"
        @update:model-value="(value) => emit('update:rule', value)"
      />
      <p v-else class="text-sm text-slate-500 dark:text-zinc-400" data-rule-catalog-state>
        {{ catalogFailed ? t('admin.badges.rule.catalogUnavailable') : t('admin.badges.rule.errors.catalogMissing') }}
      </p>
      <ul v-if="fieldErrors.rule?.length" class="mt-1 space-y-0.5" data-rule-errors>
        <li v-for="message in fieldErrors.rule" :key="message" class="text-sm text-red-600 dark:text-red-400">
          {{ message }}
        </li>
      </ul>
    </div>

    <!-- 公开说明：两语言必须成对写回（服务端整体替换），所以两个字段并排。 -->
    <div class="grid gap-3 sm:grid-cols-2">
      <UFormField :label="t('admin.badges.form.requirementSummaryZh')">
        <UTextarea v-model="summaryZh" :rows="2" class="w-full" data-field="requirementSummaryZh" />
        <template #hint>
          <span class="text-xs text-slate-500 dark:text-zinc-400">
            {{ t('admin.badges.form.requirementSummaryHint') }}
          </span>
        </template>
      </UFormField>
      <UFormField :label="t('admin.badges.form.requirementSummaryEn')">
        <UTextarea v-model="summaryEn" :rows="2" class="w-full" data-field="requirementSummaryEn" />
      </UFormField>
    </div>
    <p v-if="summaryMissing" class="text-sm text-amber-600 dark:text-amber-400" data-rule-summary-hint>
      {{ t('admin.badges.form.requirementSummaryMissing') }}
    </p>
  </div>
</template>
