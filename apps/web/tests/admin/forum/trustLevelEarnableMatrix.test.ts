import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed, ref } from 'vue'
import { fileURLToPath } from 'node:url'
import {
  earnableMatrixState,
  earnableOrphanText,
  isBaselineKey
} from '~/composables/admin/forum/trustLevels/useTrustLevelEarnableMatrix'
import {
  activeRankOf,
  activeTierCount,
  addTier,
  moveTier,
  retireTier
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  earnableKeys,
  recommendedTrustLevelsForm,
  type TrustLevelsForm,
  type TrustLevelWhitelistState
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { trustLevelIconOf, trustLevelName } from '~/composables/profile/useTrustLevel'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'
import { compileVueSfc, mount } from '../../helpers/vueSfc'

/**
 * 能力解锁矩阵的 DOM 契约（M10 / 规范 §9），N 档版。
 *
 * 三条几何口径（D-N12）必须在这里钉住，因为它们随档数变化：
 * 1. **一行一档**：行数 = `form.tiers` 里的 active 档数（TL0 不在数组里，所以矩阵里没有 TL0 行）；
 * 2. **≤6 档表格 / 7–8 档卡片**：`data-layout` 与表头的出现与否跟着档数切换；
 * 3. **分母 = active 档数 × 可操作键数**：7 档时是 49，不是写死的 28。
 *
 * 文案取自真实 zh-CN locale，测试不自造期望值：键缺失直接抛错，而不是悄悄用键名兜底。
 * 断言的是**渲染结果**（text / aria-label / disabled），不是源码字符串。
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

/** 与 `usePermissionText().permissionLabel` 同规则：先查目录文案，缺失回落裸键。 */
function permissionLabel(item: { key: string }) {
  const path = `admin.permissionCatalog.${item.key}.label`
  return typeof lookup(path) === 'string' ? t(path) : item.key
}

const WHITELIST = [
  'topic.create',
  'post.create',
  'attachment.upload',
  'topic.edit_own',
  'post.edit_own',
  'topic.delete_own',
  'post.delete_own'
]

const ready = (
  keys: string[] = WHITELIST,
  baselineKeys: string[] = []
): TrustLevelWhitelistState => ({ status: 'ready', keys, baselineKeys, error: null })

const loading = (): TrustLevelWhitelistState => ({ status: 'loading', keys: [], baselineKeys: [], error: null })

const UIconStub = { name: 'UIcon', props: { name: { type: String, default: '' } }, template: '<i :data-icon="name" />' }
const UAlertStub = {
  name: 'UAlert',
  props: { title: { type: String, default: '' }, description: { type: String, default: '' } },
  template: '<div class="u-alert-stub" :data-title="title"><slot name="description">{{ description }}</slot></div>'
}
const UButtonStub = {
  name: 'UButton',
  template: '<button type="button" v-bind="$attrs"><slot /></button>'
}
/** UCheckbox 真实实现把 attrs 转发到 `role="checkbox"` 的 button，这里保持同一语义。 */
const UCheckboxStub = {
  name: 'UCheckbox',
  props: { modelValue: { type: Boolean, default: false }, disabled: { type: Boolean, default: false } },
  emits: ['update:modelValue'],
  template: '<button type="button" role="checkbox" :aria-checked="String(modelValue)" :disabled="disabled" @click="$emit(\'update:modelValue\', !modelValue)" />'
}

const i18n = () => ({ t, locale: ref('zh-CN') })

/** 一行的几何被拆成 `SFAdminTrustLevelEarnableRow.vue`：矩阵要挂它，所以先把行编译出来。 */
const rowPath = fileURLToPath(
  new URL('../../../app/components/admin/forum/trust-levels/SFAdminTrustLevelEarnableRow.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableRow = await compileVueSfc(rowPath, 'sf-trust-earnable-row', {
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
const SFAdminTrustLevelEarnableMatrix = await compileVueSfc(matrixPath, 'sf-trust-earnable-matrix', {
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

/** 按目标 active 档数补齐（复用同一套纯函数，不手搓档位对象）。 */
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

function formWithEarnable(tierId: string, keys: string[]): TrustLevelsForm {
  const form = recommendedTrustLevelsForm()
  tierOf(form, tierId).earnable = [...keys]
  return form
}

function mountMatrix(options: {
  form?: TrustLevelsForm
  whitelist?: TrustLevelWhitelistState
  canManage?: boolean
} = {}) {
  const form = options.form ?? recommendedTrustLevelsForm()
  return mount(SFAdminTrustLevelEarnableMatrix, {
    props: {
      form,
      whitelist: options.whitelist ?? ready(),
      canManage: options.canManage ?? true
    },
    global: {
      components: {
        UIcon: UIconStub,
        UAlert: UAlertStub,
        UButton: UButtonStub,
        UCheckbox: UCheckboxStub
      }
    }
  })
}

describe('earnable matrix rendering', () => {
  test('renders one row per active tier × one checkbox per whitelisted key', () => {
    const form = recommendedTrustLevelsForm()
    const activeCount = activeTierCount(form.tiers)
    const wrapper = mountMatrix({ form })
    expect(wrapper.findAll('fieldset')).toHaveLength(activeCount)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(activeCount * WHITELIST.length)
    // 行身份是档位 id（顺序 = 判定顺序），rank 只是展示位次。
    expect(wrapper.findAll('fieldset').map(row => row.attributes('data-tier-id')))
      .toEqual(form.tiers.map(tier => tier.id))
    expect(wrapper.findAll('fieldset').map(row => row.attributes('data-level')))
      .toEqual(form.tiers.map((_tier, index) => String(index + 1)))
    // 列数 = 白名单长度，不是写死的 7：白名单变化时矩阵必须跟着变。
    const narrow = mountMatrix({ form, whitelist: ready(WHITELIST.slice(0, 3)) })
    expect(narrow.findAll('[role="checkbox"]')).toHaveLength(activeCount * 3)
  })

  test('has no TL0 row at all: TL0 lives in trust.new_user_*, not in tiers[]', () => {
    const wrapper = mountMatrix()
    // TL0 既不进 tiers 数组，也不进矩阵：与其画一行永远禁用的格子，不如不画。
    expect(wrapper.find('fieldset[data-level="0"]').exists()).toBe(false)
    expect(wrapper.find('fieldset[data-tier-id="newcomer"]').exists()).toBe(false)
    // 第一行就是 rank 1，且默认可点。
    const first = wrapper.find('fieldset[data-level="1"]')
    expect(first.exists()).toBe(true)
    expect(first.find('[role="checkbox"]').attributes('disabled')).toBeUndefined()
  })

  test('reuses the permission catalog copy for all seven keys, with no second mapping', () => {
    // 规范 §6：7 个键的人话已存在于 `admin.permissionCatalog.<键>.label`；
    // 缺键会回落裸键，英文站就会出现中文混排或裸键，所以这里逐键钉住。
    const expected = ['发布主题', '发表回复', '上传附件', '编辑自己的主题', '编辑自己的回复', '删除自己的主题', '删除自己的回复']
    expect(WHITELIST.map(key => permissionLabel({ key }))).toEqual(expected)
    expect(mountMatrix().text()).toContain('上传附件')
  })

  test('names every checkbox level → semantics → key, and points at the note', () => {
    const wrapper = mountMatrix()
    // 行 rank 2 = 档位列表里的第二档（推荐默认是 resident / 常驻用户）。
    const box = wrapper.find('fieldset[data-level="2"] [role="checkbox"]')
    const levelLabel = t('profile.trustLevel.name.resident')
    expect(box.attributes('aria-label')).toBe(t('admin.forum.trustLevels.earnable.aria.cell', {
      level: levelLabel,
      permission: permissionLabel({ key: 'topic.create' })
    }))
    expect(box.attributes('aria-describedby')).toBe('sf-trust-earnable-note')
  })

  test('states the default-install no-op up front, without inventing a key count', () => {
    // 规范 §3.3：默认安装下这 7 个键已在默认用户组里，勾选是空操作。
    // 不讲这条，等于告诉运营「勾了 TL2 就能让 TL2 用户发帖」——那是假话。
    const baseline = mountMatrix().find('[data-sf-earnable-baseline]')
    expect(baseline.exists()).toBe(true)
    // 兜底文案只断言单键机制，不再断言「人人都有」（designer 规范 §3.2）。
    expect(baseline.text()).toContain(t('admin.forum.trustLevels.earnable.baseline.title'))
    expect(baseline.text()).toContain(t('admin.forum.trustLevels.earnable.baseline.text'))
    expect(baseline.text()).not.toContain('所有人都有')
    // 白名单由 API 下发，写死数字就是第二份副本（T22）。
    expect(baseline.text()).not.toMatch(/7 个键|七个键|seven keys/)
    // 严重性通道留给 error（白名单失败）与 warning（残留键）：它不是 alert。
    expect(baseline.find('[role="alert"]').exists()).toBe(false)
    expect(baseline.classes()).not.toContain('u-alert-stub')
  })

  test('keeps the baseline note in loading and failed states, where the mechanism note is gone', () => {
    for (const whitelist of [
      loading(),
      { status: 'failed' as const, keys: [], baselineKeys: [], error: 'forbidden' }
    ]) {
      const wrapper = mountMatrix({ whitelist })
      expect(wrapper.find('[data-sf-earnable-baseline]').exists()).toBe(true)
      expect(wrapper.find('#sf-trust-earnable-note').exists()).toBe(false)
    }
  })

  test('discloses the cumulative semantics and never claims the level owns the key', () => {
    const wrapper = mountMatrix()
    const text = wrapper.text()
    expect(text).toContain(t('admin.forum.trustLevels.earnable.note'))
    expect(text).not.toMatch(/该档拥有|该档的权限|此等级授予/)
    expect(wrapper.find('#sf-trust-earnable-note').text()).toBe(t('admin.forum.trustLevels.earnable.note'))
  })

  test('shows the empty state as a normal default while still drawing the matrix', () => {
    const form = recommendedTrustLevelsForm()
    const activeCount = activeTierCount(form.tiers)
    const wrapper = mountMatrix({ form })
    expect(wrapper.find('[data-sf-earnable-empty]').exists()).toBe(true)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(activeCount * WHITELIST.length)
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe(t('admin.forum.trustLevels.earnable.count', {
      checked: 0,
      total: activeCount * WHITELIST.length
    }))
  })

  test('derives the count denominator from active tiers × operable keys, never a hardcoded 28', () => {
    const form = recommendedTrustLevelsForm()
    const activeCount = activeTierCount(form.tiers)
    expect(mountMatrix({ form }).find('[data-sf-earnable-count]').text())
      .toBe(t('admin.forum.trustLevels.earnable.count', { checked: 0, total: activeCount * WHITELIST.length }))
    expect(mountMatrix({ form, whitelist: ready(WHITELIST.slice(0, 3)) }).find('[data-sf-earnable-count]').text())
      .toBe(t('admin.forum.trustLevels.earnable.count', { checked: 0, total: activeCount * 3 }))
    // 7 档 = 7 × 7 = 49：分母跟着档数走，不是停在「7 键 × 4 档」。
    const seven = formWithActive(7)
    expect(mountMatrix({ form: seven }).find('[data-sf-earnable-count]').text())
      .toBe(t('admin.forum.trustLevels.earnable.count', { checked: 0, total: 7 * WHITELIST.length }))
  })

  test('keeps gate keys out of the operable denominator (they have no control to click)', () => {
    // 7 键里 2 个门控 ⇒ 5 × active 档数。分子同样只数可操作键。
    const form = formWithEarnable('resident', ['topic.create', 'post.create'])
    const activeCount = activeTierCount(form.tiers)
    const wrapper = mountMatrix({ form, whitelist: ready(WHITELIST, ['topic.create', 'attachment.upload']) })
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe(t('admin.forum.trustLevels.earnable.count', {
      checked: 1,
      total: activeCount * 5
    }))
  })

  test('counts checked keys per level without copying them to higher levels', () => {
    const form = formWithEarnable('resident', ['topic.create'])
    const wrapper = mountMatrix({ form })
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe(t('admin.forum.trustLevels.earnable.count', {
      checked: 1,
      total: activeTierCount(form.tiers) * WHITELIST.length
    }))
    expect(wrapper.find('[data-sf-earnable-empty]').exists()).toBe(false)
    const checked = wrapper.findAll('[role="checkbox"]').filter(box => box.attributes('aria-checked') === 'true')
    expect(checked).toHaveLength(1)
  })

  test('emits a per-cell toggle intent carrying the tier id, not a rank', async () => {
    const wrapper = mountMatrix()
    await wrapper.find('fieldset[data-level="1"] [role="checkbox"]').trigger('click')
    // 身份是 id：插入/排序/停用都不该让勾选落到别的档上。
    expect(wrapper.emitted('toggle')).toEqual([['member', 'topic.create']])
  })

  test('locks every checkbox when the account cannot manage trust levels', () => {
    const wrapper = mountMatrix({ canManage: false })
    expect(wrapper.find('[data-sf-earnable-readonly]').exists()).toBe(true)
    for (const box of wrapper.findAll('[role="checkbox"]')) {
      expect(box.attributes('disabled')).toBeDefined()
    }
  })
})

describe('earnable matrix geometry by tier count (D-N12)', () => {
  test('uses a table up to 6 active tiers and switches to per-tier cards at 7', () => {
    const six = mountMatrix({ form: formWithActive(6) })
    expect(six.find('[data-sf-earnable-region]').attributes('data-layout')).toBe('table')
    expect(six.findAll('fieldset')).toHaveLength(6)
    expect(six.findAll('fieldset[data-layout="table"]')).toHaveLength(6)
    expect(six.find('.sf-trust-earnable__columns').exists()).toBe(true)

    const seven = mountMatrix({ form: formWithActive(7) })
    expect(seven.find('[data-sf-earnable-region]').attributes('data-layout')).toBe('cards')
    expect(seven.findAll('fieldset')).toHaveLength(7)
    expect(seven.findAll('fieldset[data-layout="cards"]')).toHaveLength(7)
    // 卡片形态没有表头：每格自带名称，8 列挤在 390px 里读不了。
    expect(seven.find('.sf-trust-earnable__columns').exists()).toBe(false)
    // 格子数不变，行数跟着档数走。
    expect(seven.findAll('[role="checkbox"]')).toHaveLength(7 * WHITELIST.length)
  })

  test('also uses cards at the 8 tier ceiling', () => {
    const eight = mountMatrix({ form: formWithActive(8) })
    expect(eight.find('[data-sf-earnable-region]').attributes('data-layout')).toBe('cards')
    expect(eight.findAll('fieldset')).toHaveLength(8)
    expect(eight.findAll('[role="checkbox"]')).toHaveLength(8 * WHITELIST.length)
  })
})

describe('system baseline gate markers (D34 / Z3-b″)', () => {
  const GATED = ['topic.create', 'attachment.upload']
  const GATE_TITLE = '等级门控：TL0 不生效，TL1 起生效，由系统决定'

  test('replaces the checkbox with a static gate marker on every active tier', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const activeCount = activeTierCount(recommendedTrustLevelsForm().tiers)
    // 2 个门控键 × N 档 = 2N 个标记；勾选框 = 5 个可操作键 × N 档。
    expect(wrapper.findAll('[data-sf-earnable-gate]')).toHaveLength(2 * activeCount)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * activeCount)
    for (const row of wrapper.findAll('fieldset')) {
      expect(row.findAll('[role="checkbox"]')).toHaveLength(5)
      expect(row.findAll('[data-sf-earnable-gate]')).toHaveLength(2)
    }
  })

  test('marks the gate as an image with title, aria-label and no tab stop', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const markers = wrapper.findAll('[data-sf-earnable-gate]')
    for (const marker of markers) {
      expect(marker.attributes('role')).toBe('img')
      expect(marker.attributes('title')).toBe(GATE_TITLE)
      // 语义必须含「不可取消」：门控不是运营能关掉的选项。
      expect(marker.attributes('aria-label')).toContain('不可取消')
      expect(marker.attributes('tabindex')).toBeUndefined()
    }
    // 文案是 i18n 解析后的真值，不是 key 名。
    expect(markers[0]!.attributes('aria-label')).not.toContain('gateAria')
    expect(markers[0]!.attributes('aria-label')).toContain('发布主题')
  })

  test('puts a 等级门控 badge in the column header instead of a second sentence', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const badges = wrapper.findAll('[data-sf-earnable-gate-badge]')
    expect(badges).toHaveLength(2)
    expect(badges[0]!.text()).toContain('等级门控')
    expect(badges[0]!.text()).not.toContain('keyBadge')
    expect(badges[0]!.attributes('title')).toBe(GATE_TITLE)
  })

  test('keeps gate keys out of the operable count and keeps markers visible', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const activeCount = activeTierCount(recommendedTrustLevelsForm().tiers)
    expect(wrapper.find('[data-sf-earnable-count]').text())
      .toBe(t('admin.forum.trustLevels.earnable.count', { checked: 0, total: 5 * activeCount }))
    expect(wrapper.findAll('[data-sf-earnable-gate]')).toHaveLength(2 * activeCount)
  })

  test('rewrites the note and drops the false "everyone has them" claim', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const baseline = wrapper.find('[data-sf-earnable-baseline]').text()
    expect(baseline).toContain('等级门控键（发布主题、上传附件）由系统决定')
    expect(baseline).toContain('TL1 及以上生效')
    expect(baseline).toContain('后台无法取消')
    // 旧口径两句都必须消失：它们在 TL0 上为假（§3.2 / §3.3）。
    expect(baseline).not.toContain('所有人都有')
    expect(baseline).not.toContain('默认空集')
    expect(wrapper.find('[data-sf-earnable-empty]').text()).toContain('TL0 不生效，TL1 及以上生效')
  })

  test('red state: with the field missing there are zero markers and the 2 keys revert to checkboxes', () => {
    const wrapper = mountMatrix()
    expect(wrapper.findAll('[data-sf-earnable-gate]')).toHaveLength(0)
    expect(wrapper.findAll('[data-sf-earnable-gate-badge]')).toHaveLength(0)
    // 7 个键全部退回勾选框：字段掉了，UI 会少一句本该在的披露，这个测试必须红。
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(activeTierCount(recommendedTrustLevelsForm().tiers) * 7)
    const baseline = wrapper.find('[data-sf-earnable-baseline]').text()
    expect(baseline).toContain(t('admin.forum.trustLevels.earnable.baseline.title'))
    expect(baseline).not.toContain('等级门控')
  })

  test('excludes gate keys from the operable-count denominator: 5 × active tiers', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const activeCount = activeTierCount(recommendedTrustLevelsForm().tiers)
    expect(wrapper.find('[data-sf-earnable-count]').text())
      .toBe(t('admin.forum.trustLevels.earnable.count', { checked: 0, total: 5 * activeCount }))
  })
})

