import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url), 'utf8')
const locale = (name: string) => JSON.parse(source(`i18n/locales/${name}.json`)) as Record<string, unknown>

const reactionControl = () => source('app/components/forum/SFReactionControl.vue')
const reactionStack = () => source('app/components/forum/SFReactionStack.vue')
const reactionRoster = () => source('app/components/forum/SFReactionRoster.vue')
const reactionBar = () => source('app/components/forum/SFTopicReactionBar.vue')
const comment = () => source('app/components/forum/SFComment.vue')
const topicPage = () => source('app/components/forum/SFTopicShowPage.vue')
const homeRow = () => source('app/components/forum/SFHomeTopicRow.vue')
const commentCss = () => source('app/assets/css/sforum-comment.css')
const webOptions = () => source('app/composables/useWebOptions.ts')
const authMiddleware = () => source('app/middleware/auth.global.ts')
const topicCss = () => source('app/assets/css/sforum-topic.css')

function flattenKeys(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object') return [prefix]
  return Object.entries(value as Record<string, unknown>)
    .flatMap(([key, child]) => flattenKeys(child, prefix ? `${prefix}.${key}` : key))
}

function keysUnder(tree: Record<string, unknown>, pattern: RegExp): string[] {
  return flattenKeys(tree).filter(key => pattern.test(key)).sort()
}

describe('reaction control contract', () => {
  test('hides the whole control when the feature flag is off', () => {
    const control = reactionControl()
    expect(control).toContain('<div v-if="props.enabled" class="sf-reaction"')
    // 关闭时连按钮都不渲染，disabled 只表达请求在途
    expect(control).toContain(':disabled="pending"')
    expect(control).not.toContain(':disabled="!enabled || pending"')
  })

  test('exposes pressed state and a full text label for keyboard and screen readers', () => {
    const control = reactionControl()
    expect(control).toContain(':aria-pressed="liked"')
    expect(control).toContain(':aria-label="label"')
    expect(control).toContain(':title="label"')
    expect(control).toContain(':aria-busy="pending"')
  })

  test('routes optimistic toggle, rollback, login redirect, and double-click through the pure helpers', () => {
    const control = reactionControl()
    expect(control).toContain('forumReactionCanToggle(props.enabled, pending.value)')
    expect(control).toContain('forumReactionNeedsLogin(Boolean(user.value))')
    expect(control).toContain('forumReactionLoginTarget(route.fullPath)')
    expect(control).toContain('forumReactionRollback(previous)')
    expect(control).toContain('forumReactionFromResponse(state.liked, state.likeCount)')
  })

  test('success toast auto-dismisses while the failure toast stays until dismissed', () => {
    const control = reactionControl()
    expect(control).toContain("color: 'success'")
    expect(control).toContain('duration: 5000')
    expect(control).toContain("color: 'error'")
    expect(control).toContain('duration: 0')
  })

  test('stays inside a wrapping action row at 390px', () => {
    expect(topicCss()).toContain('.sforum-topic-page__actions {')
    expect(topicCss()).toMatch(/\.sforum-topic-page__actions \{[^}]*flex-wrap: wrap;/)
    const styles = reactionControl().split('<style scoped>')[1] || ''
    // 名单用户名允许 nowrap + 省略号；容器与计数不能锁死宽度/换行
    expect(styles).not.toMatch(/\.sf-reaction \{[^}]*white-space:\s*nowrap/)
    expect(styles).not.toMatch(/\.sf-reaction__count \{[^}]*white-space:\s*nowrap/)
    expect(styles).not.toMatch(/\.sf-reaction \{[^}]*width:\s*\d/)
  })
})

