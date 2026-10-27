import { describe, expect, test } from 'bun:test'

import {
  ADMIN_OVERVIEW_KPI_POLL_MS,
  ADMIN_OVERVIEW_RESOURCE_POLL_MS,
  applyOverviewResources,
  formatOverviewBytes,
  formatOverviewCount,
  formatOverviewCommit,
  formatOverviewDate,
  formatOverviewLoad,
  formatOverviewPercent,
  formatOverviewStorage,
  formatOverviewTrendDayCount,
  formatOverviewVersion,
  overviewActionTone,
  overviewActionsTone,
  overviewActionTotal,
  overviewAdvisoryTotal,
  overviewMemoryDisplayBytes,
  overviewPSSDisplayBytes,
  overviewPercent,
  overviewTrendBarHeightPx,
  overviewTrendDateLabel,
  overviewTrendDeltaKind,
  overviewTrendDeltaPercent,
  overviewTrendFieldMax,
  overviewTrendMax,
  overviewTrendPeakDate,
  overviewTrendSparkPath,
  overviewTrendSum,
  type AdminOverview,
  type AdminOverviewAction,
  type AdminOverviewRuntime,
  type AdminOverviewTrendDay
} from '../../app/utils/admin/adminOverview'

describe('admin overview helpers', () => {
  test('live poll intervals are 5s resources and 30s KPI', () => {
    expect(ADMIN_OVERVIEW_RESOURCE_POLL_MS).toBe(5000)
    expect(ADMIN_OVERVIEW_KPI_POLL_MS).toBe(30_000)
  })

  test('applyOverviewResources merges resource fields without clearing omitted samples', () => {
    const base: AdminOverview = {
      generatedAt: '2026-07-31T00:00:00.000Z',
      windowDays: 7,
      runtime: {
        startedAt: '2026-07-31T00:00:00.000Z',
        uptimeSeconds: 10,
        build: {
          name: 'SForum',
          version: 'dev',
          goVersion: 'go1.25',
          platform: 'linux/amd64',
          dirty: false,
          sourceUrl: 'https://example.com'
        },
        memoryBytes: 1,
        heapAllocBytes: 1,
        heapSysBytes: 1,
        sysBytes: 1,
        pluginChildCount: 0,
        resources: {
          apiMemoryBytes: 10,
          workerMemoryBytes: 0,
          pluginMemoryBytes: 0,
          totalMemoryBytes: 10,
          apiMemoryMedianBytes: 10,
          workerMemoryMedianBytes: 0,
          pluginMemoryMedianBytes: 0,
          totalMemoryMedianBytes: 10,
          memorySampleCount: 1,
          memoryWindowSeconds: 60,
          apiCpuPercent: 1,
          workerCpuPercent: 0,
          pluginCpuPercent: 0,
          totalCpuPercent: 1,
          pluginChildCount: 0,
          pluginOverlapCount: 0,
          workerFound: false,
          workerEmbedded: true,
          workerConcurrency: 7
        },
        disk: {
          totalBytes: 100,
          usedBytes: 40,
          freeBytes: 60,
          usedPercent: 40
        },
        loadAverage: {
          oneMinute: 0.25,
          fiveMinutes: 0.5,
          fifteenMinutes: 0.75
        },
        goroutineCount: 1,
        gcCount: 0,
        lastGcPauseNs: 0,
        database: {
          maxConnections: 1,
          totalConnections: 0,
          acquiredConnections: 0,
          idleConnections: 0
        }
      },
      community: {
        userCount: 0,
        activeUserCount: 0,
        disabledUserCount: 0,
        bannedUserCount: 0,
        topicCount: 0,
        activeTopicCount: 0,
        lockedTopicCount: 0,
        hiddenTopicCount: 0,
        deletedTopicCount: 0,
        commentCount: 0,
        postCount: 0,
        categoryCount: 0,
        tagCount: 0,
        pendingTagCount: 0,
        totalViews: 0
      },
      attachments: {
        totalCount: 0,
        activeCount: 0,
        disabledCount: 0,
        deletedCount: 0,
        orphanCount: 0,
        totalBytes: 0
      },
      moderation: {
        openCount: 0,
        reviewingCount: 0,
        resolvedCount: 0,
        rejectedCount: 0
      },
      extensions: {
        totalCount: 0,
        enabledCount: 0,
        pluginCount: 0,
        themeCount: 0,
        installedPluginRuntimeCount: 0,
        failedEventCount: 0
      },
      trends: { days: [] },
      topCategories: [],
      actions: []
    }

    const next = applyOverviewResources(base, {
      generatedAt: '2026-07-31T00:00:02.000Z',
      resources: {
        ...base.runtime.resources!,
        apiMemoryBytes: 20,
        totalMemoryBytes: 20,
        apiCpuPercent: 2,
        totalCpuPercent: 2
      }
    })
    expect(next.runtime.resources?.apiMemoryBytes).toBe(20)
    expect(next.runtime.disk?.usedPercent).toBe(40)
    expect(next.runtime.loadAverage?.oneMinute).toBe(0.25)

    const kept = applyOverviewResources(base, { generatedAt: '2026-07-31T00:00:04.000Z' })
    expect(kept.runtime.resources?.apiMemoryBytes).toBe(10)
    expect(kept.runtime.disk?.usedPercent).toBe(40)
    expect(kept.runtime.loadAverage?.fifteenMinutes).toBe(0.75)
  })

  test('formats memory bytes as compact MiB labels', () => {
    expect(formatOverviewBytes(0)).toBe('0 MiB')
    expect(formatOverviewBytes(1024 * 1024)).toBe('1 MiB')
    expect(formatOverviewBytes(1536 * 1024 * 1024)).toBe('1,536 MiB')
  })

  test('prefers rolling memory medians and falls back to instant RSS for old frames', () => {
    const resources = {
      apiMemoryBytes: 210,
      workerMemoryBytes: 0,
      pluginMemoryBytes: 120,
      totalMemoryBytes: 330,
      apiMemoryMedianBytes: 200,
      workerMemoryMedianBytes: 0,
      pluginMemoryMedianBytes: 100,
      totalMemoryMedianBytes: 300,
      totalPssBytes: 270,
      totalPssMedianBytes: 240,
      memorySampleCount: 12,
      pssSampleCount: 10,
      memoryWindowSeconds: 60,
      apiCpuPercent: 1,
      workerCpuPercent: 0,
      pluginCpuPercent: 0.5,
      totalCpuPercent: 1.5,
      pluginChildCount: 7,
      pluginOverlapCount: 0,
      workerFound: false,
      workerEmbedded: true,
      workerConcurrency: 7
    }
    expect(overviewMemoryDisplayBytes(resources, 'api')).toBe(200)
    expect(overviewMemoryDisplayBytes(resources, 'plugin')).toBe(100)
    expect(overviewMemoryDisplayBytes({ ...resources, memorySampleCount: 0 }, 'total')).toBe(330)
    expect(overviewPSSDisplayBytes(resources, 'total')).toBe(240)
    expect(overviewPSSDisplayBytes({ ...resources, pssSampleCount: 0 }, 'total')).toBe(270)
  })

  test('formats resource percentages and disk sizes compactly', () => {
    expect(formatOverviewPercent(0)).toBe('0%')
    expect(formatOverviewPercent(12.5)).toBe('12.5%')
    expect(formatOverviewLoad(0.125)).toBe('0.13')
    expect(formatOverviewStorage(1024 * 1024 * 1024)).toBe('1 GiB')
    expect(formatOverviewStorage(1536 * 1024 * 1024)).toBe('1.5 GiB')
  })

  test('runtime memory types expose RSS primary, Sys diagnostic, and family fields', () => {
    // 驱动真实类型形状：主 KPI 为 memoryBytes(RSS)，Sys/全家为独立字段。
    const runtime: AdminOverviewRuntime = {
      startedAt: '2026-07-21T00:00:00.000Z',
      uptimeSeconds: 60,
      build: {
        name: 'SForum',
        version: '2.8.0',
        commit: '0123456789abcdef',
        builtAt: '2026-07-29T02:00:00Z',
        goVersion: 'go1.26.5',
        platform: 'linux/arm64',
        dirty: false,
        sourceUrl: 'https://github.com/keeptrue/SForum'
      },
      memoryBytes: 160 * 1024 * 1024,
      heapAllocBytes: 80 * 1024 * 1024,
      heapSysBytes: 100 * 1024 * 1024,
      sysBytes: 280 * 1024 * 1024,
      familyMemoryBytes: 230 * 1024 * 1024,
      pluginChildCount: 4,
      goroutineCount: 40,
      gcCount: 3,
      lastGcPauseNs: 1000,
      database: {
        maxConnections: 10,
        totalConnections: 2,
        acquiredConnections: 1,
        idleConnections: 1
      }
    }
    expect(runtime.memoryBytes).toBeLessThan(runtime.sysBytes)
    expect(runtime.familyMemoryBytes).toBeGreaterThanOrEqual(runtime.memoryBytes)
    expect(runtime.pluginChildCount).toBe(4)
    expect(formatOverviewBytes(runtime.memoryBytes)).toBe('160 MiB')
    expect(formatOverviewBytes(runtime.familyMemoryBytes!)).toBe('230 MiB')
    expect(formatOverviewCommit(runtime.build.commit!)).toBe('0123456789ab')
    expect(formatOverviewVersion(runtime.build.name, runtime.build.version)).toBe('SForum v2.8.0')
    expect(formatOverviewVersion('SForum', 'dev')).toBe('SForum dev')
    expect(formatOverviewVersion('SForum', 'dev', '0123456789abcdef')).toBe('SForum dev-01234')
  })

  test('formats large counts for dashboard cards', () => {
    expect(formatOverviewCount(999)).toBe('999')
    expect(formatOverviewCount(1200)).toBe('1.2k')
    expect(formatOverviewCount(1_250_000)).toBe('1.3m')
  })

  test('formats trend day chips without long decimals', () => {
    expect(formatOverviewTrendDayCount(0)).toBe('0')
    expect(formatOverviewTrendDayCount(128)).toBe('128')
    expect(formatOverviewTrendDayCount(1200)).toBe('1.2k')
    expect(formatOverviewTrendDayCount(128_200)).toBe('128k')
    expect(formatOverviewTrendDayCount(6_000)).toBe('6k')
  })

  test('formats generated timestamps without locale-dependent output', () => {
    const localDate = new Date(2026, 6, 8, 2, 54, 29)
    expect(formatOverviewDate(localDate.toISOString())).toBe('2026-07-08 02:54:29')
    expect(formatOverviewDate('not-a-date')).toBe('')
  })

  test('calculates safe percentages', () => {
    expect(overviewPercent(5, 10)).toBe(50)
    expect(overviewPercent(1, 3)).toBe(33)
    expect(overviewPercent(5, 0)).toBe(0)
  })

  test('finds a non-zero trend maximum for bar charts', () => {
    const days: AdminOverviewTrendDay[] = [
      { date: '2026-07-02', topicCount: 0, commentCount: 0, userCount: 0 },
      { date: '2026-07-03', topicCount: 3, commentCount: 8, userCount: 1 },
      { date: '2026-07-04', topicCount: 2, commentCount: 5, userCount: 4 }
    ]

    expect(overviewTrendMax(days)).toBe(12)
    expect(overviewTrendMax([])).toBe(1)
  })

  test('builds independent sparkline / bar helpers for 01D trend cards', () => {
    const days: AdminOverviewTrendDay[] = [
      { date: '2026-07-02', topicCount: 0, commentCount: 0, userCount: 0 },
      { date: '2026-07-03', topicCount: 3, commentCount: 8, userCount: 1 },
      { date: '2026-07-04', topicCount: 2, commentCount: 5, userCount: 4 }
    ]

    expect(overviewTrendFieldMax(days, 'commentCount')).toBe(8)
    expect(overviewTrendFieldMax([], 'topicCount')).toBe(1)
    expect(overviewTrendSum(days, 'userCount')).toBe(5)
    expect(overviewTrendDeltaPercent(4, 1)).toBe(300)
    expect(overviewTrendDeltaPercent(0, 0)).toBe(0)
    expect(overviewTrendDeltaPercent(5, 0)).toBe(100)
    expect(overviewTrendDeltaKind(0, 0)).toBe('none')
    expect(overviewTrendDeltaKind(1, 1)).toBe('flat')
    expect(overviewTrendDeltaKind(0, 4)).toBe('down')
    expect(overviewTrendDeltaKind(4, 0)).toBe('up')
    expect(overviewTrendBarHeightPx(0, 8, 72)).toBe(2)
    expect(overviewTrendBarHeightPx(8, 8, 72)).toBe(72)
    expect(overviewTrendPeakDate(days, 'commentCount')).toBe('2026-07-03')
    expect(overviewTrendDateLabel('2026-07-03')).toBe('07-03')

    const spark = overviewTrendSparkPath([0, 3, 2], 280, 72, 8)
    expect(spark.points).toHaveLength(3)
    expect(spark.line.startsWith('M ')).toBe(true)
    expect(spark.area.includes('Z')).toBe(true)
    // 独立刻度：用户峰值 4 的 y 应接近顶部 pad，而不是被回复量级压扁
    const userSpark = overviewTrendSparkPath([0, 1, 4], 280, 72, 8)
    expect(userSpark.points[2].y).toBeLessThan(userSpark.points[0].y)
  })

  test('maps action severity to visual tone', () => {
    expect(overviewActionTone('danger')).toBe('danger')
    expect(overviewActionTone('warning')).toBe('warning')
    expect(overviewActionTone('info')).toBe('info')
    expect(overviewActionTone('unknown')).toBe('neutral')
  })

  test('excludes advisory actions from the pending KPI total', () => {
    const actions: AdminOverviewAction[] = [
      { key: 'moderation_queue', count: 3, severity: 'warning', route: '/moderation' },
      { key: 'orphan_attachments', count: 49, severity: 'info', route: '/attachments', advisory: true }
    ]
    expect(overviewActionTotal(actions)).toBe(3)
    expect(overviewAdvisoryTotal(actions)).toBe(49)
    expect(overviewActionsTone(actions)).toBe('warning')
  })

  test('keeps the pending KPI calm when only advisory actions have values', () => {
    const actions: AdminOverviewAction[] = [
      { key: 'moderation_queue', count: 0, severity: 'success', route: '/moderation' },
      { key: 'orphan_attachments', count: 49, severity: 'info', route: '/attachments', advisory: true }
    ]
    expect(overviewActionTotal(actions)).toBe(0)
    expect(overviewAdvisoryTotal(actions)).toBe(49)
    expect(overviewActionsTone(actions)).toBe('success')
  })
})
