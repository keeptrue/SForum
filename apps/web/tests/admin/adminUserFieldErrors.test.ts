import { describe, expect, test } from 'bun:test'
import {
  adminUserErrorFieldNames,
  firstAdminUserErrorField,
  resolveAdminUserError
} from '../../app/utils/admin/adminUserFieldErrors'

const copy = {
  fallback: '保存失败，请稍后重试。',
  fieldsSummary: (fields: Record<string, string[]>) => `点名:${Object.keys(fields).join(',')}`,
  noChanges: '没有检测到改动，请先修改再保存。'
}

describe('admin user drawer errors', () => {
  test('keeps backend field messages and collapses the summary', () => {
    const error = {
      response: {
        _data: {
          code: 422,
          message: '用户更新内容不正确，请检查账户和资料字段后重试。',
          data: {
            reason: 'user.invalid_update',
            fields: { email: ['邮箱格式不正确。'] }
          }
        }
      }
    }

    expect(resolveAdminUserError(error, copy)).toEqual({
      summary: copy.fieldsSummary({ email: ['邮箱格式不正确。'] }),
      fields: { email: ['邮箱格式不正确。'] }
    })
  })

  test('reads fields from a direct backend envelope too', () => {
    const error = {
      code: 422,
      message: '用户更新内容不正确。',
      data: {
        reason: 'user.invalid_update',
        fields: { username: ['这个用户名已被使用，请换一个。'] }
      }
    }

    expect(resolveAdminUserError(error, copy)).toEqual({
      summary: copy.fieldsSummary({ username: ['这个用户名已被使用，请换一个。'] }),
      fields: { username: ['这个用户名已被使用，请换一个。'] }
    })
  })

  test('maps user.update_empty to the no-changes copy', () => {
    const error = {
      code: 422,
      message: '用户更新内容不正确。',
      data: { reason: 'user.update_empty' }
    }

    expect(resolveAdminUserError(error, copy)).toEqual({ summary: copy.noChanges, fields: {} })
  })

  test('falls back to the envelope message, then to the copy fallback', () => {
    expect(resolveAdminUserError({
      code: 500,
      message: '服务器开小差了。',
      data: { reason: 'internal_error' }
    }, copy)).toEqual({ summary: '服务器开小差了。', fields: {} })

    expect(resolveAdminUserError(new Error('boom'), copy)).toEqual({
      summary: copy.fallback,
      fields: {}
    })
  })

  test('names the failing fields in drawer order and folds the rest', () => {
    expect(adminUserErrorFieldNames({ roleKeys: ['x'], email: ['y'], username: ['z'] }))
      .toEqual({ names: ['username', 'email', 'roleKeys'], extra: 0 })
    expect(adminUserErrorFieldNames({ roleKeys: ['x'], email: ['y'], username: ['z'], status: ['w'], bio: ['v'] }))
      .toEqual({ names: ['username', 'email', 'status'], extra: 2 })
    expect(adminUserErrorFieldNames({ unknownField: ['x'] })).toEqual({ names: ['unknownField'], extra: 0 })
  })

  test('focuses the first error in drawer visual order', () => {
    expect(firstAdminUserErrorField({ websiteUrl: ['x'], email: ['y'] })).toBe('email')
    expect(firstAdminUserErrorField({ confirmPassword: ['x'], password: ['y'] })).toBe('password')
    expect(firstAdminUserErrorField({ unknownField: ['x'] })).toBe('unknownField')
    expect(firstAdminUserErrorField({ roleKeys: ['x'], confirmPassword: ['y'] })).toBe('confirmPassword')
    expect(firstAdminUserErrorField({})).toBeNull()
  })
})
