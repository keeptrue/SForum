/**
 * 个人资料设置的分区/tab 契约与纯函数。
 *
 * 服务端 `GET /profile` 把「资料字段分区」当成数据下发（`settingsSections`）：
 * 核心三项（身份 / 介绍 / 地点与链接）固定由前端声明，扩展分区按插件顺序追加。
 * tab 合成、`?tab=` 归一化、字段错误 → 分区映射都放在这里，组件只负责渲染，
 * 这样这些规则可以用 bun test 直接覆盖，不必挂载页面。
 */

export const PROFILE_SETTINGS_CORE_TABS = ['identity', 'story', 'links'] as const
export type ProfileSettingsCoreTab = (typeof PROFILE_SETTINGS_CORE_TABS)[number]
export const PROFILE_SETTINGS_DEFAULT_TAB: ProfileSettingsCoreTab = 'identity'

/** tab / panel DOM id 的唯一拼接规则：nav 的 aria-controls 与 panel 的 id 共用。 */
export const PROFILE_SETTINGS_ID_PREFIX = 'profile-settings'

export function profileSettingsTabDomId(tabId: string): string {
  return `${PROFILE_SETTINGS_ID_PREFIX}-tab-${tabId}`
}

export function profileSettingsPanelDomId(tabId: string): string {
  return `${PROFILE_SETTINGS_ID_PREFIX}-panel-${tabId}`
}

/** 核心资料字段 → 所属分区；服务端字段错误按 fieldKey 回来，必须能落到某个 tab。 */
const CORE_FIELD_TABS: Record<string, ProfileSettingsCoreTab> = {
  displayName: 'identity',
  bio: 'story',
  signature: 'story',
  location: 'links',
  websiteUrl: 'links'
}

export type ProfileSettingsValue = string | number | boolean
/** 空串表示「用户清空了该字段」，提交时折算成 null。 */
export type ProfileSettingsDraftValue = ProfileSettingsValue | ''
export type ProfileSettingsFieldType = 'string' | 'text' | 'number' | 'boolean' | 'select'

export type ProfileSettingsFieldOption = {
  value: string
  label: string
}

export type ProfileSettingsFieldConstraints = {
  maxLength?: number
  min?: number
  max?: number
}

export type ProfileSettingsField = {
  fieldKey: string
  label: string
  description?: string
  type: ProfileSettingsFieldType
  required?: boolean
  placeholder?: string
  options?: ProfileSettingsFieldOption[]
  default?: ProfileSettingsValue
  constraints?: ProfileSettingsFieldConstraints
}

export type ProfileSettingsSection = {
  id: string
  label: string
  description?: string
  icon?: string
  source: 'core' | 'extension'
  extensionId?: string
  type: 'fields' | 'component'
  fields?: ProfileSettingsField[]
  componentId?: string
}

/** 公开资料页「关于」展示的自定义字段（服务端已按可见性过滤）。 */
export type ProfileMetaEntry = {
  /**
   * 字段标签。后端按 LocalizedText 序列化：没有多语言时是字符串，
   * 有多语言时是 `{ 'zh-CN': ..., 'en-US': ... }` —— 与 settingsSections 的
   * label 同一形态，必须走 resolveLocalizedText，否则会退化成 fieldKey。
   */
  label: LocalizedInput
  value: string
}

export type ProfileSettingsTabView = {
  id: string
  label: string
  description?: string
  icon?: string
  source: 'core' | 'extension'
  type: 'fields' | 'component'
  extensionId?: string
  componentId?: string
  fields: ProfileSettingsField[]
}

export type ProfileMetaRow = {
  fieldKey: string
  label: string
  value: string
}

/** 线上下发的展示文案：可能是纯字符串，也可能是 locale → 文案的映射。 */
export type LocalizedInput = string | Record<string, string> | null | undefined

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

/**
 * 解析展示文案。后端 LocalizedText 在没有多语言时序列化成字符串、有映射时
 * 序列化成 `{ 'zh-CN': ... }`，所以两种形态都要认：先按当前 locale 精确匹配，
 * 再按语言前缀，最后退到 en-US / 任意非空值。
 */
export function resolveLocalizedText(value: LocalizedInput, locale = '', fallback = ''): string {
  if (typeof value === 'string') {
    return value.trim() || fallback
  }
  if (!isRecord(value)) {
    return fallback
  }
  const map = value as Record<string, string>
  const candidates = [locale, locale.split('-')[0] || '', 'en-US', 'en', 'zh-CN', 'zh']
  for (const key of candidates) {
    const hit = typeof map[key] === 'string' ? map[key].trim() : ''
    if (hit) return hit
  }
  for (const entry of Object.values(map)) {
    const hit = typeof entry === 'string' ? entry.trim() : ''
    if (hit) return hit
  }
  return fallback
}

