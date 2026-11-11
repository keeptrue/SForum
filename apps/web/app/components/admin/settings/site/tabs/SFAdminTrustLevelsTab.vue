<script setup lang="ts">
/**
 * 后台「信任等级」阈值 tab（M5）。
 *
 * 位置：`/control-panel/settings?tab=trustLevels`，紧接 newcomers。
 * 本组件**不渲染页头、不渲染 UDashboardToolbar、不自建 tablist** —— 三者都属于 route shell
 * （`pages/admin/settings/index.vue`），这里再画一层就是 AGENTS.md 禁止的第二个页头。
 *
 * 契约权威是 Go（`app/Models/TrustLevels/config.go`）：`trust.levels` 是单键 JSON、
 * `levels` 是数组、只有 TL1–TL4、`requirements` 六个 metric、`limits` 四字段可空。
 * 字段名一律照抄，不自创——后端 `DisallowUnknownFields` 会直接拒绝。
 *
 * 几何与 newcomers tab 同构：UCard（header + body + sticky footer）→ 恢复默认行 → 表单网格
 * → SFAdminFormFooter。D12 的「恢复默认不会影响已获得等级」披露点只有一条常驻 UAlert：
 * `useSettingsSection().runRestore` 没有确认步骤，这句话必须在按下按钮**之前**读得到，
 * 因此禁止改写成 Toast。
 */
import type { AdminWebOption } from '~/composables/useWebOptions'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import { adminOptionMap, useAdminOptionTab } from '~/composables/admin/settings/useAdminOptionTab'
import {
  blockNonIntegerKey,
  cloneTrustLevelsForm,
  normalizeTrustLevelsForm,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  TRUST_LEVEL_EDITABLE_LEVELS,
  TRUST_LEVEL_ROWS,
  TRUST_LEVEL_THRESHOLD_METRICS,
  trustLevelsPayload,
  type TrustLevelsForm
} from '~/composables/admin/settings/useTrustLevelsThresholds'
import { trustLevelIcon, trustLevelSlug } from '~/composables/profile/useTrustLevel'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'

const props = defineProps<{ items: AdminWebOption[] }>()
const emit = defineEmits<{ saved: [items: AdminWebOption[]] }>()
const { t } = useI18n()
const toast = useToast()
const section = useSettingsSection()
const { saveOptions } = useAdminOptionTab(items => emit('saved', items))
const map = computed(() => adminOptionMap(props.items))

const initial = ref<TrustLevelsForm>(recommendedTrustLevelsForm())
const form = ref<TrustLevelsForm>(recommendedTrustLevelsForm())
const hasChanges = computed(() => JSON.stringify(form.value) !== JSON.stringify(initial.value))

watch(() => props.items, resetFromItems, { immediate: true })

function resetFromItems() {
  const config = readTrustLevelsConfig(map.value)
  initial.value = config
  form.value = cloneTrustLevelsForm(config)
}

async function save() {
  await section.runSave({
    successTitle: t('admin.settings.saved'),
    failureTitle: t('admin.settings.saveFailed'),
    prepare: () => {
      form.value = normalizeTrustLevelsForm(form.value)
    },
    save: () => saveOptions(trustLevelsPayload(form.value))
  })
}

function resetChanges() {
  form.value = cloneTrustLevelsForm(initial.value)
  toast.add({ color: 'neutral', icon: 'i-lucide-rotate-ccw', title: t('admin.settings.trustLevels.resetChanges'), duration: 5000 })
}

function restoreRecommended() {
  section.runRestore({
    title: t('admin.settings.trustLevels.restoreDefaults'),
    apply: () => {
      // 只重置阈值：limits / earnable 本刀没有编辑器，重置它们等于不可见地改运营配置。
      const recommended = recommendedTrustLevelsForm()
      form.value = {
        requirements: recommended.requirements,
        limits: form.value.limits,
        earnable: form.value.earnable
      }
    }
  })
}

function levelLabel(level: number) {
  return t(`admin.settings.trustLevels.level.${trustLevelSlug(level)}`)
}

function metricLabel(i18nKey: string) {
  return t(`admin.settings.trustLevels.metric.${i18nKey}`)
}

// 阈值读写走这一对访问器：`noUncheckedIndexedAccess` 下 `form[level][metric]` 是可能 undefined，
// 在模板里直接双下标会触发 TS2532，且真缺键时会写出 NaN。
function thresholdValue(level: number, suffix: string) {
  return form.value.requirements[String(level)]?.[suffix] ?? 0
}

function setThreshold(level: number, suffix: string, value: string | number) {
  const row = form.value.requirements[String(level)]
  if (!row) return
  const parsed = Number(value)
  row[suffix] = Number.isFinite(parsed) ? Math.trunc(parsed) : 0
}
</script>

