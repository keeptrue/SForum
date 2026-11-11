import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, ref } from 'vue'
import {
  activeRankOf,
  activeTierCount,
  addTier,
  retireTier
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  recommendedTrustLevelsForm,
  TRUST_LEVEL_THRESHOLD_METRICS,
  type TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import {
  activeTiers,
  trustLevelIconOf,
  trustLevelLadder,
  trustLevelName
} from '~/composables/profile/useTrustLevel'
import { TRUST_LEVEL_TIER_MAX } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { compileVueSfc, mount } from '../../helpers/vueSfc'

/**
 * 「等级总览」分区（spec §3）的渲染契约，N 档版。
 *
 * 本分区是**纯展示**：一个表单控件都不许有（否则「整页一次保存」的边界就被它偷偷打开）。
 * 它同时承担四件不能靠别的分区表达的事实：
 * 1. 阶梯 = 虚拟 TL0 + 当前 **active** 档位（行数随档数变，不是写死 5 行）；
 * 2. 功能开关的当前状态（关闭时徽章 + 一行说明，开关入口只在有 `settings.site.manage` 时出现）；
 * 3. 站点**已保存**的门槛与推荐默认是否一致（看 `savedRequirements` + `recommended`，
 *    不是正在编辑的 `form`：只比阈值也把「档位集合被改过」算成自定义）；
 * 4. TL0 的判定权不属于本页（`trust.new_user_*`），必须写清。
 */
const messages = JSON.parse(
  readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8')
) as Record<string, unknown>

function lookup(path: string): unknown {
  return path.split('.').reduce<unknown>(
    (node, key) => (node && typeof node === 'object' ? (node as Record<string, unknown>)[key] : undefined),
    messages
  )
}

function t(key: string, params: Record<string, unknown> = {}) {
  const template = lookup(key)
  if (typeof template !== 'string') throw new Error(`missing locale key: ${key}`)
  return template.replace(/\{(\w+)\}/g, (_match, name: string) => String(params[name] ?? `{${name}}`))
}

const UIconStub = { name: 'UIcon', props: { name: { type: String, default: '' } }, template: '<i :data-icon="name" />' }
const UBadgeStub = { name: 'UBadge', template: '<span class="u-badge"><slot /></span>' }
/**
 * UButton 真实实现带 `to` 时渲染成 `<a href>`（NuxtLink），否则是 `<button>`。
 * 替身必须保住这条差别，否则「入口指向哪一页」在测试里永远无法断言。
 */
const UButtonStub = {
  name: 'UButton',
  props: { to: { type: String, default: '' } },
  template: '<a v-if="to" :href="to" v-bind="$attrs"><slot /></a><button v-else type="button" v-bind="$attrs"><slot /></button>'
}

const overviewPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelsOverviewTab.vue', import.meta.url)
)

/** 每个用例都能换一份权限集合，所以每次重新编译（compileVueSfc 的 bindings 是编译期闭包）。 */
async function compileOverview(permissions: string[] = []) {
  return await compileVueSfc(overviewPath, `sf-trust-levels-overview-${permissions.join('-') || 'none'}`, {
    computed,
    useI18n: () => ({ t, locale: ref('zh-CN') }),
    useAuthSession: () => ({ can: (permission: string) => permissions.includes(permission) }),
    useAdminRoutes: () => ({ path: (child: string) => `/control-panel${child}` }),
    TRUST_LEVEL_THRESHOLD_METRICS,
    activeTiers,
    trustLevelIconOf,
    trustLevelLadder,
    trustLevelName,
    TRUST_LEVEL_TIER_MAX
  })
}

/** 已保存的阈值按档位 id 索引（组件用它比对 `recommended`）。 */
function requirementsById(form: TrustLevelsForm): Record<string, Record<string, number>> {
  return Object.fromEntries(form.tiers.filter(tier => !tier.retired).map(tier => [tier.id, { ...tier.requirements }]))
}

/** 按目标 active 档数补齐档位（复用纯函数，不手搓档位对象）。 */
function formWithActive(activeCount: number): TrustLevelsForm {
  let tiers = recommendedTrustLevelsForm().tiers
  while (activeTierCount(tiers) < activeCount) tiers = addTier(tiers)
  return { tiers }
}

function tierOf(form: TrustLevelsForm, tierId: string) {
  const tier = form.tiers.find(entry => entry.id === tierId)
  if (!tier) throw new Error(`missing tier: ${tierId}`)
  return tier
}

