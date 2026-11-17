import { useApiClient } from '~/composables/useApiClient'
import type { ExtensionMigrationRecord } from '~/utils/admin/extensions/migrations'

/**
 * 插件迁移账本。
 *
 * 读：`GET /admin/extensions/{id}/migrations`（`extension.view` 门）。
 * 写：`POST /admin/extensions/{id}/migrations/apply`（`extension.manage` 门）——
 * 幂等地把 `manifest.migrations` 声明的路径记进账本（带文件校验和），
 * **不执行插件 SQL**。返回的是记录后的完整账本，所以调用方可以直接用它刷新列表。
 */
export function useAdminExtensionMigrations() {
  const { request } = useApiClient()

  function endpoint(extensionId: string) {
    return `/admin/extensions/${encodeURIComponent(extensionId)}/migrations`
  }

  function list(extensionId: string) {
    return request<ExtensionMigrationRecord[]>(endpoint(extensionId))
  }

  function apply(extensionId: string) {
    return request<ExtensionMigrationRecord[]>(`${endpoint(extensionId)}/apply`, { method: 'POST' })
  }

  return { list, apply }
}
