import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import {
  composeProfileSettingsTabs,
  firstProfileSettingsErrorTab,
  profileMetaRows,
  profileSettingsErrorBuckets,
  profileSettingsErrorCounts,
  profileSettingsFieldTabMap,
  profileSettingsDraftValuesAsSettings,
  profileSettingsFieldValue,
  profileSettingsMetaDirty,
  profileSettingsMetaPayload,
  profileSettingsMetaValues,
  profileSettingsPanelDomId,
  profileSettingsTabDomId,
  profileSettingsTabForField,
  normalizeProfileSettingsSection,
  normalizeProfileSettingsSections,
  resolveLocalizedText,
  resolveProfileSettingsTab,
  type ProfileSettingsSection
} from '../../app/utils/settings/profileSettings'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const shell = () => source('../../app/components/settings/SFProfileSettingsPage.vue')
const tabNav = () => source('../../app/components/settings/SFProfileSettingsTabNav.vue')
const sharedNav = () => source('../../app/components/settings/SFSettingsTabNav.vue')
const extensionPanel = () => source('../../app/components/settings/profile/SFProfileExtensionTabPanel.vue')
const footer = () => source('../../app/components/settings/profile/SFProfileSettingsFooter.vue')
const identityTab = () => source('../../app/components/settings/profile/tabs/SFProfileIdentityTab.vue')
const storyTab = () => source('../../app/components/settings/profile/tabs/SFProfileStoryTab.vue')
const linksTab = () => source('../../app/components/settings/profile/tabs/SFProfileLinksTab.vue')
const draftComposable = () => source('../../app/composables/settings/useProfileSettingsDraft.ts')
const zhCN = () => JSON.parse(source('../../i18n/locales/zh-CN.json'))
const enUS = () => JSON.parse(source('../../i18n/locales/en-US.json'))

const CORE_LABELS = { identity: '身份', story: '介绍', links: '地点与链接' }

function section(overrides: Partial<ProfileSettingsSection> = {}): ProfileSettingsSection {
  return {
    id: 'ext.demo.fields',
    label: '演示分区',
    source: 'extension',
    extensionId: 'demo',
    type: 'fields',
    fields: [{ fieldKey: 'demo.city', label: '城市', type: 'string' }],
    ...overrides
  }
}

describe('profile settings tab composition', () => {
  test('core three tabs always lead, extension sections append in server order', () => {
    const tabs = composeProfileSettingsTabs([
      section({ id: 'ext.a', label: 'A' }),
      section({ id: 'ext.b', label: 'B' })
    ], CORE_LABELS)
    expect(tabs.map(tab => tab.id)).toEqual(['identity', 'story', 'links', 'ext.a', 'ext.b'])
    expect(tabs[0]?.label).toBe('身份')
    expect(tabs[3]?.source).toBe('extension')
  })

  test('drops unusable sections instead of rendering a dead tab', () => {
    const tabs = composeProfileSettingsTabs([
      section({ id: '', label: '无名' }),
      section({ id: 'ext.nolabel', label: '   ' }),
      section({ id: 'ext.component', type: 'component', componentId: '' }),
      section({ id: 'identity', label: '撞核心 id' }),
      section({ id: 'ext.dup', label: '重复' }),
      section({ id: 'ext.dup', label: '重复' })
    ], CORE_LABELS)
    expect(tabs.map(tab => tab.id)).toEqual(['identity', 'story', 'links', 'ext.dup'])
  })

  test('a field section without any usable field is not a tab', () => {
    const tabs = composeProfileSettingsTabs([
      section({ id: 'ext.empty', fields: [] }),
      section({ id: 'ext.onlybad', fields: [{ label: '没有键', type: 'string' } as never] })
    ], CORE_LABELS)
    expect(tabs.map(tab => tab.id)).toEqual(['identity', 'story', 'links'])
  })

  test('component sections keep their componentId, field sections never do', () => {
    const tabs = composeProfileSettingsTabs([
      section({ id: 'ext.c', type: 'component', componentId: 'profile-panel' }),
      section({ id: 'ext.f', type: 'fields' })
    ], CORE_LABELS)
    expect(tabs.find(tab => tab.id === 'ext.c')?.componentId).toBe('profile-panel')
    expect(tabs.find(tab => tab.id === 'ext.f')?.fields).toHaveLength(1)
  })
})

