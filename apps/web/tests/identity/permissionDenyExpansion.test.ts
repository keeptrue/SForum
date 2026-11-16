import { describe, expect, test } from 'bun:test'
import { ref, type Ref } from 'vue'

import { usePermissions, hasPermission } from '~/composables/identity/usePermissions'
import { useAuthSession, type CurrentUser } from '~/composables/identity/useAuthSession'
import { findAdminPageDefinition, shouldShowAdminPageInNav } from '~/config/adminModules'

/**
 * F-1 契约：前端 `can()` 只查后端下发的「已展开、已扣除」权限集。
 *
 * 会话不下发 denied 集合，前端没有判断「某个子权限被 deny」的信息。只要 `can()`
 * 自己按父权限展开，管理员 deny 掉的子权限就会在菜单里重新出现、点进去 API 403。
 * 因此这里的用例分两条路径：
 *
 * - allowed：后端把父权限展开进集合（`['settings.manage', 'settings.site.manage', ...]`），
 *   `can(子权限)` 必须为 true —— 父权限展开的语义归后端，前端只消费结果。
 * - denied：后端在展开时跳过被 deny 的子权限，集合里没有那个键，
 *   `can(子权限)` 必须为 false，侧栏也不渲染该入口。
 *
 * 用例断言的是真实导出：`usePermissions().can`、`useAuthSession().can`（admin shell
 * 侧栏用的就是这一个）与 `shouldShowAdminPageInNav`（`layouts/admin.vue` 的
 * `buildNavigationItem` 返回 null 的唯一门）。
 */

const SETTINGS_PARENT = 'settings.manage'
const SETTINGS_SITE = 'settings.site.manage'
const SETTINGS_MAIL = 'settings.mail.manage'
const SETTINGS_NOTIFICATIONS = 'settings.notifications.manage'
const SETTINGS_CHILDREN = [
  SETTINGS_SITE,
  SETTINGS_MAIL,
  SETTINGS_NOTIFICATIONS,
  'settings.avatar.manage',
  'settings.appearance.manage',
  'forum.settings.manage'
]

/**
 * 建模「后端下发什么」，不是前端判定逻辑：API 侧 `ExpandEffectivePermissions`
 * 保留父权限键、展开子权限，并跳过被显式 deny 的子权限。
 */
function sessionPermissions(denied: readonly string[] = []): string[] {
  return [SETTINGS_PARENT, ...SETTINGS_CHILDREN.filter(child => !denied.includes(child))]
}

function user(overrides: Partial<CurrentUser> = {}): CurrentUser {
  return {
    id: 7,
    username: 'operator',
    displayName: 'Operator',
    locale: 'zh-CN',
    appearance: null,
    status: 'active',
    emailVerified: true,
    isInitialSuperAdmin: false,
    avatar: { kind: 'initials', url: '', alt: 'Operator' },
    roleKeys: ['operator'],
    permissions: [],
    ...overrides
  }
}

/**
 * 只桩掉两个 Nuxt 自动导入边界（`useState` / `useApiClient`），其余全是生产代码：
 * `useAuthSession` 读的是 `useState('auth:user')` 这一格，把这一格换成用例可改的
 * ref 就能控制当前账号。
 */
function withSession(current: Ref<CurrentUser | null>, run: () => void) {
  const globals = globalThis as Record<string, unknown>
  const originalState = globals.useState
  const originalApiClient = globals.useApiClient

  globals.useState = <T>(key: string, init: () => T) => (
    key === 'auth:user' ? current as unknown as Ref<T> : ref(init())
  )
  globals.useApiClient = () => ({
    request: async () => {
      throw new Error('permission checks must never reach the API')
    }
  })

  try {
    run()
  } finally {
    globals.useState = originalState
    globals.useApiClient = originalApiClient
  }
}

type Checkers = {
  can: (permission: string) => boolean
  sessionCan: (permission: string) => boolean
}

function checkers(current: Ref<CurrentUser | null>): Checkers {
  let captured: Checkers | undefined
  withSession(current, () => {
    captured = {
      can: usePermissions().can,
      sessionCan: useAuthSession().can
    }
  })
  if (!captured) throw new Error('failed to build permission checkers')
  return captured
}

