import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

import {
  buildCustomAppearanceThemeValue,
  cloneFooterLinks,
  enabledOptionValue,
  applyAdminSEOTitleTemplate,
  isSEOIndexingAllowed,
  lightBackgroundPalettes,
  lightBackgroundPresets,
  normalizeAppearanceThemeValue,
  normalizeEnabledOption,
  normalizeLightBackground,
  passwordPolicyProgress,
  passwordPolicyProgressLevel,
  passwordPolicyRequirements,
  recommendedPasswordPolicy,
  recommendedAppearanceTheme,
  recommendedLightBackground,
  recommendedAvatarSettings,
  recommendedFooterCopyright,
  recommendedFooterLinks,
  resolveAvatarSettings,
  resolvePasswordPolicy,
  resolveAltchaWidgetSettings,
  normalizeSEOVerificationToken,
  parseSEORobotsPathList,
  resolveSEOSettings,
  resolveAppearanceTheme
} from '../../app/composables/useWebOptions'
import {
  defaultSiteFaviconUrl,
  defaultSiteLogoUrl,
  resolveSiteBrandAssetUrl
} from '../../app/utils/settings/siteBrand'

describe('appearance theme helpers', () => {
  test('normalizes preset and custom theme values', () => {
    expect(normalizeAppearanceThemeValue('violet')).toBe('violet')
    expect(normalizeAppearanceThemeValue('custom:#4F46E5')).toBe('custom:#4f46e5')
    expect(normalizeAppearanceThemeValue('custom:not-a-color')).toBe('pine_teal')
  })

  test('builds custom theme CSS variables for the root document', () => {
    const theme = buildCustomAppearanceThemeValue('#4F46E5')
    const resolved = resolveAppearanceTheme(theme)

    expect(theme).toBe('custom:#4f46e5')
    expect(resolved.dataTheme).toBe('custom')
    expect(resolved.cssVars['--sf-accent']).toBe('#4f46e5')
    expect(resolved.cssVars['--sf-accent-rgb']).toBe('79 70 229')
    expect(resolved.style).toContain('--sf-primary-500: #4f46e5')
    expect(resolved.cssVars['--ui-color-success-500']).toBe('var(--sf-primary-500)')
  })

  test('normalizes the curated daytime background presets', () => {
    expect(normalizeLightBackground(' PAPER ')).toBe('paper')
    expect(normalizeLightBackground(' MORNING_APRICOT ')).toBe('morning_apricot')
    expect(normalizeLightBackground('night_blue')).toBe('pure_white')
    expect(recommendedLightBackground).toBe('pure_white')
    expect(lightBackgroundPresets).toHaveLength(12)
    expect(lightBackgroundPalettes.paper).toEqual({
      background: '#f7f5ef',
      surface: '#fffdf8',
      muted: '#f1ede4',
      border: '#ded8cb'
    })
  })

  test('bridges Nuxt UI success tokens to the active SForum theme color', () => {
    const css = readFileSync(new URL('../../app/assets/css/main.css', import.meta.url), 'utf8')

    expect(css).toContain('--ui-color-success-500: var(--sf-primary-500);')
    expect(css).toContain('--ui-color-success-400: var(--sf-primary-400);')
    // 浅色：success 与 primary 保持品牌档位（F2：品牌色不被文本 token 接管）。
    expect(css).toContain('--ui-success: var(--ui-color-success-500);')
    // 深色：仍是 400 档（深底上已达标）。
    expect(css).toContain('--ui-success: var(--ui-color-success-400);')
  })

  test('themes native scrollbars without breaking explicit hidden scrollbars', () => {
    const css = readFileSync(new URL('../../app/assets/css/main.css', import.meta.url), 'utf8')

    expect(css).toContain('--sf-scrollbar-track:')
    expect(css).toContain('--sf-scrollbar-thumb:')
    expect(css).toContain('scrollbar-color: var(--sf-scrollbar-thumb) var(--sf-scrollbar-track);')
    expect(css).toContain('*::-webkit-scrollbar-thumb')
    expect(css).toContain('.no-scrollbar::-webkit-scrollbar')
    expect(css.indexOf('.no-scrollbar')).toBeGreaterThan(css.indexOf('*::-webkit-scrollbar-thumb'))
  })

  test('exports recommended personalization defaults', () => {
    const links = cloneFooterLinks(recommendedFooterLinks)

    expect(recommendedAppearanceTheme).toBe('pine_teal')
    expect(recommendedFooterCopyright['zh-CN']).toContain('{year}')
    expect(recommendedFooterCopyright['en-US']).toContain('{siteName}')
    expect(links.map(link => link.key)).toEqual(['terms', 'privacy', 'guidelines'])
    links[0].labels['zh-CN'] = 'Changed'
    expect(recommendedFooterLinks[0].labels['zh-CN']).toBe('服务条款')
  })
})

