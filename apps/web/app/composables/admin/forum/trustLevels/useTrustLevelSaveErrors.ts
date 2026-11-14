import { apiErrorDetails } from '~/composables/useApiClient'
import { TRUST_LEVEL_METRIC_SUFFIX_BY_KEY, type TrustLevelMetricKey } from '~/composables/profile/useTrustLevel'
import type { TrustLevelsForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { activeTiersOf } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

/**
 * 保存被拒（422）时的**字段级**诊断（P2）。
 *
 * 响应契约（后端 `data.details.errors[]`）：
 * `{ rank, tierId, field, reason, reasonCode?, params? }`
 * - `rank` 从 TL1 起；`0` = 无法定位到具体档位（全局字段 / JSON 语法错 / 停用档位）。
 * - `tierId` 为 `""` 表示新档或全局错误。
 * - `field` 是固定取值域（见 `parseSaveError` 的注释），空串 = 无法归因。
 * - 数组最多 8 条、顺序即解码顺序：**前端不截断也不补全**（上界由后端定）。
 * - `details` 可能整体缺席：此时按「只有统一码」处理，渲染回落统一文案，绝不渲染空白错误条。
 *
 * `reason` 是后端中文散文，只做**最后**回落：优先 `reasonCode` + i18n（en-US 站点不该看到中文）。
 */
export type TrustLevelSaveError = {
  /** 1 起；`0` = 无法定位到具体档位。 */
  rank: number
  tierId: string
  /** 固定取值域之一；空串 = 无法归因。 */
  field: string
  reasonCode: string
  reason: string
  params: Record<string, string | number>
}

function readParams(raw: unknown): Record<string, string | number> {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {}
  const params: Record<string, string | number> = {}
  for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
    if (typeof value === 'string' || typeof value === 'number') params[key] = value
  }
  return params
}

function readError(entry: unknown): TrustLevelSaveError | null {
  if (!entry || typeof entry !== 'object') return null
  const record = entry as Record<string, unknown>
  const rank = Number(record.rank)
  return {
    rank: Number.isFinite(rank) && rank > 0 ? Math.trunc(rank) : 0,
    tierId: typeof record.tierId === 'string' ? record.tierId.trim() : '',
    field: typeof record.field === 'string' ? record.field.trim() : '',
    reasonCode: typeof record.reasonCode === 'string' ? record.reasonCode.trim() : '',
    reason: typeof record.reason === 'string' ? record.reason : '',
    params: readParams(record.params)
  }
}

/** `details.errors[]` → 诊断列表。`details` 缺席/形状非法 → `[]`（不是错误，是「只有统一码」）。 */
export function parseSaveErrors(details: unknown): TrustLevelSaveError[] {
  const record = (details && typeof details === 'object' ? details : {}) as Record<string, unknown>
  if (!Array.isArray(record.errors)) return []
  const errors: TrustLevelSaveError[] = []
  for (const entry of record.errors) {
    const parsed = readError(entry)
    if (parsed) errors.push(parsed)
  }
  return errors
}

/** 从一次保存失败里取出诊断：`details` 缺席或为空 → `[]`。 */
export function saveErrorsFromError(error: unknown): TrustLevelSaveError[] {
  return parseSaveErrors(apiErrorDetails(error))
}

export type TrustLevelSaveErrorBuckets = {
  /** 按 tier id 归位的行内提示（含停用档）。 */
  byTierId: Record<string, TrustLevelSaveError[]>
  /** 归不到任何档位的错误：页面级错误块。 */
  pageLevel: TrustLevelSaveError[]
}

/**
 * 把诊断挂到具体档位上（渲染层的唯一判据）：
 * 1. `rank >= 1` → 第 rank 个 **active** 档位（rank 与展示位次同源）；
 * 2. `rank === 0` 且有 `tierId` → 按 id 找（停用档也可能被后端点名）；
 * 3. 其余（含两者都空、id 已不存在）→ 页面级错误块。
 *
 * 注意第 2 条不能退化成「当成 TL0」：停用档位不在 active 序列里，按 rank 找会挂错行。
 */
export function bucketSaveErrors(
  errors: readonly TrustLevelSaveError[],
  form: TrustLevelsForm
): TrustLevelSaveErrorBuckets {
  const active = activeTiersOf(form.tiers)
  const buckets: TrustLevelSaveErrorBuckets = { byTierId: {}, pageLevel: [] }
  for (const error of errors) {
    const targetId = error.rank > 0
      ? (active[error.rank - 1]?.id ?? '')
      : (error.tierId && form.tiers.some(tier => tier.id === error.tierId) ? error.tierId : '')
    if (!targetId) {
      buckets.pageLevel.push(error)
      continue
    }
    buckets.byTierId[targetId] = [...(buckets.byTierId[targetId] ?? []), error]
  }
  return buckets
}

