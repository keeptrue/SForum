import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  cloneTrustLevelsForm,
  earnableKeys,
  earnableWhitelistForSave,
  idleTrustLevelWhitelist,
  parseBaselinePermissions,
  parseEligiblePermissions,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  resolveTrustLevelWhitelist,
  sanitizeEarnableForm,
  sanitizeEarnableKeys,
  toggleEarnableKey,
  TRUST_LEVEL_ADMIN_READ_PATH,
  TRUST_LEVEL_EDITABLE_LEVELS,
  TRUST_LEVEL_SLUGS,
  TRUST_LEVEL_THRESHOLD_METRICS,
  TRUST_LEVELS_OPTION,
  TRUST_LEVELS_SCHEMA_VERSION,
  trustLevelsPayload,
  unknownEarnableKeys,
  type TrustLevelWhitelistState
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

  test('mounts the earnable matrix as the single owner of the earnable field', () => {
    // 矩阵是独立组件（规范 §1）：tab 只传状态与事件，不自己画格子。
    expect(tab).toContain('<SFAdminTrustLevelEarnableMatrix')
    expect(tab).toContain(':form="form"')
    expect(tab).toContain(':whitelist="whitelistState"')
    expect(tab).toContain(':can-manage="canManage"')
    expect(tab).toContain('@toggle="toggleEarnable"')
    expect(tab).toContain('@retry="loadWhitelist"')
    // 阈值区里没有勾选控件：两套控件混在一个 fieldset 里会让「保存一次」失去边界。
    expect(tab).not.toContain('type="checkbox"')
    expect(tab).not.toMatch(/v-model[^\n]*earnable/)
  })

  test('gates saving on trust.manage and never sends a whitelist it could not read', () => {
    expect(tab).toContain("can('trust.manage')")
    expect(tab).toContain('earnableWhitelistForSave(whitelistState.value)')
    expect(tab).toContain(':disabled="!canManage"')
    // R1：功能关闭时保存会成功但不生效，必须提示。
    expect(tab).toContain('data-sf-trust-levels-disabled')
    // 关闭态只允许一条 alert：开关关掉时端点必然 404，短路不发请求，别让失败条再报一次。
    expect(tab).toContain('if (!trustLevelsEnabled.value) return')
    expect(tab).toMatch(/<SFAdminTrustLevelEarnableMatrix\s+v-if="trustLevelsEnabled"/)
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
    // useApiClient 自己补 /api/v1：请求路径里写死前缀就是绕开它的 baseUrl。
    expect(composable).not.toContain('/api/v1/')
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

  test('treats a malformed payload as failure, never as an empty whitelist', () => {
    for (const raw of [undefined, null, 'topic.create', {}, ['topic.create', 7], ['topic.create', '']]) {
      expect(parseEligiblePermissions(raw)).toBeNull()
    }
  })

  test('separates request failure, malformed response, and a genuinely empty whitelist', () => {
    // 1) 空集合 = ready + []：后端明确说「没有可勾的键」，这是事实不是错误。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { eligiblePermissions: [] } }))
      .toEqual({ status: 'ready', keys: [], baselineKeys: [], error: null })
    // 2) 形状不对 = failed + invalid_response：不许冒充空集合（T22）。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { levels: [] } }))
      .toEqual({ status: 'failed', keys: [], baselineKeys: [], error: 'invalid_response' })
    // 3) 请求失败带后端 reason；没有 reason 时给稳定兜底码。
    expect(resolveTrustLevelWhitelist({
      ok: false,
      error: { code: 403, message: 'forbidden', data: { reason: 'forbidden' } }
    })).toEqual({ status: 'failed', keys: [], baselineKeys: [], error: 'forbidden' })
    expect(resolveTrustLevelWhitelist({ ok: false, error: new Error('network') }))
      .toEqual({ status: 'failed', keys: [], baselineKeys: [], error: 'request_failed' })
    // 4) 初始态既不是 failed 也不是 ready：组件不能据此画空矩阵。
    expect(idleTrustLevelWhitelist()).toEqual({ status: 'idle', keys: [], baselineKeys: [], error: null })
  })
})

