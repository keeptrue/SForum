<script setup lang="ts">
/**
 * 保存前的影响面确认弹窗（信任等级域，M3b）。
 *
 * 几何整段复用同页已冻结的外观弹窗规范（`sm:max-w-lg` + `min(86dvh,720px)` +
 * body 唯一滚动区 + footer 吸底 + 自建 `role="dialog"`），文案与语义走 copy-spec §3.3。
 *
 * 三条硬边界（都踩过）：
 * 1. **三态互斥按判据、不按措辞**：`estimated === null || estimated === undefined` 才是
 *    「无法估算」，**禁止** `estimated ? … : …` —— `0` 在 JS 是 falsy，那样写会把
 *    「0 人满足」渲染成「无法估算」。
 * 2. `estimated === null` 时**不渲染** `partial` 行：否则屏幕上同时出现「无法估算」与
 *    「只算了部分条件」，两句互相否定。
 * 3. 数字只在数字位出现：`≈ N 人` 的 `≈` 只在 `[data-sf-tl-impact-estimate]` 里，
 *    说明句（`meaningHint`）零数字。
 *
 * 只呈现、不判断：估算来自 `POST /admin/trust-levels/rule-impact`（草稿请求体），
 * 失败也**不**阻止保存（`loadFailed` 是信息，不是闸门）。
 */
