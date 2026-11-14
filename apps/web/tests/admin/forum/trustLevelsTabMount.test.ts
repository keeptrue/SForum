import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, defineComponent, ref } from 'vue'
import {
  earnableMatrixState,
  earnableOrphanText,
  isBaselineKey
} from '~/composables/admin/forum/trustLevels/useTrustLevelEarnableMatrix'
import {
  limitsModeOf,
  parseLimitInput,
  setLimitsMode,
  setLimitValue,
  toLimitsForm,
  TRUST_LEVEL_LIMIT_KEYS
} from '~/composables/admin/forum/trustLevels/useTrustLevelLimits'
import {
  activeRankOf,
  activeTierCount,
  addTier,
  retireTier,
  retiredTierImpactKeys,
  setTierColor
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  TIER_COLOR_TOKENS,
  TIER_MIN_DARK_CONTRAST,
  defaultTierColorName,
  isTierCustomColor,
  normalizeTierColor,
  tierAccentContrast,
  tierAccentPreview,
  tierAccentStyle,
  tierColorOf,
  tierColorVar
} from '~/utils/profile/trustLevelColors'
import {
  blockNonIntegerKey,
  earnableKeys,
  recommendedTrustLevelsForm,
  TRUST_LEVEL_THRESHOLD_METRICS,
  type TrustLevelsForm,
  type TrustLevelWhitelistState
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import {
  DEFAULT_TIER_ICON_NAMES,
  defaultTierIconName,
  tierIconClass,
  trustLevelIconOf,
  trustLevelName,
  TRUST_LEVEL_METRIC_SUFFIX_BY_KEY
} from '~/composables/profile/useTrustLevel'
import {
  bucketSaveErrors,
  parseSaveErrors,
  saveErrorFieldLabel,
  saveErrorText,
  type TrustLevelSaveError
} from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import { TRUST_LEVEL_TIER_MAX, TRUST_LEVEL_TIER_NAME_MAX } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { compileVueSfc, mount } from '../../helpers/vueSfc'

/**
 * 信任等级独立页的两个编辑分区的**挂载**行为（J-07 / J-11 / spec §5），N 档版。
 *
 * 为什么要有这个文件：`trustLevels.test.ts` 里的登记断言是静态契约——它证明「写了这行」，
 * 证明不了「运营看到什么、点了会发生什么」。AGENTS.md 禁止用源码字符串代替渲染行为，
 * 所以这里挂真组件、只把 i18n / 权限 / 组件库换成可控替身。
 *
 * 三个场景：
 * 1. thresholds：**一档一行 × 六指标**（行数 = `form.tiers` 长度，TL0 只在页尾说明里出现）、
 *    只发 `threshold(tierId, suffix, value)` 意图、D12 常驻披露、停用档仍可编辑；
 * 2. earnable：矩阵整块继承既有语义（失败态锁定、不画空矩阵、勾选只发 `toggle(tierId, key)`）；
 * 3. feature-off：不渲染矩阵，给同尺寸占位，一个勾选框都没有。
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

const te = (key: string) => typeof lookup(key) === 'string'

function permissionLabel(item: { key: string }) {
  const path = `admin.permissionCatalog.${item.key}.label`
  return te(path) ? t(path) : item.key
}

const UIconStub = { name: 'UIcon', props: { name: { type: String, default: '' } }, template: '<i :data-icon="name" />' }
const UAlertStub = {
  name: 'UAlert',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div data-testid="alert" class="u-alert" :data-title="title"><span class="u-alert__title">{{ title }}</span><slot name="description">{{ description }}</slot><slot name="actions" /></div>'
}
const UButtonStub = { name: 'UButton', template: '<button type="button" v-bind="$attrs"><slot /></button>' }
const UBadgeStub = { name: 'UBadge', template: '<span class="u-badge"><slot /></span>' }
const UFormFieldStub = {
  name: 'UFormField',
  props: { label: { type: String, default: '' }, name: { type: String, default: '' } },
  template: '<label :data-name="name"><slot /></label>'
}
/** UInput 真实实现把 `disabled` / `model-value` 透传到原生 input，这里保持同一语义。 */
const UInputStub = {
  name: 'UInput',
  props: {
    modelValue: { type: [String, Number], default: '' },
    disabled: { type: Boolean, default: false }
  },
  emits: ['update:modelValue'],
  template: '<input :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)" />'
}
const USwitchStub = {
  name: 'USwitch',
  props: { modelValue: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
  emits: ['update:modelValue'],
  template: '<button type="button" role="switch" :aria-checked="String(modelValue)" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />'
}
const UCheckboxStub = {
  name: 'UCheckbox',
  props: { modelValue: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
  emits: ['update:modelValue'],
  template: '<button type="button" role="checkbox" :aria-checked="String(modelValue)" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />'
}

/**
 * `UModal` 桩：真实实现负责焦点陷阱与 Esc，这里只保留**本组件依赖的契约**——
 * `open` 为真时渲染默认插槽（dialog 用 `#content` 提供内容）、关掉时什么都不渲染。
 * 焦点回到触发按钮是 UModal 的职责，不在被测组件里，所以这里只断言 `aria-haspopup="dialog"`。
 */
const UModalStub = {
  name: 'UModal',
  props: { open: { type: Boolean, default: false } },
  emits: ['update:open'],
  template: '<div v-if="open" data-testid="umodal"><slot name="content" /></div>'
}
/**
 * `SFIconPicker` 桩（共享组件，lucide + tabler 全量目录有它自己的用例）：
 * 这里只验「选中值原样以全名发出去」以及「自定义输入逃生口必须是关的」
 * （它会放过 `i-fa-*`，而后端只认 lucide/tabler，放到弹窗里就是保存 422）。
 */
const LazySFIconPickerStub = {
  name: 'LazySFIconPicker',
  props: {
    modelValue: { type: String, default: '' },
    label: { type: String, default: '' },
    hint: { type: String, default: '' },
    showCustomInput: { type: Boolean, default: true },
    disabled: { type: Boolean, default: false }
  },
  emits: ['update:modelValue'],
  template: `<div data-testid="icon-picker" :data-custom-input="String(showCustomInput)" :data-value="modelValue">
    <button type="button" v-for="name in ['i-tabler-crown', 'i-lucide-leaf']" :key="name" :data-sf-stub-icon="name" @click="$emit('update:modelValue', name)">{{ name }}</button>
  </div>`
}

const globalComponents = {
  UIcon: UIconStub,
  UModal: UModalStub,
  LazySFIconPicker: LazySFIconPickerStub,
  UAlert: UAlertStub,
  UButton: UButtonStub,
  UBadge: UBadgeStub,
  UFormField: UFormFieldStub,
  UInput: UInputStub,
  UCheckbox: UCheckboxStub,
  USwitch: USwitchStub
}

const i18n = () => ({ t, te, locale: ref('zh-CN') })

const rowPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelEarnableRow.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableRow = await compileVueSfc(rowPath, 'sf-trust-earnable-row-for-tab', {
  computed,
  useI18n: i18n,
  usePermissionText: () => ({ permissionLabel }),
  trustLevelIconOf,
  trustLevelName,
  tierAccentStyle,
  tierColorOf
})

const matrixPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableMatrix = await compileVueSfc(matrixPath, 'sf-trust-earnable-matrix-for-tab', {
  computed,
  useI18n: i18n,
  usePermissionText: () => ({ permissionLabel }),
  activeRankOf,
  trustLevelName,
  earnableKeys,
  earnableMatrixState,
  earnableOrphanText,
  isBaselineKey,
  SFAdminTrustLevelEarnableRow
})

const fieldErrorsPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelFieldErrors.vue', import.meta.url)
)
const SFAdminTrustLevelFieldErrors = await compileVueSfc(fieldErrorsPath, 'sf-trust-field-errors', {
  useI18n: i18n,
  saveErrorFieldLabel,
  saveErrorText,
  TRUST_LEVEL_METRIC_SUFFIX_BY_KEY
})

const thresholdsPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue', import.meta.url)
)
/**
 * 规则面板（M3b）在本文件里只需要「存在且可点击」：它的内容契约由
 * `trustLevelRulePanel.test.ts` 单独覆盖，这里只挡住编译期绑定缺失。
 */
const SFAdminTrustLevelRuleSectionStub = defineComponent({
  name: 'SFAdminTrustLevelRuleSection',
  props: { tierId: String, rule: { type: Object, default: null } },
  emits: ['update:rule', 'retry'],
  template: '<div data-rule-section-stub :data-tier-id="tierId" />'
})

const SFAdminTrustLevelThresholdsTab = await compileVueSfc(thresholdsPath, 'sf-trust-levels-thresholds', {
  SFAdminTrustLevelFieldErrors,
  SFAdminTrustLevelRuleSection: SFAdminTrustLevelRuleSectionStub,
  ruleLeaves: (raw: unknown) => {
    // 与真实实现同形（足够本文件断言「条数」用）
    const out: unknown[] = []
    const walk = (node: any) => {
      if (!node || typeof node !== 'object') return
      if (Array.isArray(node)) { node.forEach(walk); return }
      if (typeof node.type === 'string') { out.push(node); return }
      if (Array.isArray(node.children)) { node.children.forEach(walk); return }
      if ('root' in node) walk(node.root)
    }
    walk(raw)
    return out
  },
  blockNonIntegerKey,
  TRUST_LEVEL_THRESHOLD_METRICS,
  activeRankOf,
  trustLevelIconOf,
  trustLevelName,
  tierAccentStyle,
  tierColorOf,
  useI18n: i18n
})

const earnableTabPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableTab.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableTab = await compileVueSfc(earnableTabPath, 'sf-trust-levels-earnable', {
  SFAdminTrustLevelEarnableMatrix,
  useI18n: i18n
})

const previewPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelAppearancePreview.vue', import.meta.url)
)
const SFAdminTrustLevelAppearancePreview = await compileVueSfc(previewPath, 'sf-trust-tier-appearance-preview', {
  computed,
  useI18n: i18n,
  tierAccentPreview
})

const colorFieldPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelColorField.vue', import.meta.url)
)
// 注意：ColorField 自己 `import { computed, onMounted, ref, useId, watch } from 'vue'`，
// compileVueSfc 会把它们改写成从注入的 Vue 解构 —— 这里再注入同名参数会触发
// 「Cannot declare a lexical variable twice」。只注入它从其它模块引的东西。
const SFAdminTrustLevelColorField = await compileVueSfc(colorFieldPath, 'sf-trust-tier-color-field', {
  useI18n: i18n,
  TIER_MIN_DARK_CONTRAST,
  isTierCustomColor,
  normalizeTierColor,
  tierAccentContrast,
  tierColorVar
})

const appearanceDialogPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelAppearanceDialog.vue', import.meta.url)
)
const SFAdminTrustLevelAppearanceDialog = await compileVueSfc(appearanceDialogPath, 'sf-trust-tier-appearance-dialog', {
  computed,
  useI18n: i18n,
  defaultTierIconName,
  tierIconClass,
  trustLevelName,
  defaultTierColorName,
  isTierCustomColor,
  normalizeTierColor,
  SFAdminTrustLevelAppearancePreview,
  SFAdminTrustLevelColorField
})

const actionButtonPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelActionButton.vue', import.meta.url)
)
const SFAdminTrustLevelActionButton = await compileVueSfc(actionButtonPath, 'sf-trust-action-button', {})

const tierRowPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelTierRow.vue', import.meta.url)
)
const SFAdminTrustLevelTierRow = await compileVueSfc(tierRowPath, 'sf-trust-tier-row', {
  computed,
  useI18n: i18n,
  trustLevelName,
  trustLevelIconOf,
  defaultTierIconName,
  defaultTierColorName,
  TRUST_LEVEL_TIER_NAME_MAX,
  SFAdminTrustLevelFieldErrors,
  SFAdminTrustLevelActionButton
})

const limitsTabPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelLimitsTab.vue', import.meta.url)
)
const SFAdminTrustLevelLimitsTab = await compileVueSfc(limitsTabPath, 'sf-trust-levels-limits', {
  computed,
  useI18n: i18n,
  activeRankOf,
  trustLevelName,
  trustLevelIconOf,
  limitsModeOf,
  parseLimitInput,
  toLimitsForm,
  TRUST_LEVEL_LIMIT_KEYS,
  blockNonIntegerKey,
  tierAccentStyle,
  tierColorOf,
  SFAdminTrustLevelFieldErrors
})

const tiersTabPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelTiersTab.vue', import.meta.url)
)
const SFAdminTrustLevelTiersTab = await compileVueSfc(tiersTabPath, 'sf-trust-levels-tiers', {
  computed,
  ref,
  useI18n: i18n,
  activeRankOf,
  trustLevelName,
  trustLevelIconOf,
  defaultTierIconName,
  DEFAULT_TIER_ICON_NAMES,
  TRUST_LEVEL_TIER_MAX,
  TRUST_LEVEL_TIER_NAME_MAX,
  retiredTierImpactKeys,
  defaultTierColorName,
  tierAccentStyle,
  tierColorOf,
  SFAdminTrustLevelAppearanceDialog,
  SFAdminTrustLevelFieldErrors,
  SFAdminTrustLevelActionButton,
  SFAdminTrustLevelTierRow
})

const WHITELIST = [
  'topic.create',
  'post.create',
  'attachment.upload',
  'topic.edit_own',
  'post.edit_own',
  'topic.delete_own',
  'post.delete_own'
]

const ready = (keys: string[] = WHITELIST, baselineKeys: string[] = []): TrustLevelWhitelistState =>
  ({ status: 'ready', keys, baselineKeys, colorChoices: TIER_COLOR_TOKENS.map(value => ({ value, label: value })), error: null })

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

function mountThresholds(options: {
  form?: TrustLevelsForm
  canManage?: boolean
  usingFallbackRecommended?: boolean
  saveErrors?: Record<string, TrustLevelSaveError[]>
} = {}) {
  return mount(SFAdminTrustLevelThresholdsTab, {
    props: {
      form: options.form ?? recommendedTrustLevelsForm(),
      canManage: options.canManage ?? true,
      usingFallbackRecommended: options.usingFallbackRecommended ?? false,
      saveErrors: options.saveErrors ?? {}
    },
    global: { components: globalComponents }
  })
}

function mountEarnable(options: {
  form?: TrustLevelsForm
  whitelist?: TrustLevelWhitelistState
  canManage?: boolean
  trustLevelsEnabled?: boolean
} = {}) {
  return mount(SFAdminTrustLevelEarnableTab, {
    props: {
      form: options.form ?? recommendedTrustLevelsForm(),
      whitelist: options.whitelist ?? ready(),
      canManage: options.canManage ?? true,
      trustLevelsEnabled: options.trustLevelsEnabled ?? true
    },
    global: { components: globalComponents }
  })
}

