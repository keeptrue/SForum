<script setup lang="ts">
/**
 * 后台「信任等级 · 档位」分区（第四个 tab）。
 *
 * 本组件只负责**列表级**的东西：计数、上限提示、单档披露、列表容器、墓碑行；
 * 单档的字段/动作/外观面板在 `SFAdminTrustLevelTierRow`（拆分前本文件 526 行）。
 * 表单状态仍归 `useTrustLevelsAdminPage()`，页头与吸底 footer 归 route shell。
 *
 * 四条不可退让的语义：
 * 1. **顺序即判定顺序**：上移/下移只改数组顺序，`rank` 由 active 位次派生（TL0 不在列表里）。
 * 2. **id 锚定**：插入一档只改变顺序，绝不改动其它档的配置；id 不因改名/排序变化。
 * 3. **软停用**：`retired` 的档位留在列表里、配置可继续编辑，但判定与 UI 跳过（D-N8）。
 *    停用/删除按钮只发意图 —— 二次确认与影响面披露由 route shell 里的确认对话框以 modal 承担
 *    （常驻条会被滚出视口，等于没披露）。
 * 4. **每档默认就有颜色**（D-C1）：`--tl-accent` 由位次派生，运营可单档覆盖；
 *    已停用档常驻「已撤回 N 项解锁」徽标（不是 Toast —— 它是状态，不是一次事件的回声）。
 */
