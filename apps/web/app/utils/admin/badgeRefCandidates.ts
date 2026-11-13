/**
 * `objectRef` 参数候选的**读侧纯逻辑**（`GET /admin/badges/conditions` 的顶层
 * `refCandidates`，M3 冻结形状）。
 *
 * 与 `badgeFactCandidates.ts` 同一套纪律，但**降级方向相反**，别照抄：
 *
 * 1. `fact` 候选缺了 = 插件事实下拉没有文案来源，目录仍然完整，所以那侧是
 *    「一条失败提示 + 继续用目录里的值」；
 * 2. `objectRef` 候选（分类 / 标签 / 角色 / 提供方 / 徽章）**唯一来源**就是这个字段。
 *    拿不到候选就没有可选项，此时**必须退成文本输入**——渲染一个只有占位项的空下拉
 *    等于告诉运营「不能选」，而运营真正需要的是「先手填 slug，后端健康检查会校验」。
 *    这条正是线上那个 bug（`candidatesPending` 空下拉，值永远为空，保存必报缺参）。
 * 3. 因此 `unavailable` 与「空列表」在这里按**同一种**处理（都走文本兜底）：
 *    两者都不会给出任何可选项，对运营的后果完全一样。目录侧仍分开记录，
 *    便于后端联调时区分「没取到」和「真的没有」。
 *
 * 失效值（当前值不在候选里）不静默清空：追加一个标记为失效的选项，原值保留可见，
 * 与 `factKeyMissing` 同一条原则。
 */
import type { BadgeConditionCatalogEntry } from './badgeRule'

/** `objectRef` 的候选来源类型；`fact` 不走这里（它在 `factCandidates` 里）。 */
export type BadgeRefKind = 'badge' | 'category' | 'tag' | 'role' | 'provider'

/** 契约固定五项、固定顺序：读侧按它补齐缺失项，顺带当枚举真值。 */
export const BADGE_REF_KINDS: readonly BadgeRefKind[] = ['badge', 'category', 'tag', 'role', 'provider']

export type BadgeRefFlag = 'hidden' | 'disabled'

export interface BadgeRefCandidate {
  value: string
  /** 后端已按请求语言给好文案；空串时显示层回落 `value`。 */
  label: string
  flags: BadgeRefFlag[]
}

export interface BadgeRefCandidateGroup {
  kind: BadgeRefKind
  candidates: BadgeRefCandidate[]
  /** `true` = 取不到候选（**不是**「候选为空」）。两者渲染结果相同，语义不同。 */
  unavailable: boolean
  /** `true` = 后端封顶截断（500 条），需要提示「用搜索缩小范围」。 */
  truncated: boolean
}

/** 五项齐全的映射；整体字段缺失时是 `{}`，读侧按「全部不可用」处理。 */
export type BadgeRefCandidateMap = Partial<Record<BadgeRefKind, BadgeRefCandidateGroup>>

