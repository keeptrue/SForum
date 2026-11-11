import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { BADGE_METRIC_OPTIONS, adminBadgeMetricLabel } from '~/utils/admin/adminBadges'

/**
 * 徽章后台 i18n 的**双向**契约（K-01b）。
 *
 * 上一刀漏了整个 `admin.badges.*` 命名空间：后台页表头/表单/Toast 全是裸键。
 * 只查「locale 里有这些键」不够——那会漏掉**多余键**（死文案）。所以两个方向都断言：
 * 用到的键必须都在 locale 里，locale 里的键也必须都被用到。
 *
 * 文案来源：`knowledge/reports/2026-11-11-badges-admin-i18n-copy.md`（74 键）
 * + `toast.recomputeChanged`（本次新增，不在表里，避免第二份真值）。
 */
const appRoot = new URL('../../app/', import.meta.url)
const localesRoot = new URL('../../i18n/locales/', import.meta.url)

function readJson(path: URL): Record<string, unknown> {
  return JSON.parse(readFileSync(path, 'utf8')) as Record<string, unknown>
}

const zh = readJson(new URL('zh-CN.json', localesRoot))
const en = readJson(new URL('en-US.json', localesRoot))

function flatten(node: unknown, prefix = ''): Set<string> {
  const out = new Set<string>()
  if (!node || typeof node !== 'object') return out
  for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      for (const child of flatten(value, path)) out.add(child)
    } else {
      out.add(path)
    }
  }
  return out
}

function lookup(node: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (current, key) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined),
    node
  )
}

/** 源码里出现的 `admin.badges.*` 引用（含动态拼接前缀，末尾带 `.`）。 */
function referencedBadgeKeys(): { literal: Set<string>, prefixes: Set<string> } {
  const literal = new Set<string>()
  const prefixes = new Set<string>()
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name)
      if (statSync(full).isDirectory()) {
        walk(full)
        continue
      }
      if (!/\.(vue|ts)$/.test(name)) continue
      const source = readFileSync(full, 'utf8')
      for (const match of source.matchAll(/admin\.badges\.[A-Za-z0-9_.]+/g)) {
        const key = match[0]
        if (key.endsWith('.')) prefixes.add(key.slice(0, -1))
        else literal.add(key)
      }
    }
  }
  walk(appRoot.pathname)
  return { literal, prefixes }
}

// 动态前缀的枚举来源：`grantMode.` 来自 EditDialog 的 `['automatic','manual']`，
// `metrics.` 来自 `BADGE_METRIC_OPTIONS`（单一来源，不在这里另抄一份）。
// `columns.` 来自 SFAdminBadgeTable 的 sortAria（列头排序 aria 按列 id 拼键）。
const DYNAMIC_ENUMERATIONS: Record<string, readonly string[]> = {
  'admin.badges.columns': ['actions', 'enabled', 'grantMode', 'grantedCount', 'group', 'name', 'rule', 'tier'],
  'admin.badges.grantMode': ['automatic', 'manual'],
  'admin.badges.metrics': BADGE_METRIC_OPTIONS
}

describe('admin.badges i18n namespace', () => {
  test('defines every referenced key in both locales, and nothing more', () => {
    const { literal, prefixes } = referencedBadgeKeys()
    const used = new Set(literal)
    for (const prefix of prefixes) {
      const values = DYNAMIC_ENUMERATIONS[prefix]
      expect(values).toBeDefined()
      for (const value of values!) used.add(`${prefix}.${value}`)
    }

    for (const [name, locale] of [['zh-CN', zh], ['en-US', en]] as const) {
      // flatten 从传入节点起算，这里补回命名空间前缀，两侧才同一基准。
      const defined = new Set([...flatten(lookup(locale, 'admin.badges'))].map(key => `admin.badges.${key}`))
      const missing = [...used].filter(key => !defined.has(key)).sort()
      // 多余键 = 死文案：上一刀就是没人查这一侧。
      const extra = [...defined].filter(key => !used.has(key)).sort()
      expect({ locale: name, missing, extra }).toEqual({ locale: name, missing: [], extra: [] })
    }
  })

  test('keeps zh-CN and en-US key sets identical', () => {
    expect([...flatten(lookup(zh, 'admin.badges'))].sort())
      .toEqual([...flatten(lookup(en, 'admin.badges'))].sort())
  })

  test('has no unreplaced placeholders and no bare-digit copy', () => {
    const strings = [...flatten(lookup(zh, 'admin.badges'))].map(key => String(lookup(zh, `admin.badges.${key}`)))
    for (const value of strings) {
      // 每条文案要么没有插值，要么插值名在文案里闭合。
      expect(value).not.toMatch(/\{[^}]*$/)
    }
  })
})

