/**
 * 徽章规则条件的**参数层**：类型 + 校验（后台专用）。
 *
 * 与 `badgeRule.ts` 拆开是为了让「树结构与上限」和「参数 schema」各自可读；对外导出面
 * 仍由 `badgeRule.ts` re-export，调用方不需要改 import。
 *
 * 事实来源：`knowledge/plans/2026-11-12-badge-rule-engine.md` §5.2 / §5.2.1（参数精确
 * 形状已冻结，前后端同一份）。`objectRef` 的取值恒为字符串 slug，数字一律走 `int`。
 *
 * 选项（`options`）只下发**取值**，不带双语 label：文案由 `resolveParamOptionLabel`
 * 按 i18n 键解析，缺失时回落原始值。别把双语 label 塞回这里——那会让 wire 形状与
 * OpenAPI 脱钩，也会让插件条件没法只声明取值。
 */
import type { BadgeRuleError } from './badgeRule'

/**
 * 参数值形态，与后端 `ParamKind` 对齐：
 * `int` / `enum` / `window` / `objectRef` 是内置 12 条条件实际用到的四种；
 * `string` / `bool` 仅为 M3 插件条件预留（自由文本 / 开关型事实），**内置条件一条都不许用**。
 * `objectRef` 语义是「有候选列表的引用」——编辑器渲染下拉、后端 rule_health 判存在性。
 */
export type BadgeRuleParamKind = 'int' | 'enum' | 'window' | 'objectRef' | 'string' | 'bool'

/**
 * `objectRef` 指向的对象类型，供编辑器渲染候选下拉与后端健康检查使用。
 *
 * `fact` 是插件事实来源（`fact.count` 的 `factKey`）：候选值由后端在目录里填，
 * 显示文案来自独立顶层字段 `factCandidates`（M3，Errata 2）。
 */
export type BadgeRuleRefKind = 'category' | 'tag' | 'role' | 'provider' | 'badge' | 'fact'

/** 一个条件参数的声明。`min` / `max` 只对 `int` 生效。 */
export interface BadgeConditionParam {
  name: string
  kind: BadgeRuleParamKind
  required: boolean
  min?: number
  max?: number
  /**
   * 取值候选，**wire 上就是裸字符串数组**（`conditionParamDTO.Options []string`，
   * OpenAPI `items: {type: string}`）。选项文案不随数据下发，由 `resolveParamOptionLabel`
   * 按 i18n 键解析——把这里写成 `{value,label}` 曾让每个 `<option>` 的 value 变成 undefined。
   */
  options?: string[]
  /** 仅 `objectRef`：前端只据此选候选来源，**不做存在性校验**（那是后端 rule_health 的活）。 */
  refKind?: BadgeRuleRefKind
}

/** 窗口参数的 `kind` 取值，与后端 `ParamEnum` 声明一致。 */
const WINDOW_KINDS: readonly string[] = ['all', 'recent_days']

/**
 * 参数取值文案的三个**前缀族**（显式注入，不假定宿主域）。
 *
 * 共享编辑器同时服务徽章与信任等级，两族的键位形状不同（徽章的 `metrics` 在
 * `admin.badges` 下，信任等级那份在自己的子族里），所以这里只认前缀，不认具体域。
 * 每个前缀都指向一个**命名空间**（`<prefix>.<param>.options.<value>` 这种拼法才有意义）。
 */
export interface BadgeRuleLabelFamily {
  /** 参数专属选项文案：`<params>.<paramName>.options.<value>`。 */
  params: string
  /** 共享指标名：`<metrics>.<value>`（多个条件的同名指标只写一份）。 */
  metrics: string
  /** 共享枚举值名：`<enums>.<value>`。 */
  enums: string
}

/**
 * 徽章族的默认三元组：徽章侧**取值来源**不变——不传 `labelFamily` 时读的还是这三个
 * 前缀，与参数名文案修复之前完全一致。
 *
 * 注意别把这句读成「徽章侧一处可见文本都没变」：参数名（`params.<name>`）以前读的是
 * locale 根下的 `params`，那儿没有这个对象，所以**一直回落成裸 name**（下拉标签写着
 * `metric` 而不是「指标」）。那次修复让徽章与信任两边同时显示出正确文案，属于修缺陷。
 * 族隔离本身只影响「去哪一族取值」，不影响徽章侧的取词结果。
 *
 * 信任侧的契约见 `tests/admin/forum/trustLevelRuleI18n.test.ts`。
 */
export const BADGE_LABEL_FAMILY: BadgeRuleLabelFamily = {
  params: 'admin.badges.conditions.params',
  metrics: 'admin.badges.metrics',
  enums: 'admin.badges.conditions.enums'
}

