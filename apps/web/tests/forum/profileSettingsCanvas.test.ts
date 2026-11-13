import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { normalizeSiteDomain, siteDomainFromUrl } from '../../app/utils/settings/siteDomain'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const page = () => source('../../app/components/settings/SFProfileSettingsPage.vue')
// tab 化之后：草稿归 composable，分区内容各自一个文件（AGENTS 的固定 tab 规约）。
const draftState = () => source('../../app/composables/settings/useProfileSettingsDraft.ts')
const identityTab = () => source('../../app/components/settings/profile/tabs/SFProfileIdentityTab.vue')
const storyTab = () => source('../../app/components/settings/profile/tabs/SFProfileStoryTab.vue')
const linksTab = () => source('../../app/components/settings/profile/tabs/SFProfileLinksTab.vue')
const shell = () => source('../../app/components/settings/SFSettingsShell.vue')
const accountSidebar = () => source('../../app/components/settings/SFSettingsAccountSidebar.vue')
const preview = () => source('../../app/components/settings/SFProfileSettingsPreview.vue')
const accountNav = () => source('../../app/components/settings/SFSettingsAccountNav.vue')
const styles = () => source('../../app/assets/css/sforum-profile-settings.css')
const chrome = () => source('../../app/assets/css/sforum-settings.css')
const route = () => source('../../app/pages/settings/profile.vue')
const themeTemplate = () => source('../../../../extensions/builtin/themes/sforum-default/templates/settings-profile.html')
const zhCN = () => JSON.parse(source('../../i18n/locales/zh-CN.json'))
const enUS = () => JSON.parse(source('../../i18n/locales/en-US.json'))

