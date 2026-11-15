import { beforeAll, describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, flushPromises, mount, testVue } from '../helpers/vueSfc'
import * as installUtils from '../../app/utils/admin/extensions/extensionInstall'
import { adminExtensionInstallErrorMessage } from '../../app/utils/admin/extensions/hostActions'
import { useAdminExtensionInstall } from '../../app/composables/admin/useAdminExtensionInstall'
import type { AdminExtensionInstallResult } from '../../app/utils/admin/adminExtensions'

const panelPath = fileURLToPath(new URL('../../app/components/admin/extensions/SFAdminExtensionInstallPanel.vue', import.meta.url))

let Panel: Awaited<ReturnType<typeof compileVueSfc>>
let toasts: unknown[] = []
let archiveCalls: Array<{ name: string, size: number }> = []
let urlCalls: Array<{ url: string, sha256: string }> = []
let archiveResult: AdminExtensionInstallResult
let archiveError: Error | null = null
let urlError: Error | null = null

beforeAll(async () => {
  Object.assign(globalThis, {
    ref: testVue.ref,
    computed: testVue.computed,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key}:${JSON.stringify(params)}` : key
    }),
    useToast: () => ({ add: (toast: unknown) => { toasts.push(toast) } }),
    useAdminRoutes: () => ({ path: (path: string) => `/control-panel${path}` }),
    useAdminExtensionInstall: () => ({
      installArchive: async (file: File) => {
        archiveCalls.push({ name: file.name, size: file.size })
        if (archiveError) throw archiveError
        return archiveResult
      },
      installFromUrl: async (url: string, sha256: string) => {
        urlCalls.push({ url, sha256 })
        if (urlError) throw urlError
        return archiveResult
      }
    }),
    apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : ''
  })
  Panel = await compileVueSfc(panelPath, 'extension-install-panel', {
    EXTENSION_ARCHIVE_ACCEPT: installUtils.EXTENSION_ARCHIVE_ACCEPT,
    EXTENSION_ARCHIVE_MAX_BYTES: installUtils.EXTENSION_ARCHIVE_MAX_BYTES,
    formatFileSize: installUtils.formatFileSize,
    normalizeRemoteArchiveUrl: installUtils.normalizeRemoteArchiveUrl,
    normalizeSha256: installUtils.normalizeSha256,
    remoteArchiveHost: installUtils.remoteArchiveHost,
    validateArchiveFile: installUtils.validateArchiveFile,
    adminExtensionInstallErrorMessage
  })
})

function resetState() {
  toasts = []
  archiveCalls = []
  urlCalls = []
  archiveError = null
  urlError = null
  archiveResult = {
    upgraded: false,
    extension: {
      id: 'demo.plugin',
      name: 'Demo Plugin',
      version: '1.0.0',
      type: 'plugin',
      status: 'installed',
      manifest: {
        id: 'demo.plugin',
        name: 'Demo Plugin',
        version: '1.0.0',
        type: 'plugin'
      } as AdminExtensionInstallResult['extension']['manifest'],
      packageDigest: 'digest-1',
      packagePath: '/tmp/demo',
      installedAt: '2026-10-23T00:00:00Z',
      updatedAt: '2026-10-23T00:00:00Z'
    }
  }
}

function mountPanel() {
  return mount(Panel, { global: { stubs: uiStubs() } })
}

/** FormField root that owns `testId`; Nuxt UI renders exactly one per field. */
function fieldRoot(wrapper: ReturnType<typeof mount>, testId: string) {
  return wrapper.get(`[data-testid="${testId}"]`).element.closest('[data-slot="root"]')
}

/** That field's own field-level message, read from its `data-slot="error"` region. */
function fieldErrorText(wrapper: ReturnType<typeof mount>, testId: string) {
  return fieldRoot(wrapper, testId)?.querySelector('[data-slot="error"]')?.textContent?.trim() || ''
}

function uiStubs() {
  return {
    UCard: { template: '<section><slot name="header" /><slot /></section>' },
    UButton: {
      props: ['disabled', 'loading', 'type', 'to'],
      emits: ['click'],
      template: '<button :disabled="disabled" @click="$emit(\'click\', $event)"><slot /></button>'
    },
    UInput: {
      props: ['modelValue', 'disabled'],
      emits: ['update:modelValue'],
      template: '<input :value="modelValue" :disabled="disabled" @input="$emit(\'update:modelValue\', $event.target.value)">'
    },
    // 与 Nuxt UI 4 的 FormField 同构：root > wrapper(labelWrapper>label, description)
    // + container(slot, error)。错误是输入框的同级节点，不在 <label> 内 —— stub 若把它
    // 塞进 label，`closest('label')` 在生产 DOM 上就会返回 null，断言等于没测。
    UFormField: {
      props: ['label', 'error', 'description'],
      template: `<div data-slot="root">
        <div data-slot="wrapper">
          <div data-slot="labelWrapper"><span data-slot="label">{{ label }}</span></div>
          <p v-if="description" data-slot="description">{{ description }}</p>
        </div>
        <div data-slot="container">
          <slot />
          <div v-if="typeof error === 'string' && error" data-slot="error" data-form-error>{{ error }}</div>
        </div>
      </div>`
    },
    UAlert: { props: ['title', 'description'], template: '<div data-alert>{{ title }} {{ description }}</div>' },
    UBadge: { template: '<span><slot /></span>' },
    UIcon: { template: '<i />' }
  }
}

async function chooseFile(wrapper: ReturnType<typeof mount>, file: File) {
  const input = wrapper.get('[data-testid="extension-archive-input"]')
  Object.defineProperty(input.element, 'files', { value: [file], configurable: true })
  await input.trigger('change')
  await flushPromises()
}

describe('extension install panel', () => {
  test('rejects a non-zip file without calling the installer', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await chooseFile(wrapper, new File([new Uint8Array([1])], 'demo.txt', { type: 'text/plain' }))
      expect(wrapper.text()).toContain('admin.extensionInstall.upload.errorType')
      expect(archiveCalls).toHaveLength(0)
    } finally {
      wrapper.unmount()
    }
  })

  test('uploads a valid zip and renders the install result', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await chooseFile(wrapper, new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], 'demo.zip', { type: 'application/zip' }))
      await wrapper.get('[data-testid="extension-archive-submit"]').trigger('click')
      await flushPromises()
      expect(archiveCalls).toEqual([{ name: 'demo.zip', size: 4 }])
      expect(wrapper.get('[data-testid="extension-install-result"]').text()).toContain('Demo Plugin')
      expect(wrapper.get('[data-testid="extension-install-result"]').text()).toContain('demo.plugin')
      // 上传没有摘要可校验：结果区不得顶着一个「已校验」徽标。
      expect(wrapper.get('[data-testid="extension-install-result"]').text())
        .not.toContain('admin.extensionInstall.result.checksumVerified')
      expect(toasts).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('submits an https url with a pinned sha256', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await wrapper.get('[data-testid="extension-url-input"]').setValue('https://example.com/demo.zip')
      await wrapper.get('[data-testid="extension-url-checksum"]').setValue('a'.repeat(64))
      await wrapper.get('[data-testid="extension-url-form"]').trigger('submit')
      await flushPromises()
      expect(urlCalls).toEqual([{ url: 'https://example.com/demo.zip', sha256: 'a'.repeat(64) }])
      expect(wrapper.get('[data-testid="extension-install-result"]').text()).toContain('Demo Plugin')
      // 链接安装带着已校验的摘要，徽标成立。
      expect(wrapper.get('[data-testid="extension-install-result"]').text())
        .toContain('admin.extensionInstall.result.checksumVerified')
      expect(fieldErrorText(wrapper, 'extension-url-checksum')).toBe('')
    } finally {
      wrapper.unmount()
    }
  })

  test('drops the digest badge when a url install is followed by an upload', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await wrapper.get('[data-testid="extension-url-input"]').setValue('https://example.com/demo.zip')
      await wrapper.get('[data-testid="extension-url-checksum"]').setValue('a'.repeat(64))
      await wrapper.get('[data-testid="extension-url-form"]').trigger('submit')
      await flushPromises()
      expect(wrapper.get('[data-testid="extension-install-result"]').text())
        .toContain('admin.extensionInstall.result.checksumVerified')

      // 小审的探针场景：同页接着上传本地包，结果区换成上传结果后徽标必须消失。
      await chooseFile(wrapper, new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], 'demo.zip', { type: 'application/zip' }))
      await wrapper.get('[data-testid="extension-archive-submit"]').trigger('click')
      await flushPromises()
      const card = wrapper.get('[data-testid="extension-install-result"]').text()
      expect(card).toContain('Demo Plugin')
      expect(card).not.toContain('admin.extensionInstall.result.checksumVerified')
    } finally {
      wrapper.unmount()
    }
  })

  test('rejects a non-https url before any request', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await wrapper.get('[data-testid="extension-url-input"]').setValue('http://example.com/demo.zip')
      await wrapper.get('[data-testid="extension-url-form"]').trigger('submit')
      await flushPromises()
      expect(urlCalls).toHaveLength(0)
      expect(wrapper.text()).toContain('admin.extensionInstall.url.errorScheme')
    } finally {
      wrapper.unmount()
    }
  })

  test('rejects a malformed sha256 before any request', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await wrapper.get('[data-testid="extension-url-input"]').setValue('https://example.com/demo.zip')
      await wrapper.get('[data-testid="extension-url-checksum"]').setValue('not-a-digest')
      await wrapper.get('[data-testid="extension-url-form"]').trigger('submit')
      await flushPromises()
      expect(urlCalls).toHaveLength(0)
      expect(fieldErrorText(wrapper, 'extension-url-checksum')).toContain('admin.extensionInstall.url.errorChecksum')
    } finally {
      wrapper.unmount()
    }
  })

  test('rejects an empty sha256 on the checksum field without any request', async () => {
    resetState()
    const wrapper = mountPanel()
    try {
      await wrapper.get('[data-testid="extension-url-input"]').setValue('https://example.com/demo.zip')
      await wrapper.get('[data-testid="extension-url-form"]').trigger('submit')
      await flushPromises()
      // 空摘要是字段级错误：既不发起请求，也不弹 toast。
      expect(urlCalls).toHaveLength(0)
      expect(toasts).toHaveLength(0)
      // stub 与生产结构一致：输入框不在 <label> 内，错误是它的同级节点。
      expect(wrapper.get('[data-testid="extension-url-checksum"]').element.closest('label')).toBeNull()
      expect(fieldErrorText(wrapper, 'extension-url-checksum')).toContain('admin.extensionInstall.url.errorChecksumRequired')
      expect(fieldErrorText(wrapper, 'extension-url-input')).toBe('')
    } finally {
      wrapper.unmount()
    }
  })

  test('keeps a blocking install error next to the form', async () => {
    resetState()
    archiveError = new Error('upload rejected')
    const wrapper = mountPanel()
    try {
      await chooseFile(wrapper, new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], 'demo.zip', { type: 'application/zip' }))
      await wrapper.get('[data-testid="extension-archive-submit"]').trigger('click')
      await flushPromises()
      // 非信封错误按面板兜底文案呈现，不把传输层文本直接甩给管理员。
      expect(wrapper.text()).toContain('admin.extensionInstall.upload.failed')
      expect(toasts).toHaveLength(1)
    } finally {
      wrapper.unmount()
    }
  })

  test('maps a stable API reason to the shared install message', async () => {
    resetState()
    archiveError = Object.assign(new Error('mismatch'), {
      code: 422,
      message: 'extension.expected_mismatch',
      data: { reason: 'extension.expected_mismatch' }
    })
    const wrapper = mountPanel()
    try {
      await chooseFile(wrapper, new File([new Uint8Array([0x50, 0x4b, 0x03, 0x04])], 'demo.zip', { type: 'application/zip' }))
      await wrapper.get('[data-testid="extension-archive-submit"]').trigger('click')
      await flushPromises()
      expect(wrapper.text()).toContain('admin.extensionInstall.errors.expectedMismatch')
    } finally {
      wrapper.unmount()
    }
  })
})

describe('extension install helpers', () => {
  test('validates archive files', () => {
    expect(installUtils.validateArchiveFile(null)).toEqual({ ok: false, reason: 'empty' })
    expect(installUtils.validateArchiveFile({ name: 'demo.zip', size: 0 })).toEqual({ ok: false, reason: 'empty' })
    expect(installUtils.validateArchiveFile({ name: 'demo.txt', size: 10 })).toEqual({ ok: false, reason: 'type' })
    expect(installUtils.validateArchiveFile({ name: 'demo.zip', size: installUtils.EXTENSION_ARCHIVE_MAX_BYTES + 1 })).toEqual({ ok: false, reason: 'size' })
    expect(installUtils.validateArchiveFile({ name: 'demo.zip', size: 10 })).toEqual({ ok: true })
    expect(installUtils.validateArchiveFile({ name: 'demo', type: 'application/zip', size: 10 })).toEqual({ ok: true })
  })

  test('normalizes remote archive urls', () => {
    expect(installUtils.normalizeRemoteArchiveUrl('')).toEqual({ ok: false, reason: 'empty' })
    expect(installUtils.normalizeRemoteArchiveUrl('http://example.com/demo.zip')).toEqual({ ok: false, reason: 'scheme' })
    expect(installUtils.normalizeRemoteArchiveUrl('https://user:pass@example.com/demo.zip')).toEqual({ ok: false, reason: 'credentials' })
    expect(installUtils.normalizeRemoteArchiveUrl('not a url')).toEqual({ ok: false, reason: 'invalid' })
    expect(installUtils.normalizeRemoteArchiveUrl(`https://example.com/${'a'.repeat(2048)}`)).toEqual({ ok: false, reason: 'length' })
    expect(installUtils.normalizeRemoteArchiveUrl('https://example.com/demo.zip')).toEqual({ ok: true, url: 'https://example.com/demo.zip' })
  })

  test('treats sha256 as a required, well-formed digest', () => {
    expect(installUtils.normalizeSha256('')).toEqual({ ok: false, reason: 'empty' })
    expect(installUtils.normalizeSha256('   ')).toEqual({ ok: false, reason: 'empty' })
    expect(installUtils.normalizeSha256('not-a-digest')).toEqual({ ok: false, reason: 'format' })
    expect(installUtils.normalizeSha256('A'.repeat(64))).toEqual({ ok: true, value: 'a'.repeat(64) })
  })

  test('formats byte sizes for the drop hint', () => {
    expect(installUtils.formatFileSize(0)).toBe('0 B')
    expect(installUtils.formatFileSize(1024)).toBe('1 KB')
    expect(installUtils.formatFileSize(50 * 1024 * 1024)).toBe('50 MB')
  })
})

