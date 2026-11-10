import { describe, expect, test } from 'bun:test'
import {
  bookmarkActionLabelKey,
  bookmarkReasonKey,
  bookmarkRemindBounds,
  bookmarkRemindIssue,
  bookmarkRemindIssueKey,
  bookmarkRemindStatusKey,
  bookmarkEmptyStateCopy,
  bookmarkErrorKey,
  bookmarkRemindAtFromLocalInput,
  bookmarkRemindAtFromPreset,
  bookmarkRemindAtToLocalInput,
  bookmarkRemindPresetDays,
  bookmarkRemindPresetLabelKey,
  bookmarkRemindRelative,
  bookmarkRemindRelativeKey,
  bookmarkRemindRelativeParams,
  bookmarkTargetTypeIcon,
  bookmarkTargetTypeLabelKey,
  bookmarkVisibilities,
  bookmarkVisibilityHintKey,
  bookmarkVisibilityIcon,
  bookmarkVisibilityLabelKey,
  formatBookmarkRemindAt,
  isBookmarkRemindDue,
  normalizeBookmarkTargetType,
  normalizeBookmarkVisibility,
  recommendedBookmarkVisibility
} from '~/utils/forum/forumBookmark'
import { recommendedSiteDateTimeSettings } from '~/utils/siteDateTime'

const shanghai = { ...recommendedSiteDateTimeSettings, timezone: 'Asia/Shanghai' }

describe('bookmark visibility', () => {
  test('normalizes to the two supported values and defaults to private', () => {
    expect(bookmarkVisibilities).toEqual(['private', 'public'])
    expect(recommendedBookmarkVisibility).toBe('private')
    expect(normalizeBookmarkVisibility('public')).toBe('public')
    expect(normalizeBookmarkVisibility('  PUBLIC ')).toBe('public')
    expect(normalizeBookmarkVisibility('private')).toBe('private')
    expect(normalizeBookmarkVisibility(undefined)).toBe('private')
    expect(normalizeBookmarkVisibility('friends')).toBe('private')
    expect(normalizeBookmarkVisibility('friends', 'public')).toBe('public')
  })

  test('maps labels, hints and icons', () => {
    expect(bookmarkVisibilityLabelKey('private')).toBe('bookmark.visibility.private')
    expect(bookmarkVisibilityLabelKey('public')).toBe('bookmark.visibility.public')
    expect(bookmarkVisibilityHintKey('private')).toBe('bookmark.visibility.privateHint')
    expect(bookmarkVisibilityHintKey('public')).toBe('bookmark.visibility.publicHint')
    expect(bookmarkVisibilityIcon('private')).toBe('i-lucide-lock')
    expect(bookmarkVisibilityIcon('public')).toBe('i-lucide-globe')
  })

  test('action label follows the bookmarked state', () => {
    expect(bookmarkActionLabelKey(false)).toBe('bookmark.action.add')
    expect(bookmarkActionLabelKey(true)).toBe('bookmark.action.remove')
  })
})

describe('bookmark target type', () => {
  test('accepts only topic and comment', () => {
    expect(normalizeBookmarkTargetType('topic')).toBe('topic')
    expect(normalizeBookmarkTargetType('COMMENT')).toBe('comment')
    expect(normalizeBookmarkTargetType('profile')).toBeNull()
    expect(normalizeBookmarkTargetType(null)).toBeNull()
  })

  test('maps badges to distinct labels and icons', () => {
    expect(bookmarkTargetTypeLabelKey('topic')).toBe('bookmark.target.topic')
    expect(bookmarkTargetTypeLabelKey('comment')).toBe('bookmark.target.comment')
    expect(bookmarkTargetTypeIcon('topic')).toBe('i-lucide-file-text')
    expect(bookmarkTargetTypeIcon('comment')).toBe('i-lucide-message-circle')
  })
})

