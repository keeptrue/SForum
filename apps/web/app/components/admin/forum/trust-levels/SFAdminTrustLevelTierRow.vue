<script setup lang="ts">
/**
 * 单档行（`SFAdminTrustLevelTiersTab` 的列表项）。
 *
 * 拆分理由：分区组件原先 526 行（超过 AGENTS 的 500 行 review 阈值），而行内的
 * 「头 / 动作组 / 外观面板 / 字段 / 命名池 / 撤回徽标 / 字段级错误」本就是一整块内聚 UI。
 * 拆开之后，分区只管列表容器与披露，行只管自己那一档。
 *
 * 三条不变量（与拆分前逐字一致，靠 `data-sf-*` 钩子锚定）：
 * 1. **只有意图，没有状态**：行不持有表单、不调接口；每个按钮只 emit 一个意图 id + 参数。
 * 2. **停用/删除的可用性由父级算好传进来**（`canRetireNow` / `canDelete`）：
 *    「最后一档不能停用」与「已保存为停用才能删」的判据都依赖整个列表，不能在这一行里猜。
 * 3. **外观编辑不在行内**：调色板按钮只发 `appearance` 意图，编辑本身由 tab 层唯一的
 *    `SFAdminTrustLevelAppearanceDialog` 承担 —— 行内展开会把相邻档位顶出视口，
 *    而图标全量目录与明暗双底预览在行宽里都放不下。
 */