describe('system baseline keys (D34 / Z3-b″)', () => {
  test('reads baseline keys from the admin read field, never from a frontend copy', () => {
    const body = {
      eligiblePermissions: WHITELIST,
      baselinePermissions: ['topic.create', 'attachment.upload']
    }
    expect(resolveTrustLevelWhitelist({ ok: true, body })).toEqual({
      status: 'ready',
      keys: WHITELIST,
      baselineKeys: ['topic.create', 'attachment.upload'],
      error: null
    })
    // 后端没下发该字段：baselineKeys 为空 = 「无可披露的基线」，不是「基线为空集」的断言。
    expect(resolveTrustLevelWhitelist({ ok: true, body: { eligiblePermissions: WHITELIST } }))
      .toEqual({ status: 'ready', keys: WHITELIST, baselineKeys: [], error: null })
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

describe('earnable matrix pure helpers', () => {
  test('toggles one level without mutating the input form', () => {
    const base = recommendedTrustLevelsForm()
    const toggled = toggleEarnableKey(base, 2, 'topic.create', WHITELIST)
    expect(earnableKeys(toggled, 2)).toEqual(['topic.create'])
    // 纯函数：入参不动。
    expect(earnableKeys(base, 2)).toEqual([])
    expect(earnableKeys(toggleEarnableKey(toggled, 2, 'topic.create', WHITELIST), 2)).toEqual([])
  })

  test('records only that level (Permissions semantics), never copies up from Grants', () => {
    // 后端 Grants 累积（TL2 的键 TL3/TL4 也拿到），UI 写的是 Permissions 的「该档新增」。
    // 复制到高档会变成两份配置：取消 TL2 后高档仍留着键，运营看到的就是错的。
    const toggled = toggleEarnableKey(recommendedTrustLevelsForm(), 2, 'topic.create', WHITELIST)
    expect(earnableKeys(toggled, 3)).toEqual([])
    expect(earnableKeys(toggled, 4)).toEqual([])
    expect(Object.keys(toggled.earnable).sort()).toEqual(['1', '2', '3', '4'])
  })

  test('rejects TL0 and keys outside the whitelist, fail-closed', () => {
    const base = recommendedTrustLevelsForm()
    // TL0 由 trust.new_user_* 决定，config.go 拒绝它出现在 levels 里。
    expect(toggleEarnableKey(base, 0, 'topic.create', WHITELIST)).toEqual(base)
    expect(toggleEarnableKey(base, 2, 'user.ban', WHITELIST)).toEqual(base)
    // 白名单还没读到时传空数组：一律拒绝，不许先勾着再等请求回来。
    expect(toggleEarnableKey(base, 2, 'topic.create', [])).toEqual(base)
  })

  test('orders selected keys by whitelist order, not click order', () => {
    const clicked = toggleEarnableKey(
      toggleEarnableKey(recommendedTrustLevelsForm(), 2, 'post.create', WHITELIST),
      2,
      'topic.create',
      WHITELIST
    )
    expect(earnableKeys(clicked, 2)).toEqual(['topic.create', 'post.create'])
  })

  test('sanitizes every level and reports what a save would drop', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['user.ban', 'topic.create']
    const clean = sanitizeEarnableForm(form, WHITELIST)
    expect(clean.earnable['2']).toEqual(['topic.create'])
    expect(clean.earnable['1']).toEqual([])
    expect(Object.keys(clean.earnable).sort()).toEqual(['1', '2', '3', '4'])
    expect(sanitizeEarnableKeys(['user.ban', 'topic.create'], WHITELIST)).toEqual(['topic.create'])
    expect(unknownEarnableKeys(['user.ban', 'topic.create', 'user.ban'], WHITELIST)).toEqual(['user.ban'])
  })
})

describe('earnable payload contract', () => {
  test('drops out-of-whitelist keys and keeps every level earnable an array', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['1'] = ['topic.create', 'user.ban']
    const decoded = JSON.parse(trustLevelsPayload(form, WHITELIST)[0]!.value) as {
      levels: Array<Record<string, unknown>>
    }
    expect(decoded.levels.map(entry => entry.earnable)).toEqual([['topic.create'], [], [], []])
  })

  test('without a loaded whitelist it passes stored keys through instead of guessing', () => {
    // 白名单没读到时无法判断合法性：静默清空已存配置比发出一个可能被拒的键更糟。
    const form = recommendedTrustLevelsForm()
    form.earnable['3'] = ['topic.create']
    const decoded = JSON.parse(trustLevelsPayload(form)[0]!.value) as {
      levels: Array<Record<string, unknown>>
    }
    expect(decoded.levels.map(entry => entry.earnable)).toEqual([[], [], ['topic.create'], []])
  })

  test('round-trips [] as []: never null, never omitted, one entry per level', () => {
    const stored = JSON.stringify({
      schemaVersion: 1,
      levels: [
        { levelSlug: 'member', requirements: {}, limits: {}, earnable: [] },
        // 后端 decodeEarnableList：null → 空集（与 [] 等价）；只有键整个缺失才 422。
        { levelSlug: 'resident', requirements: {}, limits: {}, earnable: null },
        { levelSlug: 'veteran', requirements: {}, limits: {}, earnable: [] },
        { levelSlug: 'leader', requirements: {}, limits: {}, earnable: ['topic.create'] }
      ]
    })
    const form = readTrustLevelsConfig({
      [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value: stored } as never
    })
    expect(form.earnable).toEqual({ '1': [], '2': [], '3': [], '4': ['topic.create'] })

    const value = trustLevelsPayload(form)[0]!.value
    expect(value).not.toContain('"earnable":null')
    expect(value.match(/"earnable":\[\]/g) ?? []).toHaveLength(3)
    const decoded = JSON.parse(value) as { levels: Array<Record<string, unknown>> }
    expect(decoded.levels).toHaveLength(4)
    for (const entry of decoded.levels) {
      // T23：键必须在，且必须是数组。省略会被 DecodeConfig 判 422。
      expect(Object.keys(entry)).toContain('earnable')
      expect(Array.isArray(entry.earnable)).toBe(true)
    }
    expect(decoded.levels[3]!.earnable).toEqual(['topic.create'])
  })

  test('only hands the whitelist to the payload when the read actually succeeded', () => {
    // 失败/加载/初始态一律不传 = 原样带回：把「读不到」当成「白名单为空」会静默清空配置。
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['topic.create']
    const cases: Array<[TrustLevelWhitelistState, string[]]> = [
      [{ status: 'idle', keys: [], baselineKeys: [], error: null }, ['topic.create']],
      [{ status: 'loading', keys: [], baselineKeys: [], error: null }, ['topic.create']],
      [{ status: 'failed', keys: [], baselineKeys: [], error: 'forbidden' }, ['topic.create']],
      [{ status: 'ready', keys: ['post.create'], baselineKeys: [], error: null }, []]
    ]
    for (const [state, expected] of cases) {
      const decoded = JSON.parse(trustLevelsPayload(form, earnableWhitelistForSave(state))[0]!.value) as {
        levels: Array<Record<string, unknown>>
      }
      expect(decoded.levels[1]!.earnable).toEqual(expected)
    }
  })

  test('writes gate-key history back untouched: no control does not mean drop the value', () => {
    // S2 门控键在 UI 里没有勾选框，但存量配置必须逐字节回写——否则每次保存都会静默
    // 改写运营的历史配置（designer 规范 §6.5，本仓「静默/两份」失败类的新入口）。
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['topic.create', 'post.create']
    const state: TrustLevelWhitelistState = {
      status: 'ready',
      keys: WHITELIST,
      baselineKeys: ['topic.create', 'attachment.upload'],
      error: null
    }
    const decoded = JSON.parse(trustLevelsPayload(form, earnableWhitelistForSave(state))[0]!.value) as {
      levels: Array<Record<string, unknown>>
    }
    expect(decoded.levels[1]!.earnable).toEqual(['topic.create', 'post.create'])
    // 再走一次「勾选一个可操作键」的路径：门控键的存量值仍然在。
    const toggled = toggleEarnableKey(form, 2, 'post.edit_own', WHITELIST)
    const again = JSON.parse(trustLevelsPayload(toggled, earnableWhitelistForSave(state))[0]!.value) as {
      levels: Array<Record<string, unknown>>
    }
    expect(again.levels[1]!.earnable).toEqual(['topic.create', 'post.create', 'post.edit_own'])
  })

  test('still emits earnable for a level the form forgot', () => {
    const sparse = {
      ...recommendedTrustLevelsForm(),
      earnable: { '1': [], '2': [], '4': [] }
    }
    const decoded = JSON.parse(trustLevelsPayload(sparse)[0]!.value) as {
      levels: Array<Record<string, unknown>>
    }
    expect(decoded.levels.map(entry => entry.earnable)).toEqual([[], [], [], []])
  })
})