describe('badge rule metric copy (4-1)', () => {
  const translate = (key: string) => {
    const value = lookup(zh, key)
    if (typeof value !== 'string') throw new Error(`missing locale key: ${key}`)
    return value
  }
  const has = (key: string) => typeof lookup(zh, key) === 'string'

  test('renders localized metric names instead of backend enum values', () => {
    // 变异方向：把 adminBadgeMetricLabel 的实现改成 `return metric` → 必红。
    for (const metric of BADGE_METRIC_OPTIONS) {
      const label = adminBadgeMetricLabel(metric, translate, has)
      expect(label).not.toMatch(/_count$|^days_visited$/)
    }
    expect(adminBadgeMetricLabel('topic_count', translate, has)).toBe('主题数')
    expect(adminBadgeMetricLabel('like_received_count', translate, has)).toBe('获赞数')
  })

  test('falls back to the raw value only when the catalog has no copy', () => {
    // 静默变成空字符串比裸 slug 更糟：运营至少还能看出是哪个指标。
    expect(adminBadgeMetricLabel('brand_new_metric', translate, has)).toBe('brand_new_metric')
    expect(adminBadgeMetricLabel(undefined, translate, has)).toBe('')
  })

  test('the table wires the helper in, so the rule cell cannot print a slug', () => {
    const source = readFileSync(new URL('../../app/components/admin/badges/SFAdminBadgeTable.vue', import.meta.url), 'utf8')
    expect(source).toContain('adminBadgeMetricLabel(ruleOf(row.original)?.metric, t, te)')
    expect(source).not.toContain('metric: ruleOf(row.original)?.metric')
  })
})

describe('automatic badge grant copy (4-2)', () => {
  test('the warning describes the real rule, not a grant state', () => {
    for (const locale of [zh, en]) {
      const value = lookup(locale, 'admin.badges.grant.automaticNotGrantable')
      expect(typeof value).toBe('string')
      expect(String(value)).not.toMatch(/已授予|already granted/i)
    }
    // 旧键名与用途不符，已改名；留着就是两套说法。
    expect(lookup(zh, 'admin.badges.grant.alreadyGranted')).toBeUndefined()
  })

  test('the dialog shows it only for automatic badges', () => {
    const source = readFileSync(
      new URL('../../app/components/admin/badges/SFAdminBadgeGrantDialog.vue', import.meta.url),
      'utf8'
    )
    expect(source).toContain('v-if="!manual"')
    expect(source).toContain("t('admin.badges.grant.automaticNotGrantable')")
  })
})

describe('recompute toast copy (4-3)', () => {
  test('describes how many users were affected instead of printing a bare number', () => {
    const template = lookup(zh, 'admin.badges.toast.recomputeChanged')
    expect(typeof template).toBe('string')
    expect(String(template)).toContain('{count}')
    expect(String(template).replace('{count}', '7')).toBe('本次影响 7 个用户。')

    const source = readFileSync(new URL('../../app/pages/admin/badges.vue', import.meta.url), 'utf8')
    expect(source).not.toContain('String(changed)')
    expect(source).toContain("t('admin.badges.toast.recomputeChanged', { count: changed })")
  })
})
