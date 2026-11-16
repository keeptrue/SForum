import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { flattenKeys, LOCALES, lookupMessage, readLocale } from '../helpers/locale'

/**
 * i18n 全仓卫生的常驻棘轮（RV-09 / RV-10 / NF-03 的收口点）。
 *
 * 这一族缺陷的共同形态是「locale 与代码各说各话」：
 * - 代码 `t('common.cancel')`，两份 locale 都没有 → 界面直接显示键名（RV-09）；
 * - zh-CN 有 `userAppearanceSettings.*` 整族，en-US 一条没有 → 英文页整页露键路径（RV-10）；
 * - 审计按字面键名数出 15 个缺键，其中 12 个其实是共享规则编辑器的**前缀化短键**
 *   （`t` 由 `props.i18nKeyPrefix` 包装），真实键一直在（NF-03）。
 *
 * 所以断言分三层，缺一层就有假绿：
 * 1. 两侧键集严格相等（删一条、改一条名字都会红）；
 * 2. 代码里每个字面键都能在两份 locale 里查到；
 * 3. 模板拼接键的静态前缀必须是真实命名空间，前缀化短键必须能在**每一个**宿主族里查到。
 *
 * 扫描口径：只读 `app/**` 下的 `.vue` / `.ts`。`t('x')` 的识别要求 `t` 前面不是
 * 标识符或点（`format(` / `at(` / `rawT(` 都不会被当成翻译调用）。
 */

const APP_ROOT = fileURLToPath(new URL('../../app/', import.meta.url))

/** 共享规则编辑器把宿主域当 prop 传进来，短键必须在**每个宿主族**里都能查到。 */
const RULE_EDITOR_DIR = 'app/components/admin/rules/'

/** 会接收 `i18n-key-prefix` 的共享规则组件（宿主传值 / 组件默认值二选一）。 */
const RULE_COMPONENTS = [
  'SFAdminRuleEditor',
  'SFAdminRuleGroup',
  'SFAdminRuleSimpleForm',
  'SFAdminRuleLeaf',
  'SFAdminRuleJsonPreview',
  'SFAdminRuleParamField'
] as const

/** 宿主族的**发现**口径：从真实调用点推导，而不是手写两族（新增宿主曾会静默逃逸）。 */
export function hostPrefixFromSource(source: string): string | null {
  if (/:i18n-key-prefix\s*=/.test(source)) {
    throw new Error('i18n-key-prefix 必须是静态字面量：动态绑定会让短键棘轮静默失效')
  }
  const match = /i18n-key-prefix\s*=\s*"([^"]+)"/.exec(source)
  return match ? match[1]!.trim() : null
}

/** 组件自己的 `withDefaults` 默认前缀（宿主不传时生效）。 */
function componentDefaultPrefix(component: string): string {
  const source = readFileSync(`${APP_ROOT}components/admin/rules/${component}.vue`, 'utf8')
  const match = /i18nKeyPrefix:\s*'([^']+)'/.exec(source)
  if (!match) throw new Error(`共享规则组件 ${component} 没有声明 i18nKeyPrefix 默认值`)
  return match[1]!
}

/** 扫描宿主调用点，得到「短键必须齐全」的前缀集合。 */
function discoverRuleHostPrefixes(): string[] {
  const prefixes = new Set<string>()
  const callSite = new RegExp(`<(${RULE_COMPONENTS.join('|')})\\b([^>]*)>`, 'gs')
  for (const file of appFiles()) {
    const relativePath = file.slice(file.indexOf('app/'))
    if (relativePath.startsWith(RULE_EDITOR_DIR)) continue
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(callSite)) {
      const component = match[1]!
      const explicit = hostPrefixFromSource(match[2] ?? '')
      prefixes.add(explicit ?? componentDefaultPrefix(component))
    }
  }
  return [...prefixes].sort()
}

const RULE_HOST_PREFIXES = discoverRuleHostPrefixes()

