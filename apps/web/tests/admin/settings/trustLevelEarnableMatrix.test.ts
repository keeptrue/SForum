import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { computed } from 'vue'
import { fileURLToPath } from 'node:url'
import { earnableMatrixState, earnableOrphanText, isBaselineKey } from '~/composables/admin/settings/useTrustLevelEarnableMatrix'
import {
  earnableKeys,
  recommendedTrustLevelsForm,
  TRUST_LEVEL_ROWS,
  type TrustLevelsForm,
  type TrustLevelWhitelistState
} from '~/composables/admin/settings/useTrustLevelsThresholds'
import { trustLevelIcon, trustLevelSlug } from '~/composables/profile/useTrustLevel'
import { compileVueSfc, mount } from '../../helpers/vueSfc'

/**
 * 能力解锁矩阵的 DOM 契约（M10 / 规范 §9）。
 *
 * 文案取自真实 zh-CN locale，测试不自造期望值：键缺失直接抛错，而不是悄悄用键名兜底。
 * 断言的是**渲染结果**（innerText / aria-label / disabled），不是源码字符串——
 * 源码断言只能证明「写了这行」，证明不了运营看到什么。
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

const matrixPath = fileURLToPath(
  new URL('../../../app/components/admin/settings/site/tabs/SFAdminTrustLevelEarnableMatrix.vue', import.meta.url)
)
const SFAdminTrustLevelEarnableMatrix = await compileVueSfc(matrixPath, 'sf-trust-earnable-matrix', {
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

function mountMatrix(options: {
  form?: TrustLevelsForm
  whitelist?: TrustLevelWhitelistState
  canManage?: boolean
} = {}) {
  return mount(SFAdminTrustLevelEarnableMatrix, {
    props: {
      form: options.form ?? recommendedTrustLevelsForm(),
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
  test('renders five level rows × one checkbox per whitelisted key', () => {
    const wrapper = mountMatrix()
    expect(wrapper.findAll('fieldset')).toHaveLength(5)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * WHITELIST.length)
    // 列数 = 白名单长度，不是写死的 7：白名单变化时矩阵必须跟着变。
    const narrow = mountMatrix({ whitelist: ready(WHITELIST.slice(0, 3)) })
    expect(narrow.findAll('[role="checkbox"]')).toHaveLength(5 * 3)
  })

  test('keeps the TL0 row visible but disabled and aria-disabled', () => {
    const wrapper = mountMatrix()
    const tl0 = wrapper.find('fieldset[data-level="0"]')
    expect(tl0.exists()).toBe(true)
    expect(tl0.attributes('data-readonly')).toBe('true')
    expect(tl0.attributes('aria-disabled')).toBe('true')
    const boxes = tl0.findAll('[role="checkbox"]')
    expect(boxes).toHaveLength(WHITELIST.length)
    for (const box of boxes) {
      expect(box.attributes('disabled')).toBeDefined()
      expect(box.attributes('aria-disabled')).toBe('true')
    }
    // TL1–TL4 默认可点。
    const tl1 = wrapper.find('fieldset[data-level="1"]').find('[role="checkbox"]')
    expect(tl1.attributes('disabled')).toBeUndefined()
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
    // TL2 = resident（常驻用户）；TL1 = member（正式用户）。档位名与阈值行同一个键。
    const box = wrapper.find('fieldset[data-level="2"] [role="checkbox"]')
    const levelLabel = t('admin.settings.trustLevels.level.resident')
    expect(box.attributes('aria-label')).toBe(`${levelLabel} 新增解锁：${permissionLabel({ key: 'topic.create' })}`)
    expect(box.attributes('aria-describedby')).toBe('sf-trust-earnable-note')
  })

  test('states the default-install no-op up front, without inventing a key count', () => {
    // 规范 §3.3：默认安装下这 7 个键已在默认用户组里，勾选是空操作。
    // 不讲这条，等于告诉运营「勾了 TL2 就能让 TL2 用户发帖」——那是假话。
    const baseline = mountMatrix().find('[data-sf-earnable-baseline]')
    expect(baseline.exists()).toBe(true)
    // 兜底文案只断言单键机制，不再断言「人人都有」（designer 规范 §3.2）。
    expect(baseline.text()).toContain('勾选只对「用户组里没有该键」的用户生效')
    expect(baseline.text()).toContain('默认用户组')
    expect(baseline.text()).not.toContain('所有人都有')
    // 白名单由 API 下发，写死数字就是第二份副本（T22）。
    expect(baseline.text()).not.toMatch(/7 个键|七个键|seven keys/)
    // 严重性通道留给 error（白名单失败）与 warning（残留键）：它不是 alert。
    expect(baseline.find('[role="alert"]').exists()).toBe(false)
    expect(baseline.classes()).not.toContain('u-alert-stub')
  })

  test('keeps the baseline note in loading and failed states, where the mechanism note is gone', () => {
    for (const whitelist of [
      { status: 'loading' as const, keys: [], error: null },
      { status: 'failed' as const, keys: [], error: 'forbidden' }
    ]) {
      const wrapper = mountMatrix({ whitelist })
      expect(wrapper.find('[data-sf-earnable-baseline]').exists()).toBe(true)
      expect(wrapper.find('#sf-trust-earnable-note').exists()).toBe(false)
    }
  })

  test('discloses the cumulative semantics and never claims the level owns the key', () => {
    const wrapper = mountMatrix()
    const text = wrapper.text()
    expect(text).toContain('及以上')
    expect(text).not.toMatch(/该档拥有|该档的权限|此等级授予/)
    expect(wrapper.find('#sf-trust-earnable-note').text()).toContain('该档及以上各档同时获得')
  })

  test('shows the empty state as a normal default while still drawing the matrix', () => {
    const wrapper = mountMatrix()
    expect(wrapper.find('[data-sf-earnable-empty]').exists()).toBe(true)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * WHITELIST.length)
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe('新增项 0/28')
  })

  test('derives the count denominator from the whitelist, never a hardcoded 28', () => {
    // 分母 = 可操作键数 × 4（TL1–TL4）；TL0 不计入（算进 TL0 会得 35，那是错的）。
    expect(mountMatrix().find('[data-sf-earnable-count]').text()).toBe('新增项 0/28')
    expect(mountMatrix({ whitelist: ready(WHITELIST.slice(0, 3)) }).find('[data-sf-earnable-count]').text())
      .toBe('新增项 0/12')
  })

  test('keeps gate keys out of the operable denominator (they have no control to click)', () => {
    // 7 键里 2 个门控 ⇒ 5×4 = 20。分子同样只数可操作键。
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['topic.create', 'post.create']
    const wrapper = mountMatrix({ form, whitelist: ready(WHITELIST, ['topic.create', 'attachment.upload']) })
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe('新增项 1/20')
  })

  test('counts checked keys per level without copying them to higher levels', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['topic.create']
    const wrapper = mountMatrix({ form })
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe('新增项 1/28')
    expect(wrapper.find('[data-sf-earnable-empty]').exists()).toBe(false)
    const checked = wrapper.findAll('[role="checkbox"]').filter(box => box.attributes('aria-checked') === 'true')
    expect(checked).toHaveLength(1)
  })

  test('emits a per-cell toggle intent instead of mutating the form', async () => {
    const wrapper = mountMatrix()
    await wrapper.find('fieldset[data-level="1"] [role="checkbox"]').trigger('click')
    expect(wrapper.emitted('toggle')).toEqual([[1, 'topic.create']])
  })

  test('locks every checkbox when the account cannot manage trust levels', () => {
    const wrapper = mountMatrix({ canManage: false })
    expect(wrapper.find('[data-sf-earnable-readonly]').exists()).toBe(true)
    for (const box of wrapper.findAll('[role="checkbox"]')) {
      expect(box.attributes('disabled')).toBeDefined()
    }
  })
})

describe('system baseline gate markers (D34 / Z3-b″)', () => {
  const GATED = ['topic.create', 'attachment.upload']
  const GATE_TITLE = '等级门控：TL0 不生效，TL1 起生效，由系统决定'

  test('replaces the checkbox with a static gate marker on every level, TL0 included', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    // 2 个门控键 × 5 档 = 10 个标记；勾选框 = 5 个可操作键 × 5 档 = 25。
    expect(wrapper.findAll('[data-sf-earnable-gate]')).toHaveLength(2 * 5)
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * 5)
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

  test('keeps the TL0 row read-only for the remaining checkboxes, markers still visible', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    const tl0 = wrapper.find('fieldset[data-level="0"]')
    expect(tl0.attributes('aria-disabled')).toBe('true')
    const boxes = tl0.findAll('[role="checkbox"]')
    expect(boxes).toHaveLength(5)
    for (const box of boxes) expect(box.attributes('disabled')).toBeDefined()
    expect(tl0.findAll('[data-sf-earnable-gate]')).toHaveLength(2)
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
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(5 * 7)
    const baseline = wrapper.find('[data-sf-earnable-baseline]').text()
    expect(baseline).toContain('勾选只对「用户组里没有该键」的用户生效')
    expect(baseline).not.toContain('等级门控')
  })

  test('excludes gate keys from the operable-count denominator: 5×4 = 20, not 7×4', () => {
    const wrapper = mountMatrix({ whitelist: ready(WHITELIST, GATED) })
    expect(wrapper.find('[data-sf-earnable-count]').text()).toBe('新增项 0/20')
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
    expect(wrapper.find('[data-sf-earnable-error]').text()).toContain('保存不会改动能力解锁配置')
  })

  test('offers a retry that re-asks the host instead of refetching on its own', async () => {
    const wrapper = mountMatrix({ whitelist: failed })
    await wrapper.find('[data-sf-earnable-retry]').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  test('renders a loading placeholder with the same footprint', () => {
    const wrapper = mountMatrix({ whitelist: { status: 'loading', keys: [], error: null } })
    const loading = wrapper.find('[data-sf-earnable-loading]')
    expect(loading.exists()).toBe(true)
    expect(loading.attributes('aria-busy')).toBe('true')
    expect(wrapper.findAll('[role="checkbox"]')).toHaveLength(0)
  })
})

describe('earnable matrix orphan disclosure', () => {
  test('warns about stored keys the whitelist no longer allows, before saving drops them', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['user.ban', 'topic.create']
    const wrapper = mountMatrix({ form })
    const alert = wrapper.find('[data-sf-earnable-orphans]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('TL2 · user.ban')
    // 白名单内的键不算残留。
    expect(alert.text()).not.toContain('topic.create')
  })

  test('stays quiet when every stored key is whitelisted', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['3'] = ['topic.create']
    expect(mountMatrix({ form }).find('[data-sf-earnable-orphans]').exists()).toBe(false)
  })
})

describe('earnable matrix derived state', () => {
  const stateFor = (keys: string[], form: TrustLevelsForm = recommendedTrustLevelsForm()) =>
    earnableMatrixState({ form, whitelist: ready(keys), canManage: true })

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

  test('counts the denominator as whitelist × 4 and never includes TL0', () => {
    expect(stateFor(WHITELIST).totalCells).toBe(28)
    expect(stateFor(WHITELIST.slice(0, 3)).totalCells).toBe(12)
    const form = recommendedTrustLevelsForm()
    form.earnable['0'] = ['topic.create']
    form.earnable['3'] = ['topic.create', 'post.create']
    const state = stateFor(WHITELIST, form)
    // TL0 由 trust.new_user_* 决定，不参与 earnable：计进去会得 35，那是错的。
    expect(state.checkedCount).toBe(2)
    expect(state.totalCells).toBe(28)
  })

  test('fails closed: no keys, no orphans, not editable when the read failed', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['2'] = ['user.ban']
    const failedState = earnableMatrixState({
      form,
      whitelist: { status: 'failed', keys: [], error: 'forbidden' },
      canManage: true
    })
    expect(failedState).toMatchObject({ ready: false, failed: true, keys: [], editable: false, orphans: [] })
    // 但已存键仍要计数：失败态不画矩阵，可这份表单还是会被保存。
    expect(failedState.checkedCount).toBe(1)
  })

  test('reports orphans per level in level order and formats them for the warning', () => {
    const form = recommendedTrustLevelsForm()
    form.earnable['3'] = ['gone.three', 'topic.create']
    form.earnable['1'] = ['gone.one']
    const state = stateFor(WHITELIST, form)
    expect(state.orphans).toEqual([
      { level: 1, key: 'gone.one' },
      { level: 3, key: 'gone.three' }
    ])
    expect(earnableOrphanText(state.orphans)).toBe('TL1 · gone.one，TL3 · gone.three')
  })
})
