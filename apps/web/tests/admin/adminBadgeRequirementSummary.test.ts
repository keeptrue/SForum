import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { nextTick, ref } from 'vue'
import { useBadgeRuleTextPreview } from '../../app/composables/admin/badges/useBadgeRuleTextPreview'
import { toAdminBadgeInput } from '../../app/utils/admin/adminBadges'
import type { BadgeRule } from '../../app/utils/admin/badgeRule'

/**
 * 公开说明现在只有**一个来源**：服务端按规则生成。
 *
 * 这个文件锁两件事：
 * 1. 预览 composable 的异步行为（防抖、丢弃过期响应、失败保旧值、无规则不发请求）；
 * 2. 旧「手填 + 前端判据」的残留在源码里彻底消失（判据唯一来源已是服务端）。
 */
const ruleA: BadgeRule = {
  v: 1,
  root: {
    type: 'activity.count',
    params: { metric: 'topic_count', window: { kind: 'all' } },
    cmp: { op: 'gte', value: 3 }
  }
}

const ruleB: BadgeRule = {
  v: 1,
  root: {
    type: 'activity.count',
    params: { metric: 'comment_count', window: { kind: 'all' } },
    cmp: { op: 'gte', value: 5 }
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('useBadgeRuleTextPreview', () => {
  test('防抖：连续改动只发一次请求，且带的是最后一次的规则', async () => {
    const calls: Array<BadgeRule | null> = []
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: async (value) => {
        calls.push(value)
        return { zh: '主题数 ≥ 5', en: 'Topics ≥ 5' }
      }
    })
    await nextTick()
    rule.value = ruleB
    await nextTick()
    rule.value = ruleA
    await nextTick()

    // 防抖窗口内一次都不发。
    expect(calls).toHaveLength(0)
    await Bun.sleep(30)

    expect(calls).toHaveLength(1)
    // ref 里拿到的会是对同一对象的响应式代理，比较内容而不是引用身份。
    expect(calls[0]).toEqual(ruleA)
    expect(preview.status.value).toBe('ready')
    expect(preview.text.value).toEqual({ zh: '主题数 ≥ 5', en: 'Topics ≥ 5' })
  })

  test('先慢后快：过期响应被丢弃，不覆盖新结果', async () => {
    const slow = deferred<{ zh: string, en: string }>()
    const fast = deferred<{ zh: string, en: string }>()
    let call = 0
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: () => {
        call += 1
        return call === 1 ? slow.promise : fast.promise
      }
    })

    await Bun.sleep(20)
    rule.value = ruleB
    await Bun.sleep(20)

    fast.resolve({ zh: '第二次', en: 'second' })
    await Bun.sleep(5)
    expect(preview.text.value?.zh).toBe('第二次')

    // 慢的那次现在才回来：必须被丢掉。
    slow.resolve({ zh: '第一次', en: 'first' })
    await Bun.sleep(5)
    expect(preview.text.value?.zh).toBe('第二次')
    expect(preview.status.value).toBe('ready')
  })

  test('失败保留上一次成功文案，只把状态标成 error', async () => {
    let fail = false
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: async () => {
        if (fail) throw new Error('preview unavailable')
        return { zh: '主题数 ≥ 3', en: 'Topics ≥ 3' }
      }
    })

    await Bun.sleep(20)
    expect(preview.text.value?.zh).toBe('主题数 ≥ 3')

    fail = true
    rule.value = ruleB
    await Bun.sleep(20)

    expect(preview.status.value).toBe('error')
    expect(preview.error.value).toBeInstanceOf(Error)
    // 派生数据断了，但用户已经看到的句子不该被擦掉。
    expect(preview.text.value?.zh).toBe('主题数 ≥ 3')
  })

  test('规则为 null 时不发请求，状态回到 idle', async () => {
    const calls: unknown[] = []
    const rule = ref<BadgeRule | null>(null)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: async (value) => {
        calls.push(value)
        return { zh: 'x', en: 'y' }
      }
    })

    await Bun.sleep(20)
    expect(calls).toHaveLength(0)
    expect(preview.status.value).toBe('idle')
    expect(preview.text.value).toBeNull()

    // 有规则后立刻开始工作，再回到 null 会清空（手动徽章 / 关闭弹窗）。
    rule.value = ruleA
    await Bun.sleep(20)
    expect(calls).toHaveLength(1)
    rule.value = null
    await Bun.sleep(20)
    expect(preview.status.value).toBe('idle')
    expect(preview.text.value).toBeNull()
  })

  test('首次请求就失败：status=error、text 保持 null（不编造空串）', async () => {
    const preview = useBadgeRuleTextPreview({
      rule: () => ruleA,
      debounceMs: 5,
      loader: async () => {
        throw new Error('boom')
      }
    })

    await Bun.sleep(20)
    expect(preview.status.value).toBe('error')
    expect(preview.text.value).toBeNull()
  })

  test('enabled() 为假时不发请求（弹窗关闭不打网络），恢复后照常预览', async () => {
    const calls: unknown[] = []
    const open = ref(false)
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      enabled: () => open.value,
      debounceMs: 5,
      loader: async (value) => {
        calls.push(value)
        return { zh: '主题数 ≥ 3', en: 'Topics ≥ 3' }
      }
    })

    await Bun.sleep(20)
    expect(calls).toHaveLength(0)
    expect(preview.status.value).toBe('idle')

    open.value = true
    await Bun.sleep(20)
    expect(calls).toHaveLength(1)
    expect(preview.status.value).toBe('ready')
  })

  test('setText 之后，在途的旧响应不能再覆盖（保存回值优先）', async () => {
    const slow = deferred<{ zh: string, en: string }>()
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: () => slow.promise
    })

    await Bun.sleep(20) // 请求已发出、还没回来
    preview.setText({ zh: '落库文案', en: 'saved text' })
    slow.resolve({ zh: '草稿预览', en: 'draft' })
    await Bun.sleep(10)

    expect(preview.text.value).toEqual({ zh: '落库文案', en: 'saved text' })
  })

  test('失败之后再改规则会重试，并恢复到 ready', async () => {
    let fail = true
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: async () => {
        if (fail) throw new Error('offline')
        return { zh: '主题数 ≥ 5', en: 'Topics ≥ 5' }
      }
    })

    await Bun.sleep(20)
    expect(preview.status.value).toBe('error')

    fail = false
    rule.value = ruleB
    await Bun.sleep(20)
    expect(preview.status.value).toBe('ready')
    expect(preview.text.value?.zh).toBe('主题数 ≥ 5')
  })

  test('setText：保存回值直接覆盖预览，并清掉 pending', async () => {
    const rule = ref<BadgeRule | null>(ruleA)
    const preview = useBadgeRuleTextPreview({
      rule: () => rule.value,
      debounceMs: 5,
      loader: () => deferred<{ zh: string, en: string }>().promise
    })

    preview.setText({ zh: '落库文案', en: 'saved text' })
    expect(preview.text.value).toEqual({ zh: '落库文案', en: 'saved text' })
    expect(preview.status.value).toBe('ready')

    // 传 null = 清空（手动徽章）。
    preview.setText(null)
    expect(preview.text.value).toBeNull()
    expect(preview.status.value).toBe('idle')
  })
})

