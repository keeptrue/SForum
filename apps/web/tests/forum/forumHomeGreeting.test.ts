import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

import {
  greetingHour,
  resolveForumHomeGreetingBucket,
  type ForumHomeGreetingBucket
} from '../../app/utils/forum/forumHomeGreeting'

const instant = (iso: string) => new Date(iso)
const locale = (file: string) => JSON.parse(
  readFileSync(new URL(`../../i18n/locales/${file}`, import.meta.url), 'utf8')
)

describe('forum home greeting bucket', () => {
  test('maps Asia/Shanghai wall-clock hours to five buckets', () => {
    const cases: Array<[string, ForumHomeGreetingBucket]> = [
      ['2026-10-27T20:59:00.000Z', 'lateNight'], // 04:59
      ['2026-10-27T21:00:00.000Z', 'morning'], // 05:00
      ['2026-10-28T02:59:00.000Z', 'morning'], // 10:59
      ['2026-10-28T03:00:00.000Z', 'noon'], // 11:00
      ['2026-10-28T04:59:00.000Z', 'noon'], // 12:59
      ['2026-10-28T05:00:00.000Z', 'afternoon'], // 13:00
      ['2026-10-28T09:59:00.000Z', 'afternoon'], // 17:59
      ['2026-10-28T10:00:00.000Z', 'evening'], // 18:00
      ['2026-10-28T14:59:00.000Z', 'evening'], // 22:59
      ['2026-10-28T15:00:00.000Z', 'lateNight'], // 23:00
      ['2026-10-28T19:59:00.000Z', 'lateNight'] // 次日 03:59
    ]

    for (const [iso, bucket] of cases) {
      expect(resolveForumHomeGreetingBucket(instant(iso), 'Asia/Shanghai')).toBe(bucket)
    }
  })

  test('reads the wall clock in the given time zone across DST', () => {
    // 纽约 09:00 在冬季（EST, UTC-5）与夏季（EDT, UTC-4）对应不同 UTC 时刻。
    expect(resolveForumHomeGreetingBucket(instant('2026-01-15T14:00:00.000Z'), 'America/New_York')).toBe('morning')
    expect(resolveForumHomeGreetingBucket(instant('2026-07-15T13:00:00.000Z'), 'America/New_York')).toBe('morning')
    // 夏令时切换当天：03-07 09:00 EST 与 03-08 09:00 EDT 都是早上。
    expect(resolveForumHomeGreetingBucket(instant('2026-03-07T14:00:00.000Z'), 'America/New_York')).toBe('morning')
    expect(resolveForumHomeGreetingBucket(instant('2026-03-08T13:00:00.000Z'), 'America/New_York')).toBe('morning')
  })

  test('normalizes midnight to hour 0 instead of 24', () => {
    expect(greetingHour(instant('2026-10-27T16:00:00.000Z'), 'Asia/Shanghai')).toBe(0)
    expect(resolveForumHomeGreetingBucket(instant('2026-10-27T16:00:00.000Z'), 'Asia/Shanghai')).toBe('lateNight')
  })

  test('falls back to the runtime local hour for a missing or invalid time zone', () => {
    const at = instant('2026-10-27T01:30:00.000Z')

    expect(greetingHour(at, 'Not/AZone')).toBe(at.getHours())
    expect(greetingHour(at, '')).toBe(at.getHours())
    expect(greetingHour(at, null)).toBe(at.getHours())
    expect(resolveForumHomeGreetingBucket(at, 'Not/AZone')).toBe(resolveForumHomeGreetingBucket(at))
  })
})

describe('forum home greeting copy', () => {
  const buckets: ForumHomeGreetingBucket[] = ['morning', 'noon', 'afternoon', 'evening', 'lateNight']

  for (const file of ['zh-CN.json', 'en-US.json']) {
    test(`${file} ships every bucket and keeps the neutral fallback`, () => {
      const messages = locale(file)
      const rail = messages.home.rightRail

      for (const bucket of buckets) {
        expect(typeof rail.greeting?.[bucket]).toBe('string')
        expect(rail.greeting[bucket]).toContain('{name}')
      }
      // 缺键兜底文案（useForumHomeGreeting 在 te() 未命中时使用）。
      expect(typeof rail.welcomeUserTitle).toBe('string')
      expect(rail.welcomeUserTitle).toContain('{name}')
    })
  }

  test('no locale pins the greeting to a single time of day', () => {
    expect(locale('zh-CN.json').home.rightRail.welcomeUserTitle).not.toContain('晚上好')
  })
})
