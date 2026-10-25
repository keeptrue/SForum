import { describe, expect, test } from 'bun:test'

const component = await Bun.file(new URL('../../app/components/extensions/settings/SFTrustedSettingsComponent.vue', import.meta.url)).text()
const trustPanel = await Bun.file(new URL('../../app/components/admin/SFAdminFrontendTrustPanel.vue', import.meta.url)).text()
const sdk = await Bun.file(new URL('../../packages/admin-sdk/src/index.ts', import.meta.url)).text()
const fixture = await Bun.file(new URL('../../../../extensions/fixtures/plugins/sforum-prebuilt-settings/frontend/admin/dist/settings.mjs', import.meta.url)).text()
const manifest = JSON.parse(await Bun.file(new URL('../../../../extensions/fixtures/plugins/sforum-prebuilt-settings/sforum.extension.json', import.meta.url)).text())

describe('prebuilt extension settings component runtime', () => {
  test('loads only the authenticated immutable digest endpoint and validates API v1', () => {
    expect(component).toContain('/api/v1/admin/extensions/${encodeURIComponent(props.extension.id)}/frontend/assets/${digest.value}/${name}')
    expect(component).not.toContain('/_sforum/private-assets/extensions/')
    expect(component).toContain("fetch(assetURL('style'), { credentials: 'include' })")
    expect(component).toContain("contentType.startsWith('text/css')")
    expect(component).toContain('import(/* @vite-ignore */ assetURL(\'entry\'))')
    expect(component).toContain('module.apiVersion !== ADMIN_MICRO_FRONTEND_API_VERSION')
    expect(component).toContain("typeof module.mount !== 'function'")
    expect(component).not.toMatch(/https?:\/\//)
    expect(component).not.toContain('eval(')
  })

  test('cleans up, quarantines repeated failures, and always exposes Schema fallback', () => {
    expect(component).toContain('await currentCleanup()')
    expect(component).toContain('recordContributionFailure')
    expect(component).toContain('contributionFailureState')
    expect(component).toContain('loadQueue.then(performLoad, performLoad)')
    expect(component).toContain('<slot name="fallback" />')
    expect(component).toContain('componentFallback')
  })

  test('requires an intentional fully-trusted confirmation in the operator UI', () => {
    expect(trustPanel).toContain('confirmationPhrase.value === extension.value.id')
    expect(trustPanel).toContain('acknowledged.value')
    expect(trustPanel).toContain('currentChallenge.value?.code')
    expect(trustPanel).toContain('challengeId: currentChallenge.value.challengeId')
    expect(trustPanel).toContain('fullTrustDescription')
    expect(trustPanel).toContain('componentId: component.id')
    expect(trustPanel).toContain('digest')
  })

  test('publishes a framework-neutral bridge and a prebuilt fixture with cleanup', () => {
    expect(sdk).toContain('AdminMicroFrontendBridgeV1')
    expect(sdk).toContain('values: () => Readonly<Record<string, string>>')
    expect(sdk).toContain('request: <T>')
    expect(sdk).toContain('AdminMicroFrontendCleanup')
    // Host build identity is optional on the bridge and must be forwarded from
    // the bootstrap payload rather than fetched again by the component.
    expect(sdk).toContain('host?: AdminHostIdentity')
    expect(component).toContain('host: props.host')
    expect(fixture).toContain('export const apiVersion = 1')
    expect(fixture).toContain('export function mount(target, bridge)')
    expect(fixture).toContain('removeEventListener')
    expect(manifest.settings.ui.component.entry).toEndWith('.mjs')
    expect(manifest.settings.fields.length).toBeGreaterThan(0)
  })
})