describe('reaction mount points', () => {
  test('topic action row mounts the control and forwards the enabled flag', () => {
    const bar = reactionBar()
    expect(bar).toContain("import SFReactionControl from '~/components/forum/SFReactionControl.vue'")
    expect(bar).toContain('<SFReactionControl')
    expect(bar).toContain('target-type="topic"')
    expect(bar).toContain(':target-id="props.topicId"')
    expect(bar).toContain(':count="props.likeCount"')
    expect(bar).toContain(':liked="props.likedByMe"')
    expect(bar).toContain(':enabled="props.reactionsEnabled"')
    // 子控件的 change 必须转发给父层，否则页面状态会停在服务端初值
    expect(bar).toContain(':liked-by="props.likedBy"')
    // change 必须把头像组一起带上，否则父层回写后头像组会弹回服务端初值
    expect(bar).toContain("@change=\"(liked, count, likedBy) => emit('change', liked, count, likedBy)\"")
  })

  test('comment footer action bar mounts the control as its first child', () => {
    const file = comment()
    expect(file).toContain("import SFReactionControl from '~/components/forum/SFReactionControl.vue'")
    expect(file).toMatch(/const \{[^}]*\breactionsEnabled\b[^}]*\} = useWebOptions\(\)/)
    expect(file).toContain('target-type="comment"')
    expect(file).toContain(':target-id="comment.id"')
    expect(file).toContain(':count="comment.likeCount"')
    expect(file).toContain(':liked="comment.likedByMe === true"')
    expect(file).toContain(':enabled="reactionsEnabled"')

    const actionsIndex = file.indexOf('class="sf-comment__actions"')
    const floorIndex = file.indexOf('sf-comment__floor-actions')
    const reactionIndex = file.indexOf('<SFReactionControl')
    expect(actionsIndex).toBeGreaterThan(-1)
    expect(floorIndex).toBeGreaterThan(-1)
    // 点赞在 footer 动作行里，且是动作行的第一个元素
    expect(reactionIndex).toBeGreaterThan(actionsIndex)
    expect(file.slice(actionsIndex).slice(0, 220)).toContain('<SFReactionControl')
    // 头部 meta 组（楼层号 + 移动端更多）不再挂点赞
    expect(file.slice(floorIndex, actionsIndex)).not.toContain('<SFReactionControl')
  })


  test('topic page owns the web option gate and passes it to the action row', () => {
    const page = topicPage()
    expect(page).toMatch(/const \{[^}]*\breactionsEnabled\b[^}]*\} = useWebOptions\(\)/)
    expect(page).toContain('<SFTopicReactionBar')
    expect(page).toContain(':reactions-enabled="reactionsEnabled"')
    expect(page).toContain('@change="applyReactionChange"')
    expect(page).toContain("import { useTopicReaction } from '~/composables/forum/useTopicReaction'")
    expect(page).toMatch(/const \{[^}]*\bapplyReactionChange\b[^}]*\} = useTopicReaction\(topic\)/)
  })
})

describe('reaction feature gate resolves before first paint', () => {
  test('featureEnabled falls back to the recommended default only before options load', () => {
    const options = webOptions()
    expect(options).toContain('function featureEnabled(name: string, fallback = true)')
    expect(options).toContain("const reactionsEnabled = computed(() => featureEnabled('features.reactions'))")
  })

  test('global middleware awaits web options during SSR so the first paint is already resolved', () => {
    const middleware = authMiddleware()
    expect(middleware).toContain('await refreshWebOptions(')
    expect(middleware).toContain('if (!webOptionsLoaded.value)')
  })
})

describe('reaction i18n parity', () => {
  test('forum.reaction keys exist in both locales with the same shape', () => {
    const zh = keysUnder(locale('zh-CN'), /^forum\.reaction\./)
    const en = keysUnder(locale('en-US'), /^forum\.reaction\./)
    expect(zh).toEqual([
      'forum.reaction.empty',
      'forum.reaction.failed',
      'forum.reaction.like',
      'forum.reaction.liked',
      'forum.reaction.loadMore',
      'forum.reaction.retry',
      'forum.reaction.roster',
      'forum.reaction.rosterAria',
      'forum.reaction.rosterFailed',
      'forum.reaction.rosterTotal',
      'forum.reaction.unlike',
      'forum.reaction.unliked',
      'forum.reaction.you'
    ])
    expect(en).toEqual(zh)
  })

  test('notification reaction copy is paired across locales', () => {
    const zh = keysUnder(locale('zh-CN'), /^notifications\..*reaction/)
    const en = keysUnder(locale('en-US'), /^notifications\..*reaction/)
    // filter.reaction 是收件箱类型目录的一项，缺了会在 UI 上漏出原始 key。
    expect(zh).toEqual(['notifications.body.reaction', 'notifications.filter.reaction', 'notifications.types.reaction'])
    expect(en).toEqual(zh)
  })

  test('every reaction key the reaction surface renders is present in both locales', () => {
    const surface = [reactionControl(), reactionStack(), reactionRoster()].join('\n')
    const referenced = [...surface.matchAll(/t\('(forum\.reaction\.[a-zA-Z]+)'\)/g)].map(match => match[1])
    expect(new Set(referenced).size).toBeGreaterThanOrEqual(11)
    for (const key of referenced) {
      const lookup = (tree: Record<string, unknown>) => key.split('.').reduce<unknown>(
        (node, part) => (node as Record<string, unknown> | undefined)?.[part],
        tree
      )
      expect(typeof lookup(locale('zh-CN'))).toBe('string')
      expect(typeof lookup(locale('en-US'))).toBe('string')
    }
  })

  test('list row like count copy exists in both locales', () => {
    const lookup = (tree: Record<string, unknown>) => (tree.home as Record<string, unknown>).feed as Record<string, unknown>
    expect(typeof lookup(locale('zh-CN')).likeCount).toBe('string')
    expect(typeof lookup(locale('en-US')).likeCount).toBe('string')
  })
})

