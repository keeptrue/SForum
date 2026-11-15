/** Client-side checks shared by the admin install page and its tests. */

export const EXTENSION_ARCHIVE_MAX_BYTES = 50 * 1024 * 1024
export const EXTENSION_ARCHIVE_ACCEPT = '.zip,application/zip,application/x-zip-compressed'
export const EXTENSION_INSTALL_PATH = '/admin/extensions'
export const EXTENSION_INSTALL_FROM_URL_PATH = '/admin/extensions/install-from-url'
export const EXTENSION_REMOTE_URL_MAX_LENGTH = 2048

export type ArchiveFileLike = {
  name?: string
  size?: number
  type?: string
}

export type ArchiveFileValidation =
  | { ok: true }
  | { ok: false, reason: 'empty' | 'type' | 'size' }

export function validateArchiveFile(file: ArchiveFileLike | null | undefined): ArchiveFileValidation {
  if (!file) return { ok: false, reason: 'empty' }
  const name = String(file.name || '').trim().toLowerCase()
  const type = String(file.type || '').trim().toLowerCase()
  const size = Number(file.size || 0)
  if (!name && !type) return { ok: false, reason: 'empty' }
  if (!name.endsWith('.zip') && type !== 'application/zip' && type !== 'application/x-zip-compressed') {
    return { ok: false, reason: 'type' }
  }
  if (!Number.isFinite(size) || size <= 0) return { ok: false, reason: 'empty' }
  if (size > EXTENSION_ARCHIVE_MAX_BYTES) return { ok: false, reason: 'size' }
  return { ok: true }
}

export type RemoteUrlValidation =
  | { ok: true, url: string }
  | { ok: false, reason: 'empty' | 'length' | 'invalid' | 'scheme' | 'credentials' }

export function normalizeRemoteArchiveUrl(raw: string): RemoteUrlValidation {
  const value = String(raw ?? '').trim()
  if (!value) return { ok: false, reason: 'empty' }
  if (value.length > EXTENSION_REMOTE_URL_MAX_LENGTH) return { ok: false, reason: 'length' }

  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return { ok: false, reason: 'invalid' }
  }
  if (parsed.protocol !== 'https:') return { ok: false, reason: 'scheme' }
  if (parsed.username || parsed.password) return { ok: false, reason: 'credentials' }
  if (!parsed.hostname) return { ok: false, reason: 'invalid' }
  return { ok: true, url: parsed.toString() }
}

export type Sha256Validation =
  | { ok: true, value: string }
  | { ok: false, reason: 'empty' | 'format' }

export function normalizeSha256(raw: string): Sha256Validation {
  const value = String(raw ?? '').trim().toLowerCase()
  // 链接安装靠摘要锁定确切字节；空值不是「不校验」，而是 Host 会直接拒绝的
  // 无效请求，所以这里不给它一个合法的空值出口。
  if (!value) return { ok: false, reason: 'empty' }
  if (!/^[a-f0-9]{64}$/.test(value)) return { ok: false, reason: 'format' }
  return { ok: true, value }
}

export function formatFileSize(bytes: number): string {
  const value = Number(bytes)
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  if (value < 1024) return `${Math.round(value)} B`
  const units = ['KB', 'MB', 'GB']
  let size = value / 1024
  let unit = units[0]
  for (let index = 1; index < units.length && size >= 1024; index += 1) {
    size /= 1024
    unit = units[index]
  }
  const rounded = size >= 10 ? Math.round(size) : Math.round(size * 10) / 10
  return `${rounded} ${unit}`
}

/** Host label for a validated URL; never throws on user input. */
export function remoteArchiveHost(url: string): string {
  try {
    return new URL(url).host
  } catch {
    return ''
  }
}