/**
 * 参数取值的候选 i18n 键，**按命中优先级**排列：
 * 1) 参数专属的选项文案（`metric.options.topic_count`）；
 * 2) 共享指标名（`metrics.topic_count`，多个条件的同名指标只写一份）；
 * 3) 共享枚举值名（`enums.*`）。
 *
 * 返回数组而不是单个键，是为了让调用方只做一次「第一个存在的键」决策，
 * 文案顺序不会因为实现细节在不同控件里漂移。
 *
 * `family` 由宿主注入：**这一层不认任何域**，硬编码一个族会让另一族静默读错文案。
 */
export function paramOptionLabelKey(
  paramName: string,
  value: string,
  family: BadgeRuleLabelFamily = BADGE_LABEL_FAMILY
): string[] {
  return [
    `${family.params}.${paramName}.options.${value}`,
    `${family.metrics}.${value}`,
    `${family.enums}.${value}`
  ]
}

/**
 * 解析一个选项的显示文案：取第一个 `te()` 命中的键，全缺失时回落到**原始值**。
 *
 * 回落不是兜底美化：插件条件声明的取值宿主没有文案，显示 `my_plugin.metric` 至少还能读，
 * 显示空串等于把下拉变成了空白项（正是线上那个 bug 的观感）。
 */
export function resolveParamOptionLabel(
  paramName: string,
  value: string,
  t: (key: string) => string,
  te: (key: string) => boolean,
  family: BadgeRuleLabelFamily = BADGE_LABEL_FAMILY
): string {
  for (const key of paramOptionLabelKey(paramName, value, family)) {
    if (te(key)) return t(key)
  }
  return value
}

export function isIntegerIn(value: unknown, min: number, max: number): boolean {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isInteger(numeric) && numeric >= min && numeric <= max
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function invalidParam(spec: BadgeConditionParam, path: number[], errors: BadgeRuleError[]) {
  errors.push({
    code: 'rule.param_invalid',
    path,
    field: `params.${spec.name}`,
    params: { param: spec.name, kind: spec.kind }
  })
}

export function validateParamSpec(
  spec: BadgeConditionParam,
  value: unknown,
  path: number[],
  errors: BadgeRuleError[]
) {
  const field = `params.${spec.name}`
  if (value === undefined || value === null || value === '') {
    if (spec.required) {
      errors.push({ code: 'rule.param_required', path, field, params: { param: spec.name } })
    }
    return
  }

  switch (spec.kind) {
    case 'int': {
      const numeric = typeof value === 'number' ? value : Number(value)
      if (!Number.isInteger(numeric)) {
        invalidParam(spec, path, errors)
        return
      }
      const belowMin = spec.min !== undefined && numeric < spec.min
      const aboveMax = spec.max !== undefined && numeric > spec.max
      if (belowMin || aboveMax) {
        errors.push({
          code: 'rule.param_range',
          path,
          field,
          params: { param: spec.name, value: numeric, min: spec.min ?? null, max: spec.max ?? null }
        })
      }
      return
    }
    case 'enum': {
      if (typeof value !== 'string') {
        invalidParam(spec, path, errors)
        return
      }
      const allowed = spec.options ?? []
      if (!allowed.includes(value)) {
        errors.push({ code: 'rule.param_enum', path, field, params: { param: spec.name, value } })
      }
      return
    }
    case 'window': {
      if (!isPlainObject(value)) {
        invalidParam(spec, path, errors)
        return
      }
      const kind = value.kind
      if (typeof kind !== 'string' || !WINDOW_KINDS.includes(kind)) {
        errors.push({ code: 'rule.param_window', path, field: `${field}.kind`, params: { param: spec.name, kind: String(kind) } })
      }
      // `window.days` 的区间由引擎级规则统一管（`badgeRule.ts` 的 dayFields），不重复报。
      return
    }
    case 'objectRef': {
      // 冻结口径：objectRef 恒为字符串 slug；数字一律走 int，不做类型兼容。
      if (typeof value !== 'string' || value.trim() === '') {
        invalidParam(spec, path, errors)
      }
      return
    }
    case 'string': {
      // 内置 12 条条件未使用；留给 M3 插件条件的自由文本参数。
      if (typeof value !== 'string') invalidParam(spec, path, errors)
      return
    }
    case 'bool': {
      // 内置 12 条条件未使用；留给 M3 插件条件的开关型事实。
      if (typeof value !== 'boolean') invalidParam(spec, path, errors)
    }
  }
}

/** 有 `params` 声明时才走这一层；未声明的参数键同后端 `CodeParamUnknown` 一样拒绝。 */
export function validateParams(
  specs: readonly BadgeConditionParam[],
  params: Record<string, unknown> | undefined,
  path: number[],
  errors: BadgeRuleError[]
) {
  const declared = new Set(specs.map(spec => spec.name))
  for (const key of Object.keys(params ?? {})) {
    if (!declared.has(key)) {
      errors.push({ code: 'rule.param_unknown', path, field: `params.${key}`, params: { param: key } })
    }
  }
  for (const spec of specs) {
    validateParamSpec(spec, params?.[spec.name], path, errors)
  }
}