/** 前缀化短键里的 `${...}` 取值来自组件内部固定枚举，扫描器按此展开。 */
const SCOPED_ENUMERATIONS: Record<string, readonly string[]> = {
  ops: ['all', 'any', 'not'],
  compare: ['gte', 'lte']
}

/** `t(` / `$t(` / `tm(` / `i18n.t(` 的首个字面量参数。 */
const CALL_PATTERN = /(?<![.\w$])(?:\$t|i18n\.t|\bt|tm)\(\s*(['"`])((?:\\.|(?!\1)[^\\])*?)\1/g

function appFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = `${dir}${name}`
      if (statSync(full).isDirectory()) walk(`${full}/`)
      else if (/\.(vue|ts)$/.test(name)) out.push(full)
    }
  }
  walk(APP_ROOT)
  return out
}

/** 文件内的字符串常量：`${rulePrefix}.errors...` 这类模板键要能静态展开。 */
function localStringConstants(source: string): Map<string, string> {
  const constants = new Map<string, string>()
  for (const match of source.matchAll(/\bconst\s+([A-Za-z_$][\w$]*)\s*=\s*'([^'\\\n]*)'/g)) {
    constants.set(match[1]!, match[2]!)
  }
  return constants
}

/** 把模板键里能静态展开的插值替换掉，剩下的 `${...}` 才是真动态。 */
function expandLocalConstants(raw: string, constants: Map<string, string>): string {
  return raw.replace(/\$\{([A-Za-z_$][\w$]*)\}/g, (match, name: string) => constants.get(name) ?? match)
}

/** 静态前缀：第一处动态插值之前、最后一个完整段。 */
function staticPrefix(raw: string): string {
  const cut = raw.slice(0, raw.indexOf('${'))
  if (cut.endsWith('.')) return cut.slice(0, -1)
  const lastDot = cut.lastIndexOf('.')
  return lastDot === -1 ? '' : cut.slice(0, lastDot)
}

interface Reference {
  key: string
  file: string
  scoped: boolean
}

function collectReferences(): Reference[] {
  const refs: Reference[] = []
  for (const file of appFiles()) {
    const relativePath = file.slice(file.indexOf('app/'))
    const source = readFileSync(file, 'utf8')
    const constants = localStringConstants(source)
    for (const match of source.matchAll(CALL_PATTERN)) {
      const expanded = expandLocalConstants(match[2]!, constants)
      // 纯词条（`t('empty')`）在规则编辑器外不存在；保留它们才能让「键名写错」也变红。
      refs.push({ key: expanded, file: relativePath, scoped: relativePath.startsWith(RULE_EDITOR_DIR) })
    }
  }
  return refs
}

const references = collectReferences()
const zh = readLocale('zh-CN')
const en = readLocale('en-US')
const zhKeys = flattenKeys(zh)
const enKeys = flattenKeys(en)

describe('locale key parity (RV-10)', () => {
  test('zh-CN 与 en-US 的键集严格相等', () => {
    // 反真空绿：两份 locale 都必须是完整目录，不是被截断的空壳。
    expect(zhKeys.size).toBeGreaterThan(6000)
    expect(enKeys.size).toBe(zhKeys.size)

    const zhOnly = [...zhKeys].filter(key => !enKeys.has(key)).sort()
    const enOnly = [...enKeys].filter(key => !zhKeys.has(key)).sort()
    expect({ zhOnly, enOnly }).toEqual({ zhOnly: [], enOnly: [] })
  })

  test('本轮新增的英文文案是真英文，不是中文原样复制', () => {
    // 只锁定本轮落地的族：既有目录里 `admin.settings.locale.zhCN`（「简体中文」是
    // 语言自称）与 `admin.siteChrome.announcements.titleZhPlaceholder`（示范中文公告）
    // 本来就是中文，全量扫会把这两处正确的文案判成缺陷。
    const families = [
      'userAppearanceSettings.',
      'common.',
      'search.placeholder',
      'search.filtersLabel',
      'forum.feed.',
      'auth.loginOrRegister',
      'composer.editor.',
      'composer.shortcodes.fallback.',
      'composer.emoji.',
      'admin.extensions.contributions.governance.disabledBadge'
    ]
    const offenders: string[] = []
    let checked = 0
    for (const key of [...enKeys].sort()) {
      if (!families.some(family => key === family || key.startsWith(family))) continue
      const value = lookupMessage('en-US', key)
      if (typeof value !== 'string') continue
      checked += 1
      if (/[\u3400-\u4dbf\u4e00-\u9fff]/.test(value)) offenders.push(`${key} :: ${value}`)
    }
    // 反真空绿：族名写错时 checked 会掉到 0，这条断言立刻失效。
    expect(checked).toBeGreaterThan(80)
    expect(offenders).toEqual([])
  })
})