describe('toAdminBadgeInput · 公开说明不再写回', () => {
  const form = {
    slug: 'resident',
    name: 'Resident',
    description: '',
    groupId: null,
    tier: 'bronze',
    icon: 'i-lucide-medal',
    grantMode: 'automatic',
    metric: 'topic_count',
    threshold: 3,
    enabled: true,
    position: 0,
    rule: ruleA
  }

  test('请求体不含 requirementSummary，其它字段保持原样', () => {
    const body = toAdminBadgeInput(form)
    expect('requirementSummary' in body).toBe(false)
    expect('requirementText' in body).toBe(false)
    expect(body.slug).toBe('resident')
    expect(body.grantMode).toBe('automatic')
    expect(body.metric).toBe('topic_count')
    expect(body.threshold).toBe(3)
    expect(body.rule).toBe(ruleA)
  })

  test('rule 缺省时连 rule 键都不出现（三态契约不变）', () => {
    const body = toAdminBadgeInput({ ...form, rule: undefined })
    expect('rule' in body).toBe(false)
    expect('requirementSummary' in body).toBe(false)
  })
})

describe('手填判据的源码残留（必须为零）', () => {
  const badgesRoot = '../../app/components/admin/badges/'
  const read = (rel: string) => readFileSync(new URL(`${badgesRoot}${rel}`, import.meta.url), 'utf8')

  test('Section 不再引用 badgeRuleNeedsSummary，也不再渲染 textarea', () => {
    const section = read('SFAdminBadgeRuleSection.vue')
    expect(section).not.toContain('badgeRuleNeedsSummary')
    expect(section).not.toContain('UTextarea')
    expect(section).not.toContain('requirementSummaryHint')
    expect(section).not.toContain('requirementSummaryMissing')
    // 只读预览的三个状态锚点都在。
    expect(section).toContain('requirementTextAuto')
    expect(section).toContain('requirementTextPreviewFailed')
    expect(section).toContain('requirementTextEmpty')
  })

  test('Section 没有把公开说明再变回可编辑（无 defineModel / v-model）', () => {
    const section = read('SFAdminBadgeRuleSection.vue')
    expect(section).not.toContain('defineModel')
    expect(section).not.toContain('requirement-summary-zh')
    expect(section).not.toContain('requirement-summary-en')
  })

  test('rule.ts 不再有前端侧「能否自动生成」判据', () => {
    const source = readFileSync(new URL('../../app/utils/admin/rule.ts', import.meta.url), 'utf8')
    expect(source).not.toContain('badgeRuleNeedsSummary')
    expect(source).not.toContain('AUTO_SUMMARY_METRICS')
  })

  test('徽章弹窗家族 5 处都不再拿 #hint 承载长提示', () => {
    const files = [
      'SFAdminBadgeRuleSection.vue',
      'SFAdminBadgeEditDialog.vue',
      'SFAdminBadgeTierDialog.vue',
      'SFAdminBadgeTierDeleteDialog.vue',
      'tabs/SFAdminBadgesGroupsTab.vue'
    ]
    for (const file of files) {
      expect({ file, hintSlots: read(file).includes('#hint') }).toEqual({ file, hintSlots: false })
    }
  })
})
