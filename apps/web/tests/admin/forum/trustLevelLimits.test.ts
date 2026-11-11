import { describe, expect, test } from 'bun:test'
import {
  fromLimitsForm,
  limitsModeOf,
  limitValueOf,
  parseLimitInput,
  setLimitsMode,
  setLimitValue,
  toLimitsForm,
  TRUST_LEVEL_LIMIT_KEYS,
  TRUST_LEVEL_LIMIT_MAX
} from '~/composables/admin/forum/trustLevels/useTrustLevelLimits'
import { fallbackTiers, trustLevelsPayload } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { retireTier, type TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'

/**
 * 分档限额的三态（D-N11）：`null` 继承 / 对象逐字段覆盖 / `0` 不限额。
 *
 * 最贵的一类 bug 是「0 被当成没配」或「空字段被写成 0」——两者都不报错，
 * 只会在生产里把某个档位的冷却静默改成 0（= 免冷却）。所以这里逐条钉死。
 */

function tiers(): TierForm[] {
  return fallbackTiers()
}

describe('limits mode (D-N11)', () => {
  test('null means inherit, an object means per-field override', () => {
    expect(limitsModeOf(null)).toBe('inherit')
    expect(limitsModeOf(undefined)).toBe('inherit')
    expect(limitsModeOf(fromLimitsForm({}))).toBe('custom')
    // 全 null 的对象仍是「逐字段覆盖」模式：对象存在就代表运营进过这个模式。
    expect(limitsModeOf(fromLimitsForm({ topicCooldownSeconds: '' }))).toBe('custom')
  })

  test('switching to inherit drops the object; switching to custom lands four nulls', () => {
    const list = tiers()
    const id = list[0]!.id
    const custom = setLimitsMode(list, id, 'custom')
    expect(custom[0]!.limits).toEqual({
      topicCooldownSeconds: null,
      commentCooldownSeconds: null,
      dailyTopicLimit: null,
      dailyCommentLimit: null
    })
    // 进入 custom 后四个字段仍然各自「继承」，只是模式变了。
    expect(limitsModeOf(custom[0]!.limits)).toBe('custom')

    const inherited = setLimitsMode(custom, id, 'inherit')
    expect(inherited[0]!.limits).toBeNull()
    // 纯函数：入参不动。
    expect(custom[0]!.limits).not.toBeNull()
  })

  test('switching mode on an unknown tier is a no-op (fail-closed)', () => {
    const list = tiers()
    const next = setLimitsMode(list, 'nope', 'custom')
    expect(next.map(tier => tier.limits)).toEqual(list.map(tier => tier.limits))
  })
})

describe('limit values: 0 is not "unset"', () => {
  test('0 is kept as an explicit override while an empty input stays null', () => {
    const list = tiers()
    const id = list[0]!.id
    const inherited = setLimitsMode(list, id, 'custom')
    const withZero = setLimitValue(inherited, id, 'topicCooldownSeconds', 0)
    expect(withZero[0]!.limits).toEqual({
      topicCooldownSeconds: 0,
      commentCooldownSeconds: null,
      dailyTopicLimit: null,
      dailyCommentLimit: null
    })
    // 0 与 null 是两个不同的存储值，不能互相折叠。
    expect(limitValueOf(withZero[0]!.limits, 'topicCooldownSeconds')).toBe(0)
    expect(limitValueOf(withZero[0]!.limits, 'dailyTopicLimit')).toBeNull()

    // 清空一个已填的字段 = 回到「该字段继承」。
    const cleared = setLimitValue(withZero, id, 'topicCooldownSeconds', null)
    expect(cleared[0]!.limits!.topicCooldownSeconds).toBeNull()
    const clearedByEmptyString = setLimitValue(withZero, id, 'topicCooldownSeconds', '')
    expect(clearedByEmptyString[0]!.limits!.topicCooldownSeconds).toBeNull()
  })

  test('writing a value while the tier still inherits upgrades it to an object', () => {
    const list = tiers()
    const id = list[1]!.id
    const upgraded = setLimitValue(list, id, 'dailyTopicLimit', '5')
    expect(limitsModeOf(upgraded[1]!.limits)).toBe('custom')
    expect(upgraded[1]!.limits!.dailyTopicLimit).toBe(5)
  })

  test('invalid input is refused instead of written (fail-closed, no throw)', () => {
    const list = tiers()
    const id = list[0]!.id
    const base = setLimitValue(setLimitsMode(list, id, 'custom'), id, 'dailyTopicLimit', 7)
    for (const bad of ['-1', '1.5', 'abc', '1e3', 1e9]) {
      const next = setLimitValue(base, id, 'dailyTopicLimit', bad as string | number)
      expect(next[0]!.limits!.dailyTopicLimit).toBe(7)
      expect(next).not.toBe(base)
    }
    // 纯空白等于「清空」→ 回到继承（不是非法值、更不是 0）。
    expect(setLimitValue(base, id, 'dailyTopicLimit', ' ')[0]!.limits!.dailyTopicLimit).toBeNull()
    // 未知 key / 未知 id 也不改任何东西。
    expect(setLimitValue(base, id, 'nope' as never, 3)[0]!.limits!.dailyTopicLimit).toBe(7)
    expect(setLimitValue(base, 'nope', 'dailyTopicLimit', 3).map(tier => tier.limits))
      .toEqual(base.map(tier => tier.limits))
  })

  test('parseLimitInput distinguishes empty / zero / invalid', () => {
    expect(parseLimitInput('', 'dailyTopicLimit')).toBeNull()
    expect(parseLimitInput(' ', 'dailyTopicLimit')).toBeNull()
    expect(parseLimitInput('0', 'dailyTopicLimit')).toBe(0)
    expect(parseLimitInput(0, 'dailyTopicLimit')).toBe(0)
    expect(parseLimitInput('12', 'dailyTopicLimit')).toBe(12)
    expect(parseLimitInput('-3', 'dailyTopicLimit')).toBeUndefined()
    expect(parseLimitInput('3.5', 'dailyTopicLimit')).toBeUndefined()
    expect(parseLimitInput('x', 'dailyTopicLimit')).toBeUndefined()
    // 输入护栏：超过上限按非法处理，不静默截断。
    expect(parseLimitInput(String(TRUST_LEVEL_LIMIT_MAX.dailyTopicLimit + 1), 'dailyTopicLimit')).toBeUndefined()
    expect(parseLimitInput(String(TRUST_LEVEL_LIMIT_MAX.dailyTopicLimit), 'dailyTopicLimit'))
      .toBe(TRUST_LEVEL_LIMIT_MAX.dailyTopicLimit)
  })

  test('the string form round-trips through toLimitsForm / fromLimitsForm', () => {
    const original = { topicCooldownSeconds: 0, commentCooldownSeconds: null, dailyTopicLimit: 30, dailyCommentLimit: null }
    const input = toLimitsForm(original)
    expect(input).toEqual({ topicCooldownSeconds: '0', commentCooldownSeconds: '', dailyTopicLimit: '30', dailyCommentLimit: '' })
    expect(fromLimitsForm(input)).toEqual(original)
    // inherit（null）也有一致的字符串形态：四个空串。
    expect(toLimitsForm(null)).toEqual({ topicCooldownSeconds: '', commentCooldownSeconds: '', dailyTopicLimit: '', dailyCommentLimit: '' })
  })

  test('retired tiers keep their limits editable and are never dropped', () => {
    const list = tiers()
    const id = list[1]!.id
    const retired = retireTier(list, id)
    // 停用只改 retired 位，limits 原样保留（软停用语义，D-N8）。
    expect(retired[1]!.limits).toEqual(list[1]!.limits)
    const edited = setLimitValue(setLimitsMode(retired, id, 'custom'), id, 'dailyCommentLimit', 42)
    expect(edited[1]!.retired).toBe(true)
    expect(edited[1]!.limits!.dailyCommentLimit).toBe(42)
  })
})

describe('payload shape: limits is never omitted', () => {
  function decoded(form: { tiers: TierForm[] }) {
    return JSON.parse(trustLevelsPayload({ tiers: form.tiers })[0]!.value) as {
      tiers: Array<Record<string, unknown>>
    }
  }

  test('an inheriting tier writes limits: null (never a missing key)', () => {
    const list = tiers()
    const config = decoded({ tiers: list })
    for (const tier of config.tiers) {
      expect('limits' in tier).toBe(true)
      expect(tier.limits).toBeNull()
    }
  })

  test('a custom tier writes an object that keeps 0 and null apart', () => {
    const list = tiers()
    const id = list[0]!.id
    const custom = setLimitValue(setLimitsMode(list, id, 'custom'), id, 'dailyTopicLimit', 0)
    const config = decoded({ tiers: custom })
    expect(config.tiers[0]!.limits).toEqual({
      topicCooldownSeconds: null,
      commentCooldownSeconds: null,
      dailyTopicLimit: 0,
      dailyCommentLimit: null
    })
    // 四个键都在（后端按字段覆盖，缺键与 null 的语义不同）。
    expect(Object.keys(config.tiers[0]!.limits as Record<string, unknown>).sort())
      .toEqual([...TRUST_LEVEL_LIMIT_KEYS].sort())
  })
})