describe('profile settings tab query contract', () => {
  const tabs = composeProfileSettingsTabs([section()], CORE_LABELS)

  test('accepts a known tab, including extension ids', () => {
    expect(resolveProfileSettingsTab('story', tabs)).toBe('story')
    expect(resolveProfileSettingsTab('ext.demo.fields', tabs)).toBe('ext.demo.fields')
    expect(resolveProfileSettingsTab(['links'], tabs)).toBe('links')
  })

  test('falls back to identity for unknown, empty and array-shaped values', () => {
    expect(resolveProfileSettingsTab('nope', tabs)).toBe('identity')
    expect(resolveProfileSettingsTab(undefined, tabs)).toBe('identity')
    expect(resolveProfileSettingsTab('', tabs)).toBe('identity')
    expect(resolveProfileSettingsTab(['unknown', 'story'], tabs)).toBe('identity')
  })

  test('keeps the current tab when it is still valid (no flip-flop on data refresh)', () => {
    expect(resolveProfileSettingsTab('unknown', tabs, 'ext.demo.fields')).toBe('ext.demo.fields')
  })

  test('the shell writes ?tab= with replace and drops it for the default tab', () => {
    const src = shell()
    expect(src).toContain("router.replace({ query })")
    expect(src).not.toContain('router.push(')
    expect(src).toContain('if (tab === PROFILE_SETTINGS_DEFAULT_TAB)')
    expect(src).toContain('delete query.tab')
    // 深链里的插件分区 id 必须等 GET /profile 落地后再归一化，否则会被当成未知值。
    expect(src).toContain('if (!formReady.value) return')
    expect(src).toContain('resolveProfileSettingsTab(route.query.tab, list, activeTab.value)')
  })
})

describe('profile settings field → tab mapping', () => {
  test('core fields map to their own tabs', () => {
    expect(profileSettingsTabForField('displayName')).toBe('identity')
    expect(profileSettingsTabForField('bio')).toBe('story')
    expect(profileSettingsTabForField('signature')).toBe('story')
    expect(profileSettingsTabForField('location')).toBe('links')
    expect(profileSettingsTabForField('websiteUrl')).toBe('links')
    expect(profileSettingsTabForField('unknown')).toBe('')
  })

  test('extension fields map to the section that declared them', () => {
    const map = profileSettingsFieldTabMap([section({ id: 'ext.demo.fields' })])
    expect(map['demo.city']).toBe('ext.demo.fields')
    expect(map.bio).toBe('story')
  })

  test('core validation keys map to their tabs exactly as the API names them', () => {
    // 后端 core 校验错误用的是资料字段名，不是组合键；这张表就是契约。
    const table: Array<[string, string]> = [
      ['websiteUrl', 'links'],
      ['location', 'links'],
      ['bio', 'story'],
      ['signature', 'story'],
      ['displayName', 'identity']
    ]
    for (const [fieldKey, tabId] of table) {
      expect(profileSettingsTabForField(fieldKey)).toBe(tabId)
      expect(profileSettingsErrorCounts({ [fieldKey]: ['不对'] })).toEqual({ [tabId]: 1 })
    }
    expect(profileSettingsErrorCounts({
      websiteUrl: ['不是合法网址'],
      bio: ['太长'],
      signature: ['太长'],
      displayName: ['重名']
    })).toEqual({ links: 1, story: 2, identity: 1 })
  })

  test('keys that belong to no tab land in the global bucket instead of vanishing', () => {
    const buckets = profileSettingsErrorBuckets({
      websiteUrl: ['不是合法网址'],
      'x.unknown.field': ['未声明'],
      nickname: ['冷却中']
    }, [section({ id: 'ext.demo.fields' })])
    expect(buckets.byTab).toEqual({ links: 1 })
    expect(buckets.global).toBe(2)
    expect(buckets.unaligned).toEqual(['x.unknown.field', 'nickname'])
  })

  test('core errors also paint the tab dot and the footer summary', () => {
    const tabs = composeProfileSettingsTabs([], CORE_LABELS)
    const counts = profileSettingsErrorCounts({ websiteUrl: ['不是合法网址'] })
    expect(firstProfileSettingsErrorTab(counts, tabs)).toEqual({ id: 'links', count: 1 })
    const shellSrc = source('../../app/components/settings/SFProfileSettingsPage.vue')
    const footerSrc = source('../../app/components/settings/profile/SFProfileSettingsFooter.vue')
    expect(shellSrc).toContain(':error-counts="errorCounts"')
    expect(shellSrc).toContain(':global-error-count="globalErrorCount"')
    expect(footerSrc).toContain('profileSettings.tabs.errorGlobal')
    expect(footerSrc).toContain('role="status"')
    for (const locale of [zhCN(), enUS()]) {
      expect(locale.profileSettings.tabs.errorGlobal).toBeTruthy()
    }
  })

  test('successful saves report once: Toast only, no duplicate inline alert', () => {
    const draft = draftComposable()
    const shellSrc = shell()
    expect(draft).toContain("title: t('profileSettings.saved')")
    expect(draft).toContain('duration: 5000')
    expect(shellSrc).not.toContain('successMessage')
    expect(draft).not.toContain('successMessage')
    // 错误仍保留可关闭的内联 alert（用户必须读到）。
    expect(shellSrc).toContain('variant="danger"')
  })

  test('error counts become per-tab counts, unknown keys are ignored', () => {
    const counts = profileSettingsErrorCounts({
      bio: ['太长了'],
      signature: ['不能包含链接'],
      'demo.city': ['必填'],
      ghost: ['不存在的字段']
    }, [section()])
    expect(counts).toEqual({ story: 2, 'ext.demo.fields': 1 })
  })

  test('the first error tab follows tab order, not object key order', () => {
    const tabs = composeProfileSettingsTabs([section()], CORE_LABELS)
    const counts = profileSettingsErrorCounts({ 'demo.city': ['必填'], signature: ['太长了'] }, [section()])
    expect(firstProfileSettingsErrorTab(counts, tabs)).toEqual({ id: 'story', count: 1 })
  })

  test('tab errors surface only after submit and jump on click', () => {
    const src = shell()
    expect(src).toContain(':error-counts="errorCounts"')
    expect(src).toContain('function jumpToErrorTab()')
    expect(src).toContain('activeTab.value = first.id')
    // 提交失败只把错误落进 fieldErrors，不自动切走用户正在编辑的分区。
    expect(draftComposable()).toContain('fieldErrors.value = apiErrorFields(error)')
    expect(draftComposable()).not.toContain('activeTab')
  })
})

