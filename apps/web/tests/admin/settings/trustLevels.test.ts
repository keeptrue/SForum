import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  cloneTrustLevelsForm,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  TRUST_LEVEL_EDITABLE_LEVELS,
  TRUST_LEVEL_SLUGS,
  TRUST_LEVEL_THRESHOLD_METRICS,
  TRUST_LEVELS_OPTION,
  TRUST_LEVELS_SCHEMA_VERSION,
  trustLevelsPayload
} from '~/composables/admin/settings/useTrustLevelsThresholds'

/**
 * 后台「信任等级」tab 的登记与契约。
 *
 * 6 处登记点漏任何一处都会**静默失效**（URL 回落 basic / 渲染空面板 / 显示裸键）；
 * 另外 `trust.levels` 的形状由 Go 契约（`app/Models/TrustLevels/config.go`）决定，
 * 后端开了 `DisallowUnknownFields`，所以字段名、数组形态、档位范围在这里逐条钉住。
 */
const shell = readFileSync(
  new URL('../../../app/pages/admin/settings/index.vue', import.meta.url),
  'utf8'
)
const tab = readFileSync(
  new URL('../../../app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue', import.meta.url),
  'utf8'
)
const composable = readFileSync(
  new URL('../../../app/composables/admin/settings/useTrustLevelsThresholds.ts', import.meta.url),
  'utf8'
)
const zh = JSON.parse(readFileSync(new URL('../../../i18n/locales/zh-CN.json', import.meta.url), 'utf8'))
const en = JSON.parse(readFileSync(new URL('../../../i18n/locales/en-US.json', import.meta.url), 'utf8'))

const METRIC_SUFFIXES = [
  'days_visited',
  'topic_count',
  'comment_count',
  'like_received_count',
  'like_given_count',
  'bookmark_count'
]

describe('admin trust levels tab registration', () => {
  test('registers the tab in all four shell slots', () => {
    expect(shell).toContain("import SFAdminTrustLevelsTab from '~/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue'")
    // 联合类型：漏了会让 normalizeTab 的返回值无法赋给 activeTab。
    expect(shell).toMatch(/type SettingsTab = [^\n]*'trustLevels'/)
    // validTabs：漏了 `?tab=trustLevels` 会被静默回落 basic。
    expect(shell).toMatch(/const validTabs: SettingsTab\[\] = \[[^\]]*'newcomers', 'trustLevels'[^\]]*\]/)
    expect(shell).toContain("{ id: 'trustLevels', label: t('admin.settings.tabs.trustLevels'), icon: 'i-lucide-gauge' }")
    // tabComponents：漏了渲染空面板且不报错。
    expect(shell).toContain('trustLevels: SFAdminTrustLevelsTab')
  })

  test('sits right after newcomers in the tab strip', () => {
    const newcomersIndex = shell.indexOf("id: 'newcomers'")
    const trustIndex = shell.indexOf("id: 'trustLevels'")
    const maintenanceIndex = shell.indexOf("id: 'maintenance'")
    expect(newcomersIndex).toBeGreaterThan(-1)
    expect(trustIndex).toBeGreaterThan(newcomersIndex)
    expect(maintenanceIndex).toBeGreaterThan(trustIndex)
  })

  test('does not render a second page header or a second tab system', () => {
    // 注释里可以提到这些名字，但模板里不许出现它们的标签。
    expect(tab).not.toMatch(/<UDashboardToolbar/)
    expect(tab).not.toMatch(/<SFAdminFixedTabNav/)
    expect(tab).not.toContain('role="tablist"')
    expect(tab).not.toContain("t('admin.settings.title')")
  })

  test('renders one fieldset per level with a legend and six named inputs', () => {
    expect(tab).toContain('<fieldset')
    expect(tab).toContain('<legend')
    expect(tab).toContain(':data-level="String(level)"')
    expect(tab).toContain(':name="`trust.level.${level}.${metric.suffix}`"')
    expect(tab).toContain('v-for="level in TRUST_LEVEL_ROWS"')
    expect(tab).toContain('v-for="metric in TRUST_LEVEL_THRESHOLD_METRICS"')
    expect(tab).toContain("t('admin.settings.trustLevels.zeroUnlimited')")
  })

  test('keeps TL0 read-only because the Go config rejects it', () => {
    expect(tab).toContain(":data-readonly=\"level === 0 ? 'true' : 'false'\"")
    expect(tab).toContain("t('admin.settings.trustLevels.tl0Hint')")
    expect(tab).toContain('disabled')
  })

  test('lays the six thresholds out as one row per level at desktop width', () => {
    // 6 列 = metric 数，否则「5 档一屏可见」是假的。
    expect(tab).toContain('grid-template-columns: repeat(6, minmax(0, 1fr))')
    expect(tab).toContain('grid-template-columns: repeat(3, minmax(0, 1fr))')
    expect(tab).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(tab).toContain('grid-column: span 2')
  })

  test('keeps the D12 disclosure as a permanent alert next to runRestore', () => {
    expect(tab).toContain("t('admin.settings.trustLevels.restoreHint')")
    expect(tab).toContain('section.runRestore(')
    expect(tab).toContain("t('admin.settings.trustLevels.restoreDefaults')")
  })

  test('does not draw the capability-unlock matrix before the M4 contract exists', () => {
    expect(tab).not.toContain('type="checkbox"')
    // 没有绑定到 earnable 的输入控件（保存时原样带回不算编辑器）。
    expect(tab).not.toMatch(/v-model[^\n]*earnable/)
    expect(tab).toContain('D17 的 earnable 编辑入口留位')
  })

  test('never invents an endpoint path', () => {
    expect(tab).not.toMatch(/\/api\/v1\//)
    expect(composable).not.toMatch(/\/api\/v1\//)
  })
})