/** 行内提示的字段名（i18n）；`requirements.<metric>` / `limits.<key>` 按前缀拆开。 */
export function saveErrorFieldLabel(
  field: string,
  translate: (key: string) => string,
  isKnown: (key: string) => boolean,
  metricLabel?: (suffix: string) => string
): string {
  if (!field) return translate('admin.forum.trustLevels.errors.field.unknown')
  // i18n 键不能用点号分段（vue-i18n 会把点当路径分隔符），所以存储字段名前缀 `name.zh`
  // 在这里归一成 `name_zh`，与 locale 里的扁平键一一对应。
  const exact = `admin.forum.trustLevels.errors.field.${field.replaceAll('.', '_')}`
  if (isKnown(exact)) return translate(exact)
  if (field.startsWith('requirements.')) {
    const suffix = field.slice('requirements.'.length)
    return metricLabel ? metricLabel(suffix) : translate('admin.forum.trustLevels.errors.field.requirements')
  }
  if (field.startsWith('limits.')) {
    const suffix = field.slice('limits.'.length)
    return translate(`admin.forum.trustLevels.errors.field.limits_${suffix}`)
  }
  return field
}

/**
 * `reasonCode` 分线（39 条，OpenAPI enum 与 Go 常量表同序）：
 *
 * - **specific**（21 条）：运营在后台表单里真的能造出来 —— 名字超长、图标不在白名单、
 *   想清空但后端要求必须给数组、条件树超上限 / 引用了被删对象……每条都值得一句准确的话。
 * - **config**（18 条）：配置体本身坏了（类型错、语法错、v1 存量迁移的 `level_*`）。
 *   这些走一个通用桶：主句说「格式不被接受」，`reason` 只作为技术细节出现。
 *
 * 两张表是**互斥且穷尽** 39 条的护栏；`trustLevels.test.ts` 里的完整性测试会比对后端枚举，
 * 后端加第 40 个 code 而前端没登记时测试直接红 —— 这是唯一能防「en-US 站点悄悄显示中文」的办法。
 *
 * M3 新增的 6 条（`earnable_not_declared` + 5 条规则码）全部进 **specific**：它们都是运营
 * 在后台能主动造出来的形态，修法各不相同（删一条条件 / 减一层嵌套 / 重选引用 / 启用插件）。
 * 规则码的定位坐标在 `params.path`（`ruleengine.Error.Path`），文案不再复述「第几档」。
 */
export const SPECIFIC_ERROR_CODES = [
  'tier_count_out_of_range',
  'id_invalid',
  'id_reserved_for_tl0',
  'id_duplicate',
  'icon_not_whitelisted',
  'name_missing',
  'name_too_long',
  'requirement_negative',
  'requirement_unknown_metric',
  'limits_missing',
  'limit_negative',
  'earnable_missing',
  'earnable_key_empty',
  'earnable_not_whitelisted',
  // M3：插件权限的写路径拒绝（按 `params.cause` 分两句，见 `saveErrorText` 的同码变体分支）。
  'earnable_not_declared',
  // M3：条件树的 5 条规则码。全部是 tier 级（带 `rank`/`tierId`），修法各不相同，不能合并。
  'rule_too_complex',
  'rule_unknown_condition',
  'rule_shape_invalid',
  'rule_reference_invalid',
  'rule_condition_not_available',
  // D-C1：运营在色板里选不到白名单外的值，这个码只会来自并发/旧载荷，值得一句准确的话。
  'color_not_whitelisted'
] as const

export const CONFIG_BUCKET_ERROR_CODES = [
  'schema_version_unsupported',
  'schema_shape_invalid',
  'json_syntax_invalid',
  'json_type_mismatch',
  'unknown_field',
  'config_unparseable',
  'tail_content',
  'limits_unparseable',
  'earnable_unparseable',
  'earnable_invalid',
  'id_derivation_conflict',
  'level_unknown',
  'level_reserved_for_tl0',
  'level_duplicate',
  'level_legacy_missing',
  // D-C3：墓碑 id 是顶层数组，坏了就整块配置不可信 —— 归配置桶。
  'deleted_tier_id_invalid',
  'deleted_tier_id_duplicate',
  'deleted_tier_id_present'
] as const

const SPECIFIC = new Set<string>(SPECIFIC_ERROR_CODES)
const CONFIG_BUCKET = new Set<string>(CONFIG_BUCKET_ERROR_CODES)

export function isSpecificErrorCode(code: string): boolean {
  return SPECIFIC.has(code)
}