import type { TrustLevelNameSuggestion } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { trustLevelIconOf, trustLevelName } from '~/composables/profile/useTrustLevel'
import { TRUST_LEVEL_TIER_NAME_MAX, type TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import type { TrustLevelSaveError } from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import SFAdminTrustLevelFieldErrors from '~/components/admin/forum/trust-levels/SFAdminTrustLevelFieldErrors.vue'
import SFAdminTrustLevelActionButton from '~/components/admin/forum/trust-levels/SFAdminTrustLevelActionButton.vue'

const props = withDefaults(defineProps<{
  tier: TierForm
  /** active 位次（1 起）；停用档为 `null`（它不参与判定，没有位次）。 */
  rank: number | null
  /** 数组位置：停用档的图标/颜色兜底按它取（与停用前的展示连续）。 */
  index: number
  isFirst: boolean
  isLast: boolean
  canManage: boolean
  canAdd: boolean
  canRetireNow: boolean
  /** 外部算好的删除闸门：该档已保存为停用。 */
  canDelete: boolean
  /** 行根的 `--tl-accent`（父级按有效色算好，行不重复一份配色逻辑）。 */
  accentStyle: Record<string, string>
  namePool: TrustLevelNameSuggestion[]
  saveErrors: TrustLevelSaveError[]
  /** 该档停用/删除会撤回的解锁数量（父级按整个列表算，行只负责渲染）。 */
  retiredImpactCount: number
}>(), {
  saveErrors: () => [],
  retiredImpactCount: 0
})

const emit = defineEmits<{
  insert: [rank: number]
  move: [id: string, delta: number]
  rename: [id: string, value: string, target: 'zh' | 'en']
  setIcon: [id: string, icon: string | null]
  setColor: [id: string, color: string | null]
  retire: [id: string]
  restore: [id: string]
  delete: [id: string]
  appearance: [id: string]
}>()

const { t, locale } = useI18n()
/** 命名池按钮显示哪一语种：模板里不做 `locale.startsWith`（ref 在压缩/测试环境下不一定被解包）。 */
const preferZh = computed(() => locale.value.toLowerCase().startsWith('zh'))

/** 图标/颜色的兜底位次：active 用 rank，停用档用数组位置（与拆分前同一口径）。 */
const fallbackRank = computed(() => props.rank ?? props.index)
const name = computed(() => trustLevelName(props.tier, locale.value, props.rank ?? props.index + 1, t))
const icon = computed(() => trustLevelIconOf(props.tier, fallbackRank.value))
const deleteLocked = computed(() => t('admin.forum.trustLevels.tiers.deleteLocked'))

function onNameInput(value: string | number, target: 'zh' | 'en') {
  emit('rename', props.tier.id, String(value ?? ''), target)
}

function applySuggestion(suggestion: TrustLevelNameSuggestion) {
  if (!props.canManage) return
  emit('rename', props.tier.id, suggestion.zh, 'zh')
  emit('rename', props.tier.id, suggestion.en, 'en')
}
</script>

<template>
  <li
    class="sf-tier-row"
    :style="accentStyle"
    data-sf-tl-accent
    :data-sf-tier-row="tier.id"
    :data-tier-id="tier.id"
    :data-retired="tier.retired ? 'true' : 'false'"
    :data-rank="tier.retired ? '' : String(rank ?? '')"
  >
    <div class="sf-tier-row__head">
      <span class="sf-tier-row__rank" aria-hidden="true">
        {{ tier.retired ? t('admin.forum.trustLevels.tiers.retiredBadge') : `TL${rank}` }}
      </span>
      <span class="sf-tier-row__icon" :data-tier-id="tier.id" aria-hidden="true">
        <UIcon :name="icon" class="size-3.5" />
      </span>
      <span class="sf-tier-row__name">{{ name }}</span>
      <span class="sf-tier-row__id" :title="t('admin.forum.trustLevels.tiers.idHint')">{{ tier.id }}</span>

      <!-- 已停用档撤回的解锁：常驻徽标（不是 Toast），停用档在列表里始终带着自己的后果。 -->
      <UBadge
        v-if="tier.retired && retiredImpactCount > 0"
        color="warning"
        variant="soft"
        size="sm"
        data-sf-tier-retired-impact
      >
        {{ t('admin.forum.trustLevels.tiers.retiredImpact', { count: retiredImpactCount }) }}
      </UBadge>

      <div class="sf-tier-row__actions">
        <SFAdminTrustLevelActionButton
          icon="i-lucide-arrow-up"
          :label="t('admin.forum.trustLevels.tiers.moveUp')"
          :title="t('admin.forum.trustLevels.tiers.moveUp')"
          :disabled="!canManage || tier.retired || isFirst"
          :data-sf-tier-move-up="tier.id"
          @click="emit('move', tier.id, -1)"
        />
        <SFAdminTrustLevelActionButton
          icon="i-lucide-arrow-down"
          :label="t('admin.forum.trustLevels.tiers.moveDown')"
          :title="t('admin.forum.trustLevels.tiers.moveDown')"
          :disabled="!canManage || tier.retired || isLast"
          :data-sf-tier-move-down="tier.id"
          @click="emit('move', tier.id, 1)"
        />
        <SFAdminTrustLevelActionButton
          icon="i-lucide-corner-down-right"
          :label="t('admin.forum.trustLevels.tiers.insertBefore')"
          :title="t('admin.forum.trustLevels.tiers.insertBefore')"
          :disabled="!canManage || !canAdd || tier.retired"
          :data-sf-tier-insert="tier.id"
          @click="emit('insert', rank ?? 0)"
        />
        <SFAdminTrustLevelActionButton
          icon="i-lucide-palette"
          :label="t('admin.forum.trustLevels.tiers.appearance')"
          :title="t('admin.forum.trustLevels.tiers.appearance')"
          :disabled="!canManage"
          aria-haspopup="dialog"
          :data-sf-tier-appearance-toggle="tier.id"
          @click="emit('appearance', tier.id)"
        />
        <SFAdminTrustLevelActionButton
          v-if="tier.retired"
          variant="outline"
          icon="i-lucide-undo-2"
          :disabled="!canManage || !canAdd"
          :data-sf-tier-restore="tier.id"
          @click="emit('restore', tier.id)"
        >
          {{ t('admin.forum.trustLevels.tiers.restore') }}
        </SFAdminTrustLevelActionButton>
        <SFAdminTrustLevelActionButton
          v-if="tier.retired"
          variant="outline"
          icon="i-lucide-trash-2"
          :disabled="!canManage || !canDelete"
          :title="canDelete ? undefined : deleteLocked"
          :data-sf-tier-delete="tier.id"
          @click="emit('delete', tier.id)"
        >
          {{ t('admin.forum.trustLevels.tiers.delete') }}
        </SFAdminTrustLevelActionButton>
        <SFAdminTrustLevelActionButton
          v-else
          variant="outline"
          icon="i-lucide-ban"
          :disabled="!canManage || !canRetireNow"
          :title="canRetireNow ? undefined : t('admin.forum.trustLevels.tiers.lastTierLocked')"
          :data-sf-tier-retire="tier.id"
          @click="emit('retire', tier.id)"
        >
          {{ t('admin.forum.trustLevels.tiers.retire') }}
        </SFAdminTrustLevelActionButton>
      </div>
    </div>

    <SFAdminTrustLevelFieldErrors :errors="saveErrors" />

    <div class="sf-tier-row__fields">
      <UFormField :label="t('admin.forum.trustLevels.tiers.nameZh')" :name="`trust.tier.${tier.id}.name.zh`">
        <UInput
          :model-value="tier.name.zh"
          :maxlength="TRUST_LEVEL_TIER_NAME_MAX"
          :disabled="!canManage"
          :placeholder="t('admin.forum.trustLevels.tiers.namePlaceholder')"
          class="w-full"
          :data-sf-tier-name-zh="tier.id"
          @update:model-value="(value: string | number) => onNameInput(value, 'zh')"
        />
      </UFormField>
      <UFormField :label="t('admin.forum.trustLevels.tiers.nameEn')" :name="`trust.tier.${tier.id}.name.en`">
        <UInput
          :model-value="tier.name.en"
          :maxlength="TRUST_LEVEL_TIER_NAME_MAX"
          :disabled="!canManage"
          :placeholder="t('admin.forum.trustLevels.tiers.namePlaceholder')"
          class="w-full"
          :data-sf-tier-name-en="tier.id"
          @update:model-value="(value: string | number) => onNameInput(value, 'en')"
        />
      </UFormField>
    </div>

    <!-- 命名池：只是编辑辅助（D-N3），点一下把两种语言一起填好，存储里仍只有 name{zh,en}。 -->
    <div v-if="namePool.length > 0 && canManage" class="sf-tier-row__pool" data-sf-tier-name-pool>
      <span class="sf-tier-row__pool-label">{{ t('admin.forum.trustLevels.tiers.namePool') }}</span>
      <UButton
        v-for="suggestion in namePool"
        :key="`${suggestion.zh}-${suggestion.en}`"
        type="button"
        size="xs"
        color="neutral"
        variant="ghost"
        :data-sf-tier-name-suggestion="suggestion.en"
        @click="applySuggestion(suggestion)"
      >
        {{ preferZh ? (suggestion.zh || suggestion.en) : (suggestion.en || suggestion.zh) }}
      </UButton>
    </div>
  </li>
</template>

<style scoped>
.sf-tier-row {
  min-width: 0;
  padding: 12px;
  border: 1px solid var(--sf-border);
  border-radius: 10px;
  background: var(--sf-card);
  /* 色条/底色强度由行自己决定，色相来自 --tl-accent（位次派生或运营覆盖）。 */
  --tl-accent-strength: 65%;
  --tl-accent-tint: 12%;

  /* 老环境（Chromium < 111）没有 color-mix：先给一条纯 token 声明，别让左边直接没有边框。 */
  border-left: 3px solid var(--sf-border);
}

/* 停用档位的色条与底色一并降强度（浅色 45%、深色 55%）；**文字**不降透明度。 */
.sf-tier-row[data-retired="true"] {
  --tl-accent-strength: 45%;
  --tl-accent-tint: 6%;
  border-style: dashed;
}

.dark .sf-tier-row[data-retired="true"] {
  --tl-accent-strength: 55%;
  --tl-accent-tint: 8%;
}

@supports (color: color-mix(in srgb, red, blue)) {
  .sf-tier-row {
    border-left: 3px solid color-mix(in srgb, var(--tl-accent) var(--tl-accent-strength), var(--sf-border));
  }
}

.sf-tier-row__head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.sf-tier-row__rank {
  flex: 0 0 auto;
  padding: 1px 6px;
  border-radius: 6px;
  background: var(--sf-muted);
  color: var(--sf-fg-secondary);
  font-size: 11px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

/* 有 color-mix 时 rank chip 与图标块一起被档位色「点亮」；没有时退回中性配色。 */
@supports (color: color-mix(in srgb, red, blue)) {
  .sf-tier-row__rank {
    border: 1px solid color-mix(in srgb, var(--tl-accent) 35%, var(--sf-border));
    background: color-mix(in srgb, var(--tl-accent) var(--tl-accent-tint), var(--sf-card));
    color: var(--tl-accent);
  }
}

.sf-tier-row__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  color: var(--sf-fg-secondary);
}

@supports (color: color-mix(in srgb, red, blue)) {
  .sf-tier-row__icon {
    border-color: color-mix(in srgb, var(--tl-accent) 35%, var(--sf-border));
    background: color-mix(in srgb, var(--tl-accent) var(--tl-accent-tint), var(--sf-card));
    color: var(--tl-accent);
  }
}

.sf-tier-row__name {
  min-width: 0;
  overflow: hidden;
  color: var(--sf-fg);
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sf-tier-row__id {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-family: var(--sf-font-mono, ui-monospace, monospace);
}

/* 窄屏（390）下命令组换行占满一行，不挤压档位名。 */
.sf-tier-row__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-left: auto;
}

.sf-tier-row__fields {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 8px;
  margin-top: 10px;
}

@media (min-width: 640px) {
  .sf-tier-row__fields {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.sf-tier-row__pool {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px;
  margin-top: 8px;
}

.sf-tier-row__pool-label {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}
</style>