function coerceBoolean(value: unknown): boolean {
  if (typeof value === 'boolean') return value
  if (typeof value === 'number') return value !== 0
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase()
    return normalized === 'true' || normalized === '1' || normalized === 'yes' || normalized === 'on'
  }
  return false
}

function normalizeFieldType(value: unknown): ProfileSettingsFieldType {
  switch (value) {
    case 'text':
    case 'number':
    case 'boolean':
    case 'select':
      return value
    default:
      return 'string'
  }
}

/**
 * 归一化一个声明式字段。线上字段键是 `key`（manifest 契约），值/错误用的键是
 * `fieldKey`；两种写法都收，展示文案按 locale 解析成字符串。
 */
function normalizeSettingsField(raw: Record<string, unknown>, locale: string): ProfileSettingsField | null {
  const fieldKey = String(raw.fieldKey ?? raw.key ?? '').trim()
  if (!fieldKey) return null
  const label = resolveLocalizedText(raw.label as LocalizedInput, locale, fieldKey)
  const options = Array.isArray(raw.options)
    ? raw.options.flatMap((option) => {
      if (!isRecord(option)) return []
      const optionValue = String(option.value ?? '').trim()
      if (!optionValue) return []
      return [{
        value: optionValue,
        label: resolveLocalizedText(option.label as LocalizedInput, locale, optionValue)
      }]
    })
    : undefined
  const constraints = isRecord(raw.constraints) ? raw.constraints : undefined
  return {
    fieldKey,
    label,
    description: resolveLocalizedText(raw.description as LocalizedInput, locale) || undefined,
    type: normalizeFieldType(raw.type),
    required: raw.required === true,
    placeholder: resolveLocalizedText(raw.placeholder as LocalizedInput, locale) || undefined,
    options,
    default: typeof raw.default === 'string' || typeof raw.default === 'number' || typeof raw.default === 'boolean'
      ? raw.default
      : undefined,
    constraints: constraints
      ? {
          maxLength: Number.isFinite(Number(constraints.maxLength)) ? Number(constraints.maxLength) : undefined,
          min: Number.isFinite(Number(constraints.min)) ? Number(constraints.min) : undefined,
          max: Number.isFinite(Number(constraints.max)) ? Number(constraints.max) : undefined
        }
      : undefined
  }
}

/** 归一化一个设置分区；不可用的条目返回 null，由合成阶段丢弃。 */
export function normalizeProfileSettingsSection(raw: unknown, locale = ''): ProfileSettingsSection | null {
  if (!isRecord(raw)) return null
  const id = String(raw.id ?? '').trim()
  const label = resolveLocalizedText(raw.label as LocalizedInput, locale)
  if (!id || !label) return null
  const extensionId = String(raw.extensionId ?? '').trim()
  // 个人设置面只允许 fields / component（admin.settings.tabs 才允许 settings），
  // 因此除 component 外一律按宿主渲染的 fields 处理。
  const type = raw.type === 'component' ? 'component' : 'fields'
  // 字段顺序按 effectiveSortOrder（读模型名）排：站点治理关掉/调序某个字段后，
  // 线上顺序才是真的顺序，不能靠数组天然顺序碰运气。
  const fields = Array.isArray(raw.fields)
    ? raw.fields
        .flatMap((entry, index) => {
          const field = isRecord(entry) ? normalizeSettingsField(entry, locale) : null
          if (!field) return []
          const order = isRecord(entry) && Number.isFinite(Number(entry.effectiveSortOrder))
            ? Number(entry.effectiveSortOrder)
            : index
          return [{ field, order, index }]
        })
        .sort((left, right) => (left.order - right.order) || (left.index - right.index))
        .map(entry => entry.field)
    : []
  return {
    id,
    label,
    description: resolveLocalizedText(raw.description as LocalizedInput, locale) || undefined,
    icon: String(raw.icon ?? '').trim() || undefined,
    source: raw.source === 'core' ? 'core' : (extensionId ? 'extension' : 'core'),
    extensionId: extensionId || undefined,
    type,
    fields,
    componentId: String(raw.componentId ?? '').trim() || undefined
  }
}

export function normalizeProfileSettingsSections(raw: unknown, locale = ''): ProfileSettingsSection[] {
  if (!Array.isArray(raw)) return []
  return raw.flatMap(entry => {
    const section = normalizeProfileSettingsSection(entry, locale)
    return section ? [section] : []
  })
}

function normalizeString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

/**
 * 合成 tab 顺序：核心三项在前，扩展分区按服务端顺序追加。
 * 缺 label / 缺 id / id 重复 / 组件型分区缺 componentId 的条目直接丢，
 * 不渲染「点进去什么也没有」的 tab（也不做 disabled tab）。
 */
