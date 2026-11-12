<script setup lang="ts">
/**
 * 保存既有自动徽章前的影响面确认弹窗。
 *
 * 只呈现 `GET /admin/badges/:id/rule-impact` 的**估算**，不自己算人数：
 * - 估算对象是**库里已保存的规则**（草稿还没有身份），文案必须说清这一点；
 * - 三种呈现互斥：有值（含 0）→「≈ N 人」+ 依据句；`estimated === null` →「无法估算」
 *   + 逐个列出不可估的条件；`partial` → 追加「只算了部分条件，是上界」；
 * - `null` 与 `0` 是两件事：`0` 是读数为零，`null` 是算不出来，两者不许互相顶替。
 *
 * `method` 用显式分支而不是按 method 名拼 i18n 键：拼出来的键既躲不过静态 i18n 扫描
 * （会被当成动态前缀），也只能靠运行时才发现拼错。
 */
import type { BadgeConditionCatalogEntry } from '~/utils/admin/badgeRule'
import type {
  AdminBadgeRuleImpact,
  AdminBadgeRuleImpactStatus
} from '~/composables/admin/badges/useAdminBadgeRuleImpact'

const props = withDefaults(defineProps<{
  impact: AdminBadgeRuleImpact | null
  status?: AdminBadgeRuleImpactStatus
  /** 本次是否改动了规则草稿：是则追加「保存后按新规则在后台重新评估」。 */
  ruleTouched?: boolean
  /** 条件目录：不可估的条件优先显示本地化名字，查不到回退原始 key（不静默丢）。 */
  catalog?: BadgeConditionCatalogEntry[]
}>(), {
  status: 'idle',
  ruleTouched: false,
  catalog: () => []
})

const open = defineModel<boolean>('open', { required: true })

const emit = defineEmits<{ confirm: [], cancel: [] }>()

const { t, locale } = useI18n()

const failed = computed(() => props.status === 'error')
const loading = computed(() => props.status === 'loading')
/** 有读数（含 0）才算「可估」；`null` 走「无法估算」分支。 */
const estimable = computed(() => props.impact !== null && props.impact.estimated !== null)

/** 估算没拿到数时按钮改成「仍然保存」：这不是正常确认，而是明知未知仍继续。 */
const confirmLabel = computed(() => (estimable.value
  ? t('admin.badges.rule.impact.confirm')
  : t('admin.badges.rule.impact.confirmAnyway')))

function conditionLabel(key: string): string {
  const entry = props.catalog.find(item => item.key === key)
  if (!entry) return key
  const zh = locale.value.toLowerCase().startsWith('zh')
  return (zh ? entry.labelZh : entry.labelEn) || key
}

function confirm() {
  open.value = false
  emit('confirm')
}

function cancel() {
  open.value = false
  emit('cancel')
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-md' }">
    <template #content>
      <div class="sf-rule-impact" data-rule-impact>
        <h3 class="text-base font-bold text-slate-900 dark:text-white">
          {{ t('admin.badges.rule.impact.title') }}
        </h3>

        <div class="mt-3 space-y-2 text-sm text-slate-600 dark:text-zinc-300">
          <!-- 对象必须写在最前面：估的是库里那条规则，不是这次编辑的草稿。 -->
          <p data-impact-scope>{{ t('admin.badges.rule.impact.scopeHint') }}</p>
          <p v-if="props.ruleTouched" class="text-slate-500 dark:text-zinc-400" data-impact-rule-changed>
            {{ t('admin.badges.rule.impact.ruleChangedHint') }}
          </p>

          <p v-if="loading" data-impact-loading>{{ t('admin.badges.rule.impact.loading') }}</p>

          <p v-else-if="failed" class="text-amber-600 dark:text-amber-400" data-impact-failed>
            {{ t('admin.badges.rule.impact.loadFailed') }}
          </p>

          <template v-else-if="props.impact">
            <template v-if="estimable">
              <p class="text-lg font-bold text-slate-900 dark:text-white" data-impact-estimate>
                {{ t('admin.badges.rule.impact.estimate', { count: props.impact.estimated }) }}
              </p>
              <p v-if="props.impact.method === 'single_leaf'" data-impact-method>
                {{ t('admin.badges.rule.impact.method.single_leaf') }}
              </p>
              <p v-else-if="props.impact.method === 'strictest_leaf'" data-impact-method>
                {{ t('admin.badges.rule.impact.method.strictest_leaf') }}
              </p>
              <p v-else-if="props.impact.method === 'strictest_leaf_partial'" data-impact-method>
                {{ t('admin.badges.rule.impact.method.strictest_leaf_partial') }}
              </p>
              <p v-else-if="props.impact.method === 'sum_of_leaves'" data-impact-method>
                {{ t('admin.badges.rule.impact.method.sum_of_leaves') }}
              </p>
              <p v-else-if="props.impact.method === 'complement_of_leaf'" data-impact-method>
                {{ t('admin.badges.rule.impact.method.complement_of_leaf') }}
              </p>
              <p v-else data-impact-method>
                {{ t('admin.badges.rule.impact.method.unsupported_condition') }}
              </p>
            </template>

            <template v-else>
              <p class="text-lg font-bold text-slate-900 dark:text-white" data-impact-unestimable>
                {{ t('admin.badges.rule.impact.unestimable') }}
              </p>
              <p class="text-slate-500 dark:text-zinc-400" data-impact-null-hint>
                {{ t('admin.badges.rule.impact.nullHint') }}
              </p>
              <template v-if="props.impact.unsupportedConditions.length > 0">
                <p data-impact-unsupported-title>{{ t('admin.badges.rule.impact.unsupportedTitle') }}</p>
                <ul class="list-disc space-y-0.5 pl-5" data-impact-unsupported>
                  <li v-for="key in props.impact.unsupportedConditions" :key="key">
                    {{ conditionLabel(key) }}
                  </li>
                </ul>
              </template>
              <p v-else data-impact-unsupported-fallback>
                {{ t('admin.badges.rule.impact.method.unsupported_condition') }}
              </p>
            </template>

            <p v-if="props.impact.partial" class="text-amber-600 dark:text-amber-400" data-impact-partial>
              {{ t('admin.badges.rule.impact.partialHint') }}
            </p>
          </template>
        </div>

        <div class="mt-4 flex items-center justify-end gap-2">
          <UButton color="neutral" variant="ghost" data-impact-cancel @click="cancel">
            {{ t('admin.badges.rule.impact.cancel') }}
          </UButton>
          <UButton color="primary" data-impact-confirm @click="confirm">
            {{ confirmLabel }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
