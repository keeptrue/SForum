import { describe, expect, test } from 'bun:test'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'

/**
 * `/badges` 公共徽章目录的**遍历型**契约（B-15 / B-24 / B-27）。
 *
 * 徽章上一刀的结构性盲区：整块 `badges.*` 键缺失而 84 条断言 0 FAIL——因为没人扫源码。
 * 所以这里不人工核对键，两个方向都求差：
 * - 正向：源码里出现的 `badges.catalog.*` 键必须在两份 locale 里都有；
 * - 反向：locale 里的 `badges.catalog.*` 键必须都被源码用到（死文案也算失败）。
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

function sourceFiles(relative: string): string[] {
  const dir = join(appRoot.pathname, relative)
  const files: string[] = []
  const walk = (current: string) => {
    for (const name of readdirSync(current)) {
      const full = join(current, name)
      if (statSync(full).isDirectory()) {
        walk(full)
        continue
      }
      if (/\.(vue|ts)$/.test(name)) files.push(full)
    }
  }
  walk(dir)
  return files
}

/**
 * 扫描面 = 本页自己的 route shell / 岛 / composable，
 * 加上 `SFProfileBadgeTab.vue`：catalog 变体的标题、组计数、空态与「查看全部徽章」内链
 * 都落在那个共用组件里，漏扫它会让这些键在反向检查里被误判成死文案。
 */
const SCAN_SOURCES = [
  ...sourceFiles('pages/badges'),
  ...sourceFiles('components/forum/badges'),
  ...sourceFiles('composables/forum/badges'),
  join(appRoot.pathname, 'components/profile/SFProfileBadgeTab.vue')
]

/** 源码里出现的 `badges.catalog.*` 引用。 */
function referencedCatalogKeys(): { used: Set<string>, dynamic: Set<string> } {
  const used = new Set<string>()
  const dynamic = new Set<string>()
  for (const file of SCAN_SOURCES) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(/badges\.catalog\.[A-Za-z0-9_.]+/g)) {
      const key = match[0]
      if (key.endsWith('.')) dynamic.add(key.slice(0, -1))
      else used.add(key)
    }
  }
  return { used, dynamic }
}

/** designer 规范 §12 的七个键 + 资料页内链文案（B-27）。 */
const DESIGNED_KEYS = [
  'badges.catalog.anonymousHint',
  'badges.catalog.ariaLabelEarned',
  'badges.catalog.description',
  'badges.catalog.earned',
  'badges.catalog.emptyDescription',
  'badges.catalog.emptyTitle',
  'badges.catalog.groupTotal',
  'badges.catalog.headerCount',
  'badges.catalog.title',
  'badges.catalog.truncated',
  'badges.catalog.ungrouped',
  'badges.catalog.viewAll'
]

function definedCatalogKeys(locale: Record<string, unknown>): Set<string> {
  return new Set([...flatten(lookup(locale, 'badges.catalog'))].map(key => `badges.catalog.${key}`))
}

const routeShell = () => readFileSync(join(appRoot.pathname, 'pages/badges/index.vue'), 'utf8')
const catalogIsland = () => readFileSync(join(appRoot.pathname, 'components/forum/badges/SFBadgeCatalogPage.vue'), 'utf8')
const badgeTab = () => readFileSync(join(appRoot.pathname, 'components/profile/SFProfileBadgeTab.vue'), 'utf8')

describe('badges.catalog i18n namespace (B-24)', () => {
  test('defines every referenced key in both locales, and nothing more', () => {
    const { used, dynamic } = referencedCatalogKeys()
    // 目录键全部是字面量：一旦出现动态拼接，反向检查就失去意义，必须当场红。
    expect({ dynamic }).toEqual({ dynamic: new Set<string>() })

    for (const [name, locale] of [['zh-CN', zh], ['en-US', en]] as const) {
      const defined = definedCatalogKeys(locale)
      const missing = [...used].filter(key => !defined.has(key)).sort()
      const unused = [...defined].filter(key => !used.has(key)).sort()
      expect({ locale: name, missing, unused }).toEqual({ locale: name, missing: [], unused: [] })
    }
  })

  test('keeps zh-CN and en-US catalog key sets identical', () => {
    expect([...definedCatalogKeys(zh)].sort()).toEqual([...definedCatalogKeys(en)].sort())
  })

  test('pins the designed key list so a typo cannot pass as a rename', () => {
    expect([...definedCatalogKeys(zh)].sort()).toEqual(DESIGNED_KEYS)
  })
})