/** 下拉项：值恒为 slug / key 本身，`label` 只负责显示。 */
export interface RefOption {
  value: string
  label: string
  flags: BadgeRefFlag[]
  /** 当前值已不在候选里：保留原值，只加失效标记。 */
  stale: boolean
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function isBadgeRefKind(value: unknown): value is BadgeRefKind {
  return typeof value === 'string' && (BADGE_REF_KINDS as readonly string[]).includes(value)
}

function normalizeFlags(raw: unknown): BadgeRefFlag[] {
  if (!Array.isArray(raw)) return []
  const flags: BadgeRefFlag[] = []
  for (const flag of raw) {
    if (flag === 'hidden' || flag === 'disabled') flags.push(flag)
  }
  return flags
}

function normalizeCandidates(raw: unknown): BadgeRefCandidate[] {
  if (!Array.isArray(raw)) return []
  return raw.flatMap((row) => {
    if (!row || typeof row !== 'object') return []
    const item = row as Record<string, unknown>
    const value = text(item.value)
    // 值为空 = 选了也存不下去（必填参数），当坏数据丢掉，不留一个点了就报错的选项。
    if (value === '') return []
    return [{ value, label: text(item.label), flags: normalizeFlags(item.flags) }]
  })
}

function unavailableGroup(kind: BadgeRefKind): BadgeRefCandidateGroup {
  return { kind, candidates: [], unavailable: true, truncated: false }
}

/**
 * `undefined`（后端还没升级）与任何非数组形状都退成**全项不可用**：`{}`。
 * 返回 `{}` 而不是 `null`，因为这里没有「整块失败」这条通道——每个 kind 自带
 * `unavailable`，读侧只需要一个稳定可迭代的映射。
 *
 * 数组里缺某个 kind 时补一条 `unavailable: true` 的组：契约说固定五项，
 * 缺项只能解释为「这一项取不到」，不能解释为「这一类没有候选」。
 */
export function normalizeRefCandidates(payload: unknown): BadgeRefCandidateMap {
  if (!Array.isArray(payload)) return {}
  const map: BadgeRefCandidateMap = {}
  for (const row of payload) {
    if (!row || typeof row !== 'object') continue
    const item = row as Record<string, unknown>
    const kind = item.kind
    if (!isBadgeRefKind(kind)) continue
    map[kind] = {
      kind,
      candidates: normalizeCandidates(item.candidates),
      unavailable: item.unavailable === true,
      truncated: item.truncated === true
    }
  }
  for (const kind of BADGE_REF_KINDS) {
    if (!map[kind]) map[kind] = unavailableGroup(kind)
  }
  return map
}

/** 取某一类候选；缺失即「不可用」，调用方不必自己判 `undefined`。 */
export function refKindGroup(
  map: BadgeRefCandidateMap | undefined,
  kind: BadgeRefKind | undefined
): BadgeRefCandidateGroup {
  if (!isBadgeRefKind(kind)) return unavailableGroup('badge')
  return map?.[kind] ?? unavailableGroup(kind)
}

/**
 * 是否只能手填：候选取不到、或候选为空。两者都给不出可选项，必须给文本输入——
 * 这是本文件存在的理由，别在这里加「至少渲染一个占位 option」的分支。
 */
export function refKindManual(map: BadgeRefCandidateMap | undefined, kind: BadgeRefKind | undefined): boolean {
  const group = refKindGroup(map, kind)
  return group.unavailable || group.candidates.length === 0
}

/**
 * 下拉项 = 候选 + （必要时）一个失效项。
 *
 * 候选不可用时返回 `[]`：调用方此时已经走文本输入，没有下拉可渲染。
 */
export function refKindItems(
  map: BadgeRefCandidateMap | undefined,
  kind: BadgeRefKind | undefined,
  current: string
): RefOption[] {
  const group = refKindGroup(map, kind)
  if (group.unavailable) return []
  const items: RefOption[] = group.candidates.map(candidate => ({
    value: candidate.value,
    label: candidate.label || candidate.value,
    flags: candidate.flags,
    stale: false
  }))
  const value = text(current)
  if (value !== '' && !items.some(item => item.value === value)) {
    items.push({ value, label: value, flags: [], stale: true })
  }
  return items
}

/**
 * 显示文案 = 标签 + 标记后缀（失效 / 已隐藏 / 已停用）。
 *
 * 标记文案按语言整体下发（中文「（失效）」、英文「 (stale)」），所以这里直接拼接、
 * 不自己加空格：分隔符属于文案，不属于代码。
 */
export function refOptionLabel(
  option: RefOption,
  markers: { stale?: string, hidden?: string, disabled?: string }
): string {
  const suffix = [
    option.stale ? (markers.stale ?? '') : '',
    option.flags.includes('hidden') ? (markers.hidden ?? '') : '',
    option.flags.includes('disabled') ? (markers.disabled ?? '') : ''
  ].join('')
  return `${option.label}${suffix}`
}

/**
 * 去掉 `badge` 类候选里的**自身**：徽章规则不能要求「拥有自己」（自引用永不成立，
 * 而且会绕过授予链路）。其余 kind 原样返回引用。
 *
 * 过滤后为空**不补任何占位项**：`refKindManual` 会看到空列表并退成文本输入——
 * 这正是新建徽章时的正常状态（还没有任何其它徽章可选）。
 */
export function excludeBadgeSlug(
  map: BadgeRefCandidateMap | undefined,
  slug: string
): BadgeRefCandidateMap {
  if (!map) return {}
  const group = map.badge
  const self = text(slug).trim()
  if (!group || self === '') return map
  const candidates = group.candidates.filter(candidate => candidate.value !== self)
  if (candidates.length === group.candidates.length) return map
  return { ...map, badge: { ...group, candidates } }
}

/**
 * 诊断不变量：目录声明了非 `fact` 的 `objectRef` 参数，响应却没给 `refCandidates` 数组。
 *
 * **与 `factCandidatesInvariantBroken` 的关键差异**：那边把命中判成失败
 * （`factCandidatesFailed`，界面上是一条故障提示）；这边**只作诊断，不升级成失败**。
 * 原因：内置条件（`object.authoring` / `badge.owned` / `identity.role` …）从 M1 起就
 * 恒带 `objectRef` 参数，所以后端没升级时这条不变量**必然**命中——把它当故障会让 M3
 * 并行上线期间每次打开弹窗都报一条假故障。真正的用户体验由每个 kind 的 `unavailable`
 * 承担：照常退成文本输入，保存不受影响。
 */
export function refCandidatesInvariantBroken(
  items: readonly BadgeConditionCatalogEntry[],
  raw: unknown
): boolean {
  const declaresObjectRef = items.some(entry =>
    (entry.params ?? []).some(param => param.kind === 'objectRef' && param.refKind !== 'fact')
  )
  if (!declaresObjectRef) return false
  if (Array.isArray(raw)) return false
  return true
}
