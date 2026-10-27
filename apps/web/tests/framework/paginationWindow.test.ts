import { describe, expect, test } from 'bun:test'

import { buildPaginationWindow } from '../../app/utils/pagination/window'

describe('pagination window', () => {
  test('keeps every page when the list is short', () => {
    expect(buildPaginationWindow(1, 1)).toEqual([1])
    expect(buildPaginationWindow(1, 2)).toEqual([1, 2])
    // 回归：4 页第 1 页曾渲染成 1 2 … 4，第 3 页被藏进省略号。
    expect(buildPaginationWindow(1, 4)).toEqual([1, 2, 3, 4])
    expect(buildPaginationWindow(3, 4)).toEqual([1, 2, 3, 4])
    expect(buildPaginationWindow(1, 7)).toEqual([1, 2, 3, 4, 5, 6, 7])
  })

  test('only collapses a gap of at least two pages', () => {
    expect(buildPaginationWindow(1, 8)).toEqual([1, 2, 3, 4, 5, 'ellipsis-end', 8])
    expect(buildPaginationWindow(4, 8)).toEqual([1, 2, 3, 4, 5, 'ellipsis-end', 8])
    expect(buildPaginationWindow(8, 8)).toEqual([1, 'ellipsis-start', 4, 5, 6, 7, 8])
    expect(buildPaginationWindow(10, 20)).toEqual([1, 'ellipsis-start', 9, 10, 11, 'ellipsis-end', 20])
    expect(buildPaginationWindow(20, 20)).toEqual([1, 'ellipsis-start', 16, 17, 18, 19, 20])
  })

  test('clamps out-of-range input instead of dropping pages', () => {
    expect(buildPaginationWindow(0, 4)).toEqual([1, 2, 3, 4])
    expect(buildPaginationWindow(-3, 4)).toEqual([1, 2, 3, 4])
    expect(buildPaginationWindow(99, 4)).toEqual([1, 2, 3, 4])
    expect(buildPaginationWindow(1, 0)).toEqual([1])
    expect(buildPaginationWindow(Number.NaN, Number.NaN)).toEqual([1])
  })

  test('honours siblingCount', () => {
    expect(buildPaginationWindow(5, 10, 0)).toEqual([1, 'ellipsis-start', 5, 'ellipsis-end', 10])
    expect(buildPaginationWindow(5, 10, 2)).toEqual([1, 2, 3, 4, 5, 6, 7, 'ellipsis-end', 10])
    expect(buildPaginationWindow(6, 9, 1)).toEqual([1, 'ellipsis-start', 5, 6, 7, 8, 9])
    expect(buildPaginationWindow(5, 10, -1)).toEqual([1, 'ellipsis-start', 5, 'ellipsis-end', 10])
  })

  test('never hides exactly one page behind an ellipsis', () => {
    for (let total = 1; total <= 40; total += 1) {
      for (let current = 1; current <= total; current += 1) {
        const items = buildPaginationWindow(current, total)
        const numbers = items.filter((item): item is number => typeof item === 'number')

        expect(numbers[0]).toBe(1)
        expect(numbers[numbers.length - 1]).toBe(total)
        for (let index = 1; index < numbers.length; index += 1) {
          const gap = numbers[index]! - numbers[index - 1]!
          expect(gap).toBeGreaterThan(0)
          // gap === 2 意味着中间那页可点，却被省略号吃掉了。
          expect(gap).not.toBe(2)
        }
        // 每个缺口正好对应一个省略号占位。
        const gaps = numbers.filter((value, index) => index > 0 && value - numbers[index - 1]! > 1).length
        const ellipses = items.filter(item => typeof item !== 'number').length
        expect(ellipses).toBe(gaps)
      }
    }
  })
})
