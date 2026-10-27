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
    expect(identity).toContain('<slot name="status-badges" />')
    expect(identity).toContain('<slot name="badges" />')
    expect(identity).toContain('<slot name="footer" />')

    const updateBadges = identity.indexOf('<SFAdminExtensionUpdateBadges')
    expect(updateBadges).toBeGreaterThan(closeButton)

    // 非 selectable 时徽章顺序是 状态 → 运行态/缺失 → 核心更新 → 商城链接 → 页面徽章。
    const nonSelectable = identity.indexOf('<SFAdminExtensionUpdateBadges :badges="props.badges" />')
    expect(nonSelectable).toBeGreaterThan(0)
    const statusLabel = identity.indexOf('{{ props.statusLabel }}', identity.indexOf('<template v-else>'))
    expect(statusLabel).toBeGreaterThan(0)
    expect(statusLabel).toBeLessThan(nonSelectable)
    expect(identity).toContain('<SFAdminExtensionUpdateState :extension="props.extension" />')

    // 商城徽章是 chip（不再是灰字链接）：颜色与前置图标来自 surface 声明，
    // 尾部 arrow-up-right 只表示「跳到后台另一页」；不可点时不伪装成链接。
    expect(badges).toContain('<a')
    expect(badges).not.toContain('<button')
    expect(badges).toContain('<UBadge')
    expect(badges).toContain(':color="badgeColor(badge)"')
    expect(badges).toContain('variant="soft"')
    expect(badges).toContain(':icon="badgeIcon(badge)"')
    expect(badges).toContain('trailing-icon="i-lucide-arrow-up-right"')
    expect(badges).toContain('adminRoutes.path(badge.pageId)')
    expect(badges).toContain("typeof badge.pageId === 'string' && badge.pageId")
    expect(badges).toContain('i-lucide-arrow-up-right')
    expect(badges).toContain("t('admin.extensions.update.from', { extension: badge.sourceExtensionId })")
    expect(badges).toContain('cursor-default')
    // tone 是 surface 声明的语义，Core 不得改写；缺失图标时回落到 arrow-up-circle。
    expect(badges).toContain('return badge.tone')
    expect(badges).toContain("return badge.icon || 'i-lucide-arrow-up-circle'")
    expect(badges).not.toContain('text-slate-600')
  })

  test('pages place page-specific badges inside the identity cluster and notices in the footer', async () => {
    const plugins = await read('../../app/pages/admin/extensions/plugins.vue')
    const themes = await read('../../app/pages/admin/extensions/themes.vue')
    const overview = await read('../../app/pages/admin/extensions/index.vue')

    for (const page of [plugins, themes, overview]) {
      expect(page).toContain('<SFAdminExtensionIdentity')
      expect(page).toContain(':badges="rowBadges.get(item.id) || []"')
      expect(page).toContain('<template #status-badges>')
      expect(page).toContain('<template #badges>')
      // 旧的三态之外的「待升级」徽章与升级按钮条件渲染已移除。
      expect(page).not.toContain('stagedVersionBadge')
      expect(page).not.toContain('stagedVersionHint')
      expect(page).toContain('<SFAdminExtensionPrimaryAction')
      expect(page).toContain('t(\'admin.extensions.update.moreActions\', { name: display.name })')
    }

    expect(overview).toContain('selectable')
    expect(overview).toContain(':selected="activeSelected?.id === item.id"')
    expect(overview).toContain('@select="selectRow(item)"')
    // 筛掉当前选中行时清空选中并进入详情空态，不自动改选下一行。
    expect(overview).toContain('detailDismissed.value = true')
    expect(overview).toContain("t('admin.extensions.update.noSelection')")

    // 插件/主题页把宿主动作披露放 footer slot，页面断言字符串保持不变。
    expect(plugins).toContain('<template #footer>')
    expect(plugins).toContain('<SFAdminExtensionHostActionsNotice :extension="item" />')
    expect(themes).toContain('<template #footer>')
    expect(themes).toContain('<SFAdminExtensionHostActionsNotice :extension="item" />')

    // 运行态徽章（L1）在核心更新徽章（L2）之前。
    const runtime = plugins.indexOf('runtimeStatusLabelKey(item)')
    expect(runtime).toBeGreaterThan(0)
    expect(plugins.indexOf('<template #status-badges>')).toBeLessThan(runtime)
    expect(runtime).toBeLessThan(plugins.indexOf('<template #badges>'))

    // 总览页把描述/版本行/作者移出按钮后，不再保留旧的 <button> 包裹块。
    expect(overview).not.toContain('class="block w-full min-w-0 rounded-md text-left outline-none')
  })

  test('toolbars expose the update-only filter and the builtin restart hint', async () => {
    const overview = await read('../../app/pages/admin/extensions/index.vue')
    const plugins = await read('../../app/pages/admin/extensions/plugins.vue')
    const themes = await read('../../app/pages/admin/extensions/themes.vue')

    for (const page of [overview, plugins, themes]) {
      expect(page).toContain('onlyWithUpdates')
      expect(page).toContain('t(\'admin.extensions.update.onlyWithUpdatesAria\'')
      expect(page).toContain('t(\'admin.extensions.update.onlyWithUpdatesAriaEmpty\')')
      expect(page).toContain('t(\'admin.extensions.update.allUpToDate\')')
      expect(page).toContain('t(\'admin.extensions.update.showAll\')')
    }
    expect(overview).toContain('builtinContentUpdateCount(extensions.value)')
    expect(plugins).toContain('builtinContentUpdateCount(plugins.value)')
    // 主题页没有「内置更新待重启」提示（主题不参与自动重启）。
    expect(themes).not.toContain('builtinPendingRestart')
  })

  test('keeps the manage entry and the row menu trigger on every row', async () => {
    const menu = await read('../../app/composables/admin/useExtensionRowMenu.ts')
    // 主题行即使卸载不可见也必须保留「管理」：菜单永远返回至少一项，页面不隐藏触发器。
    expect(menu).toContain('return [items]')
    expect(menu).not.toContain('return null')
    expect(menu.indexOf("t('admin.extensions.manage')"))
      .toBeLessThan(menu.indexOf("t('admin.extensions.configureStorage')"))

    for (const path of pages) {
      const page = await read(`../../${path}`)
      expect(page).not.toContain('v-if="rowMenus[item.id]"')
      expect(page).toContain(':items="rowMenus[item.id] || []"')
    }
  })

  test('every menu trigger binds its open state to aria-expanded', async () => {
    const count = (haystack: string, needle: string) => haystack.split(needle).length - 1

    for (const path of pages) {
      const page = await read(`../../${path}`)
      // reka-ui 触发器本身会下发 aria-expanded；这里显式绑定 open 作为防御，
      // 保证自定义触发器的可访问状态不依赖组件库实现细节。
      expect(page).toContain('<template #default="{ open }">')
      expect(page).toContain(':aria-expanded="open"')
      // 逐个触发器都要绑：插件页除行尾 ⋯ 外还有检查器工具栏的 ⋯。
      expect(count(page, ':aria-expanded="open"')).toBe(count(page, '<UDropdownMenu'))
    }

    const plugins = await read('../../app/pages/admin/extensions/plugins.vue')
    expect(count(plugins, '<UDropdownMenu')).toBe(2)
  })

  test('ships the update-check and update-state copy in both locales', async () => {
    for (const locale of ['zh-CN', 'en-US']) {
      const catalog = JSON.parse(await read(`../../i18n/locales/${locale}.json`))
      expect(catalog.admin.extensions.updateCheck).toEqual({
        refresh: expect.any(String),
        failed: expect.any(String),
        retry: expect.any(String)
      })
      const update = catalog.admin.extensions.update
      for (const key of [
        'available',
        'availableHint',
        'restartToApply',
        'restartHint',
        'restartEnableHint',
        'activateToApply',
        'activateHint',
        'upgradeNow',
        'upgradeAndEnable',
        'restartAndEnable',
        'upgradedTo',
        'restartedToApply',
        'onlyWithUpdates',
        'onlyWithUpdatesAria',
        'onlyWithUpdatesAriaEmpty',
        'builtinPendingRestart',
        'allUpToDate',
        'allUpToDateDescription',
        'noneInView',
        'noneInViewDescription',
        'showAll',
        'moreActions',
        'from',
        'currentVersion',
        'newVersion',
        'contentUpdated',
        'digest',
        'restartTitle',
        'restartBody',
        'restartConfirm',
        'noSelection',
        'noSelectionDescription'
      ]) {
        expect(update[key]).toBeString()
      }
      expect(catalog.admin.extensions.stagedVersion).toBeUndefined()
      expect(catalog.admin.extensions.stagedVersionBadge).toBeUndefined()
      expect(catalog.admin.extensions.stagedVersionHint).toBeUndefined()
      expect(catalog.admin.extensions.upgradeUnavailable).toBeUndefined()
    }
  })
})
