/**
 * 首页右栏问候语的时段分档。
 *
 * 只回答「某个时刻在某时区属于哪一档」：文案由 i18n 决定，时区来源由调用方决定
 * （SSR 用站点时区，挂载后改用访客本地时区）。
 */

export type ForumHomeGreetingBucket = 'morning' | 'noon' | 'afternoon' | 'evening' | 'lateNight'

/**
 * 取指定 IANA 时区的当地小时（0–23）。
 * 时区缺失或非法时回落到运行环境本地小时，保证问候语不会因为配置错误而空白。
 */
export function greetingHour(at: number | Date, timeZone?: string | null): number {
  const date = at instanceof Date ? at : new Date(at)
  const zone = String(timeZone || '').trim()

  if (zone) {
    try {
      const parts = new Intl.DateTimeFormat('en-US', {
        timeZone: zone,
        hour: '2-digit',
        hourCycle: 'h23'
      }).formatToParts(date)
      const hour = Number.parseInt(parts.find(part => part.type === 'hour')?.value ?? '', 10)
      if (Number.isFinite(hour)) {
        // 部分引擎在午夜返回 24（h24 语义），统一折回 0。
        return ((hour % 24) + 24) % 24
      }
    } catch {
      // 非法 IANA 时区：交给下面的本地小时兜底。
    }
  }

  return date.getHours()
}

/** 时段边界：05 早 / 11 中 / 13 下 / 18 晚 / 23 深夜，左闭右开。 */
export function resolveForumHomeGreetingBucket(
  at: number | Date,
  timeZone?: string | null
): ForumHomeGreetingBucket {
  const hour = greetingHour(at, timeZone)

  if (hour >= 5 && hour < 11) return 'morning'
  if (hour >= 11 && hour < 13) return 'noon'
  if (hour >= 13 && hour < 18) return 'afternoon'
  if (hour >= 18 && hour < 23) return 'evening'
  return 'lateNight'
}