/** 与 `layouts/admin.vue` 的 `buildNavigationItem` 同一道门：返回 false 即不渲染。 */
function navEntryVisible(can: (permission: string) => boolean, pageId: string): boolean {
  const page = findAdminPageDefinition(pageId)
  if (!page) throw new Error(`unknown admin page: ${pageId}`)
  return shouldShowAdminPageInNav(page, can, { professionalMode: true, operationsMode: true })
}

describe('can() · allowed 路径（父权限展开由后端完成）', () => {
  test('后端展开后的子权限键直接命中', () => {
    const { can, sessionCan } = checkers(ref(user({ permissions: sessionPermissions() })))

    expect(can(SETTINGS_SITE)).toBe(true)
    expect(can(SETTINGS_MAIL)).toBe(true)
    expect(can(SETTINGS_NOTIFICATIONS)).toBe(true)
    expect(sessionCan(SETTINGS_SITE)).toBe(true)
    expect(hasPermission(user({ permissions: sessionPermissions() }), SETTINGS_SITE)).toBe(true)
  })

  test('未 deny 时侧栏渲染对应入口', () => {
    const { can, sessionCan } = checkers(ref(user({ permissions: sessionPermissions() })))

    expect(navEntryVisible(can, '/settings')).toBe(true)
    expect(navEntryVisible(can, '/settings/mail')).toBe(true)
    expect(navEntryVisible(sessionCan, '/settings')).toBe(true)
  })
})

describe('can() · denied 路径（显式 deny 压过父权限展开）', () => {
  test('deny 子权限后 can(child) 为 false', () => {
    const { can, sessionCan } = checkers(ref(user({ permissions: sessionPermissions([SETTINGS_SITE]) })))

    expect(can(SETTINGS_SITE)).toBe(false)
    expect(sessionCan(SETTINGS_SITE)).toBe(false)
    expect(hasPermission(user({ permissions: sessionPermissions([SETTINGS_SITE]) }), SETTINGS_SITE)).toBe(false)
    // 同批未被 deny 的兄弟权限不受影响。
    expect(can(SETTINGS_MAIL)).toBe(true)
  })

  test('被 deny 的入口不出现在侧栏', () => {
    const { can, sessionCan } = checkers(ref(user({ permissions: sessionPermissions([SETTINGS_SITE]) })))

    expect(navEntryVisible(can, '/settings')).toBe(false)
    expect(navEntryVisible(sessionCan, '/settings')).toBe(false)
  })

  test('any 模式下把该页所有候选权限都 deny 后入口同样消失', () => {
    const denied = [SETTINGS_MAIL, SETTINGS_NOTIFICATIONS]
    const { can } = checkers(ref(user({ permissions: sessionPermissions(denied) })))

    expect(can(SETTINGS_MAIL)).toBe(false)
    expect(can(SETTINGS_NOTIFICATIONS)).toBe(false)
    expect(navEntryVisible(can, '/settings/mail')).toBe(false)
  })
})

describe('can() · 不再自行展开父权限（F-1 回归门）', () => {
  test('会话只有父权限键时子权限一律为 false', () => {
    const { can, sessionCan } = checkers(ref(user({ permissions: [SETTINGS_PARENT] })))

    // 旧实现会在这里凭父权限展开返回 true —— 而会话没有 denied 信息，
    // 那个 true 无法区分「真的被授予」和「管理员刚刚 deny 掉」。
    expect(can(SETTINGS_SITE)).toBe(false)
    expect(can(SETTINGS_MAIL)).toBe(false)
    expect(sessionCan(SETTINGS_SITE)).toBe(false)
    expect(navEntryVisible(can, '/settings')).toBe(false)
    // 父权限键本身仍在集合里，父键查询照常为 true。
    expect(can(SETTINGS_PARENT)).toBe(true)
  })
})

describe('can() · 身份边界', () => {
  test('super_admin 绕过权限集', () => {
    const { can, sessionCan } = checkers(ref(user({ roleKeys: ['super_admin'], permissions: [] })))

    expect(can(SETTINGS_SITE)).toBe(true)
    expect(sessionCan(SETTINGS_SITE)).toBe(true)
  })

  test('未登录一律为 false', () => {
    const { can, sessionCan } = checkers(ref(null))

    expect(can(SETTINGS_SITE)).toBe(false)
    expect(sessionCan(SETTINGS_SITE)).toBe(false)
    expect(hasPermission(null, SETTINGS_SITE)).toBe(false)
  })
})
