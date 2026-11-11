import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url), 'utf8')
const locale = (name: string) => JSON.parse(source(`i18n/locales/${name}.json`)) as Record<string, unknown>

const bookmarkComposable = () => source('app/composables/forum/useBookmark.ts')
const bookmarkControl = () => source('app/components/forum/SFBookmarkControl.vue')
const bookmarkUtils = () => source('app/utils/forum/forumBookmark.ts')
const profileBookmarks = () => source('app/composables/profile/useProfileBookmarks.ts')
const profileTab = () => source('app/components/profile/SFProfileBookmarkTab.vue')
const profilePage = () => source('app/components/profile/SFProfileShowPage.vue')
const topicBar = () => source('app/components/forum/SFTopicReactionBar.vue')
const topicPage = () => source('app/components/forum/SFTopicShowPage.vue')
const comment = () => source('app/components/forum/SFComment.vue')
const webOptions = () => source('app/composables/useWebOptions.ts')

function flattenKeys(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object') return [prefix]
  return Object.entries(value as Record<string, unknown>)
    .flatMap(([key, child]) => flattenKeys(child, prefix ? `${prefix}.${key}` : key))
}

describe('bookmark write contract', () => {
  test('uses the topic/comment bookmark endpoints and the shared write body', () => {
    const file = bookmarkComposable()
    expect(file).toContain('`/topics/${targetId}/bookmark`')
    expect(file).toContain('`/comments/${targetId}/bookmark`')
    expect(file).toContain("method: 'PUT'")
    expect(file).toContain("method: 'DELETE'")
    // 可见性与提醒共用同一个 PUT：写路径只有一处，三个动作都复用它。
    expect(file.match(/method: 'PUT'/g)?.length).toBe(1)
    expect(file.match(/api\.bookmark\(/g)?.length).toBe(3)
    expect(file).toContain('visibility: visibility.value')
    expect(file).toContain('remindAt: value')
  })

  test('clearing a reminder uses the explicit clearReminder switch', () => {
    const file = bookmarkComposable()
    expect(file).toContain('clearReminder?: boolean')
    expect(file).toContain('{ visibility: visibility.value, clearReminder: true }')
    // 清除成功后本地必须真置空：即便响应回显旧 remindAt。
    expect(file).toContain('{ ...state, remindAt: null, remindedAt: null }')
  })

  test('keeps the response type in one place, mirroring the backend BookmarkState', () => {
    const file = bookmarkComposable()
    expect(file).toContain('export type BookmarkState = {')
    for (const field of ['targetType', 'targetId', 'visibility', 'bookmarked', 'publicBookmarkCount', 'remindAt']) {
      expect(file).toContain(`${field}`)
    }
    expect(file).toContain('export type BookmarkWriteInput = {')
    expect(file).toContain('export type BookmarkList = {')
  })

  test('lists use page/perPage (the Profile controller口径), and the owner scope is server-decided', () => {
    const file = bookmarkComposable()
    expect(file).toContain('`/profile/bookmarks?page=${page}&perPage=${perPage}`')
    expect(file).toContain('`/profiles/${encodeURIComponent(username)}/bookmarks?${query}`')
    expect(profileBookmarks()).toContain('api.listMyBookmarks(nextPage, BOOKMARK_LIST_LIMIT)')
    expect(profileBookmarks()).toContain('api.listUserBookmarks(username.value, nextPage, BOOKMARK_LIST_LIMIT)')
  })
})

describe('bookmark control contract', () => {
  test('hides the whole control when the feature flag is off', () => {
    const control = bookmarkControl()
    expect(control).toContain('<div v-if="props.enabled" class="sf-bookmark"')
    // 关闭时连按钮都不渲染；disabled 只表达请求在途
    expect(control).toContain(':disabled="pending"')
  })

  test('exposes pressed state plus a full text label for keyboard and screen readers', () => {
    const control = bookmarkControl()
    expect(control).toContain(':aria-pressed="bookmarked"')
    expect(control).toContain(':aria-label="actionLabel"')
    expect(control).toContain(':title="actionLabel"')
    expect(control).toContain("i-tabler-bookmark-filled")
    expect(control).toContain("i-lucide-bookmark")
    expect(control).not.toMatch(/[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u)
  })

  test('bounds the custom reminder input and validates before submitting', () => {
    const control = bookmarkControl()
    expect(control).toContain(':min="bounds.minInput"')
    expect(control).toContain(':max="bounds.maxInput"')
    expect(control).toContain('bookmarkRemindIssue(iso)')
    expect(control).toContain("bookmarkRemindIssueKey(customIssue.value)")
    expect(bookmarkUtils()).toContain('export function bookmarkRemindBounds(')
    expect(bookmarkUtils()).toContain('export function bookmarkRemindIssue(')
  })

  test('separates 已提醒 from 将提醒', () => {
    expect(bookmarkUtils()).toContain('export function bookmarkRemindStatusKey(')
    expect(bookmarkControl()).toContain('bookmarkRemindStatusKey(remindedAt.value)')
    expect(profileTab()).toContain('bookmarkRemindStatusKey(entry.remindedAt)')
  })

  test('the reminder chip is owner-only, and its tooltip never leaks a timestamp to visitors', () => {
    const tab = profileTab()
    expect(tab).toContain('v-if="self && (entry.remindedAt || entry.remindAt)"')
    expect(tab).toContain('if (!props.self) {')
  })

  test('offers both visibilities and the reminder presets from the pure util', () => {
    const control = bookmarkControl()
    expect(control).toContain('v-for="option in bookmarkVisibilities"')
    expect(control).toContain('bookmarkVisibilityLabelKey(option)')
    expect(control).toContain('v-for="preset in bookmarkRemindPresets"')
    expect(control).toContain('bookmarkRemindAtFromPreset(preset.id)')
    expect(control).toContain('type="datetime-local"')
    expect(bookmarkUtils()).toContain("export const bookmarkRemindPresets: { id: BookmarkRemindPreset; days: number }[]")
  })

  test('the settings menu trigger keeps an accessible name on every width', () => {
    const control = bookmarkControl()
    expect(control).toContain(':aria-label="t(\'bookmark.controlLabel\')"')
    expect(control).toContain(':title="t(\'bookmark.controlLabel\')"')
    // 窄屏不横向溢出：弹层宽度受视口约束
    expect(control).toContain('width: min(16rem, calc(100vw - 2rem))')
  })
})

describe('bookmark mount points', () => {
  test('topic action row mounts the control and re-emits its change', () => {
    const bar = topicBar()
    expect(bar).toContain("import SFBookmarkControl from '~/components/forum/SFBookmarkControl.vue'")
    expect(bar).toContain('bookmark: [state: BookmarkState]')
    expect(bar).toContain('@change="state => emit(\'bookmark\', state)"')
    expect(bar).toContain(':enabled="props.bookmarksEnabled"')
  })

  test('topic page owns the web option gate and writes the settled state back', () => {
    const page = topicPage()
    expect(page).toMatch(/const \{[^}]*\bbookmarksEnabled\b[^}]*\} = useWebOptions\(\)/)
    expect(page).toContain(':bookmarked="topic.bookmarkedByMe === true"')
    expect(page).toContain(':bookmarks-enabled="bookmarksEnabled"')
    expect(page).toContain('@bookmark="applyBookmarkChange"')
    // 回写逻辑与点赞同住在 useTopicReaction：页面只保留一行解构，避免顶到架构基线上限。
    expect(page).toContain('const { applyReactionChange, applyBookmarkChange } = useTopicReaction(topic)')
    expect(source('app/composables/forum/useTopicReaction.ts'))
      .toContain('topic.value.bookmarkedByMe = state.bookmarked')
  })

  test('comment footer row mounts the control behind the same gate', () => {
    const file = comment()
    expect(file).toContain("import SFBookmarkControl from '~/components/forum/SFBookmarkControl.vue'")
    expect(file).toMatch(/const \{[^}]*\bbookmarksEnabled\b[^}]*\} = useWebOptions\(\)/)
    expect(file).toContain(':enabled="bookmarksEnabled"')
    expect(file).toContain(':bookmarked="comment.bookmarkedByMe === true"')
    const actionsIndex = file.indexOf('class="sf-comment__actions"')
    const bookmarkIndex = file.indexOf('<SFBookmarkControl')
    expect(actionsIndex).toBeGreaterThan(-1)
    expect(bookmarkIndex).toBeGreaterThan(actionsIndex)
  })

  test('the flag resolves from features.bookmarks like reactions', () => {
    expect(webOptions()).toContain("const bookmarksEnabled = computed(() => featureEnabled('features.bookmarks'))")
    expect(webOptions()).toContain('bookmarksEnabled,')
  })
})

