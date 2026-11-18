import { describe, expect, it } from 'vitest'
import { _api, addAPIProvider } from '@iconify/vue'

/**
 * 生产回归的守卫：客户端默认图标 provider 必须指向同源 `/api/_nuxt_icon`。
 *
 * 背景：`@nuxt/icon` 2.6.2 用 `DEFAULT_RESOURCES = ['https://api.iconify.design']`
 * 判断「provider 还是默认值」，而 `@iconify/vue` 5.x 的默认是三个主机，
 * 长度不等 → 它永远不会替换 resources → 动态图标请求公共 API → 被
 * `connect-src 'self'` 拦掉。本测试直接断言修复函数把默认 provider 换掉了。
 */
describe('client icon provider', () => {
  it('points the default iconify provider at the same-origin endpoint', async () => {
    const original = _api.getAPIConfig('')?.resources || []
    const globals = globalThis as unknown as Record<string, unknown>
    const previousDefineNuxtPlugin = globals.defineNuxtPlugin
    // 插件文件顶层要调用 defineNuxtPlugin；bun test 里没有 Nuxt auto-import。
    globals.defineNuxtPlugin = (plugin: unknown) => plugin
    try {
      const { applyLocalIconProvider, default: plugin } = await import('../../app/plugins/icon-local-provider.client')
      expect(typeof plugin).toBe('function')

      expect(applyLocalIconProvider()).toBe(true)
      expect(_api.getAPIConfig('')?.resources).toEqual(['/api/_nuxt_icon'])
      // 幂等：已经是同源端点就不再改写。
      expect(applyLocalIconProvider()).toBe(false)
      expect(_api.getAPIConfig('')?.resources).toEqual(['/api/_nuxt_icon'])
    } finally {
      addAPIProvider('', { resources: original })
      if (previousDefineNuxtPlugin === undefined) {
        delete globals.defineNuxtPlugin
      } else {
        globals.defineNuxtPlugin = previousDefineNuxtPlugin
      }
    }
  })

  it('never allows a third-party iconify host on the client', async () => {
    const source = await import('node:fs').then(fs => fs.readFileSync(
      new URL('../../app/plugins/icon-local-provider.client.ts', import.meta.url),
      'utf8'
    ))
    // 注释里记录证据可以出现主机名；**代码**里不许把公共主机加回白名单。
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '')
    expect(code).toContain("'/api/_nuxt_icon'")
    expect(code).not.toMatch(/api\.iconify\.design|api\.simplesvg\.com|api\.unisvg\.com/)
  })
})