async function mountOverview(options: {
  form?: TrustLevelsForm
  recommended?: TrustLevelsForm
  savedRequirements?: Record<string, Record<string, number>>
  trustLevelsEnabled?: boolean
  permissions?: string[]
} = {}) {
  const form = options.form ?? recommendedTrustLevelsForm()
  const recommended = options.recommended ?? recommendedTrustLevelsForm()
  const Component = await compileOverview(options.permissions)
  return mount(Component, {
    props: {
      form,
      recommended,
      savedRequirements: options.savedRequirements ?? requirementsById(recommended),
      trustLevelsEnabled: options.trustLevelsEnabled ?? true
    },
    global: { components: { UIcon: UIconStub, UBadge: UBadgeStub, UButton: UButtonStub } }
  })
}

describe('overview: N-tier ladder', () => {
  test('renders TL0 plus one row per active tier, each with its name, TL code and rank order', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = await mountOverview({ form })
    const rows = wrapper.findAll('[data-sf-trust-levels-ladder] li')
    // 行数 = 1（虚拟 TL0）+ active 档数，不是常量 5。
    expect(rows).toHaveLength(1 + activeTierCount(form.tiers))
    expect(rows.map(row => row.attributes('data-level')))
      .toEqual(rows.map((_row, level) => String(level)))
    const expectedNames = [
      t('profile.trustLevel.name.newcomer'),
      ...activeTiers(form.tiers).map(tier => t(`profile.trustLevel.name.${tier.id}`))
    ]
    for (const [level, row] of rows.entries()) {
      expect(row.text()).toContain(expectedNames[level]!)
      expect(row.text()).toContain(`TL${level}`)
    }
  })

  test('grows with the active tier count instead of assuming five levels', async () => {
    const form = formWithActive(7)
    const wrapper = await mountOverview({ form })
    const rows = wrapper.findAll('[data-sf-trust-levels-ladder] li')
    expect(rows).toHaveLength(8)
    // 最高档按 active 档数算：倒数第一行是 rank 7，不是写死的 TL4。
    const top = rows[rows.length - 1]!
    expect(top.attributes('data-level')).toBe('7')
    expect(top.text()).toContain('TL7')

    const eight = await mountOverview({ form: formWithActive(8) })
    expect(eight.findAll('[data-sf-trust-levels-ladder] li')).toHaveLength(9)
  })

  test('skips retired tiers, so a retired top tier does not look like the highest', async () => {
    const tiers = retireTier(recommendedTrustLevelsForm().tiers, 'leader')
    const wrapper = await mountOverview({ form: { tiers } })
    const rows = wrapper.findAll('[data-sf-trust-levels-ladder] li')
    expect(rows).toHaveLength(1 + activeTierCount(tiers))
    const top = rows[rows.length - 1]!
    expect(top.text()).toContain(t('profile.trustLevel.name.veteran'))
    expect(top.text()).toContain(`TL${activeRankOf(tiers, 'veteran')}`)
    // 停用档仍然被披露（配置没丢），只是不进阶梯。
    expect(wrapper.find('[data-sf-trust-levels-tier-summary]').text())
      .toBe(t('admin.forum.trustLevels.overview.tierSummary', {
        active: activeTierCount(tiers),
        retired: tiers.length - activeTierCount(tiers),
        max: 8
      }))
  })

  test('says TL0 is decided elsewhere instead of showing it a threshold', async () => {
    const wrapper = await mountOverview()
    const tl0 = wrapper.find('[data-sf-trust-levels-ladder] li[data-level="0"]')
    expect(tl0.text()).toContain(t('admin.forum.trustLevels.tl0Hint'))
    // TL0 没有门槛摘要：`data-metrics` 是 0，且不是「本档不设门槛」那句（那句话会误导成可配置）。
    expect(tl0.attributes('data-metrics')).toBe('0')
    expect(tl0.text()).not.toContain(t('admin.forum.trustLevels.overview.ladder.noThresholds'))
  })

  test('summarises only the non-zero metrics, in Go Metrics order', async () => {
    const wrapper = await mountOverview()
    const tl1 = wrapper.find('[data-sf-trust-levels-ladder] li[data-level="1"]')
    // 推荐默认 TL1 = 3 天 / 5 回复：两项非 0，其余四项不出现。
    expect(tl1.attributes('data-metrics')).toBe('2')
    expect(tl1.text()).toContain(t('admin.forum.trustLevels.metric.daysVisited'))
    expect(tl1.text()).toContain(t('admin.forum.trustLevels.metric.commentCount'))
    expect(tl1.text()).not.toContain(t('admin.forum.trustLevels.metric.bookmarkCount'))

    const form = recommendedTrustLevelsForm()
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) tierOf(form, 'resident').requirements[metric.suffix] = 0
    const empty = await mountOverview({ form })
    expect(empty.find('[data-sf-trust-levels-ladder] li[data-level="2"]').text())
      .toContain(t('admin.forum.trustLevels.overview.ladder.noThresholds'))
  })

  test('shows the earned-unlock badge only for tiers that actually earn something', async () => {
    const form = recommendedTrustLevelsForm()
    tierOf(form, 'resident').earnable = ['topic.create', 'post.create']
    const wrapper = await mountOverview({ form })
    const badges = wrapper.findAll('[data-sf-trust-levels-earnable-count]')
    expect(badges).toHaveLength(1)
    expect(badges[0]!.attributes('data-sf-trust-levels-earnable-count')).toBe('2')
    expect(badges[0]!.text()).toBe(t('admin.forum.trustLevels.overview.ladder.earnable', { count: 2 }))
    // 0 项不渲染徽章：写「新增解锁 0 项」等于给每档各加一条噪声。
    expect(wrapper.find('[data-sf-trust-levels-ladder] li[data-level="3"] [data-sf-trust-levels-earnable-count]').exists()).toBe(false)
  })
})

