<script setup lang="ts">
/**
 * 规则树的条件组（`all` / `any` / `not`），递归渲染自身与叶子。
 *
 * 每个节点都持有整份 `rule` 与自己的 `path`，所以增删改直接用 `badgeRule.ts` 的
 * 不可变原语算出新文档再向上 emit —— 组件里不拼 JSON，也不做本地可变状态。
 *
 * 两处 UI 层护栏：
 * 1. 深度 5 / 叶子 32 / 节点 64 到顶时，添加按钮 `disabled` + `title` 说明原因；
 * 2. `not` 没有「添加子节点」入口（它只允许 1 个子节点，多出来的必然 422），
 *    删除 `not` 的唯一子节点时连 `not` 一起移除，否则会留下一个永远修不好的空 `not`。
 */
import {
  BADGE_RULE_LIMITS,
  createDefaultLeaf,
  createGroup,
  insertChild,
  isGroupNode,
  removeNode,
  replaceNode,
  ruleStats,
  wrapWithGroup,
  type BadgeConditionCatalogEntry,
  type BadgeFactCandidate,
  type BadgeRule,
  type BadgeRuleGroup,
  type BadgeRuleLeaf,
  type BadgeRuleOp
} from '~/utils/admin/badgeRule'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import SFAdminBadgeRuleLeaf from '~/components/admin/badges/rule/SFAdminBadgeRuleLeaf.vue'

const props = defineProps<{
  rule: BadgeRule
  node: BadgeRuleGroup
  path: number[]
  depth: number
  catalog: BadgeConditionCatalogEntry[]
  /** 插件事实候选：递归往下带，直到 fact.count 叶子。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /** objectRef 参数候选：递归往下带，直到参数控件。 */
  refCandidates?: BadgeRefCandidateMap
}>()

const emit = defineEmits<{ 'update:rule': [rule: BadgeRule] }>()

const { t } = useI18n()

const isNot = computed(() => props.node.op === 'not')
const stats = computed(() => ruleStats(props.rule))

/** 到顶原因只给第一个命中的上限，`title` 一句话说清，不堆三个数字。 */
const limitReason = computed(() => {
  if (props.depth + 1 > BADGE_RULE_LIMITS.maxDepth) {
    return t('admin.badges.rule.limitDepth', { max: BADGE_RULE_LIMITS.maxDepth })
  }
  if (stats.value.leaves >= BADGE_RULE_LIMITS.maxLeaves) {
    return t('admin.badges.rule.limitLeaves', { max: BADGE_RULE_LIMITS.maxLeaves })
  }
  if (stats.value.nodes >= BADGE_RULE_LIMITS.maxNodes) {
    return t('admin.badges.rule.limitNodes', { max: BADGE_RULE_LIMITS.maxNodes })
  }
  return ''
})

const canAdd = computed(() => limitReason.value === '')

/** 默认条件固定 `activity.count`：唯一零参数歧义的内置条件。 */
const defaultEntry = computed(() => props.catalog.find(item => item.key === 'activity.count') ?? props.catalog[0] ?? null)

type Row = { index: number, group: BadgeRuleGroup | null, leaf: BadgeRuleLeaf | null }

const rows = computed<Row[]>(() => props.node.children.map((child, index) => (
  isGroupNode(child)
    ? { index, group: child, leaf: null }
    : { index, group: null, leaf: child }
)))

function update(next: BadgeRule) {
  emit('update:rule', next)
}

function addLeaf() {
  const entry = defaultEntry.value
  if (!entry || !canAdd.value) return
  update(insertChild(props.rule, props.path, createDefaultLeaf(entry)))
}

function addGroup() {
  const entry = defaultEntry.value
  if (!entry || !canAdd.value) return
  update(insertChild(props.rule, props.path, createGroup('all', [createDefaultLeaf(entry)])))
}

function setOp(event: Event) {
  const op = (event.target as HTMLSelectElement).value as BadgeRuleOp
  // `not` 只接受 1 个子节点；多子节点时下拉里这一项是 disabled，这里再兜一次。
  if (op === 'not' && props.node.children.length !== 1) return
  update(replaceNode(props.rule, props.path, { op, children: props.node.children }))
}

function removeChild(index: number) {
  if (isNot.value && props.node.children.length === 1) {
    if (props.path.length === 0) {
      // 根就是 `not`：没有父组可删，退化成空 `all` 草稿，让用户重新组织。
      update(replaceNode(props.rule, [], createGroup('all', [])))
      return
    }
    update(removeNode(props.rule, props.path))
    return
  }
  update(removeNode(props.rule, [...props.path, index]))
}

function wrapChild(index: number) {
  update(wrapWithGroup(props.rule, [...props.path, index], 'not'))
}

function unwrap() {
  const only = props.node.children[0]
  if (!isNot.value || props.node.children.length !== 1 || !only) return
  update(replaceNode(props.rule, props.path, only))
}

function opLabel(op: BadgeRuleOp) {
  return t(`admin.badges.rule.ops.${op}`)
}
</script>