describe('install transport', () => {
  test('sends sha256 as a required body field instead of omitting it', async () => {
    const requests: Array<{ path: string, body: Record<string, unknown> }> = []
    Object.assign(globalThis, {
      useApiClient: () => ({
        request: async (path: string, config: { body: Record<string, unknown> }) => {
          requests.push({ path, body: config.body })
          return { upgraded: false, extension: {} }
        }
      })
    })
    const { installFromUrl } = useAdminExtensionInstall()
    await installFromUrl('https://example.com/demo.zip', 'a'.repeat(64))
    expect(requests.map(entry => entry.path)).toEqual(['/admin/extensions/install-from-url'])
    expect(requests[0]?.body).toEqual({ url: 'https://example.com/demo.zip', sha256: 'a'.repeat(64) })
  })
})

describe('required digest message', () => {
  test('maps the 422 reason to its own bilingual message instead of the generic failure', () => {
    const error = {
      code: 422,
      message: 'extension.remote_digest_required',
      data: { reason: 'extension.remote_digest_required' }
    }
    expect(adminExtensionInstallErrorMessage(key => key, error, 'admin.extensionInstall.url.failed'))
      .toBe('admin.extensionInstall.errors.remoteDigestRequired')

    // 只补一种语言会被回退掩盖，所以两种语言都钉住；摘要也不再是「可选」。
    const localeDir = join(import.meta.dir, '../../i18n/locales')
    for (const name of ['zh-CN.json', 'en-US.json']) {
      const locale = JSON.parse(readFileSync(join(localeDir, name), 'utf8')) as {
        admin: { extensionInstall: { url: Record<string, string>, errors: Record<string, string> } }
      }
      const block = locale.admin.extensionInstall
      expect(block.errors.remoteDigestRequired, `${name}: errors.remoteDigestRequired`).toBeTruthy()
      expect(block.url.errorChecksumRequired, `${name}: url.errorChecksumRequired`).toBeTruthy()
      expect(block.url.checksumLabel, `${name}: checksumLabel`).not.toMatch(/optional|可选/i)
    }
  })
})
