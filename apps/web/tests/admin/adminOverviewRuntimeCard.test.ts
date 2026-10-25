import { beforeAll, describe, expect, test } from 'bun:test'
import { fileURLToPath } from 'node:url'
import { compileVueSfc, mount, testVue } from '../helpers/vueSfc'

const cardPath = fileURLToPath(new URL('../../app/components/admin/SFAdminOverviewRuntimeCard.vue', import.meta.url))
let RuntimeCard: Awaited<ReturnType<typeof compileVueSfc>>

const runtime = {
  startedAt: '2026-10-26T00:00:00.000Z',
  uptimeSeconds: 1140,
  build: {
    name: 'SForum',
    version: 'dev',
    commit: '6f94488addd5',
    builtAt: '2026-10-26T01:40:34+08:00',
    goVersion: 'go1.27.1',
    platform: 'linux/amd64',
    dirty: true,
    sourceUrl: 'https://github.com/zhuchunshu/SForum'
  },
  memoryBytes: 1024,
  heapAllocBytes: 512,
  heapSysBytes: 768,
  sysBytes: 2048,
  pluginChildCount: 0,
  goroutineCount: 349,
  gcCount: 249,
  lastGcPauseNs: 0,
  database: { maxConnections: 10, totalConnections: 2, acquiredConnections: 1, idleConnections: 1 },
  worker: { stale: false, status: 'ok' as const, ageSeconds: 9 },
  queueLag: { waiting: 3, running: 0, failed: 2 }
}

beforeAll(async () => {
  Object.assign(globalThis, {
    computed: testVue.computed,
    useI18n: () => ({ t: (key: string) => key })
  })
  RuntimeCard = await compileVueSfc(cardPath, 'admin-overview-runtime-card', {
    useI18n: () => ({ t: (key: string) => key }),
    formatOverviewBytes: (value: number) => `${value} B`,
    formatOverviewCommit: (value: string) => value.slice(0, 12),
    formatOverviewCount: (value: number) => String(value),
    formatOverviewDate: () => '2026-10-26 01:40',
    formatOverviewUptime: (seconds: number) => `${seconds}s`
  })
})

describe('admin overview runtime card', () => {
  test('renders the build target platform row next to the Go version', () => {
    const wrapper = mount(RuntimeCard, {
      props: { runtime },
      global: {
        stubs: {
          UCard: { template: '<section><slot name="header" /><slot /></section>' },
          UButton: { template: '<button><slot /></button>' },
          UIcon: true
        }
      }
    })

    try {
      const labels = wrapper.findAll('span').map(node => node.text())
      expect(labels).toContain('admin.home.runtime.goVersion')
      expect(labels).toContain('admin.home.runtime.arch')
      expect(labels).toContain('linux/amd64')
    } finally {
      wrapper.unmount()
    }
  })
})