describe('home topic row like count', () => {
  test('renders the heart and the number only when the count is positive', () => {
    const row = homeRow()
    expect(row).toContain('v-if="topic.likeCount > 0"')
    expect(row).toContain(':aria-label="t(\'home.feed.likeCount\', { count: topic.likeCount })"')
    expect(row.match(/i-lucide-heart/g)?.length).toBe(1)
    const block = row.slice(row.indexOf('v-if="topic.likeCount > 0"'))
    expect(block.slice(0, block.indexOf('</span>'))).toContain('{{ topic.likeCount }}')
  })
})

describe('reaction visual contract', () => {
  test('liked state swaps to a filled glyph instead of the mask fill hack', () => {
    const control = reactionControl()
    expect(control).toContain(":name=\"liked ? 'i-tabler-heart-filled' : 'i-lucide-heart'\"")
    expect(control).not.toContain('fill: currentColor')
  })

  test('hit target reaches 38px without touching the type size', () => {
    const control = reactionControl()
    expect(control).toContain('min-width: 2.375rem')
    expect(control).toContain('min-height: 2.375rem')
    expect(control).not.toMatch(/\.sf-reaction__button \{[^}]*font-size/)
  })

  test('roster caps its width and truncates long names', () => {
    const roster = reactionRoster()
    // 320×256 基线；390 视口下留出边距不溢出
    expect(roster).toContain('width: min(320px, calc(100vw - 2rem));')
    expect(roster).toContain('max-height: 256px;')
    expect(roster).toContain('scrollbar-gutter: stable;')
    expect(roster).toMatch(/\.sf-roster__name \{[^}]*text-overflow: ellipsis;/)
    expect(roster).toMatch(/\.sf-roster__name \{[^}]*white-space: nowrap;/)
    // 「（你）」必须在截断 span 之外
    expect(roster).toMatch(/\.sf-roster__you \{[^}]*flex-shrink: 0;/)
  })

  test('list row like count uses the secondary text token', () => {
    expect(homeRow()).toContain('text-[11px] tabular-nums text-[var(--sf-public-text-secondary)]')
  })
})

describe('comment footer reaction contract', () => {
  test('matches sibling action typography and keeps the count fully opaque', () => {
    const css = commentCss()
    expect(css).toContain('.sf-comment__actions .sf-reaction')
    expect(css).toMatch(/\.sf-comment__actions \.sf-reaction\.sf-reaction--sm \{[^}]*font-size: 0\.75rem;/)
    // 透明度只压文字按钮，不再整行压暗点赞计数
    expect(css).toContain('.sf-comment__actions > .sf-comment__action {')
    expect(css).not.toMatch(/\.sf-comment__actions \{\n  margin-top: 8px;\n  opacity: \.72;/)
    expect(css).toMatch(/\.sf-comment__actions \.sf-reaction\.sf-reaction--sm \{[^}]*font-size: 12\.5px;/)
  })

  test('the footer row wraps instead of overflowing on narrow screens', () => {
    expect(commentCss()).toMatch(/\.sf-comment__actions \{[^}]*flex-wrap: wrap;/)
  })
})