describe('thresholds partition rendering', () => {
  test('draws one row per tier × six named metric inputs, keyed by tier id', () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountThresholds({ form })
    expect(wrapper.findAll('fieldset')).toHaveLength(form.tiers.length)
    expect(wrapper.findAll('input')).toHaveLength(form.tiers.length * TRUST_LEVEL_THRESHOLD_METRICS.length)
    // 行身份 = 档位 id（插入/排序都不会让阈值落到别的档上）。
    expect(wrapper.findAll('fieldset').map(fieldset => fieldset.attributes('data-tier-id')))
      .toEqual(form.tiers.map(tier => tier.id))
    // 输入名与契约字段一一对应：名字错了，表单提交就落到别的键上。
    for (const tier of form.tiers) {
      const row = wrapper.find(`fieldset[data-tier-id="${tier.id}"]`)
      expect(row.exists()).toBe(true)
      const names = row.findAll('label').map(label => label.attributes('data-name'))
      expect(names).toEqual(TRUST_LEVEL_THRESHOLD_METRICS.map(metric => `trust.tier.${tier.id}.${metric.suffix}`))
      expect(row.find('legend').text()).toContain(`TL${activeRankOf(form.tiers, tier.id)}`)
    }
  })

  test('adds a row per extra tier, all editable, with no TL0 fieldset', () => {
    const form = formWithActive(7)
    const wrapper = mountThresholds({ form })
    expect(wrapper.findAll('fieldset')).toHaveLength(7)
    // TL0 由 trust.new_user_* 决定：不给控件，只在页尾说明。
    expect(wrapper.find('fieldset[data-tier-id="newcomer"]').exists()).toBe(false)
    expect(wrapper.findAll('input').filter(input => input.attributes('disabled') !== undefined)).toHaveLength(0)
    const hint = wrapper.find('[data-sf-trust-levels-tl0-hint]')
    expect(hint.text()).toBe(t('admin.forum.trustLevels.tl0Hint'))
    // 最后一档（rank 7）也有自己的输入，行数不是写死的 4。
    expect(wrapper.find('fieldset[data-tier-id="tier_3"] legend').text()).toContain('TL7')
  })

  test('keeps the TL0 explanation once, and no editable TL0 inputs at all', () => {
    const wrapper = mountThresholds()
    const hint = wrapper.find('[data-sf-trust-levels-tl0-hint]')
    expect(hint.text()).toContain(t('admin.forum.trustLevels.tl0Hint'))
    // TL0 既没有行也没有占位数字：0 会被读成「当前门槛」。
    expect(wrapper.findAll('fieldset')).toHaveLength(recommendedTrustLevelsForm().tiers.length)
    expect(wrapper.findAll('input')).toHaveLength(recommendedTrustLevelsForm().tiers.length * TRUST_LEVEL_THRESHOLD_METRICS.length)
  })

  test('shows the stored values of every tier', () => {
    const form = recommendedTrustLevelsForm()
    tierOf(form, 'member').requirements.days_visited = 7
    const wrapper = mountThresholds({ form })
    const memberInputs = wrapper.find('fieldset[data-tier-id="member"]').findAll('input')
    expect(memberInputs[0]!.attributes('value')).toBe('7')
  })

  test('emits a per-field threshold intent with the tier id instead of mutating the form', async () => {
    const wrapper = mountThresholds()
    const residentFirst = wrapper.find('fieldset[data-tier-id="resident"]').findAll('input')[0]!
    await residentFirst.setValue('12')
    expect(wrapper.emitted('threshold')?.[0]).toEqual(['resident', TRUST_LEVEL_THRESHOLD_METRICS[0]!.suffix, '12'])
  })

  test('keeps retired tiers visible and editable, marked with the retired badge', () => {
    const tiers = retireTier(recommendedTrustLevelsForm().tiers, 'leader')
    const wrapper = mountThresholds({ form: { tiers } })
    const retired = wrapper.find('fieldset[data-tier-id="leader"]')
    expect(retired.attributes('data-retired')).toBe('true')
    // 软停用保留全部配置：六个输入仍在，也仍可编辑（停用语义只体现在判定上）。
    expect(retired.findAll('input')).toHaveLength(TRUST_LEVEL_THRESHOLD_METRICS.length)
    expect(retired.find('legend').text()).toContain(t('admin.forum.trustLevels.tiers.retiredBadge'))
    expect(retired.find('legend').text()).not.toContain('TL4')
  })

  test('locks every input when the account cannot manage trust levels', () => {
    const wrapper = mountThresholds({ canManage: false })
    for (const input of wrapper.findAll('input')) {
      expect(input.attributes('disabled')).toBeDefined()
    }
    expect(wrapper.find('[data-sf-trust-levels-restore]').attributes('disabled')).toBeDefined()
  })

  test('keeps the D12 disclosure as a permanent alert and emits restore', async () => {
    const wrapper = mountThresholds()
    const hint = wrapper.find('[data-sf-trust-levels-restore-hint]')
    expect(hint.exists()).toBe(true)
    expect(hint.attributes('data-title')).toBe(t('admin.forum.trustLevels.restoreHint'))
    await wrapper.find('[data-sf-trust-levels-restore]').trigger('click')
    expect(wrapper.emitted('restore')).toHaveLength(1)
  })

  test('says so when the recommended values are the local fallback (D-N9)', () => {
    expect(mountThresholds().find('[data-sf-trust-levels-restore-fallback]').exists()).toBe(false)
    const fallback = mountThresholds({ usingFallbackRecommended: true })
    const alert = fallback.find('[data-sf-trust-levels-restore-fallback]')
    expect(alert.exists()).toBe(true)
    expect(alert.attributes('data-title')).toBe(t('admin.forum.trustLevels.restoreFallback.title'))
  })

  test('states the 0 = unlimited rule once, at the bottom of the grid', () => {
    const wrapper = mountThresholds()
    expect(wrapper.find('[data-sf-trust-levels-zero-unlimited]').text())
      .toBe(t('admin.forum.trustLevels.zeroUnlimited'))
  })
})

