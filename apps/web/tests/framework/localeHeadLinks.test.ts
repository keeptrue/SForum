import { describe, expect, test } from 'bun:test'
import { localeHeadLinks, localeHeadMeta } from '../../app/utils/seo/localeHeadLinks'

// @nuxtjs/i18n 的 useLocaleHead() 只给出 Record<string, string>[]；这两个适配器
// 负责把它重建成 unhead v3 的判别联合能接受的形状。运行时值必须原样保留，
// 因为 hreflang / canonical 是 SEO 归属代码的输出，不能被适配层改写。
describe('i18n locale head adapters', () => {
  test('keeps alternate links with hreflang and key', () => {
    expect(localeHeadLinks([
      { key: 'i18n-alt-zh-CN', rel: 'alternate', href: 'https://example.test/', hreflang: 'zh-CN' },
      { key: 'i18n-xd', rel: 'alternate', href: 'https://example.test/', hreflang: 'x-default' }
    ])).toEqual([
      { key: 'i18n-alt-zh-CN', rel: 'alternate', href: 'https://example.test/', hreflang: 'zh-CN' },
      { key: 'i18n-xd', rel: 'alternate', href: 'https://example.test/', hreflang: 'x-default' }
    ])
  })

  test('keeps the canonical link i18n emits', () => {
    expect(localeHeadLinks([
      { key: 'i18n-can', rel: 'canonical', href: 'https://example.test/t/1' }
    ])).toEqual([
      { key: 'i18n-can', rel: 'canonical', href: 'https://example.test/t/1' }
    ])
  })

  test('drops entries without href or with an unknown rel', () => {
    expect(localeHeadLinks([
      { key: 'broken', rel: 'alternate' },
      { key: 'weird', rel: 'preconnect', href: 'https://example.test' }
    ])).toEqual([])
  })

  test('returns an empty list for the default seo:false locale head', () => {
    expect(localeHeadLinks()).toEqual([])
    expect(localeHeadLinks([])).toEqual([])
  })

  test('maps og meta by property and keeps name-form meta', () => {
    expect(localeHeadMeta([
      { key: 'i18n-og-url', property: 'og:url', content: 'https://example.test/' },
      { key: 'i18n-og', property: 'og:locale', content: 'zh_CN' },
      { key: 'plain', name: 'description', content: 'desc' }
    ])).toEqual([
      { key: 'i18n-og-url', property: 'og:url', content: 'https://example.test/' },
      { key: 'i18n-og', property: 'og:locale', content: 'zh_CN' },
      { key: 'plain', name: 'description', content: 'desc' }
    ])
  })

  test('drops meta without content', () => {
    expect(localeHeadMeta([{ key: 'empty', property: 'og:url' }])).toEqual([])
    expect(localeHeadMeta()).toEqual([])
  })
})