describe('referenced keys resolve (RV-09 / NF-03)', () => {
  test('每个字面 t()/$t()/tm() 引用键都在两份 locale 里', () => {
    // 反真空绿：扫描面必须真的覆盖产品代码，而不是正则失配后的空集合。
    expect(references.length).toBeGreaterThan(4000)

    const missing: string[] = []
    for (const reference of references) {
      if (reference.scoped || reference.key.includes('${')) continue
      for (const locale of LOCALES) {
        if (lookupMessage(locale, reference.key) === undefined) {
          missing.push(`${locale} :: ${reference.key} <- ${reference.file}`)
        }
      }
    }
    expect(missing.sort()).toEqual([])
  })

  test('前缀化短键在每个宿主族里都能查到', () => {
    const shortKeys = new Map<string, Set<string>>()
    for (const reference of references) {
      if (!reference.scoped) continue
      if (reference.key.includes('${')) {
        const prefix = staticPrefix(reference.key)
        const segment = prefix.split('.').pop() ?? ''
        const values = SCOPED_ENUMERATIONS[segment]
        expect({ key: reference.key, file: reference.file, accepted: Boolean(values) })
          .toEqual({ key: reference.key, file: reference.file, accepted: true })
        for (const value of values!) {
          const expanded = `${prefix}.${value}`
          if (!shortKeys.has(expanded)) shortKeys.set(expanded, new Set())
          shortKeys.get(expanded)!.add(reference.file)
        }
        continue
      }
      if (!shortKeys.has(reference.key)) shortKeys.set(reference.key, new Set())
      shortKeys.get(reference.key)!.add(reference.file)
    }
    // 共享编辑器确实在被引用：空集合会让这条断言变成永远通过。
    expect(shortKeys.size).toBeGreaterThanOrEqual(30)

    const missing: string[] = []
    for (const [shortKey, files] of [...shortKeys].sort()) {
      for (const host of RULE_HOST_PREFIXES) {
        const fullKey = `${host}.${shortKey}`
        for (const locale of LOCALES) {
          if (lookupMessage(locale, fullKey) === undefined) {
            missing.push(`${locale} :: ${fullKey} <- ${[...files].join(',')}`)
          }
        }
      }
    }
    expect(missing.sort()).toEqual([])
  })

  test('模板拼接键的静态前缀都是真实命名空间', () => {
    const prefixes = new Map<string, Set<string>>()
    for (const reference of references) {
      if (reference.scoped || !reference.key.includes('${')) continue
      const prefix = staticPrefix(reference.key)
      // 前缀为空 = 键完全由运行时拼装，静态扫描到此为止，必须显式记录而不是静默跳过。
      if (!prefix) throw new Error(`cannot statically resolve i18n key: ${reference.key} <- ${reference.file}`)
      if (!prefixes.has(prefix)) prefixes.set(prefix, new Set())
      prefixes.get(prefix)!.add(reference.file)
    }
    expect(prefixes.size).toBeGreaterThanOrEqual(100)

    const unrouted: string[] = []
    for (const [prefix, files] of [...prefixes].sort()) {
      for (const locale of LOCALES) {
        const keys = locale === 'zh-CN' ? zhKeys : enKeys
        const resolved = [...keys].some(key => key === prefix || key.startsWith(`${prefix}.`))
        if (!resolved) unrouted.push(`${locale} :: ${prefix} <- ${[...files].join(',')}`)
      }
    }
    expect(unrouted.sort()).toEqual([])
  })
})

