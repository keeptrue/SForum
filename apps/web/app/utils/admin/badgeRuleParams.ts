/**
 * 徽章规则条件的**参数层**：类型 + 校验（后台专用）。
 *
 * 与 `badgeRule.ts` 拆开是为了让「树结构与上限」和「参数 schema」各自可读；对外导出面
 * 仍由 `badgeRule.ts` re-export，调用方不需要改 import。
 *
 * 事实来源：`knowledge/plans/2026-11-12-badge-rule-engine.md` §5.2 / §5.2.1（参数精确
 * 形状已冻结，前后端同一份）。`objectRef` 的取值恒为字符串 slug，数字一律走 `int`。
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

export interface BadgeConditionParamOption {
  value: string
  labelZh: string
  labelEn: string
}

/** 一个条件参数的声明。`min` / `max` 只对 `int` 生效。 */
export interface BadgeConditionParam {
  name: string
  kind: BadgeRuleParamKind
  required: boolean
  min?: number
  max?: number
  options?: BadgeConditionParamOption[]
  /** 仅 `objectRef`：前端只据此选候选来源，**不做存在性校验**（那是后端 rule_health 的活）。 */
  refKind?: BadgeRuleRefKind
}

/** 窗口参数的 `kind` 取值，与后端 `ParamEnum` 声明一致。 */
const WINDOW_KINDS: readonly string[] = ['all', 'recent_days']

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
      const allowed = (spec.options ?? []).map(option => option.value)
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