describe('earnable partition under the standalone page (J-07 / J-11)', () => {
  test('renders the matrix for a ready whitelist: one row per active tier × one control per key', () => {
    const form = recommendedTrustLevelsForm()
    const activeCount = activeTierCount(form.tiers)
    const wrapper = mountEarnable({ form })
    expect(wrapper.find('[data-sf-trust-levels-earnable]').exists()).toBe(true)
    expect(wrapper.find('[data-sf-trust-levels-earnable-feature-off]').exists()).toBe(false)
    expect(wrapper.findAll('fieldset')).toHaveLength(activeCount)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(activeCount * WHITELIST.length)
    expect(wrapper.findAll('fieldset').map(row => row.attributes('data-tier-id')))
      .toEqual(form.tiers.map(tier => tier.id))
  })

  test('locks the matrix on a failed read instead of drawing an empty one (T22)', () => {
    const wrapper = mountEarnable({
      whitelist: { status: 'failed', keys: [], baselineKeys: [], error: 'internal_error' }
    })
    const region = wrapper.find('[data-sf-earnable-region]')
    expect(region.exists()).toBe(true)
    expect(region.find('[data-sf-earnable-error]').exists()).toBe(true)
    expect(region.find('[data-sf-earnable-locked]').exists()).toBe(true)
    expect(region.find('.sf-trust-earnable__cells').exists()).toBe(false)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
    expect(region.text()).toContain('internal_error')
  })

  test('shows the loading placeholder without any checkbox', () => {
    const wrapper = mountEarnable({ whitelist: { status: 'loading', keys: [], baselineKeys: [], error: null } })
    expect(wrapper.find('[data-sf-earnable-loading]').exists()).toBe(true)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
  })

  test('feature off renders the placeholder and zero checkboxes, never the matrix', () => {
    const wrapper = mountEarnable({ trustLevelsEnabled: false })
    expect(wrapper.find('[data-sf-trust-levels-earnable-feature-off]').exists()).toBe(true)
    expect(wrapper.find('[data-sf-trust-levels-earnable-feature-off]').text())
      .toBe(t('admin.forum.trustLevels.earnable.featureOff'))
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
    expect(wrapper.find('[data-sf-earnable-region]').exists()).toBe(false)
    // 关闭 ≠ 失败：失败条一条都不许出现。
    expect(wrapper.find('[data-sf-earnable-error]').exists()).toBe(false)
  })

  test('bubbles a per-cell toggle intent with the tier id, and a retry intent, to the page', async () => {
    const wrapper = mountEarnable()
    await wrapper.find('fieldset[data-level="1"] [role="checkbox"]').trigger('click')
    expect(wrapper.emitted('toggle')?.[0]).toEqual(['member', 'topic.create'])

    const failed = mountEarnable({
      whitelist: { status: 'failed', keys: [], baselineKeys: [], error: 'forbidden' }
    })
    await failed.find('[data-sf-earnable-retry]').trigger('click')
    expect(failed.emitted('retry')).toHaveLength(1)
  })

  test('keeps the readonly fallback when the account cannot manage', () => {
    const wrapper = mountEarnable({ canManage: false })
    expect(wrapper.find('[data-sf-earnable-readonly]').exists()).toBe(true)
    for (const box of wrapper.findAll('[role="checkbox"]')) {
      expect(box.attributes('disabled')).toBeDefined()
    }
  })

  test('never offers restore inside the earnable partition', () => {
    // 推荐默认的 earnable 是 ∅：这里放「恢复默认」等于一键清空运营策略。
    expect(mountEarnable().find('[data-sf-trust-levels-restore]').exists()).toBe(false)
  })
})

describe('thresholds partition under the new tier ceiling', () => {
  test('renders 8 rows for 8 active tiers without collapsing to the old five', () => {
    const form = formWithActive(8)
    const wrapper = mountThresholds({ form })
    expect(wrapper.findAll('fieldset')).toHaveLength(8)
    expect(wrapper.findAll('input')).toHaveLength(8 * TRUST_LEVEL_THRESHOLD_METRICS.length)
    expect(wrapper.findAll('fieldset').map(fieldset => fieldset.attributes('data-tier-id')))
      .toEqual(form.tiers.map(tier => tier.id))
    // 每个 fieldset 都带六个输入，行内没有半格。
    for (const fieldset of wrapper.findAll('fieldset')) {
      expect(fieldset.findAll('input')).toHaveLength(TRUST_LEVEL_THRESHOLD_METRICS.length)
    }
  })
})

function mountTiers(options: {
  form?: TrustLevelsForm
  canManage?: boolean
  canAdd?: boolean
  canRetire?: boolean
  saveErrors?: Record<string, TrustLevelSaveError[]>
  colorChoices?: Array<{ value: string, label: string }>
  savedRetiredIds?: string[]
  deletedTierIds?: string[]
} = {}) {
  return mount(SFAdminTrustLevelTiersTab, {
    props: {
      tiers: (options.form ?? recommendedTrustLevelsForm()).tiers,
      canManage: options.canManage ?? true,
      canAdd: options.canAdd ?? true,
      canRetire: options.canRetire ?? true,
      namePool: [{ zh: '种子用户', en: 'Seed' }],
      colorChoices: options.colorChoices ?? TIER_COLOR_TOKENS.map(value => ({ value, label: value })),
      savedRetiredIds: options.savedRetiredIds ?? [],
      deletedTierIds: options.deletedTierIds ?? [],
      saveErrors: options.saveErrors ?? {}
    },
    global: { components: globalComponents }
  })
}

