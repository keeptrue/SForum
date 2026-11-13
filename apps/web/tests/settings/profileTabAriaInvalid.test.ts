import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 核心三区（身份 / 介绍 / 地点与链接）的字段错误必须能被读屏听见：
 * 只有 `is-invalid` 这个视觉类是不够的，裸 input 也要 `aria-invalid="true"`，
 * 与 SFDeclarativeSettingsField（插件字段）那套保持一致。
 */
const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

const i18nStub = () => ({ t: (key: string) => key })

const linksPath = new URL('../../app/components/settings/profile/tabs/SFProfileLinksTab.vue', import.meta.url).pathname
const storyPath = new URL('../../app/components/settings/profile/tabs/SFProfileStoryTab.vue', import.meta.url).pathname
const LinksTab = await compileVueSfc(linksPath, 'links-tab', { useI18n: i18nStub, t: (key: string) => key })
const StoryTab = await compileVueSfc(storyPath, 'story-tab', { useI18n: i18nStub, t: (key: string) => key })

describe('core profile tabs expose aria-invalid', () => {
  test('links: both inputs flip aria-invalid only when their field has an error', () => {
    const clean = mount(LinksTab, {
      props: { locationDraft: '', websiteDraft: '', busy: false, fieldErrors: {} },
      global: { provide: { t: (key: string) => key } }
    })
    expect(clean.find('#profile-settings-location').attributes('aria-invalid')).toBeUndefined()
    expect(clean.find('#profile-settings-website').attributes('aria-invalid')).toBeUndefined()

    const invalid = mount(LinksTab, {
      props: {
        locationDraft: '',
        websiteDraft: 'nope',
        busy: false,
        fieldErrors: { websiteUrl: ['不是合法网址'], location: ['太长了'] }
      },
      global: { provide: { t: (key: string) => key } }
    })
    const location = invalid.find('#profile-settings-location')
    const website = invalid.find('#profile-settings-website')
    expect(location.attributes('aria-invalid')).toBe('true')
    expect(website.attributes('aria-invalid')).toBe('true')
    // 错误只有一处时另一处不能跟着变红
    const partial = mount(LinksTab, {
      props: { locationDraft: '', websiteDraft: 'nope', busy: false, fieldErrors: { websiteUrl: ['不是合法网址'] } },
      global: { provide: { t: (key: string) => key } }
    })
    expect(partial.find('#profile-settings-location').attributes('aria-invalid')).toBeUndefined()
    expect(partial.find('#profile-settings-website').attributes('aria-invalid')).toBe('true')
    // 描述关联仍在（错误 id 进 aria-describedby）
    expect(website.attributes('aria-describedby')).toContain('profile-settings-website-error')
  })

  test('story: bio textarea and signature input both carry the conditional attribute', () => {
    const wrapper = mount(StoryTab, {
      props: { bioDraft: '', signatureDraft: '', busy: false, fieldErrors: { bio: ['太长'], signature: ['太长'] } },
      global: { provide: { t: (key: string) => key } }
    })
    expect(wrapper.find('#profile-settings-bio').attributes('aria-invalid')).toBe('true')
    expect(wrapper.find('#profile-settings-signature').attributes('aria-invalid')).toBe('true')

    const clean = mount(StoryTab, {
      props: { bioDraft: '', signatureDraft: '', busy: false, fieldErrors: {} },
      global: { provide: { t: (key: string) => key } }
    })
    expect(clean.find('#profile-settings-bio').attributes('aria-invalid')).toBeUndefined()
  })

  test('identity: username and nickname inputs report invalid state to assistive tech', () => {
    const identity = source('../../app/components/settings/profile/tabs/SFProfileIdentityTab.vue')
    expect(identity).toContain(':aria-invalid="usernameError ? \'true\' : undefined"')
    expect(identity).toContain('profile-settings-username-error')
    const nickname = source('../../app/components/settings/SFProfileNicknameField.vue')
    expect(nickname).toContain(':aria-invalid="conflictMessage ? \'true\' : undefined"')
  })

  test('the declarative (plugin) field keeps the same contract', () => {
    const field = source('../../app/components/settings/shared/SFDeclarativeSettingsField.vue')
    expect(field).toContain(':aria-invalid="hasError ? \'true\' : undefined"')
  })
})
