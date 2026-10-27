import { describe, expect, test } from 'bun:test'

import {
  builtinContentUpdateCount,
  compareExtensionVersions,
  resolveExtensionUpdateState
} from '../../app/utils/admin/extensions/updateState'

function item(
  version: string,
  stagedVersion?: string,
  digests: { active?: string, staged?: string } = {},
  type: 'plugin' | 'theme' = 'plugin'
) {
  return {
    version,
    type,
    packageDigest: digests.active,
    stagedVersion: stagedVersion === undefined
      ? undefined
      : { version: stagedVersion, packageDigest: digests.staged }
  }
}

describe('resolveExtensionUpdateState', () => {
  test('staged 版本更高时判为版本升级', () => {
    expect(resolveExtensionUpdateState(item('1.0.0', '1.2.0'))).toEqual({
      kind: 'version',
      version: '1.2.0',
      current: '1.0.0'
    })
  })

  test('版本号按数字段比较，1.9.0 低于 1.10.0', () => {
    expect(resolveExtensionUpdateState(item('1.9.0', '1.10.0')).kind).toBe('version')
    expect(resolveExtensionUpdateState(item('1.10.0', '1.9.0')).kind).toBe('none')
    expect(compareExtensionVersions('1.10.0', '1.9.0')).toBe(1)
  })

  test('同版本比较包摘要：相同摘要不是更新，不同摘要才是内容更新', () => {
    expect(resolveExtensionUpdateState(item('1.0.0', '1.0.0', {
      active: 'a'.repeat(64),
      staged: 'a'.repeat(64)
    }))).toEqual({ kind: 'none', version: '1.0.0', current: '1.0.0' })
    expect(resolveExtensionUpdateState(item('1.0.0', '1.0.0', {
      active: 'a'.repeat(64),
      staged: 'b'.repeat(64)
    })).kind).toBe('content')
    // 摘要缺失时无法证明内容没变，保留内容更新。
    expect(resolveExtensionUpdateState(item('1.0.0', '1.0.0', { active: 'a'.repeat(64) })).kind).toBe('content')
  })

  test('版本相同但内容重新 stage 时判为内容更新', () => {
    expect(resolveExtensionUpdateState(item('1.0.0', '1.0.0'))).toEqual({
      kind: 'content',
      version: '1.0.0',
      current: '1.0.0'
    })
    // build metadata 不参与优先级比较。
    expect(resolveExtensionUpdateState(item('1.0.0', '1.0.0+20261027')).kind).toBe('content')
  })

  test('staged 版本更低时不提示更新', () => {
    expect(resolveExtensionUpdateState(item('2.0.0', '1.9.9'))).toEqual({
      kind: 'none',
      version: '2.0.0',
      current: '2.0.0'
    })
  })

  test('没有 staged 版本时没有更新', () => {
    expect(resolveExtensionUpdateState(item('1.0.0'))).toEqual({
      kind: 'none',
      version: '1.0.0',
      current: '1.0.0'
    })
    expect(resolveExtensionUpdateState({ version: '1.0.0', stagedVersion: { version: '' } }).kind).toBe('none')
  })

  test('任一侧不是合法 semver 时不猜更新', () => {
    expect(resolveExtensionUpdateState(item('1.0.0', 'latest')).kind).toBe('none')
    expect(resolveExtensionUpdateState(item('dev', '1.2.0')).kind).toBe('none')
    expect(resolveExtensionUpdateState(item('1.0', '1.2.0')).kind).toBe('none')
    // 前导零在 semver 中非法。
    expect(resolveExtensionUpdateState(item('1.0.0', '01.2.0')).kind).toBe('none')
    expect(compareExtensionVersions('1.0.0', 'not-a-version')).toBeNull()
  })

  test('prerelease 参与比较：1.2.0-rc.1 低于 1.2.0', () => {
    expect(resolveExtensionUpdateState(item('1.2.0-rc.1', '1.2.0'))).toEqual({
      kind: 'version',
      version: '1.2.0',
      current: '1.2.0-rc.1'
    })
    // 反向：正式版收到旧 prerelease 的 stage，不算更新。
    expect(resolveExtensionUpdateState(item('1.2.0', '1.2.0-rc.1')).kind).toBe('none')
    // 数字标识符按数值比较，rc.10 > rc.9。
    expect(resolveExtensionUpdateState(item('1.2.0-rc.9', '1.2.0-rc.10')).kind).toBe('version')
    // 数字标识符优先级低于字母标识符。
    expect(resolveExtensionUpdateState(item('1.2.0-1', '1.2.0-alpha')).kind).toBe('version')
    expect(resolveExtensionUpdateState(item('1.2.0-alpha', '1.2.0-alpha.1')).kind).toBe('version')
  })
})

describe('builtinContentUpdateCount', () => {
  test('只统计内置插件的同版本内容更新，主题走激活不计入', () => {
    const builtinContent = { ...item('1.0.0', '1.0.0'), source: 'builtin' }
    const builtinVersion = { ...item('1.0.0', '1.2.0'), source: 'builtin' }
    const uploadedContent = { ...item('1.0.0', '1.0.0'), source: 'uploaded' }
    const noUpdate = { ...item('1.0.0'), source: 'builtin' }
    const builtinThemeContent = { ...item('1.0.0', '1.0.0', {}, 'theme'), source: 'builtin' }
    expect(builtinContentUpdateCount([
      builtinContent,
      builtinVersion,
      uploadedContent,
      noUpdate,
      builtinThemeContent
    ])).toBe(1)
  })
})
