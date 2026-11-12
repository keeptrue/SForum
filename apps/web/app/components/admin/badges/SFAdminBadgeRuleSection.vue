<script setup lang="ts">
/**
 * 徽章弹窗里的「自动授予」区（健康清单 + 规则编辑器 + 规则字段错误 + 公开说明预览）。
 *
 * 从 `SFAdminBadgeEditDialog.vue` 抽出来的纯展示段：**不持有状态**，规则草稿与
 * `useBadgeRuleDraft`、公开说明预览（`useBadgeRuleTextPreview`）都归弹窗——草稿的
 * reset/validate/payload 与保存流程绑在一起，搬进来只会造出第二套生命周期。
 * 这里只把弹窗算好的值渲染出来，并把编辑器的 `update:modelValue` 原样往上抛。
 */
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRule } from '~/utils/admin/badgeRule'
import type { BadgeRuleText, BadgeRuleTextStatus } from '~/composables/admin/badges/useBadgeRuleTextPreview'
import type { BadgeRuleHealthIssue } from '~/composables/admin/badges/useAdminBadgeRuleHealth'
import SFAdminBadgeRuleEditor from '~/components/admin/badges/rule/SFAdminBadgeRuleEditor.vue'

const props = withDefaults(defineProps<{
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
  /** 服务端生成的公开说明（草稿预览 / 保存回值），**只读**展示。 */
  requirementText?: BadgeRuleText | null
  requirementTextStatus?: BadgeRuleTextStatus
}>(), {
  fieldErrors: () => ({}),
  catalog: () => [],
  catalogFailed: false,
  factCandidates: () => [],
  factCandidatesFailed: false,
  draftRule: null,
  ruleMissing: false,
  healthIssues: () => [],
  healthFailed: false,
  requirementText: null,
  requirementTextStatus: 'idle'
})

const emit = defineEmits<{ 'update:rule': [value: BadgeRule] }>()

const { t, te } = useI18n()

/**
 * 公开说明是**派生数据**：句子由服务端按规则生成，后台只预览、不许手改。
 * 三种非就绪态都不进 `fieldErrors`——预览失败不阻断保存。
 */
const previewEmpty = computed(() => !props.draftRule)
const previewLoading = computed(() => !props.requirementText && props.requirementTextStatus === 'pending')
const previewFailed = computed(() => props.requirementTextStatus === 'error')
const previewZh = computed(() => props.requirementText?.zh ?? '')
const previewEn = computed(() => props.requirementText?.en ?? '')

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
    <!-- 规则接管判定后，指标/阈值两栏既没有写路径也没有信息量，整段删掉；历史值仍在表里显示。 -->

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

    <!-- 公开说明：只读预览，句子由服务端按规则生成（与公开面同一实现）。 -->
    <div class="min-w-0" data-requirement-text>
      <p v-if="previewEmpty" class="text-sm text-slate-500 dark:text-zinc-400" data-requirement-text-empty>
        {{ t('admin.badges.form.requirementTextEmpty') }}
      </p>
      <template v-else>
        <div class="grid gap-3 sm:grid-cols-2">
          <div class="min-w-0">
            <p class="text-xs font-medium text-slate-500 dark:text-zinc-400">
              {{ t('admin.badges.form.requirementSummaryZh') }}
            </p>
            <p
              class="mt-1 whitespace-pre-wrap break-words text-sm text-slate-700 dark:text-zinc-200"
              data-requirement-text-zh
              :aria-busy="previewLoading"
            >
              <span v-if="previewLoading" aria-hidden="true">…</span>
              <template v-else>{{ previewZh }}</template>
            </p>
          </div>
          <div class="min-w-0">
            <p class="text-xs font-medium text-slate-500 dark:text-zinc-400">
              {{ t('admin.badges.form.requirementSummaryEn') }}
            </p>
            <p
              class="mt-1 whitespace-pre-wrap break-words text-sm text-slate-700 dark:text-zinc-200"
              data-requirement-text-en
              :aria-busy="previewLoading"
            >
              <span v-if="previewLoading" aria-hidden="true">…</span>
              <template v-else>{{ previewEn }}</template>
            </p>
          </div>
        </div>
        <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400" data-requirement-text-auto>
          {{ t('admin.badges.form.requirementTextAuto') }}
        </p>
        <p v-if="previewFailed" class="mt-1 text-xs text-amber-600 dark:text-amber-400" data-requirement-text-failed>
          {{ t('admin.badges.form.requirementTextPreviewFailed') }}
        </p>
      </template>
    </div>
  </div>
</template>
