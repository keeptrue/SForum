import { describe, expect, test } from 'bun:test'
import { inventory } from '../../../../tests/a11y/listStateInventory.mjs'

/**
 * UX2-17 剩余面棘轮：86 个页面的列表三态（加载 / 空 / 错误）现状。
 *
 * 审计原文：「85 页里其余 bespoke 状态区（列表空态、加载中、行内校验）未逐页扫」。
 * 这里把口径落成可复跑的实现（`tests/a11y/listStateInventory.mjs`），并用**白名单**
 * 钉住剩余缺口：新增页面若自建状态区而不登记，这条测试立刻红；修好一页却忘了
 * 删白名单，也会红（避免清单变成永久豁免）。
 *
 * 本轮统一的 5 处：entity-meta（筛选页签 + 列表三态）、forum/content（列表空态）、
 * permissions、webhooks、jobs。
 */

/**
 * 仍为 bespoke / 不适用共享三态的位置。每条都必须写清「为什么不能统一」或
 * 「什么时候复位」，不允许空理由。
 */
const ALLOWED_GAPS: Record<string, string> = {
  'admin/attachments/manager.vue': 'loading-keeps-data：刷新保留上次数据 + 工具栏按钮 loading，刻意不做骨架（避免每次刷新列表闪烁）；空态与 error toast 已共享。',
  'admin/database.vue': 'static-dump：表/索引结构是只读转储，无异步空态；查询不到表时整页给出提示，不需要行级三态。',
  'admin/extensions/contributions.vue': 'loading-keeps-data：贡献列表随扩展目录一次取回，刷新用工具栏 loading，不插骨架。',
  'admin/extensions/events.vue': 'loading-keeps-data：同上（事件目录与贡献同源）。',
  'admin/extensions/index.vue': 'loading-keeps-data：扩展总览是卡片网格，刷新用工具栏 loading；错误走 useAdminExtensionsManager 的 error toast。',
  'admin/extensions/pages.vue': 'delegated-bespoke：页面注册表表格自带 `v-if` 空行，尚未换成 SFEmptyState，待下一轮随注册表页统一。',
  'admin/extensions/query-catalog.vue': 'delegated-bespoke：SFAdminQueryCatalogTable 是纯展示表（数据由页面预取），三态由外层页签条承担，组件内不重复。',
  'admin/extensions/route-catalog.vue': 'delegated-bespoke：SFAdminRouteCatalogGroup 按分组渲染，空分组直接不渲染（分组为空即无内容），不做组内空态。',
  'admin/jobs.vue': 'toast-error：列表错误由 useAdminJobs 的 error toast 表达（后台列表统一约定，已有 adminErrorToastContract 棘轮）；加载/空已共享。',
  'admin/moderation.vue': 'delegated-bespoke：ModerationDecisionTable 自建 loading 行与空行（非 SF 前缀组件，扫描器按约定不跟随），待随审核工作台统一。',
  'admin/schedules.vue': 'delegated-bespoke：定时任务表空态是自建文案（`admin.schedules.empty`），数据源与 jobs 同族，随 jobs 一起换共享组件更安全。',
  'admin/search.vue': 'delegated-bespoke：历史表加载/空态是 UX2-05/06 已验收的状态机（含 403/500 语义与 historyError 约束），本轮不动，避免回退已验证行为。',
  'admin/users.vue': 'scanner-false-positive：被跟随的是 SFAdminUserPreviewModal（预览模态，不是列表），用户表本身由 SFAdminUsersTable 承担三态。',
  'c/[categorySlug].vue': 'public-error-boundary：公开页错误由页面级错误边界（SFErrorPageContent）接管，列表内不再叠一层。',
  'components.vue': 'dev-only：组件演示页只在开发环境可见，不属于产品面。',
  'moderation/index.vue': 'public-error-boundary：同上（公开审核页错误由页面级边界接管）。',
  'tags/[tagSlug].vue': 'public-error-boundary：同上。'
}

function gapPages(): string[] {
  return inventory()
    .filter(row => {
      const state = row.inlineList ? row : row.delegated[0]
      if (!state) return false
      const missing = !state.loading || !state.empty || !state.error
      return missing
    })
    .map(row => row.page)
}

describe('UX2-17 列表三态：86 页清单与缺口白名单', () => {
  test('清单覆盖全部 86 个页面（反真空绿）', () => {
    const rows = inventory()
    expect(rows.length).toBe(86)
    // 每行都必须能算出「有没有列表面」，否则说明扫描口径坏了。
    expect(rows.every(row => typeof row.inlineList === 'boolean')).toBe(true)
  })

  test('有列表三态缺口的页面全部登记了理由', () => {
    const gaps = gapPages().sort()
    const undocumented = gaps.filter(page => !ALLOWED_GAPS[page])
    expect(undocumented).toEqual([])
  })

  test('白名单没有过期条目：修好的页面必须从清单里删掉', () => {
    const gaps = new Set(gapPages())
    const stale = Object.keys(ALLOWED_GAPS).filter(page => !gaps.has(page)).sort()
    expect(stale).toEqual([])
  })

  test('本轮统一过的页面不再出现在缺口清单里', () => {
    const gaps = gapPages()
    for (const page of ['admin/forum/content.vue', 'admin/permissions.vue', 'admin/webhooks.vue']) {
      expect(gaps).not.toContain(page)
    }
  })

  test('每条理由都写了分类前缀（避免「以后再修」这种空话）', () => {
    const prefixes = ['loading-keeps-data', 'static-dump', 'delegated-bespoke', 'toast-error', 'scanner-false-positive', 'public-error-boundary', 'dev-only']
    const offenders = Object.entries(ALLOWED_GAPS)
      .filter(([, reason]) => !prefixes.some(prefix => reason.startsWith(`${prefix}：`)))
      .map(([page]) => page)
    expect(offenders).toEqual([])
  })
})
