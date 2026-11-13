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
 *    两者都不会给出任何可选项，对运营的后果完全一样。**但提示文案必须分开**
 *    （`refKindMode` 的三态就是为这句话服务的）：「候选取不到，请直接填写」与
 *    「这一类目前没有候选项」是两件不同的事，把正常状态说成故障会让运营去查后端。
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
 * 某一类引用参数的控件形态，三态：
 * - `select`：有候选，渲染可搜索选择器；
 * - `unavailable`：候选取不到（后端降级 / 老后端 / 未注册的 kind）；
 * - `empty`：取到了，但这一类确实一条候选都没有（例如新站点还没有任何分类）。
 *
 * 渲染只有两叉（后两者都出文本框）——这条读侧契约的理由见文件头；三态存在的唯一目的
 * 是让提示文案说真话。**别把后两者合并回一个布尔值**：那正是上一版的缺陷
 * （「候选暂时读不到」把正常状态说成故障）。
 */
export type BadgeRefMode = 'select' | 'unavailable' | 'empty'

export function refKindMode(map: BadgeRefCandidateMap | undefined, kind: BadgeRefKind | undefined): BadgeRefMode {
  const group = refKindGroup(map, kind)
  if (group.unavailable) return 'unavailable'
  return group.candidates.length === 0 ? 'empty' : 'select'
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
 * 过滤后为空**不补任何占位项**：`refKindMode` 会看到空列表并退成文本输入——
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
  const declared = declaredRefKinds(items)
  if (declared.registered.length === 0 && declared.unknown.length === 0) return false
  return !Array.isArray(raw)
}

/** 目录里声明的 `objectRef` 参数按 `refKind` 去重后的两个桶。 */
export interface DeclaredRefKinds {
  /** 落在 `BADGE_REF_KINDS` 白名单里的声明：这些必须由 `refCandidates` 覆盖。 */
  registered: BadgeRefKind[]
  /** 白名单外的声明（插件把 refKind 拼错 / 用了前端还不认识的类型）。 */
  unknown: string[]
}

/**
 * 从条件目录的 `items[].params[]` 收集声明过的 `refKind`（`fact` 不算：它走
 * `factCandidates`，是另一个面）。
 *
 * 去重 + 按 `BADGE_REF_KINDS` 顺序输出：调用方要拿它跟响应做差集，顺序必须稳定，
 * 否则断言只能比集合、比不了列表。
 */
export function declaredRefKinds(items: readonly BadgeConditionCatalogEntry[]): DeclaredRefKinds {
  const seen = new Set<string>()
  for (const entry of items) {
    for (const param of entry.params ?? []) {
      if (param.kind !== 'objectRef') continue
      const refKind = param.refKind
      if (typeof refKind !== 'string') continue
      const trimmed = refKind.trim()
      if (trimmed === '' || trimmed === 'fact') continue
      seen.add(trimmed)
    }
  }
  const registered = BADGE_REF_KINDS.filter(kind => seen.has(kind))
  const unknown = [...seen].filter(value => !isBadgeRefKind(value)).sort()
  return { registered, unknown }
}

/**
 * 「目录声明了、响应却没给」的 kind 列表 —— 真回归的判据。
 *
 * 三种情况必须区别对待（合并任何一种都会让下一个人误判）：
 * 1. 响应**整体没有** `refCandidates`（老后端）→ 合法，返回 `[]`，静默走文本兜底；
 * 2. 响应有数组但缺了某个目录已声明的 kind → 返回该 kind，调用方给**可见诊断**；
 * 3. 声明的 kind 不在白名单里 → **不**进这个列表（它不可能出现在响应里，报「缺失」
 *    只会误导），由 `declaredRefKinds().unknown` 单独报「类型未注册」。
 */
export function refCandidatesMissingKinds(
  items: readonly BadgeConditionCatalogEntry[],
  raw: unknown
): BadgeRefKind[] {
  if (!Array.isArray(raw)) return []
  const present = new Set<string>()
  for (const row of raw) {
    if (!row || typeof row !== 'object') continue
    const kind = (row as Record<string, unknown>).kind
    if (isBadgeRefKind(kind)) present.add(kind)
  }
  return declaredRefKinds(items).registered.filter(kind => !present.has(kind))
}