describe('overview: row-list ladder geometry (§15.3)', () => {
  test('renders the ladder as a divided ul, not an ordered stepped list', async () => {
    const wrapper = await mountOverview()
    const ladder = wrapper.find('[data-sf-trust-levels-ladder]')
    // 清单语义用 ul；`<ol>` 是 §3 的旧阶梯，已弃用。
    const list = ladder.element.querySelector('ul')
    expect(list).not.toBeNull()
    expect(ladder.element.querySelector('ol')).toBeNull()
    expect(list!.className).toContain('divide-y')
    expect(list!.className).toContain('list-none')
  })

  test('separates rows with divide-y only — no threshold-row border/margin language', async () => {
    const wrapper = await mountOverview()
    const rows = wrapper.findAll('[data-sf-trust-levels-ladder] li')
    expect(rows).toHaveLength(1 + activeTierCount(recommendedTrustLevelsForm().tiers))
    for (const row of rows) {
      const classes = row.classes().join(' ')
      // 阈值行那套 border-t / mt-3.5 / pt-3.5 会让「清单行」读成「表单行」。
      expect(classes).not.toContain('border-t')
      expect(classes).not.toContain('mt-3.5')
      expect(classes).not.toContain('pt-3.5')
      expect(classes).toContain('py-3')
    }
  })

  test('lets the right-hand group wrap to its own full-width row below 640', async () => {
    const wrapper = await mountOverview()
    const tl2 = wrapper.find('[data-sf-trust-levels-ladder] li[data-level="2"]')
    const groups = tl2.element.querySelectorAll(':scope > div > div')
    expect(groups).toHaveLength(2)
    const left = groups[0] as HTMLElement
    const right = groups[1] as HTMLElement
    expect(left.className).toContain('min-w-0')
    // 右侧组：≥640 与左组两端对齐，<640 整行换行（不截断摘要）。
    expect(right.className).toContain('w-full')
    expect(right.className).toContain('sm:w-auto')
    expect(right.textContent).toBeTruthy()
  })

  test('keeps the TL0 explanation on the same row position as the other summaries', async () => {
    const wrapper = await mountOverview()
    const tl0 = wrapper.find('[data-sf-trust-levels-ladder] li[data-level="0"]')
    // TL0 不是单独一块：说明挂在行内 p.mt-1，与其它各档摘要同一位置。
    const paragraph = tl0.element.querySelector('p')
    expect(paragraph).not.toBeNull()
    expect(paragraph!.className).toContain('mt-1')
    expect(paragraph!.textContent).toContain(t('admin.forum.trustLevels.tl0Hint'))
  })
})