describe('site brand asset helpers', () => {
  test('uses the Core logo for unset logo and favicon options', () => {
    const asset = readFileSync(new URL('../../public/brand/sforum-logo.svg', import.meta.url), 'utf8')

    expect(defaultSiteLogoUrl).toBe('/brand/sforum-logo.svg')
    expect(defaultSiteFaviconUrl).toBe(defaultSiteLogoUrl)
    expect(asset).toContain('<title id="title">SForum logo</title>')
    expect(resolveSiteBrandAssetUrl('', defaultSiteLogoUrl)).toBe(defaultSiteLogoUrl)
    expect(resolveSiteBrandAssetUrl('   ', defaultSiteFaviconUrl)).toBe(defaultSiteFaviconUrl)
    expect(resolveSiteBrandAssetUrl(undefined, defaultSiteLogoUrl)).toBe(defaultSiteLogoUrl)
  })

  test('keeps an operator-configured brand asset URL authoritative', () => {
    expect(resolveSiteBrandAssetUrl(' /uploads/custom-logo.png ', defaultSiteLogoUrl))
      .toBe('/uploads/custom-logo.png')
  })
})

describe('human verification option helpers', () => {
  test('normalizes enabled scenario values', () => {
    expect(normalizeEnabledOption('enabled')).toBe(true)
    expect(normalizeEnabledOption('true')).toBe(true)
    expect(normalizeEnabledOption('0', true)).toBe(false)
    expect(normalizeEnabledOption('unexpected', true)).toBe(true)
    expect(enabledOptionValue(false)).toBe('disabled')
  })

  test('resolves ALTCHA widget settings with safe defaults', () => {
    const settings = resolveAltchaWidgetSettings({
      'human_verification.altcha.widget.type': 'switch',
      'human_verification.altcha.widget.auto': 'onfocus',
      'human_verification.altcha.widget.display': 'floating',
      'human_verification.altcha.widget.hide_logo': 'disabled',
      'human_verification.altcha.widget.hide_footer': 'enabled',
      'human_verification.altcha.widget.workers': '4',
      'human_verification.altcha.widget.min_duration_ms': '1200'
    })

    expect(settings.type).toBe('switch')
    expect(settings.auto).toBe('onfocus')
    expect(settings.display).toBe('floating')
    expect(settings.hideLogo).toBe(false)
    expect(settings.hideFooter).toBe(true)
    expect(settings.workers).toBe(4)
    expect(settings.minDuration).toBe(1200)

    const fallback = resolveAltchaWidgetSettings({
      'human_verification.altcha.widget.type': 'button',
      'human_verification.altcha.widget.auto': 'always',
      'human_verification.altcha.widget.display': 'modal',
      'human_verification.altcha.widget.workers': '99',
      'human_verification.altcha.widget.min_duration_ms': '-1'
    })
    expect(fallback.type).toBe('checkbox')
    expect(fallback.auto).toBe('off')
    expect(fallback.display).toBe('standard')
    expect(fallback.workers).toBe(2)
    expect(fallback.minDuration).toBe(500)
  })
})

describe('password policy helpers', () => {
  test('resolves recommended password policy defaults', () => {
    const policy = resolvePasswordPolicy({})

    expect(policy).toEqual(recommendedPasswordPolicy)
  })

  test('computes password requirements and progress', () => {
    const policy = resolvePasswordPolicy({
      'identity.password.min_length': '8',
      'identity.password.max_length': '64',
      'identity.password.require_uppercase': 'enabled',
      'identity.password.require_number': 'enabled',
      'identity.password.require_symbol': 'enabled'
    })

    const weak = passwordPolicyRequirements('lowercase', policy)
    expect(weak.filter(item => item.met).map(item => item.key)).toEqual(['length'])
    expect(passwordPolicyProgress('lowercase', policy)).toBe(25)

    const strong = passwordPolicyRequirements('Passw0rd!', policy)
    expect(strong.every(item => item.met)).toBe(true)
    expect(passwordPolicyProgress('Passw0rd!', policy)).toBe(100)
  })

  test('shows gradual length progress for the recommended password policy', () => {
    const policy = resolvePasswordPolicy({})

    expect(passwordPolicyProgress('', policy)).toBe(0)
    expect(passwordPolicyProgress('phrase', policy)).toBe(50)
    expect(passwordPolicyProgress('long phrase!', policy)).toBe(100)
  })

  test('maps password progress to color level', () => {
    expect(passwordPolicyProgressLevel(0)).toBe('empty')
    expect(passwordPolicyProgressLevel(1)).toBe('weak')
    expect(passwordPolicyProgressLevel(50)).toBe('weak')
    expect(passwordPolicyProgressLevel(51)).toBe('medium')
    expect(passwordPolicyProgressLevel(99)).toBe('medium')
    expect(passwordPolicyProgressLevel(100)).toBe('strong')
  })
})

