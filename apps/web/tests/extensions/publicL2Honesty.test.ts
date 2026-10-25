import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('public L2 trust disclosure', () => {
  const widget = source('../../app/components/SFExtensionWidget.vue')
  const en = source('../../i18n/locales/en-US.json')
  const zh = source('../../i18n/locales/zh-CN.json')
  const guide = source('../../../../docs/extensions/authoring-guide.md')

  test('widget keeps the machine trust attestation but renders no in-page honesty banner', () => {
    // 信任契约仍在：bridge trust 值 + DOM 上的 data-l2-trust 观测点。
    expect(widget).toContain('PUBLIC_FRONTEND_TRUST_NOTICE')
    expect(widget).toContain('data-l2-trust')
    // 页内黄色披露横幅已按产品决定移除：不允许残留标记、文案键或图标。
    expect(widget).not.toContain('public-l2-honesty')
    expect(widget).not.toContain('l2Honesty')
    expect(widget).not.toContain('i-tabler-shield-exclamation')
    expect(widget).not.toMatch(/[\u{1F300}-\u{1FAFF}]/u)
  })

  test('locales drop the banner copy; authoring docs describe trust without the banner', () => {
    for (const locale of [en, zh]) {
      expect(locale).not.toContain('"l2Honesty"')
      expect(locale).toContain('"l2Retry"')
    }
    expect(guide).toContain('fully_trusted_browser_code')
    expect(guide).toContain('not a sandbox')
  })

  test('public bridge exposes only the release-level coreVersion', () => {
    // 公开面只给 coreVersion；buildVersion/commit 由管理面承载。
    expect(widget).toContain('host: current.coreVersion ? Object.freeze({ coreVersion: current.coreVersion }) : undefined')
    expect(widget).not.toContain('buildVersion')
    expect(guide).toContain('host.coreVersion')
  })
})