describe('regression anchors', () => {
  test('RV-09 的取消/更多是真实文案，不是键名', () => {
    expect(lookupMessage('zh-CN', 'common.cancel')).toBe('取消')
    expect(lookupMessage('en-US', 'common.cancel')).toBe('Cancel')
    expect(lookupMessage('zh-CN', 'common.more')).toBe('更多')
    expect(lookupMessage('en-US', 'common.more')).toBe('More')

    // 消费端仍在引用这两个键：缺陷是「键不存在」，不是「没人用」。
    expect(readFileSync(`${APP_ROOT}components/admin/SFAdminFrontendTrustPanel.vue`, 'utf8'))
      .toContain("t('common.cancel')")
    expect(readFileSync(`${APP_ROOT}pages/admin/extensions/plugins.vue`, 'utf8'))
      .toContain("t('common.more')")
  })

  test('RV-10 的外观设置整族在英文侧有真英文', () => {
    const keys = [...zhKeys].filter(key => key.startsWith('userAppearanceSettings.'))
    expect(keys.length).toBe(28)
    for (const key of keys) {
      const value = lookupMessage('en-US', key)
      expect(typeof value).toBe('string')
      expect(value!.trim()).not.toBe('')
    }
  })

  test('UX2-19 的跳转占位文案两侧都有', () => {
    expect(lookupMessage('zh-CN', 'common.redirecting')).toBe('正在跳转…')
    expect(lookupMessage('en-US', 'common.redirecting')).toBe('Redirecting…')
  })

  test('NF-03 的治理徽标文案两侧都有', () => {
    expect(lookupMessage('zh-CN', 'admin.extensions.contributions.governance.disabledBadge')).toBe('已停用')
    expect(lookupMessage('en-US', 'admin.extensions.contributions.governance.disabledBadge')).toBe('Disabled')
  })
})

describe('共享规则编辑器的宿主族（发现式棘轮）', () => {
  test('宿主族从真实调用点推导：至少两族，且与当前实现一致', () => {
    // 反真空绿：一条调用点都扫不到时，下面的短键检查会变成永远通过。
    expect(RULE_HOST_PREFIXES.length).toBeGreaterThanOrEqual(2)
    // 新增宿主族必须同步更新这条断言 —— 这就是「新族不会静默逃逸」的检查点：
    // 忘了补 locale 键时，「前缀化短键在每个宿主族里都能查到」会先红；忘了登记族名时这条先红。
    expect(RULE_HOST_PREFIXES).toEqual(['admin.badges.rule', 'admin.forum.trustLevels.rule'])
  })

  test('显式属性优先；未传时回落共享组件的默认前缀', () => {
    expect(hostPrefixFromSource(' i18n-key-prefix="admin.forum.trustLevels.rule"')).toBe('admin.forum.trustLevels.rule')
    // 徽章页不传该属性（依赖组件默认值）→ 解析结果为空，由 discoverRuleHostPrefixes 回落到默认值，
    // 因此 `admin.badges.rule` 仍出现在发现集合里（上一条断言已钉住）。
    expect(hostPrefixFromSource(' :model-value="draftRule"')).toBeNull()
  })

  test('动态绑定直接报错：静态扫描读不到前缀时必须显式失败', () => {
    expect(() => hostPrefixFromSource(' :i18n-key-prefix="somePrefix"')).toThrow()
  })
})