export function isConfigBucketErrorCode(code: string): boolean {
  return CONFIG_BUCKET.has(code)
}

/**
 * 人读文案：
 * 1. 专属码命中 i18n → 用它（`params` 插值）；
 * 2. 配置体坏了的桶 → 主句 + 「技术细节：{reason}」（`reason` 不进主句）；
 * 3. 其余（后端新增了前端还没跟上的码）→ 回落后端 `reason`，再否则统一兜底句。
 *
 * **永远不返回空串** —— 空行内提示比没有提示更糟。
 */
/** `requirements.<metric>` 的 suffix → i18n 键（`days_visited` → `daysVisited`）。 */
const METRIC_KEY_BY_SUFFIX = Object.fromEntries(
  Object.entries(TRUST_LEVEL_METRIC_SUFFIX_BY_KEY).map(([key, suffix]) => [suffix, key as TrustLevelMetricKey])
) as Record<string, TrustLevelMetricKey>

/**
 * 把机器值翻成人话再插进句子。
 *
 * 这类问题的共同形态是「后端给的稳定标识符被直接塞进中文/英文句子」：
 * - `locale`：`zh` / `en` → 中文 / 英文（否则读成「zh 名称超过…」）；
 * - `metric`：`days_visited` → 访问天数（六项判定指标）；
 * - `key`：`topicCooldownSeconds` → 发主题冷却（分档限额的四个字段名）；
 * - `permission`：`topic.create` → 发布主题（复用权限目录，不新建映射表）。
 *
 * `id` / `icon` / 未知 `metric` **保持原样**：它们分别是运营自己输入的值、图标白名单的短名
 * （与图标选择器同一套词）、以及「我不认识这个指标」的诊断证据 —— 翻译它们反而让人查不到问题。
 */
function humanizeParams(
  reasonCode: string,
  params: Record<string, string | number>,
  translate: (key: string, params?: Record<string, string | number>) => string,
  isKnown: (key: string) => boolean
): Record<string, string | number> {
  const next = { ...params }

  if (typeof next.locale === 'string') {
    const key = `admin.forum.trustLevels.errors.locale.${next.locale}`
    if (isKnown(key)) next.locale = translate(key)
  }

  if (typeof next.metric === 'string') {
    const metricKey = METRIC_KEY_BY_SUFFIX[next.metric]
    if (metricKey) next.metric = translate(`admin.forum.trustLevels.metric.${metricKey}`)
  }

  if (reasonCode === 'limit_negative' && typeof next.key === 'string') {
    const key = `admin.forum.trustLevels.errors.field.limits_${next.key}`
    if (isKnown(key)) next.key = translate(key)
  }

  if (typeof next.permission === 'string') {
    const key = `admin.permissionCatalog.${next.permission}.label`
    if (isKnown(key)) next.permission = translate(key)
  }

  return next
}

/**
 * 同码两种成因的变体后缀：**同一个稳定码**，不是第 40 个码。
 *
 * `earnable_not_declared` 的修法取决于成因，两句必须分开给（copy-spec §1.2）：
 * - `plugin_disabled` → 「启用插件」，配置不用动；
 * - 默认（`undeclared` / 字段缺失）→ 「删掉这个勾选」。
 *
 * 合并成一句的后果是实测过的：写成「从未被声明」，运营会去删勾选，等插件恢复时配置
 * 已被自己删掉。所以这里按 `params.cause` 选键，不在码表里加第 40 条（那会把三处同步面
 * 扩大到四处）。
 */
function variantSuffix(error: TrustLevelSaveError): string {
  if (error.reasonCode === 'earnable_not_declared' && error.params.cause === 'plugin_disabled') {
    return '_plugin_disabled'
  }
  return ''
}

export function saveErrorText(
  error: TrustLevelSaveError,
  translate: (key: string, params?: Record<string, string | number>) => string,
  isKnown: (key: string) => boolean
): string {
  if (error.reasonCode && isSpecificErrorCode(error.reasonCode)) {
    const key = `admin.forum.trustLevels.errors.code.${error.reasonCode}${variantSuffix(error)}`
    if (isKnown(key)) return translate(key, humanizeParams(error.reasonCode, error.params, translate, isKnown))
  }
  if (error.reasonCode && isConfigBucketErrorCode(error.reasonCode)) {
    const main = translate('admin.forum.trustLevels.errors.config.main')
    if (!error.reason) return main
    return `${main} ${translate('admin.forum.trustLevels.errors.config.detail', { reason: error.reason })}`
  }
  if (error.reason) return error.reason
  return translate('admin.forum.trustLevels.errors.generic')
}