<template>
  <div class="sf-rule-group" :data-rule-group="node.op" :data-depth="depth">
    <div class="sf-rule-group__head">
      <label class="sf-rule-group__op">
        <span class="sf-rule-group__label">{{ t('admin.badges.rule.groupOp') }}</span>
        <select class="sf-rule-group__select" data-rule-group-op :value="node.op" @change="setOp">
          <option value="all">{{ opLabel('all') }}</option>
          <option value="any">{{ opLabel('any') }}</option>
          <option
            value="not"
            :disabled="!isNot && node.children.length !== 1"
            :title="!isNot && node.children.length !== 1 ? t('admin.badges.rule.notNeedsOneChild') : undefined"
          >
            {{ opLabel('not') }}
          </option>
        </select>
      </label>

      <div class="sf-rule-group__actions">
        <button
          v-if="isNot && node.children.length === 1"
          type="button"
          class="sf-rule-action"
          :aria-label="t('admin.badges.rule.unwrap')"
          :title="t('admin.badges.rule.unwrap')"
          @click="unwrap"
        >
          <UIcon name="i-lucide-undo-2" aria-hidden="true" />
        </button>
        <template v-if="!isNot">
          <button
            type="button"
            class="sf-rule-add"
            data-rule-add-condition
            :aria-label="t('admin.badges.rule.addCondition')"
            :title="canAdd ? t('admin.badges.rule.addCondition') : limitReason"
            :disabled="!canAdd"
            @click="addLeaf"
          >
            <UIcon name="i-lucide-plus" aria-hidden="true" />
            <span>{{ t('admin.badges.rule.addCondition') }}</span>
          </button>
          <button
            type="button"
            class="sf-rule-add"
            data-rule-add-group
            :aria-label="t('admin.badges.rule.addGroup')"
            :title="canAdd ? t('admin.badges.rule.addGroup') : limitReason"
            :disabled="!canAdd"
            @click="addGroup"
          >
            <UIcon name="i-lucide-brackets" aria-hidden="true" />
            <span>{{ t('admin.badges.rule.addGroup') }}</span>
          </button>
        </template>
      </div>
    </div>

    <p v-if="node.children.length === 0" class="sf-rule-group__empty">
      {{ t('admin.badges.rule.emptyGroup') }}
    </p>

    <ul v-else class="sf-rule-group__children">
      <li v-for="row in rows" :key="row.index" class="sf-rule-node" :data-rule-node="row.index">
        <div class="sf-rule-node__actions">
          <button
            type="button"
            class="sf-rule-action"
            data-rule-wrap-not
            :aria-label="t('admin.badges.rule.wrapNot')"
            :title="t('admin.badges.rule.wrapNot')"
            @click="wrapChild(row.index)"
          >
            <UIcon name="i-lucide-ban" aria-hidden="true" />
          </button>
          <button
            type="button"
            class="sf-rule-action"
            data-rule-remove
            :aria-label="t('admin.badges.rule.remove')"
            :title="t('admin.badges.rule.remove')"
            @click="removeChild(row.index)"
          >
            <UIcon name="i-lucide-trash-2" aria-hidden="true" />
          </button>
        </div>

        <SFAdminBadgeRuleGroup
          v-if="row.group"
          :rule="rule"
          :node="row.group"
          :path="[...path, row.index]"
          :depth="depth + 1"
          :catalog="catalog"
        :fact-candidates="factCandidates ?? []"
        :fact-candidates-failed="factCandidatesFailed === true"
        :ref-candidates="refCandidates ?? {}"
          @update:rule="update"
        />
        <SFAdminBadgeRuleLeaf
          v-else-if="row.leaf"
          :rule="rule"
          :node="row.leaf"
          :path="[...path, row.index]"
          :depth="depth + 1"
          :catalog="catalog"
        :fact-candidates="factCandidates ?? []"
        :fact-candidates-failed="factCandidatesFailed === true"
        :ref-candidates="refCandidates ?? {}"
          @update:rule="update"
        />
      </li>
    </ul>
  </div>
</template>

<style scoped>
.sf-rule-group {
  display: flex;
  flex-direction: column;
  gap: 8px;
  box-sizing: border-box;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid var(--sf-border);
  border-radius: 12px;
  background: var(--sf-muted);
}

.sf-rule-group__head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px;
  min-width: 0;
}

.sf-rule-group__op {
  display: flex;
  flex: 1 1 140px;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sf-rule-group__label {
  color: var(--sf-fg-tertiary);
  font-size: 11px;
}

.sf-rule-group__select {
  box-sizing: border-box;
  width: 100%;
  min-width: 0;
  padding: 4px 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-bg);
  color: var(--sf-fg);
  font-size: 13px;
}

.sf-rule-group__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.sf-rule-add,
.sf-rule-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  box-sizing: border-box;
  min-width: 28px;
  min-height: 28px;
  padding: 4px 8px;
  border: 1px solid var(--sf-border);
  border-radius: 8px;
  background: var(--sf-card);
  color: var(--sf-fg-secondary);
  font-size: 12px;
}

.sf-rule-add:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.sf-rule-add:focus-visible,
.sf-rule-action:focus-visible,
.sf-rule-group__select:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 1px;
}

.sf-rule-group__empty {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
}

.sf-rule-group__children {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.sf-rule-node {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}

.sf-rule-node__actions {
  display: flex;
  justify-content: flex-end;
  gap: 6px;
}

/* 390 档：按钮只留图标（aria-label/title 仍在），避免把参数行挤成两行以上。 */
@media (max-width: 480px) {
  .sf-rule-add span {
    display: none;
  }
}
</style>
