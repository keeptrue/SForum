import { describe, expect, test } from 'bun:test'
import { computed, ref, unref } from 'vue'
import { useSForumSeo } from '../../app/composables/seo/useSForumSeo'
import type { SEOResolverSettings } from '../../app/utils/seo/seoResolver'

/**
 * `useSForumSeo` 的 head 输出契约。
 *
 * 为什么要有这条：unhead v3 的 `MetaFlat` 没有 `keywords`（它只在 `MetaNames`
 * 字符串联合里），所以 keywords 从 `useSeoMeta` 移到了 `useHead` 的 `meta`。
 * 迁移后「关键词到底还在不在 head 里」只有浏览器能看见，而本机 dev 的首页关键词
 * 是空的 —— 那意味着这条路径在真实渲染里永远是「不产出」，测不出回归。
 *
 * 这里把 `useHead` / `useSeoMeta` 的入参捕获下来，直接断言最终会写进 head 的条目：
 * 有配置时恰好一条 `{ name: 'keywords', content }`，没配置时一条都没有。
 * 断言对象是**入参结构**而不是源码字符串，改写法不会假绿。
 */

const baseSettings = (overrides: Partial<SEOResolverSettings> = {}): SEOResolverSettings => ({
  siteName: 'SForum',
  siteUrl: 'https://example.com',
  seoSiteName: 'SForum',
  homeTitle: 'SForum',
  homeDescription: 'Community discussions.',
  homeKeywords: '',
  homeOGTitle: '',
  homeOGDescription: '',
  homeOGImageUrl: '',
  pageTitleTemplate: '{pageTitle} | {seoSiteName}',
  pageDefaultDescription: 'Community discussions.',
  allowIndexing: true,
  policies: {
    home: { titleTemplate: '{seoSiteName}', descriptionSources: ['content', 'site_default'], defaultImageUrl: '', indexMode: 'index', includeInSitemap: true, schemaType: 'WebSite' },
    category: { titleTemplate: '{categoryName} | {seoSiteName}', descriptionSources: ['content', 'site_default'], defaultImageUrl: '', indexMode: 'index', includeInSitemap: true, schemaType: 'CollectionPage' },
    tag: { titleTemplate: '{tagName} | {seoSiteName}', descriptionSources: ['content', 'site_default'], defaultImageUrl: '', indexMode: 'index', includeInSitemap: true, schemaType: 'CollectionPage' },
    topic: { titleTemplate: '{topicTitle} | {seoSiteName}', descriptionSources: ['summary', 'excerpt', 'site_default'], defaultImageUrl: '', indexMode: 'index', includeInSitemap: true, schemaType: 'DiscussionForumPosting' },
    profile: { titleTemplate: '{authorName} | {seoSiteName}', descriptionSources: ['content', 'site_default'], defaultImageUrl: '', indexMode: 'noindex', includeInSitemap: false, schemaType: 'ProfilePage' },
    static: { titleTemplate: '{pageTitle} | {seoSiteName}', descriptionSources: ['content', 'site_default'], defaultImageUrl: '', indexMode: 'index', includeInSitemap: true, schemaType: 'WebPage' }
  },
  ...overrides
})

type HeadMetaEntry = { name?: string, property?: string, content?: string, key?: string }

const seoMetaInputs: Record<string, unknown>[] = []
const headInputs: unknown[] = []

/** 装好 Nuxt 自动导入的桩：只换上下文来源，`resolveSEO` 等纯函数保持真实实现。 */
function install(options: { homeKeywords?: string, indexable?: boolean } = {}) {
  seoMetaInputs.length = 0
  headInputs.length = 0
  const globals = globalThis as unknown as Record<string, unknown>
  globals.computed = computed
  globals.unref = unref
  globals.useRoute = () => ({ path: '/' })
  globals.useSeoMeta = (input: Record<string, unknown>) => { seoMetaInputs.push(input) }
  globals.useHead = (input: unknown) => { headInputs.push(input) }
  globals.useWebOptions = () => ({
    siteUrl: ref('https://example.com'),
    seoIndexable: ref(options.indexable ?? true),
    seoSettings: ref(baseSettings({ homeKeywords: options.homeKeywords ?? '' }))
  })
}

/** `useHead` 收到的是工厂函数（保证响应式）；取最后一次调用的求值结果。 */
function headMeta(): HeadMetaEntry[] {
  const input = headInputs.at(-1)
  const resolved = typeof input === 'function' ? (input as () => { meta?: HeadMetaEntry[] })() : input as { meta?: HeadMetaEntry[] }
  return resolved?.meta ?? []
}

describe('useSForumSeo head output', () => {
  test('emits exactly one keywords meta when the site configured homepage keywords', () => {
    install({ homeKeywords: '开源论坛, 社区讨论' })

    useSForumSeo({ title: '首页' })

    const keywords = headMeta().filter(entry => entry.name === 'keywords')
    expect(keywords).toHaveLength(1)
    expect(keywords[0]!.content).toBe('开源论坛, 社区讨论')
    // 同一份 head 里 robots 也必须只有一条（迁移时最容易顺手写重）。
    expect(headMeta().filter(entry => entry.name === 'robots')).toHaveLength(1)
  })

  test('emits no keywords meta when the setting is empty', () => {
    install({ homeKeywords: '' })

    useSForumSeo({ title: '首页' })

    expect(headMeta().filter(entry => entry.name === 'keywords')).toHaveLength(0)
    expect(headMeta().filter(entry => entry.name === 'robots')).toHaveLength(1)
  })

  test('keeps keywords out of the useSeoMeta payload (unhead v3 MetaFlat has no keywords)', () => {
    install({ homeKeywords: '开源论坛' })

    useSForumSeo({ title: '首页' })

    expect(seoMetaInputs).toHaveLength(1)
    expect(Object.keys(seoMetaInputs[0]!)).not.toContain('keywords')
    expect(Object.keys(seoMetaInputs[0]!)).toContain('title')
  })

  test('noindex sites still get the keywords entry (robots and keywords are independent)', () => {
    install({ homeKeywords: '内部社区', indexable: false })

    useSForumSeo({ title: '首页' })

    const meta = headMeta()
    expect(meta.filter(entry => entry.name === 'keywords')).toHaveLength(1)
    expect(meta.find(entry => entry.name === 'robots')?.content).toContain('noindex')
  })
})
