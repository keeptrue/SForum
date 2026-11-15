import { describe, expect, test } from 'bun:test'
import { radioGroupNextIndex, radioGroupTabIndex } from '../../app/utils/a11y/rovingRadioGroup'

/**
 * radiogroup 的 roving tabindex（RV-21）。
 *
 * 钉住两件事：整组只有一个 tab 停靠点；方向键循环、Home/End 到两端。
 */

const values = ['pine_teal', 'ocean_blue', 'violet', 'rose', 'amber', 'custom'] as const

describe('radioGroupTabIndex', () => {
  test('只有选中项是 0，其余都是 -1', () => {
    expect(values.map(value => radioGroupTabIndex(values, 'violet', value)))
      .toEqual([-1, -1, 0, -1, -1, -1])
  })

  test('自定义配色也是组内一员，选中后它就是唯一停靠点', () => {
    expect(values.map(value => radioGroupTabIndex(values, 'custom', value)))
      .toEqual([-1, -1, -1, -1, -1, 0])
  })

  test('没有选中项（或选中项不在组内）时退回第一项', () => {
    expect(radioGroupTabIndex(values, null, 'pine_teal')).toBe(0)
    expect(radioGroupTabIndex(values, 'not-a-theme', 'pine_teal')).toBe(0)
    expect(radioGroupTabIndex([], null, 'anything')).toBe(-1)
  })
})

describe('radioGroupNextIndex', () => {
  test('方向键左右上下循环移动', () => {
    expect(radioGroupNextIndex(6, 0, 'ArrowRight')).toBe(1)
    expect(radioGroupNextIndex(6, 0, 'ArrowDown')).toBe(1)
    expect(radioGroupNextIndex(6, 5, 'ArrowRight')).toBe(0)
    expect(radioGroupNextIndex(6, 0, 'ArrowLeft')).toBe(5)
    expect(radioGroupNextIndex(6, 0, 'ArrowUp')).toBe(5)
    expect(radioGroupNextIndex(6, 3, 'ArrowLeft')).toBe(2)
  })

  test('Home/End 到两端，其它键不处理', () => {
    expect(radioGroupNextIndex(6, 3, 'Home')).toBe(0)
    expect(radioGroupNextIndex(6, 3, 'End')).toBe(5)
    expect(radioGroupNextIndex(6, 3, 'Enter')).toBeNull()
    expect(radioGroupNextIndex(6, 3, ' ')).toBeNull()
    expect(radioGroupNextIndex(0, 0, 'ArrowRight')).toBeNull()
  })
})