describe('overview: saved configuration state badge', () => {
  test('reads the recommended badge from saved requirements, not from the edited form', async () => {
    const recommended = recommendedTrustLevelsForm()
    const edited = recommendedTrustLevelsForm()
    tierOf(edited, 'member').requirements.days_visited = 99
    // 正在编辑的值不影响徽章：徽章回答「站点当前用什么」，未保存由 footer 承担。
    const wrapper = await mountOverview({ form: edited, recommended, savedRequirements: requirementsById(recommended) })
    const state = wrapper.find('[data-sf-trust-level-state]')
    expect(state.attributes('data-sf-trust-level-state')).toBe('recommended')
    expect(state.text()).toContain(t('admin.forum.trustLevels.overview.state.recommended'))
  })

  test('flips to custom when any editable tier differs, and says what to do next', async () => {
    const recommended = recommendedTrustLevelsForm()
    const saved = requirementsById(recommended)
    saved['veteran']!.like_received_count = 42
    const wrapper = await mountOverview({ recommended, savedRequirements: saved })
    const state = wrapper.find('[data-sf-trust-level-state]')
    expect(state.attributes('data-sf-trust-level-state')).toBe('custom')
    expect(state.text()).toContain(t('admin.forum.trustLevels.overview.state.custom'))
    expect(state.text()).toContain(t('admin.forum.trustLevels.overview.state.customHint'))
  })

  test('flips to custom when the tier set itself differs, even if every threshold matches', async () => {
    const recommended = recommendedTrustLevelsForm()
    const saved = requirementsById(recommended)
    // 运营加了一档但没改任何阈值：只比阈值会把它显示成「推荐默认」。
    saved['tier-9'] = { ...tierOf(recommended, 'member').requirements }
    const wrapper = await mountOverview({ recommended, savedRequirements: saved })
    expect(wrapper.find('[data-sf-trust-level-state]').attributes('data-sf-trust-level-state')).toBe('custom')
  })

  test('ignores limits and earnable: they are not threshold configuration', async () => {
    const form = recommendedTrustLevelsForm()
    tierOf(form, 'resident').earnable = ['topic.create']
    tierOf(form, 'resident').limits = {
      topicCooldownSeconds: 30,
      commentCooldownSeconds: null,
      dailyTopicLimit: 5,
      dailyCommentLimit: null
    }
    const wrapper = await mountOverview({ form })
    // 只勾了一项能力解锁就把站点标成「自定义门槛」，会让运营去找一个不存在的门槛改动。
    expect(wrapper.find('[data-sf-trust-level-state]').attributes('data-sf-trust-level-state')).toBe('recommended')
  })
})

describe('overview: feature switch and permissions', () => {
  test('feature off shows a disabled badge plus one explanation line, never an alert', async () => {
    const wrapper = await mountOverview({ trustLevelsEnabled: false })
    const feature = wrapper.find('[data-sf-trust-levels-feature-state]')
    expect(feature.attributes('data-sf-trust-levels-feature-state')).toBe('off')
    expect(feature.text()).toContain(t('admin.forum.trustLevels.overview.feature.disabled'))
    expect(feature.text()).toContain(t('admin.forum.trustLevels.overview.feature.disabledHint'))
    // 全页只允许 shell 那一条关闭警告；分区再画一条同名警告 = 同因两条。
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.findAll('.u-alert')).toHaveLength(0)
  })

  test('offers the switch entry only to accounts that can change site settings', async () => {
    const without = await mountOverview({ trustLevelsEnabled: false })
    expect(without.find('[data-sf-trust-levels-open-features]').exists()).toBe(false)

    const withPermission = await mountOverview({ trustLevelsEnabled: false, permissions: ['settings.site.manage'] })
    const entry = withPermission.find('[data-sf-trust-levels-open-features]')
    expect(entry.exists()).toBe(true)
    expect(entry.attributes('href')).toBe('/control-panel/settings/features')
    // 开关打开时不需要这个入口。
    expect((await mountOverview({ permissions: ['settings.site.manage'] })).find('[data-sf-trust-levels-open-features]').exists()).toBe(false)
  })
})

describe('overview: read-only by construction', () => {
  test('renders zero form controls, so it can never become a second save entry', async () => {
    const wrapper = await mountOverview({ form: recommendedTrustLevelsForm() })
    expect(wrapper.findAll('input')).toHaveLength(0)
    expect(wrapper.findAll('select')).toHaveLength(0)
    expect(wrapper.findAll('textarea')).toHaveLength(0)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
    expect(wrapper.findAll('form')).toHaveLength(0)
  })

  test('emits jump intents for the three other partitions', async () => {
    const wrapper = await mountOverview()
    await wrapper.find('[data-sf-trust-levels-jump-tiers]').trigger('click')
    await wrapper.find('[data-sf-trust-levels-jump-thresholds]').trigger('click')
    await wrapper.find('[data-sf-trust-levels-jump-earnable]').trigger('click')
    expect(wrapper.emitted('jump')).toEqual([['tiers'], ['thresholds'], ['earnable']])
  })

  test('discloses the three scope facts and the limits gap once each', async () => {
    const wrapper = await mountOverview()
    const scope = wrapper.find('[data-sf-trust-levels-scope]')
    expect(scope.findAll('li')).toHaveLength(3)
    expect(scope.text()).toContain(t('admin.forum.trustLevels.overview.scope.tl0'))
    expect(scope.text()).toContain(t('admin.forum.trustLevels.overview.scope.levels'))
    expect(scope.text()).toContain(t('admin.forum.trustLevels.overview.scope.stack'))
    // D-D：分档限额本次不做，必须写清而不是留一个空位让运营猜。
    expect(wrapper.find('[data-sf-trust-levels-limits-note]').text())
      .toBe(t('admin.forum.trustLevels.overview.limitsNote'))
  })
})