export function composeProfileSettingsTabs(
  sections: ProfileSettingsSection[] | undefined,
  coreLabels: Record<ProfileSettingsCoreTab, string>
): ProfileSettingsTabView[] {
  const tabs: ProfileSettingsTabView[] = PROFILE_SETTINGS_CORE_TABS.map(id => ({
    id,
    label: coreLabels[id],
    source: 'core' as const,
    type: 'fields' as const,
    fields: []
  }))
  const seen = new Set(tabs.map(tab => tab.id))
  for (const section of sections || []) {
    const id = normalizeString(section?.id).trim()
    const label = normalizeString(section?.label).trim()
    if (!id || !label || seen.has(id)) continue
    if (section.type === 'component' && !normalizeString(section.componentId).trim()) continue
    // 字段分区若一个字段都没有（权限过滤后可能剩下空壳），等于一个空 tab：不渲染。
    const fields = section.type === 'component'
      ? []
      : (section.fields || []).filter(field => Boolean(field?.fieldKey))
    if (section.type !== 'component' && fields.length === 0) continue
    seen.add(id)
    tabs.push({
      id,
      label,
      description: normalizeString(section.description).trim() || undefined,
      icon: normalizeString(section.icon).trim() || undefined,
      source: section.source === 'extension' ? 'extension' : 'core',
      type: section.type === 'component' ? 'component' : 'fields',
      extensionId: normalizeString(section.extensionId).trim() || undefined,
      componentId: normalizeString(section.componentId).trim() || undefined,
      fields: section.type === 'component' ? [] : (section.fields || []).filter(field => Boolean(field?.fieldKey))
    })
  }
  return tabs
}

/** fieldKey → 分区 id。扩展字段先看自己所在分区，核心字段走固定表。 */
export function profileSettingsFieldTabMap(sections: ProfileSettingsSection[] | undefined): Record<string, string> {
  const map: Record<string, string> = { ...CORE_FIELD_TABS }
  for (const section of sections || []) {
    if (section?.type === 'component' || !normalizeString(section?.id).trim()) continue
    for (const field of section.fields || []) {
      const fieldKey = normalizeString(field?.fieldKey).trim()
      if (!fieldKey) continue
      map[fieldKey] = section.id
    }
  }
  return map
}

export function profileSettingsTabForField(fieldKey: string, sections?: ProfileSettingsSection[]): string {
  return profileSettingsFieldTabMap(sections)[fieldKey] || ''
}

/**
 * 字段错误 → 每个分区有几处待修正，外加「不属于任何分区」的全局桶。
 *
 * 核心字段的键由服务端直接用资料字段名（`displayName` / `bio` / `signature` /
 * `location` / `websiteUrl`），插件字段用组合键（`x.<ext>.<key>`）。两组都必须
 * 落到 tab 上：错误贴到看不见的地方等于没报错。既不在核心表、也不在任何插件
 * 分区里的键进 `global`，由页脚单独说明（不制造幽灵 tab，也不静默丢掉）。
 */
export type ProfileSettingsErrorBuckets = {
  byTab: Record<string, number>
  /** 无法归属到任何已渲染 tab 的错误条数。 */
  global: number
  /** 归属失败的字段键，便于诊断与测试断言。 */
  unaligned: string[]
}

export function profileSettingsErrorBuckets(
  fieldErrors: Record<string, string[]> | undefined,
  sections?: ProfileSettingsSection[]
): ProfileSettingsErrorBuckets {
  const tabByField = profileSettingsFieldTabMap(sections)
  const available = new Set(composeProfileSettingsTabs(sections, {
    identity: '',
    story: '',
    links: ''
  }).map(tab => tab.id))
  const byTab: Record<string, number> = {}
  const unaligned: string[] = []
  let global = 0
  for (const [fieldKey, messages] of Object.entries(fieldErrors || {})) {
    if (!Array.isArray(messages) || messages.length === 0) continue
    const tabId = tabByField[fieldKey]
    if (tabId && available.has(tabId)) {
      byTab[tabId] = (byTab[tabId] || 0) + 1
      continue
    }
    unaligned.push(fieldKey)
    global += 1
  }
  return { byTab, global, unaligned }
}

/** 只要每 tab 的计数（tab 错误点 / 页脚汇总据此渲染）。 */
export function profileSettingsErrorCounts(
  fieldErrors: Record<string, string[]> | undefined,
  sections?: ProfileSettingsSection[]
): Record<string, number> {
  return profileSettingsErrorBuckets(fieldErrors, sections).byTab
}

/** 按 tab 顺序取第一个有错误的分区；页脚汇总与错误点都靠它决定跳哪。 */
export function firstProfileSettingsErrorTab(
  errorCounts: Record<string, number>,
  tabs: ProfileSettingsTabView[]
): { id: string, count: number } | null {
  for (const tab of tabs) {
    const count = errorCounts[tab.id] || 0
    if (count > 0) return { id: tab.id, count }
  }
  return null
}

