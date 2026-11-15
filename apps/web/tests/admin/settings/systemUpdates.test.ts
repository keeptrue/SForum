import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const component = readFileSync(
  new URL('../../../app/components/admin/settings/site/tabs/SFAdminSiteUpdatesTab.vue', import.meta.url),
  'utf8'
)
const composable = readFileSync(
  new URL('../../../app/composables/admin/useAdminSystemUpdates.ts', import.meta.url),
  'utf8'
)
const prompt = readFileSync(
  new URL('../../../app/components/admin/system/SFAdminSystemUpdatePrompt.vue', import.meta.url),
  'utf8'
)
const promptModel = readFileSync(
  new URL('../../../app/utils/admin/systemUpdatePrompt.ts', import.meta.url),
  'utf8'
)
const adminLayout = readFileSync(
  new URL('../../../app/layouts/admin.vue', import.meta.url),
  'utf8'
)

describe('admin system update surface', () => {
  test('keeps cached status and forced checks on their permission-specific endpoints', () => {
    expect(composable).toContain("'/admin/system-updates/check'")
    expect(composable).toContain("'/admin/system-updates'")
    expect(composable).toContain("method: options.force ? 'POST' : undefined")
  })

  test('supports mirror save, official-source restore, and persistent error feedback', () => {
    expect(component).toContain("system.updates.github_mirror_url")
    expect(component).toContain("form.mirrorUrl = ''")
    expect(component).toContain('updates.refresh({ force: true })')
    expect(component).toContain("toast.add({ color: 'error'")
    // RV-20：错误 toast 必须常驻（`duration: 0`）。旧断言把「不带 duration」当契约，
    // 与本轮「错误提示不自动消失」相反，已随契约更新。
    expect(component).toContain("color: 'error', duration: 0")
  })

  test('shows update availability in a modal and suppresses repeat prompts for six hours', () => {
    expect(prompt).toContain("status.state !== 'update_available'")
    expect(prompt).toContain("localStorage.getItem(STORAGE_KEY)")
    expect(prompt).toContain("localStorage.setItem(STORAGE_KEY, systemUpdatePromptRecord())")
    expect(prompt).toContain('<UModal v-model:open="open"')
    expect(promptModel).toContain('6 * 60 * 60 * 1000')
  })

  test('refreshes update status every five minutes while the admin shell stays open', () => {
    expect(adminLayout).toContain('const systemUpdateRefreshIntervalMs = 5 * 60 * 1000')
    expect(adminLayout).toContain('systemUpdates.refresh().catch(() => null)')
    expect(adminLayout).toContain('clearInterval(systemUpdateRefreshTimer)')
  })
})
