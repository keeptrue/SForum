import { describe, expect, test } from 'bun:test'

import { normalizeAuthReturnPath } from '../../app/utils/identity/authReturn'

describe('normalizeAuthReturnPath：归一化后的协议相对地址', () => {
  test('路径归一化塌陷成 //host 时返回 null', () => {
    // URL 归一化会吃掉 `..` 段，`/..//evil` 的 pathname 变成 `//evil`；
    // 只检查原始输入 startsWith('//') 拦不住，navigateTo 会把它判成外链抛错。
    expect(normalizeAuthReturnPath('/..//evil.example')).toBeNull()
    expect(normalizeAuthReturnPath('/%2e%2e//evil.example')).toBeNull()
    expect(normalizeAuthReturnPath('/%2E%2E//evil.example')).toBeNull()
    expect(normalizeAuthReturnPath('/a/../..//x')).toBeNull()
    expect(normalizeAuthReturnPath('/.//evil.example')).toBeNull()
  })

  test('反斜杠被归一化成协议相对地址时同样拒绝', () => {
    expect(normalizeAuthReturnPath('/\\evil.example')).toBeNull()
  })

  test('正常站内路径原样返回', () => {
    expect(normalizeAuthReturnPath('/topics/1?x=1')).toBe('/topics/1?x=1')
    expect(normalizeAuthReturnPath('/topics/1?x=1#reply-2')).toBe('/topics/1?x=1#reply-2')
    expect(normalizeAuthReturnPath('/')).toBe('/')
  })
})

describe('normalizeAuthReturnPath：非法百分号转义', () => {
  test('pathname/search/hash 的非法转义都返回 null', () => {
    expect(normalizeAuthReturnPath('/%E0%A4%A')).toBeNull()
    expect(normalizeAuthReturnPath('/safe?next=%E0%A4%A')).toBeNull()
    expect(normalizeAuthReturnPath('/safe#%E0%A4%A')).toBeNull()
  })
})
