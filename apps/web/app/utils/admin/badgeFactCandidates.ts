/**
 * 插件事实候选的**读侧纯逻辑**（`GET /admin/badges/conditions` 的顶层 `factCandidates`）。
 *
 * 三条不可让步的语义（M3.1 冻结）：
 * 1. 下拉项的**值**永远是 `factKey` / `variant` 本身，插件声明只负责**显示文案**；
 * 2. `variants` 为空**不等于**「空下拉」——编辑器整项隐藏，并把 `variant` 置空串；
 * 3. 旧规则里的 `factKey` 若已不在候选里（扩展停用/卸载），原值必须保留可见，
 *    **不许静默清空**：清空等于替运营删掉了规则的一半。
 */
import type {
  BadgeConditionCatalogEntry,
  BadgeConditionParam,
  BadgeFactCandidate,
  BadgeRuleLeaf
} from './badgeRule'

export interface FactOption {
  value: string
  label: string
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/**
 * 双语字段两种来路都要认：宿主扁平化后的 `labelZh/labelEn`，以及清单原样的
 * `label: {zh, en}`。只认一种会让另一侧的文案静默变成空串。
 */
function bilingual(raw: Record<string, unknown>, nested: string, flatZh: string, flatEn: string) {
  const node = raw[nested]
  const object = node && typeof node === 'object' ? (node as Record<string, unknown>) : {}
  return {
    zh: text(object.zh) || text(object['zh-CN']) || text(raw[flatZh]),
    en: text(object.en) || text(object['en-US']) || text(raw[flatEn])
  }
}

function normalizeCandidate(row: unknown): BadgeFactCandidate | null {
  if (!row || typeof row !== 'object') return null
  const raw = row as Record<string, unknown>
  const key = text(raw.key)
  if (key === '') return null
  const label = bilingual(raw, 'label', 'labelZh', 'labelEn')
  const unit = bilingual(raw, 'unit', 'unitZh', 'unitEn')
  const compare = Array.isArray(raw.compare)
    ? raw.compare.filter((value): value is string => typeof value === 'string')
    : []
  const variants = Array.isArray(raw.variants)
    ? raw.variants.flatMap((variant) => {
      if (!variant || typeof variant !== 'object') return []
      const item = variant as Record<string, unknown>
      const value = text(item.value)
      if (value === '') return []
      const copy = bilingual(item, 'label', 'labelZh', 'labelEn')
      return [{ value, labelZh: copy.zh, labelEn: copy.en }]
    })
    : []
  return {
    key,
    labelZh: label.zh,
    labelEn: label.en,
    unitZh: unit.zh,
    unitEn: unit.en,
    compare,
    variants
  }
}

/**
 * `undefined`（后端还没升级）= 正常空候选，**不算失败**：M3 并行上线期间不该天天报警。
 * 给了但不是数组、或整体不可用时返回 `null`，由调用方降级成 `[]` + 一条可见提示。
 */
export function normalizeFactCandidates(payload: unknown): BadgeFactCandidate[] | null {
  if (payload === undefined) return []
  if (!Array.isArray(payload)) return null
  return payload.flatMap((row) => {
    const candidate = normalizeCandidate(row)
    return candidate ? [candidate] : []
  })
}

/** 显示文案：当前语言 → 另一语言 → 回退（通常是 key）。两侧都空才回 key。 */
export function factCandidateLabel(candidate: BadgeFactCandidate, zh: boolean, fallback: string): string {
  const primary = zh ? candidate.labelZh : candidate.labelEn
  const secondary = zh ? candidate.labelEn : candidate.labelZh
  return primary || secondary || fallback || candidate.key
}

function pickCandidate(candidates: readonly BadgeFactCandidate[], key: string): BadgeFactCandidate | null {
  if (key === '') return null
  return candidates.find(candidate => candidate.key === key) ?? null
}

/** `factKey` 下拉：候选值来自目录 `options`，显示文案来自 `factCandidates`（缺则回 key）。 */
export function factKeyOptions(
  values: readonly string[],
  candidates: readonly BadgeFactCandidate[],
  zh: boolean
): FactOption[] {
  return values.map((value) => {
    const candidate = pickCandidate(candidates, value)
    return { value, label: candidate ? factCandidateLabel(candidate, zh, value) : value }
  })
}

/**
 * `variant` 下拉：只返回**当前 factKey 的**变体。未知 key（扩展已卸载）返回 `[]`，
 * 调用方据此隐藏整项而不是渲染一个空下拉。
 */
export function factVariantOptions(
  candidates: readonly BadgeFactCandidate[],
  key: string,
  zh: boolean
): FactOption[] {
  const candidate = pickCandidate(candidates, key)
  if (!candidate) return []
  return candidate.variants.map(variant => ({
    value: variant.value,
    label: (zh ? variant.labelZh : variant.labelEn) || (zh ? variant.labelEn : variant.labelZh) || variant.value
  }))
}

/** 事实计数的单位（「次」/「times」），用于比较值后面；没有就不显示。 */
export function factCandidateUnit(candidates: readonly BadgeFactCandidate[], key: string, zh: boolean): string {
  const candidate = pickCandidate(candidates, key)
  if (!candidate) return ''
  return (zh ? candidate.unitZh : candidate.unitEn) || (zh ? candidate.unitEn : candidate.unitZh) || ''
}

/** 旧规则引用了已不存在的 `factKey`：保留原值，只加失效标记。 */
export function factKeyMissing(candidates: readonly BadgeFactCandidate[], key: string): boolean {
  return key !== '' && !candidates.some(candidate => candidate.key === key)
}

export function isFactCountParam(spec: BadgeConditionParam, entry: BadgeConditionCatalogEntry | null): boolean {
  return entry?.key === 'fact.count' && spec.name === 'factKey'
}

/**
 * 选完 `factKey` 后归一 `variant`：新 key 没有变体（或旧变体不属于它）时置空串。
 *
 * 这是**用户动作**的归一，不做渲染期副作用：在渲染里 emit 会让「打开弹窗」变成一次
 * 隐式写操作，草稿的 touched 状态也会被莫名其妙点亮。
 */
export function applyFactKeySelection(
  params: Record<string, unknown> | undefined,
  candidates: readonly BadgeFactCandidate[],
  key: string,
  zh: boolean
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...(params ?? {}), factKey: key }
  const variants = factVariantOptions(candidates, key, zh)
  const current = typeof next.variant === 'string' ? next.variant : ''
  if (variants.length === 0) {
    next.variant = ''
    return next
  }
  if (current !== '' && !variants.some(option => option.value === current)) next.variant = ''
  return next
}

/** 编辑器只有在这个节点是事实计数时才需要候选；其余叶子不读候选。 */
export function isFactCountLeaf(node: BadgeRuleLeaf): boolean {
  return node.type === 'fact.count'
}
