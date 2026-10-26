import { describe, expect, test } from 'bun:test'
import { FetchError } from 'ofetch'
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

  test('reads fields from a real ofetch FetchError', () => {
    const error = new FetchError('422 Unprocessable Entity')
    error.statusCode = 422
    error.response = {
      _data: {
        code: 422,
        message: '提交的用户信息有误，请按标出的字段修改后再保存。',
        data: {
          reason: 'user.invalid_update',
          fields: { displayName: ['显示名不能为空。'], roleKeys: ['所选用户组不存在。'] }
        }
      }
    } as unknown as typeof error.response

    expect(resolveAdminUserError(error, copy)).toEqual({
      summary: copy.fieldsSummary({ displayName: ['显示名不能为空。'], roleKeys: ['所选用户组不存在。'] }),
      fields: { displayName: ['显示名不能为空。'], roleKeys: ['所选用户组不存在。'] }
    })
  })

  // 网络中断、代理返回 HTML、调用方抛字符串……这些都必须在抽屉里降级成兜底文案，不能抛。
  test('degrades unknown error shapes to the fallback copy without throwing', () => {
    // 完全没有可用 message 的形态：必须落到兜底文案。
    const silent: unknown[] = [
      null,
      undefined,
      'boom',
      42,
      [],
      {},
      { response: { _data: 'not-an-envelope' } }
    ]
    for (const shape of silent) {
      expect(() => resolveAdminUserError(shape, copy)).not.toThrow()
      expect(resolveAdminUserError(shape, copy)).toEqual({ summary: copy.fallback, fields: {} })
    }

    // 有信封 message、但 fields 不是字符串数组：用 message 兜底，仍然不抛。
    const malformedFields: unknown[] = [
      { code: 422, message: 'x', data: { fields: { email: 'not-an-array' } } },
      { code: 422, message: 'x', data: { fields: null } }
    ]
    for (const shape of malformedFields) {
      expect(() => resolveAdminUserError(shape, copy)).not.toThrow()
      expect(resolveAdminUserError(shape, copy)).toEqual({ summary: 'x', fields: {} })
    }
  })

  test('treats an empty fields object as no field errors', () => {
    expect(resolveAdminUserError({
      code: 422,
      message: '提交的用户信息有误，请按标出的字段修改后再保存。',
      data: { reason: 'user.invalid_update', fields: {} }
    }, copy)).toEqual({ summary: '提交的用户信息有误，请按标出的字段修改后再保存。', fields: {} })
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
