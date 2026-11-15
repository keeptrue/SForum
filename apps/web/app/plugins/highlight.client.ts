import { watch } from 'vue'
import { enhanceCodeBlocks } from '~/utils/forum/codeHighlight'
import { enhanceRichContent, rerenderRichContent } from '~/utils/content/richContent'
import type { RichContentLabels, RichContentOptions } from '~/utils/content/richContentTypes'

export default defineNuxtPlugin((nuxtApp) => {
  const toast = nuxtApp.vueApp.runWithContext(() => useToast())
  const i18n = nuxtApp.$i18n as { t?: (key: string) => unknown } | undefined
  const translate = (key: string, fallback: string) => {
    const value = i18n?.t?.(key)
    return typeof value === 'string' && value !== key ? value : fallback
  }

  const codeOptions = {
    labels: {
      code: translate('codeBlock.code', '代码块'),
      copy: translate('codeBlock.copy', '复制'),
      copied: translate('codeBlock.copied', '已复制'),
      plainText: translate('codeBlock.plainText', '纯文本')
    },
    onCopySuccess: () => toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: translate('codeBlock.copySuccess', '代码已复制'),
      duration: 5000
    }),
    onCopyError: () => toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-triangle-alert',
      title: translate('codeBlock.copyFailed', '复制失败，请手动选择代码')
    })
  }

  // 文案键与 codeBlock.* 同形取法：缺键时用中文兜底，渲染器自己不拼文案。
  const richLabels: RichContentLabels = {
    diagram: translate('richContent.diagram', '图表'),
    chart: translate('richContent.chart', '统计图'),
    math: translate('richContent.math', '公式'),
    source: translate('richContent.source', '查看源码'),
    invalid: translate('richContent.invalid', '图表语法有误，已显示源码'),
    tooLarge: translate('richContent.tooLarge', '图表超出大小限制，已显示源码'),
    unsupported: translate('richContent.unsupported', '暂不支持该图表类型，已显示源码'),
    cspBlocked: translate('richContent.cspBlocked', '当前页面安全策略下以源码显示'),
    error: translate('richContent.error', '图表渲染失败，已显示源码')
  }

  // 颜色模式状态由 @nuxtjs/color-mode 的客户端插件建立；万一插件顺序变化导致状态还没建好，
  // 这里降级为浅色而不是在 setup 阶段抛错。
  const colorMode = useColorMode() as { value?: string } | undefined
  const richOptions: RichContentOptions = {
    labels: richLabels,
    colorMode: () => (colorMode?.value === 'dark' ? 'dark' : 'light'),
    // 页面挂了带摘要的扩展组件时文档策略生效，mermaid 的内联样式会被拦，降级为源码。
    cspActive: () => !!document.querySelector('[data-document-policy-digest]')
  }

  // root → 当前一轮富内容 pass 的 cleanup。WeakSet 无法遍历，颜色模式切换要遍历已挂载
  // root，所以用 Map 持有 cleanup，并在 unmounted 时删除，不长期持有已卸载节点。
  const passes = new Map<Element, () => void>()

  const enhance = (el: Element) => {
    enhanceCodeBlocks(el, codeOptions)
    // 富内容 pass 幂等：已接管的围栏与公式会跳过，只处理新内容；先收掉上一轮，
    // 避免 observer 与在飞渲染在 updated 里越积越多。
    passes.get(el)?.()
    passes.set(el, enhanceRichContent(el, richOptions))
  }

  nuxtApp.vueApp.directive('highlight', {
    mounted(el: Element) {
      enhance(el)
    },
    updated(el: Element) {
      enhance(el)
    },
    unmounted(el: Element) {
      passes.get(el)?.()
      passes.delete(el)
    },
    getSSRProps() {
      // 服务端渲染时不输出额外属性，高亮完全在客户端进行。
      return {}
    },
  })

  // 颜色模式变化：mermaid 的 SVG 与 chart 的 canvas 都烤进了主题色，必须重渲染。
  watch(() => colorMode?.value, () => {
    for (const [el, cleanup] of passes) {
      cleanup()
      if (!el.isConnected) {
        passes.delete(el)
        continue
      }
      passes.set(el, rerenderRichContent(el, richOptions))
    }
  })
})
