import { describe, expect, test } from 'bun:test'

import {
  resolveTrustedSettingsFirstPaint,
  type TrustedSettingsFirstPaintState
} from '../../app/components/extensions/settings/firstPaint'

const settingsSource = await Bun.file(
  new URL('../../app/components/extensions/settings/SFTrustedSettingsComponent.vue', import.meta.url)
).text()

function state(overrides: Partial<TrustedSettingsFirstPaintState> = {}): TrustedSettingsFirstPaintState {
  return {
    mounted: false,
    failed: false,
    forcedFallback: false,
    trustResolved: false,
    hasStatus: false,
    trusted: false,
    hasComponent: false,
    ...overrides
  }
}

/** 已授权、组件描述符齐全、尚未挂载：修复前正是这一格画出整张 Schema 表单。 */
const trustedReady: Partial<TrustedSettingsFirstPaintState> = {
  trustResolved: true,
  hasStatus: true,
  trusted: true,
  hasComponent: true
}

describe('trusted settings first paint', () => {
  test('trust unresolved: skeleton only, never the Schema form', () => {
    const paint = resolveTrustedSettingsFirstPaint(state())
    expect(paint.showSkeleton).toBe(true)
    expect(paint.showSchemaFallback).toBe(false)
  })

  test('authorized but not mounted yet: still skeleton, not the Schema form', () => {
    // 回归点：信任结论由 page-bootstrap 同步带回，未挂载窗口必须继续给骨架。
    const paint = resolveTrustedSettingsFirstPaint(state(trustedReady))
    expect(paint.showSkeleton).toBe(true)
    expect(paint.showSchemaFallback).toBe(false)
  })

  test('mounted: neither skeleton nor Schema form', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({ ...trustedReady, mounted: true }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(false)
  })

  test('load failure: alert plus Schema form, no skeleton', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({ ...trustedReady, failed: true }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('forced Schema fallback: no skeleton', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({
      ...trustedReady,
      forcedFallback: true,
      trusted: false
    }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('status read failure stays distinct from denial and shows the Schema form', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({
      trustResolved: true,
      hasStatus: false,
      trusted: false,
      hasComponent: false
    }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('denied trust: Schema form, no skeleton', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({
      trustResolved: true,
      hasStatus: true,
      trusted: false,
      hasComponent: true
    }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('missing component descriptor: Schema form, no skeleton', () => {
    const paint = resolveTrustedSettingsFirstPaint(state({
      trustResolved: true,
      hasStatus: true,
      trusted: true,
      hasComponent: false
    }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('descriptor without a digest counts as missing: Schema form, not a stuck skeleton', () => {
    // performLoad 在 !digest.value 时直接早退，装上不去。hasComponent 若只认描述符，
    // 页面就会永远停在骨架、连失败告警都不出现。契约是「描述符 + digest 都齐才算有组件」。
    expect(settingsSource).toContain('hasComponent: Boolean(component.value && digest.value)')
    expect(settingsSource).toContain('!component.value || !digest.value')
    const paint = resolveTrustedSettingsFirstPaint(state({
      trustResolved: true,
      hasStatus: true,
      trusted: true,
      hasComponent: false
    }))
    expect(paint.showSkeleton).toBe(false)
    expect(paint.showSchemaFallback).toBe(true)
  })

  test('the two branches are mutually exclusive in every combination', () => {
    const flags: Array<keyof TrustedSettingsFirstPaintState> = [
      'mounted',
      'failed',
      'forcedFallback',
      'trustResolved',
      'hasStatus',
      'trusted',
      'hasComponent'
    ]
    for (let mask = 0; mask < 1 << flags.length; mask += 1) {
      const input = state()
      flags.forEach((flag, index) => {
        input[flag] = ((mask >> index) & 1) === 1
      })
      const paint = resolveTrustedSettingsFirstPaint(input)
      expect(paint.showSkeleton && paint.showSchemaFallback).toBe(false)
      // 骨架只在「还没挂载、没失败、没被强制回退」时才可能出现。
      if (paint.showSkeleton) {
        expect(input.mounted).toBe(false)
        expect(input.failed).toBe(false)
        expect(input.forcedFallback).toBe(false)
      }
      // 信任未读到 → 永不提前渲染 Schema 表单（否则就是表单→组件的二次闪替）。
      if (!input.trustResolved) {
        expect(paint.showSchemaFallback).toBe(false)
      }
      // 已挂载 → 两个首帧分支都不再参与渲染。
      if (input.mounted) {
        expect(paint.showSkeleton).toBe(false)
        expect(paint.showSchemaFallback).toBe(false)
      }
    }
  })
})
