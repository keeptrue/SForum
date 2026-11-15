import type { AdminExtensionInstallResult } from '~/utils/admin/adminExtensions'
import type { AdminHostInstallRequest, AdminHostInstallType } from '~/utils/admin/extensions/hostActions'
import {
  EXTENSION_INSTALL_FROM_URL_PATH,
  EXTENSION_INSTALL_PATH
} from '~/utils/admin/extensions/extensionInstall'

/**
 * Options forwarded to the single install POST. The admin page panel and the
 * trusted-page bridge share this transport: one request shape, one error
 * surface, one result type.
 */
export type AdminInstallFromUrlOptions = {
  /** Declared package identity; the Host aborts when the manifest disagrees. */
  expectations?: Pick<AdminHostInstallRequest, 'expectedExtensionId' | 'expectedType' | 'expectedVersion'> & {
    source?: string
  }
  /** Per-call timeout override. The bridge passes its synchronous-install budget. */
  timeout?: number
}

/**
 * Normalizes the Host install result into the bridge contract. `requiresTrust`
 * mirrors the service-layer gate: an executable backend on a non-trusted
 * artifact still needs the administrator's exact-artifact confirmation.
 */
export function toAdminHostInstallResult(result: AdminExtensionInstallResult): {
  extensionId: string
  version: string
  type: AdminHostInstallType
  packageDigest: string
  upgraded: boolean
  requiresTrust: boolean
} {
  const extension = result.extension
  const version = extension.stagedVersion?.version || extension.version
  const packageDigest = extension.stagedVersion?.packageDigest || extension.packageDigest
  const type: AdminHostInstallType = extension.type === 'theme' ? 'theme' : 'plugin'
  const manifest = (extension.stagedVersion?.manifest || extension.manifest) as
    | { backend?: { entry?: string } }
    | undefined
  return {
    extensionId: extension.id,
    version,
    type,
    packageDigest,
    upgraded: result.upgraded === true,
    // 带可执行后端的包在启用时仍要管理员做 exact-artifact 信任确认；这里如实
    // 回报，让目录页能提前提示，而不是让调用方以为装完就能用。
    requiresTrust: Boolean(manifest?.backend?.entry)
  }
}

export const useAdminExtensionInstall = () => {
  const { request } = useApiClient()

  function installArchive(file: File) {
    const form = new FormData()
    form.append('file', file)
    return request<AdminExtensionInstallResult>(EXTENSION_INSTALL_PATH, {
      method: 'POST',
      body: form
    })
  }

  /**
   * `sha256` 是必填请求字段（Host 对缺摘要的链接安装直接回 422），因此这里
   * 既不给默认值、也不按条件省略：调用方必须在提交前校验，否则一次注定
   * 失败的请求会被发出去。
   */
  function installFromUrl(url: string, sha256: string, options: AdminInstallFromUrlOptions = {}) {
    const body: Record<string, unknown> = { url, sha256 }
    const expectations = options.expectations
    if (expectations) {
      if (expectations.expectedExtensionId) body.expectedExtensionId = expectations.expectedExtensionId
      if (expectations.expectedVersion) body.expectedVersion = expectations.expectedVersion
      if (expectations.expectedType) body.expectedType = expectations.expectedType
      if (expectations.source) body.source = expectations.source
    }
    return request<AdminExtensionInstallResult>(EXTENSION_INSTALL_FROM_URL_PATH, {
      method: 'POST',
      body,
      // 同步安装会一直占着这条请求；不显式放宽会被 useApiClient 的 30 秒默认值掐断。
      ...(options.timeout ? { timeout: options.timeout } : {})
    })
  }

  return { installArchive, installFromUrl }
}
