import { ref } from 'vue'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate } from '~/utils/admin/badgeRule'
import { normalizeFactCandidates } from '~/utils/admin/badgeFactCandidates'
import {
  normalizeRefCandidates,
  refCandidatesInvariantBroken,
  refCandidatesMissingKinds as missingRefKinds,
  type BadgeRefCandidateMap
} from '~/utils/admin/badgeRefCandidates'

/**
 * 后台条件目录（`GET /admin/badges/conditions`）。
 *
 * M1.4 端点可能还没上线：请求失败（404 / 网络错 / 非 200）或返回形状不符时，一律退化成
 * **空目录 + `failed = true`**，不抛错、不阻塞页面。规则编辑器用
 * `catalog.length > 0` 作双口径开关，页面壳不要再写第二套判断。
 *
 * `factCandidates` 是同一响应的**顶层新增字段**（M3，Errata 2）：插件声明的事实候选，
 * 供 `fact.count` 的 `factKey` / `variant` 下拉显示插件文案。它单独降级：
 * 目录可用但候选读不到时，规则编辑器照常工作，只少一组候选 + 一条提示。
 *
 * `refCandidates` 也是顶层新增字段（M3）：分类 / 标签 / 角色 / 提供方 / 徽章的可选项，
 * 供 `objectRef` 参数渲染可搜索选择。它**没有失败标志**——读不到就退成 `{}`，
 * 每个 kind 由读侧按 `unavailable` 处理（退文本输入），所以 `failed` 语义不变。
 *
 * 两条诊断必须分开（合并就分不清「老后端」与「真回归」）：
 * - `refCandidatesIncomplete`：目录声明了 objectRef 参数，响应里**整个字段都没有**。
 *   老后端必然命中 → 只记录，不参与 UI。
 * - `refCandidatesMissingKinds`：响应**给了数组**却缺某个目录已声明的 kind → 真回归，
 *   界面要给可见诊断（`SFAdminBadgeRuleSection` 的 `data-ref-candidates-incomplete`）。
 *
 * `loader` 是测试注入点：默认实现走 `useApiClient()`（Nuxt 自动导入），
 * 传了 loader 就不再触碰网络层。
 */
export type BadgeConditionsLoader = () => Promise<unknown>

/** 兼容 `{ items: [...] }` 与裸数组两种成功形状；无法识别时返回 null。 */
export function normalizeConditionsPayload(payload: unknown): BadgeConditionCatalogEntry[] | null {
  const candidate = Array.isArray(payload) ? payload : (payload as { items?: unknown } | null)?.items
  if (!Array.isArray(candidate)) return null
  return candidate.flatMap((item) => {
    if (!item || typeof item !== 'object' || typeof (item as { key?: unknown }).key !== 'string') return []
    return [normalizeEntryParams(item as BadgeConditionCatalogEntry)]
  })
}

/**
 * 参数层归一：`options` 在 wire / OpenAPI 上是**字符串数组**（`conditionParamDTO.Options
 * []string`）。写前端的把这里当成 `{value,label}` 对象数组时，每个 `<option>` 的 value
 * 都变成 undefined，下拉整片空白——所以在读入口先收敛形状。
 *
 * 若后端将来真改成对象型 options，这里是**唯一**放宽点：把对象里的 value 取出来，
 * 界面层继续只按值渲染，不必跟着二次改造。
 */
function normalizeParamSpec(raw: unknown): unknown {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return raw
  const spec = raw as Record<string, unknown>
  if (!('options' in spec)) return spec
  const options = Array.isArray(spec.options)
    ? spec.options.filter((value): value is string => typeof value === 'string')
    : []
  return { ...spec, options }
}

/** `params` 是可选字段：缺省表示「这个条件没声明参数表」，不补空数组，也不改名不丢键。 */
function normalizeEntryParams(entry: BadgeConditionCatalogEntry): BadgeConditionCatalogEntry {
  const raw = (entry as { params?: unknown }).params
  if (raw === undefined) return entry
  if (!Array.isArray(raw)) return { ...entry, params: [] }
  return { ...entry, params: raw.map(normalizeParamSpec) as BadgeConditionCatalogEntry['params'] }
}