describe('profile settings tab nav a11y contract', () => {
  test('shares one tab bar implementation with the account security page', () => {
    const nav = sharedNav()
    expect(nav).toContain('role="tablist"')
    expect(nav).toContain('role="tab"')
    expect(nav).toContain(':aria-selected="isActive(tab.id)"')
    expect(nav).toContain(':aria-controls="panelElementId(tab.id)"')
    expect(nav).toContain(':tabindex="isActive(tab.id) ? 0 : -1"')
    expect(nav).toContain('sforum-settings__tabs')
    expect(nav).toContain('sforum-settings__tab')
    // 循环 + Home/End
    expect(nav).toContain("case 'ArrowRight'")
    expect(nav).toContain("case 'ArrowLeft'")
    expect(nav).toContain("case 'Home'")
    expect(nav).toContain("case 'End'")
    expect(nav).toContain("inline: 'nearest'")
    // 链接模型（账号安全）仍走路由
    expect(nav).toContain('aria-current')
  })

  test('the panel owns the id pairing and only one panel renders at a time', () => {
    const src = shell()
    expect(src).toContain(':id="profileSettingsPanelDomId(activeTab)"')
    expect(src).toContain(':aria-labelledby="profileSettingsTabDomId(activeTab)"')
    expect(src).toContain('role="tabpanel"')
    expect(profileSettingsTabDomId('identity')).toBe('profile-settings-tab-identity')
    expect(profileSettingsPanelDomId('identity')).toBe('profile-settings-panel-identity')
    // v-if 链 = 同时只有一个面板；不是若干 v-show 摆在一起。
    expect(src.match(/role="tabpanel"/g)).toHaveLength(1)
    expect(src).toContain('v-else-if="activeSection"')
  })

  test('error dots are decorative with hidden text, and long plugin labels keep a title', () => {
    const nav = sharedNav()
    expect(nav).toContain('sforum-settings__tab-error')
    expect(nav).toContain('aria-hidden="true"')
    expect(nav).toContain('class="sr-only"')
    expect(nav).toContain(':aria-describedby="tab.error ? errorId(tab.id) : undefined"')
    expect(nav).toContain(':title="tab.title"')
    expect(tabNav()).toContain("title: tab.source === 'extension' ? tab.label : undefined")
    // 没有计数徽标，也没有 emoji。
    expect(nav).not.toContain('tab.count')
    expect(nav).not.toMatch(/\p{Extended_Pictographic}/u)
  })

  test('no anchor nav or duplicated page heading survives the tab conversion', () => {
    const src = shell()
    expect(src).not.toContain('sforum-settings__section-nav')
    expect(src).not.toContain('href="#profile-settings-')
    for (const tab of [identityTab(), storyTab(), linksTab()]) {
      expect(tab).not.toMatch(/<h2\b/)
      expect(tab).toContain('sforum-settings-profile__panel-description')
    }
    expect(source('../../app/assets/css/sforum-settings.css')).not.toContain('section-nav')
  })
})