describe('bookmark remind time', () => {
  const now = new Date('2026-01-01T00:00:00.000Z')

  test('presets resolve to UTC ISO offsets from now', () => {
    expect(bookmarkRemindAtFromPreset('day1', now)).toBe('2026-01-02T00:00:00.000Z')
    expect(bookmarkRemindAtFromPreset('day3', now)).toBe('2026-01-04T00:00:00.000Z')
    expect(bookmarkRemindAtFromPreset('week1', now)).toBe('2026-01-08T00:00:00.000Z')
  })

  test('unknown preset ids fall back to one day instead of NaN', () => {
    expect(bookmarkRemindPresetDays('day7')).toBe(1)
    expect(bookmarkRemindPresetDays('week1')).toBe(7)
    expect(bookmarkRemindPresetLabelKey('week1')).toBe('bookmark.remind.preset.week1')
    expect(bookmarkRemindPresetLabelKey('nope')).toBe('bookmark.remind.preset.day1')
    expect(bookmarkRemindAtFromPreset('nope', now)).toBe('2026-01-02T00:00:00.000Z')
  })

  test('datetime-local input converts to UTC and back without losing the instant', () => {
    expect(bookmarkRemindAtFromLocalInput('')).toBeNull()
    expect(bookmarkRemindAtFromLocalInput('   ')).toBeNull()
    expect(bookmarkRemindAtFromLocalInput('not-a-date')).toBeNull()

    const iso = bookmarkRemindAtFromLocalInput('2026-01-05T09:30')
    expect(iso).not.toBeNull()
    expect(iso?.endsWith('Z')).toBe(true)
    // 输入按浏览器本地墙钟解释；往返必须回到同一时刻，与时区无关。
    expect(bookmarkRemindAtToLocalInput(iso)).toBe('2026-01-05T09:30')
    expect(bookmarkRemindAtToLocalInput('')).toBe('')
    expect(bookmarkRemindAtToLocalInput('not-a-date')).toBe('')
  })

  test('formats the reminder in the site timezone and format', () => {
    expect(formatBookmarkRemindAt('2026-01-02T00:00:00.000Z', shanghai)).toBe('2026-01-02 08:00')
    expect(formatBookmarkRemindAt('2026-01-02T00:00:00.000Z', recommendedSiteDateTimeSettings)).toBe('2026-01-02 00:00')
    expect(formatBookmarkRemindAt(null, shanghai)).toBe('')
  })

  test('detects due reminders, and never calls a missing reminder due', () => {
    expect(isBookmarkRemindDue('2025-12-31T23:59:00.000Z', now)).toBe(true)
    expect(isBookmarkRemindDue('2026-01-01T00:00:00.000Z', now)).toBe(true)
    expect(isBookmarkRemindDue('2026-01-01T00:01:00.000Z', now)).toBe(false)
    expect(isBookmarkRemindDue(null, now)).toBe(false)
    expect(isBookmarkRemindDue('not-a-date', now)).toBe(false)
  })
})

describe('bookmark remind relative description', () => {
  // now = 2026-01-01 08:00 上海
  const now = new Date('2026-01-01T00:00:00.000Z')

  test('classifies by the site calendar day, not the UTC day', () => {
    expect(bookmarkRemindRelative(null, now, 'Asia/Shanghai')).toEqual({ kind: 'none' })
    expect(bookmarkRemindRelative('not-a-date', now, 'Asia/Shanghai')).toEqual({ kind: 'none' })
    expect(bookmarkRemindRelative('2025-12-31T20:00:00.000Z', now, 'Asia/Shanghai')).toEqual({ kind: 'due' })
    expect(bookmarkRemindRelative('2026-01-01T06:00:00.000Z', now, 'Asia/Shanghai')).toEqual({ kind: 'today' })
    expect(bookmarkRemindRelative('2026-01-02T02:00:00.000Z', now, 'Asia/Shanghai')).toEqual({ kind: 'tomorrow' })
    expect(bookmarkRemindRelative('2026-01-06T00:00:00.000Z', now, 'Asia/Shanghai')).toEqual({ kind: 'inDays', days: 5 })
  })

  test('a later UTC day can still be tomorrow locally and vice versa', () => {
    // UTC 同日 17:00 → 上海已是次日 01:00。
    expect(bookmarkRemindRelative('2026-01-01T17:00:00.000Z', now, 'Asia/Shanghai')).toEqual({ kind: 'tomorrow' })
    // 同一时刻在 UTC 下就是今天。
    expect(bookmarkRemindRelative('2026-01-01T17:00:00.000Z', now, 'UTC')).toEqual({ kind: 'today' })
  })

  test('a reminder that lands earlier on the same local day is already due', () => {
    expect(bookmarkRemindRelative('2025-12-31T23:00:00.000Z', now, 'UTC')).toEqual({ kind: 'due' })
  })

  test('maps relative states to i18n keys and params', () => {
    expect(bookmarkRemindRelativeKey({ kind: 'none' })).toBe('bookmark.remind.none')
    expect(bookmarkRemindRelativeKey({ kind: 'due' })).toBe('bookmark.remind.relative.due')
    expect(bookmarkRemindRelativeKey({ kind: 'today' })).toBe('bookmark.remind.relative.today')
    expect(bookmarkRemindRelativeKey({ kind: 'tomorrow' })).toBe('bookmark.remind.relative.tomorrow')
    expect(bookmarkRemindRelativeKey({ kind: 'inDays', days: 5 })).toBe('bookmark.remind.relative.inDays')
    expect(bookmarkRemindRelativeParams({ kind: 'inDays', days: 5 })).toEqual({ days: 5 })
    expect(bookmarkRemindRelativeParams({ kind: 'today' })).toEqual({})
  })
})