describe('trust.levels payload contract', () => {
  test('metrics match the Go Metrics list, without the phantom account_age_days', () => {
    expect(TRUST_LEVEL_THRESHOLD_METRICS.map(metric => metric.suffix)).toEqual(METRIC_SUFFIXES)
    expect(composable).not.toContain('account_age_days')
  })

  test('emits schemaVersion + a levels array of TL1..TL4 with slug/requirements/limits/earnable', () => {
    const payload = trustLevelsPayload(recommendedTrustLevelsForm())
    expect(payload).toHaveLength(1)
    expect(payload[0]!.name).toBe(TRUST_LEVELS_OPTION)

    const decoded = JSON.parse(payload[0]!.value) as {
      schemaVersion: number
      levels: Array<Record<string, unknown>>
    }
    expect(decoded.schemaVersion).toBe(TRUST_LEVELS_SCHEMA_VERSION)
    expect(decoded.levels).toHaveLength(4)
    expect(decoded.levels.map(entry => entry.levelSlug))
      .toEqual(['member', 'resident', 'veteran', 'leader'])
    // TL0 由 trust.new_user_* 决定，config.go 会直接拒绝。
    expect(decoded.levels.map(entry => entry.levelSlug)).not.toContain('newcomer')

    for (const entry of decoded.levels) {
      expect(Object.keys(entry).sort()).toEqual(['earnable', 'levelSlug', 'limits', 'requirements'])
      expect(Object.keys(entry.requirements as Record<string, number>).sort()).toEqual([...METRIC_SUFFIXES].sort())
      expect(entry.limits).toEqual({
        topicCooldownSeconds: null,
        commentCooldownSeconds: null,
        dailyTopicLimit: null,
        dailyCommentLimit: null
      })
      expect(entry.earnable).toEqual([])
    }
  })

  test('recommended values equal the Go DefaultPolicy anchors', () => {
    const form = recommendedTrustLevelsForm()
    // TL1 保守：3 天 / 5 回复，不要求发主题与点赞。
    expect(form.requirements['1']).toEqual({
      days_visited: 3,
      topic_count: 0,
      comment_count: 5,
      like_received_count: 0,
      like_given_count: 0,
      bookmark_count: 0
    })
    // TL4 = 百日 / 25 主题 / 250 回复 / 收 100 给 80 / 收藏 20。
    expect(form.requirements['4']).toEqual({
      days_visited: 100,
      topic_count: 25,
      comment_count: 250,
      like_received_count: 100,
      like_given_count: 80,
      bookmark_count: 20
    })
  })

  test('reads an existing config back without dropping limits or earnable', () => {
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

    expect(form.requirements['1']!.days_visited).toBe(4)
    // 0 是「免冷却」，不能与 null（继承站点默认）混为一谈（D27）。
    expect(form.limits['1']).toEqual({
      topicCooldownSeconds: 0,
      commentCooldownSeconds: null,
      dailyTopicLimit: 5,
      dailyCommentLimit: null
    })
    expect(form.earnable['1']).toEqual(['topic.create'])
    // TL0 不进表单。
    expect(Object.keys(form.requirements).sort()).toEqual(['1', '2', '3', '4'])

    const roundTrip = JSON.parse(trustLevelsPayload(form)[0]!.value) as { levels: Array<Record<string, unknown>> }
    expect(roundTrip.levels).toHaveLength(4)
    expect(roundTrip.levels[0]!.earnable).toEqual(['topic.create'])
    expect((roundTrip.levels[0]!.limits as Record<string, unknown>).topicCooldownSeconds).toBe(0)
  })

  test('falls back to the recommended defaults on unknown schemaVersion or broken JSON', () => {
    const recommended = recommendedTrustLevelsForm()
    for (const value of ['{"schemaVersion":99,"levels":[]}', 'not-json', '']) {
      const form = readTrustLevelsConfig({ [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value } as never })
      expect(form.requirements).toEqual(recommended.requirements)
    }
  })

  test('keeps four editable levels and five display rows', () => {
    expect([...TRUST_LEVEL_EDITABLE_LEVELS]).toEqual([1, 2, 3, 4])
    expect(TRUST_LEVEL_SLUGS[0]).toBe('newcomer')
    const clone = cloneTrustLevelsForm(recommendedTrustLevelsForm())
    clone.requirements['2']!.topic_count = 99
    // 深拷贝：改副本不能污染原对象。
    expect(recommendedTrustLevelsForm().requirements['2']!.topic_count).toBe(2)
  })
})

describe('trust level locale keys', () => {
  test('ships every tab key in both locales', () => {
    for (const locale of [zh, en]) {
      expect(locale.admin.settings.tabs.trustLevels).toBeTruthy()
      const section = locale.admin.settings.trustLevels
      for (const key of ['title', 'description', 'restoreDefaults', 'restoreHint', 'zeroUnlimited', 'resetChanges', 'tl0Hint']) {
        expect(section[key]).toBeTruthy()
      }
      for (const slug of ['newcomer', 'member', 'resident', 'veteran', 'leader']) {
        expect(section.level[slug]).toBeTruthy()
      }
      expect(Object.keys(section.metric).sort())
        .toEqual(['bookmarkCount', 'commentCount', 'daysVisited', 'likeGiven', 'likeReceived', 'topicCount'])
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