describe('/badges route shell (B-15)', () => {
  test('is a thin outlet shell that owns SEO through useSForumSeo', () => {
    const source = routeShell()
    expect(source).toContain('SFPageOutlet')
    expect(source).toContain('page="forum.badge.index"')
    expect(source).toContain('<SFBadgeCatalogPage')
    expect(source).toContain('useSForumSeo')
    // 公共页 title / canonical / robots 只能由 SEO resolver 拥有，不许再出现第二个 owner。
    expect(source).not.toContain('useSeoMeta')
  })

  test('the island reuses the shared badge component instead of a second card implementation', () => {
    const source = catalogIsland()
    expect(source).toContain('<SFProfileBadgeTab')
    expect(source).toContain('variant="catalog"')
    expect(source).not.toContain('sf-profile-badge-card')
  })
})

describe('badge tab variant contract', () => {
  test('profile stays the default variant, so the profile tab keeps its behaviour', () => {
    const source = badgeTab()
    expect(source).toContain("variant?: BadgeTabVariant")
    expect(source).toContain("variant: 'profile'")
    expect(source).toContain("isCatalog ? t('badges.catalog.title') : t('profile.tabs.badges')")
  })

  test('the profile tab links to the public catalog with a real anchor (B-27)', () => {
    const source = badgeTab()
    expect(source).toContain('<NuxtLink')
    expect(source).toContain("localePath('/badges')")
    expect(source).toContain("t('badges.catalog.viewAll')")
    // 真链接，不是 JS 改 location。
    expect(source).not.toContain('window.location')
    expect(source).not.toContain('location.href')
  })
})

/**
 * M4：主题模板 + 三路声明（template 路径 / templates[] / packageFiles[]）。
 * 只加后端映射或只加模板都会让岛渲染成空位——所以配对面在这里一起断言。
 */
const themeRoot = new URL('../../../../extensions/builtin/themes/sforum-default/', import.meta.url)
const themeJson = () => JSON.parse(readFileSync(new URL('theme.json', themeRoot), 'utf8')) as {
  pages: Array<{ id: string, action: string, target: string, template: string, contract: string }>
}
const manifest = () => JSON.parse(readFileSync(new URL('sforum.extension.json', themeRoot), 'utf8')) as {
  packageFiles: Array<{ digest: string, id: string, kind: string, path: string }>
  templates: Array<{ action: string, contractVersion: string, digest: string, id: string, path: string, themeOverrideKey: string, viewModelSchema: string }>
}
const badgeTemplate = () => readFileSync(new URL('templates/badges.html', themeRoot), 'utf8')
const themeTemplateVue = () => readFileSync(join(appRoot.pathname, 'components/SFThemeTemplate.vue'), 'utf8')

describe('/badges theme template (B-19)', () => {
  test('keeps the three-segment shell with the bare body island tag', () => {
    const html = badgeTemplate()
    expect(html.split('\n')[0]).toBe('<!-- sforum.page.badge_index@1 -->')
    expect(html).toContain('data-theme-owned="presentation"')
    expect(html).toContain('data-page="forum.badge.index"')
    expect(html).toContain('data-theme-region="body"')
    expect(html).toContain('<sf-navbar></sf-navbar>')
    expect(html).toContain('<sf-badge-index-page></sf-badge-index-page>')
    expect(html).toContain('<sf-footer></sf-footer>')
  })

  test('theme.json declares the page once, with the exact contract and template path', () => {
    const page = themeJson().pages.find(entry => entry.target === 'forum.badge.index')
    expect(page).toEqual({
      id: 'sforum.default-theme.badge_index',
      action: 'replace',
      target: 'forum.badge.index',
      template: 'templates/badges.html',
      contract: 'sforum.page.badge_index@1'
    })
    // 每个主题化页面都要有模板声明（反向：声明路径也要都在 packageFiles 里）。
    const declared = new Set(manifest().templates.map(entry => entry.path))
    expect(themeJson().pages.filter(entry => !declared.has(entry.template))).toEqual([])
    expect(manifest().templates.filter(entry => !manifest().packageFiles.some(file => file.path === entry.path))).toEqual([])
  })

  test('manifest declares the same path as packageFiles[].path and templates[].path', () => {
    const page = themeJson().pages.find(entry => entry.target === 'forum.badge.index')!
    const files = manifest().packageFiles.filter(entry => entry.path === page.template)
    const declarations = manifest().templates.filter(entry => entry.path === page.template)

    expect(files).toHaveLength(1)
    expect(declarations).toHaveLength(1)
    expect(files[0]!.kind).toBe('template')
    expect(files[0]!.id).toBe('sforum.default-theme.file.template.badges')
    expect(declarations[0]!.id).toBe('sforum.default-theme.template.badges')
    expect(declarations[0]!.themeOverrideKey).toBe(page.id)
    expect(declarations[0]!.viewModelSchema).toBe(page.contract)
    // digest 由 `extension digest --write` 重算，这里只钉「两份声明同一摘要、形状是 sha256」。
    expect(files[0]!.digest).toMatch(/^[0-9a-f]{64}$/)
    expect(declarations[0]!.digest).toBe(files[0]!.digest)
  })
})

