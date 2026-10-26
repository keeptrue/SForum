import { describe, expect, test } from 'bun:test'

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import {
  activityVisible,
  citationTarget,
  citationsVisible,
  commentCitationsPath,
  toolArgumentSummary,
  toolCallsVisible,
  visibleCitations,
  visibleToolCalls,
  type ForumCitation,
  type ForumToolCall
} from '../../app/components/forum/aiActivity'
import { stripHtmlComments } from '../helpers/sourceText'

function source(relative: string): string {
  return readFileSync(fileURLToPath(new URL(relative, import.meta.url)), 'utf8')
}

// 注释里会提到被禁止的写法（说明为什么不用它），断言必须只看真实代码。
// HTML 注释的剥离必须迭代到不动点，否则嵌套构造会重新拼出注释边界。
function code(relative: string): string {
  return stripHtmlComments(
    source(relative)
      .split('\n')
      .filter(line => !line.trimStart().startsWith('//')
        && !line.trimStart().startsWith('*')
        && !line.trimStart().startsWith('/*'))
      .join('\n')
  )
}

function locale(name: string): Record<string, unknown> {
  return JSON.parse(source(`../../i18n/locales/${name}.json`)) as Record<string, unknown>
}

function lookup(catalog: Record<string, unknown>, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => {
    if (!node || typeof node !== 'object') return undefined
    return (node as Record<string, unknown>)[part]
  }, catalog)
}

const activityBlock = source('../../app/components/forum/SFCommentAIActivity.vue')
const helperSource = source('../../app/components/forum/aiActivity.ts')

describe('citation anchors', () => {
  test('a comment citation jumps to that floor', () => {
    expect(citationTarget({ topicId: 42, commentId: 913 }))
      .toEqual({ kind: 'comment', targetId: 913, href: '/t/42#comment-913' })
  })

  test('a topic-level citation links without an anchor', () => {
    // 契约原话：主题级引用省略 commentId，此时链接不带锚点。
    expect(citationTarget({ topicId: 42 }))
      .toEqual({ kind: 'topic', targetId: 42, href: '/t/42' })
  })

  test('a comment without its topic is listed but not linked', () => {
    // 缺话题 id 就无法跨话题定位：给一个猜出来的路径比不给链接更糟。
    const target = citationTarget({ commentId: 913 })
    expect(target.kind).toBe('comment')
    expect(target.targetId).toBe(913)
    expect(target.href).toBe('')
  })

  test('a citation with no usable id has no target at all', () => {
    expect(citationTarget({}).kind).toBe('none')
    expect(citationTarget({ topicId: 0, commentId: 0 }).kind).toBe('none')
    expect(citationTarget({ topicId: -1 }).kind).toBe('none')
    expect(citationTarget({ commentId: 1.5 }).kind).toBe('none')
  })
})

describe('zero citations render nothing', () => {
  test('an empty list is not visible', () => {
    expect(citationsVisible([])).toBe(false)
    expect(visibleCitations([])).toEqual([])
  })

  test('missing or malformed payloads degrade to "no citations", not an error', () => {
    // B4 易错点 1：模型不遵守格式时降级为无引用。前端同理，不因载荷异常而抛错。
    expect(citationsVisible(undefined)).toBe(false)
    expect(citationsVisible(null)).toBe(false)
    expect(visibleCitations(undefined)).toEqual([])
    expect(citationsVisible([{} as ForumCitation])).toBe(false)
  })

  test('unresolvable entries are dropped instead of rendered as dead text', () => {
    expect(visibleCitations([{ excerpt: 'no ids at all' }])).toEqual([])
  })
})

