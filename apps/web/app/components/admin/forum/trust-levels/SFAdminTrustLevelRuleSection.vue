<script setup lang="ts">
/**
 * 阈值 tab 里每档的「附加条件」面板内容（M3b）。
 *
 * 只做三件事：**选态 → 渲染**。五态互斥的判据来自调用方（目录三态 + 权限 + 功能开关），
 * 本组件不重新判断一遍——两处判断必然漂移，而失败态一旦落成空白，运营就会以为「这档没条件」
 * 然后保存，等于把已存条件当成不存在。
 *
 * 五态优先级（copy-spec §3.2）：
 *   readOnly（无权限）> featureOff（开关关闭）> loading > loadFailed（含目录空）> 编辑态。
 *
 * 编辑态里还分「有规则 / 没规则 / 规则读不出来」：最后一种**不静默**，给一行
 * 「规则数据无法解析」并保留「添加条件」作为替换入口（原值仍在载荷里，未编辑则原样回传）。
 */
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRule } from '~/utils/admin/rule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import type { BadgeRuleLabelFamily } from '~/utils/admin/badgeRuleParams'
import { createDefaultRule } from '~/utils/admin/rule'
import {
  decodeRuleDocument
} from '~/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import type { TrustLevelConditionsStatus } from '~/composables/admin/forum/trustLevels/useAdminTrustLevelConditions'
import type { TrustLevelRuleDocument } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import SFAdminRuleEditor from '~/components/admin/rules/SFAdminRuleEditor.vue'

const props = withDefaults(defineProps<{
  tierId: string
  /** 读面下发的原样规则（未编辑时必须逐字节回传，所以这里不解码成规范形状）。 */
  rule: TrustLevelRuleDocument
  catalog?: BadgeConditionCatalogEntry[]
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  refCandidates?: BadgeRefCandidateMap
  conditionsStatus?: TrustLevelConditionsStatus
  conditionsError?: string
  canManage?: boolean
  featureOff?: boolean
  retired?: boolean
}>(), {
  catalog: () => [],
  factCandidates: () => [],
  factCandidatesFailed: false,
  refCandidates: () => ({}),
  conditionsStatus: 'idle',
  conditionsError: '',
  canManage: true,
  featureOff: false,
  retired: false
})

const emit = defineEmits<{
  'update:rule': [rule: TrustLevelRuleDocument]
  retry: []
}>()

const { t } = useI18n()

const rulePrefix = 'admin.forum.trustLevels.rule'

/**
 * 条件族（提示句 / 占位符 / 参数名 / 指标名）的信任等级副本。
 *
 * 键位是徽章「条件族 + 指标族」的**逐字副本**（徽章前缀见 `BADGE_LABEL_FAMILY`，
 * 这里刻意不写徽章的绝对键：本目录一旦出现徽章键，就是跨族取词的信号），
 * 由 `tests/admin/forum/trustLevelRuleI18n.test.ts` 逐键比对。跨族取词会让徽章族
 * 一次改名静默改掉信任侧的指标名与下拉标签（M3b-F1 修的就是这个缺陷）。
 */
const trustConditionPrefix = 'admin.forum.trustLevels.ruleConditions'
const trustLabelFamily: BadgeRuleLabelFamily = {
  params: 'admin.forum.trustLevels.ruleConditions.params',
  metrics: 'admin.forum.trustLevels.ruleConditions.metrics',
  enums: 'admin.forum.trustLevels.ruleConditions.enums'
}

/** 目录是否**可用**：`failed` 与「成功但 0 条」都算不可用（后者等于没有可选条件）。 */
const catalogUnavailable = computed(() => (
  props.conditionsStatus === 'failed'
  || (props.conditionsStatus === 'ready' && props.catalog.length === 0)
))

const loading = computed(() => props.conditionsStatus === 'loading' || props.conditionsStatus === 'idle')

/** 原样规则 → 可编辑文档。`null` = 这档没有规则。 */
const document = computed<BadgeRule | null>(() => decodeRuleDocument(props.rule))
const undecodable = computed(() => props.rule !== null && props.rule !== undefined && document.value === null)

function addRule() {
  const entry = props.catalog.find(item => item.key === 'activity.count') ?? props.catalog[0]
  if (!entry) return
  emit('update:rule', createDefaultRule(entry) as unknown as TrustLevelRuleDocument)
}

function update(next: BadgeRule) {
  emit('update:rule', next as unknown as TrustLevelRuleDocument)
}
</script>

