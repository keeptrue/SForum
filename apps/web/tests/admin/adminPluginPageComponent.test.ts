import { describe, expect, test } from 'bun:test'
import { compileTemplate, parse } from '@vue/compiler-sfc'
import { installTestDom } from '../helpers/dom'

installTestDom()

const routeFilename = 'app/pages/admin/extensions/[extensionId]/pages/[...pagePath].vue'
const route = await Bun.file(new URL('../../app/pages/admin/extensions/[extensionId]/pages/[...pagePath].vue', import.meta.url)).text()
const component = await Bun.file(new URL('../../app/components/extensions/admin/SFTrustedAdminPageComponent.vue', import.meta.url)).text()
const settingsComponent = await Bun.file(new URL('../../app/components/extensions/settings/SFTrustedSettingsComponent.vue', import.meta.url)).text()
const sdk = await Bun.file(new URL('../../packages/admin-sdk/src/index.ts', import.meta.url)).text()
const manifest = JSON.parse(await Bun.file(
  new URL('../../../../extensions/fixtures/plugins/sforum-prebuilt-settings/sforum.extension.json', import.meta.url)
).text())
const dashboard = await import(
  new URL('../../../../extensions/fixtures/plugins/sforum-prebuilt-settings/frontend/admin/dist/dashboard.mjs', import.meta.url).href
)

describe('trusted admin plugin pages', () => {
  test('composes the component view as a distinct branch of the existing admin route', () => {
    const { descriptor } = parse(route, { filename: routeFilename })
    const compiled = compileTemplate({
      source: descriptor.template?.content || '',
      filename: routeFilename,
      id: 'trusted-admin-plugin-page'
    })
    expect(compiled.errors).toEqual([])

    const componentBranch = descriptor.template?.ast.children.find(child => child.type === 1
      && child.tag === 'div'
      && child.props.some(prop => prop.type === 7
        && prop.name === 'else-if'
        && prop.exp?.type === 4
        && prop.exp.content === 'isComponentView && extension && adminPage'))
    expect(componentBranch?.type).toBe(1)
    if (!componentBranch || componentBranch.type !== 1) return

    const outlet = componentBranch.children.find(child => child.type === 1 && child.tag === 'SFTrustedAdminPageComponent')
    expect(outlet?.type).toBe(1)
    expect(outlet?.type === 1 && outlet.props.some(prop => prop.type === 7 && prop.name === 'else')).toBeTrue()
  })

  test('fixture mounts, invokes the page bridge, and cleans up its body', async () => {
    const declaration = manifest.admin.pages.find((page: { path: string }) => page.path === '/dashboard')
    expect(declaration.view).toBe('component')
    expect(declaration.component.id).toBe('dashboard')

    const target = document.createElement('div')
    const toasts: unknown[] = []
    const cleanup = await dashboard.mount(target, {
      extensionId: manifest.id,
      page: declaration,
      toast: (input: unknown) => toasts.push(input)
    })
    expect(target.textContent).toContain('Host shell inherited')
    expect(target.textContent).toContain('SForum Host')
    target.querySelector('button')?.click()
    expect(toasts).toEqual([{
      title: 'Plugin action completed',
      description: 'Ready from the Vue reference plugin',
      kind: 'success'
    }])
    await cleanup()
    expect(target.childElementCount).toBe(0)
  })

  test('publishes a page bridge without settings authority and isolates failures', () => {
    expect(sdk).toContain('export type AdminPageBridgeV1')
    expect(sdk).toContain('page: AdminPageDescriptor')
    const pageBridge = sdk.slice(sdk.indexOf('export type AdminPageBridgeV1'), sdk.indexOf('export type AdminMicroFrontendCleanup'))
    expect(pageBridge).not.toContain('settings:')
    // 宿主版本随 page-bootstrap 一起下发，经 bridge.host 透出，不再多发请求。
    expect(pageBridge).toContain('host?: AdminHostIdentity')
    expect(component).toContain('host: props.host')
    expect(route).toContain(':host="pageHostIdentity"')
    expect(component).toContain('recordContributionFailure')
    expect(component).toContain('<SFAdminFrontendTrustPanel')
    expect(component).toContain('await currentCleanup()')
    expect(component).toContain('loadIsCurrent(generation)')
    expect(component).toContain('loadGeneration += 1')
  })

  test('page bridge reads raw plugin JSON, settings keeps the Host envelope', () => {
    const requestWrapper = component.slice(
      component.indexOf('request: <T>('),
      component.indexOf('toast: (input)')
    )
    // 插件路由返回裸 JSON：页面桥必须显式要 body，不能靠宿主信封解包。
    expect(requestWrapper).toContain("responseMode: 'body'")
    // 模式是宿主契约：先展开插件选项，再覆盖 responseMode，插件改不了。
    expect(requestWrapper.indexOf('...options')).toBeGreaterThan(-1)
    expect(requestWrapper.indexOf('responseMode')).toBeGreaterThan(requestWrapper.indexOf('...options'))
    // 设置页走的是宿主信封（settings save/reset），必须保持默认 'data'。
    expect(settingsComponent).toContain('const { request } = useApiClient()')
    expect(settingsComponent).not.toContain("responseMode: 'body'")
  })
})
