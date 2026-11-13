import { describe, expect, test } from 'bun:test'
import { compileVueSfc, mount } from '../helpers/vueSfc'

/**
 * 资料设置的输入上限必须与后端同口径（Unicode 码点）。
 *
 * 回归背景：计数器曾用 `bioDraft.length`，那是 UTF-16 码元数。178 个汉字曾显示
 * `178 / 500`，但服务端按字节算 534 字节 → 保存被拒「内容超出长度上限」。
 * 现在服务端按码点校验（简介 160、签名 200），前端 `Array.from(str).length`
 * 才是同一把尺子。
 *
 * 这里是挂载真实 SFC 的行为断言：非 BMP 字符（emoji）码点 1、码元 2，
 * 一旦有人把计数改回 `.length`，下面的期望值就会整体翻倍而变红。
 * 同一类「控件拦得比服务端狠」的残留也在这里钉住：没有计数器的
 * 所在地 / 网站输入框，maxlength 必须取 2× 码点上限而不是 1×。
 */
const storyPath = new URL('../../app/components/settings/profile/tabs/SFProfileStoryTab.vue', import.meta.url).pathname
const StoryTab = await compileVueSfc(storyPath, 'story-tab', {
  useI18n: () => ({ t: (key: string) => key }),
  t: (key: string) => key
})

const linksPath = new URL('../../app/components/settings/profile/tabs/SFProfileLinksTab.vue', import.meta.url).pathname
const LinksTab = await compileVueSfc(linksPath, 'links-tab', {
  useI18n: () => ({ t: (key: string) => key }),
  t: (key: string) => key
})

function mountStory(bioDraft: string, signatureDraft = '') {
  return mount(StoryTab, {
    props: { bioDraft, signatureDraft, busy: false, fieldErrors: {} },
    global: { provide: { t: (key: string) => key } }
  })
}

/** 两个字段的计数器文本，顺序与模板一致：简介、签名。 */
function counters(wrapper: ReturnType<typeof mountStory>) {
  return wrapper.findAll('.sforum-settings-profile__field-topline span').map(node => node.text())
}

describe('profile story tab character counter', () => {
  test('160 个汉字正好显示 160 / 160，不显示字节数 480', () => {
    const wrapper = mountStory('好'.repeat(160))
    expect(counters(wrapper)[0]).toBe('160 / 160')
    // 480 是同一段文本的 UTF-8 字节数：前端不该用字节当刻度。
    expect(wrapper.text()).not.toContain('480')
  })

  test('178 个汉字超限时仍显示真实码点数 178 / 160', () => {
    const wrapper = mountStory('好'.repeat(178))
    expect(counters(wrapper)[0]).toBe('178 / 160')
    expect(wrapper.text()).not.toContain('534')
  })

  test('码点计数：160 个 emoji 仍是 160 / 160（不是 UTF-16 码元的 320）', () => {
    const wrapper = mountStory('😀'.repeat(160))
    expect(counters(wrapper)[0]).toBe('160 / 160')
    expect(wrapper.text()).not.toContain('320')
  })

  test('签名用同一口径：200 个 emoji 是 200 / 200（不是 400）', () => {
    const wrapper = mountStory('', '😀'.repeat(200))
    expect(counters(wrapper)[1]).toBe('200 / 200')
    expect(wrapper.text()).not.toContain('400')
  })

  test('两个字段各自独立计数，互不串位', () => {
    const wrapper = mountStory('好'.repeat(12), '😀'.repeat(7))
    expect(counters(wrapper)).toEqual(['12 / 160', '7 / 200'])
  })

  test('HTML maxlength 放宽到 2×，不拦住合法的 160/200 码点输入', () => {
    const wrapper = mountStory('')
    const bio = wrapper.get('#profile-settings-bio')
    const signature = wrapper.get('#profile-settings-signature')
    // 码点上限 160 / 200；一个码点最多 2 个 UTF-16 码元，所以控件上限取 320 / 400。
    expect(Number(bio.attributes('maxlength'))).toBe(320)
    expect(Number(signature.attributes('maxlength'))).toBe(400)
    expect(Number(bio.attributes('maxlength'))).toBeGreaterThanOrEqual('😀'.repeat(160).length)
    expect(Number(signature.attributes('maxlength'))).toBeGreaterThanOrEqual('😀'.repeat(200).length)
  })
})

describe('profile links tab input limits', () => {
  test('所在地 / 网站的 maxlength 也是 2× 码点上限，不提前拦住 emoji', () => {
    const wrapper = mount(LinksTab, {
      props: { locationDraft: '', websiteDraft: '', busy: false, fieldErrors: {} },
      global: { provide: { t: (key: string) => key } }
    })
    const location = wrapper.get('#profile-settings-location')
    const website = wrapper.get('#profile-settings-website')
    // 码点上限 100 / 200；一个码点最多 2 个 UTF-16 码元，所以控件上限取 200 / 400。
    expect(Number(location.attributes('maxlength'))).toBe(200)
    expect(Number(website.attributes('maxlength'))).toBe(400)
    expect(Number(location.attributes('maxlength'))).toBeGreaterThanOrEqual('😀'.repeat(100).length)
    expect(Number(website.attributes('maxlength'))).toBeGreaterThanOrEqual('😀'.repeat(200).length)
  })
})