<template>
  <div class="sf-tl-rule-section" data-sf-tl-rule-section :data-tier-id="tierId">
    <!-- 1) 无权限：比功能开关更根本，先命中先定态。 -->
    <template v-if="!canManage">
      <p class="sf-tl-rule-section__title" data-sf-tl-rule-readonly>
        {{ t('admin.forum.trustLevels.conditions.panel.readOnly.title') }}
      </p>
      <p class="sf-tl-rule-section__line">
        {{ t('admin.forum.trustLevels.conditions.panel.readOnly.description') }}
      </p>
    </template>

    <!-- 2) 功能开关关闭：不查网络，别让运营去查网络。 -->
    <p v-else-if="featureOff" class="sf-tl-rule-section__line" data-sf-tl-rule-feature-off>
      {{ t('admin.forum.trustLevels.conditions.panel.featureOff') }}
    </p>

    <!-- 3) 目录拉取中 -->
    <p v-else-if="loading" class="sf-tl-rule-section__line" data-sf-tl-rule-loading>
      {{ t('admin.forum.trustLevels.conditions.panel.loading') }}
    </p>

    <!-- 4) 失败态：**永不空白**（空白会让运营把「读不到」当「没条件」）。 -->
    <template v-else-if="catalogUnavailable">
      <p class="sf-tl-rule-section__title" data-sf-tl-rule-load-failed>
        <UIcon name="i-lucide-circle-alert" class="sf-tl-rule-section__icon" aria-hidden="true" />
        {{ t('admin.forum.trustLevels.conditions.panel.loadFailed.title') }}
      </p>
      <p class="sf-tl-rule-section__line">
        {{ t('admin.forum.trustLevels.conditions.panel.loadFailed.description') }}
      </p>
      <p v-if="conditionsError" class="sf-tl-rule-section__line" data-sf-tl-rule-load-failed-reason>
        {{ t('admin.forum.trustLevels.conditions.panel.loadFailed.reason', { reason: conditionsError }) }}
      </p>
      <UButton
        type="button"
        color="neutral"
        variant="outline"
        size="xs"
        data-sf-tl-rule-retry
        @click="emit('retry')"
      >
        {{ t('admin.forum.trustLevels.conditions.panel.loadFailed.retry') }}
      </UButton>
    </template>

    <!-- 5) 编辑态 -->
    <template v-else>
      <p class="sf-tl-rule-section__line">
        {{ t('admin.forum.trustLevels.conditions.panel.description') }}
      </p>

      <p v-if="retired" class="sf-tl-rule-section__line" data-sf-tl-rule-retired>
        {{ t('admin.forum.trustLevels.conditions.panel.retired') }}
      </p>

      <!-- 有原值但读不出来：给一行诊断 + 替换入口，不静默当成「没规则」。 -->
      <template v-if="undecodable">
        <p class="sf-tl-rule-section__title" data-sf-tl-rule-undecodable>
          {{ t(`${rulePrefix}.errors.engine.decode_failed`) }}
        </p>
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          size="xs"
          data-sf-tl-rule-add
          @click="addRule"
        >
          {{ t('admin.forum.trustLevels.conditions.toggle.add') }}
        </UButton>
      </template>

      <!-- 没有规则：空态 + 添加条件（一行说明 + 按钮，不包卡片）。 -->
      <template v-else-if="!document">
        <p class="sf-tl-rule-section__line" data-sf-tl-rule-empty>
          {{ t('admin.forum.trustLevels.conditions.panel.empty.description') }}
        </p>
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          size="sm"
          leading-icon="i-lucide-plus"
          data-sf-tl-rule-add
          @click="addRule"
        >
          {{ t('admin.forum.trustLevels.conditions.toggle.add') }}
        </UButton>
      </template>

      <!-- 正常态：共享编辑器（前缀走信任等级族，禁止跨族取词）。 -->
      <SFAdminRuleEditor
        v-else
        :model-value="document"
        :catalog="catalog"
        :fact-candidates="factCandidates"
        :fact-candidates-failed="factCandidatesFailed"
        :ref-candidates="refCandidates"
        i18n-key-prefix="admin.forum.trustLevels.rule"
        :condition-i18n-prefix="trustConditionPrefix"
        :label-family="trustLabelFamily"
        @update:model-value="update"
      />
    </template>
  </div>
</template>

<style scoped>
.sf-tl-rule-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
  /* 面板内部零 overflow：唯一滚动容器是 layouts/admin.vue 的 .sforum-admin-scroll-area。 */
  min-width: 0;
}

.sf-tl-rule-section__title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  color: var(--sf-fg);
  font-size: 12px;
  font-weight: 600;
}

.sf-tl-rule-section__icon {
  width: 14px;
  height: 14px;
  color: var(--sf-fg-secondary);
}

.sf-tl-rule-section__line {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}
</style>