describe('profile settings draft contract', () => {
  test('one PUT carries core fields plus meta, nickname only when changed', () => {
    const src = draftComposable()
    expect(src.match(/updateMyProfile\(/g)).toHaveLength(1)
    expect(src).toContain('...(nicknameChanged ? { displayName: draft.displayName } : {})')
    expect(src).toContain('...(metaChanged ? { meta } : {})')
    expect(src).toContain('await options.updateLanguage(draft.locale)')
  })

  test('meta payload only carries changed keys and clears with null', () => {
    expect(profileSettingsMetaPayload({ 'demo.city': '上海' }, { 'demo.city': '北京' }))
      .toEqual({ 'demo.city': '上海' })
    expect(profileSettingsMetaPayload({ 'demo.city': '' }, { 'demo.city': '北京' }))
      .toEqual({ 'demo.city': null })
    expect(profileSettingsMetaPayload({ 'demo.city': '北京' }, { 'demo.city': '北京' })).toEqual({})
    expect(profileSettingsMetaDirty({ 'demo.city': '北京' }, { 'demo.city': '北京' })).toBe(false)
  })

  test('declarative field values respect saved value, then default, then empty', () => {
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'string' }, { a: 'saved' })).toBe('saved')
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'string', default: 'd' }, {})).toBe('d')
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'number' }, {})).toBe('')
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'boolean' }, {})).toBe(false)
    expect(profileSettingsMetaValues([section()], { 'demo.city': '上海' })).toEqual({ 'demo.city': '上海' })
  })

  test('PUT /profile echoes neither sections nor values — the draft must survive the save', () => {
    // 实测：PUT 只回资料行。若拿回显直接重建基线，插件 tab 会消失、刚存的值会被抹成默认值。
    const src = draftComposable()
    expect(src).toContain('settingsSections: data.settingsSections ?? previous?.settingsSections')
    expect(src).toContain('const sections = data.settingsSections ? sectionsOf(data) : sectionsOf(options.profile.value)')
    expect(src).toContain('profileSettingsDraftValuesAsSettings(submittedMeta)')
    expect(src).toContain('const snapshot = updated ? mergeSnapshot(updated, metaDraft.value) : null')
    expect(profileSettingsDraftValuesAsSettings({ 'x.a.b': 'light', 'x.a.c': '', 'x.a.d': false }))
      .toEqual({ 'x.a.b': 'light', 'x.a.d': false })
  })

  test('reset restores the saved baseline for both core fields and meta', () => {
    const src = draftComposable()
    expect(src).toContain('Object.assign(draft, baseline.value)')
    expect(src).toContain('metaDraft.value = { ...metaBaseline.value }')
    expect(src).toContain("title: t('profileSettings.resetDone')")
  })

  test('the five field types, required marker and error wiring live in one renderer', () => {
    const field = source('../../app/components/settings/shared/SFDeclarativeSettingsField.vue')
    for (const type of ['text', 'select', 'boolean', 'number']) {
      expect(field).toContain(`field.type === '${type}'`)
    }
    expect(field).toContain('field.required')
    expect(field).toContain('aria-describedby')
    expect(field).toContain('role="alert"')
    expect(extensionPanel()).toContain('<SFDeclarativeSettingsField')
  })
})