/**
 * 「响应声称有插件条件，却没给候选」= 后端回归，必须当失败，不能安静地当空候选。
 *
 * 判据：目录里出现了 `fact.count`（后端只有在**有候选**时才发布它，见
 * `PublishableDefinitions`），那 `factCandidates` 就必须存在且是数组。
 * 两者**同时缺失**是 M1 时代的正常形态（没有插件条件），保持安静——否则老后端上
 * 每次打开弹窗都会报一条假故障。
 *
 * 这条不变量是防「静默」的：宽松解析会让一次后端字段改名表现为「插件条件全不见」，
 * 运营看不出任何异常，正是假绿清单里的头号形态。
 */
export function factCandidatesInvariantBroken(items: readonly BadgeConditionCatalogEntry[], raw: unknown): boolean {
  const claimsFacts = items.some(item => item.key === 'fact.count')
  if (!claimsFacts) return false
  return !Array.isArray(raw)
}

export function useAdminBadgeConditions(loader?: BadgeConditionsLoader) {
  const catalog = ref<BadgeConditionCatalogEntry[]>([])
  const factCandidates = ref<BadgeFactCandidate[]>([])
  const failed = ref(false)
  /** 候选整体不可用的独立标志：目录正常时编辑器仍可用，只提示候选缺失。 */
  const factCandidatesFailed = ref(false)
  /** `objectRef` 候选：缺项/整体缺失都是 `{}`（读侧当作每项不可用 → 文本兜底）。 */
  const refCandidates = ref<BadgeRefCandidateMap>({})
  /** 仅诊断：目录声明了 objectRef 参数却没拿到 `refCandidates` 数组。 */
  const refCandidatesIncomplete = ref(false)
  /** 真回归诊断：响应有数组，却缺了目录已声明的 kind（老后端时为 `[]`）。 */
  const refCandidatesMissingKinds = ref<string[]>([])
  const pending = ref(false)
  const loaded = ref(false)

  async function load() {
    if (pending.value) return
    pending.value = true
    try {
      const payload = loader ? await loader() : await useApiClient().request<unknown>('/admin/badges/conditions')
      const items = normalizeConditionsPayload(payload)
      if (!items) throw new Error('unexpected conditions payload')
      catalog.value = items
      failed.value = false
      // 候选的解析有两层：字段自身形状 + 与目录的**不变量**（见 factCandidatesInvariantBroken）。
      const rawCandidates = (payload as { factCandidates?: unknown } | null)?.factCandidates
      const candidates = normalizeFactCandidates(rawCandidates)
      const invariantBroken = factCandidatesInvariantBroken(items, rawCandidates)
      factCandidates.value = invariantBroken ? [] : candidates ?? []
      factCandidatesFailed.value = candidates === null || invariantBroken
      // objectRef 候选：形状不合/字段缺失都归一到 {}，读侧逐 kind 降级到文本输入。
      // 不变量只记录，不当失败（见 refCandidatesInvariantBroken 的注释）。
      const rawRefs = (payload as { refCandidates?: unknown } | null)?.refCandidates
      refCandidates.value = normalizeRefCandidates(rawRefs)
      refCandidatesIncomplete.value = refCandidatesInvariantBroken(items, rawRefs)
      refCandidatesMissingKinds.value = missingRefKinds(items, rawRefs)
    } catch {
      // 端点未上线 / 网络失败 / 形状不符：空目录让 ruleReady 保持 false，失败态交给 UI 提示。
      catalog.value = []
      factCandidates.value = []
      refCandidates.value = {}
      refCandidatesIncomplete.value = false
      refCandidatesMissingKinds.value = []
      failed.value = true
      factCandidatesFailed.value = true
    } finally {
      pending.value = false
      loaded.value = true
    }
  }

  return {
    catalog,
    factCandidates,
    factCandidatesFailed,
    refCandidates,
    refCandidatesIncomplete,
    refCandidatesMissingKinds,
    failed,
    pending,
    loaded,
    load
  }
}
