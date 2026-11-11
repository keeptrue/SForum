import { describe, expect, test } from 'bun:test'
import {
  activeRankOf,
  maxTierSeq,
  activeTierCount,
  addTier,
  canAddTier,
  canDeleteTier,
  canRetireTier,
  deleteTier,
  insertTierAt,
  moveTier,
  nextTierId,
  renameTier,
  retiredImpactKeys,
  retiredTierImpactKeys,
  retireTier,
  restoreTier,
  setTierColor,
  setTierIcon,
  setTierRequirement,
  TRUST_LEVEL_TIER_MAX,
  zeroRequirements,
  type TierForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  TIER_COLOR_TOKENS,
  defaultTierColorName,
  tierAccentStyle,
  tierColorOf
} from '~/utils/profile/trustLevelColors'
import {
  fallbackTiers,
  recommendedTrustLevelsForm,
  trustLevelsPayload
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'

/**
 * 动态档位的编辑层：插入/停用/排序/边界/锚定。
 *
 * 最贵的一类 bug 是「插入一档把别人的阈值搬走」或「停用掉了最后一档」——
 * 两者都不会报错，只会静默改运营配置，所以这里逐条钉死（D-N7 / D-N8）。
 */

function tiers(): TierForm[] {
  return fallbackTiers()
}

function withEarnable(list: TierForm[], index: number, keys: string[]): TierForm[] {
  const next = list.map(tier => ({ ...tier, earnable: [...tier.earnable] }))
  next[index]!.earnable = keys
  return next
}

describe('tier identity and rank', () => {
  test('rank comes from the active position, TL0 is not in the array', () => {
    const list = tiers()
    expect(activeTierCount(list)).toBe(4)
    expect(activeRankOf(list, list[0]!.id)).toBe(1)
    expect(activeRankOf(list, list[3]!.id)).toBe(4)
    expect(activeRankOf(list, 'nope')).toBeNull()
  })

  test('maxTierSeq reads only the tier_<n> family', () => {
    const list = tiers()
    expect(maxTierSeq(list)).toBe(0)
    expect(maxTierSeq(addTier(list, null, 6))).toBe(6)
  })

  test('a retired tier has no rank', () => {
    const list = retireTier(tiers(), tiers()[1]!.id)
    expect(activeRankOf(list, list[1]!.id)).toBeNull()
    expect(activeTierCount(list)).toBe(3)
  })

  test('generates backend-shaped ids (tier_<n>, underscore) that never collide', () => {
    const list = addTier(tiers())
    const ids = list.map(tier => tier.id)
    expect(new Set(ids).size).toBe(ids.length)
    // 后端只认 `tier_<数字>` 或内置五档 id；连字符格式保存必然 422 id_invalid。
    const added = ids[ids.length - 1]!
    expect(added).toMatch(/^tier_[0-9]+$/)
    expect(added).not.toContain('-')
    // 已占用时继续自增，不复用。
    expect(nextTierId(addTier(list))).not.toBe(added)
  })

  test('honours the server-provided sequence, and keeps incrementing on local repeats', () => {
    expect(nextTierId(tiers(), 6)).toBe('tier_6')
    const once = addTier(tiers(), null, 6)
    expect(once[once.length - 1]!.id).toBe('tier_6')
    // 同一页连续插两档：调用方传同一个 seq 也不能造出重复 id。
    const twice = addTier(once, null, 6)
    expect(twice[twice.length - 1]!.id).toBe('tier_7')
    expect(new Set(twice.map(tier => tier.id)).size).toBe(twice.length)
    // 插入同样按 seq 走，且格式一致。
    const inserted = insertTierAt(tiers(), 2, null, 9)
    expect(inserted[1]!.id).toBe('tier_9')
  })
})

describe('adding and inserting tiers', () => {
  test('adds at the end with zero thresholds and an explicit empty earnable', () => {
    const list = addTier(tiers())
    expect(list).toHaveLength(5)
    const added = list[4]!
    expect(added.retired).toBe(false)
    expect(added.earnable).toEqual([])
    expect(added.requirements).toEqual(zeroRequirements())
  })

  test('caps active tiers at 8 and never grows the array past it', () => {
    let list = tiers()
    for (let index = 0; index < 10; index += 1) list = addTier(list)
    expect(activeTierCount(list)).toBe(TRUST_LEVEL_TIER_MAX)
    expect(canAddTier(list)).toBe(false)
    // 越界时返回等值副本，不抛错。
    expect(addTier(list)).toEqual(list)
  })

  test('inserting at a rank changes only the order, never another tier\'s config', () => {
    const before = withEarnable(tiers(), 1, ['topic.create'])
    const beforeById = new Map(before.map(tier => [tier.id, tier]))

    const after = insertTierAt(before, 2)

    expect(activeTierCount(after)).toBe(5)
    // 锚定不变量：原有每档的身份与配置逐字保留。
    for (const tier of after) {
      const original = beforeById.get(tier.id)
      if (!original) continue
      expect(tier).toEqual(original)
    }
    // 新档插在原来第 2 档之前：原第 2 档退到第 3 位，身份不变。
    expect(after[2]!.id).toBe(before[1]!.id)
    expect(after[1]!.id).not.toBe(before[1]!.id)
    expect(after.map(entry => entry.id).filter(id => id !== after[1]!.id))
      .toEqual(before.map(entry => entry.id))
  })

  test('inserting past the last rank appends instead of losing the tier', () => {
    const list = insertTierAt(tiers(), 99)
    expect(list).toHaveLength(5)
    expect(list[4]!.retired).toBe(false)
  })
})

describe('retire and restore (soft retire)', () => {
  test('retiring keeps the config and the array position', () => {
    const before = withEarnable(tiers(), 2, ['topic.create', 'post.create'])
    const after = retireTier(before, before[2]!.id)
    expect(after).toHaveLength(before.length)
    expect(after[2]!.retired).toBe(true)
    expect(after[2]!.earnable).toEqual(['topic.create', 'post.create'])
    expect(after[2]!.requirements).toEqual(before[2]!.requirements)
  })

  test('the last active tier cannot be retired', () => {
    let list = tiers()
    for (const tier of tiers().slice(1)) list = retireTier(list, tier.id)
    expect(activeTierCount(list)).toBe(1)
    expect(canRetireTier(list)).toBe(false)
    expect(retireTier(list, list[0]!.id)).toEqual(list)
  })

  test('restoring returns the tier to its original position and respects the cap', () => {
    const retired = retireTier(tiers(), tiers()[1]!.id)
    const restored = restoreTier(retired, retired[1]!.id)
    expect(restored[1]!.id).toBe(retired[1]!.id)
    expect(restored[1]!.retired).toBe(false)
    expect(activeRankOf(restored, restored[1]!.id)).toBe(2)

    let full = tiers()
    for (let index = 0; index < 4; index += 1) full = addTier(full)
    expect(activeTierCount(full)).toBe(TRUST_LEVEL_TIER_MAX)
    const withRetired = retireTier(full, full[0]!.id)
    // 停用一档后再加满：恢复会超上限，必须拒绝而不是造出 9 档。
    let capped = withRetired
    while (canAddTier(capped)) capped = addTier(capped)
    expect(restoreTier(capped, withRetired[0]!.id)).toEqual(capped)
  })
})

describe('ordering', () => {
  test('moving up swaps with the previous active tier and keeps configs attached to ids', () => {
    const before = withEarnable(tiers(), 1, ['topic.create'])
    const moved = moveTier(before, before[1]!.id, -1)
    expect(moved[0]!.id).toBe(before[1]!.id)
    expect(moved[1]!.id).toBe(before[0]!.id)
    expect(moved[0]!.earnable).toEqual(['topic.create'])
  })

  test('moving past either end is a no-op copy', () => {
    const list = tiers()
    expect(moveTier(list, list[0]!.id, -1)).toEqual(list)
    expect(moveTier(list, list[3]!.id, 1)).toEqual(list)
    expect(moveTier(list, 'nope', 1)).toEqual(list)
  })

  test('a retired tier between two active ones does not block the swap', () => {
    const list = retireTier(tiers(), tiers()[1]!.id)
    const moved = moveTier(list, list[2]!.id, -1)
    // active 顺序换了（veteran 上移到 resident 之前），停用档位留在数组原位置。
    const activeIds = moved.filter(tier => !tier.retired).map(tier => tier.id)
    // veteran 上移一位：active 顺序变成 [veteran, member, leader]。
    expect(activeIds).toEqual([list[2]!.id, list[0]!.id, list[3]!.id])
    expect(moved[1]!.id).toBe(list[1]!.id)
    expect(moved[1]!.retired).toBe(true)
  })
})

describe('names and icons', () => {
  test('renaming only touches the requested language', () => {
    const list = tiers()
    const renamed = renameTier(list, list[0]!.id, 'zh', '  种子  用户 ')
    expect(renamed[0]!.name.zh).toBe('种子 用户')
    expect(renamed[0]!.name.en).toBe(list[0]!.name.en)
    // 其它档位不受影响。
    expect(renamed[1]).toEqual(list[1])
  })

  test('renaming caps the length instead of writing an unbounded string', () => {
    const list = tiers()
    const renamed = renameTier(list, list[0]!.id, 'en', 'x'.repeat(200))
    expect(renamed[0]!.name.en.length).toBe(40)
  })

  test('setting an icon is gated by shape, not by the recommendation list', () => {
    const list = tiers()
    const id = list[0]!.id
    // 全名（`i-<collection>-<name>`）是合法取值：图标选择器用 lucide + tabler 全量目录，
    // 拿 16 项推荐表当闸门只会把运营的真实选择静默丢掉（存进去还会被后端接受）。
    expect(setTierIcon(list, id, 'i-tabler-crown')[0]!.icon).toBe('i-tabler-crown')
    // 16 个默认短名仍可直接写入（历史数据 / 默认序列）。
    expect(setTierIcon(list, id, 'leaf')[0]!.icon).toBe('leaf')
    // 未知集合、缺 collection、空白：等值副本，不写坏配置（后端会以 422 拒掉）。
    expect(setTierIcon(list, id, 'i-fa-rocket')).toEqual(list)
    expect(setTierIcon(list, id, 'rocket')).toEqual(list)
    expect(setTierIcon(list, id, '   ')).toEqual(list)
    // 归一化只 trim：剥掉 `i-lucide-` 前缀会得到不在白名单里的短名（arrow-up 之类）→ 保存 422。
    expect(setTierIcon(list, id, '  i-lucide-arrow-up  ')[0]!.icon).toBe('i-lucide-arrow-up')
    // null = 清空，回落 rank 默认序列。
    expect(setTierIcon(list, id, null)[0]!.icon).toBeNull()
  })

  test('threshold writes clamp to non-negative integers', () => {
    const list = tiers()
    const id = list[0]!.id
    expect(setTierRequirement(list, id, 'days_visited', '12')[0]!.requirements.days_visited).toBe(12)
    expect(setTierRequirement(list, id, 'days_visited', -3)[0]!.requirements.days_visited).toBe(0)
    expect(setTierRequirement(list, id, 'days_visited', 'abc')[0]!.requirements.days_visited).toBe(0)
    expect(setTierRequirement(list, id, 'days_visited', 999999)[0]!.requirements.days_visited).toBe(0)
    // 纯函数：入参不动。
    expect(list[0]!.requirements.days_visited).not.toBe(12)
  })
})

describe('retire impact disclosure (D-N8)', () => {
  test('lists only keys the retired tier grants and no active tier does', () => {
    const base = withEarnable(tiers(), 1, ['topic.create'])
    const retired = retireTier(base, base[1]!.id)
    expect(retiredImpactKeys(retired)).toEqual(['topic.create'])
  })

  test('reports nothing when an active tier still grants the same key', () => {
    let base = withEarnable(tiers(), 1, ['topic.create'])
    base = withEarnable(base, 2, ['topic.create'])
    const retired = retireTier(base, base[1]!.id)
    expect(retiredImpactKeys(retired)).toEqual([])
  })
})

describe('tier colors (D-C1)', () => {
  test('every tier has an effective color by default, and the 9 defaults never repeat', () => {
    // 零配置：TL0..TL8 共 9 个合法 rank 各一个不同的色。
    const seen = new Set<string>()
    for (let rank = 0; rank < TIER_COLOR_TOKENS.length; rank += 1) seen.add(defaultTierColorName(rank))
    expect(seen.size).toBe(TIER_COLOR_TOKENS.length)
    expect(defaultTierColorName(0)).toBe('slate')
    expect(defaultTierColorName(1)).toBe('teal')
    // 9 色的理由：满配 8 档时 TL8 必须是 amber，不能循环回 slate 与 TL0 撞色。
    expect(TIER_COLOR_TOKENS).toHaveLength(9)
    expect(defaultTierColorName(8)).toBe('amber')
    expect(new Set([defaultTierColorName(0), defaultTierColorName(8)]).size).toBe(2)
    // 超出 9 个 token 后循环取，不返回 undefined。
    expect(defaultTierColorName(TIER_COLOR_TOKENS.length)).toBe(defaultTierColorName(0))

    const list = tiers()
    // 未设颜色的档位也拿得到有效色（存储值 → 位次默认）。
    expect(list.map((tier, index) => tierColorOf(tier, index + 1)))
      .toEqual(['teal', 'emerald', 'sky', 'indigo'])
  })

  test('the stored color wins while it is on the whitelist, otherwise the rank default applies', () => {
    expect(tierColorOf({ color: 'rose' }, 1)).toBe('rose')
    expect(tierColorOf({ color: 'not-a-color' }, 1)).toBe('teal')
    expect(tierColorOf({ color: null }, 2)).toBe('emerald')
    expect(tierColorOf(null, 0)).toBe('slate')
  })

  test('setTierColor gates on shape (token or hex) and returns an equal copy on rejection', () => {
    const list = tiers()
    const id = list[0]!.id
    expect(setTierColor(list, id, 'violet')[0]!.color).toBe('violet')
    // 自定义 hex：大小写归一后落表单（后端也是小写存储，两处必须一致）。
    expect(setTierColor(list, id, '#7C3AED')[0]!.color).toBe('#7c3aed')
    expect(setTierColor(list, id, '#7c3aed')[0]!.color).toBe('#7c3aed')
    // 推荐色板只是渲染候选，**不是闸门**：候选之外的合法 hex 也必须能落进表单。
    expect(setTierColor(list, id, '#0F766E')[0]!.color).toBe('#0f766e')
    // 形状不合法 → 等值副本，不写坏配置。
    for (const bad of ['chartreuse', 'red', '#fff', '#7c3aed00', 'rgb(0,0,0)']) {
      const rejected = setTierColor(list, id, bad)
      expect(rejected[0]!.color).toBeNull()
      expect(rejected).not.toBe(list)
    }
    // null = 回到「按位次自动」。
    expect(setTierColor(setTierColor(list, id, 'violet'), id, null)[0]!.color).toBeNull()
    // 不存在的 id 也是等值副本，不抛错。
    expect(setTierColor(list, 'nope', 'violet').map(tier => tier.color)).toEqual(list.map(tier => tier.color))
  })

  test('tierAccentStyle always yields a usable --tl-accent, falling back to slate', () => {
    expect(tierAccentStyle('rose')).toEqual({ '--tl-accent': 'var(--sf-tl-rose)' })
    // 白名单外的 token 若直接写进 var()，color-mix 会让整条色条失效 → 必须回退。
    expect(tierAccentStyle('chartreuse')).toEqual({ '--tl-accent': 'var(--sf-tl-slate)' })
    expect(tierAccentStyle(null)).toEqual({ '--tl-accent': 'var(--sf-tl-slate)' })
  })
})

describe('tier deletion (D-C2 / D-C3)', () => {
  test('only a retired tier can be deleted', () => {
    const list = tiers()
    expect(canDeleteTier(list, list[0]!.id)).toBe(false)
    const retired = retireTier(list, list[1]!.id)
    expect(canDeleteTier(retired, list[1]!.id)).toBe(true)
    expect(canDeleteTier(retired, 'nope')).toBe(false)
  })

  test('deleting an active or unknown tier is a no-op (fail-closed)', () => {
    const list = tiers()
    expect(deleteTier(list, list[0]!.id)).toHaveLength(list.length)
    expect(deleteTier(list, 'nope')).toHaveLength(list.length)
  })

  test('deleting a retired tier removes it and never moves another tier\'s rank', () => {
    const list = tiers()
    const target = list[1]!.id
    const retired = retireTier(list, target)
    const ranksBefore = retired
      .filter(tier => !tier.retired)
      .map(tier => [tier.id, activeRankOf(retired, tier.id)] as const)
    const deleted = deleteTier(retired, target)
    expect(deleted.some(tier => tier.id === target)).toBe(false)
    // 删除的档位本来就不占 rank：其余档位位次逐字不变（这就是「删除永不改 rank」）。
    expect(deleted.map(tier => [tier.id, activeRankOf(deleted, tier.id)] as const)).toEqual(ranksBefore)
    // 纯函数：入参不动。
    expect(retired).toHaveLength(list.length)
  })

  test('retiredTierImpactKeys reports per-tier impact, not the whole retired set', () => {
    let base = withEarnable(tiers(), 1, ['topic.create'])
    base = withEarnable(base, 2, ['post.edit_own'])
    base = retireTier(retireTier(base, base[1]!.id), base[2]!.id)
    expect(retiredTierImpactKeys(base, base[1]!.id)).toEqual(['topic.create'])
    expect(retiredTierImpactKeys(base, base[2]!.id)).toEqual(['post.edit_own'])
    // 数组位置的两档互不吞并；未知 id 不返回垃圾。
    expect(retiredTierImpactKeys(base, 'nope')).toEqual([])

    // 另一 active 档也给了同一个键 → 不算该档独占的影响。
    let shared = withEarnable(tiers(), 1, ['topic.create'])
    shared = withEarnable(shared, 2, ['topic.create'])
    expect(retiredTierImpactKeys(shared, shared[1]!.id)).toEqual([])
  })
})

describe('appearance round-trip through the payload', () => {
  const configOf = (payload: Array<{ name: string, value: string }>) => {
    expect(payload).toHaveLength(1)
    return JSON.parse(payload[0]!.value) as {
      tiers: Array<{ id: string, icon?: string, color?: string }>
    }
  }

  test('a host icon full name survives serialisation verbatim (never stripped to a short name)', () => {
    const form = recommendedTrustLevelsForm()
    const tiersWithIcon = setTierIcon(form.tiers, form.tiers[0]!.id, 'i-tabler-crown')
    const config = configOf(trustLevelsPayload({ tiers: tiersWithIcon }))
    // 剥前缀会写出 `crown`… 不，`i-tabler-crown` → `crown` 恰好也在 16 项里，
    // 所以这里用 tabler 独有的多段名：剥完既不在白名单、也丢掉了集合语义。
    const withSegmented = setTierIcon(form.tiers, form.tiers[0]!.id, 'i-tabler-device-desktop')
    expect(configOf(trustLevelsPayload({ tiers: withSegmented })).tiers[0]!.icon).toBe('i-tabler-device-desktop')
    expect(config.tiers[0]!.icon).toBe('i-tabler-crown')
  })

  test('a custom hex is serialised lowercase and an automatic color omits the key', () => {
    const form = recommendedTrustLevelsForm()
    const id = form.tiers[0]!.id
    const custom = configOf(trustLevelsPayload({ tiers: setTierColor(form.tiers, id, '#7C3AED') }))
    expect(custom.tiers[0]!.color).toBe('#7c3aed')

    // 未配置 = 省略键（后端 omitempty + 空串语义是「跟随位次」），不能写成空串。
    const auto = configOf(trustLevelsPayload({ tiers: setTierColor(form.tiers, id, null) }))
    expect(auto.tiers[0]!.color).toBeUndefined()
  })

  test('an unset icon falls back to the rank default instead of an empty string', () => {
    const form = recommendedTrustLevelsForm()
    const cleared = setTierIcon(form.tiers, form.tiers[0]!.id, null)
    const config = configOf(trustLevelsPayload({ tiers: cleared }))
    // 空串在 UI 上是「没有图标」，后端契约里 icon 是 required。
    expect(config.tiers[0]!.icon).toBeTruthy()
    expect(config.tiers[0]!.icon).not.toBe('')
  })
})