describe('profile bookmarks tab', () => {
  test('sits between 回复 and 关于 in the built-in tab order', () => {
    const page = profilePage()
    const tabs = source('app/utils/profile/profileTabs.ts')
    const topics = tabs.indexOf("value: 'topics'")
    const replies = tabs.indexOf("value: 'replies'")
    const bookmarks = tabs.indexOf("value: 'bookmarks'")
    const about = tabs.indexOf("value: 'about'")
    expect(topics).toBeGreaterThan(-1)
    expect(replies).toBeGreaterThan(topics)
    expect(bookmarks).toBeGreaterThan(replies)
    expect(about).toBeGreaterThan(bookmarks)
    expect(tabs).toContain("t('profile.tabs.bookmarks')")
  })

  test('the bookmarks tab only exists while features.bookmarks is on', () => {
    const page = profilePage()
    expect(page).toContain('bookmarksEnabled, badgesEnabled } = useWebOptions()')
    expect(source('app/utils/profile/profileTabs.ts')).toContain('input.bookmarksEnabled ?')
    // 开关翻掉后 tab 从 profileTabs 消失：回落与 query 纠正已抽到 useProfileTabQuery，
    // 页面只负责把当前 tab 值喂给 resetTabIfMissing。
    expect(page).toContain('resetTabIfMissing(tabs.map(tab => tab.value))')
    expect(source('app/composables/profile/useProfileTabQuery.ts')).toContain('activeTab.value = defaultTab')
  })

  test('renders the tab panel with self/public scope and a retry path', () => {
    const page = profilePage()
    expect(page).toContain('<SFProfileBookmarkTab')
    expect(page).toContain(':self="isSelf"')
    expect(page).toContain('@load-more="loadMoreBookmarks"')
    expect(page).toContain('@retry="retryBookmarks"')
    expect(page).toContain('self: () => isSelf.value')
  })

  test('private rows carry a lock marker and the empty state offers a home CTA', () => {
    const tab = profileTab()
    expect(tab).toContain("entry.visibility === 'private'")
    expect(tab).toContain('i-lucide-lock')
    expect(tab).toContain('bookmarkEmptyStateCopy')
    expect(tab).toContain('bookmark.empty.cta')
    expect(tab).toContain('bookmarkTargetTypeLabelKey(entry.targetType)')
    expect(tab).toContain('#comment-${comment.id}')
  })

  test('caches the loaded list across tab switches and only fetches when active', () => {
    const file = profileBookmarks()
    expect(file).toContain('if (!options.active() || loaded.value || loading.value || !username.value)')
    expect(file).toContain('watch(() => options.active()')
    // 混合流按收藏时间倒序
    expect(file).toContain('Date.parse(right.createdAt) - Date.parse(left.createdAt)')
  })
})