describe('profile settings canvas', () => {
  test('keeps Page Registry and default theme ownership boundaries intact', () => {
    expect(route()).toContain('SFPageOutlet')
    expect(route()).toContain('page="forum.settings.profile"')
    expect(route()).toContain('<SFProfileSettingsPage')
    expect(themeTemplate()).toContain('data-theme-owned="presentation"')
    expect(themeTemplate()).toContain('data-page="forum.settings.profile"')
    expect(themeTemplate()).toContain('<sf-profile-settings')
  })

  test('loads and saves profile fields plus the private default language', () => {
    const src = page()
    const draft = draftState()
    expect(src).toContain('profileApi.getMyProfile()')
    expect(draft).toContain('profileApi.updateMyProfile({')
    expect(draft).toContain('bio: draft.bio')
    expect(draft).toContain('signature: draft.signature')
    expect(draft).toContain('location: draft.location')
    expect(draft).toContain('websiteUrl: draft.websiteUrl')
    expect(draft).toContain('await options.updateLanguage(draft.locale)')
    expect(identityTab()).toContain("t('profileSettings.defaultLanguage')")
    expect(identityTab()).toContain(':model-value="localeDraft"')
    for (const file of [src, draft, identityTab(), storyTab(), linksTab()]) {
      expect(file).not.toContain('company')
      expect(file).not.toContain('portfolio')
      expect(file).not.toContain('followers')
      expect(file).not.toContain('privacy')
    }
  })

  test('uses the runtime website domain for the public profile path', () => {
    const src = page()
    expect(src).toContain('const { siteDomain, avatarSettings, nicknameSettings } = useWebOptions()')
    expect(src).toContain('`${siteDomain.value}/u/`')
    // 前缀字段搬进身份分区：宿主算，tab 只管渲染。
    expect(src).toContain(':public-profile-prefix="publicProfilePrefix"')
    expect(identityTab()).toContain('{{ publicProfilePrefix }}')
    expect(src).not.toContain("t('profileSettings.profilePathPrefix')")
    expect(normalizeSiteDomain(' https://Forum.Example.com/// ')).toBe('Forum.Example.com')
    expect(siteDomainFromUrl('https://forum.example.com:8443/base')).toBe('forum.example.com:8443')
  })

  test('edits the nickname through the same profile save flow', () => {
    const src = page()
    const draft = draftState()
    const identity = identityTab()
    expect(identity).toContain('import SFProfileNicknameField')
    expect(identity).toContain(':model-value="displayNameDraft"')
    expect(identity).toContain(':policy="nicknameSettings"')
    expect(draft).toContain('draft.displayName !== baseline.value.displayName')
    expect(draft).toContain('...(nicknameChanged ? { displayName: draft.displayName } : {})')
    // 改名后导航栏读的是会话里的 displayName，保存成功要一起同步。
    expect(src).toContain('syncAuthUser({ displayName: updated.displayName })')
    // 用户名保持只读：昵称才是可改的展示标签（属性已分行，逐项断言）。
    expect(identity).toContain(':value="profile.username"')
    expect(identity).toMatch(/:value="profile\.username"[\s\S]{0,120}?readonly/)
  })

  test('preserves avatar upload and removal through the real profile API', () => {
    const src = identityTab()
    expect(src).toContain('const avatarAccept = computed')
    expect(src).toContain('can(FORUM_PERMISSIONS.attachmentUpload)')
    expect(src).toContain('const canUploadAvatar = computed')
    expect(src).toContain('props.avatarSettings.allowGif')
    expect(src).toContain('file.size > props.avatarSettings.maxSizeKb * 1024')
    expect(src).toContain("file.type === 'image/gif'")
    expect(src).toContain('profileApi.uploadAvatar(file)')
    expect(src).toContain('profileApi.deleteAvatar()')
    // 头像结果交给宿主同步会话（导航/右栏读的是会话里的 avatar）。
    expect(src).toContain("emit('avatar-changed', updated)")
    expect(page()).toContain('syncAuthUser({ avatar: updated.avatar })')
    expect(src).toContain(':disabled="!canUploadAvatar || avatarBusy"')
    expect(src).toContain(':loading="avatarBusy"')
  })

  test('renders the public preview from the local draft and marks unsaved edits', () => {
    const src = page()
    const draft = draftState()
    const previewSrc = preview()
    expect(draft).toContain('const isDirty = computed')
    expect(draft).toContain('function resetDraft()')
    expect(draft).toContain("title: t('profileSettings.resetDone')")
    expect(draft).toContain('draft.bio !== baseline.value.bio')
    expect(draft).toContain('draft.location !== baseline.value.location')
    expect(src).toContain('const publicWebsiteHref = computed(() => safeUrl(draft.websiteUrl))')
    expect(previewSrc).toContain('v-if="!dirty && websiteHref"')
    expect(previewSrc).toContain('profileSettings.preview.unsaved')
    expect(previewSrc).toContain("t('profile.joinedOn'")
    expect(src).not.toContain('data-preview-signature')
    expect(src).not.toContain("i-lucide-quote")
  })

  test('ships nickname copy and conflict messages in both locales', () => {
    const uiKeys = [
      'nickname',
      'nicknamePlaceholder',
      'nicknameHint',
      'nicknameEmptyHint',
      'nicknameLocked',
      'nicknameCooldown',
      'nicknameCooldownUntil',
      'nicknameCooldownDays',
      'nicknameCooldownHours',
      'nicknameCooldownMinutes',
      'nicknameDuplicates'
    ]
    // 冲突文案与 API 消息包同名（profile.nickname_*），前端直接翻译服务端返回的 key。
    const conflictKeys = [
      'nickname_invalid_chars',
      'nickname_too_short',
      'nickname_too_long',
      'nickname_reserved',
      'nickname_username_taken',
      'nickname_at_prefix'
    ]
    const adminKeys = [
      'nicknameTitle',
      'nicknameDescription',
      'nicknameAllowUserChange',
      'nicknameAllowUserChangeHint',
      'nicknameCooldownHint',
      'nicknameMinLength',
      'nicknameMaxLength',
      'nicknameHistoryPublic',
      'nicknameReservedHint'
    ]
    for (const locale of [zhCN(), enUS()]) {
      for (const key of uiKeys) {
        expect(locale.profileSettings[key]).toBeTruthy()
      }
      for (const key of conflictKeys) {
        expect(locale.profile[key]).toBeTruthy()
      }
      for (const key of adminKeys) {
        expect(locale.admin.settings.registration[key]).toBeTruthy()
      }
      for (const key of ['nicknameHistorySection', 'nicknameHistoryEmpty', 'nicknameHistorySourceAdmin', 'nicknameHistorySourceSelf']) {
        expect(locale.admin.users[key]).toBeTruthy()
      }
    }
  })

  test('uses public three-column chrome aligned with home and notifications', () => {
    const src = page()
    const shellSrc = shell()
    const css = styles()
    const chromeCss = chrome()
    const nav = accountNav()
    const sidebar = accountSidebar()
    expect(src).toContain('<SFSettingsShell')
    expect(src).toContain('active="profile"')
    expect(src).toContain('class="sforum-settings-profile"')
    expect(src).toContain('<SFProfileSettingsPreview')
    expect(src).toContain('<style src="~/assets/css/sforum-profile-settings.css" lang="css"></style>')
    expect(shellSrc).toContain('data-layout="fullwidth-3col"')
    expect(shellSrc).toContain('sforum-settings__layout')
    expect(shellSrc).toContain('<SFSettingsAccountSidebar')
    // 左栏实现（含账号导航与移动端抽屉所有者）由账号设置侧栏组件独占，
    // 插件公开页的 sf-account-settings-sidebar 岛复用同一份，防止再次复制漂移。
    expect(sidebar).toContain('<SFSettingsAccountNav')
    expect(sidebar).toContain('<SFResponsivePublicSidebar')
    expect(sidebar).toContain('<SFHomeNavigation')
    expect(sidebar).toContain('navigation-mode="route"')
    expect(sidebar).toContain(':show-categories="false"')
    expect(sidebar).toContain('owner-id="forum.settings"')
    expect(shellSrc).not.toContain('<SFSettingsAccountNav')
    expect(shellSrc).not.toContain("useState<boolean>('forum-mobile-menu-open'")
    expect(shellSrc).toContain("useState<boolean>('forum-mobile-info-open'")
    expect(shellSrc).not.toContain('@click="mobileInfoOpen = true"')
    expect(shellSrc).not.toContain('@click="openMobileMenu"')
    expect(shellSrc).toContain('sforum-mobile-drawer__backdrop')
    expect(shellSrc).not.toContain('sforum-mobile-drawer sforum-mobile-drawer--left')
    expect(shellSrc).toContain('sforum-mobile-drawer sforum-mobile-drawer--right')
    expect(shellSrc).toContain('<style src="~/assets/css/sforum-settings.css" lang="css"></style>')
    expect(chromeCss).toContain('grid-template-columns: var(--sf-public-sidebar-width) minmax(0, 1fr) var(--sf-public-right-rail-width)')
    expect(chromeCss).toMatch(/\.sforum-settings__main\s*\{[^}]*background: var\(--sf-public-surface\);/s)
    expect(chromeCss).toContain('@media (max-width: 1100px)')
    expect(chromeCss).toContain('@media (max-width: 980px)')
    expect(css).toContain('sforum-settings-profile__form')
    expect(nav).toContain("localePath('/settings/profile')")
    // 侧栏已合并为单一「账号安全」入口：登录方式 / 密码是其子路由 tab。
    expect(nav).not.toContain("localePath('/settings/login-methods')")
    expect(nav).not.toContain("localePath('/settings/password')")
    expect(nav).toContain("localePath('/settings/security')")
    expect(nav).toContain("localePath('/settings/tokens')")
    expect(src).not.toContain('ssr: false')
    expect(src).not.toContain('layout: false')
    expect(src).not.toContain('sf-profile-settings-canvas__layout')
  })

  test('uses the active appearance color for successful profile actions', () => {
    // 保存 toast 在草稿 composable，头像上传/移除 toast 在身份分区；合计仍只有 3 处。
    const owners = [draftState(), identityTab()]
    expect(owners.join('').match(/color: 'primary'/g)?.length).toBe(3)
    for (const file of owners) {
      expect(file).not.toContain("color: 'success'")
    }
  })

  test('keeps i18n complete for the canvas, preview, and visibility copy', () => {
    for (const messages of [zhCN(), enUS()]) {
      const copy = messages.profileSettings
      expect(copy.canvasTitle).toBeTruthy()
      expect(copy.sections.identity).toBeTruthy()
      expect(copy.sections.story).toBeTruthy()
      expect(copy.sections.links).toBeTruthy()
      expect(copy.preview.unsaved).toBeTruthy()
      expect(copy.preview.open).toBeTruthy()
      expect(copy.scope.publicFields).toBeTruthy()
      expect(copy.scope.privateFields).toBeTruthy()
      expect(copy.signaturePublicHint).toBeTruthy()
      expect(copy.avatarUploadPermissionDenied).toBeTruthy()
      expect(copy.resetChanges).toBeTruthy()
      expect(copy.resetDone).toBeTruthy()
      expect(copy.defaultLanguage).toBeTruthy()
      expect(copy.defaultLanguageHint).toBeTruthy()
      expect(copy.saved).toBeTruthy()
      expect(copy.saveFailed).toBeTruthy()
      expect(messages.accountSecurity.rail.devicesTitle).toBeTruthy()
      expect(messages.accountSecurity.rail.tokens).toBeTruthy()
      expect(messages.localPasswordSettings.title).toBeTruthy()
      expect(messages.localPasswordSettings.rail.passwordTitle).toBeTruthy()
    }
  })
})
