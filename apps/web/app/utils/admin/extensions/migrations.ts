/**
 * 插件迁移账本（`GET/POST /admin/extensions/{id}/migrations[/apply]`）的展示侧派生。
 *
 * 契约事实（`ExtensionMigrationRecord`）：`path` / `checksum` / `status` /
 * `appliedAt` 必填，`message` 可选；`status` 只有 `recorded` / `applied` / `failed`
 * 三个值 —— v1 宿主只记录校验和（`recorded`），`applied` 留给未来的执行器，
 * `failed` 表示文件缺失或不安全。所以 UI 不能把 `recorded` 说成「已执行」。
 */

export type ExtensionMigrationStatus = 'recorded' | 'applied' | 'failed'

export type ExtensionMigrationRecord = {
  path: string
  checksum: string
  status: ExtensionMigrationStatus
  appliedAt: string
  message?: string
}

/** 状态 → i18n 键。未知状态原样显示，不猜含义。 */
export function migrationStatusLabelKey(status: string): string | null {
  switch (status) {
    case 'recorded':
    case 'applied':
    case 'failed':
      return `admin.extensions.migrations.status.${status}`
    default:
      return null
  }
}

/** 账本里只要有 failed，页面顶部就提示「有文件缺失/不安全」，否则容易漏看。 */
export function migrationFailureCount(rows: readonly ExtensionMigrationRecord[] | undefined | null): number {
  return (rows || []).filter(row => row.status === 'failed').length
}

/**
 * 校验和只在审计时需要「完整值可查、列表不撑爆」：列表显示前 12 位，
 * `title` 属性给完整值。空校验和返回空串（`failed` 行可能没有校验和）。
 */
export function shortMigrationChecksum(checksum: string | undefined | null): string {
  const value = (checksum || '').trim()
  if (!value) return ''
  return value.length <= 12 ? value : `${value.slice(0, 12)}…`
}

/** apply 之后没有新增行时给出「已是当前状态」的反馈，而不是假装成功。 */
export function migrationLedgerChanged(before: readonly ExtensionMigrationRecord[], after: readonly ExtensionMigrationRecord[]): boolean {
  if (before.length !== after.length) return true
  const beforePaths = new Set(before.map(row => `${row.path}\u0000${row.checksum}`))
  return after.some(row => !beforePaths.has(`${row.path}\u0000${row.checksum}`))
}
