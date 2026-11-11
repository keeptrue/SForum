import { describe, expect, test } from 'bun:test'
import {
  BADGE_SORT_KEYS,
  badgeFiltersActive,
  filterBadges,
  grantRevocable,
  grantSourceOf,
  sortBadges,
  validateBadgeGroupForm,
  type AdminBadgeFilters,
  type AdminBadgeGrantRow,
  type AdminBadgeRow
} from '~/utils/admin/adminBadges'

/** 造行只填 filterBadges/sortBadges 用得到的字段，其余给类型合法的零值。 */
function badge(overrides: Partial<AdminBadgeRow> & Pick<AdminBadgeRow, 'id' | 'name'>): AdminBadgeRow {
  return {
    slug: `badge-${overrides.id}`,
    description: '',
    icon: 'i-lucide-medal',
    tier: 'bronze',
    groupId: null,
    groupName: null,
    grantMode: 'manual',
    metric: null,
    threshold: null,
    grantedCount: 0,
    enabled: true,
    position: 0,
    ...overrides
  }
}

const list: AdminBadgeRow[] = [
  badge({ id: 1, name: 'Pioneer', slug: 'pioneer', tier: 'gold', groupId: 2, groupName: 'Content', grantMode: 'automatic', grantedCount: 12, enabled: true }),
  badge({ id: 2, name: 'Chatter', slug: 'chatter', tier: 'silver', groupId: null, grantMode: 'automatic', grantedCount: 40, enabled: false }),
  badge({ id: 3, name: 'Astronomer', slug: 'astro', tier: 'bronze', groupId: 1, groupName: 'Honor', grantMode: 'manual', grantedCount: 3, enabled: true, description: 'stargazer' })
]

const EMPTY: AdminBadgeFilters = { q: '', group: '', tier: '', grantMode: '', enabled: '' }

describe('filterBadges', () => {
  test('passes everything through when no filter is set', () => {
    expect(filterBadges(list, EMPTY)).toEqual(list)
  })

  test('matches q against name, slug, and description case-insensitively', () => {
    expect(filterBadges(list, { ...EMPTY, q: 'pion' }).map(row => row.id)).toEqual([1])
    expect(filterBadges(list, { ...EMPTY, q: 'CHATTER' }).map(row => row.id)).toEqual([2])
    expect(filterBadges(list, { ...EMPTY, q: 'stargazer' }).map(row => row.id)).toEqual([3])
    expect(filterBadges(list, { ...EMPTY, q: 'no-such-badge' })).toEqual([])
  })

  test('filters by explicit group id and by the ungrouped sentinel', () => {
    expect(filterBadges(list, { ...EMPTY, group: '1' }).map(row => row.id)).toEqual([3])
    expect(filterBadges(list, { ...EMPTY, group: 'none' }).map(row => row.id)).toEqual([2])
    expect(filterBadges(list, { ...EMPTY, group: '999' })).toEqual([])
  })

  test('filters by tier, grant mode, and enabled state', () => {
    expect(filterBadges(list, { ...EMPTY, tier: 'gold' }).map(row => row.id)).toEqual([1])
    expect(filterBadges(list, { ...EMPTY, grantMode: 'manual' }).map(row => row.id)).toEqual([3])
    expect(filterBadges(list, { ...EMPTY, enabled: 'true' }).map(row => row.id)).toEqual([1, 3])
    expect(filterBadges(list, { ...EMPTY, enabled: 'false' }).map(row => row.id)).toEqual([2])
  })

  test('composes filters conjunctively', () => {
    expect(filterBadges(list, { ...EMPTY, enabled: 'true', tier: 'bronze' }).map(row => row.id)).toEqual([3])
    expect(filterBadges(list, { ...EMPTY, enabled: 'false', tier: 'gold' })).toEqual([])
  })

  test('badgeFiltersActive distinguishes an empty directory from a filtered-out one', () => {
    expect(badgeFiltersActive(EMPTY)).toBe(false)
    expect(badgeFiltersActive({ ...EMPTY, q: 'x' })).toBe(true)
    expect(badgeFiltersActive({ ...EMPTY, enabled: 'false' })).toBe(true)
  })
})

