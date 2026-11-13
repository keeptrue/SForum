import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'

/**
 * 全站唯一的展示名解析：displayName -> username -> fallback。
 *
 * 接受任何带 username/displayName 的对象（UserSummary、PublicProfile 等），
 * 回退终点由调用方通过 fallback 表达（评论区用 `#userId`，通知用 i18n unknownAuthor），
 * 本函数不写死任何场景语义，也不做网络兜底——资料补齐是预览卡片的职责。
 */
export type ForumUserRef = {
  username?: string | null
  displayName?: string | null
  avatar?: ForumUserSummary['avatar'] | null
} | undefined | null

export function userDisplayName(user: ForumUserRef, fallback = ''): string {
  const display = user?.displayName?.trim()
  if (display) {
    return display
  }
  const username = user?.username?.trim()
  if (username) {
    return username
  }
  return fallback
}

/**
 * 身份行里那个 `@username` 是否还需要单独出现。
 *
 * 与 `userDisplayName` 是同一件事的两半：`userDisplayName` 决定「认人」的那一行写什么
 * （昵称 → 用户名 → 场景兜底），本函数决定它后面要不要再补一个 `@username`。
 * 两者指向同一个字符串时，多写一遍只是把同一个词重复两次——而这两种情况都很常见：
 * 昵称留空时后端 `validateNicknameInput` 直接把 username 回填成 displayName，
 * 用户也可能真的把昵称设成与 username 完全相同的字符串。
 *
 * 第一个入参必须是**已解析的展示名**（`userDisplayName(...)` 的结果），不是
 * `profile.displayName` 原值：空昵称场景下后者是空串，拿它比较会漏判。
 * 两端 trim 后严格全等；空 username 视为「没有可展示的 handle」。
 */
export function shouldShowUserHandle(
  displayName: string | null | undefined,
  username: string | null | undefined
): boolean {
  const handle = username?.trim() || ''
  if (!handle) {
    return false
  }
  return handle !== (displayName ?? '').trim()
}
