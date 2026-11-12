import { describe, expect, test } from 'bun:test'
import { existsSync, readFileSync } from 'node:fs'
import {
  DEFAULT_TIER_ICON_NAMES,
  FALLBACK_TIERS,
  TL0_TIER,
  defaultTierIconName,
  ladderTierName,
  parseTrustLevelCatalog,
  tierIconClass,
  trustLevelIconOf,
  trustLevelLadder,
  trustLevelLadderMax,
  trustLevelName,
  trustLevelTierName,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { TIER_COLOR_TOKENS } from '~/utils/profile/trustLevelColors'

/**
 * D-N3 四层回退链 + 档位身份透传。
 *
 * 第三层（内置五档 i18n）与第四层（TL{rank}）的区别是「有没有可读的运营名」；
 * 最关键的一条是：**服务端的档位身份永不被本地数组改写**（lead 报告里的必修项）。
 */

const translate = (dict: Record<string, string>) => (key: string) => dict[key] ?? key

const tier = (overrides: Partial<TrustLevelTier> = {}): TrustLevelTier => ({
  id: 'tier_9',
  name: { zh: '', en: '' },
  icon: null,
  ...overrides
})

describe('tier name fallback chain', () => {
  test('1) uses the stored name in the current locale', () => {
    const named = tier({ name: { zh: '种子', en: 'Seed' } })
    expect(trustLevelName(named, 'zh-CN', 3)).toBe('种子')
    expect(trustLevelName(named, 'en-US', 3)).toBe('Seed')
  })

  test('2) falls back to the other language when the current one is empty', () => {
    expect(trustLevelName(tier({ name: { zh: '', en: 'Seed' } }), 'zh-CN', 3)).toBe('Seed')
    expect(trustLevelName(tier({ name: { zh: '种子', en: '' } }), 'en-US', 3)).toBe('种子')
    expect(trustLevelTierName(tier({ name: { zh: '  ', en: '  ' } }), 'zh-CN')).toBe('')
  })

  test('3) uses the built-in i18n key only when the id hits a built-in tier', () => {
    const t = translate({ 'profile.trustLevel.name.member': '正式用户' })
    expect(trustLevelName(tier({ id: 'member' }), 'zh-CN', 1, t)).toBe('正式用户')
    // 未命中（运营自定义 id）→ 不走 i18n，直接 TL{rank}。
    expect(trustLevelName(tier({ id: 'tier_9' }), 'zh-CN', 9, t)).toBe('TL9')
  })

  test('4) falls back to TL{rank}, never to an empty string', () => {
    expect(trustLevelName(null, 'zh-CN', 0)).toBe('TL0')
    expect(trustLevelName(tier(), 'en-US', 7)).toBe('TL7')
    // i18n 未命中时 vue-i18n 原样返回键：那比 TL{rank} 更糟，必须被识别出来。
    const missingKeyTranslate = translate({})
    expect(trustLevelName(tier({ id: 'veteran' }), 'zh-CN', 3, missingKeyTranslate)).toBe('TL3')
  })
})

describe('icons', () => {
  test('normalises short names and accepts already-prefixed classes', () => {
    expect(tierIconClass('leaf')).toBe('i-lucide-leaf')
    expect(tierIconClass('i-lucide-leaf')).toBe('i-lucide-leaf')
    expect(tierIconClass('')).toBe('')
    expect(tierIconClass(null)).toBe('')
  })

  test('falls back to the rank sequence when no icon is stored', () => {
    expect(defaultTierIconName(0)).toBe('sprout')
    expect(defaultTierIconName(1)).toBe('leaf')
    // 超出 16 项序列后循环取，不返回 undefined（rank 8 现在是 flame，不是回到 sprout）。
    expect(defaultTierIconName(DEFAULT_TIER_ICON_NAMES.length)).toBe(defaultTierIconName(0))
    expect(defaultTierIconName(8)).toBe('flame')
    expect(trustLevelIconOf(tier(), 1)).toBe('i-lucide-leaf')
    expect(trustLevelIconOf(tier({ icon: 'crown' }), 1)).toBe('i-lucide-crown')
  })
})

describe('ladder', () => {
  test('puts the virtual TL0 at rank 0 and skips retired tiers', () => {
    const ladder = trustLevelLadder([
      { ...FALLBACK_TIERS[0]!, retired: true },
      FALLBACK_TIERS[1]!
    ])
    expect(ladder[0]!.id).toBe(TL0_TIER.id)
    expect(ladder).toHaveLength(2)
    expect(trustLevelLadderMax(ladder)).toBe(1)
  })

  test('clamps out-of-range ranks instead of returning undefined', () => {
    const ladder = trustLevelLadder(FALLBACK_TIERS)
    expect(ladderTierName(ladder, 99, 'en-US')).toBe(ladder[ladder.length - 1]!.name.en)
    expect(ladderTierName(ladder, -1, 'en-US')).toBe('TL0')
  })
})

describe('public read surface', () => {
  test('accepts the wire `levels[]` shape, with level + levelSlug, and sorts by level', () => {
    const catalog = parseTrustLevelCatalog({
      levels: [
        { level: 2, levelSlug: 'member', name: { zh: '正式用户', en: 'Member' }, icon: 'leaf', retired: false },
        { level: 1, levelSlug: 'seed', name: { zh: '种子', en: 'Seed' }, icon: 'sprout', retired: false },
        { level: 3, id: 'elder', name: { zh: '', en: 'Elder' }, icon: null, retired: true }
      ]
    })
    expect(catalog?.map(entry => entry.id)).toEqual(['seed', 'member', 'elder'])
    expect(catalog?.[1]!.icon).toBe('leaf')
    expect(catalog?.[2]!.retired).toBe(true)
  })

  test('reads the wire displayName and skips the server-synthesised TL0 entry', () => {
    const catalog = parseTrustLevelCatalog({
      levels: [
        { level: 0, levelSlug: 'newcomer', displayName: { zh: '新用户', en: 'Newcomer' }, icon: 'sprout', editable: false },
        { level: 1, levelSlug: 'member', displayName: { zh: '正式用户', en: 'Member' }, icon: 'leaf', editable: true }
      ]
    })
    // TL0 由本地 trustLevelLadder 补，不重复出现。
    expect(catalog?.map(entry => entry.id)).toEqual(['member'])
    expect(catalog?.[0]!.name).toEqual({ zh: '正式用户', en: 'Member' })
  })

  test('keeps an operator-defined identity untouched (never mapped back to a built-in slug)', () => {
    const catalog = parseTrustLevelCatalog({ levels: [{ level: 1, levelSlug: 'tier_9', name: { zh: '', en: '' } }] })
    expect(catalog?.[0]!.id).toBe('tier_9')
  })

  test('reads the wire color only from the whitelist, otherwise null (= follow the position)', () => {
    const catalog = parseTrustLevelCatalog({
      levels: [
        { level: 1, levelSlug: 'member', name: { zh: '', en: '' }, color: 'rose' },
        { level: 2, levelSlug: 'resident', name: { zh: '', en: '' }, color: 'chartreuse' },
        { level: 3, levelSlug: 'veteran', name: { zh: '', en: '' } }
      ]
    })
    expect(catalog?.[0]!.color).toBe('rose')
    // 白名单外的值不许进展示层：否则行内会写出一条 color-mix 失效的样式。
    expect(catalog?.[1]!.color).toBeNull()
    expect(catalog?.[2]!.color).toBeNull()
  })

  test('returns null for a missing catalog so callers can fall back', () => {
    expect(parseTrustLevelCatalog(null)).toBeNull()
    expect(parseTrustLevelCatalog({})).toBeNull()
    expect(parseTrustLevelCatalog({ levels: 'nope' })).toBeNull()
  })
})

describe('fallback icon names stay identical to the backend whitelist', () => {
  // 离线也可跑：直接读 Go 源码里的 `tierIcons` 变量，不依赖后端进程。
  const candidates = [
    new URL('../../../../../apps/api/app/Models/TrustLevels/tier_icons.go', import.meta.url),
    new URL('../../../api/app/Models/TrustLevels/tier_icons.go', import.meta.url)
  ]
  const goPath = candidates.find(url => existsSync(url)) ?? null

  test('the frontend fallback sequence equals tier_icons.go verbatim', () => {
    // 找不到后端文件也必须红：静默跳过等于没有护栏（这是本仓「两份真相」的入口）。
    expect(goPath).not.toBeNull()
    const source = readFileSync(goPath!, 'utf8')
    const block = source.match(/var tierIcons = \[\]string\{([\s\S]*?)\}/)
    expect(block).not.toBeNull()
    const backendIcons = [...block![1]!.matchAll(/"(\w+)"/g)].map(match => match[1]!)
    expect(backendIcons.length).toBe(16)
    // 顺序也是语义：前 5 项 = TL0..TL4 的默认图标。
    expect([...DEFAULT_TIER_ICON_NAMES]).toEqual(backendIcons)
  })
})

describe('levelSlug is passed through, never recomputed (regression)', () => {
  const source = readFileSync('app/composables/profile/useTrustLevel.ts', 'utf8')

  test('the public view adopts the server levelSlug verbatim', () => {
    expect(source).toContain('levelSlug: base.levelSlug')
    // 曾经按本地五元素数组重算 slug：那会把运营自定义的档位 id 静默改回内置 slug。
    expect(source).not.toContain('levelSlug: trustLevelSlug(level)')
  })

  test('TRUST_LEVEL_SLUGS / TRUST_LEVEL_MAX survive only as fallback sources', () => {
    // 内置五档 id 是「回退名 + 默认图标序列」的锚点，不再决定档位数量。
    expect(FALLBACK_TIERS.map(entry => entry.id)).toEqual(['member', 'resident', 'veteran', 'leader'])
    expect(TL0_TIER.id).toBe('newcomer')
    // 兜底档位数量与内置五档一致（TL0 单独一档）。
    expect(trustLevelLadder(FALLBACK_TIERS)).toHaveLength(5)
  })
})

describe('tier color names stay identical to the backend whitelist', () => {
  // 与上面图标那条同构：离线读 Go 源码，不依赖后端进程。
  // 色板已上提到 app/Support/Palette（徽章等级表与信任等级共用），TrustLevels 只剩薄包装：
  // 三个候选路径都试，找到哪份就用哪份 —— 护栏的对象是「同一份 9 色」，不是某个文件位置。
  const candidates = [
    new URL('../../../../../apps/api/app/Support/Palette/tier_colors.go', import.meta.url),
    new URL('../../../api/app/Support/Palette/tier_colors.go', import.meta.url),
    new URL('../../../../../apps/api/app/Models/TrustLevels/tier_colors.go', import.meta.url),
    new URL('../../../api/app/Models/TrustLevels/tier_colors.go', import.meta.url)
  ]
  const goPath = candidates.find(url => existsSync(url)) ?? null

  test('TIER_COLOR_TOKENS equals tier_colors.go verbatim', () => {
    // 找不到后端文件也必须红：静默跳过等于没有护栏（两份真相就是这样长出来的）。
    expect(goPath).not.toBeNull()
    const source = readFileSync(goPath!, 'utf8')
    const block = source.match(/var tierColors = \[\]string\{([\s\S]*?)\}/)
    expect(block).not.toBeNull()
    const backendColors = [...block![1]!.matchAll(/"([\w-]+)"/g)].map(match => match[1]!)
    expect(backendColors).toHaveLength(9)
    // 顺序也是语义：第 n 项就是 rank n 的默认色（TL0 = slate）。
    expect([...TIER_COLOR_TOKENS]).toEqual(backendColors)
  })

  test('TL0 and the fallback tiers carry their default colors', () => {
    expect(TL0_TIER.color).toBe('slate')
    // 兜底目录不可用时阶梯也要有色，否则整条阶梯掉成单色。
    expect(FALLBACK_TIERS.map(entry => entry.color))
      .toEqual(['teal', 'emerald', 'sky', 'indigo'])
  })
})