import type { BadgeConditionCatalogEntry } from '~/utils/admin/rule'
import type { TrustLevelImpactTierResult, TrustLevelImpactStatus } from '~/composables/admin/forum/trustLevels/useTrustLevelRuleImpact'
import { trustLevelName } from '~/composables/profile/useTrustLevel'
import type { TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

const props = withDefaults(defineProps<{
  open: boolean
  status?: TrustLevelImpactStatus
  /** 逐档结论（与请求同序）；`status !== 'ready'` 时为空数组。 */
  results?: TrustLevelImpactTierResult[]
  /** 档位表单：只用来取显示名，不参与估算。 */
  tiers?: readonly TierForm[]
  /** 条件目录：把 `unsupportedConditions` 的 key 换成人读名字。 */
  catalog?: BadgeConditionCatalogEntry[]
}>(), {
  status: 'idle',
  results: () => [],
  tiers: () => [],
  catalog: () => []
})

const emit = defineEmits<{ confirm: [], cancel: [] }>()

const { t, locale } = useI18n()

const open = computed({
  get: () => props.open,
  set: (value: boolean) => { if (!value) emit('cancel') }
})

const failed = computed(() => props.status === 'error')
const loading = computed(() => props.status === 'loading')
const results = computed(() => props.results ?? [])

/**
 * 有值（含 0）才算「可估」。**必须用 `=== null || === undefined`**：
 * `estimated ? …` 会把 0 当成算不出来。
 */
function estimable(result: TrustLevelImpactTierResult): boolean {
  return result.estimated !== null && result.estimated !== undefined
}

/** 态 B 才列不可估算条件；态 A′ 由 `partialHint` 一句话承担（避免重复）。 */
const unsupported = computed(() => {
  const seen: string[] = []
  for (const result of results.value) {
    if (estimable(result)) continue
    for (const key of result.unsupportedConditions ?? []) if (!seen.includes(key)) seen.push(key)
  }
  return seen
})

/** >5 条收口：默认 5 条，其余由一个 `aria-expanded` 按钮揭示（不内滚、不裁切）。 */
const unsupportedExpanded = ref(false)
const UNSUPPORTED_VISIBLE = 5
const visibleUnsupported = computed(() => (
  unsupportedExpanded.value ? unsupported.value : unsupported.value.slice(0, UNSUPPORTED_VISIBLE)
))
const hiddenUnsupportedCount = computed(() => Math.max(0, unsupported.value.length - UNSUPPORTED_VISIBLE))

function conditionLabel(key: string): string {
  const entry = props.catalog.find(item => item.key === key)
  if (!entry) return key
  const zh = String(locale.value ?? '').toLowerCase().startsWith('zh')
  return (zh ? entry.labelZh : entry.labelEn) || key
}

function tierLabel(tierId: string): string {
  const tier = props.tiers.find(item => item.id === tierId)
  if (!tier) return tierId
  const rank = props.tiers.filter(item => !item.retired).findIndex(item => item.id === tierId) + 1
  return trustLevelName(tier, locale.value, rank > 0 ? rank : 0, t)
}

const confirmLabel = computed(() => (failed.value
  ? t('admin.forum.trustLevels.conditions.impact.confirmAnyway')
  : t('admin.forum.trustLevels.conditions.impact.confirm')))

const titleId = 'sf-tl-impact-title'
const scopeId = 'sf-tl-impact-scope'

function confirm() {
  emit('confirm')
}

function cancel() {
  emit('cancel')
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-lg overflow-hidden' }" data-sf-tl-impact-modal>
    <template #content>
      <div
        class="sf-tl-impact"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        :aria-describedby="scopeId"
        data-sf-tl-impact-dialog
        :data-status="status"
      >
        <header class="sf-tl-impact__header">
          <h3 :id="titleId" class="sf-tl-impact__title">
            {{ t('admin.forum.trustLevels.conditions.impact.title') }}
          </h3>
          <!-- 对象必须写在最前面：估的是「这次要保存的配置」，不是库里那份。 -->
          <p :id="scopeId" class="sf-tl-impact__subtitle" data-sf-tl-impact-scope>
            {{ t('admin.forum.trustLevels.conditions.impact.scopeHint') }}
          </p>
        </header>

        <div class="sf-tl-impact__body" :aria-busy="loading ? 'true' : 'false'" data-sf-tl-impact-body>
          <p v-if="loading" class="sf-tl-impact__line" data-sf-tl-impact-loading>
            {{ t('admin.forum.trustLevels.conditions.impact.loading') }}
          </p>

          <p v-else-if="failed" class="sf-tl-impact__line sf-tl-impact__line--warning" data-sf-tl-impact-load-failed>
            {{ t('admin.forum.trustLevels.conditions.impact.loadFailed') }}
          </p>

          <template v-else>
            <p class="sf-tl-impact__line" data-sf-tl-impact-meaning>
              {{ t('admin.forum.trustLevels.conditions.impact.meaningHint') }}
            </p>

            <section
              v-for="result in results"
              :key="result.tierId || 'tier'"
              class="sf-tl-impact__tier"
              :data-sf-tl-impact-tier="result.tierId"
            >
              <p class="sf-tl-impact__tier-name">{{ tierLabel(result.tierId) }}</p>

              <template v-if="estimable(result)">
                <!-- 唯一数字位：`≈` 只在这里出现，说明句零数字。 -->
                <p class="sf-tl-impact__estimate" data-sf-tl-impact-estimate>
                  {{ t('admin.forum.trustLevels.conditions.impact.estimate', { count: result.estimated }) }}
                </p>
                <p v-if="result.method" class="sf-tl-impact__basis" data-sf-tl-impact-method>
                  {{ t(`admin.forum.trustLevels.conditions.impact.method.${result.method}`) }}
                </p>
                <!-- 态 A′：partial 是 A 的**追加行**，不是替代态。 -->
                <p v-if="result.partial" class="sf-tl-impact__line sf-tl-impact__line--warning" data-sf-tl-impact-partial>
                  {{ t('admin.forum.trustLevels.conditions.impact.partialHint') }}
                </p>
              </template>

              <template v-else>
                <!-- 态 B：无 ≈、无数字、不渲染 partial。 -->
                <p class="sf-tl-impact__unestimable" data-sf-tl-impact-unestimable>
                  {{ t('admin.forum.trustLevels.conditions.impact.unestimable') }}
                </p>
                <p class="sf-tl-impact__line" data-sf-tl-impact-null-hint>
                  {{ t('admin.forum.trustLevels.conditions.impact.nullHint') }}
                </p>
              </template>
            </section>

            <template v-if="unsupported.length > 0">
              <p class="sf-tl-impact__line" data-sf-tl-impact-unsupported-title>
                {{ t('admin.forum.trustLevels.conditions.impact.unsupportedTitle') }}
              </p>
              <ul class="sf-tl-impact__list" data-sf-tl-impact-unsupported>
                <li v-for="key in visibleUnsupported" :key="key">{{ conditionLabel(key) }}</li>
              </ul>
              <button
                v-if="hiddenUnsupportedCount > 0 || unsupportedExpanded"
                type="button"
                class="sf-tl-impact__more"
                data-sf-tl-impact-unsupported-more
                :aria-expanded="unsupportedExpanded ? 'true' : 'false'"
                @click="unsupportedExpanded = !unsupportedExpanded"
              >
                {{ unsupportedExpanded
                  ? t('admin.forum.trustLevels.conditions.impact.unsupportedLess')
                  : t('admin.forum.trustLevels.conditions.impact.unsupportedMore', { count: hiddenUnsupportedCount }) }}
              </button>
            </template>

            <p class="sf-tl-impact__line sf-tl-impact__line--warning" data-sf-tl-impact-consequence>
              {{ t('admin.forum.trustLevels.conditions.impact.consequence') }}
            </p>
          </template>
        </div>

        <footer class="sf-tl-impact__footer">
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            data-sf-tl-impact-cancel
            autofocus
            @click="cancel"
          >
            {{ t('admin.forum.trustLevels.conditions.impact.cancel') }}
          </UButton>
          <UButton
            type="button"
            color="warning"
            variant="solid"
            :disabled="loading"
            data-sf-tl-impact-confirm
            @click="confirm"
          >
            {{ confirmLabel }}
          </UButton>
        </footer>
      </div>
    </template>
  </UModal>
</template>

<style scoped>
/* 整段复用 2026-11-12 冻结的外观弹窗几何，只换宽度（本页三个确认框都是 lg）。 */
.sf-tl-impact {
  display: flex;
  flex-direction: column;
  max-height: 85vh;
  max-height: min(86dvh, 720px);
  min-height: 0;
}

@media (max-width: 639px) {
  .sf-tl-impact {
    max-height: calc(100dvh - 48px);
  }
}

.sf-tl-impact__header {
  flex: 0 0 auto;
  padding: 16px 18px 12px;
  border-bottom: 1px solid var(--sf-border);
}

.sf-tl-impact__title {
  margin: 0;
  color: var(--sf-fg);
  font-size: 16px;
  font-weight: 700;
}

.sf-tl-impact__subtitle {
  margin: 4px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}

/* body 是弹窗内**唯一**滚动区；footer 吸底不参与滚动。 */
.sf-tl-impact__body {
  display: grid;
  flex: 1 1 auto;
  gap: 10px;
  min-height: 0;
  padding: 16px 18px 24px;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.sf-tl-impact__line {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}

.sf-tl-impact__line--warning {
  color: var(--sf-fg-secondary);
}

.sf-tl-impact__tier {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.sf-tl-impact__tier-name {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
}

/* 数字位：≈ 15/500 次级、数字 18/700 主色、单位 12/500 次级，基线对齐。 */
.sf-tl-impact__estimate {
  display: flex;
  align-items: baseline;
  gap: 4px;
  margin: 0;
  color: var(--sf-fg);
  font-size: 18px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.sf-tl-impact__unestimable {
  margin: 0;
  color: var(--sf-fg);
  font-size: 16px;
  font-weight: 700;
}

.sf-tl-impact__basis {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  line-height: 1.5;
}

.sf-tl-impact__list {
  display: grid;
  gap: 0;
  margin: 0;
  padding-left: 20px;
  list-style: disc;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 18px;
}

.sf-tl-impact__list li {
  overflow-wrap: anywhere;
}

.sf-tl-impact__more {
  align-self: start;
  padding: 2px 0;
  border: 0;
  background: transparent;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  text-decoration: underline;
}

.sf-tl-impact__more:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-tl-impact__footer {
  position: sticky;
  bottom: 0;
  z-index: 1;
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 18px;
  border-top: 1px solid var(--sf-border);
  background: var(--sf-card);
}

@media (max-width: 639px) {
  .sf-tl-impact__header,
  .sf-tl-impact__body,
  .sf-tl-impact__footer {
    padding-right: 14px;
    padding-left: 14px;
  }
}
</style>
