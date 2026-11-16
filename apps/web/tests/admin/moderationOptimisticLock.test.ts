import { describe, expect, test } from 'bun:test'

// NF-02：举报更新的乐观锁必须真的接在线上，而不是只存在于 API 里。
// 上一轮的实测结论是「composable 不发 expectedRevision + 函数零调用点 = 并发保护空转」，
// 所以这里钉住三件事：类型带版本号、请求带版本号、409 有可见反馈并刷新。
const api = await Bun.file(new URL('../../app/composables/moderation/useModerationApi.ts', import.meta.url)).text()
const page = await Bun.file(new URL('../../app/pages/admin/moderation.vue', import.meta.url)).text()
const zh = await Bun.file(new URL('../../i18n/locales/zh-CN.json', import.meta.url)).json()
const en = await Bun.file(new URL('../../i18n/locales/en-US.json', import.meta.url)).json()

describe('NF-02 moderation report optimistic lock', () => {
  test('the report type carries the revision the API returns', () => {
    expect(api).toContain('revision: number')
    expect(api).toContain('乐观锁版本')
  })

  test('updateReport always sends expectedRevision', () => {
    expect(api).toContain('expectedRevision: number')
    expect(api).toContain('method: \'PATCH\'')
  })

  test('the admin page submits the revision it read', () => {
    expect(page).toContain('expectedRevision: row.revision')
    expect(page).toContain('moderationApi.updateReport(row.id')
    expect(page).toContain('moderationApi.listReports({ status: \'open\'')
  })

  test('a 409 is surfaced and refreshes the list instead of silently overwriting', () => {
    expect(page).toContain('apiErrorStatusCode(error) === 409')
    expect(page).toContain('await refreshReports()')
    expect(page).toContain('data-testid="admin-moderation-report-conflict"')
  })

  test('the conflict message is localized in both locales', () => {
    for (const locale of [zh, en]) {
      const reports = locale.admin?.moderation?.reports
      expect(reports?.conflict).toBeTruthy()
      expect(reports?.resolve).toBeTruthy()
      expect(reports?.reject).toBeTruthy()
    }
  })
})
