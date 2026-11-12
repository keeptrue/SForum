<script setup lang="ts">
/**
 * 徽章规则编辑器：整棵条件树的入口。
 *
 * 受控组件：`modelValue` 进、`update:modelValue` 出新文档；组件内部没有可变副本，
 * 所有增删改都由子节点用 `badgeRule.ts` 的不可变原语算完再逐层 emit 上来。
 * 目录（条件清单 + 参数形状）由宿主注入，本组件不取数。
 */
import { isGroupNode, type BadgeConditionCatalogEntry, type BadgeFactCandidate, type BadgeRule } from '~/utils/admin/badgeRule'
import SFAdminBadgeRuleGroup from '~/components/admin/badges/rule/SFAdminBadgeRuleGroup.vue'
import SFAdminBadgeRuleLeaf from '~/components/admin/badges/rule/SFAdminBadgeRuleLeaf.vue'
import SFAdminBadgeRuleJsonPreview from '~/components/admin/badges/rule/SFAdminBadgeRuleJsonPreview.vue'

const props = defineProps<{
  modelValue: BadgeRule
  catalog: BadgeConditionCatalogEntry[]
  /** 插件事实候选：只有 fact.count 叶子消费，但树上每一层都要往下带到它。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [value: BadgeRule] }>()

const { t } = useI18n()

const rootPath: number[] = []
const rootGroup = computed(() => (isGroupNode(props.modelValue.root) ? props.modelValue.root : null))
const rootLeaf = computed(() => (isGroupNode(props.modelValue.root) ? null : props.modelValue.root))

function update(next: BadgeRule) {
  emit('update:modelValue', next)
}
</script>

<template>
  <section class="sf-rule-editor" data-sf-badge-rule-editor :aria-label="t('admin.badges.rule.title')">
    <p v-if="catalog.length === 0" class="sf-rule-editor__notice">
      {{ t('admin.badges.conditions.empty') }}
    </p>

    <SFAdminBadgeRuleGroup
      v-else-if="rootGroup"
      :rule="modelValue"
      :node="rootGroup"
      :path="rootPath"
      :depth="1"
      :catalog="catalog"
      :fact-candidates="factCandidates ?? []"
      :fact-candidates-failed="factCandidatesFailed === true"
      @update:rule="update"
    />
    <SFAdminBadgeRuleLeaf
      v-else-if="rootLeaf"
      :rule="modelValue"
      :node="rootLeaf"
      :path="rootPath"
      :depth="1"
      :catalog="catalog"
      :fact-candidates="factCandidates ?? []"
      :fact-candidates-failed="factCandidatesFailed === true"
      @update:rule="update"
    />

    <SFAdminBadgeRuleJsonPreview :rule="modelValue" />
  </section>
</template>

<style scoped>
.sf-rule-editor {
  display: flex;
  flex-direction: column;
  box-sizing: border-box;
  min-width: 0;
}

.sf-rule-editor__notice {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
}
</style>
