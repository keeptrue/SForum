import { describe, expect, test } from 'bun:test'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { SPECIFIC_ERROR_CODES, saveErrorText } from '../../../app/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'

/**
 * `admin.forum.trustLevels` 的**死键棘轮**。
 *
 * 为什么需要它：这个族有 360 个键，而唯一的双向断言（`adminBadgesI18n.test.ts`）只覆盖
 * `admin.badges`。pm 的 copy-spec §3.2 那三条键（面板标题 / 空态标题 / `impact.row`）
 * 就是因为「没人能自动发现它们是死键」被否掉的——本轮 F1 删了 `earnable_not_declared` 的
 * 变体键，如果只删文案不加闸门，下一次还会留下孤儿键。
 *
 * 做法与徽章那份同源：把 locale 键集合与代码里实际取值的集合比对。
 * 两个必然的「非字面量消费」必须显式声明，否则会误判成死键：
 * 1. **动态族**（`metric.${key}` / `errors.code.${code}` 这类）→ 整族算已使用；
 * 2. **三元拼接**（`${mode === 'delete' ? 'tierDelete' : 'tiers.retireConfirm'}.cancel`）
 *    → 静态扫描拿不到具体键，这里列出解析后的 4 条；
 * 3. **前缀 prop**：共享规则编辑器读 `admin.forum.trustLevels.rule.*`（整族由 prop 注入，
 *    键清单在 `trustLevelRuleI18n.test.ts` 里逐键钉死）。
 *
 * `KNOWN_DEAD_KEYS` 是本轮盘点出的**存量**孤儿键（≥ `9396dd155` 之前就在，不是 F1 引入的）。
 * 它们是棘轮：只许减不许增。谁给它们接上消费者，就把对应行从这里删掉；谁新增一条无消费的
 * locale 键，这个测试立刻红。
 */
const NAMESPACE = ['admin', 'forum', 'trustLevels'] as const

const KNOWN_DEAD_KEYS = [
  'earnable.aria.cumulative',
  'errors.tierTitle',
  'icons.label',
  'level.leader',
  'level.member',
  'level.newcomer',
  'level.resident',
  'level.veteran',
  'tiers.icon'
].sort()

/** 模板拼接的静态头（`X.${...}` → 记 `X`），整族算已使用。 */
const DYNAMIC_FAMILIES = [
  'colors',
  'conditions.impact.method',
  'errors.code',
  'errors.field',
  'errors.locale',
  'limits.fields',
  'metric',
  'restoreConfirm.items',
  'tabs',
  'tierDelete.items'
] as const

/** 三元拼接解析出的具体键（扫描器读不到分支）。 */
const DYNAMIC_KEYS = [
  'tierDelete.cancel',
  'tierDelete.confirm',
  'tiers.retireConfirm.cancel',
  'tiers.retireConfirm.confirm'
] as const

/** 共享编辑器经前缀 prop 读取的整族。 */
const PREFIXED_FAMILIES = ['rule'] as const

const zh = JSON.parse(
  readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function flatten(node: unknown, prefix = ''): string[] {
  if (node && typeof node === 'object' && !Array.isArray(node)) {
    return Object.entries(node as Record<string, unknown>)
      .flatMap(([key, child]) => flatten(child, prefix ? `${prefix}.${key}` : key))
  }
  return [prefix]
}

function namespace(): Record<string, unknown> {
  return NAMESPACE.reduce<unknown>(
    (node, key) => (node as Record<string, unknown>)[key],
    zh
  ) as Record<string, unknown>
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) sourceFiles(full, out)
    else if (/\.(vue|ts)$/.test(name)) out.push(full)
  }
  return out
}

/** 代码里出现的字面量引用（含 `${...}` 拼接的静态头）。 */
function referenced(): { literal: Set<string>, dynamic: Set<string>, prefixed: Set<string> } {
  const literal = new Set<string>()
  const dynamic = new Set<string>()
  const prefixed = new Set<string>()
  for (const file of sourceFiles(new URL('../../../app/', import.meta.url).pathname)) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(new RegExp(`admin\\.forum\\.trustLevels\\.([A-Za-z0-9_.${'$'}{]+)`, 'g'))) {
      const rest = match[1] as string
      if (rest.includes('${')) {
        const head = rest.split('${')[0]!.replace(/\.$/, '')
        // 空头 = `admin.forum.trustLevels.${…}`，三元拼接在 DYNAMIC_KEYS 里显式声明。
        if (head) dynamic.add(head)
        continue
      }
      literal.add(rest.replace(/\.$/, ''))
    }
    // 共享编辑器：前缀经 prop 传入，整族由它消费。
    if (/i18n-key-prefix="admin\.forum\.trustLevels\.rule"|i18nKeyPrefix:\s*'admin\.forum\.trustLevels\.rule'/.test(source)) {
      prefixed.add('rule')
    }
  }
  return { literal, dynamic, prefixed }
}

