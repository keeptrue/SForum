import { describe, expect, test } from 'bun:test'

const read = (path: string) => Bun.file(new URL(path, import.meta.url)).text()

const pages = [
  'app/pages/admin/extensions/plugins.vue',
  'app/pages/admin/extensions/themes.vue',
  'app/pages/admin/extensions/index.vue'
]

describe('extension update badges', () => {
  test('every extension page checks updates through the list surface, not a bespoke flag', async () => {
    for (const path of pages) {
      const page = await read(`../../${path}`)
      expect(page).toContain('const updateSurfaces = useAdminListSurfaces({')
      expect(page).toContain('const surfaceFailures = computed(() => updateSurfaces.failures.value.filter(item => item.id !== \'catalog\'))')
      expect(page).toContain('v-if="updateSurfaces.hasListColumns.value"')
      expect(page).toContain(':loading="updateSurfaces.pending.value"')
      expect(page).toContain('t(\'admin.extensions.updateCheck.refresh\')')
      // 检查更新只重查 surface；整页刷新统一走 refreshAll（先列表、后 surface）。
      expect(page).toContain('@click="updateSurfaces.refreshSurfaces()"')
      expect(page).toContain('async function refreshAll()')
      expect(page).toContain('await refresh()')
      expect(page).toContain('await updateSurfaces.refreshSurfaces()')
      expect(page).toContain('@click="refreshAll()"')
      // surface 失败只提示非 catalog 项，且保留重试入口（错误提示不自动关闭）。
      expect(page).toContain('t(\'admin.extensions.updateCheck.failed\')')
      expect(page).toContain('t(\'admin.extensions.updateCheck.retry\')')
      expect(page).not.toContain('updateSurfaces.failureMessage')
    }
  })

  test('the overview selection button keeps update links outside the button', async () => {
    const identity = await read('../../app/components/admin/extensions/SFAdminExtensionIdentity.vue')
    const badges = await read('../../app/components/admin/extensions/SFAdminExtensionUpdateBadges.vue')

    // 身份组件只渲染一个按钮，链接全部下沉到徽章组件：按钮内部不允许出现 <a>。
    const buttonStart = identity.indexOf('<button')
    const closeButton = identity.indexOf('</button>')
    expect(buttonStart).toBeGreaterThan(0)
    expect(closeButton).toBeGreaterThan(buttonStart)
    expect(identity.slice(buttonStart, closeButton)).not.toContain('<a')
    expect(identity).toContain(':aria-pressed="props.selected ? \'true\' : \'false\'"')
    expect(identity).toContain('@click="emit(\'select\')"')
    expect(identity).toContain('<slot name="badges" />')
    expect(identity).toContain('<slot name="footer" />')

    const updateBadges = identity.indexOf('<SFAdminExtensionUpdateBadges v-if="props.selectable"')
    expect(closeButton).toBeGreaterThan(0)
    expect(updateBadges).toBeGreaterThan(closeButton)

    // 非 selectable 时徽章紧跟名字，在状态徽章之前。
    const nonSelectable = identity.indexOf('<SFAdminExtensionUpdateBadges :badges="props.badges" :extension-name="props.display.name" />')
    expect(nonSelectable).toBeGreaterThan(0)
    expect(nonSelectable).toBeLessThan(identity.indexOf('{{ props.statusLabel }}', nonSelectable))

    expect(badges).toContain('<a')
    expect(badges).not.toContain('<button')
    expect(badges).toContain('adminRoutes.path(badge.pageId)')
    expect(badges).toContain("typeof badge.pageId === 'string' && badge.pageId")
    expect(badges).toContain('props.extensionName ? `${detail} · ${props.extensionName}` : detail')
  })

  test('pages place page-specific badges inside the identity cluster and notices in the footer', async () => {
    const plugins = await read('../../app/pages/admin/extensions/plugins.vue')
    const themes = await read('../../app/pages/admin/extensions/themes.vue')
    const overview = await read('../../app/pages/admin/extensions/index.vue')

    for (const page of [plugins, themes, overview]) {
      expect(page).toContain('<SFAdminExtensionIdentity')
      expect(page).toContain(':badges="updateSurfaces.badgesFor(item.id)"')
      expect(page).toContain('<template #badges>')
    }

    expect(overview).toContain('selectable')
    expect(overview).toContain(':selected="selected?.id === item.id"')
    expect(overview).toContain('@select="selectedId = item.id"')

    // 插件/主题页把宿主动作披露放 footer slot，页面断言字符串保持不变。
    expect(plugins).toContain('<template #footer>')
    expect(plugins).toContain('<SFAdminExtensionHostActionsNotice :extension="item" />')
    expect(themes).toContain('<template #footer>')
    expect(themes).toContain('<SFAdminExtensionHostActionsNotice :extension="item" />')

    // 总览页把描述/版本行/作者移出按钮后，不再保留旧的 <button> 包裹块。
    expect(overview).not.toContain('class="block w-full min-w-0 rounded-md text-left outline-none')
  })

  test('ships the update-check copy in both locales', async () => {
    for (const locale of ['zh-CN', 'en-US']) {
      const catalog = JSON.parse(await read(`../../i18n/locales/${locale}.json`))
      expect(catalog.admin.extensions.updateCheck).toEqual({
        refresh: expect.any(String),
        failed: expect.any(String),
        retry: expect.any(String)
      })
    }
  })
})