import type { TrustLevelColorChoice, TrustLevelIconChoice, TrustLevelNameSuggestion } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { DEFAULT_TIER_ICON_NAMES } from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import {
  activeRankOf,
  retiredTierImpactKeys,
  TRUST_LEVEL_TIER_MAX,
  type TierForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import type { TrustLevelSaveError } from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import SFAdminTrustLevelActionButton from '~/components/admin/forum/trust-levels/SFAdminTrustLevelActionButton.vue'
import SFAdminTrustLevelTierRow from '~/components/admin/forum/trust-levels/SFAdminTrustLevelTierRow.vue'

const props = withDefaults(defineProps<{
  tiers: TierForm[]
  canManage: boolean
  canAdd: boolean
  canRetire: boolean
  namePool: TrustLevelNameSuggestion[]
  iconChoices: TrustLevelIconChoice[]
  /** 颜色白名单（D-C1）：空 = 后端未下发 → 颜色选择器不编造候选。 */
  colorChoices?: TrustLevelColorChoice[]
  /** **已保存**的停用档 id：只有它命中时才允许删除（删除两步闸门，D-C2）。 */
  savedRetiredIds?: string[]
  /** 删除墓碑：只用于列表底部的灰字说明，永不渲染成可点条目。 */
  deletedTierIds?: string[]
  /** 按 tier id 归位的保存失败明细（P2）。 */
  saveErrors?: Record<string, TrustLevelSaveError[]>
}>(), {
  colorChoices: () => [],
  savedRetiredIds: () => [],
  deletedTierIds: () => [],
  saveErrors: () => ({})
})

const emit = defineEmits<{
  add: []
  insert: [rank: number]
  move: [id: string, delta: number]
  rename: [id: string, value: string, target: 'zh' | 'en']
  setIcon: [id: string, icon: string | null]
  setColor: [id: string, color: string | null]
  retire: [id: string]
  restore: [id: string]
  delete: [id: string]
}>()

const { t } = useI18n()
const activeCount = computed(() => props.tiers.filter(tier => !tier.retired).length)
const atLimit = computed(() => activeCount.value >= TRUST_LEVEL_TIER_MAX)
/**
 * 能否停用：active 档数 > 1。判据看**当前列表**而不是父组件传的布尔：
 * 「最后一档点不动但不说为什么」就是这样漏出去的（父传值一旦失准，按钮就成了静默死键）。
 */
const canRetireNow = computed(() => props.canRetire && activeCount.value > 1)
const lastTierLocked = computed(() => t('admin.forum.trustLevels.tiers.lastTierLocked'))
const limitHint = computed(() => t('admin.forum.trustLevels.tiers.limitHint', { max: TRUST_LEVEL_TIER_MAX }))
/** 一次只展开一个「外观」面板（图标 + 颜色同屏）：多于一个就是两套焦点目标，键盘用户会迷失。 */
const appearanceFor = ref<string | null>(null)
/** 默认图标序列只是展示参考（未选图标时 rank 决定用哪个）。 */
const defaultIconPreview = computed(() => DEFAULT_TIER_ICON_NAMES.slice(0, TRUST_LEVEL_TIER_MAX))

function tierRank(id: string) {
  return activeRankOf(props.tiers, id)
}

/** 颜色的位次口径与图标一致：停用档传数组位置（它不参与判定，颜色只是「长什么样」）。 */
function tierColorRank(tier: TierForm) {
  return tier.retired
    ? props.tiers.indexOf(tier)
    : (activeRankOf(props.tiers, tier.id) ?? props.tiers.indexOf(tier))
}

/** 有效色（存储值优先，否则按位次）。行根把结果落成 `--tl-accent`。 */
function tierAccent(tier: TierForm) {
  return tierAccentStyle(tierColorOf(tier, tierColorRank(tier)))
}

/** 删除闸门：该档已保存为停用（判据在父级，行只拿布尔）。 */
function isDeleteUnlocked(id: string) {
  return props.savedRetiredIds.includes(id)
}

/** 该档停用/删除会撤回的解锁数量（常驻徽标；0 时不渲染，避免一行噪声）。 */
function tierRetiredImpact(tier: TierForm) {
  return retiredTierImpactKeys(props.tiers, tier.id).length
}

function toggleAppearance(id: string) {
  appearanceFor.value = appearanceFor.value === id ? null : id
}
</script>

<template>
  <section class="grid min-w-0 gap-4" data-sf-trust-level-tiers aria-labelledby="sf-trust-levels-panel-title">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="min-w-0 text-xs text-muted" data-sf-tier-count>
        {{ t('admin.forum.trustLevels.tiers.activeCount', { count: activeCount, max: TRUST_LEVEL_TIER_MAX }) }}
      </p>
      <SFAdminTrustLevelActionButton
        variant="outline"
        icon="i-lucide-plus"
        :disabled="!canManage || !canAdd"
        :title="atLimit ? limitHint : undefined"
        data-sf-tier-add
        @click="emit('add')"
      >
        {{ t('admin.forum.trustLevels.tiers.add') }}
      </SFAdminTrustLevelActionButton>
    </div>

    <p v-if="atLimit" class="text-xs text-muted" data-sf-tier-limit-hint>{{ limitHint }}</p>

    <!--
      N = 1 的后果披露（计划 §9）：只剩一档时账龄闸不再产生任何差异。
      常驻、不自动消失、不进 Toast —— 运营在按「新增一档」之前就该读到它。
    -->
    <UAlert
      v-if="activeCount === 1"
      color="warning"
      variant="soft"
      icon="i-lucide-info"
      :title="t('admin.forum.trustLevels.tiers.singleTier.title')"
      :description="t('admin.forum.trustLevels.tiers.singleTier.description')"
      data-sf-tier-single-disclosure
    />

    <!-- 顺序即判定顺序：上移/下移而不是拖拽（键盘与触屏都可达，不依赖指针精度）。 -->
    <ol class="m-0 grid list-none gap-3 p-0" data-sf-tier-list>
      <SFAdminTrustLevelTierRow
        v-for="(tier, index) in tiers"
        :key="tier.id"
        :tier="tier"
        :rank="tier.retired ? null : tierRank(tier.id)"
        :index="index"
        :is-first="index === 0"
        :is-last="index === tiers.length - 1"
        :can-manage="canManage"
        :can-add="canAdd"
        :can-retire-now="canRetireNow"
        :can-delete="isDeleteUnlocked(tier.id)"
        :appearance-open="appearanceFor === tier.id"
        :accent-style="tierAccent(tier)"
        :name-pool="namePool"
        :icon-choices="iconChoices"
        :color-choices="colorChoices"
        :save-errors="saveErrors?.[tier.id] ?? []"
        :retired-impact-count="tierRetiredImpact(tier)"
        @insert="emit('insert', $event)"
        @move="(id: string, delta: number) => emit('move', id, delta)"
        @rename="(id: string, value: string, target: 'zh' | 'en') => emit('rename', id, value, target)"
        @set-icon="(id: string, icon: string | null) => emit('setIcon', id, icon)"
        @set-color="(id: string, color: string | null) => emit('setColor', id, color)"
        @retire="emit('retire', $event)"
        @restore="emit('restore', $event)"
        @delete="emit('delete', $event)"
        @toggle-appearance="toggleAppearance"
      />
    </ol>

    <!-- 最后一档不能停用的理由：常驻、非 Toast，就在列表下方。 -->
    <p v-if="activeCount === 1" class="text-xs text-muted" data-sf-tier-last-locked>
      {{ lastTierLocked }}
    </p>

    <!-- 删除墓碑：id 已经永久作废，这里只是把「为什么新档从更大的序号开始」讲清楚。 -->
    <p v-if="deletedTierIds.length > 0" class="text-xs text-muted" data-sf-tier-tombstones>
      {{ t('admin.forum.trustLevels.tiers.tombstones', { ids: deletedTierIds.join(' · ') }) }}
    </p>

    <p class="text-xs text-muted" data-sf-tier-default-icon-sequence>
      {{ t('admin.forum.trustLevels.tiers.defaultIconHint', { icons: defaultIconPreview.join(' · ') }) }}
    </p>
  </section>
</template>