describe('admin.forum.trustLevels 死键棘轮', () => {
  test('locale 里没有**新增**的无消费者键（存量孤儿键逐条登记）', () => {
    const defined = flatten(namespace()).sort()
    const { literal, dynamic, prefixed } = referenced()

    const used = new Set<string>(literal)
    for (const key of DYNAMIC_KEYS) used.add(key)
    for (const family of [...DYNAMIC_FAMILIES, ...prefixed]) {
      for (const key of defined) {
        if (key === family || key.startsWith(`${family}.`)) used.add(key)
      }
    }
    // 家族声明本身（`errors.code` 这类）不是叶子键，从 defined 侧不会出现，无需特判。

    const unused = defined.filter(key => !used.has(key)).sort()
    // 棘轮：集合必须与登记表**完全相等** —— 新增孤儿键红，已接上消费者的存量键没删也红。
    expect(unused).toEqual([...KNOWN_DEAD_KEYS])
  })

  test('`errors.code.*` 的条数 == specific 码表条数（变体键不能悄悄回来）', () => {
    // 不写死那条被删掉的键名：它一旦回来就会被上面的棘轮判成孤儿键；这里再用**条数**
    // 钉一次，防止「有人给变体键补了消费者」把棘轮喂绿。
    for (const locale of ['zh-CN', 'en-US']) {
      const doc = JSON.parse(
        readFileSync(new URL(`../../../i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      ) as Record<string, unknown>
      const keys = flatten(namespaceOf(doc)).filter(key => key.startsWith('errors.code.'))
      expect({ locale, count: keys.length }).toEqual({ locale, count: SPECIFIC_ERROR_CODES.length })
      expect(keys).toContain('errors.code.earnable_not_declared')
    }
  })
})

function namespaceOf(doc: Record<string, unknown>): unknown {
  return NAMESPACE.reduce<unknown>((node, key) => (node as Record<string, unknown>)[key], doc)
}

/**
 * F1 的行为面：变体分支删掉后，`earnable_not_declared` 必须**只有一句**通用文案，
 * 且后端即使还带着旧字段也不能把渲染搞成空串。
 */
describe('F1：earnable_not_declared 只剩单一成因的一句', () => {
  const doc = zh
  const lookup = (key: string): unknown => key.split('.').reduce<unknown>(
    (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
    doc
  )
  const t = (key: string, params: Record<string, string | number> = {}) => {
    const value = lookup(key)
    if (typeof value !== 'string') return `[${key}]`
    return value.replace(/\{(\w+)\}/g, (_m, name: string) => String(params[name] ?? `{${name}}`))
  }
  const te = (key: string) => typeof lookup(key) === 'string'

  test('渲染出的是那一句通用文案（不是原始键、不是空串）', () => {
    const text = saveErrorText(
      { rank: 2, tierId: 'member', field: 'earnable', reasonCode: 'earnable_not_declared', reason: '', params: {} },
      t,
      te
    )
    expect(text).toBe(t('admin.forum.trustLevels.errors.code.earnable_not_declared'))
    expect(text).not.toMatch(/^\[/)
    expect(text.length).toBeGreaterThan(0)
  })

  test('后端若仍带旧的成因字段，也只是被忽略 —— 不会拼出第二条文案', () => {
    const withStrayParam = saveErrorText(
      {
        rank: 2,
        tierId: 'member',
        field: 'earnable',
        reasonCode: 'earnable_not_declared',
        reason: '',
        params: { cause: 'anything', plugin: 'acme-crm' }
      },
      t,
      te
    )
    expect(withStrayParam).toBe(t('admin.forum.trustLevels.errors.code.earnable_not_declared'))
    // 旧变体句里的占位符不能被拼进通用句（缺键残留会渲染成字面 `{plugin}`）。
    expect(withStrayParam).not.toContain('{plugin}')
    expect(withStrayParam).not.toContain('acme-crm')
  })
})
