import type { AdminExtension } from '~/utils/admin/adminExtensions'

/**
 * 更新状态派生：宿主把新包 stage 在 `stagedVersion`，页面只需要回答
 * “有没有更新、是哪一种更新”。这里不碰任何 UI，方便三页与工具栏共用同一判定。
 *
 * 判定顺序（与 knowledge/decisions/2026-10-27-extension-update-vocabulary.md 一致）：
 * 制品不可用 → 无更新；无 staged → 无更新；semver 比较；同版本再比包摘要。
 */
export type ExtensionUpdateState = {
  kind: 'version' | 'content' | 'none'
  /** 目标版本（staged）；kind 为 none 时与 current 相同，表示没有独立目标。 */
  version: string
  /** 当前生效版本。 */
  current: string
}

type ParsedVersion = {
  major: number
  minor: number
  patch: number
  prerelease: string[]
}

/** 允许调用方只传比较所需的字段；页面直接传 AdminExtension 即可。 */
export type ExtensionUpdateCandidate = Pick<AdminExtension, 'version' | 'stagedVersion'> & {
  packageDigest?: string | null
  source?: string | null
  type?: string | null
}

// 仓库没有 semver 依赖，也不想为一次比较引入 40KB 的包。这里的比较器只覆盖
// semver 2.0.0 的优先级规则：数字段 + prerelease 标识符，忽略 build metadata。
const VERSION_PATTERN = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/

function isNumericIdentifier(value: string): boolean {
  return /^\d+$/.test(value)
}

function parseVersion(raw: string): ParsedVersion | null {
  const match = VERSION_PATTERN.exec(raw)
  if (!match) return null
  const [, major = '', minor = '', patch = '', prerelease] = match
  // semver 禁止数字段带前导零，这类值视为非法而不是按整数悄悄归一化。
  if ([major, minor, patch].some(part => part.length > 1 && part.startsWith('0'))) return null
  const majorValue = Number(major)
  const minorValue = Number(minor)
  const patchValue = Number(patch)
  if (![majorValue, minorValue, patchValue].every(value => Number.isSafeInteger(value))) return null
  const identifiers = prerelease ? prerelease.split('.') : []
  if (identifiers.some(identifier => !identifier || (isNumericIdentifier(identifier) && identifier.length > 1 && identifier.startsWith('0')))) {
    return null
  }
  return { major: majorValue, minor: minorValue, patch: patchValue, prerelease: identifiers }
}

function comparePrerelease(left: string[], right: string[]): number {
  if (!left.length && !right.length) return 0
  // 无 prerelease 的正式版优先级更高。
  if (!left.length) return 1
  if (!right.length) return -1
  const length = Math.max(left.length, right.length)
  for (let index = 0; index < length; index += 1) {
    const a = left[index]
    const b = right[index]
    // 前缀相同时，标识符更少的一方优先级更低。
    if (a === undefined) return -1
    if (b === undefined) return 1
    if (a === b) continue
    const aNumeric = isNumericIdentifier(a)
    const bNumeric = isNumericIdentifier(b)
    if (aNumeric && bNumeric) return Number(a) > Number(b) ? 1 : -1
    // 数字标识符优先级低于字母标识符。
    if (aNumeric) return -1
    if (bNumeric) return 1
    return a > b ? 1 : -1
  }
  return 0
}

export function compareExtensionVersions(left: string, right: string): number | null {
  const a = parseVersion(left.trim())
  const b = parseVersion(right.trim())
  if (!a || !b) return null
  for (const key of ['major', 'minor', 'patch'] as const) {
    if (a[key] !== b[key]) return a[key] > b[key] ? 1 : -1
  }
  return comparePrerelease(a.prerelease, b.prerelease)
}

function sameDigest(item: ExtensionUpdateCandidate) {
  const staged = item.stagedVersion?.packageDigest
  const active = item.packageDigest
  // 摘要缺失时无法证明“内容没变”，保留内容更新（旧数据与单测都走这条）。
  return Boolean(staged && active && staged === active)
}

export function resolveExtensionUpdateState(
  item: ExtensionUpdateCandidate
): ExtensionUpdateState {
  const current = typeof item.version === 'string' ? item.version.trim() : ''
  const staged = item.stagedVersion?.version
  const none: ExtensionUpdateState = { kind: 'none', version: current, current }
  if (typeof staged !== 'string' || !staged.trim()) return none
  const target = staged.trim()
  const order = compareExtensionVersions(target, current)
  // 任一侧不是合法 semver 时不猜更新：宁可少一个徽章，也不误报可升级。
  if (order === null) return none
  if (order > 0) return { kind: 'version', version: target, current }
  if (order < 0) return none
  return sameDigest(item) ? none : { kind: 'content', version: target, current }
}

/** 工具栏「{m} 个内置更新待重启」：只计内置插件的同版本内容更新，主题走激活。 */
export function builtinContentUpdateCount(items: ExtensionUpdateCandidate[]): number {
  return items.filter(item =>
    item.source === 'builtin'
    && item.type === 'plugin'
    && resolveExtensionUpdateState(item).kind === 'content').length
}
