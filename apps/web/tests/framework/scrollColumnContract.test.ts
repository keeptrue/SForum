import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const themeCss = readFileSync(
  new URL('../../app/assets/css/sforum-theme.css', import.meta.url),
  'utf8'
)

describe('center column scroll contract', () => {
  test('keeps children at their intrinsic height inside the scroll column', () => {
    // 桌面端中心列是固定高度的 flex 滚动容器；带 overflow 的子项自动最小尺寸为 0，
    // 缺这条约束会被压扁到 0~2px（账号安全的设备卡片、资料页的分区导航）。
    expect(themeCss).toMatch(/\.sforum-content-column\s*>\s*\*\s*\{[^}]*flex-shrink:\s*0;/s)
  })

  test('declares the column as a flex column', () => {
    expect(themeCss).toMatch(/\.sforum-content-column\s*\{[^}]*display:\s*flex;[^}]*flex-direction:\s*column;/s)
  })
})
