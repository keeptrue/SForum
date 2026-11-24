import { describe, expect, test } from 'bun:test'
import { highlightSegments, tokenizeQuery } from '../../app/utils/forum/searchHighlight'

describe('search highlight segments', () => {
  test('splits a single term case-insensitively', () => {
    expect(highlightSegments('Nuxt 4 迁移清单', ['nuxt'])).toEqual([
      { text: 'Nuxt', hit: true },
      { text: ' 4 迁移清单', hit: false }
    ])
  })

  test('highlights every term of a multi-word query independently', () => {
    const segments = highlightSegments('Nuxt 的 Redis 会话策略', tokenizeQuery('nuxt redis'))
    expect(segments.filter(s => s.hit).map(s => s.text)).toEqual(['Nuxt', 'Redis'])
  })

  test('prefers the longer term so a short prefix cannot split it', () => {
    const segments = highlightSegments('postgres 与 post', ['post', 'postgres'])
    expect(segments.filter(s => s.hit).map(s => s.text)).toEqual(['postgres', 'post'])
  })

  test('returns one plain segment for empty, blank, or stop-word-only queries', () => {
    for (const query of ['', '   ', '的', 'the and', '\t\n']) {
      expect(highlightSegments('站内搜索', tokenizeQuery(query))).toEqual([
        { text: '站内搜索', hit: false }
      ])
    }
  })

  test('empty text yields no segments', () => {
    expect(highlightSegments('', ['nuxt'])).toEqual([])
  })

  test('merges adjacent hits into one segment instead of one mark per character', () => {
    expect(highlightSegments('aaab', ['a'])).toEqual([
      { text: 'aaa', hit: true },
      { text: 'b', hit: false }
    ])
  })

  test('never returns markup: segments carry raw text only', () => {
    const segments = highlightSegments('<img src=x onerror=alert(1)> nuxt', ['nuxt'])
    expect(segments.some(s => s.text.includes('<img'))).toBe(true)
    // 分段渲染由模板插值转义，函数本身不产出任何标签
    expect(segments.every(s => !s.text.includes('<mark'))).toBe(true)
  })
})