/** `?tab=` 归一化：未知 / 空值回落到第一个可用 tab（默认 identity）。 */
export function resolveProfileSettingsTab(
  value: unknown,
  tabs: ProfileSettingsTabView[],
  fallback: string = PROFILE_SETTINGS_DEFAULT_TAB
): string {
  const raw = Array.isArray(value) ? value[0] : value
  const candidate = typeof raw === 'string' ? raw.trim() : ''
  const ids = tabs.map(tab => tab.id)
  if (candidate && ids.includes(candidate)) return candidate
  return ids.includes(fallback) ? fallback : (ids[0] || fallback)
}

/** 声明式字段的初始值：已保存值优先，其次字段默认值，最后按类型给空值。 */
export function profileSettingsFieldValue(
  field: ProfileSettingsField,
  values: Record<string, ProfileSettingsValue> | undefined
): ProfileSettingsDraftValue {
  const raw = values?.[field.fieldKey]
  const source = raw === undefined || raw === null ? field.default : raw
  // boolean 的线上默认值是字符串（"true"/"false"），Boolean('false') 会是 true。
  if (field.type === 'boolean') return coerceBoolean(source)
  if (source === undefined || source === null) return ''
  if (field.type === 'number') {
    const numeric = typeof source === 'number' ? source : Number(source)
    return Number.isFinite(numeric) ? numeric : ''
  }
  return String(source)
}

function comparableValue(value: ProfileSettingsDraftValue | null | undefined): ProfileSettingsDraftValue {
  if (value === null || value === undefined) return ''
  if (typeof value === 'number') return Number.isFinite(value) ? value : ''
  return value
}

/** 一次 PUT 的 meta 载荷：只带真正改动的键，清空折算成 null。 */
export function profileSettingsMetaPayload(
  draft: Record<string, ProfileSettingsDraftValue>,
  baseline: Record<string, ProfileSettingsDraftValue> | undefined
): Record<string, ProfileSettingsValue | null> {
  const payload: Record<string, ProfileSettingsValue | null> = {}
  const keys = new Set([...Object.keys(draft), ...Object.keys(baseline || {})])
  for (const key of keys) {
    const next = comparableValue(draft[key])
    const previous = comparableValue(baseline?.[key])
    if (next === previous) continue
    payload[key] = next === '' ? null : next
  }
  return payload
}

export function profileSettingsMetaDirty(
  draft: Record<string, ProfileSettingsDraftValue>,
  baseline: Record<string, ProfileSettingsDraftValue> | undefined
): boolean {
  return Object.keys(profileSettingsMetaPayload(draft, baseline)).length > 0
}

/**
 * 把「刚提交成功的草稿」折算成 settingsValues 形状：空串表示清空，直接丢掉该键，
 * 让 hydrate 时回落到字段默认值。PUT /profile 不回显 settingsValues 时用它建立基线。
 */
export function profileSettingsDraftValuesAsSettings(
  draft: Record<string, ProfileSettingsDraftValue>
): Record<string, ProfileSettingsValue> {
  const values: Record<string, ProfileSettingsValue> = {}
  for (const [key, value] of Object.entries(draft)) {
    if (value === '' || value === null || value === undefined) continue
    values[key] = value
  }
  return values
}

/** 按分区字段顺序铺开草稿默认值，供 hydrate 时建立 baseline。 */
export function profileSettingsMetaValues(
  sections: ProfileSettingsSection[] | undefined,
  values: Record<string, ProfileSettingsValue> | undefined
): Record<string, ProfileSettingsDraftValue> {
  const result: Record<string, ProfileSettingsDraftValue> = {}
  for (const section of sections || []) {
    if (section?.type === 'component') continue
    for (const field of section.fields || []) {
      if (!field?.fieldKey) continue
      result[field.fieldKey] = profileSettingsFieldValue(field, values)
    }
  }
  return result
}

/** 公开主页「关于」的自定义字段行：空值不渲染，标签缺失时退回 fieldKey。 */
export function profileMetaRows(
  meta: Record<string, ProfileMetaEntry> | undefined,
  locale = ''
): ProfileMetaRow[] {
  const rows: ProfileMetaRow[] = []
  for (const [fieldKey, entry] of Object.entries(meta || {})) {
    const value = normalizeString(entry?.value).trim()
    if (!value) continue
    rows.push({
      fieldKey,
      // 标签可能本地化；解析失败才退回字段键，绝不把组合键当标签显示。
      label: resolveLocalizedText(entry?.label, locale, fieldKey),
      value
    })
  }
  return rows
}