describe('bookmark i18n parity', () => {
  test('bookmark keys exist in both locales with the same shape', () => {
    const zh = flattenKeys(locale('zh-CN').bookmark, 'bookmark').sort()
    const en = flattenKeys(locale('en-US').bookmark, 'bookmark').sort()
    expect(zh.length).toBeGreaterThanOrEqual(45)
    expect(en).toEqual(zh)
    for (const key of [
      'bookmark.action.add',
      'bookmark.action.remove',
      'bookmark.visibility.private',
      'bookmark.visibility.public',
      'bookmark.remind.preset.day1',
      'bookmark.remind.preset.day3',
      'bookmark.remind.preset.week1',
      'bookmark.remind.custom',
      'bookmark.remind.relative.today',
      'bookmark.remind.relative.tomorrow',
      'bookmark.remind.relative.inDays',
      'bookmark.remind.relative.due',
      'bookmark.empty.selfTitle',
      'bookmark.empty.publicTitle',
      'bookmark.error.forbidden',
      'bookmark.error.notFound',
      'bookmark.error.rateLimited',
      'bookmark.error.inPast',
      'bookmark.error.tooFar',
      'bookmark.error.limit',
      'bookmark.remind.status.pending',
      'bookmark.remind.status.done',
      'bookmark.profileTab.loadMore',
      'bookmark.profileTab.loadFailed'
    ]) {
      expect(zh).toContain(key)
    }
  })

  test('profile tab label is paired', () => {
    const zhTabs = (locale('zh-CN').profile as Record<string, Record<string, string>>).tabs
    const enTabs = (locale('en-US').profile as Record<string, Record<string, string>>).tabs
    expect(zhTabs.bookmarks).toBe('收藏')
    expect(enTabs.bookmarks).toBe('Bookmarks')
    expect(Object.keys(zhTabs).sort()).toEqual(Object.keys(enTabs).sort())
  })

  test('every bookmark key the surface renders resolves in both locales', () => {
    const surface = [bookmarkControl(), profileTab()].join('\n')
    const referenced = [...surface.matchAll(/t\('(bookmark\.[a-zA-Z.]+)'/g)].map(match => match[1])
    expect(referenced.length).toBeGreaterThan(10)
    const lookup = (tree: Record<string, unknown>, key: string) => key.split('.').reduce<unknown>(
      (node, part) => (node as Record<string, unknown> | undefined)?.[part],
      tree
    )
    for (const key of new Set(referenced)) {
      expect(typeof lookup(locale('zh-CN'), key)).toBe('string')
      expect(typeof lookup(locale('en-US'), key)).toBe('string')
    }
  })
})
