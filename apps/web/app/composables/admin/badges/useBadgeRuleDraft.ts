import { computed, ref, type Ref } from 'vue'
import {
  createDefaultRule,
  createGroup,
  validateRuleDraft,
  type BadgeConditionCatalogEntry,
  type BadgeRule,
  type BadgeRuleError
} from '~/utils/admin/badgeRule'
import type { AdminBadgeRow } from '~/utils/admin/adminBadges'

/**
 * 徽章规则草稿：弹窗里「规则」那一块的全部状态与判定。
 *
 * 从 `SFAdminBadgeEditDialog` 抽出来是因为弹窗已经贴到 500 行复核线，而 M1.4 还要往里加
 * 影响确认。这里只管三件事：草稿、提交三态、保存前校验；i18n 文案由调用方通过
 * `errorText` 注入（composable 不依赖 Nuxt 自动导入，便于单测）。
 *
 * 三态（PATCH 是 merge 语义）：
 * - `undefined` = **不改**（键都不出现）：手动徽章、目录未接线、或 automatic 且没动过规则；
 * - `null` = 清空（契约支持，当前 UI 不产生：automatic 必须有规则，manual 由后端收敛）；
 * - 对象 = 替换。
 */
export interface BadgeRuleDraftOptions {
  catalog: Ref<BadgeConditionCatalogEntry[]>
  automatic: Ref<boolean>
  badge: Ref<AdminBadgeRow | null>
  /** 弹窗的字段错误表；`rule` 键由本 composable 独占写入。 */
  fieldErrors: Ref<Record<string, string[]>>
  /** 错误码 → 文案，由视图层提供（i18n 只属于视图层）。 */
  errorText: (error: BadgeRuleError) => string
}

export function useBadgeRuleDraft(options: BadgeRuleDraftOptions) {
  const draftRule = ref<BadgeRule | null>(null)
  const touched = ref(false)
  /**
   * 服务端这条 automatic 徽章**没有规则**（`rule` 为 null，或列表还没带这个字段）。
   * 不自动补默认规则：自动补会掩盖脏数据，让运营以为规则一直都在。
   */
  const missing = ref(false)

  /** 目录是否已接线；M1.4 之前页面还没传 catalog，此时保持旧口径（只发 metric/threshold）。 */
  const ready = computed(() => options.catalog.value.length > 0)

  const defaultEntry = computed(
    () => options.catalog.value.find(entry => entry.key === 'activity.count') ?? options.catalog.value[0] ?? null
  )

  function clearErrors() {
    if (!options.fieldErrors.value.rule) return
    const next = { ...options.fieldErrors.value }
    delete next.rule
    options.fieldErrors.value = next
  }

  function setErrors(messages: string[]) {
    options.fieldErrors.value = { ...options.fieldErrors.value, rule: messages }
  }

  /** 打开弹窗时重置：有规则就加载；automatic 缺规则就渲染空树并标记 `missing`。 */
  function reset() {
    const rule = options.badge.value?.rule
    draftRule.value = rule ?? null
    touched.value = false
    missing.value = false
    clearErrors()
    if (!options.automatic.value || !ready.value) return
    if (rule && typeof rule === 'object') return
    draftRule.value = { v: 1, root: createGroup('all', []) }
    missing.value = true
  }

  /** 只在「手动 → 自动」时补默认规则；打开既有 automatic 徽章不调用。 */
  function ensureRule() {
    if (draftRule.value) return
    const entry = defaultEntry.value
    if (!entry) return
    draftRule.value = createDefaultRule(entry)
    missing.value = false
  }

  function onUpdate(next: BadgeRule) {
    draftRule.value = next
    touched.value = true
    missing.value = false
    clearErrors()
  }

  function payload(): BadgeRule | null | undefined {
    if (!options.automatic.value || !ready.value) return undefined
    if (options.badge.value?.grantMode === 'automatic' && !touched.value) return undefined
    return draftRule.value
  }

  /**
   * 保存前校验。只在「这次真的会发规则」时拦：
   * - 没动规则且不补规则 → 不发 → 不校验（M3 卸载插件后仍能改徽章名字）；
   * - 服务端缺规则的脏数据 → 校验（空树会被拒，别让运营以为保存成功了）。
   */
  function validate(): boolean {
    if (!options.automatic.value || !ready.value) {
      clearErrors()
      return true
    }
    if (payload() === undefined && !missing.value) {
      clearErrors()
      return true
    }
    if (!draftRule.value) {
      draftRule.value = { v: 1, root: createGroup('all', []) }
    }
    const result = validateRuleDraft(draftRule.value, options.catalog.value)
    if (result.ok) {
      clearErrors()
      return true
    }
    setErrors(result.errors.map(error => options.errorText(error)))
    return false
  }

  return {
    draftRule,
    touched,
    missing,
    ready,
    defaultEntry,
    reset,
    ensureRule,
    onUpdate,
    payload,
    validate,
    clearErrors
  }
}