describe('bookmark empty state and errors', () => {
  test('self view invites the user to start bookmarking', () => {
    expect(bookmarkEmptyStateCopy('self')).toEqual({
      title: 'bookmark.empty.selfTitle',
      description: 'bookmark.empty.selfDescription',
      icon: 'i-lucide-bookmark'
    })
  })

  test('public view explains the other user has nothing public', () => {
    expect(bookmarkEmptyStateCopy('public').title).toBe('bookmark.empty.publicTitle')
    expect(bookmarkEmptyStateCopy('public').description).toBe('bookmark.empty.publicDescription')
  })

  test('maps HTTP statuses to copy keys', () => {
    expect(bookmarkErrorKey(401)).toBe('bookmark.error.forbidden')
    expect(bookmarkErrorKey(403)).toBe('bookmark.error.forbidden')
    expect(bookmarkErrorKey(404)).toBe('bookmark.error.notFound')
    expect(bookmarkErrorKey(429)).toBe('bookmark.error.rateLimited')
    expect(bookmarkErrorKey(503)).toBe('bookmark.error.unavailable')
    expect(bookmarkErrorKey(500)).toBe('bookmark.error.unknown')
    expect(bookmarkErrorKey(null)).toBe('bookmark.error.unknown')
  })
})


describe('bookmark remind bounds and validation', () => {
  const now = new Date('2026-01-01T00:00:00.000Z')
  const MINUTE = 60_000
  const YEAR = 365 * 86400000

  test('bounds are now+1 minute and now+729 days (one day of slack under the 730-day server cap)', () => {
    const bounds = bookmarkRemindBounds(now)
    const maxAhead = new Date(bounds.maxInput).getTime() - now.getTime()
    // datetime-local 只到分钟精度，允许一分钟内的取整误差。
    expect(Math.abs(new Date(bounds.minInput).getTime() - (now.getTime() + MINUTE))).toBeLessThanOrEqual(MINUTE)
    expect(Math.abs(maxAhead - 729 * 86400000)).toBeLessThanOrEqual(MINUTE)
    // 余量必须真的留在后端 730 天上限之内，否则跨闰年 + 提交延迟仍会吃 422 too_far。
    expect(maxAhead).toBeLessThan(730 * 86400000)
    expect(bounds.minInput).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    expect(bounds.maxInput).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
  })

  test('past and too-far values are flagged before any request', () => {
    expect(bookmarkRemindIssue('2025-12-31T23:00:00.000Z', now)).toBe('in_past')
    // 不到 1 分钟也算过去：提交后立刻过期没有意义。
    expect(bookmarkRemindIssue(new Date(now.getTime() + 30_000).toISOString(), now)).toBe('in_past')
    expect(bookmarkRemindIssue(new Date(now.getTime() + 5 * MINUTE).toISOString(), now)).toBeNull()
    expect(bookmarkRemindIssue(new Date(now.getTime() + 3 * YEAR).toISOString(), now)).toBe('too_far')
  })

  test('empty and unparseable values are not validation errors (empty means clear)', () => {
    expect(bookmarkRemindIssue(null, now)).toBeNull()
    expect(bookmarkRemindIssue('', now)).toBeNull()
    expect(bookmarkRemindIssue('not-a-date', now)).toBeNull()
  })

  test('issue and reason codes map to their own copy keys', () => {
    expect(bookmarkRemindIssueKey('in_past')).toBe('bookmark.error.inPast')
    expect(bookmarkRemindIssueKey('too_far')).toBe('bookmark.error.tooFar')
    expect(bookmarkRemindIssueKey(null)).toBe('')
    expect(bookmarkReasonKey('forum.bookmark_remind_in_past')).toBe('bookmark.error.inPast')
    expect(bookmarkReasonKey('forum.bookmark_remind_too_far')).toBe('bookmark.error.tooFar')
    expect(bookmarkReasonKey('forum.bookmark_remind_limit')).toBe('bookmark.error.limit')
    expect(bookmarkReasonKey('forum.bookmarks_disabled')).toBe('bookmark.error.unavailable')
    expect(bookmarkReasonKey('forum.bookmark_target_invalid')).toBe('bookmark.error.notFound')
    expect(bookmarkReasonKey('')).toBe('')
    expect(bookmarkReasonKey('forum.something_else')).toBe('')
  })

  test('remindedAt decides 已提醒 vs 将提醒', () => {
    expect(bookmarkRemindStatusKey('2026-01-03T00:00:00.000Z')).toBe('bookmark.remind.status.done')
    expect(bookmarkRemindStatusKey(null)).toBe('bookmark.remind.status.pending')
    expect(bookmarkRemindStatusKey(undefined)).toBe('bookmark.remind.status.pending')
  })
})