describe('sortBadges', () => {
  test('keeps the backend order when no sort key is selected', () => {
    expect(sortBadges(list, '', 'asc')).toBe(list)
  })

  test('sorts by name with a stable copy (input untouched)', () => {
    const sorted = sortBadges(list, 'name', 'asc')
    expect(sorted.map(row => row.name)).toEqual(['Astronomer', 'Chatter', 'Pioneer'])
    expect(sorted).not.toBe(list)
    expect(list.map(row => row.id)).toEqual([1, 2, 3])
  })

  test('sorts tiers by weight, gold above silver above bronze (topBadgeOrderSQL parity)', () => {
    expect(sortBadges(list, 'tier', 'asc').map(row => row.tier)).toEqual(['bronze', 'silver', 'gold'])
    expect(sortBadges(list, 'tier', 'desc').map(row => row.tier)).toEqual(['gold', 'silver', 'bronze'])
  })

  test('sorts grantedCount numerically, not lexically', () => {
    expect(sortBadges(list, 'grantedCount', 'desc').map(row => row.grantedCount)).toEqual([40, 12, 3])
  })

  test('exposes exactly the sortable keys the table headers emit', () => {
    expect(BADGE_SORT_KEYS).toEqual(['name', 'tier', 'grantedCount'])
  })

  test('keeps the original relative order for equal sort keys (stable)', () => {
    const ties = [
      badge({ id: 11, name: 'B', grantedCount: 5, tier: 'gold' }),
      badge({ id: 12, name: 'A', grantedCount: 5, tier: 'gold' }),
      badge({ id: 13, name: 'C', grantedCount: 5, tier: 'gold' })
    ]
    expect(sortBadges(ties, 'grantedCount', 'desc').map(row => row.id)).toEqual([11, 12, 13])
    expect(sortBadges(ties, 'tier', 'asc').map(row => row.id)).toEqual([11, 12, 13])
  })

  test('sorts an unknown tier as the lowest weight instead of dropping the row', () => {
    const odd = [
      badge({ id: 21, name: 'Odd', tier: 'platinum' as AdminBadgeRow['tier'] }),
      badge({ id: 22, name: 'Bronze', tier: 'bronze' })
    ]
    const sorted = sortBadges(odd, 'tier', 'asc')
    expect(sorted.map(row => row.id)).toEqual([21, 22])
    expect(sorted).toHaveLength(2)
  })
})

describe('grant source discriminator', () => {
  function grant(overrides: Partial<AdminBadgeGrantRow>): AdminBadgeGrantRow {
    return {
      userId: 7,
      username: 'alice',
      displayName: 'Alice',
      badgeId: 1,
      badgeSlug: 'pioneer',
      grantedAt: '2026-11-11T00:00:00Z',
      grantedBy: null,
      grantReason: '',
      ...overrides
    }
  }

  test('a non-null grantedBy is a manual grant, regardless of reason text', () => {
    expect(grantSourceOf(grant({ grantedBy: 3, grantReason: 'event reward' }))).toBe('manual')
    expect(grantSourceOf(grant({ grantedBy: 3, grantReason: '' }))).toBe('manual')
  })

  test('null grantedBy with the recompute reason is a batch recompute grant', () => {
    expect(grantSourceOf(grant({ grantedBy: null, grantReason: 'recompute' }))).toBe('recompute')
  })

  test('null grantedBy without a reason is an automatic rule grant', () => {
    expect(grantSourceOf(grant({ grantedBy: null, grantReason: '' }))).toBe('automatic')
  })

  test('only manual rows are revocable (revoke on others is a 422 no-op)', () => {
    expect(grantRevocable(grant({ grantedBy: 3, grantReason: '' }))).toBe(true)
    expect(grantRevocable(grant({ grantedBy: null, grantReason: 'recompute' }))).toBe(false)
    expect(grantRevocable(grant({ grantedBy: null, grantReason: '' }))).toBe(false)
  })
})

describe('validateBadgeGroupForm', () => {
  test('requires key and name', () => {
    expect(validateBadgeGroupForm({ key: '', name: '' })).toEqual({ key: 'required', name: 'required' })
    expect(validateBadgeGroupForm({ key: 'content', name: '' })).toEqual({ name: 'required' })
    expect(validateBadgeGroupForm({ key: '  ', name: 'Content' })).toEqual({ key: 'required' })
    expect(validateBadgeGroupForm({ key: 'content', name: 'Content' })).toEqual({})
  })
})