describe('citation rendering shape', () => {
  test('numbers the visible citations in payload order', () => {
    const visible = visibleCitations([
      { topicId: 1, commentId: 2, excerpt: 'a' },
      { topicId: 1, commentId: 3, excerpt: 'b' }
    ])
    expect(visible.map(entry => entry.index)).toEqual([1, 2])
    expect(visible.map(entry => entry.targetId)).toEqual([2, 3])
  })

  test('mixes comment and topic anchors without losing either', () => {
    const visible = visibleCitations([
      { topicId: 7, commentId: 11, excerpt: 'floor' },
      { topicId: 7, excerpt: 'main post' }
    ])
    expect(visible.map(entry => entry.kind)).toEqual(['comment', 'topic'])
    expect(visible[1]?.href).toBe('/t/7')
  })

  test('clips long excerpts without splitting multi-byte characters', () => {
    const long = '引'.repeat(400)
    const entry = visibleCitations([{ topicId: 1, commentId: 2, excerpt: long }])[0]!
    expect(entry.excerpt.endsWith('…')).toBe(true)
    // 240 code points（与后端 MaxCitationExcerptRunes 一致）+ 省略号：没有切开字符。
    expect(Array.from(entry.excerpt)).toHaveLength(241)
    expect(entry.excerpt).not.toContain('\uFFFD')
  })

  test('does not clip an excerpt the server already accepted', () => {
    // 后端上限就是 240 rune。前端再截更短会静默丢弃服务端的真实内容。
    const atLimit = '依'.repeat(240)
    const entry = visibleCitations([{ topicId: 1, commentId: 2, excerpt: atLimit }])[0]!
    expect(entry.excerpt).toBe(atLimit)
    expect(entry.excerpt).not.toContain('…')
  })

  test('trims whitespace-only excerpts to nothing', () => {
    const entry = visibleCitations([{ topicId: 1, commentId: 2, excerpt: '   ' }])[0]!
    expect(entry.excerpt).toBe('')
  })
})

describe('tool call projection', () => {
  test('zero tool calls are not visible', () => {
    expect(toolCallsVisible([])).toBe(false)
    expect(toolCallsVisible(undefined)).toBe(false)
    expect(toolCallsVisible(null)).toBe(false)
    expect(visibleToolCalls([])).toEqual([])
  })

  test('unnamed entries are dropped: a call with no tool explains nothing', () => {
    expect(visibleToolCalls([{ arguments: '{"query":"x"}' } as ForumToolCall])).toEqual([])
    expect(visibleToolCalls([{ tool: '   ' }])).toEqual([])
  })

  test('keeps the payload order and numbers from 1', () => {
    const visible = visibleToolCalls([
      { tool: 'web-search', arguments: '{"query":"SForum"}', latencyMs: 120 },
      { tool: 'forum-search', arguments: '{"query":"SForum"}', isError: true }
    ])
    expect(visible.map(entry => entry.tool)).toEqual(['web-search', 'forum-search'])
    expect(visible.map(entry => entry.index)).toEqual([1, 2])
    expect(visible[1]?.isError).toBe(true)
  })

  test('negative or missing latency degrades to 0 instead of rendering garbage', () => {
    const visible = visibleToolCalls([{ tool: 'web-search', latencyMs: -5 }, { tool: 'x' }])
    expect(visible.map(entry => entry.latencyMs)).toEqual([0, 0])
  })

  test('the summary prefers the query field of a JSON argument payload', () => {
    expect(toolArgumentSummary('{"query":"SForum 是什么"}')).toBe('SForum 是什么')
    expect(toolArgumentSummary('{"query":"  "}')).toBe('{"query":"  "}')
  })

  test('a fetch tool argument shows the url, not the raw JSON', () => {
    // 公开面上出现 {"url": "..."} 是机器味道；读者只想知道它去读了哪一页。
    expect(toolArgumentSummary('{"url":"https://github.com/keeptrue/SForum/releases"}'))
      .toBe('https://github.com/keeptrue/SForum/releases')
    // query 仍然优先于 url（同一条调用里两者都有时，检索词才是「在做什么」）。
    expect(toolArgumentSummary('{"query":"SForum","url":"https://example.com"}')).toBe('SForum')
  })

  test('non-JSON arguments are shown as-is, never swallowed', () => {
    expect(toolArgumentSummary('SForum 是什么论坛程序')).toBe('SForum 是什么论坛程序')
    expect(toolArgumentSummary('')).toBe('')
    expect(toolArgumentSummary(undefined)).toBe('')
  })

  test('a long argument summary is clipped without splitting characters', () => {
    const summary = toolArgumentSummary('检'.repeat(200))
    expect(summary.endsWith('…')).toBe(true)
    expect(Array.from(summary)).toHaveLength(81)
    expect(summary).not.toContain('\uFFFD')
  })
})