describe('page shell: tab wiring and states that must not leak a form', () => {
  const shell = readFileSync(
    new URL('../../../app/pages/admin/forum/trust-levels.vue', import.meta.url),
    'utf8'
  )

  /** 面板区（KeepAlive 内）：每个 tab 恰有一个分支，条件指令紧跟在组件标签后。 */
  const panelStart = shell.indexOf('data-sf-trust-levels-panel')
  const panelEnd = shell.indexOf('</KeepAlive>', panelStart)
  const panel = panelStart >= 0 && panelEnd > panelStart ? shell.slice(panelStart, panelEnd) : ''

  /** 从某组件标签到下一个组件标签之间的片段（避免跨分支误匹配）。 */
  function panelBranch(tag: string): string {
    const start = panel.indexOf(`<${tag}`)
    expect(start).toBeGreaterThanOrEqual(0)
    const next = panel.indexOf('<SFAdmin', start + 1)
    return panel.slice(start, next < 0 ? undefined : next)
  }

  test('lists all five partitions, tiers and limits included', () => {
    // tab 列表是页面态的一部分：少了 tiers，档位管理就没有入口（只能靠 URL 猜）。
    expect(shell).toContain("const tabIds: TrustLevelsTab[] = ['overview', 'tiers', 'thresholds', 'limits', 'earnable']")
    for (const tab of ['overview', 'tiers', 'thresholds', 'limits', 'earnable']) {
      // 文案键必须在 locale 里存在，否则 tab 会渲染成裸键。
      expect(t(`admin.forum.trustLevels.tabs.${tab}`)).toBeTruthy()
      // tab 标签由 `tabIds` 逐项取键（`tabs` computed 里的 `${id}`），不是各写一遍。
      expect(shell).toContain('t(`admin.forum.trustLevels.tabs.${id}`)')
    }
    expect(shell).toContain('<div v-if="showTabs" data-sf-trust-levels-tabs>')
  })

  test('maps each tab to exactly one panel component', () => {
    const branches: Array<[tab: string, tag: string, directive: string]> = [
      ['overview', 'SFAdminTrustLevelsOverviewTab', `v-if="activeTab === 'overview'"`],
      ['tiers', 'SFAdminTrustLevelTiersTab', `v-else-if="activeTab === 'tiers'"`],
      ['thresholds', 'SFAdminTrustLevelThresholdsTab', `v-else-if="activeTab === 'thresholds'"`],
      ['limits', 'SFAdminTrustLevelLimitsTab', `v-else-if="activeTab === 'limits'"`],
      ['earnable', 'SFAdminTrustLevelEarnableTab', 'v-else']
    ]
    for (const [, tag, directive] of branches) {
      expect(panelBranch(tag)).toContain(directive)
    }
    // 最后一个分支是兜底 v-else：它不能再带条件，否则会漏一个 tab。
    expect(panelBranch('SFAdminTrustLevelEarnableTab')).not.toContain('v-else-if')
    // 五个分区、五个分支：多一个或少一个都意味着某个 tab 没有面板。
    expect(panel.match(/<SFAdminTrustLevel\w+Tab/g) ?? []).toHaveLength(5)
    expect(panel).toContain(':data-tab="activeTab"')
  })

  test('feature-off renders exactly one warning and no second copy in the partitions', () => {
    expect(shell.match(/data-sf-trust-levels-disabled/g) ?? []).toHaveLength(1)
    expect(shell).toMatch(/v-if="pageState === 'feature-off'"/)
    // loading / failed / forbidden 三态不渲染 footer（spec §6 胜 §2）。
    expect(shell).toMatch(/const showFooter = computed\(\(\) => pageState\.value === 'ready' \|\| pageState\.value === 'feature-off'\)/)
  })

  test('forbidden renders no tabs and no form at all', () => {
    // 无 trust.manage 时载荷缺 trust.levels 键，回填出的推荐默认值就是假现值（D-B）。
    expect(shell).toMatch(/const showTabs = computed\(\(\) => pageState\.value !== 'forbidden'\)/)
    expect(shell).toContain('<form v-if="showCard"')
    expect(shell).toMatch(/const showCard = computed\(\(\) => pageState\.value === 'ready' \|\| pageState\.value === 'feature-off' \|\| pageState\.value === 'loading'\)/)
    expect(shell).toContain('data-sf-trust-levels-forbidden')
    // 失败态给的是错误条 + 重试，不是一张空表。
    expect(shell).toContain('data-sf-trust-levels-load-error')
    expect(shell).toContain('data-sf-trust-levels-retry')
  })

  test('gates saving behind a blocking structure confirmation (P1-4)', () => {
    const composable = readFileSync(
      new URL('../../../app/composables/admin/forum/trustLevels/useTrustLevelsAdminPage.ts', import.meta.url),
      'utf8'
    )
    // 判据是**纯函数**：active 序列变化才确认；只改阈值/名字/图标/解锁不弹。
    expect(composable).toContain('tierStructureChanged(initial.value, form.value)')
    expect(composable).toMatch(/if \(structureChanged\.value\) \{\s*structureConfirmOpen\.value = true\s*return\s*\}/)
    expect(composable).toContain('async function performSave()')
    expect(composable).toContain('async function confirmStructureSave()')

    // 阻塞式确认在 shell：两条披露 + 明确的接受/取消，且不是 Toast。
    expect(shell).toContain('data-sf-trust-levels-structure-confirm')
    expect(shell).toContain('data-sf-trust-levels-structure-accept')
    expect(shell).toContain('data-sf-trust-levels-structure-cancel')
    expect(shell).toContain("t('admin.forum.trustLevels.structureConfirm.reevaluate')")
    expect(shell).toContain("t('admin.forum.trustLevels.structureConfirm.noMigration')")
    expect(shell).not.toMatch(/toast[\s\S]{0,200}structureConfirm/)
    // P2：字段级错误贴表单；页面级块只在有明细时出现，绝不留空白条。
    expect(shell).toContain('v-if="saveErrorBuckets.pageLevel.length > 0"')
    expect(shell).toContain('data-sf-trust-levels-save-errors')
    expect(composable).toContain('saveErrorsFromError(error)')
    expect(composable).toContain("t('admin.forum.trustLevels.errors.summary', { count: errors.length })")
  })

  test('routes retire and delete intents through one blocking dialog component', () => {
    // D-N8 / D-C2：停用与删除都要在确认那一刻读到后果，所以都走 modal，不再用页面顶部常驻条
    // （常驻条会被滚出视口，列表第 6 档按下停用时等于没披露）。
    expect(shell).toContain('SFAdminTrustLevelTierConfirmDialog')
    expect(shell).toContain(':open="Boolean(retireRequest)"')
    expect(shell).toContain(':open="Boolean(deleteRequest)"')
    expect(shell).toContain('@confirm="confirmRetire"')
    expect(shell).toContain('@confirm="confirmDelete"')
    // 旧实现（顶部 UAlert + 两个手写按钮）必须彻底退场，否则会出现两套确认路径。
    expect(shell).not.toContain('data-sf-trust-levels-retire-confirm')

    const dialog = readFileSync(
      new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelTierConfirmDialog.vue', import.meta.url),
      'utf8'
    )
    // 影响面逐条披露；无影响时也要说一句，不能留空。
    expect(dialog).toContain("mode: 'retire' | 'delete' | null")
    expect(dialog).toContain('impactKeys.length > 0')
    expect(dialog).toContain("t('admin.forum.trustLevels.tiers.retireConfirm.noImpact')")
    // 删除文案必须提「不改等级」「id 永不复用」，且不允许出现「可恢复」的承诺。
    expect(dialog).toContain("t('admin.forum.trustLevels.tierDelete.rankSafe')")
    expect(dialog).toContain("t('admin.forum.trustLevels.tierDelete.idNote')")
    expect(dialog).not.toContain('tierDelete.restore')
    // 自建内容根补 ARIA（UModal 的内容根不带 role/aria-modal）。
    expect(dialog).toContain('role="dialog"')
    expect(dialog).toContain('aria-modal="true"')
  })
})
