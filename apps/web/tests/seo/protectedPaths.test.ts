import { describe, expect, test } from 'bun:test'
import { isProtectedSEOPath } from '../../server/utils/seoProtectedPaths'

const ADMIN_PREFIX = '/control-panel'

describe('isProtectedSEOPath', () => {
  test('protects the session-owned inbox and account settings', () => {
    expect(isProtectedSEOPath('/notifications', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/notifications/123', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/settings', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/settings/profile', ADMIN_PREFIX)).toBe(true)
  })

  test('leaves public content indexable', () => {
    for (const path of ['/', '/t/1', '/u/1', '/c/general', '/notificationsfoo', '/settingsfoo']) {
      expect(isProtectedSEOPath(path, ADMIN_PREFIX)).toBe(false)
    }
  })

  test('matches the admin prefix on segment boundaries only', () => {
    expect(isProtectedSEOPath('/control-panel', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/control-panel/settings/mail', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/control-panelx', ADMIN_PREFIX)).toBe(false)
    // 前缀由环境变量传入，换一个前缀就必须跟着变，不能写死。
    expect(isProtectedSEOPath('/admin', '/admin')).toBe(true)
    expect(isProtectedSEOPath('/control-panel', '/admin')).toBe(false)
  })

  test('keeps the pre-existing protected surfaces', () => {
    expect(isProtectedSEOPath('/api/v1/notifications', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/login', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/register/confirm', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/components', ADMIN_PREFIX)).toBe(true)
    expect(isProtectedSEOPath('/components/buttons', ADMIN_PREFIX)).toBe(true)
  })
})