describe('plugin sections inside the profile page', () => {
  test('component sections mount the L2 widget and disable the page save', () => {
    const panel = extensionPanel()
    expect(panel).toContain('<SFExtensionWidget')
    expect(panel).toContain(':extension-id="section.extensionId || \'\'"')
    expect(panel).toContain(':component-id="section.componentId || \'\'"')
    const src = shell()
    expect(src).toContain("const pluginManagedTab = computed(() => activeSection.value?.source === 'extension' && activeSection.value.type === 'component')")
    expect(src).toContain("t('profileSettings.extension.saveManagedByExtension')")
    expect(src).toContain(':save-disabled-reason="saveDisabledReason"')
    // 禁用原因不能只靠 hover：aria-describedby 指向可见的原因行，title 兜底。
    expect(footer()).toContain(':aria-describedby="saveDisabledReason ? reasonId : undefined"')
    expect(footer()).toContain(':title="saveDisabledReason || undefined"')
    expect(src).toContain('const canSave = computed(() => isDirty.value && !saving.value && !pending.value && !pluginManagedTab.value)')
  })

  test('the footer stays inside the form and carries the error summary', () => {
    const src = shell()
    const formOpen = src.indexOf('<form')
    const formClose = src.indexOf('</form>')
    const footerTag = src.indexOf('<SFProfileSettingsFooter')
    expect(formOpen).toBeGreaterThan(-1)
    expect(footerTag).toBeGreaterThan(formOpen)
    expect(footerTag).toBeLessThan(formClose)
    expect(footer()).toContain('profileSettings.tabs.errorSummary')
    expect(footer()).toContain(':disabled="!canSave"')
  })

  test('tab and extension copy exists in both locales', () => {
    for (const locale of [zhCN(), enUS()]) {
      const tabs = locale.profileSettings.tabs
      for (const key of ['ariaLabel', 'identity', 'story', 'links', 'errorDot', 'errorSummary']) {
        expect(tabs[key]).toBeTruthy()
      }
      for (const key of ['componentNotice', 'componentLoading', 'saveManagedByExtension', 'required']) {
        expect(locale.profileSettings.extension[key]).toBeTruthy()
      }
    }
  })
})

describe('public profile meta rows', () => {
  test('renders label + value, skips empty values and falls back to the field key', () => {
    expect(profileMetaRows({
      gitlab: { label: 'GitLab', value: ' https://gitlab.com/u ' },
      empty: { label: '空', value: '   ' },
      nolabel: { label: '', value: '43' }
    })).toEqual([
      { fieldKey: 'gitlab', label: 'GitLab', value: 'https://gitlab.com/u' },
      { fieldKey: 'nolabel', label: 'nolabel', value: '43' }
    ])
    expect(profileMetaRows(undefined)).toEqual([])
  })

  test('a localized label object renders as its locale text, never as the field key', () => {
    const rows = profileMetaRows({
      'x.sforum-sett-7dc19bb0.signature_line': {
        label: { 'en-US': 'Signature line', 'zh-CN': '签名档' },
        value: '公开签名档-复验'
      }
    }, 'zh-CN')
    expect(rows[0]?.label).toBe('签名档')
    expect(rows[0]?.label).not.toContain('x.sforum-sett')
    // 换语言就换文案；标签缺失才回落到 fieldKey。
    expect(profileMetaRows({
      'x.a.b': { label: { 'en-US': 'Signature line', 'zh-CN': '签名档' }, value: 'v' }
    }, 'en-US')[0]?.label).toBe('Signature line')
    expect(profileMetaRows({ 'x.a.b': { label: '', value: 'v' } }, 'zh-CN')[0]?.label).toBe('x.a.b')
  })

  test('the about tab reads the TOP-LEVEL meta and never the nested profile.meta', () => {
    const page = source('../../app/components/profile/SFProfileShowPage.vue')
    // 契约（contracts/openapi/schemas/profile.yaml）：meta 是 PublicProfile 的顶层属性，
    // 与 profile / settingsValues 同级。读成 profile.meta 会永远拿到 undefined。
    expect(page).toContain('profileMetaRows(profile.value?.meta, locale.value)')
    expect(page).not.toContain('profile.value?.profile.meta')
    expect(page).toContain('v-for="row in metaRows"')
    expect(page).toContain('metaHref(row.value)')
    expect(page).toContain('rel="noopener noreferrer nofollow"')
  })

  test('the API contract declares meta on PublicProfile, not inside ProfileData', () => {
    const api = source('../../app/composables/profile/useProfileApi.ts')
    const publicProfile = api.match(/export type PublicProfile = \{[\s\S]*?\n\}/)?.[0] ?? ''
    const profileData = api.match(/export type ProfileData = \{[\s\S]*?\n\}/)?.[0] ?? ''
    expect(publicProfile).toContain('meta?: Record<string, ProfileMetaEntry>')
    expect(profileData).not.toContain('meta?:')
    // 注释指明层级，避免下一个人又按嵌套读
    expect(api).toContain('顶层属性')
  })
})

