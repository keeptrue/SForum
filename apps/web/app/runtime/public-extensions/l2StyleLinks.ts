import type { PublicFrontendAssetReference } from './types'

export type PublicL2StyleLink = {
  rel: 'stylesheet'
  href: string
  integrity: string
  'data-sforum-extension': string
  'data-sforum-asset': string
  'data-sforum-digest': string
}

/**
 * 页面 L2 样式 <link> 的唯一描述来源：SSR 预注入与客户端换页都走这里。
 *
 * integrity 直接取 Host 策略里的 `PublicFrontendAssetReference.integrity`。
 * 该字段在 parsePublicFrontendAssetReference 中已被断言等于
 * `digestIntegrity(digest)`，客户端 loadStylesheet 与内容样式注入读的也是同一字段，
 * 因此预注入 link 与实际生效的 CSS 字节绑定同一份摘要，不在这里二次推导哈希。
 */
export function buildPublicL2StyleLinks(
  styles: readonly PublicFrontendAssetReference[]
): PublicL2StyleLink[] {
  return styles.map(style => ({
    rel: 'stylesheet',
    href: style.assetPath,
    integrity: style.integrity,
    'data-sforum-extension': style.extensionId,
    'data-sforum-asset': style.handle,
    'data-sforum-digest': style.digest
  }))
}
