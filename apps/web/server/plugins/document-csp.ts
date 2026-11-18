import {
  DOCUMENT_CSP_NONCE_CONTEXT_KEY,
  applyDocumentCspNonceToHtmlChunk
} from '../../app/composables/errors/documentCspNonce'

/**
 * 把 per-request CSP nonce 打到 Nuxt 渲染出的内联 `<script>` 上。
 *
 * 文档 CSP 由 `applyPublicPageDocumentPolicy` 写在响应头里，并把同一个 nonce 放进
 * `event.context`；本插件只负责「让 HTML 与响应头对上」，不自己决定任何策略。
 * 该响应没有 nonce（没写 CSP 的路径）时原样返回。
 *
 * 只处理 Nuxt 自己拥有的 chunk：`head`（headTags，含内联 payload 与入口脚本）与
 * `bodyAppend`（bodyClose 位置的脚本）。`body` 装的是页面 SSR 内容，里面可能含
 * `v-html` 注入的内联脚本——那些脚本在改动前就被 `script-src 'self'` 挡着，
 * 给它们打 nonce 等于把内容层注入合法化，是纵深防御的净损失。
 *
 * 前提：当前渲染走 Nuxt 的非流式路径。若将来启用 `experimental.streaming`，
 * `render:html` 只拿到 shell，流式分块不会经过这里，需要另做适配。
 */
const NONCEABLE_CHUNKS = ['head', 'bodyAppend'] as const

export default defineNitroPlugin((nitroApp) => {
  nitroApp.hooks.hook('render:html', (html, { event }) => {
    const nonce = String(
      (event.context as Record<string, unknown>)[DOCUMENT_CSP_NONCE_CONTEXT_KEY] ?? ''
    ).trim()
    if (!nonce) {
      return
    }
    for (const key of NONCEABLE_CHUNKS) {
      const chunks = html[key]
      for (let index = 0; index < chunks.length; index++) {
        const chunk = chunks[index]
        if (typeof chunk === 'string') {
          chunks[index] = applyDocumentCspNonceToHtmlChunk(chunk, nonce)
        }
      }
    }
  })
})