describe('tiers partition (档位管理)', () => {
  test('renders one row per tier, ranked from the active order', () => {
    const form = recommendedTrustLevelsForm()
    const rows = mountTiers({ form }).findAll('[data-sf-tier-row]')
    expect(rows).toHaveLength(form.tiers.length)
    expect(rows.map(row => row.attributes('data-rank'))).toEqual(['1', '2', '3', '4'])
  })

  test('a retired tier has no rank and offers restore', () => {
    const form = recommendedTrustLevelsForm()
    const retired = retireTier(form.tiers, form.tiers[1]!.id)
    const wrapper = mountTiers({ form: { tiers: retired } })
    const row = wrapper.find(`[data-sf-tier-row="${form.tiers[1]!.id}"]`)
    expect(row.attributes('data-rank')).toBe('')
    expect(row.text()).toContain(t('admin.forum.trustLevels.tiers.retiredBadge'))
    expect(row.find(`[data-sf-tier-restore="${form.tiers[1]!.id}"]`).exists()).toBe(true)
  })

  test('emits add / insert / move / retire intents keyed by tier id', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    await wrapper.find('[data-sf-tier-add]').trigger('click')
    expect(wrapper.emitted('add')).toHaveLength(1)

    await wrapper.find(`[data-sf-tier-insert="${form.tiers[1]!.id}"]`).trigger('click')
    expect(wrapper.emitted('insert')?.[0]).toEqual([2])

    await wrapper.find(`[data-sf-tier-move-down="${form.tiers[0]!.id}"]`).trigger('click')
    expect(wrapper.emitted('move')?.[0]).toEqual([form.tiers[0]!.id, 1])

    await wrapper.find(`[data-sf-tier-retire="${form.tiers[2]!.id}"]`).trigger('click')
    expect(wrapper.emitted('retire')?.[0]).toEqual([form.tiers[2]!.id])
  })

  test('move-up emits (id, -1), and both list ends are disabled at render level', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const firstId = form.tiers[0]!.id
    const lastId = form.tiers[form.tiers.length - 1]!.id
    // 首行不能上移、末行不能下移：断言的是原生 disabled（与 B2 同一条教训：谓词对不等于渲染对）。
    expect(wrapper.find(`[data-sf-tier-move-up="${firstId}"]`).attributes('disabled')).toBeDefined()
    expect(wrapper.find(`[data-sf-tier-move-down="${lastId}"]`).attributes('disabled')).toBeDefined()
    // 同一行的另一方向仍然可用（不是把整行禁掉）。
    expect(wrapper.find(`[data-sf-tier-move-down="${firstId}"]`).attributes('disabled')).toBeUndefined()
    expect(wrapper.find(`[data-sf-tier-move-up="${lastId}"]`).attributes('disabled')).toBeUndefined()

    // 上移发的是 (id, -1) —— 这个方向此前没有任何消费者断言。
    await wrapper.find(`[data-sf-tier-move-up="${form.tiers[2]!.id}"]`).trigger('click')
    expect(wrapper.emitted('move')?.[0]).toEqual([form.tiers[2]!.id, -1])
  })

  test('renaming emits per-language edits and the name pool fills both', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[0]!.id
    await wrapper.find(`[data-sf-tier-name-zh="${id}"]`).setValue('种子')
    expect(wrapper.emitted('rename')?.[0]).toEqual([id, '种子', 'zh'])

    await wrapper.find('[data-sf-tier-name-suggestion="Seed"]').trigger('click')
    const renames = wrapper.emitted('rename') ?? []
    expect(renames).toContainEqual([id, '种子用户', 'zh'])
    expect(renames).toContainEqual([id, 'Seed', 'en'])
  })

  test('the last-tier retire control renders a native disabled attribute (render-level, not predicate)', () => {
    // 只看最终渲染字符串：不依赖谓词、不依赖组件实例链 —— B2 的漏检就是被这两层夹在中间的。
    const single = { tiers: [recommendedTrustLevelsForm().tiers[0]!] }
    const wrapper = mountTiers({ form: single })
    const retire = wrapper.find(`[data-sf-tier-retire="${single.tiers[0]!.id}"]`)
    expect(retire.element.tagName).toBe('BUTTON')
    expect(retire.attributes('disabled')).toBeDefined()
    expect(retire.attributes('aria-disabled')).toBe('true')
    expect(retire.html()).toContain('disabled')

    // 可用时不写 aria-disabled（"false" 是显式宣告「可用」，视觉不一致时反而误导 AT）。
    const many = mountTiers()
    const enabled = many.find(`[data-sf-tier-retire="${recommendedTrustLevelsForm().tiers[2]!.id}"]`)
    expect(enabled.attributes('disabled')).toBeUndefined()
    expect(enabled.attributes('aria-disabled')).toBeUndefined()
  })

  test('the last active tier explains why retire is unavailable instead of failing silently', () => {
    const single = { tiers: [recommendedTrustLevelsForm().tiers[0]!] }
    const one = mountTiers({ form: single })
    const retire = one.find(`[data-sf-tier-retire="${single.tiers[0]!.id}"]`)
    expect(retire.attributes('disabled')).toBeDefined()
    expect(retire.attributes('aria-disabled')).toBe('true')
    expect(retire.attributes('title')).toBe(t('admin.forum.trustLevels.tiers.lastTierLocked'))
    // 理由要在正文里看得见（不是只挂在 title 上）。
    const reason = one.find('[data-sf-tier-last-locked]')
    expect(reason.exists()).toBe(true)
    expect(reason.text()).toBe(t('admin.forum.trustLevels.tiers.lastTierLocked'))

    // 多档时按钮可用，且这段理由不出现。
    const many = mountTiers()
    expect(many.find(`[data-sf-tier-retire="${recommendedTrustLevelsForm().tiers[2]!.id}"]`).attributes('disabled')).toBeUndefined()
    expect(many.find('[data-sf-tier-last-locked]').exists()).toBe(false)
  })

  test('disables add at the tier cap and retire on the last active tier', () => {
    const capped = formWithActive(TRUST_LEVEL_TIER_MAX)
    expect(mountTiers({ form: capped, canAdd: false }).find('[data-sf-tier-add]').attributes('disabled')).toBeDefined()

    const single = { tiers: [recommendedTrustLevelsForm().tiers[0]!] }
    const last = mountTiers({ form: single, canRetire: false })
    expect(last.find(`[data-sf-tier-retire="${single.tiers[0]!.id}"]`).attributes('disabled')).toBeDefined()
  })

  test('discloses the N = 1 consequence, permanently and only when it applies', () => {
    const single = { tiers: [recommendedTrustLevelsForm().tiers[0]!] }
    const one = mountTiers({ form: single })
    const disclosure = one.find('[data-sf-tier-single-disclosure]')
    expect(disclosure.exists()).toBe(true)
    expect(disclosure.attributes('data-title')).toBe(t('admin.forum.trustLevels.tiers.singleTier.title'))
    expect(disclosure.text()).toContain(t('admin.forum.trustLevels.tiers.singleTier.description'))
    expect(mountTiers().find('[data-sf-tier-single-disclosure]').exists()).toBe(false)
  })

  test('the appearance toggle opens exactly one dialog instead of an inline panel', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[0]!.id
    const toggle = wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`)

    // 行内面板已删除：开关只发意图，编辑在 tab 层唯一的 dialog 里（每行一个实例 = N 套焦点目标）。
    expect(wrapper.find('[data-sf-tier-appearance]').exists()).toBe(false)
    expect(toggle.attributes('aria-haspopup')).toBe('dialog')
    // `aria-expanded` 是折叠区的语义，弹窗没有它 —— 留着会让读屏 announce 一个不存在的展开态。
    expect(toggle.attributes('aria-expanded')).toBeUndefined()

    await toggle.trigger('click')
    const dialogs = wrapper.findAll('[data-sf-tl-appearance-dialog]')
    expect(dialogs).toHaveLength(1)
    expect(dialogs[0]!.attributes('role')).toBe('dialog')
    expect(dialogs[0]!.attributes('aria-modal')).toBe('true')
    // aria-labelledby 必须指向真实存在的标题节点，否则等于没名字。
    const labelledBy = dialogs[0]!.attributes('aria-labelledby')!
    expect(wrapper.find(`[id="${labelledBy}"]`).exists()).toBe(true)

    // 换一行只换编辑目标，不叠第二个 dialog。
    await wrapper.find(`[data-sf-tier-appearance-toggle="${form.tiers[1]!.id}"]`).trigger('click')
    expect(wrapper.findAll('[data-sf-tl-appearance-dialog]')).toHaveLength(1)
  })

  test('the dialog previews both page modes, lists the server palette and offers a custom hex', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    await wrapper.find(`[data-sf-tier-appearance-toggle="${form.tiers[0]!.id}"]`).trigger('click')

    // 双底预览：两张卡各带自己的 scope，组件里没有「当前是否暗色」的判断。
    const cards = wrapper.findAll('[data-sf-tl-preview-card]')
    expect(cards.map(card => card.attributes('data-sf-tl-scope'))).toEqual(['light', 'dark'])

    // 9 个推荐色逐项来自后端 colorChoices，不多不少（前端不自己抄一份色表）。
    const swatches = wrapper.findAll('[data-sf-color-choice]')
    expect(swatches.map(input => input.attributes('data-sf-color-choice'))).toEqual([...TIER_COLOR_TOKENS])
    expect(wrapper.find('[data-sf-color-picker-grid]').exists()).toBe(true)
    // 「按位次自动」是独立 chip，不是第 10 个色块（10 块在 390 上会折成 9+1，孤块会被读成另一种颜色）。
    expect(wrapper.find('[data-sf-color-picker-auto]').exists()).toBe(true)

    // 自定义入口：原生取色器 + hex 文本框 + 应用。
    expect(wrapper.find('[data-sf-color-custom-picker]').exists()).toBe(true)
    expect(wrapper.find('[data-sf-color-hex-input]').exists()).toBe(true)
    // 空草稿时「应用」不可用：不许把空值写进表单。
    expect(wrapper.find('[data-sf-color-apply]').attributes('disabled')).toBeDefined()
  })

  test('a recommended color, the auto chip, and a custom hex each emit one intent', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[2]!.id
    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')

    await wrapper.find('[data-sf-color-choice="rose"]').trigger('change')
    expect(wrapper.emitted('setColor')?.[0]).toEqual([id, 'rose'])

    // 「按位次自动」= 明确的 null（选完能退回自动，而不是回不去）。
    await wrapper.find('[data-sf-color-picker-auto]').trigger('change')
    expect(wrapper.emitted('setColor')?.[1]).toEqual([id, null])

    // 自定义色：大写输入、小写落表单（后端 hex 一律小写存储）。
    const hexInput = wrapper.find('[data-sf-color-hex-input]')
    await hexInput.setValue('#7C3AED')
    expect(wrapper.find('[data-sf-color-invalid]').exists()).toBe(false)
    await wrapper.find('[data-sf-color-apply]').trigger('click')
    expect(wrapper.emitted('setColor')?.[2]).toEqual([id, '#7c3aed'])
  })

  test('an invalid hex is refused in place and never reaches the form', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[0]!.id
    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')

    await wrapper.find('[data-sf-color-hex-input]').setValue('#12')
    // 字段级错误就地显示（Toast 不适合「还没提交的输入错误」），且应用按钮不可用。
    expect(wrapper.find('[data-sf-color-invalid]').text()).toBe(t('admin.forum.trustLevels.appearanceDialog.colorInvalidHex'))
    expect(wrapper.find('[data-sf-color-apply]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-sf-color-apply]').trigger('click')
    expect(wrapper.emitted('setColor')).toBeUndefined()

    // 对比度提示只是提示：极深的品牌色在深色页面上会糊，仍然允许应用。
    await wrapper.find('[data-sf-color-hex-input]').setValue('#111827')
    expect(wrapper.find('[data-sf-color-low-contrast]').exists()).toBe(true)
    await wrapper.find('[data-sf-color-apply]').trigger('click')
    expect(wrapper.emitted('setColor')?.[0]).toEqual([id, '#111827'])
  })

  test('the icon picker emits the full host name and keeps the custom-input escape hatch closed', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[1]!.id
    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')

    // 自定义输入会放过 `i-fa-*`，而后端只认 lucide/tabler —— 放开就是保存 422。
    const picker = wrapper.find('[data-testid="icon-picker"]')
    expect(picker.attributes('data-custom-input')).toBe('false')
    // 选中的是 `i-collection-name` 全名：存短名会在渲染层被补成 i-lucide-*，tabler 图标就丢了。
    await wrapper.find('[data-sf-stub-icon="i-tabler-crown"]').trigger('click')
    expect(wrapper.emitted('setIcon')?.[0]).toEqual([id, 'i-tabler-crown'])
  })

  test('done only closes the dialog; reset sends both appearance intents back to automatic', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form })
    const id = form.tiers[0]!.id
    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')

    await wrapper.find('[data-sf-appearance-done]').trigger('click')
    // 「完成」不保存、不写表单：外观改动早就进了表单，保存仍归页面级 footer。
    expect(wrapper.emitted('setColor')).toBeUndefined()
    expect(wrapper.emitted('setIcon')).toBeUndefined()
    expect(wrapper.find('[data-sf-tl-appearance-dialog]').exists()).toBe(false)

    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')
    await wrapper.find('[data-sf-appearance-reset]').trigger('click')
    expect(wrapper.emitted('setIcon')?.[0]).toEqual([id, null])
    expect(wrapper.emitted('setColor')?.[0]).toEqual([id, null])
  })

  test('an empty color palette explains itself while the custom hex stays usable', async () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({ form, colorChoices: [] })
    const id = form.tiers[0]!.id
    await wrapper.find(`[data-sf-tier-appearance-toggle="${id}"]`).trigger('click')

    // 后端没下发推荐色 → 只说明「没有候选」，不编造色块，也不画空 radiogroup。
    expect(wrapper.find('[data-sf-color-picker-empty]').exists()).toBe(true)
    expect(wrapper.find('[data-sf-color-picker-grid]').exists()).toBe(false)
    expect(wrapper.findAll('[data-sf-color-choice]')).toHaveLength(0)
    // 但自定义色仍然可用（它不是候选表，是用户输入值）。
    expect(wrapper.find('[data-sf-color-hex-input]').exists()).toBe(true)
    await wrapper.find('[data-sf-color-hex-input]').setValue('#0f766e')
    await wrapper.find('[data-sf-color-apply]').trigger('click')
    expect(wrapper.emitted('setColor')?.[0]).toEqual([id, '#0f766e'])
  })

  test('renders the effective tier color as --tl-accent, and the default sequence by rank', () => {
    const form = recommendedTrustLevelsForm()
    const rows = mountTiers({ form }).findAll('[data-sf-tier-row]')
    // 未配色 → 按 active 位次取默认色（TL1 = teal …）。
    const styles = rows.map(row => (row.attributes('style') ?? '').replace(/\s+/g, ''))
    expect(styles).toEqual([1, 2, 3, 4].map(rank => `--tl-accent:var(--sf-tl-${defaultTierColorName(rank)});`))
    // 自定义 hex 档靠全局规则把 --tl-accent-light/-dark 落成 --tl-accent：属性必须与 style 同元素。
    expect(rows.every(row => row.attributes('data-sf-tl-accent') !== undefined)).toBe(true)

    // 单档覆盖色优先，且只动那一档。
    const colored = setTierColor(form.tiers, form.tiers[0]!.id, 'rose')
    const overridden = mountTiers({ form: { tiers: colored } }).findAll('[data-sf-tier-row]')
    expect(overridden[0]!.attributes('style')).toContain('var(--sf-tl-rose)')
    expect(overridden[1]!.attributes('style')).toContain(`var(--sf-tl-${defaultTierColorName(2)})`)
    expect(tierAccentStyle(tierColorOf(colored[0], 1))).toEqual({ '--tl-accent': 'var(--sf-tl-rose)' })
  })

  test('thresholds / limits / earnable 的行根与 tiers 行根一样带同一份 --tl-accent', () => {
    const form = recommendedTrustLevelsForm()
    const expected = [1, 2, 3, 4].map(rank => `--tl-accent: var(--sf-tl-${defaultTierColorName(rank)});`)

    const thresholds = mountThresholds({ form }).findAll('fieldset')
    expect(thresholds.map(row => row.attributes('style'))).toEqual(expected)
    expect(thresholds.every(row => row.find('.sf-tl-mark').exists())).toBe(true)
    expect(thresholds.every(row => row.attributes('data-sf-tl-accent') !== undefined)).toBe(true)

    const limits = mountLimits({ form }).findAll('[data-sf-limits-tier]')
    expect(limits.map(row => row.attributes('style'))).toEqual(expected)
    expect(limits.every(row => row.find('.sf-tl-mark').exists())).toBe(true)
    expect(limits.every(row => row.attributes('data-sf-tl-accent') !== undefined)).toBe(true)

    const earnable = mountEarnable({ form }).findAll('.sf-trust-earnable__row')
    expect(earnable.length).toBe(form.tiers.length)
    expect(earnable.map(row => row.attributes('style'))).toEqual(expected)
    expect(earnable.every(row => row.find('.sf-tl-mark').exists())).toBe(true)
    expect(earnable.every(row => row.attributes('data-sf-tl-accent') !== undefined)).toBe(true)
  })

  test('a retired tier carries a permanent withdrawn-unlocks badge instead of a toast', () => {
    let form = recommendedTrustLevelsForm()
    const target = form.tiers[1]!.id
    // 该档独占的解锁：停用后会被撤回。
    form = { tiers: form.tiers.map(tier => (tier.id === target ? { ...tier, earnable: ['topic.create'] } : tier)) }
    form = { tiers: retireTier(form.tiers, target) }
    const badge = mountTiers({ form }).find(`[data-sf-tier-row="${target}"]`).find('[data-sf-tier-retired-impact]')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toBe(t('admin.forum.trustLevels.tiers.retiredImpact', { count: 1 }))

    // 其它 active 档也给了同一个键 → 不算撤回，不渲染徽标。
    const shared = {
      tiers: retireTier(
        form.tiers.map(tier => (tier.id === form.tiers[0]!.id ? { ...tier, earnable: ['topic.create'] } : tier)),
        target
      )
    }
    expect(mountTiers({ form: shared }).find(`[data-sf-tier-row="${target}"]`).find('[data-sf-tier-retired-impact]').exists())
      .toBe(false)
  })

  test('delete is offered only for retired tiers whose retirement is already saved', async () => {
    const form = recommendedTrustLevelsForm()
    const active = form.tiers[0]!.id
    expect(mountTiers({ form }).find(`[data-sf-tier-delete="${active}"]`).exists()).toBe(false)

    const target = form.tiers[1]!.id
    const retired = { tiers: retireTier(form.tiers, target) }

    // 已停用但**未保存** → 原生 disabled（渲染层，不是谓词），且 title 说明原因。
    const locked = mountTiers({ form: retired }).find(`[data-sf-tier-delete="${target}"]`)
    expect(locked.attributes('disabled')).toBeDefined()
    expect(locked.attributes('aria-disabled')).toBe('true')
    expect(locked.attributes('title')).toBe(t('admin.forum.trustLevels.tiers.deleteLocked'))

    // 已保存为停用 → 可点，且只发意图。
    const unlocked = mountTiers({ form: retired, savedRetiredIds: [target] })
    const button = unlocked.find(`[data-sf-tier-delete="${target}"]`)
    expect(button.attributes('disabled')).toBeUndefined()
    await button.trigger('click')
    expect(unlocked.emitted('delete')?.[0]).toEqual([target])
  })

  test('renders the deletion tombstones only when ids are actually tombstoned', () => {
    expect(mountTiers().find('[data-sf-tier-tombstones]').exists()).toBe(false)
    const tombstones = mountTiers({ deletedTierIds: ['tier_7', 'tier_9'] }).find('[data-sf-tier-tombstones]')
    expect(tombstones.exists()).toBe(true)
    expect(tombstones.text()).toBe(t('admin.forum.trustLevels.tiers.tombstones', { ids: 'tier_7 · tier_9' }))
  })
})

describe('field-level save errors (P2)', () => {
  const error = (overrides: Partial<TrustLevelSaveError>): TrustLevelSaveError => ({
    rank: 0,
    tierId: '',
    field: '',
    reasonCode: '',
    reason: '',
    params: {},
    ...overrides
  })

  test('renders nothing when details are absent — the unified code stays in the toast', () => {
    expect(mountThresholds({ saveErrors: {} }).findAll('[data-sf-tier-errors]')).toHaveLength(0)
    expect(mountTiers({ saveErrors: {} }).findAll('[data-sf-tier-errors]')).toHaveLength(0)
  })

  test('a rank-keyed error lands on that tier row, not on row one', () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountTiers({
      form,
      saveErrors: { [form.tiers[2]!.id]: [error({ rank: 3, field: 'icon', reason: '图标不在白名单内' })] }
    })
    const rows = wrapper.findAll('[data-sf-tier-row]')
    expect(rows[0]!.find('[data-sf-tier-errors]').exists()).toBe(false)
    expect(rows[1]!.find('[data-sf-tier-errors]').exists()).toBe(false)
    const third = rows[2]!.find('[data-sf-tier-errors]')
    expect(third.text()).toContain(t('admin.forum.trustLevels.errors.field.icon'))
    expect(third.text()).toContain('图标不在白名单内')
  })

  test('a retired tier is addressed by id, never treated as TL0', () => {
    const form = recommendedTrustLevelsForm()
    const retired = retireTier(form.tiers, form.tiers[1]!.id)
    const buckets = bucketSaveErrors([error({ rank: 0, tierId: form.tiers[1]!.id, field: 'name.en' })], { tiers: retired })
    expect(Object.keys(buckets.byTierId)).toEqual([form.tiers[1]!.id])
    expect(buckets.pageLevel).toHaveLength(0)

    const row = mountTiers({ form: { tiers: retired }, saveErrors: buckets.byTierId })
      .find(`[data-sf-tier-row="${form.tiers[1]!.id}"]`)
    expect(row.find('[data-sf-tier-errors]').text())
      .toContain(t('admin.forum.trustLevels.errors.field.name_en'))
  })

  test('unlocatable errors fall to the page-level bucket instead of a random row', () => {
    const form = recommendedTrustLevelsForm()
    const buckets = bucketSaveErrors([
      error({ rank: 0, tierId: '', field: 'schemaVersion' }),
      error({ rank: 0, tierId: 'gone', field: '' })
    ], form)
    expect(buckets.pageLevel).toHaveLength(2)
    expect(buckets.byTierId).toEqual({})
  })

  test('a real 422 payload (36-char Chinese name) lands inline on that tier row', () => {
    // 后端 name_too_long 的 params 是 locale/maxLength；reason 是中文散文，只在 code 未登记时兜底。
    const form = recommendedTrustLevelsForm()
    const target = form.tiers[2]!
    const details = {
      errors: [{
        rank: 3,
        tierId: target.id,
        field: 'name.zh',
        reasonCode: 'name_too_long',
        reason: '中文名称超过 32 个字符上限',
        params: { locale: 'zh', maxLength: 32 }
      }]
    }
    const parsed = parseSaveErrors(details)
    const buckets = bucketSaveErrors(parsed, form)
    expect(buckets.pageLevel).toHaveLength(0)

    const wrapper = mountTiers({ form, saveErrors: buckets.byTierId })
    const rows = wrapper.findAll('[data-sf-tier-row]')
    const inline = rows[2]!.find('[data-sf-tier-errors]')
    expect(inline.exists()).toBe(true)
    expect(inline.text()).toContain(t('admin.forum.trustLevels.errors.field.name_zh'))
    // 专属码 → i18n 文案（插值），不是后端中文散文。
    expect(inline.text()).toContain(t('admin.forum.trustLevels.errors.code.name_too_long', { locale: '中文', maxLength: 32 }))
    expect(rows[0]!.find('[data-sf-tier-errors]').exists()).toBe(false)
    expect(rows[3]!.find('[data-sf-tier-errors]').exists()).toBe(false)
  })

  test('unattributable details stay out of the rows and go to the page-level bucket', () => {
    const form = recommendedTrustLevelsForm()
    const parsed = parseSaveErrors({
      errors: [
        { rank: 0, tierId: '', field: 'schemaVersion', reasonCode: 'schema_version_unsupported', reason: '版本不支持', params: {} },
        { rank: 0, tierId: '', field: 'tiers', reasonCode: 'schema_shape_invalid', reason: '形状不对', params: {} }
      ]
    })
    const buckets = bucketSaveErrors(parsed, form)
    expect(buckets.pageLevel).toHaveLength(2)
    expect(buckets.byTierId).toEqual({})
    expect(mountThresholds({ form, saveErrors: buckets.byTierId }).findAll('[data-sf-tier-errors]')).toHaveLength(0)
  })

  test('reasonCode wins over the backend prose and interpolates params', () => {
    const known = (key: string) => key === 'admin.forum.trustLevels.errors.code.name_too_long'
    const translate = (key: string, params: Record<string, string | number> = {}) =>
      (known(key) ? `名称超过 ${params.maxLength} 个字符` : `[${key}]`)
    // 真实枚举：name_too_long 的 params 是 locale / maxLength。
    const withCode = { ...error({ reasonCode: 'name_too_long', reason: '名称过长', params: { locale: 'zh', maxLength: 32 } }) }
    expect(saveErrorText(withCode, translate, known)).toBe('名称超过 32 个字符')
    // 配置体坏了的桶：主句 + 技术细节，reason 不进主句。
    const bucket = { ...withCode, reasonCode: 'json_syntax_invalid', reason: 'unexpected end of JSON input' }
    expect(saveErrorText(bucket, (key: string) => (key.endsWith('config.main') ? '格式不被接受' : `技术上：${'{reason}'}`), () => true))
      .toContain('格式不被接受')
    // 前端还没跟上的新 code：回落后端 reason，不显示 [key]。
    expect(saveErrorText({ ...withCode, reasonCode: 'brand_new_code' }, translate, known)).toBe('名称过长')
  })
})

function mountLimits(options: {
  form?: TrustLevelsForm
  canManage?: boolean
  saveErrors?: Record<string, TrustLevelSaveError[]>
} = {}) {
  return mount(SFAdminTrustLevelLimitsTab, {
    props: {
      form: options.form ?? recommendedTrustLevelsForm(),
      canManage: options.canManage ?? true,
      saveErrors: options.saveErrors ?? {}
    },
    global: { components: globalComponents }
  })
}

describe('limits partition (发言限额)', () => {
  test('one fieldset per tier, each with exactly the four limit inputs', () => {
    const form = recommendedTrustLevelsForm()
    const wrapper = mountLimits({ form })
    const rows = wrapper.findAll('[data-sf-limits-tier]')
    expect(rows).toHaveLength(form.tiers.length)
    expect(rows.map(row => row.attributes('data-tier-id'))).toEqual(form.tiers.map(tier => tier.id))
    for (const [index, row] of rows.entries()) {
      const inputs = row.findAll('[data-sf-limit-input]')
      expect(inputs).toHaveLength(4)
      expect(inputs.map(input => input.attributes('data-sf-limit-input')))
        .toEqual(TRUST_LEVEL_LIMIT_KEYS.map(key => `${form.tiers[index]!.id}.${key}`))
    }
  })

  test('inherit mode disables all four inputs and says the tier follows the site defaults', () => {
    const form = recommendedTrustLevelsForm()
    expect(limitsModeOf(form.tiers[0]!.limits)).toBe('inherit')
    const wrapper = mountLimits({ form })
    const row = wrapper.find(`[data-sf-limits-tier="${form.tiers[0]!.id}"]`)
    expect(row.attributes('data-limits-mode')).toBe('inherit')
    for (const input of row.findAll('input')) {
      expect(input.attributes('disabled')).toBeDefined()
    }
    expect(row.find('[data-sf-limits-inherit-note]').text()).toBe(t('admin.forum.trustLevels.limits.inheritNote'))
    expect(row.find('[data-sf-limits-mode-state]').text()).toBe(t('admin.forum.trustLevels.limits.inheritBadge'))
    // 开启状态下开关按的是「继承」语义。
    expect(row.find('[data-sf-limits-mode]').attributes('aria-checked')).toBe('true')
  })

  test('turning the switch off emits the custom intent; 0 is emitted as 0, not as inherit', async () => {
    const form = recommendedTrustLevelsForm()
    const id = form.tiers[0]!.id
    const wrapper = mountLimits({ form })
    await wrapper.find(`[data-sf-limits-mode="${id}"]`).trigger('click')
    expect(wrapper.emitted('mode')?.[0]).toEqual([id, 'custom'])

    // 继承态下输入是禁用的（parent 还没切模式，props 未变），所以换成 custom 形态再验 0 的透传：
    const customForm = { tiers: setLimitsMode(form.tiers, id, 'custom') }
    const editable = mountLimits({ form: customForm })
    const input = editable.find(`[data-sf-limit-input="${id}.dailyTopicLimit"]`)
    expect(input.attributes('disabled')).toBeUndefined()
    await input.setValue('0')
    // 传出去的是字符串 "0"（纯函数 `parseLimitInput` 才会把它与空串区分开）。
    expect(editable.emitted('limit')?.[0]).toEqual([id, 'dailyTopicLimit', '0'])
  })

  test('a custom tier enables the inputs and shows the zero hint instead of the inherit note', () => {
    const form = recommendedTrustLevelsForm()
    const id = form.tiers[1]!.id
    const custom = setLimitValue(setLimitsMode(form.tiers, id, 'custom'), id, 'dailyTopicLimit', 0)
    const wrapper = mountLimits({ form: { tiers: custom } })
    const row = wrapper.find(`[data-sf-limits-tier="${id}"]`)
    expect(row.attributes('data-limits-mode')).toBe('custom')
    expect(row.find('[data-sf-limits-inherit-note]').exists()).toBe(false)
    expect(row.find('[data-sf-limits-zero-hint]').text()).toBe(t('admin.forum.trustLevels.limits.zeroHint'))
    const inputs = row.findAll('input')
    for (const input of inputs) expect(input.attributes('disabled')).toBeUndefined()
    // 0 显示成 "0"，没配的字段显示为空串（= 继承），两者在 UI 上可区分。
    expect(row.find(`[data-sf-limit-input="${id}.dailyTopicLimit"]`).element.value).toBe('0')
    expect(row.find(`[data-sf-limit-input="${id}.dailyCommentLimit"]`).element.value).toBe('')
  })

  test('a retired tier stays editable (soft retire keeps the configuration)', () => {
    const form = recommendedTrustLevelsForm()
    const id = form.tiers[1]!.id
    const retired = retireTier(form.tiers, id)
    const custom = setLimitsMode(retired, id, 'custom')
    const row = mountLimits({ form: { tiers: custom } }).find(`[data-sf-limits-tier="${id}"]`)
    expect(row.attributes('data-retired')).toBe('true')
    expect(row.find('[data-sf-limits-mode]').attributes('disabled')).toBeUndefined()
    for (const input of row.findAll('input')) expect(input.attributes('disabled')).toBeUndefined()
  })

  test('pasting an invalid value bounces back to the stored one instead of being silently kept', async () => {
    const form = recommendedTrustLevelsForm()
    const id = form.tiers[0]!.id
    const custom = setLimitValue(setLimitsMode(form.tiers, id, 'custom'), id, 'dailyTopicLimit', 7)
    const wrapper = mountLimits({ form: { tiers: custom } })
    const input = wrapper.find(`[data-sf-limit-input="${id}.dailyTopicLimit"]`)
    const element = input.element as HTMLInputElement

    // 模拟粘贴：`blockNonIntegerKey` 拦不到（不是 keydown），值先进 DOM，再触发 change。
    await input.setValue('1.5')
    await input.trigger('change')
    // 纯函数 fail-closed 后 prop 没变，组件必须显式回弹，否则运营以为存进去了。
    expect(element.value).toBe('7')

    // 超输入护栏同样回弹。
    await input.setValue('999999999')
    await input.trigger('blur')
    expect(element.value).toBe('7')

    // 合法值（含 0 与空串）照常提交。
    await input.setValue('0')
    await input.trigger('change')
    const emitted = wrapper.emitted('limit') ?? []
    expect(emitted[emitted.length - 1]).toEqual([id, 'dailyTopicLimit', '0'])
  })

  test('a read-only viewer gets a disabled switch and disabled inputs', () => {
    const wrapper = mountLimits({ canManage: false })
    expect(wrapper.find('[data-sf-limits-mode]').attributes('disabled')).toBeDefined()
    for (const input of wrapper.findAll('input')) expect(input.attributes('disabled')).toBeDefined()
  })
})