describe('wire shape normalisation (manifest payload vs settingsSection)', () => {
  test('localized text accepts a plain string or a locale map', () => {
    expect(resolveLocalizedText('  城市 ', 'zh-CN')).toBe('城市')
    expect(resolveLocalizedText({ 'zh-CN': '城市', 'en-US': 'City' }, 'zh-CN')).toBe('城市')
    expect(resolveLocalizedText({ 'zh-CN': '城市', 'en-US': 'City' }, 'en-US')).toBe('City')
    // 只给了别的语言也要能显示，不能渲染成空标签
    expect(resolveLocalizedText({ 'ja-JP': '市区' }, 'zh-CN')).toBe('市区')
    expect(resolveLocalizedText(undefined, 'zh-CN')).toBe('')
  })

  test('manifest `key` and host `fieldKey` are both accepted, labels resolved', () => {
    const section = normalizeProfileSettingsSection({
      id: 'x.demo.city',
      extensionId: 'demo',
      label: { 'zh-CN': '演示分区', 'en-US': 'Demo' },
      description: '一段说明',
      type: 'fields',
      fields: [
        { key: 'demo.city', label: '城市', type: 'string', required: true, placeholder: '请输入', constraints: { maxLength: 20 } },
        { fieldKey: 'demo.size', label: { 'zh-CN': '规模', 'en-US': 'Size' }, type: 'select', options: [{ value: 's', label: { 'zh-CN': '小', 'en-US': 'S' } }] }
      ]
    }, 'zh-CN')
    expect(section?.label).toBe('演示分区')
    expect(section?.description).toBe('一段说明')
    expect(section?.source).toBe('extension')
    expect(section?.fields.map(field => field.fieldKey)).toEqual(['demo.city', 'demo.size'])
    expect(section?.fields[0]?.required).toBe(true)
    expect(section?.fields[0]?.constraints?.maxLength).toBe(20)
    expect(section?.fields[1]?.options?.[0]).toEqual({ value: 's', label: '小' })
  })

  test('a field key has exactly two accepted spellings — no third alias', () => {
    const src = source('../../app/utils/settings/profileSettings.ts')
    // 契约名是 fieldKey；`key` 是 manifest 载荷的历史写法，只作防御性双收。
    expect(src).toContain('raw.fieldKey ?? raw.key')
    expect(src).not.toContain('field_key')
    expect(src).not.toContain('fieldName')
    expect(src).not.toContain('raw.keyName')
    // 分区 id 只认 `id`，不猜第二个名字。
    expect(src).not.toContain('sectionId')
  })

  test('fields follow the server effectiveSortOrder, not the array order', () => {
    const section = normalizeProfileSettingsSection({
      id: 'x.demo',
      extensionId: 'demo',
      label: '演示',
      type: 'fields',
      fields: [
        { fieldKey: 'x.demo.b', label: 'B', type: 'string', effectiveSortOrder: 20 },
        { fieldKey: 'x.demo.a', label: 'A', type: 'string', effectiveSortOrder: 10 }
      ]
    }, 'zh-CN')
    expect(section?.fields.map(field => field.fieldKey)).toEqual(['x.demo.a', 'x.demo.b'])
  })

  test('unsupported and incomplete entries are dropped, not rendered as empty tabs', () => {
    expect(normalizeProfileSettingsSection({ id: '', label: '无名' })).toBeNull()
    expect(normalizeProfileSettingsSection({ id: 'x.a', label: '   ' })).toBeNull()
    expect(normalizeProfileSettingsSections([{ id: 'x.a', label: 'A' }, null, 'nope'])).toHaveLength(1)
    expect(normalizeProfileSettingsSections(undefined)).toEqual([])
  })

  test('tab type decides rendering: component keeps its componentId, settings falls back to fields', () => {
    const component = normalizeProfileSettingsSection({ id: 'x.c', label: 'C', extensionId: 'demo', type: 'component', componentId: 'panel' })
    expect(component?.type).toBe('component')
    expect(component?.componentId).toBe('panel')
    // 个人设置面只允许 fields/component：settings 形状不属于这里。
    expect(normalizeProfileSettingsSection({ id: 'x.s', label: 'S', type: 'settings' })?.type).toBe('fields')
  })

  test('boolean defaults arrive as strings and must not read as true', () => {
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'boolean', default: 'false' }, {})).toBe(false)
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'boolean', default: 'true' }, {})).toBe(true)
    expect(profileSettingsFieldValue({ fieldKey: 'a', label: 'A', type: 'number' }, { a: '7' })).toBe(7)
  })
})