describe('the block appears only when there is something to show', () => {
  test('either partition makes the block visible', () => {
    expect(activityVisible([], [])).toBe(false)
    expect(activityVisible([{ topicId: 1, commentId: 2 }], [])).toBe(true)
    expect(activityVisible([], [{ tool: 'web-search' }])).toBe(true)
    expect(activityVisible(undefined, undefined)).toBe(false)
  })
})

describe('citation block contract discipline', () => {
  test('the skeleton reads the three documented fields and nothing invented', () => {
    // 契约来源是 OpenAPI CommentCitation。形状变化时先改 aiActivity.ts。
    const typeBlock = helperSource.slice(
      helperSource.indexOf('export type ForumCitation'),
      helperSource.indexOf('}', helperSource.indexOf('export type ForumCitation'))
    )
    expect(typeBlock).toContain('topicId')
    expect(typeBlock).toContain('commentId')
    expect(typeBlock).toContain('excerpt')
    // 没有凭空的第四、第五个字段。
    expect(typeBlock.match(/^\s{2}\w+\??:/gm)?.length).toBe(3)
  })

  test('the tool call projection carries no result digest', () => {
    // resultDigest 是工具返回给模型的正文片段：服务端不投影它，前端也不能自己造。
    const typeBlock = helperSource.slice(
      helperSource.indexOf('export type ForumToolCall'),
      helperSource.indexOf('}', helperSource.indexOf('export type ForumToolCall'))
    )
    expect(typeBlock).not.toContain('resultDigest')
    expect(typeBlock).not.toContain('result')
    expect(typeBlock.match(/^\s{2}\w+\??:/gm)?.length).toBe(4)
  })

  test('the block renders only once a fetch proves there is activity', () => {
    // 零结果不渲染，且**未知也不渲染**：浏览器 QA 实测过旧写法（未取数时先渲染
    // 收起入口）的真实后果——一页 5 条普通评论就挂了 5 个「依据」标记，暗示
    // 人人都有出处。判据只能是「取回非空」。
    expect(activityBlock).toContain('const showBlock = computed(() => activityVisible(items.value, toolCalls.value))')
    expect(activityBlock).toContain('v-if="showBlock"')
    expect(activityBlock).toContain('activityVisible(items.value, toolCalls.value)')
    // 反向控制：不允许再出现「未取数就渲染」的判据。
    expect(activityBlock).not.toContain('!settled.value ||')
  })

  test('one request per comment serves both partitions', () => {
    // 契约是单条评论粒度、无批量端点：请求数必然等于评论数，所以真正能控的是
    // 并发而不是次数——共享 limiter 让一页 20 条最多 4 个在飞。工具调用与依据
    // 共用同一个请求，这也正是不新增 /tool-calls 路由的理由。
    expect(activityBlock).toContain('createCitationFetcher()')
    expect(activityBlock).toContain('fetcher.run(loadOnce)')
    expect(activityBlock).toContain('if (settled.value || pending.value) return')
    expect(activityBlock).toContain('commentCitationsPath(id)')
    expect(activityBlock.match(/commentCitationsPath\(id\)/g)?.length).toBe(1)
    // 只取一次：settled 之后不再重复请求（展开/收起不再触发取数）。
    expect(helperSource).toContain('export const citationsConcurrency = 4')
    expect(helperSource).toContain('createCitationFetcher')
  })

  test('a 404 or network failure is just "no activity", never an error surface', () => {
    // 契约刻意让「不存在」与「不可见」不可区分（防 commentID 枚举）。
    expect(activityBlock).toContain('} catch {')
    const body = code('../../app/components/forum/SFCommentAIActivity.vue')
    expect(body).not.toContain('无权限')
    expect(body).not.toMatch(/forbidden/i)
  })

  test('excerpt and arguments are rendered as text, never as HTML', () => {
    // excerpt 是任意用户写下的原始正文、arguments 是模型输出：
    // v-html 等于在这个块里开一个存储型 XSS 面。
    expect(activityBlock).toContain('{{ entry.excerpt }}')
    expect(activityBlock).toContain('{{ entry.argumentSummary }}')
    const body = code('../../app/components/forum/SFCommentAIActivity.vue')
    expect(body).not.toContain('v-html')
    expect(body).not.toContain('innerHTML')
    // 保留换行用 pre-wrap，而不是退回 HTML 渲染。
    expect(activityBlock).toContain('white-space: pre-wrap')
  })

  test('links to a real floor or shows plain text, never a fake link', () => {
    expect(activityBlock).toContain('v-if="entry.href"')
    expect(activityBlock).toContain(':to="entry.href"')
    expect(activityBlock).toContain('sf-comment-ai-activity__target--plain')
  })

  test('is a disclosure, not a headline: collapsed by default', () => {
    expect(activityBlock).toContain('<details')
    expect(activityBlock).toContain(':open="open"')
    expect(activityBlock).toContain('open: false')
    // 新控件必须有可读的折叠标题（aria-label 与 title 同源，避免两者分叉）。
    expect(activityBlock).toContain(':aria-label="summaryLabel"')
    expect(activityBlock).toContain(':title="summaryLabel"')
  })

  test('is mounted on the comment renderer so it can actually be reached', () => {
    // 组件 + 逻辑 + 测试齐全但零调用点 = 端到端不成立，用户永远看不到活动块。
    const comment = source('../../app/components/forum/SFComment.vue')
    expect(comment).toContain("import SFCommentAIActivity from '~/components/forum/SFCommentAIActivity.vue'")
    expect(comment).toContain('<SFCommentAIActivity')
    expect(comment).toContain(':comment-id="commentNode.id"')
    // 删除态没有正文可依据，不显示。
    expect(comment).toContain('v-if="commentNode && !isDeletedComment"')
  })

  test('builds the endpoint path without paging parameters', () => {
    // 契约没有分页与 limit：传了也会被忽略，所以不构造它们。
    expect(commentCitationsPath(913)).toBe('/comments/913/citations')
    expect(commentCitationsPath(1)).not.toContain('limit')
    expect(commentCitationsPath(1)).not.toContain('perPage')
  })
})