describe('earnable matrix failure state (T22)', () => {
  const failed: TrustLevelWhitelistState = { status: 'failed', keys: [], baselineKeys: [], error: 'forbidden' }

  test('renders the error alert plus a locked placeholder and no matrix at all', () => {
    const wrapper = mountMatrix({ whitelist: failed })
    expect(wrapper.find('[data-sf-earnable-error]').exists()).toBe(true)
    expect(wrapper.find('[data-sf-earnable-locked]').exists()).toBe(true)
    // 保留项：基线说明不依赖白名单，失败态照常渲染（规范 §5.1）。
    expect(wrapper.find('[data-sf-earnable-baseline]').exists()).toBe(true)
    // 空矩阵 = 谎：失败态一个格子都不许出现。
    expect(wrapper.find('.sf-trust-earnable__columns').exists()).toBe(false)
    expect(wrapper.find('.sf-trust-earnable__cells').exists()).toBe(false)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
    // 失败态没有可解释的对象：说明与计数都不渲染。
    expect(wrapper.find('#sf-trust-earnable-note').exists()).toBe(false)
    expect(wrapper.find('[data-sf-earnable-count]').exists()).toBe(false)
  })

  test('shows the raw reason so operators can diagnose it', () => {
    const wrapper = mountMatrix({ whitelist: failed })
    expect(wrapper.find('[data-sf-earnable-reason]').text()).toContain('forbidden')
    expect(wrapper.find('[data-sf-earnable-error]').text())
      .toContain(t('admin.forum.trustLevels.earnable.loadFailed.description'))
  })

  test('offers a retry that re-asks the host instead of refetching on its own', async () => {
    const wrapper = mountMatrix({ whitelist: failed })
    await wrapper.find('[data-sf-earnable-retry]').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  test('renders a loading placeholder with the same footprint', () => {
    const wrapper = mountMatrix({ whitelist: loading() })
    const placeholder = wrapper.find('[data-sf-earnable-loading]')
    expect(placeholder.exists()).toBe(true)
    expect(placeholder.attributes('aria-busy')).toBe('true')
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
  })
})

describe('earnable matrix orphan disclosure', () => {
  test('warns about stored keys the whitelist no longer allows, before saving drops them', () => {
    const form = formWithEarnable('resident', ['user.ban', 'topic.create'])
    const wrapper = mountMatrix({ form })
    const alert = wrapper.find('[data-sf-earnable-orphans]')
    expect(alert.exists()).toBe(true)
    // 披露指向的是档位**名字**（id 锚定，改名后跟着新名字）。
    expect(alert.text()).toContain(`${t('profile.trustLevel.name.resident')} · user.ban`)
    // 白名单内的键不算残留。
    expect(alert.text()).not.toContain('topic.create')
  })

  test('stays quiet when every stored key is whitelisted', () => {
    const form = formWithEarnable('veteran', ['topic.create'])
    expect(mountMatrix({ form }).find('[data-sf-earnable-orphans]').exists()).toBe(false)
  })
})

describe('earnable matrix derived state', () => {
  const stateFor = (keys: string[], form: TrustLevelsForm = recommendedTrustLevelsForm(), baselineKeys: string[] = []) =>
    earnableMatrixState({ form, whitelist: ready(keys, baselineKeys), canManage: true })

  test('derives rows, ranks and tier count from the active tiers', () => {
    const form = recommendedTrustLevelsForm()
    const state = stateFor(WHITELIST, form)
    expect(state.rows.map(row => row.id)).toEqual(form.tiers.map(tier => tier.id))
    expect(state.rows.map(row => row.rank)).toEqual(form.tiers.map((_tier, index) => index + 1))
    expect(state.rows.map(row => row.tier.name.zh)).toEqual(form.tiers.map(tier => tier.name.zh))
    expect(state.tierCount).toBe(activeTierCount(form.tiers))

    // 停用档不进矩阵：它保留配置，但不参与判定。
    let retired = form.tiers
    retired = retireTier(retired, 'leader')
    const withoutTop = stateFor(WHITELIST, { tiers: retired })
    expect(withoutTop.tierCount).toBe(activeTierCount(retired))
    expect(withoutTop.rows.map(row => row.id)).not.toContain('leader')
  })

  test('rank follows the array order after a move, not the tier id', () => {
    // 上移一档只改数组顺序：rank 由 active 位次派生，id 与配置逐字保留。
    const moved = moveTier(recommendedTrustLevelsForm().tiers, 'veteran', -1)
    const state = stateFor(WHITELIST, { tiers: moved })
    expect(state.rows.map(row => row.id)).toEqual(moved.map(tier => tier.id))
    expect(state.rows.map(row => row.rank)).toEqual(moved.map((_tier, index) => index + 1))
    expect(activeRankOf(moved, 'veteran')).toBe(2)
  })

  test('a genuinely empty whitelist is ready, not failed', () => {
    // 空数组 = 后端说「没有可勾的键」；failed 才是「读不到」。两者不许互相冒充（T22）。
    const state = stateFor([])
    expect(state).toMatchObject({ ready: true, failed: false, keys: [], editable: true })
    expect([state.columnCount, state.totalCells]).toEqual([0, 0])
  })

  test('the same key checked in two tiers counts twice: they are two cells', () => {
    const form = recommendedTrustLevelsForm()
    tierOf(form, 'member').earnable = ['topic.create']
    tierOf(form, 'resident').earnable = ['topic.create']
    // 累积语义由后端裁决；矩阵只数格子，不把低档的勾选复制或合并到高档。
    expect(stateFor(WHITELIST, form).checkedCount).toBe(2)
  })

  test('the span flags depend on the key count, never on the tier count', () => {
    for (const activeCount of [4, 6, 7, 8]) {
      const state = stateFor(WHITELIST, formWithActive(activeCount))
      expect([state.spanTwo, state.spanThree]).toEqual([true, true])
    }
  })

  test('derives columns and the remainder-based span rule from the whitelist, not from 7', () => {
    // 判据是余数（规范 §2.3）：断言的是「跟着白名单变」，不是「等于 7」——
    // 后者在任何实现下都成立时就没有信息量。
    const seven = stateFor(WHITELIST)
    expect([seven.columnCount, seven.spanTwo, seven.spanThree]).toEqual([7, true, true])
    const six = stateFor(WHITELIST.slice(0, 6))
    expect([six.columnCount, six.spanTwo, six.spanThree]).toEqual([6, false, false])
    const three = stateFor(WHITELIST.slice(0, 3))
    expect([three.columnCount, three.spanTwo, three.spanThree]).toEqual([3, true, false])
    // 9 项：`9 % 3 === 0` → 3 列不该跨，`9 % 2 === 1` → 2 列该跨。奇数 ≠ 跨列。
    const nine = stateFor([...WHITELIST, 'extra.a', 'extra.b'])
    expect([nine.columnCount, nine.spanTwo, nine.spanThree]).toEqual([9, true, false])
  })

  test('counts the denominator as active tiers × operable keys and never includes TL0', () => {
    const four = recommendedTrustLevelsForm()
    expect(stateFor(WHITELIST, four).totalCells).toBe(activeTierCount(four.tiers) * WHITELIST.length)
    expect(stateFor(WHITELIST.slice(0, 3), four).totalCells).toBe(activeTierCount(four.tiers) * 3)

    const seven = formWithActive(7)
    expect(stateFor(WHITELIST, seven).totalCells).toBe(7 * WHITELIST.length)

    const form = recommendedTrustLevelsForm()
    tierOf(form, 'member').earnable = ['topic.create']
    tierOf(form, 'veteran').earnable = ['topic.create', 'post.create']
    const state = stateFor(WHITELIST, form)
    // 分子只数 active 档里的可操作键；分母不重复计数。
    expect(state.checkedCount).toBe(3)
    expect(state.totalCells).toBe(activeTierCount(form.tiers) * WHITELIST.length)
  })

  test('keeps gate keys out of checkedCount but on the form', () => {
    const form = formWithEarnable('resident', ['topic.create'])
    const state = stateFor(WHITELIST, form, ['topic.create'])
    // 门控键的存量勾选原样保留在表单里（保存时逐字节回写），但不是「新增项」。
    expect(tierOf(form, 'resident').earnable).toEqual(['topic.create'])
    expect(state.checkedCount).toBe(0)
  })

  test('fails closed: no keys, no orphans, not editable when the read failed', () => {
    const form = formWithEarnable('resident', ['user.ban'])
    const failedState = earnableMatrixState({
      form,
      whitelist: { status: 'failed', keys: [], baselineKeys: [], error: 'forbidden' },
      canManage: true
    })
    expect(failedState).toMatchObject({ ready: false, failed: true, keys: [], editable: false, orphans: [] })
    // 但已存键仍要计数：失败态不画矩阵，可这份表单还是会被保存。
    expect(failedState.checkedCount).toBe(1)
  })

  test('reports orphans by tier id in tier order and formats them through a label resolver', () => {
    const form = recommendedTrustLevelsForm()
    tierOf(form, 'veteran').earnable = ['gone.three', 'topic.create']
    tierOf(form, 'member').earnable = ['gone.one']
    const state = stateFor(WHITELIST, form)
    expect(state.orphans).toEqual([
      { tierId: 'member', key: 'gone.one' },
      { tierId: 'veteran', key: 'gone.three' }
    ])
    // 第二个参数是**解析函数**：档位名由调用方用 i18n 组合，纯函数不引入文案。
    const labels: Record<string, string> = {
      member: t('profile.trustLevel.name.member'),
      veteran: t('profile.trustLevel.name.veteran')
    }
    expect(earnableOrphanText(state.orphans, tierId => labels[tierId] ?? tierId))
      .toBe(`${labels.member} · gone.one，${labels.veteran} · gone.three`)
  })
})
