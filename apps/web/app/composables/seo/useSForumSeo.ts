import type { ComputedRef, Ref } from 'vue'
import type { SEOSettings } from '../useWebOptions'
import { resolveSEO, type SEOPageContext } from '~/utils/seo/seoResolver'
import { buildSEOStructuredData, serializeStructuredData } from '~/utils/seo/seoStructuredData'

type MaybeReactive<T> = T | Ref<T> | ComputedRef<T> | (() => T)

export type SForumSchemaInput = {
  type?: 'WebPage' | 'DiscussionForumPosting'
  datePublished?: string
  dateModified?: string
  authorName?: string
}

export type SForumSEOInput = {
  title: MaybeReactive<string>
  description?: MaybeReactive<string | undefined>
  path?: MaybeReactive<string | undefined>
  image?: MaybeReactive<string | undefined>
  type?: MaybeReactive<'website' | 'article'>
  noindex?: MaybeReactive<boolean | undefined>
  nofollow?: MaybeReactive<boolean | undefined>
  schema?: MaybeReactive<SForumSchemaInput | undefined>
}

export const useSForumSeo = (input: SForumSEOInput | MaybeReactive<SEOPageContext>) => {
  const route = useRoute()
  const { siteUrl, seoSettings, seoIndexable } = useWebOptions()

  const pageContext = computed(() => normalizePageContext(input, route.path || '/'))
  const resolved = computed(() => resolveSEO({
    ...seoSettings.value,
    siteUrl: siteUrl.value,
    allowIndexing: seoIndexable.value
  }, pageContext.value))

  useSeoMeta({
    title: () => resolved.value.title,
    description: () => resolved.value.description,
    ogTitle: () => resolved.value.ogTitle,
    ogDescription: () => resolved.value.ogDescription,
    ogType: () => pageContext.value.type === 'topic' ? 'article' : 'website',
    ogUrl: () => resolved.value.canonicalUrl,
    ogImage: () => resolved.value.image || undefined,
    twitterCard: () => seoSettings.value.twitterCard,
    twitterSite: () => seoSettings.value.twitterSite || undefined,
    twitterTitle: () => resolved.value.ogTitle,
    twitterDescription: () => resolved.value.ogDescription,
    twitterImage: () => resolved.value.image || undefined
  })

  useHead(() => ({
    link: [
      { key: 'canonical', rel: 'canonical', href: resolved.value.canonicalUrl }
    ],
    meta: [
      { key: 'robots', name: 'robots', content: resolved.value.robots },
      // unhead v3 的 MetaFlat 里没有 keywords（它只存在于 MetaNames 字符串联合），
      // 所以 keywords 走 useHead 的 meta 而不是 useSeoMeta；SEO 归属仍在本文件。
      ...(resolved.value.keywords
        ? [{ key: 'keywords', name: 'keywords', content: resolved.value.keywords }]
        : []),
      ...verificationMeta(seoSettings.value)
    ],
    script: structuredDataScript({
      settings: seoSettings.value,
      siteName: resolved.value.siteName,
      siteUrl: siteUrl.value,
      canonicalUrl: resolved.value.canonicalUrl,
      title: resolved.value.title,
      description: resolved.value.description,
      image: resolved.value.image,
      resolved: resolved.value
    })
  }))

  return {
    title: computed(() => resolved.value.title),
    description: computed(() => resolved.value.description),
    canonicalUrl: computed(() => resolved.value.canonicalUrl),
    robotsRule: computed(() => resolved.value.robots),
    resolved
  }
}

function normalizePageContext(input: SForumSEOInput | MaybeReactive<SEOPageContext>, routePath: string): SEOPageContext {
  const value = resolveReactive(input as MaybeReactive<SForumSEOInput | SEOPageContext>)
  if (value && isPageContext(value)) {
    return value
  }
  const legacy = value as SForumSEOInput | undefined
  const title = resolveReactive(legacy?.title)?.trim() || ''
  const path = resolveReactive(legacy?.path) || routePath
  const schema = resolveReactive(legacy?.schema)
  return {
    type: schema?.type === 'DiscussionForumPosting' ? 'topic' : path === '/' ? 'home' : 'static',
    path,
    title,
    description: resolveReactive(legacy?.description),
    image: resolveReactive(legacy?.image),
    noindex: resolveReactive(legacy?.noindex),
    nofollow: resolveReactive(legacy?.nofollow),
    variables: {
      pageTitle: title,
      topicTitle: title,
      authorName: schema?.authorName
    },
    datePublished: schema?.datePublished,
    dateModified: schema?.dateModified,
    authorName: schema?.authorName
  }
}

function isPageContext(value: SForumSEOInput | SEOPageContext): value is SEOPageContext {
  return ['home', 'category', 'tag', 'topic', 'profile', 'static'].includes(String(value.type))
}

function resolveReactive<T>(value: MaybeReactive<T> | undefined): T | undefined {
  if (typeof value === 'function') {
    return (value as () => T)()
  }
  return unref(value)
}

function verificationMeta(settings: SEOSettings) {
  return [
    settings.googleVerification ? { key: 'google-site-verification', name: 'google-site-verification', content: settings.googleVerification } : null,
    settings.bingVerification ? { key: 'msvalidate.01', name: 'msvalidate.01', content: settings.bingVerification } : null,
    settings.baiduVerification ? { key: 'baidu-site-verification', name: 'baidu-site-verification', content: settings.baiduVerification } : null,
    settings.yandexVerification ? { key: 'yandex-verification', name: 'yandex-verification', content: settings.yandexVerification } : null
  ].filter((item): item is { key: string, name: string, content: string } => item !== null)
}

function structuredDataScript(input: {
  settings: SEOSettings
  siteName: string
  siteUrl: string
  canonicalUrl: string
  title: string
  description: string
  image: string
  resolved: ReturnType<typeof resolveSEO>
}) {
  if (!input.settings.schemaOrgEnabled) {
    return []
  }

  const graph = buildSEOStructuredData({
    siteUrl: input.siteUrl, siteName: input.siteName, canonicalUrl: input.canonicalUrl,
    title: input.title, description: input.description, image: input.image,
    schemaType: input.resolved.schemaType, indexable: !input.resolved.robots.startsWith('noindex'),
    searchActionEnabled: input.settings.schemaOrgSearchActionEnabled,
    discussionEnabled: input.settings.schemaOrgDiscussionEnabled,
    organizationLogoUrl: input.settings.schemaOrgOrganizationLogoUrl,
    breadcrumbs: input.resolved.context.breadcrumbs,
    authorName: input.resolved.context.authorName,
    datePublished: input.resolved.context.datePublished,
    dateModified: input.resolved.context.dateModified
  })

  return [
    {
      key: 'sforum-schema-org',
      // unhead v3 把 script.type 收窄成按类型判别的字面量（JsonLdScript 要求
      // 'application/ld+json'），因此这里必须保留字面量类型而不是 string。
      type: 'application/ld+json' as const,
      innerHTML: serializeStructuredData(graph)
    }
  ]
}