describe('avatar option helpers', () => {
  test('resolves recommended avatar settings by default', () => {
    expect(resolveAvatarSettings({})).toEqual(recommendedAvatarSettings)
  })

  test('normalizes avatar provider, source, and upload limits', () => {
    const settings = resolveAvatarSettings({
      'avatar.allow_upload': 'disabled',
      'avatar.default_provider': 'gravatar',
      'avatar.gravatar_base_url': 'https://avatar.example.com/base',
      'avatar.gravatar_hash_algorithm': 'md5',
      'avatar.max_size_kb': '512',
      'avatar.allow_gif': 'enabled',
      'avatar.compress_enabled': 'disabled'
    })

    expect(settings.allowUpload).toBe(false)
    expect(settings.defaultProvider).toBe('gravatar')
    expect(settings.gravatarBaseUrl).toBe('https://avatar.example.com/base/')
    expect(settings.gravatarHashAlgorithm).toBe('md5')
    expect(settings.maxSizeKb).toBe(512)
    expect(settings.allowGif).toBe(true)
    expect(settings.compressEnabled).toBe(false)

    const fallback = resolveAvatarSettings({
      'avatar.default_provider': 'identicon',
      'avatar.gravatar_base_url': 'javascript:alert(1)',
      'avatar.gravatar_hash_algorithm': 'sha1',
      'avatar.max_size_kb': '99999',
      'avatar.max_dimension': '0',
      'avatar.target_dimension': '9000',
      'avatar.compress_quality': '101'
    })
    expect(fallback.defaultProvider).toBe(recommendedAvatarSettings.defaultProvider)
    expect(fallback.gravatarBaseUrl).toBe(recommendedAvatarSettings.gravatarBaseUrl)
    expect(fallback.gravatarHashAlgorithm).toBe(recommendedAvatarSettings.gravatarHashAlgorithm)
    expect(fallback.maxSizeKb).toBe(recommendedAvatarSettings.maxSizeKb)
    expect(fallback.maxDimension).toBe(recommendedAvatarSettings.maxDimension)
    expect(fallback.targetDimension).toBe(recommendedAvatarSettings.targetDimension)
    expect(fallback.compressQuality).toBe(recommendedAvatarSettings.compressQuality)
  })
})

describe('seo option helpers', () => {
  test('resolves independent SEO identity and content policies', () => {
    const settings = resolveSEOSettings({
      'site.name': 'SForum',
      'seo.site.inherit_site_name': 'disabled',
      'seo.site.name': 'SForum Developers',
      'seo.home.title': 'Developer Q&A',
      'seo.home.description': 'Questions and open source discussions.',
      'seo.home.keywords': 'developers,open source',
      'seo.content_type.profile.index_mode': 'index',
      'seo.content_type.topic.description_source': 'topic_summary,topic_excerpt,site_default'
    })

    expect(settings.seoSiteName).toBe('SForum Developers')
    expect(settings.homeTitle).toBe('Developer Q&A')
    expect(settings.homeKeywords).toBe('developers,open source')
    expect(settings.policies.profile.indexMode).toBe('index')
    expect(settings.policies.topic.descriptionSources).toEqual(['topic_summary', 'topic_excerpt', 'site_default'])
  })

  test('resolves runtime SEO settings with safe defaults', () => {
    const settings = resolveSEOSettings({
      'seo.twitter_card': 'summary',
      'seo.twitter_site': 'sforum_app',
      'seo.robots.extra_disallow': '/admin\n/private\n/admin',
      'seo.allow_indexing': 'enabled'
    })

    expect(settings.twitterCard).toBe('summary')
    expect(settings.twitterSite).toBe('@sforum_app')
    expect(settings.robotsExtraDisallow).toEqual(['/admin', '/private'])
    expect(settings.policies.category.indexMode).toBe('index')
    expect(settings.policies.topic.includeInSitemap).toBe(true)
    expect(settings.policies.profile.indexMode).toBe('noindex')
    expect(isSEOIndexingAllowed(settings, 'http://127.0.0.1:3000')).toBe(false)
    expect(isSEOIndexingAllowed(settings, 'https://forum.example.com')).toBe(true)
  })

  test('normalizes SEO token, robots paths, and title templates', () => {
    expect(normalizeSEOVerificationToken(' google-token ')).toBe('google-token')
    expect(normalizeSEOVerificationToken('<script>')).toBe('')
    expect(parseSEORobotsPathList('/ok\nrelative\n//bad\n/path?q=1')).toEqual(['/ok', '/path?q=1'])
    expect(applyAdminSEOTitleTemplate('用户管理', '管理后台', 'SForum')).toBe('用户管理 - 管理后台 - SForum')
    expect(applyAdminSEOTitleTemplate('', '管理后台', 'SForum')).toBe('管理后台 - SForum')
    expect(applyAdminSEOTitleTemplate('Users', 'Admin', 'My Forum')).toBe('Users - Admin - My Forum')
  })
})
