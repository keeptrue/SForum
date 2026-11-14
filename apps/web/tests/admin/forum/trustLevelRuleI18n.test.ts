import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

/**
 * 信任等级的规则族文案契约（M3b）。
 *
 * 共享编辑器（`components/admin/rules/*`）把 i18n 前缀参数化了，信任等级读的是
 * `admin.forum.trustLevels.rule.*`。这一族是 `admin.badges.rule.*` 的**逐字副本**：
 * 同一句话在两条渲染路径上都必须一样，跨族 `t()` 又会在一处改名时静默改掉另一处
 * （copy-spec §3.3.2 对 `impact.method.*` 的同一条裁决）。
 *
 * 本文件钉住三件事：两份 locale 键集相同、与徽章族逐键等值、且不多出徽章没有的键
 * （多出来就是自造文案）。
 */
const load = (locale: string) =>
  JSON.parse(readFileSync(new URL(`../../../i18n/locales/${locale}.json`, import.meta.url), 'utf8')) as Record<string, unknown>

function flatten(node: unknown, prefix = ''): Map<string, string> {
  const out = new Map<string, string>()
  const walk = (value: unknown, path: string) => {
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
        walk(child, path ? `${path}.${key}` : key)
      }
      return
    }
    out.set(path, String(value))
  }
  walk(node, prefix)
  return out
}

const pick = (doc: Record<string, unknown>, path: string) => {
  const value = path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    doc
  )
  expect(value).toBeDefined()
  return flatten(value)
}

const zh = load('zh-CN')
const en = load('en-US')

describe('信任等级规则族（admin.forum.trustLevels.rule）', () => {
  test('中英键集完全一致（缺一边 = 一边站点会看到原始 key）', () => {
    const zhKeys = [...pick(zh, 'admin.forum.trustLevels.rule').keys()].sort()
    const enKeys = [...pick(en, 'admin.forum.trustLevels.rule').keys()].sort()
    expect(zhKeys).toEqual(enKeys)
  })

  test('与徽章族逐键等值（同一句话，两条渲染路径）', () => {
    for (const [locale, doc] of [['zh-CN', zh], ['en-US', en]] as const) {
      const tl = pick(doc, 'admin.forum.trustLevels.rule')
      const badge = pick(doc, 'admin.badges.rule')
      const drift = [...tl.entries()].filter(([key, value]) => badge.get(key) !== value).map(([key]) => key)
      expect({ locale, drift }).toEqual({ locale, drift: [] })
    }
  })

  test('不多出徽章族没有的键（自造文案会被这条抓住）', () => {
    const tl = pick(zh, 'admin.forum.trustLevels.rule')
    const badge = pick(zh, 'admin.badges.rule')
    const invented = [...tl.keys()].filter(key => !badge.has(key)).sort()
    expect(invented).toEqual([])
  })

  test('共享编辑器真正用到的短键都在这一族里（缺键时 te 会静默回落原始 key）', () => {
    const tl = pick(zh, 'admin.forum.trustLevels.rule')
    // 与 `components/admin/rules/*` 的短键调用点一致：任何一条缺失都会让信任等级页显示原始 key。
    for (const key of [
      'title',
      'mode.simple',
      'mode.advanced',
      'modeAdvancedOnly',
      'groupOp',
      'condition',
      'compareOp',
      'value',
      'addCondition',
      'addGroup',
      'remove',
      'wrapNot',
      'unwrap',
      'notNeedsOneChild',
      'emptyGroup',
      'limitDepth',
      'limitLeaves',
      'limitNodes',
      'jsonTitle',
      'copy',
      'copied',
      'copyFailed',
      'paramUnsupported',
      'sentence.tpl',
      'sentence.allTime',
      'sentence.withinDays',
      'sentence.joinAll',
      'sentence.joinAny',
      'sentence.negate',
      'sentence.empty',
      'ops.all',
      'ops.any',
      'ops.not',
      'compare.gte',
      'compare.lte',
      'window.all',
      'window.recent_days',
      'simple.allOf',
      'simple.addCondition',
      'simple.empty',
      'simple.delete',
      'simple.windowAll',
      'simple.windowRecent',
      'errors.generic',
      'errors.empty_group',
      'health.missing_fact_key'
    ]) {
      expect({ key, present: tl.has(key) }).toEqual({ key, present: true })
    }
  })
})

describe('信任等级条件文案族（admin.forum.trustLevels.conditions）', () => {
  test('中英键集一致，且影响面三态的键齐全', () => {
    const zhKeys = [...pick(zh, 'admin.forum.trustLevels.conditions').keys()].sort()
    const enKeys = [...pick(en, 'admin.forum.trustLevels.conditions').keys()].sort()
    expect(zhKeys).toEqual(enKeys)
    for (const key of [
      'toggle.more',
      'toggle.add',
      'panel.loading',
      'panel.empty.description',
      'panel.loadFailed.title',
      'panel.loadFailed.description',
      'panel.loadFailed.reason',
      'panel.loadFailed.retry',
      'panel.readOnly.title',
      'panel.readOnly.description',
      'panel.featureOff',
      'panel.retired',
      'panel.description',
      'impact.title',
      'impact.scopeHint',
      'impact.estimate',
      'impact.meaningHint',
      'impact.unestimable',
      'impact.nullHint',
      'impact.unsupportedTitle',
      'impact.partialHint',
      'impact.loading',
      'impact.loadFailed',
      'impact.consequence',
      'impact.confirm',
      'impact.confirmAnyway',
      'impact.cancel',
      'impact.unsupportedMore',
      'impact.unsupportedLess',
      'impact.method.single_leaf',
      'impact.method.strictest_leaf',
      'impact.method.strictest_leaf_partial',
      'impact.method.sum_of_leaves',
      'impact.method.complement_of_leaf',
      'impact.method.unsupported_condition'
    ]) {
      expect({ key, present: zhKeys.includes(key) }).toEqual({ key, present: true })
    }
  })

  test('唯一带 ≈ 的键只有数字位那一条；说明句零数字占位（Q2b）', () => {
    const zhMap = pick(zh, 'admin.forum.trustLevels.conditions')
    const withApprox = [...zhMap.entries()].filter(([, value]) => value.includes('≈')).map(([key]) => key)
    expect(withApprox).toEqual(['impact.estimate'])
    expect(zhMap.get('impact.meaningHint')).not.toMatch(/\{\w+\}|\d/)
  })

  test('禁用「保存后才能估算」这类作废口径（CP-4）', () => {
    const zhMap = pick(zh, 'admin.forum.trustLevels.conditions')
    for (const value of zhMap.values()) expect(value).not.toContain('保存后才能估算')
    expect([...zhMap.keys()].some(key => key.includes('newTierHint'))).toBe(false)
  })

  test('不新增第三键族', () => {
    const settings = pick(zh, 'admin.settings')
    expect([...settings.keys()].filter(key => key.toLowerCase().includes('trustlevel'))).toEqual([])
  })
})
