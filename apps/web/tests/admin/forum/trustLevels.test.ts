import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { adminPageDefinitions, adminSidebarNavigation, type AdminNavigationFolderEntry } from '../../../app/config/adminModules'
import { shouldLoadWhitelist } from '~/composables/admin/forum/trustLevels/useTrustLevelsAdminPage'
import {
  CONFIG_BUCKET_ERROR_CODES,
  SPECIFIC_ERROR_CODES,
  parseSaveErrors,
  saveErrorText,
  type TrustLevelSaveError
} from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import {
  cloneTrustLevelsForm,
  earnableKeys,
  earnableWhitelistForSave,
  idleTrustLevelWhitelist,
  parseBaselinePermissions,
  parseEligiblePermissions,
  parseRecommendedConfig,
  tierStructureChanged,
  parseNamePool,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  resolveTrustLevelWhitelist,
  sanitizeEarnableForm,
  sanitizeEarnableKeys,
  toggleEarnableKey,
  TRUST_LEVEL_ADMIN_READ_PATH,
  TRUST_LEVEL_SLUGS,
  normalizeTrustLevelsForm,
  TRUST_LEVEL_THRESHOLD_METRICS,
  TRUST_LEVELS_OPTION,
  TRUST_LEVELS_SCHEMA_VERSION,
  trustLevelsPayload,
  unknownEarnableKeys,
  type TrustLevelWhitelistState
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'

/**
 * 后台「信任等级」独立页（`/control-panel/forum/trust-levels`）的登记与契约。
 *
 * 两类断言必须分开读：
 * 1. **登记**（第一个 describe）：页面从 settings tab 提升为独立页后，漏登记的表现从
 *    「静默回落 basic」变成「侧栏没有入口 / 页面渲染空面板 / 旧链接 404」。断言的是
 *    注册表结构与 shell 槽位，不是某段实现字符串。
 * 2. **契约**（其余 describe）：`trust.levels` 的形状由 Go 契约
 *    （`app/Models/TrustLevels/config.go`，`DisallowUnknownFields`）决定，字段名、数组形态、
 *    档位范围、null≠0、门控键回写逐条钉住——这些断言随文件迁移**不得改写**。
 */
const page = readFileSync(
  new URL('../../../app/pages/admin/forum/trust-levels.vue', import.meta.url),
  'utf8'
)
const thresholdsTab = readFileSync(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue', import.meta.url),
  'utf8'
)
const earnableTab = readFileSync(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableTab.vue', import.meta.url),
  'utf8'
)
const overviewTab = readFileSync(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelsOverviewTab.vue', import.meta.url),
  'utf8'
)
const settingsShell = readFileSync(
  new URL('../../../app/pages/admin/settings/index.vue', import.meta.url),
  'utf8'
)
const composable = readFileSync(
  new URL('../../../app/composables/admin/forum/trustLevels/useTrustLevelsForm.ts', import.meta.url),
  'utf8'
)
const pageComposable = readFileSync(
  new URL('../../../app/composables/admin/forum/trustLevels/useTrustLevelsAdminPage.ts', import.meta.url),
  'utf8'
)
const zh = JSON.parse(readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8'))
const en = JSON.parse(readFileSync(new URL('../../../i18n/locales/en-US.json', import.meta.url), 'utf8'))

function lookupKey(node: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>(
    (current, key) => (current && typeof current === 'object' ? (current as Record<string, unknown>)[key] : undefined),
    node
  )
}

const METRIC_SUFFIXES = [
  'days_visited',
  'topic_count',
  'comment_count',
  'like_received_count',
  'like_given_count',
  'bookmark_count'
]

function userPermissionFolder() {
  return adminSidebarNavigation
    .flat()
    .find((entry): entry is AdminNavigationFolderEntry => entry.type === 'folder' && entry.labelKey === 'admin.nav.userPermission')
}

describe('standalone page registration (admin registry + shell)', () => {
  test('registers /forum/trust-levels gated by trust.manage only', () => {
    const definition = adminPageDefinitions.find(item => item.id === '/forum/trust-levels')
    expect(definition).toBeDefined()
    // D-B：读写同权。只有 admin.access 的账号读到的是缺键载荷，渲染出来就是假现值。
    expect(definition?.requiredPermissions).toEqual(['trust.manage'])
    expect(definition?.permissionMode).toBeUndefined()
    expect(definition?.labelKey).toBe('admin.nav.trustLevels')
    expect(definition?.icon).toBe('i-lucide-gauge')
    expect(definition?.componentName).toBe('AdminForumTrustLevels')
  })

  test('sits under the user & permission folder, right after badges', () => {
    const children = userPermissionFolder()?.children.map(entry => (entry.type === 'page' ? entry.pageId : '')) ?? []
    expect(children).toEqual([
      '/users',
      '/roles',
      '/permissions',
      '/badges',
      '/forum/trust-levels'
    ])
    // 恰好一处入口：同一个页面在侧栏出现两次，运营会以为是两份配置。
    const occurrences = adminSidebarNavigation
      .flat()
      .flatMap(entry => (entry.type === 'folder' ? entry.children ?? [] : [entry]))
      .filter(entry => entry.type === 'page' && entry.pageId === '/forum/trust-levels')
    expect(occurrences).toHaveLength(1)
  })

  test('the page shell registers itself by page id and owns the five-partition slot set', () => {
    expect(page).toContain("useAdminPage('/forum/trust-levels')")
    expect(page).toContain("name: 'AdminForumTrustLevels'")
    // 五件套：联合类型 / tabIds / tabs 计算 / tab 图标 / 面板派发。
    expect(page).toContain("type TrustLevelsTab = 'overview' | 'tiers' | 'thresholds' | 'limits' | 'earnable'")
    expect(page).toContain("const tabIds: TrustLevelsTab[] = ['overview', 'tiers', 'thresholds', 'limits', 'earnable']")
    expect(page).toContain('t(`admin.forum.trustLevels.tabs.${id}`)')
    expect(page).toMatch(/const tabIcons: Record<TrustLevelsTab, string> = \{[\s\S]*?overview: 'i-lucide-list'[\s\S]*?tiers: 'i-lucide-layers'[\s\S]*?thresholds: 'i-lucide-gauge'[\s\S]*?limits: 'i-lucide-timer'[\s\S]*?earnable: 'i-lucide-key-round'[\s\S]*?\}/)
    // 卡头键：每个 tab 都有 title / description 两个映射项，漏一个就是 undefined。
    expect(page).toMatch(/const panelTitleKey: Record<TrustLevelsTab, string> = \{[\s\S]*?tiers: 'admin\.forum\.trustLevels\.tiers\.title'/)
    expect(page).toMatch(/const panelDescriptionKey: Record<TrustLevelsTab, string> = \{[\s\S]*?tiers: 'admin\.forum\.trustLevels\.sections\.tiers\.description'/)
    // 任何时刻只有一个活动面板。
    expect(page).toContain("v-if=\"activeTab === 'overview'\"")
    expect(page).toContain("v-else-if=\"activeTab === 'tiers'\"")
    expect(page).toContain("v-else-if=\"activeTab === 'thresholds'\"")
    expect(page).toContain("v-else-if=\"activeTab === 'limits'\"")
    expect(page).toMatch(/<SFAdminTrustLevelEarnableTab\s+v-else/)
    // 默认 tab 仍是 overview。
    expect(page).toMatch(/normalizeTab\(value\)[\s\S]*?: 'overview'/)
  })

  test('keeps ?tab= as the single tab authority in both directions', () => {
    expect(page).toContain('normalizeTab(route.query.tab)')
    expect(page).toMatch(/watch\(\(\) => route\.query\.tab/)
    expect(page).toMatch(/router\.replace\(\{ query: \{ \.\.\.route\.query, tab \} \}\)/)
    // 未知 tab 回落 overview（缺省面板），不是空白页。
    expect(page).toContain("? raw as TrustLevelsTab : 'overview'")
  })

  test('renders one page header, one toolbar and one tab strip — only in the shell', () => {
    expect(page).toContain('<UDashboardToolbar')
    expect(page).toContain('<SFAdminFixedTabNav')
    for (const [name, source] of [
      ['thresholds', thresholdsTab],
      ['earnable', earnableTab],
      ['overview', overviewTab]
    ] as const) {
      // 分区再画一层就是 AGENTS.md 禁止的第二个页头 / 第二套 tab。
      expect(source).not.toMatch(/<UDashboardToolbar/)
      expect(source).not.toMatch(/<SFAdminFixedTabNav/)
      expect(source).not.toContain('role="tablist"')
      expect(source).not.toContain("admin.forum.trustLevels.page.title")
      // 保存/重置只有一个出口：分区里出现页级 footer 钩子就说明出现了第二个保存入口。
      expect(source).not.toContain('data-sf-trust-levels-save')
      expect(source).not.toContain('data-sf-trust-levels-footer')
      expect(name).toBeTruthy()
    }
  })

  test('keeps the page-level footer as the only save entry, with stable hooks', () => {
    expect(page).toContain('data-sf-trust-levels-footer')
    expect(page).toContain('data-sf-trust-levels-save')
    expect(page).toContain('data-sf-trust-levels-reset')
    expect(page).toContain('data-sf-trust-levels-unsaved')
    // 手搓 #left / #actions 两个槽（先例 SFAdminForumTabFrame）：钩子必须落在本页按钮上。
    expect(page).toContain('<template #left>')
    expect(page).toContain('<template #actions>')
    expect(page).toContain("t('admin.forum.trustLevels.saveScope')")
    // 提交走表单 submit + 按钮 click 两条路，Enter 提交也不会漏保存。
    expect(page).toContain('@submit.prevent="save"')
    expect(page).toContain(':disabled="!canManage || !hasChanges"')
  })

  test('derives the five page states from one source and gates each on its own', () => {
    // 五态只在编排层算一次（shell 不各算一套），spec §2.1 的顺序即优先级。
    expect(pageComposable).toContain("export type TrustLevelsPageState = 'loading' | 'failed' | 'forbidden' | 'feature-off' | 'ready'")
    expect(pageComposable).toMatch(/if \(!canManage\.value\) return 'forbidden'/)
    expect(pageComposable).toMatch(/if \(status === 401 \|\| status === 403\) return 'forbidden'/)
    // 任何 error 都落 failed：刷新失败也不静默展示旧值（P1 修复后的新契约）。
    expect(pageComposable).toMatch(/if \(error\.value\) return 'failed'/)
    expect(pageComposable).not.toContain('&& !hasLoaded')
    expect(pageComposable).toMatch(/if \(!hasLoaded\.value\) return 'loading'/)
    // 语义等价于「hasLoaded 为真且 error 非空 ⇒ failed」：failed 分支必须排在 loading 之前，
    // 否则先命中 loading 就永远到不了 failed。这里断言源码顺序，因为编排层依赖 Nuxt 自动导入
    // 无法在 bun 里挂载；行为侧由浏览器轮次（刷新失败态）覆盖。
    expect(pageComposable.indexOf("if (error.value) return 'failed'"))
      .toBeLessThan(pageComposable.indexOf("if (!hasLoaded.value) return 'loading'"))
    expect(pageComposable).toMatch(/if \(!trustLevelsEnabled\.value\) return 'feature-off'/)
    expect(pageComposable).toContain("return 'ready'")
    // `hasLoaded` 必须从 `useAsyncData` 的 data 派生：硬加载复用水合 payload 时 handler 不重跑，
    // 副作用式布尔会恒为 false 并把页面永久卡在 loading（P1）。
    expect(pageComposable).toMatch(/const hasLoaded = computed\(\(\) => items\.value !== null && items\.value !== undefined\)/)
    expect(pageComposable).not.toMatch(/const hasLoaded = ref\(/)
    expect(pageComposable).not.toContain('hasLoaded.value = true')
    // loading / failed / forbidden 不渲染 footer（spec §6 胜 §2）：footer 只在 ready / feature-off。
    expect(page).toMatch(/const showFooter = computed\(\(\) => pageState\.value === 'ready' \|\| pageState\.value === 'feature-off'\)/)
    // forbidden 连 tablist 都不渲染。
    expect(page).toMatch(/const showTabs = computed\(\(\) => pageState\.value !== 'forbidden'\)/)
    expect(page).toContain('data-sf-page-state="pageState"')
  })

  test('thresholds tab owns one row per tier, keyed by tier id', () => {
    expect(thresholdsTab).toContain('v-for="tier in form.tiers"')
    expect(thresholdsTab).toContain('v-for="metric in TRUST_LEVEL_THRESHOLD_METRICS"')
    expect(thresholdsTab).toContain(':data-tier-id="tier.id"')
    expect(thresholdsTab).toContain('`trust.tier.${tier.id}.${metric.suffix}`')
    // TL0 不在 tiers 数组里：只给说明，不给控件。
    expect(thresholdsTab).toContain("t('admin.forum.trustLevels.tl0Hint')")
    // 停用档位保留配置、可继续编辑，但带标记。
    expect(thresholdsTab).toContain(":data-retired=\"tier.retired ? 'true' : 'false'\"")
    // D12 常驻披露点仍在阈值分区里，且不是 Toast。
    expect(thresholdsTab).toContain('restoreHint')
  })

  test('keeps the D12 disclosure permanently next to restore, inside the thresholds tab', () => {
    expect(thresholdsTab).toContain("t('admin.forum.trustLevels.restoreHint')")
    expect(thresholdsTab).toContain("t('admin.forum.trustLevels.restoreDefaults')")
    expect(thresholdsTab).toContain("emit('restore')")
    // D12 是「无确认步骤」的补偿：写成 Toast 会在 5 秒后消失。
    expect(thresholdsTab).not.toMatch(/toast[\s\S]*restoreHint/)
    // 恢复默认是**服务端动作**：先确认再 POST 专用端点，不再本地套 recommendedConfig。
    expect(pageComposable).toContain("export const TRUST_LEVEL_ADMIN_RESTORE_PATH = '/admin/trust-levels/restore-defaults'")
    expect(pageComposable).toMatch(/function restoreThresholds\(\) \{\s*if \(!canManage\.value\) return\s*restoreConfirmOpen\.value = true/)
    expect(pageComposable).toMatch(/request<unknown>\(TRUST_LEVEL_ADMIN_RESTORE_PATH, \{ method: 'POST' \}\)/)
    // 推荐值仍来自读面（D-N9），只是写回权交给服务端。
    expect(pageComposable).toContain('whitelistState.value.recommended ?? recommendedTrustLevelsForm()')
  })

  test('saves through the dedicated trust-levels endpoint, not the generic web-options one', () => {
    // 通用 `PUT /admin/web-options` 的 422 只有 `options.invalid_value`，拿不到字段路径；
    // 只有专用端点回 `details.errors[]`，字段级明细才可能落到运营眼前。
    expect(pageComposable).toContain("export const TRUST_LEVEL_ADMIN_WRITE_PATH = '/admin/trust-levels'")
    expect(pageComposable).toMatch(/request<unknown>\(TRUST_LEVEL_ADMIN_WRITE_PATH, \{\s*method: 'PUT',\s*body: \{ config: trustLevelsConfig\(/)
    // 允许注释里提到它（解释为什么换通道），但不允许有以它为目的地的请求路径。
    expect(pageComposable).not.toContain("'/admin/web-options'")
    // 单键全量覆盖：一次 PUT 交整份 v2 配置。
    expect(pageComposable).toContain('trustLevelsConfig(form.value, earnableWhitelistForSave(whitelistState.value))')
    // 成功后以响应载荷作为新基线（否则「保存成功但 hasChanges 仍为真」）。
    expect(pageComposable).toContain('initial.value = next')
    expect(pageComposable).toContain('resolveTrustLevelWhitelist({ ok: true, body })')
  })

  test('earnable tab wraps the matrix and never renders restore or save', () => {
    expect(earnableTab).toContain('<SFAdminTrustLevelEarnableMatrix')
    expect(earnableTab).toContain(':form="form"')
    expect(earnableTab).toContain(':whitelist="whitelist"')
    expect(earnableTab).toContain(':can-manage="canManage"')
    expect(earnableTab).toContain('@toggle="(tierId: string, key: string) => emit(\'toggle\', tierId, key)"')
    expect(earnableTab).toContain('@retry="emit(\'retry\')"')
    // 推荐默认的 earnable 是 ∅：把「恢复默认」放进本分区等于一键清空运营策略。
    expect(earnableTab).not.toContain('restoreDefaults')
    expect(earnableTab).not.toContain('restore')
    // 开关关闭时给占位而不是让矩阵停在 idle（那既不是失败也不是就绪）。
    expect(earnableTab).toContain('data-sf-trust-levels-earnable-feature-off')
    expect(earnableTab).toMatch(/<SFAdminTrustLevelEarnableMatrix\s+v-else/)
  })

  test('never invents an endpoint path', () => {
    for (const source of [page, thresholdsTab, earnableTab, overviewTab, pageComposable, composable]) {
      expect(source).not.toMatch(/\/api\/v1\//)
    }
  })
})

/**
 * §15「视觉对齐论坛设置」的静态契约。
 *
 * 卡头是 `UCard#header`（route shell 渲染）而不是第二个页头：页头三件套
 * （h2 + UDashboardToolbar + tablist）仍各一处。这里钉的是**结构与键映射**，
 * 视觉观感由浏览器轮次（spec §14.8）负责。
 */
describe('visual language aligned with forum settings (§15)', () => {
  const matrix = readFileSync(
    new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue', import.meta.url),
    'utf8'
  )

  test('renders one card header per partition with title, hint and the option-key badge', () => {
    expect(page).toContain('data-sf-trust-levels-card-header')
    expect(page).toContain('<h3 id="sf-trust-levels-panel-title" class="text-base font-bold">')
    expect(page).toContain('<p class="mt-1 text-xs text-muted">{{ panelDescription }}</p>')
    // 键前缀徽章是这套语言的固定成分：字面量进模板，不进 i18n（同 SFAdminForumTabFrame）。
    expect(page).toMatch(/<UBadge color="neutral" variant="soft" class="font-mono">trust\.levels<\/UBadge>/)
    // 卡头必须住在 UCard 的 #header 槽里，不是面板里。
    expect(page.indexOf('data-sf-trust-levels-card-header')).toBeGreaterThan(page.indexOf('<template #header>'))
  })

  test('maps each tab to its frozen card-header keys (thresholds uses title, not the tab label)', () => {
    expect(page).toMatch(/overview: 'admin\.forum\.trustLevels\.overview\.title'/)
    expect(page).toMatch(/thresholds: 'admin\.forum\.trustLevels\.title'/)
    expect(page).toMatch(/earnable: 'admin\.forum\.trustLevels\.earnable\.title'/)
    expect(page).toMatch(/overview: 'admin\.forum\.trustLevels\.sections\.overview\.description'/)
    expect(page).toMatch(/thresholds: 'admin\.forum\.trustLevels\.sections\.thresholds\.description'/)
    expect(page).toMatch(/earnable: 'admin\.forum\.trustLevels\.sections\.earnable\.description'/)
    // thresholds 的卡头标题不许退化成 tab 标签键。
    expect(page).not.toMatch(/thresholds: 'admin\.forum\.trustLevels\.tabs\.thresholds'/)
  })

  test('shows the recommended-configuration alert only where a card exists, above the tabs', () => {
    expect(page).toMatch(/<UAlert\s+v-if="pageState === 'ready' \|\| pageState === 'feature-off'"/)
    expect(page).toContain('data-sf-trust-levels-recommended')
    expect(page).toContain("t('admin.forum.trustLevels.recommended.title')")
    expect(page).toContain("t('admin.forum.trustLevels.recommended.description')")
    // 竖列顺序：关闭警告 → 推荐配置 → tablist。
    const disabledIndex = page.indexOf('data-sf-trust-levels-disabled')
    const recommendedIndex = page.indexOf('data-sf-trust-levels-recommended')
    const tabsIndex = page.indexOf('data-sf-trust-levels-tabs')
    expect(disabledIndex).toBeGreaterThan(-1)
    expect(recommendedIndex).toBeGreaterThan(disabledIndex)
    expect(tabsIndex).toBeGreaterThan(recommendedIndex)
    // 卡片体（form）负责的态为 showCard；推荐条不得越过它单独出现在 forbidden/failed/loading。
    expect(page).toMatch(/const showFooter = computed\(\(\) => pageState\.value === 'ready' \|\| pageState\.value === 'feature-off'\)/)
  })

  test('keeps exactly one h2, one toolbar and one tablist on the page', () => {
    expect(page.match(/<h2\b/g) ?? []).toHaveLength(1)
    expect(page.match(/<UDashboardToolbar/g) ?? []).toHaveLength(1)
    expect(page.match(/<SFAdminFixedTabNav/g) ?? []).toHaveLength(1)
    // 卡头是 h3，不是第二个 h2。模态确认自带一个对话框标题，所以先把它摘掉再数。
    const withoutModal = page.replace(/<UModal[\s\S]*?<\/UModal>/g, '')
    expect(withoutModal.match(/<h3\b/g) ?? []).toHaveLength(1)
  })

  test('uses the grid card body and drops the old space-y-5 rhythm', () => {
    expect(page).toContain('<div class="grid max-w-5xl gap-6" data-sf-trust-levels-panel :data-tab="activeTab">')
    for (const [name, source] of [['page', page], ['overview', overviewTab], ['thresholds', thresholdsTab], ['earnable', earnableTab]] as const) {
      // 两套间距叠用会得到 20 + 24 的怪值（§15.4）。
      expect(source).not.toContain('space-y-5')
      expect(name).toBeTruthy()
    }
  })

  test('removes the duplicate matrix title and points aria-labelledby at the card header', () => {
    // 卡头已用 earnable.title；卡体再画同名 h3 会让读屏出现两个同名条目。
    expect(matrix).not.toContain('sf-trust-earnable__title')
    expect(matrix).not.toMatch(/<h3\b/)
    expect(matrix).toContain('aria-labelledby="sf-trust-levels-panel-title"')
  })
})

describe('settings shell after the promotion', () => {
  test('drops trustLevels from all four registration slots', () => {
    const union = settingsShell.match(/type SettingsTab = ([^\n]+)/)?.[1] ?? ''
    const validTabs = settingsShell.match(/const validTabs: SettingsTab\[\] = (\[[^\]]*\])/)?.[1] ?? ''
    expect(union).not.toContain('trustLevels')
    expect(validTabs).not.toContain('trustLevels')
    expect(settingsShell).not.toContain("id: 'trustLevels'")
    expect(settingsShell).not.toMatch(/^\s*trustLevels:/m)
    expect(settingsShell).not.toContain('SFAdminTrustLevelsTab')
  })

  test('redirects the legacy ?tab=trustLevels deep link before initializing', () => {
    expect(settingsShell).toContain("route.query.tab === 'trustLevels'")
    expect(settingsShell).toContain("'/forum/trust-levels'")
    expect(settingsShell).toMatch(/navigateTo\(\{[\s\S]*?replace: true/)
    // 重定向必须早于 activeTab 初始化，否则先渲染一次 basic 再跳（闪一帧错页面）。
    expect(settingsShell.indexOf("route.query.tab === 'trustLevels'"))
      .toBeLessThan(settingsShell.indexOf('ref<SettingsTab>(normalizeTab('))
  })
})

describe('trust.levels payload contract (schemaVersion 2)', () => {
  test('metrics match the Go Metrics list, without the phantom account_age_days', () => {
    expect(TRUST_LEVEL_THRESHOLD_METRICS.map(metric => metric.suffix)).toEqual(METRIC_SUFFIXES)
    expect(composable).not.toContain('account_age_days')
  })

  test('emits schemaVersion 2 + a tiers array with id/name/icon/requirements/limits/earnable/retired', () => {
    const payload = trustLevelsPayload(recommendedTrustLevelsForm())
    expect(payload).toHaveLength(1)
    expect(payload[0]!.name).toBe(TRUST_LEVELS_OPTION)
    expect(TRUST_LEVELS_SCHEMA_VERSION).toBe(2)

    const decoded = JSON.parse(payload[0]!.value) as {
      schemaVersion: number
      tiers: Array<Record<string, unknown>>
    }
    expect(decoded.schemaVersion).toBe(2)
    expect(decoded.tiers).toHaveLength(4)
    expect(decoded.tiers.map(entry => entry.id)).toEqual(['member', 'resident', 'veteran', 'leader'])
    // TL0 由 trust.new_user_* 决定，不在 tiers 数组里。
    expect(decoded.tiers.map(entry => entry.id)).not.toContain('newcomer')

    for (const entry of decoded.tiers) {
      expect(Object.keys(entry).sort()).toEqual(['earnable', 'icon', 'id', 'limits', 'name', 'requirements', 'retired'])
      expect(Object.keys(entry.requirements as Record<string, number>).sort()).toEqual([...METRIC_SUFFIXES].sort())
      // limits 为 null = 继承站点默认；不是 0（免冷却）。
      expect(entry.limits).toBeNull()
      // T23：earnable 必须每档显式出现且是数组。
      expect(Array.isArray(entry.earnable)).toBe(true)
      expect(entry.earnable).toEqual([])
      // icon 始终是字符串短名：null 会让后端白名单校验炸掉。
      expect(typeof entry.icon).toBe('string')
      expect(entry.retired).toBe(false)
    }
  })

  test('recommended (local fallback) values equal the Go DefaultPolicy anchors', () => {
    const form = recommendedTrustLevelsForm()
    expect(form.tiers.map(tier => tier.id)).toEqual(['member', 'resident', 'veteran', 'leader'])
    // 第一档保守：3 天 / 5 回复，不要求发主题与点赞。
    expect(form.tiers[0]!.requirements).toEqual({
      days_visited: 3,
      topic_count: 0,
      comment_count: 5,
      like_received_count: 0,
      like_given_count: 0,
      bookmark_count: 0
    })
    // 最高档 = 百日 / 25 主题 / 250 回复 / 收 100 给 80 / 收藏 20。
    expect(form.tiers[3]!.requirements).toEqual({
      days_visited: 100,
      topic_count: 25,
      comment_count: 250,
      like_received_count: 100,
      like_given_count: 80,
      bookmark_count: 20
    })
  })

  test('upgrades a v1 config in memory: ids and config kept, names empty so i18n fallback applies', () => {
    const stored = JSON.stringify({
      schemaVersion: 1,
      levels: [
        { levelSlug: 'newcomer', requirements: {}, limits: {}, earnable: [] },
        {
          levelSlug: 'member',
          requirements: { days_visited: 4, topic_count: 1, comment_count: 6, like_received_count: 0, like_given_count: 0, bookmark_count: 0 },
          limits: { topicCooldownSeconds: 0, commentCooldownSeconds: null, dailyTopicLimit: 5, dailyCommentLimit: null },
          earnable: ['topic.create']
        },
        { levelSlug: 'resident', requirements: {}, limits: {}, earnable: [] },
        { levelSlug: 'veteran', requirements: {}, limits: {}, earnable: [] },
        { levelSlug: 'leader', requirements: {}, limits: {}, earnable: [] }
      ]
    })
    const form = readTrustLevelsConfig({ [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value: stored } as never })

    expect(form.tiers.map(tier => tier.id)).toEqual(['member', 'resident', 'veteran', 'leader'])
    expect(form.tiers[0]!.requirements.days_visited).toBe(4)
    // 0 是「免冷却」，不能与 null（继承站点默认）混为一谈（D27）。
    expect(form.tiers[0]!.limits).toEqual({
      topicCooldownSeconds: 0,
      commentCooldownSeconds: null,
      dailyTopicLimit: 5,
      dailyCommentLimit: null
    })
    expect(form.tiers[0]!.earnable).toEqual(['topic.create'])
    // 名字留空 = 走内置五档 i18n 回退，而不是前端编一个英文名写进存储。
    expect(form.tiers[0]!.name).toEqual({ zh: '', en: '' })

    // 写出只写 v2。
    const roundTrip = JSON.parse(trustLevelsPayload(form)[0]!.value) as {
      schemaVersion: number
      tiers: Array<Record<string, unknown>>
    }
    expect(roundTrip.schemaVersion).toBe(2)
    expect(roundTrip.tiers).toHaveLength(4)
    expect(roundTrip.tiers[0]!.earnable).toEqual(['topic.create'])
    expect((roundTrip.tiers[0]!.limits as Record<string, unknown>).topicCooldownSeconds).toBe(0)
  })

  test('falls back to the local defaults on unknown schemaVersion, broken JSON or an empty tiers array', () => {
    const fallback = recommendedTrustLevelsForm()
    for (const value of ['{"schemaVersion":99,"tiers":[]}', 'not-json', '', '{"schemaVersion":2,"tiers":[]}']) {
      const form = readTrustLevelsConfig({ [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value } as never })
      expect(form.tiers.map(tier => tier.id)).toEqual(fallback.tiers.map(tier => tier.id))
    }
  })

  test('local fallback and the server recommendation decode to the same shape (D-N9 drift guard)', () => {
    // 本仓是两套语言：真正的 DefaultPolicy 等值在 Go 侧；前端能守的是「同一份 JSON 形状
    // 经本地兜底表与经读面 recommendedConfig 解码后逐字相同」——字段名/顺序漂移会在这里红。
    const fallback = recommendedTrustLevelsForm()
    const asRecommended = parseRecommendedConfig(trustLevelsPayload(fallback)[0]!.value)
    expect(asRecommended).toEqual(fallback)
    expect(asRecommended!.tiers.map(tier => tier.id))
      .toEqual(recommendedTrustLevelsForm().tiers.map(tier => tier.id))
  })

  test('reads tiers back without dropping limits, earnable, icons or the retired flag', () => {
    const stored = JSON.stringify({
      schemaVersion: 2,
      tiers: [
        { id: 'seed', name: { zh: '种子', en: 'Seed' }, icon: 'sprout', requirements: {}, limits: null, earnable: [], retired: true },
        { id: 'member', name: { zh: '正式用户', en: 'Member' }, icon: 'leaf', requirements: { days_visited: 7 }, limits: null, earnable: ['topic.create'], retired: false }
      ]
    })
    const form = readTrustLevelsConfig({ [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value: stored } as never })
    expect(form.tiers).toHaveLength(2)
    // 停用档位保留在数组里（软停用），配置一字不少。
    expect(form.tiers[0]!.retired).toBe(true)
    expect(form.tiers[1]!.requirements.days_visited).toBe(7)
    expect(form.tiers[1]!.icon).toBe('leaf')
    expect(form.tiers[1]!.earnable).toEqual(['topic.create'])
  })

  test('caps active tiers at 8 but never drops a retired tier', () => {
    const base = Array.from({ length: 10 }, (_, index) => ({
      id: `t${index}`,
      name: { zh: '', en: '' },
      icon: null,
      requirements: {},
      limits: null,
      earnable: [],
      retired: false
    }))
    expect(normalizeTrustLevelsForm({ tiers: base }).tiers).toHaveLength(8)

    const withRetired = normalizeTrustLevelsForm({
      tiers: [...base.slice(0, 8), { ...base[9]!, id: 'old', retired: true }]
    })
    expect(withRetired.tiers).toHaveLength(9)
    expect(withRetired.tiers.filter(tier => !tier.retired)).toHaveLength(8)
  })

  test('clones deeply: mutating a copy never pollutes the source', () => {
    const clone = cloneTrustLevelsForm(recommendedTrustLevelsForm())
    clone.tiers[1]!.requirements.topic_count = 99
    clone.tiers[1]!.earnable.push('topic.create')
    expect(recommendedTrustLevelsForm().tiers[1]!.requirements.topic_count).toBe(2)
    expect(recommendedTrustLevelsForm().tiers[1]!.earnable).toEqual([])
    expect(TRUST_LEVEL_SLUGS[0]).toBe('newcomer')
  })
})

describe('reasonCode coverage: 33 backend codes, 15 specific + 18 bucket (P2 guardrail)', () => {
  /** 后端枚举的字面抄本（OpenAPI enum 与 Go 常量表同序）。**加第 34 条时这里必须跟着加**。 */
  const BACKEND_CODES = [
    'tier_count_out_of_range',
    'id_invalid',
    'id_reserved_for_tl0',
    'id_duplicate',
    'icon_not_whitelisted',
    'name_missing',
    'name_too_long',
    'requirement_negative',
    'requirement_unknown_metric',
    'limits_missing',
    'limit_negative',
    'earnable_missing',
    'earnable_key_empty',
    'earnable_not_whitelisted',
    'color_not_whitelisted',
    'schema_version_unsupported',
    'schema_shape_invalid',
    'json_syntax_invalid',
    'json_type_mismatch',
    'unknown_field',
    'config_unparseable',
    'tail_content',
    'limits_unparseable',
    'earnable_unparseable',
    'earnable_invalid',
    'id_derivation_conflict',
    'level_unknown',
    'level_reserved_for_tl0',
    'level_duplicate',
    'level_legacy_missing',
    // D-C3：删除墓碑是配置级字段，rank 恒为 0。
    'deleted_tier_id_invalid',
    'deleted_tier_id_duplicate',
    'deleted_tier_id_present'
  ] as const

  const loadLocale = (locale: string) =>
    JSON.parse(readFileSync(new URL(`../../../i18n/locales/${locale}.json`, import.meta.url), 'utf8')) as Record<string, unknown>
  const codeKey = (code: string) => ['admin', 'forum', 'trustLevels', 'errors', 'code', code]
  const hasPath = (doc: Record<string, unknown>, path: string[]) =>
    path.reduce<unknown>((node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined), doc) !== undefined

  test('the two lists are disjoint and together cover exactly the backend enum', () => {
    expect(BACKEND_CODES).toHaveLength(33)
    expect(SPECIFIC_ERROR_CODES).toHaveLength(15)
    expect(CONFIG_BUCKET_ERROR_CODES).toHaveLength(18)
    const union = [...SPECIFIC_ERROR_CODES, ...CONFIG_BUCKET_ERROR_CODES]
    expect(new Set(union).size).toBe(33)
    expect(new Set(union)).toEqual(new Set(BACKEND_CODES))
    // 分线本身也要稳：专属码不能同时被登记进通用桶。
    expect(SPECIFIC_ERROR_CODES.filter(code => (CONFIG_BUCKET_ERROR_CODES as readonly string[]).includes(code)))
      .toEqual([])
  })

  test('every specific code has its own copy in both locales', () => {
    for (const locale of ['zh-CN', 'en-US']) {
      const doc = loadLocale(locale)
      for (const code of SPECIFIC_ERROR_CODES) {
        expect(hasPath(doc, codeKey(code))).toBe(true)
      }
    }
  })

  test('every bucket code has no dedicated copy but renders the shared bucket text', () => {
    const zhDoc = loadLocale('zh-CN')
    const t = (key: string, params: Record<string, string | number> = {}) => {
      const value = key.split('.').reduce<unknown>(
        (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
        zhDoc
      )
      if (typeof value !== 'string') return `[${key}]`
      return value.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
    }
    const te = (key: string) => !t(key).startsWith('[')
    const main = t('admin.forum.trustLevels.errors.config.main')

    for (const code of CONFIG_BUCKET_ERROR_CODES) {
      expect(hasPath(zhDoc, codeKey(code))).toBe(false)
      const error: TrustLevelSaveError = {
        rank: 0,
        tierId: '',
        field: 'tiers',
        reasonCode: code,
        reason: '后端中文散文',
        params: {}
      }
      const text = saveErrorText(error, t, te)
      expect(text).toContain(main)
      // reason 只作为技术细节出现，不进主句。
      expect(text).toContain('技术细节：后端中文散文')
    }
  })

  test('machine identifiers are humanised before they reach the sentence', () => {
    const doc = loadLocale('zh-CN')
    const resolve = (key: string, params: Record<string, string | number> = {}) => {
      const value = key.split('.').reduce<unknown>(
        (node, part) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined),
        doc
      )
      if (typeof value !== 'string') return `[${key}]`
      return value.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
    }
    const te = (key: string) => !resolve(key).startsWith('[')
    const base: TrustLevelSaveError = { rank: 1, tierId: '', field: '', reasonCode: '', reason: '', params: {} }

    // 指标：days_visited → 访问天数
    expect(saveErrorText({
      ...base,
      reasonCode: 'requirement_negative',
      params: { metric: 'days_visited', value: -3 }
    }, resolve, te)).toBe('判定门槛不能为负：访问天数 当前是 -3。')

    // 分档限额键：topicCooldownSeconds → 发主题冷却
    expect(saveErrorText({
      ...base,
      reasonCode: 'limit_negative',
      params: { key: 'topicCooldownSeconds', value: -1 }
    }, resolve, te)).toBe('分档限额不能为负：发主题冷却 当前是 -1。')

    // 权限键：topic.create → 发布主题（复用权限目录，不新建映射）
    expect(saveErrorText({
      ...base,
      reasonCode: 'earnable_not_whitelisted',
      params: { permission: 'topic.create' }
    }, resolve, te)).toContain('发布主题')

    // 未知指标保持原样：那是诊断证据，翻译它反而查不到问题。
    expect(saveErrorText({
      ...base,
      reasonCode: 'requirement_unknown_metric',
      params: { metric: 'karma_score' }
    }, resolve, te)).toContain('karma_score')
  })

  test('a code the frontend has not caught up with falls back to the reason, never to a raw key', () => {
    const zhDoc = loadLocale('zh-CN')
    const t = (key: string) => key
    const te = (key: string) => hasPath(zhDoc, key.split('.'))
    expect(saveErrorText({
      rank: 1,
      tierId: '',
      field: '',
      reasonCode: 'brand_new_code_from_v2',
      reason: '新增的校验失败',
      params: {}
    }, t, te)).toBe('新增的校验失败')
  })
})

describe('save error details (P2)', () => {
  test('an absent details payload is not an error: it means "unified code only"', () => {
    expect(parseSaveErrors(undefined)).toEqual([])
    expect(parseSaveErrors({})).toEqual([])
    expect(parseSaveErrors({ errors: null })).toEqual([])
    expect(parseSaveErrors({ errors: 'nope' })).toEqual([])
    // 逐条形状非法时只丢那一条，不整块失败。
    expect(parseSaveErrors({ errors: [null, 7, { rank: 1, field: 'icon' }] })).toHaveLength(1)
  })

  test('keeps the backend order and never truncates or pads (the 8-item cap is theirs)', () => {
    const errors = Array.from({ length: 9 }, (_, index) => ({
      rank: index + 1,
      tierId: `t${index}`,
      field: 'icon',
      reasonCode: 'trust.icon_not_allowed',
      reason: '图标不在白名单内',
      params: { icon: 'rocket' }
    }))
    const parsed = parseSaveErrors({ errors })
    expect(parsed).toHaveLength(9)
    expect(parsed.map(error => error.rank)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9])
    expect(parsed[0]).toEqual({
      rank: 1,
      tierId: 't0',
      field: 'icon',
      reasonCode: 'trust.icon_not_allowed',
      reason: '图标不在白名单内',
      params: { icon: 'rocket' }
    })
  })

  test('normalises an unusable rank to 0 instead of guessing a tier', () => {
    expect(parseSaveErrors({ errors: [{ rank: -2, tierId: 'tier_6' }] })[0]!.rank).toBe(0)
    expect(parseSaveErrors({ errors: [{ rank: 'x' }] })[0]!.rank).toBe(0)
    expect(parseSaveErrors({ errors: [{ rank: 2.9 }] })[0]!.rank).toBe(2)
  })
})

describe('tier structure change gate (P1-4)', () => {
  const base = () => recommendedTrustLevelsForm()

  test('inserting a tier before another one counts as a structure change', () => {
    const saved = base()
    const next = { tiers: [...saved.tiers.slice(0, 1), { ...saved.tiers[1]!, id: 'tier-1' }, ...saved.tiers.slice(1)] }
    expect(tierStructureChanged(saved, next)).toBe(true)
  })

  test('reordering counts, even when the set is identical', () => {
    const saved = base()
    const next = { tiers: [saved.tiers[1]!, saved.tiers[0]!, ...saved.tiers.slice(2)] }
    expect(tierStructureChanged(saved, next)).toBe(true)
  })

  test('retiring counts', () => {
    const saved = base()
    const next = { tiers: saved.tiers.map((tier, index) => (index === 1 ? { ...tier, retired: true } : tier)) }
    expect(tierStructureChanged(saved, next)).toBe(true)
  })

  test('threshold-only edits do not count', () => {
    const saved = base()
    const next = { tiers: saved.tiers.map(tier => ({ ...tier, requirements: { ...tier.requirements, topic_count: 99 } })) }
    expect(tierStructureChanged(saved, next)).toBe(false)
  })

  test('renames, icons and earnable edits do not count', () => {
    const saved = base()
    const next = {
      tiers: saved.tiers.map(tier => ({
        ...tier,
        name: { zh: '改名', en: 'renamed' },
        icon: 'crown',
        earnable: ['topic.create']
      }))
    }
    expect(tierStructureChanged(saved, next)).toBe(false)
  })
})

describe('trust level locale keys', () => {
  test('ships every page key under admin.forum.trustLevels in both locales', () => {
    for (const locale of [zh, en]) {
      expect(locale.admin.nav.trustLevels).toBeTruthy()
      const section = locale.admin.forum.trustLevels
      // §15.6：`description` 已退役（并入 `sections.thresholds.description`），这里不再必查。
      for (const key of ['title', 'restoreDefaults', 'restoreHint', 'zeroUnlimited', 'resetChanges', 'tl0Hint', 'saveScope', 'reset']) {
        expect(section[key]).toBeTruthy()
      }
      // §15 卡头 / 推荐配置条 / 三个分区说明的定稿键。
      for (const key of ['recommended.title', 'recommended.description']) {
        expect(lookupKey(section, key)).toBeTruthy()
      }
      for (const area of ['overview', 'thresholds', 'earnable']) {
        expect(section.sections[area].description).toBeTruthy()
      }
      // 工具栏左槽仍在用 `page.description`：退役不得误伤。
      expect(section.page.description).toBeTruthy()
      for (const slug of ['newcomer', 'member', 'resident', 'veteran', 'leader']) {
        expect(section.level[slug]).toBeTruthy()
      }
      expect(Object.keys(section.metric).sort())
        .toEqual(['bookmarkCount', 'commentCount', 'daysVisited', 'likeGiven', 'likeReceived', 'topicCount'])
      for (const tab of ['overview', 'thresholds', 'earnable']) {
        expect(section.tabs[tab]).toBeTruthy()
      }
      expect(section.tabs.label).toBeTruthy()
      for (const key of ['title', 'description', 'loading', 'retry', 'unsaved']) {
        expect(section.page[key]).toBeTruthy()
      }
      expect(section.page.forbidden.title).toBeTruthy()
      expect(section.page.forbidden.description).toBeTruthy()
      expect(section.overview.title).toBeTruthy()
      expect(section.overview.state.recommended).toBeTruthy()
      expect(section.overview.state.custom).toBeTruthy()
      expect(section.overview.ladder.earnable).toBeTruthy()
      expect(section.disabled.title).toBeTruthy()
      expect(section.disabled.description).toBeTruthy()
    }
  })

  test('retires the two duplicated descriptions on both locales (§15.6)', () => {
    for (const locale of [zh, en]) {
      const section = locale.admin.forum.trustLevels
      // 分区说明只允许一个位置（卡头）；旧的两份副本必须删净，否则改一处忘一处。
      expect(section.description).toBeUndefined()
      expect(section.overview.description).toBeUndefined()
      expect(section.sections.overview.description).toBeTruthy()
    }
  })

  test('leaves no second copy of the page copy under admin.settings', () => {
    // 同一段文案留两份 = 改一处忘一处，最终两份都对不上实现。
    for (const locale of [zh, en]) {
      expect(locale.admin.settings.tabs.trustLevels).toBeUndefined()
      expect(locale.admin.settings.trustLevels).toBeUndefined()
    }
  })

  test('ships the public card keys in both locales', () => {
    for (const locale of [zh, en]) {
      const card = locale.profile.trustLevel
      expect(card.title).toBeTruthy()
      expect(card.code).toBe('TL{level}')
      expect(card.progress.ariaLabel).toBeTruthy()
      expect(card.progress.valueText).toBeTruthy()
      expect(card.nextHint).toBeTruthy()
      for (const slug of ['newcomer', 'member', 'resident', 'veteran', 'leader']) {
        expect(card.name[slug]).toBeTruthy()
      }
      // 公开侧与后台同集合：没有 accountAge（幻影指标已删）。
      expect(Object.keys(card.metric).sort())
        .toEqual(['bookmarkCount', 'commentCount', 'daysVisited', 'likeGiven', 'likeReceived', 'topicCount'])
    }
    // D21：五档 zh 显示名整组带「用户」，与用户组词族零字面重叠。
    expect(zh.profile.trustLevel.name.member).toBe('正式用户')
    expect(en.profile.trustLevel.name.member).toBe('Member')
  })
})

/**
 * earnable 白名单（M10 第一步）。
 *
 * 下面这些键字面量是**测试夹具**，不是第二份真相：生产代码里出现同样的字面量才是 bug
 * （`registry.go` 的 `earnableWhitelist` 是唯一真相，前端唯一数据源是后台读面的
 * `eligiblePermissions`）。上面有一条测试专门钉「生产代码里零副本」。
 */
const WHITELIST = [
  'topic.create',
  'post.create',
  'attachment.upload',
  'topic.edit_own',
  'post.edit_own',
  'topic.delete_own',
  'post.delete_own'
]

describe('earnable whitelist data source', () => {
  test('reads the admin surface the OpenAPI contract declares, without inventing a prefix', () => {
    expect(TRUST_LEVEL_ADMIN_READ_PATH).toBe('/admin/trust-levels')
    expect(composable).toContain(`TRUST_LEVEL_ADMIN_READ_PATH = '${TRUST_LEVEL_ADMIN_READ_PATH}'`)
    expect(composable).toContain('eligiblePermissions')
  })

  test('keeps zero copies of the seven keys in production code', () => {
    for (const key of WHITELIST) {
      expect(composable).not.toContain(`'${key}'`)
    }
  })

  test('parses the whitelist as an ordered, de-duplicated key list', () => {
    expect(parseEligiblePermissions(WHITELIST)).toEqual(WHITELIST)
    // 顺序即 UI 展示顺序（J-02）：只去重，不排序。
    expect(parseEligiblePermissions(['post.create', 'topic.create', 'post.create']))
      .toEqual(['post.create', 'topic.create'])
    expect(parseEligiblePermissions([])).toEqual([])
  })

  test('zips the parallel-array name pool the wire actually sends', () => {
    expect(parseNamePool({ zh: ['种子', '常客'], en: ['Seed', 'Regular'] })).toEqual([
      { zh: '种子', en: 'Seed' },
      { zh: '常客', en: 'Regular' }
    ])
    // 数组形式仍然认（历史/测试便利），空条目与重复项丢弃。
    expect(parseNamePool([{ zh: '种子', en: 'Seed' }, { zh: '', en: '' }, { zh: '种子', en: 'Seed' }]))
      .toEqual([{ zh: '种子', en: 'Seed' }])
    expect(parseNamePool(undefined)).toEqual([])
  })

  test('treats a malformed payload as failure, never as an empty whitelist', () => {
    for (const raw of [undefined, null, 'topic.create', {}, ['topic.create', 7], ['topic.create', '']]) {
      expect(parseEligiblePermissions(raw)).toBeNull()
    }
  })

  test('separates request failure, malformed response, and a genuinely empty whitelist', () => {
    // 1) 空集合 = ready + []：后端明确说「没有可勾的键」，这是事实不是错误。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { eligiblePermissions: [] } }))
      .toEqual({ ...idleTrustLevelWhitelist(), status: 'ready' })
    // 2) 形状不对 = failed + invalid_response：不许冒充空集合（T22）。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { levels: [] } }))
      .toEqual({ ...idleTrustLevelWhitelist(), status: 'failed', error: 'invalid_response' })
    // 3) 请求失败带后端 reason；没有 reason 时给稳定兜底码。
    expect(resolveTrustLevelWhitelist({
      ok: false,
      error: { code: 403, message: 'forbidden', data: { reason: 'forbidden' } }
    })).toEqual({ ...idleTrustLevelWhitelist(), status: 'failed', error: 'forbidden' })
    expect(resolveTrustLevelWhitelist({ ok: false, error: new Error('network') }))
      .toEqual({ ...idleTrustLevelWhitelist(), status: 'failed', error: 'request_failed' })
    // 4) 初始态既不是 failed 也不是 ready：组件不能据此画空矩阵。
    expect(idleTrustLevelWhitelist()).toEqual({
      status: 'idle',
      keys: [],
      baselineKeys: [],
      recommended: null,
      namePool: [],
      iconChoices: [],
      colorChoices: [],
      nextTierSeq: 0,
      error: null
    })
  })
})

describe('system baseline keys (D34 / Z3-b″)', () => {
  test('reads baseline keys from the admin read field, never from a frontend copy', () => {
    const body = {
      eligiblePermissions: WHITELIST,
      baselinePermissions: ['topic.create', 'attachment.upload']
    }
    expect(resolveTrustLevelWhitelist({ ok: true, body })).toEqual({
      ...idleTrustLevelWhitelist(),
      status: 'ready',
      keys: WHITELIST,
      baselineKeys: ['topic.create', 'attachment.upload'],
      error: null
    })
    // 后端没下发该字段：baselineKeys 为空 = 「无可披露的基线」，不是「基线为空集」的断言。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { eligiblePermissions: WHITELIST } }))
      .toEqual({ ...idleTrustLevelWhitelist(), status: 'ready', keys: WHITELIST })
  })

  test('drops baseline keys that are malformed or outside the whitelist instead of failing the read', () => {
    // 基线只是附加标注：它脏了不该让运营看不到矩阵（与白名单「形状非法即失败」相反）。
    for (const raw of [undefined, null, 'topic.create', {}, 7]) {
      expect(parseBaselinePermissions(raw, WHITELIST)).toEqual([])
    }
    expect(parseBaselinePermissions(['topic.create', 'user.ban', '', 7, 'topic.create'], WHITELIST))
      .toEqual(['topic.create'])
  })

  test('keeps zero baseline-key literals in the production composable', () => {
    // 第二份副本防线：基线键只允许来自后端响应，源码里出现字面量就说明有人在写死真相。
    for (const key of ['topic.create', 'attachment.upload']) {
      expect(composable).not.toContain(`'${key}'`)
    }
  })
})

describe('earnable matrix pure helpers (tier ids)', () => {
  /** 档位身份是 id，不是下标：下面所有动作都以 id 为键（D-N1）。 */
  const tierIds = () => recommendedTrustLevelsForm().tiers.map(tier => tier.id)

  test('toggles one tier without mutating the input form', () => {
    const [first, second] = tierIds() as [string, string]
    const base = recommendedTrustLevelsForm()
    const toggled = toggleEarnableKey(base, second, 'topic.create', WHITELIST)
    expect(earnableKeys(toggled, second)).toEqual(['topic.create'])
    // 纯函数：入参不动。
    expect(earnableKeys(base, second)).toEqual([])
    expect(earnableKeys(toggleEarnableKey(toggled, second, 'topic.create', WHITELIST), second)).toEqual([])
    expect(earnableKeys(toggled, first)).toEqual([])
  })

  test('records only that tier (Permissions semantics), never copies up from Grants', () => {
    // 后端 Grants 累积（低档的键高档也拿到），UI 写的是 Permissions 的「该档新增」。
    const ids = tierIds()
    const toggled = toggleEarnableKey(recommendedTrustLevelsForm(), ids[1]!, 'topic.create', WHITELIST)
    expect(earnableKeys(toggled, ids[2]!)).toEqual([])
    expect(earnableKeys(toggled, ids[3]!)).toEqual([])
    expect(toggled.tiers).toHaveLength(4)
  })

  test('rejects retired tiers, unknown tier ids and keys outside the whitelist, fail-closed', () => {
    const ids = tierIds()
    const base = recommendedTrustLevelsForm()
    expect(toggleEarnableKey(base, 'not-a-tier', 'topic.create', WHITELIST)).toEqual(base)
    expect(toggleEarnableKey(base, ids[1]!, 'user.ban', WHITELIST)).toEqual(base)
    // 白名单还没读到时传空数组：一律拒绝，不许先勾着再等请求回来。
    expect(toggleEarnableKey(base, ids[1]!, 'topic.create', [])).toEqual(base)

    const retired = cloneTrustLevelsForm(base)
    retired.tiers[1]!.retired = true
    expect(toggleEarnableKey(retired, ids[1]!, 'topic.create', WHITELIST)).toEqual(retired)
  })

  test('orders selected keys by whitelist order, not click order', () => {
    const second = tierIds()[1]!
    const clicked = toggleEarnableKey(
      toggleEarnableKey(recommendedTrustLevelsForm(), second, 'post.create', WHITELIST),
      second,
      'topic.create',
      WHITELIST
    )
    expect(earnableKeys(clicked, second)).toEqual(['topic.create', 'post.create'])
  })

  test('sanitizes every tier and reports what a save would drop', () => {
    const ids = tierIds()
    const form = recommendedTrustLevelsForm()
    form.tiers[1]!.earnable = ['user.ban', 'topic.create']
    const clean = sanitizeEarnableForm(form, WHITELIST)
    expect(clean.tiers[1]!.earnable).toEqual(['topic.create'])
    expect(clean.tiers[0]!.earnable).toEqual([])
    expect(clean.tiers).toHaveLength(4)
    expect(clean.tiers.map(tier => tier.id)).toEqual(ids)
    expect(sanitizeEarnableKeys(['user.ban', 'topic.create'], WHITELIST)).toEqual(['topic.create'])
    expect(unknownEarnableKeys(['user.ban', 'topic.create', 'user.ban'], WHITELIST)).toEqual(['user.ban'])
  })
})

describe('earnable payload contract (tier ids)', () => {
  test('drops out-of-whitelist keys and keeps every tier earnable an array', () => {
    const form = recommendedTrustLevelsForm()
    form.tiers[0]!.earnable = ['topic.create', 'user.ban']
    const decoded = JSON.parse(trustLevelsPayload(form, WHITELIST)[0]!.value) as {
      tiers: Array<Record<string, unknown>>
    }
    expect(decoded.tiers.map(entry => entry.earnable)).toEqual([['topic.create'], [], [], []])
  })

  test('without a loaded whitelist it passes stored keys through instead of guessing', () => {
    // 白名单没读到时无法判断合法性：静默清空已存配置比发出一个可能被拒的键更糟。
    const form = recommendedTrustLevelsForm()
    form.tiers[2]!.earnable = ['topic.create']
    const decoded = JSON.parse(trustLevelsPayload(form)[0]!.value) as {
      tiers: Array<Record<string, unknown>>
    }
    expect(decoded.tiers.map(entry => entry.earnable)).toEqual([[], [], ['topic.create'], []])
  })

  test('round-trips [] as []: never null, never omitted, one entry per tier', () => {
    const stored = JSON.stringify({
      schemaVersion: 2,
      tiers: [
        { id: 'member', name: { zh: '', en: '' }, icon: 'leaf', requirements: {}, limits: null, earnable: [], retired: false },
        // 后端 decodeEarnableList：null → 空集（与 [] 等价）；只有键整个缺失才 422。
        { id: 'resident', name: { zh: '', en: '' }, icon: 'house', requirements: {}, limits: null, earnable: null, retired: false },
        { id: 'veteran', name: { zh: '', en: '' }, icon: 'mountain-snow', requirements: {}, limits: null, earnable: [], retired: false },
        { id: 'leader', name: { zh: '', en: '' }, icon: 'crown', requirements: {}, limits: null, earnable: ['topic.create'], retired: false }
      ]
    })
    const form = readTrustLevelsConfig({
      [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value: stored } as never
    })
    expect(form.tiers.map(tier => tier.earnable)).toEqual([[], [], [], ['topic.create']])

    const value = trustLevelsPayload(form)[0]!.value
    expect(value).not.toContain('"earnable":null')
    const decoded = JSON.parse(value) as { tiers: Array<Record<string, unknown>> }
    expect(decoded.tiers).toHaveLength(4)
    for (const entry of decoded.tiers) {
      // T23：键必须在，且必须是数组。省略会被 DecodeConfig 判 422。
      expect(Object.keys(entry)).toContain('earnable')
      expect(Array.isArray(entry.earnable)).toBe(true)
    }
    expect(decoded.tiers[3]!.earnable).toEqual(['topic.create'])
  })

  test('only hands the whitelist to the payload when the read actually succeeded', () => {
    // 失败/加载/初始态一律不传 = 原样带回：把「读不到」当成「白名单为空」会静默清空配置。
    const form = recommendedTrustLevelsForm()
    form.tiers[1]!.earnable = ['topic.create']
    const cases: Array<[TrustLevelWhitelistState, string[]]> = [
      [idleTrustLevelWhitelist(), ['topic.create']],
      [{ ...idleTrustLevelWhitelist(), status: 'loading' }, ['topic.create']],
      [{ ...idleTrustLevelWhitelist(), status: 'failed', error: 'forbidden' }, ['topic.create']],
      [{ ...idleTrustLevelWhitelist(), status: 'ready', keys: ['post.create'] }, []]
    ]
    for (const [state, expected] of cases) {
      const decoded = JSON.parse(trustLevelsPayload(form, earnableWhitelistForSave(state))[0]!.value) as {
        tiers: Array<Record<string, unknown>>
      }
      expect(decoded.tiers[1]!.earnable).toEqual(expected)
    }
  })

  test('writes gate-key history back untouched: no control does not mean drop the value', () => {
    // 门控键在 UI 里没有勾选框，但存量配置必须逐字节回写——否则每次保存都会静默改写历史配置。
    const form = recommendedTrustLevelsForm()
    form.tiers[1]!.earnable = ['topic.create', 'post.create']
    const state: TrustLevelWhitelistState = {
      ...idleTrustLevelWhitelist(),
      status: 'ready',
      keys: WHITELIST,
      baselineKeys: ['topic.create', 'attachment.upload']
    }
    const decoded = JSON.parse(trustLevelsPayload(form, earnableWhitelistForSave(state))[0]!.value) as {
      tiers: Array<Record<string, unknown>>
    }
    expect(decoded.tiers[1]!.earnable).toEqual(['topic.create', 'post.create'])
    // 再走一次「勾选一个可操作键」的路径：门控键的存量值仍然在。
    const toggled = toggleEarnableKey(form, form.tiers[1]!.id, 'post.edit_own', WHITELIST)
    const again = JSON.parse(trustLevelsPayload(toggled, earnableWhitelistForSave(state))[0]!.value) as {
      tiers: Array<Record<string, unknown>>
    }
    expect(again.tiers[1]!.earnable).toEqual(['topic.create', 'post.create', 'post.edit_own'])
  })
})

describe('feature switch gates the whitelist request (D31)', () => {
  test('the pure predicate is the only judge: off ⇒ no request, on ⇒ request', () => {
    expect(shouldLoadWhitelist(false)).toBe(false)
    expect(shouldLoadWhitelist(true)).toBe(true)
  })

  test('every trigger point routes through the predicate, never around it', () => {
    // 三个触发点（挂载 / 保存后 / 开关打开）都必须先问判据：漏一个就会在关闭态多发一次 404。
    const calls = pageComposable.match(/shouldLoadWhitelist\(/g) ?? []
    // 1 处定义 + 2 个调用点（挂载与保存后共用 maybeLoadWhitelist，开关打开一处）。
    expect(calls).toHaveLength(3)
    // 判据一：功能开关（D31）。
    expect(pageComposable).toMatch(/if \(!shouldLoadWhitelist\(trustLevelsEnabled\.value\)\) return/)
    // 判据二：P3-b 的同轮去重 —— 只短路 loading，`ready`（保存后刷新）与 `failed`（重试）必须可达。
    expect(pageComposable).toMatch(/if \(whitelistState\.value\.status === 'loading'\) return/)
    expect(pageComposable).not.toMatch(/whitelistState\.value\.status !== 'idle'/)
    expect(pageComposable).toMatch(/onMounted\(maybeLoadWhitelist\)/)
    expect(pageComposable).toMatch(/watch\(items, maybeLoadWhitelist\)/)
    expect(pageComposable).toMatch(/watch\(trustLevelsEnabled, enabled => \{\s*if \(!shouldLoadWhitelist\(enabled\)\) return\s*void loadWhitelist\(\)/)
    // 三条发请求的路径：保存后刷新（经 maybeLoadWhitelist）、开关被打开（绕过去重直接读）、
    // 以及 id 冲突类 422 之后重取 nextTierSeq（否则运营会卡在重复 id 上）。
    expect(pageComposable.match(/void loadWhitelist\(\)/g) ?? []).toHaveLength(3)
    expect(pageComposable).toMatch(/error\.reasonCode === 'id_duplicate' \|\| error\.reasonCode === 'id_derivation_conflict'/)
    // 去重判据必须住在 maybeLoadWhitelist 里，而不是散在调用点：否则保存后刷新会被误挡。
    const maybeLoadBody = pageComposable.slice(
      pageComposable.indexOf('function maybeLoadWhitelist()'),
      pageComposable.indexOf('onMounted(maybeLoadWhitelist)')
    )
    expect(maybeLoadBody).toContain("status === 'loading'")
    expect(maybeLoadBody).toContain('void loadWhitelist()')
  })
})