describe('bilingual copy', () => {
  test('ships every key in both locales under the same names', () => {
    const zh = locale('zh-CN')
    const en = locale('en-US')
    const keys = [
      'topicDetail.citations.title',
      'topicDetail.citations.commentLabel',
      'topicDetail.citations.topicLabel',
      'topicDetail.citations.jumpToComment',
      'topicDetail.citations.jumpToTopic',
      'topicDetail.aiActivity.summary',
      'topicDetail.aiActivity.loading',
      'topicDetail.aiActivity.toolCallsTitle',
      'topicDetail.aiActivity.both',
      'topicDetail.aiActivity.failed',
      'topicDetail.aiActivity.latency'
    ]
    for (const key of keys) {
      const zhValue = lookup(zh, key)
      const enValue = lookup(en, key)
      expect(typeof zhValue).toBe('string')
      expect(typeof enValue).toBe('string')
      expect((zhValue as string).trim().length).toBeGreaterThan(0)
      expect((enValue as string).trim().length).toBeGreaterThan(0)
    }
  })

  test('uses no emoji as iconography', () => {
    const emoji = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u
    for (const name of ['zh-CN', 'en-US']) {
      const block = JSON.stringify(lookup(locale(name), 'topicDetail.citations'))
      const activity = JSON.stringify(lookup(locale(name), 'topicDetail.aiActivity'))
      expect(emoji.test(block)).toBe(false)
      expect(emoji.test(activity)).toBe(false)
    }
  })
})
