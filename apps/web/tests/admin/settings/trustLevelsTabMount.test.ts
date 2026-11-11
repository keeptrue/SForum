import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { computed, onMounted, ref, watch } from 'vue'
import {
  adminOptionMap,
  useAdminOptionTab
} from '~/composables/admin/settings/useAdminOptionTab'
import { earnableMatrixState, earnableOrphanText, isBaselineKey } from '~/composables/admin/settings/useTrustLevelEarnableMatrix'
import {
  blockNonIntegerKey,
  cloneTrustLevelsForm,
  earnableKeys,
  earnableWhitelistForSave,
  normalizeTrustLevelsForm,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  toggleEarnableKey,
  TRUST_LEVEL_EDITABLE_LEVELS,
  TRUST_LEVEL_ROWS,
  TRUST_LEVEL_THRESHOLD_METRICS,
  trustLevelsPayload,
  useTrustLevelEarnableWhitelist
} from '~/composables/admin/settings/useTrustLevelsThresholds'
import { trustLevelIcon, trustLevelSlug } from '~/composables/profile/useTrustLevel'
import { normalizeEnabledOption } from '~/composables/useWebOptions'
import { compileVueSfc, flushPromises, mount } from '../../helpers/vueSfc'

/**
 * 信任等级 tab 的**挂载**行为（J-07 / J-11）。
 *
 * 为什么要有这个文件：`trustLevels.test.ts` 里对 `if (!trustLevelsEnabled.value) return`
 * 的断言只是静态契约——它证明「写了这行」，证明不了「关掉开关时真的一次请求都没发」。
 * AGENTS.md 明确禁止用源码字符串匹配代替渲染行为，所以这里挂真组件、注入真状态机，
 * 只把传输层换成计数器。
 *
 * 两个场景：
 * 1. 白名单 500 → 矩阵区内部出错误条 + 锁定占位 + **一个可交互勾选框都没有**（T22：空矩阵 = 谎）；
 * 2. 功能开关关闭 → 只有一条 alert，且 `request` 调用次数为 **0**（J-11）。
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
const UCardStub = {
  name: 'UCard',
  template: '<div class="u-card"><slot name="header" /><slot /><slot name="footer" /></div>'
}
const UBadgeStub = { name: 'UBadge', template: '<span class="u-badge"><slot /></span>' }
const UButtonStub = { name: 'UButton', template: '<button type="button" v-bind="$attrs"><slot /></button>' }
const UInputStub = {
  name: 'UInput',
  props: { modelValue: { type: [String, Number], default: '' } },
  template: '<input :value="modelValue" />'
}
const UFormFieldStub = { name: 'UFormField', props: { label: { type: String, default: '' } }, template: '<label><slot /></label>' }
/** 每条 alert 都带 data-testid，测试靠计数证明「同一原因只有一条」。 */
const UAlertStub = {
  name: 'UAlert',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div data-testid="alert" class="u-alert" :data-title="title"><span class="u-alert__title">{{ title }}</span><slot name="description">{{ description }}</slot></div>'
}
const UCheckboxStub = {
  name: 'UCheckbox',
  props: { modelValue: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
  emits: ['update:modelValue'],
  template: '<button type="button" role="checkbox" :aria-checked="String(modelValue)" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />'
}
const SFAdminFormFooterStub = {
  name: 'SFAdminFormFooter',
  props: { saving: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
  emits: ['submit', 'reset'],
  template: '<div data-testid="footer"><button type="button" :disabled="disabled" @click="$emit(\'submit\')">save</button></div>'
}

const matrixPath = fileURLToPath(
  new URL('../../../app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableMatrix = await compileVueSfc(matrixPath, 'sf-trust-earnable-matrix-for-tab', {
  computed,
  useI18n: () => ({ t }),
  usePermissionText: () => ({ permissionLabel }),
  trustLevelIcon,
  trustLevelSlug,
  earnableKeys,
  earnableMatrixState,
  earnableOrphanText,
  isBaselineKey,
  TRUST_LEVEL_ROWS
})

const tabPath = fileURLToPath(
  new URL('../../../app/components/admin/settings/site/tabs/SFAdminTrustLevelsTab.vue', import.meta.url)
)

type RequestLog = string[]

/** 每次挂载都新建一个 tab 组件实例，把「请求计数器」绑到这一次的闭包里。 */
async function compileTab(requestLog: RequestLog, respond: (path: string) => Promise<unknown>) {
  return await compileVueSfc(tabPath, `sf-trust-levels-tab-${requestLog.length}-${Math.random()}`, {
    SFAdminFormFooter: SFAdminFormFooterStub,
    SFAdminTrustLevelEarnableMatrix,
    adminOptionMap,
    useAdminOptionTab: () => ({ saveOptions: async () => {} }),
    blockNonIntegerKey,
    cloneTrustLevelsForm,
    earnableWhitelistForSave,
    normalizeTrustLevelsForm,
    readTrustLevelsConfig,
    recommendedTrustLevelsForm,
    TRUST_LEVEL_EDITABLE_LEVELS,
    TRUST_LEVEL_ROWS,
    TRUST_LEVEL_THRESHOLD_METRICS,
    toggleEarnableKey,
    trustLevelsPayload,
    // 真状态机 + 假传输层：只有真实请求次数能证明「短路生效」。
    useTrustLevelEarnableWhitelist: () => useTrustLevelEarnableWhitelist((path: string) => {
      requestLog.push(path)
      return respond(path)
    }),
    useAuthSession: () => ({ can: () => true }),
    trustLevelIcon,
    trustLevelSlug,
    normalizeEnabledOption,
    useSettingsSection: () => ({
      saving: ref(false),
      runSave: async (options: { save?: () => Promise<void> | void }) => { await options.save?.() },
      runRestore: () => {}
    }),
    useI18n: () => ({ t, te }),
    useToast: () => ({ add: () => {} }),
    ref,
    computed,
    watch,
    onMounted
  })
}

function items(featureEnabled: boolean) {
  return [
    { name: 'features.trust_levels', value: featureEnabled ? 'enabled' : 'disabled' },
    { name: 'trust.levels', value: '' }
  ]
}

function mountTab(Component: Awaited<ReturnType<typeof compileTab>>, featureEnabled: boolean) {
  return mount(Component, {
    props: { items: items(featureEnabled) },
    global: {
      components: {
        UIcon: UIconStub,
        UCard: UCardStub,
        UBadge: UBadgeStub,
        UButton: UButtonStub,
        UInput: UInputStub,
        UFormField: UFormFieldStub,
        UAlert: UAlertStub,
        UCheckbox: UCheckboxStub
      }
    }
  })
}

describe('trust levels tab — whitelist request fails (J-07)', () => {
  test('locks the matrix, shows the error inside the region, and never draws an empty matrix', async () => {
    const requestLog: RequestLog = []
    const Tab = await compileTab(requestLog, async () => {
      throw { code: 500, message: 'server error', data: { reason: 'internal_error' } }
    })
    const wrapper = mountTab(Tab, true)
    await flushPromises()

    expect(requestLog).toEqual(['/admin/trust-levels'])
    // 错误条在矩阵区内部（规范 §5.1），不是 UCard header。
    const region = wrapper.find('[data-sf-earnable-region]')
    expect(region.exists()).toBe(true)
    expect(region.find('[data-sf-earnable-error]').exists()).toBe(true)
    expect(region.find('[data-sf-earnable-locked]').exists()).toBe(true)
    // T22：空矩阵 = 「没有可勾的键」= 谎。失败态一个格子、一个勾选框都不许有。
    expect(region.find('.sf-trust-earnable__cells').exists()).toBe(false)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
    expect(region.text()).toContain('internal_error')
  })

  test('keeps saving available: a failed read must not wipe stored capability unlocks', async () => {
    const requestLog: RequestLog = []
    const Tab = await compileTab(requestLog, async () => {
      throw { code: 500, message: 'server error', data: { reason: 'internal_error' } }
    })
    const wrapper = mountTab(Tab, true)
    await flushPromises()
    // 保存按钮没有因为白名单失败而禁用（阈值仍可保存），载荷走「原样带回」分支。
    expect(wrapper.find('[data-testid="footer"] button').attributes('disabled')).toBeUndefined()
  })
})

describe('trust levels tab — feature switch off (J-11)', () => {
  test('shows exactly one alert and sends zero whitelist requests', async () => {
    const requestLog: RequestLog = []
    const Tab = await compileTab(requestLog, async () => ({ eligiblePermissions: [] }))
    const wrapper = mountTab(Tab, false)
    await flushPromises()

    // 后端 requireAdmin 先判开关，关掉时端点必然 404：发了就只多一条同因错误。
    expect(requestLog).toHaveLength(0)
    const titles = wrapper.findAll('[data-testid="alert"]').map(alert => alert.attributes('data-title'))
    // 关闭态只允许一条**关闭**提示；「关闭」不等于「失败」，失败条一条都不许出现。
    // （页面上另一条常驻 alert 是 D12 的恢复默认披露，与开关无关。）
    expect(titles.filter(title => title === t('admin.settings.trustLevels.disabled.title'))).toHaveLength(1)
    expect(titles).not.toContain(t('admin.settings.trustLevels.earnable.loadFailed.title'))
    expect(wrapper.find('[data-sf-earnable-error]').exists()).toBe(false)
    // 矩阵整块不渲染：既没有错误条，也没有锁定占位，更没有格子。
    expect(wrapper.find('[data-sf-earnable-region]').exists()).toBe(false)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
  })

  test('loads the whitelist once when the switch is on', async () => {
    // 对照实验：同一份代码在开关打开时**必须**发请求，否则上一条「0 次」可能是假绿。
    const requestLog: RequestLog = []
    const Tab = await compileTab(requestLog, async () => ({ eligiblePermissions: ['topic.create'] }))
    const wrapper = mountTab(Tab, true)
    await flushPromises()

    expect(requestLog).toEqual(['/admin/trust-levels'])
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * 1)
    expect(wrapper.find('[data-sf-earnable-error]').exists()).toBe(false)
  })
})