<template>
  <form class="flex flex-col" @submit.prevent="save">
    <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }">
      <template #header>
        <div class="flex items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-bold">{{ t('admin.settings.trustLevels.title') }}</h2>
            <p class="mt-1 text-xs text-muted">{{ t('admin.settings.trustLevels.description') }}</p>
          </div>
          <UBadge color="neutral" variant="soft" class="font-mono">trust.levels</UBadge>
        </div>
      </template>

      <div class="space-y-5">
        <!-- D12 唯一披露点：runRestore 无确认框，这句必须常驻，不能改成 Toast。 -->
        <div class="flex flex-wrap items-start justify-between gap-3">
          <UAlert color="neutral" variant="soft" icon="i-lucide-info" :title="t('admin.settings.trustLevels.restoreHint')" class="flex-1" />
          <UButton type="button" color="neutral" variant="outline" leading-icon="i-lucide-rotate-ccw" class="self-start" @click="restoreRecommended">
            {{ t('admin.settings.trustLevels.restoreDefaults') }}
          </UButton>
        </div>

        <!-- 一行一档，5 行一屏可见：折叠会让运营漏改一档。 -->
        <fieldset
          v-for="level in TRUST_LEVEL_ROWS"
          :key="level"
          class="sf-trust-threshold-row mt-3.5 border-t border-slate-200 pt-3.5 first:mt-0 first:border-t-0 first:pt-0 dark:border-zinc-800"
          :data-level="String(level)"
          :data-readonly="level === 0 ? 'true' : 'false'"
        >
          <legend class="sf-trust-threshold-row__legend">
            <span class="sf-trust-threshold-row__icon" :data-level="String(level)" aria-hidden="true">
              <UIcon :name="trustLevelIcon(level)" class="sf-trust-threshold-row__glyph" />
            </span>
            <span class="sf-trust-threshold-row__name">{{ levelLabel(level) }}</span>
            <!-- 与资料页卡同一键：TL 编号是标识符不是文案，一处格式、两处展示。 -->
            <span class="sf-trust-threshold-row__code">{{ t('profile.trustLevel.code', { level }) }}</span>
          </legend>

          <!-- TL0 由 trust.new_user_* 决定，config.go 拒绝它出现在 trust.levels 里：只读展示。 -->
          <p v-if="level === 0" class="mb-2 text-xs text-muted">{{ t('admin.settings.trustLevels.tl0Hint') }}</p>

          <div class="sf-trust-threshold-row__grid">
            <UFormField
              v-for="metric in TRUST_LEVEL_THRESHOLD_METRICS"
              :key="metric.suffix"
              :label="metricLabel(metric.i18nKey)"
              :name="`trust.level.${level}.${metric.suffix}`"
            >
              <UInput
                v-if="level === 0"
                :model-value="''"
                type="number"
                placeholder="—"
                disabled
                class="w-full"
              />
              <UInput
                v-else
                :model-value="thresholdValue(level, metric.suffix)"
                type="number"
                min="0"
                :max="metric.max"
                class="w-full"
                @keydown="blockNonIntegerKey"
                @update:model-value="(value: string | number) => setThreshold(level, metric.suffix, value)"
              />
            </UFormField>
          </div>
        </fieldset>

        <p class="text-xs text-muted">{{ t('admin.settings.trustLevels.zeroUnlimited') }}</p>

        <!--
          D17 的 earnable 编辑入口留位：能力解锁集合的 API 形状未定稿（M4 只冻结阈值读写），
          这里刻意不画勾选矩阵 —— 画了就是与后端能力映射表并列的第二套真相。
          后端契约到位后，在本行下方插入 `<SFAdminTrustLevelsEarnable />`（每档一组复选框，
          数据源为 `trust.levels.levels[].earnable`，几何沿用同一 UCard body）。
          注意：即使没有编辑器，保存也必须把已存在的 `earnable` 原样带回（见 trustLevelsPayload）。
        -->
      </div>

      <template #footer>
        <SFAdminFormFooter
          :saving="section.saving.value"
          :show-unsaved-alert="hasChanges"
          :submit-text="t('admin.settings.save')"
          @reset="resetChanges"
        />
      </template>
    </UCard>
  </form>
</template>

<style scoped>
.sf-trust-threshold-row {
  margin: 0;
  padding: 0;
  border: 0;
}

.sf-trust-threshold-row__legend {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
  padding: 0;
}

/* 24×24 比资料页卡小一档：后台行密度优先。环线/填充沿用五档阶梯（规范 §2.2）。 */
.sf-trust-threshold-row__icon {
  --tl-accent: var(--sf-accent);

  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: transparent;
  color: var(--sf-fg-tertiary);
}

.dark .sf-trust-threshold-row__icon {
  --tl-accent: var(--sf-accent-dark);
}

.sf-trust-threshold-row__glyph {
  width: 14px;
  height: 14px;
}

.sf-trust-threshold-row__icon[data-level="0"] {
  border-style: dashed;
}

.sf-trust-threshold-row__icon[data-level="1"] {
  color: var(--sf-fg-secondary);
}

.sf-trust-threshold-row__icon[data-level="2"] {
  border-color: color-mix(in srgb, var(--tl-accent) 34%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

.sf-trust-threshold-row__icon[data-level="3"] {
  border: 2px solid color-mix(in srgb, var(--tl-accent) 55%, transparent);
  background: var(--sf-accent-soft);
  color: var(--tl-accent);
}

.sf-trust-threshold-row__icon[data-level="4"] {
  border-color: var(--tl-accent);
  background: var(--tl-accent);
  color: var(--sf-card);
}

.sf-trust-threshold-row__name {
  color: inherit;
  font-size: 14px;
  font-weight: 600;
}

.sf-trust-threshold-row__code {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

/* ≤767（含 390）：2 列 + 末项跨满，无半格空隙、无横向溢出。 */
.sf-trust-threshold-row__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.sf-trust-threshold-row__grid > :last-child {
  grid-column: span 2;
}

/* 768–1279：3 列（6 项 → 3+3，无空格）。 */
@media (min-width: 768px) {
  .sf-trust-threshold-row__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }

  .sf-trust-threshold-row__grid > :last-child {
    grid-column: auto;
  }
}

/* ≥1280：6 列 —— 恰好等于 metric 数（Go `Metrics` 6 项），一行一档才成立。 */
@media (min-width: 1280px) {
  .sf-trust-threshold-row__grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}
</style>