describe('/badges body island registration (paired surfaces)', () => {
  test('registers the component id and the legacy tag in the frontend theme template maps', () => {
    const source = themeTemplateVue()
    expect(source).toContain("'forum.component.badge_index': defineAsyncComponent(() => import('./forum/badges/SFBadgeCatalogPage.vue'))")
    expect(source).toContain("'sf-badge-index-page': { componentId: 'forum.component.badge_index' }")
  })

  test('the island tag in the template resolves through that binding, not through a host slot', () => {
    const html = badgeTemplate()
    const body = html.slice(html.indexOf('data-theme-region="body"'), html.indexOf('<sf-footer>'))
    const tag = body.match(/<(sf-[a-z-]+)>/)?.[1]
    expect(tag).toBe('sf-badge-index-page')
    expect(themeTemplateVue()).toContain(`'${tag}': { componentId: 'forum.component.badge_index' }`)
  })
})

describe('catalog wire mapping (M5b / M4 修订)', () => {
  test('the whitelist now admits rule-engine progress and still bans grant and operator fields', () => {
    const composable = readFileSync(join(appRoot.pathname, 'composables/forum/badges/useBadgesCatalog.ts'), 'utf8')
    expect(composable).toContain("request<PublicBadgeCatalogResponse>('/badges')")
    // M4：进度改由规则引擎派生，公开面在「未获得 + 可测」时下发 current/threshold
    //（阈值本来就已经出现在 requirementText 里，藏数字只是自欺）。
    expect(composable).toContain('current')
    expect(composable).toContain('threshold')
    // 仍然禁止：授予时间、条件内部 metric、以及运营开关 enabled（判赋值，不判注释里的提及）。
    expect(composable).not.toContain('grantedAt:')
    expect(composable).not.toMatch(/\bmetric:/)
    expect(composable).not.toMatch(/\benabled:/)
    expect(composable).toContain("badges.catalog.ungrouped")
  })

  test('the mapper carries progress through and invents none of the banned fields', async () => {
    const { mapPublicBadgeCatalog } = await import('../../app/composables/forum/badges/useBadgesCatalog')
    const groups = mapPublicBadgeCatalog({
      groups: [{
        key: 'content',
        name: '',
        badges: [{
          slug: 'first-post',
          name: '初次发帖',
          description: '',
          icon: '',
          tier: 'bronze',
          group: 'content',
          earned: false,
          current: 3,
          threshold: 10
        }]
      }],
      truncated: false
    }, '未分组')

    const badge = groups[0]!.badges[0]!
    expect(badge.current).toBe(3)
    expect(badge.threshold).toBe(10)
    expect('grantedAt' in badge).toBe(false)
    expect('metric' in badge).toBe(false)
    expect('enabled' in badge).toBe(false)
    expect(groups[0]!.name).toBe('未分组')
  })

  test('the island surfaces truncation and owns no cache logic', () => {
    const island = catalogIsland()
    expect(island).toContain("t('badges.catalog.truncated')")
    expect(island).toContain('role="status"')
    expect(island).not.toContain('Cache-Control')
    expect(island).not.toContain('progress')
  })
})
