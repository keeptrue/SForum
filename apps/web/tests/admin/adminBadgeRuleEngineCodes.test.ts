import { describe, expect, test } from 'bun:test'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * 引擎错误码的穷尽性守门：两套词表并存时，唯一能防住漂移的手段。
 *
 * 源码位置按**目录**扫，不按单文件：`validate.go` 现在 311 行，拆成
 * `validate_shape.go` / `validate_param.go` 是迟早的事，目录比文件名稳定。
 * 只看非测试文件，避免 `_test.go` 里的断言字符串被当成码。
 *
 * 解析链与 `SFAdminBadgeEditDialog.engineIssueText` 一致：
 * `errors.engine.<code>` → `errors.<code>` → `errors.generic`（兜底，不计入通过）。
 */
const RULE_ENGINE_CODES = [
  'decode_failed',
  'version_unsupported',
  'shape_invalid',
  'too_deep',
  'too_many_nodes',
  'too_many_leaves',
  'unknown_condition',
  'compare_missing',
  'compare_invalid',
  'compare_not_allowed',
  'compare_forbidden',
  'value_out_of_range',
  'param_unknown',
  'param_missing',
  'param_invalid',
  'catalog_invalid',
  'resolve_failed'
] as const

/** 当前真实数量（17）。低于它就是抽取失效，不是「后端删了码」——删码要走清单比对。 */
const MIN_EXTRACTED_CODES = 17

const ENGINE_DIR = new URL('../../../../apps/api/app/Support/RuleEngine/', import.meta.url)
const CODE_PATTERN = /Code[A-Za-z]+\s*=\s*"([a-z0-9_]+)"/g
const CODE_SHAPE = /^[a-z][a-z0-9_]*$/

function locale(name: string): Record<string, unknown> {
  return JSON.parse(
    readFileSync(new URL(`../../i18n/locales/${name}.json`, import.meta.url), 'utf8')
  ) as Record<string, unknown>
}

function hasCopy(messages: Record<string, unknown>, path: string): boolean {
  const value = path
    .split('.')
    .reduce<unknown>(
      (current, key) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined),
      messages
    )
  return typeof value === 'string' && value.length > 0
}

function engineSources(): { file: string, source: string }[] {
  const dir = fileURLToPath(ENGINE_DIR)
  return readdirSync(dir)
    .filter(name => name.endsWith('.go') && !name.endsWith('_test.go'))
    .sort()
    .map(name => ({ file: name, source: readFileSync(new URL(name, ENGINE_DIR), 'utf8') }))
}

function extractEngineCodes(): string[] {
  const codes = new Set<string>()
  for (const { source } of engineSources()) {
    for (const match of source.matchAll(CODE_PATTERN)) {
      codes.add(match[1] as string)
    }
  }
  return [...codes].sort()
}

describe('rule engine error codes', () => {
  test('抽取结果有下界、形状合法、且与前端清单逐项一致', () => {
    const files = engineSources().map(item => item.file)
    expect(files.length).toBeGreaterThan(0)

    const extracted = extractEngineCodes()
    if (extracted.length < MIN_EXTRACTED_CODES) {
      throw new Error(
        `只从 ${files.join(', ')} 抽取到 ${extracted.length} 个引擎错误码，期望至少 ${MIN_EXTRACTED_CODES} 个；`
        + '若为 0，说明正则与 RuleEngine 源码格式不再匹配（码定义被改写/挪走），不是后端删码。'
      )
    }

    const malformed = extracted.filter(code => !CODE_SHAPE.test(code))
    if (malformed.length > 0) {
      throw new Error(`抽取到不符合 ^[a-z][a-z0-9_]*$ 的值（正则把非码内容抓进来了）：${malformed.join(', ')}`)
    }

    const expected = new Set<string>(RULE_ENGINE_CODES)
    const actual = new Set(extracted)
    const missing = [...expected].filter(code => !actual.has(code))
    const extra = [...actual].filter(code => !expected.has(code))
    if (missing.length > 0 || extra.length > 0) {
      throw new Error(
        `引擎码清单漂移（源：${files.join(', ')}，抽取 ${extracted.length} 个）：`
        + `前端清单缺 [${missing.join(', ')}]，前端清单多 [${extra.join(', ')}]。`
        + '改码时同步 RULE_ENGINE_CODES 与 admin.badges.rule.errors.* 文案。'
      )
    }
  })

  test('每个引擎码在 zh/en 都能解析出文案，不落到 generic', () => {
    for (const name of ['zh-CN', 'en-US'] as const) {
      const messages = locale(name)
      for (const code of RULE_ENGINE_CODES) {
        const resolvable = hasCopy(messages, `admin.badges.rule.errors.engine.${code}`)
          || hasCopy(messages, `admin.badges.rule.errors.${code}`)
        expect({ locale: name, code, resolvable }).toEqual({ locale: name, code, resolvable: true })
      }
    }
  })

  test('engine 子命名空间只放没有前端等价物的码，同名码不重复写文案', () => {
    const messages = locale('zh-CN')
    for (const code of RULE_ENGINE_CODES) {
      if (hasCopy(messages, `admin.badges.rule.errors.${code}`)) {
        expect({ code, duplicated: hasCopy(messages, `admin.badges.rule.errors.engine.${code}`) })
          .toEqual({ code, duplicated: false })
      }
    }
  })
})
