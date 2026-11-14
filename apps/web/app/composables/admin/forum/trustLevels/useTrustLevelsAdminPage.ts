import type { AdminWebOption } from '~/composables/useWebOptions'
import { apiErrorReason, apiErrorStatusCode, useApiClient } from '~/composables/useApiClient'
import { adminOptionMap } from '~/composables/admin/settings/useAdminOptionTab'
import {
  cloneTrustLevelsForm,
  earnableWhitelistForSave,
  normalizeTrustLevelsForm,
  readTrustLevelsConfig,
  recommendedTrustLevelsForm,
  resolveTrustLevelWhitelist,
  tierStructureChanged,
  toggleEarnableKey,
  TRUST_LEVELS_OPTION,
  trustLevelsConfig,
  useTrustLevelEarnableWhitelist,
  type TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import {
  addTier as addTierTo,
  activeTierCount,
  activeTiersOf,
  canAddTier,
  canRetireTier,
  insertTierAt as insertTierInto,
  maxTierSeq,
  moveTier as moveTierIn,
  renameTier as renameTierIn,
  retiredImpactKeys,
  restoreTier as restoreTierIn,
  setTierColor as setTierColorIn,
  setTierIcon as setTierIconIn,
  setTierRequirement,
  TRUST_LEVEL_TIER_MAX,
  type TierForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import {
  setLimitValue as setLimitValueIn,
  setLimitsMode as setLimitsModeIn,
  type TrustLevelLimitKey,
  type TrustLevelLimitMode
} from '~/composables/admin/forum/trustLevels/useTrustLevelLimits'
import {
  useTrustLevelTierIntents,
  type TrustLevelDeleteRequest,
  type TrustLevelRetireRequest
} from '~/composables/admin/forum/trustLevels/useTrustLevelTierIntents'
import {
  bucketSaveErrors,
  saveErrorsFromError,
  type TrustLevelSaveError
} from '~/composables/admin/forum/trustLevels/useTrustLevelSaveErrors'
import {
  catalogUsabilityFor,
  anyTierRuleDirty,
  firstRuleErrorTierId
} from '~/composables/admin/forum/trustLevels/useTrustLevelRuleDraft'
import {
  hasRulePayload,
  useTrustLevelRuleImpact
} from '~/composables/admin/forum/trustLevels/useTrustLevelRuleImpact'
import { useAdminTrustLevelConditions } from '~/composables/admin/forum/trustLevels/useAdminTrustLevelConditions'
import type { TrustLevelRuleDocument } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useSettingsSection } from '~/composables/settings/useSettingsSection'

/**
 * 「信任等级」独立页的编排层（route shell 只负责 tab 选择与渲染）。
 *
 * 四件事必须住在这里而不是分区组件里：
 * 1. **表单状态跨分区存活**：切 tab 不丢改动（KeepAlive + 单一 form ref），档位顺序只有一份；
 * 2. **白名单四态**：`idle / loading / ready / failed` 互不冒充，功能关闭时**不发**请求；
 * 3. **保存只有一个出口**：`PUT /admin/trust-levels`，body 是整份 v2 配置（D-N10）。
 *    走专用端点的原因是通用 `PUT /admin/web-options` 的 422 只有一句 `options.invalid_value`，
 *    拿不到任何字段路径；专用端点才返回 `details.errors[]`。
 *    白名单没读到（idle/loading/failed）就原样带回已存配置 —— 把「读不到」当「白名单为空」
 *    会在保存时静默清空运营已配好的能力解锁，那是一次没有报错的数据丢失；
 * 4. **停用是二次确认**（D-N8）：`requestRetire` 只立起确认态，真正写表单要 `confirmRetire`。
 *    确认文案与「可能降权」披露由 shell 的常驻 UAlert 承担（Toast 只报结果，不承担披露）。
 *
 * 特征开关读 public option `features.trust_levels`，不再从 admin 载荷里读同名键。
 */
/**
 * 信任等级的**专用写端点**（不是通用 `PUT /admin/web-options`）：
 * 只有这条路的 422 带 `details.errors[]`，通用那条只会回一句 `options.invalid_value`。
 * 读面路径见 `TRUST_LEVEL_ADMIN_READ_PATH`。
 */
export const TRUST_LEVEL_ADMIN_WRITE_PATH = '/admin/trust-levels'
export const TRUST_LEVEL_ADMIN_RESTORE_PATH = '/admin/trust-levels/restore-defaults'

/** 页面五态：一个状态机、一个 `data-sf-page-state`（spec §2）。 */
export type TrustLevelsPageState = 'loading' | 'failed' | 'forbidden' | 'feature-off' | 'ready'

/**
 * 停用 / 删除确认态由 `useTrustLevelTierIntents` 拥有，这里只做对外导出，
 * 让既有 import（`TrustLevelRetireRequest`）不必跟着搬家。
 */
export type { TrustLevelDeleteRequest, TrustLevelRetireRequest }

/**
 * 白名单读面的唯一前置判据：功能开关。
 *
 * 关掉 `features.trust_levels` 时后端 `requireAdmin` 先短路，`/admin/trust-levels` 必然 404，
 * 所以前端一次请求都不该发（D31：开关必须在调用 resolver 之前短路）。
 */
export function shouldLoadWhitelist(trustLevelsEnabled: boolean): boolean {
  return trustLevelsEnabled
}

export function useTrustLevelsAdminPage() {
  const { t, locale } = useI18n()
  const { can } = useAuthSession()
  const toast = useToast()
  const section = useSettingsSection()
  const { request } = useApiClient()
  const { options, trustLevelsEnabled, fetchAdminEnvelope } = useWebOptions()

  /**
   * 写权限为 `trust.manage`（D-B）：读面按每条 option 的 `managePermission` 过滤，没有该权限时
   * 载荷里**根本没有 `trust.levels` 键**，而回填会把「键缺失」当推荐默认 —— 渲染出来就是假现值。
   * 保留 `canManage` 作为纵深防御：权限可能在渲染与保存之间变化，保存入口只认它。
   */
  const canManage = computed(() => can('trust.manage'))

  const { data: items, pending, error, refresh } = useAsyncData<AdminWebOption[]>(
    'admin-web-options',
    async () => (await fetchAdminEnvelope()).data
  )

  /**
   * `hasLoaded` 的真相**只能来自数据本身**，不能靠 handler 里的副作用置位：
   * 硬加载会复用 SSR payload 水合，handler 不重跑 —— 那时副作用式布尔恒为 false。
   */
  const hasLoaded = computed(() => items.value !== null && items.value !== undefined)

  const map = computed(() => adminOptionMap(items.value ?? []))

  /**
   * 五态判定（spec §2.1，先命中先定态）：forbidden → failed → loading → feature-off → ready。
   */
  const pageState = computed<TrustLevelsPageState>(() => {
    if (!canManage.value) return 'forbidden'
    const status = apiErrorStatusCode(error.value)
    if (status === 401 || status === 403) return 'forbidden'
    if (error.value) return 'failed'
    if (!hasLoaded.value) return 'loading'
    if (!trustLevelsEnabled.value) return 'feature-off'
    return 'ready'
  })

  const loadErrorReason = computed(() => apiErrorReason(error.value) || '')

  /**
   * 白名单（矩阵的可勾集合）与表单状态**分开存**：
   * 阈值回填绝不等这个请求——多一个往返就多一次「表单卡住」的机会。
   * 它同时带来 D-N9 的权威推荐值与 D-N3/D-N4 的编辑辅助（命名池、图标白名单）。
   */
  const { state: whitelistState, load: loadWhitelist } = useTrustLevelEarnableWhitelist()

  /**
   * 推荐默认值的权威来源（D-N9）：读面的 `recommendedConfig`；读不到时回落本地 `FALLBACK_TIERS`。
   * `usingFallbackRecommended` 让「恢复默认」旁的披露说实话 —— 不假装本地表就是后端推荐值。
   */
  const recommended = computed<TrustLevelsForm>(
    () => whitelistState.value.recommended ?? recommendedTrustLevelsForm()
  )
  const usingFallbackRecommended = computed(() => whitelistState.value.recommended === null)

  const initial = ref<TrustLevelsForm>(recommendedTrustLevelsForm())
  const form = ref<TrustLevelsForm>(recommendedTrustLevelsForm())
  const hasChanges = computed(() => JSON.stringify(form.value) !== JSON.stringify(initial.value))

  watch([map, recommended], resetFromItems, { immediate: true })

  /**
   * 开关关闭时**不发**这个请求（`status` 停在 idle）。三个触发点都只走 `shouldLoadWhitelist`。
   */
  function maybeLoadWhitelist() {
    if (!shouldLoadWhitelist(trustLevelsEnabled.value)) return
    // 只短路 `loading`：`ready` 必须可达（保存后要刷新白名单），`failed` 之后的重试也必须可达。
    if (whitelistState.value.status === 'loading') return
    void loadWhitelist()
  }

  /**
   * 条件目录的加载时机：功能开关打开 + 页面 ready。**开关关闭时一次请求都不发**
   * （与白名单同口径：后端 `requireAdmin` 会先短路，请求必然 404）。
   */
  function maybeLoadConditions() {
    if (!trustLevelsEnabled.value) return
    if (conditions.status.value === 'loading' || conditions.status.value === 'ready') return
    void conditions.load()
  }

  onMounted(maybeLoadWhitelist)
  onMounted(maybeLoadConditions)
  watch(pageState, (state) => {
    if (state === 'ready') maybeLoadConditions()
  })
  watch(items, maybeLoadWhitelist)
  watch(trustLevelsEnabled, enabled => {
    if (!shouldLoadWhitelist(enabled)) return
    void loadWhitelist()
  })

  function resetFromItems() {
    const config = readTrustLevelsConfig(map.value, recommended.value)
    // 读面晚到（推荐值/停用档位带着配置回来）时不要覆盖运营正在编辑的改动。
    if (hasChanges.value) return
    initial.value = config
    form.value = cloneTrustLevelsForm(config)
  }

  const tierCount = computed(() => activeTierCount(form.value.tiers))
  const tiers = computed(() => form.value.tiers)
  const activeTiers = computed(() => activeTiersOf(form.value.tiers))
  const canAdd = computed(() => canAddTier(form.value.tiers))
  const canRetire = computed(() => canRetireTier(form.value.tiers))
  const tierLimitReached = computed(() => tierCount.value >= TRUST_LEVEL_TIER_MAX)

  /* ────────────────────────── 档位编辑（纯函数落表单） ────────────────────────── */

  /**
   * 档位数组的唯一写通道。必须整份 clone 后再替换：`form` 除了 `tiers` 还带
   * `deletedTierIds`（删除墓碑），只写 `{ tiers }` 会把墓碑静默抹掉 —— 那等于让 id 重新可用。
   */
  function applyTiers(mutate: (tiers: TierForm[]) => TierForm[]) {
    if (!canManage.value) return
    const current = cloneTrustLevelsForm(form.value)
    form.value = { ...current, tiers: mutate(current.tiers) }
  }

  /**
   * 新档 id 的序号：后端 `nextTierSeq` 是权威，但**同一页连续插两档**时本地必须先自增，
   * 否则两档会拿到同一个 id（保存必 422 `id_duplicate`）。保存成功后以响应载荷重取。
   */
  const localSeq = ref(0)
  watch(() => whitelistState.value.nextTierSeq, (value) => {
    if (value > 0) localSeq.value = value
  }, { immediate: true })

  function takeSeq(): number {
    const seq = Math.max(
      localSeq.value,
      whitelistState.value.nextTierSeq,
      maxTierSeq(form.value.tiers) + 1
    )
    localSeq.value = seq + 1
    return seq
  }

  function addTier() {
    if (!canManage.value || !canAddTier(form.value.tiers)) return
    const seq = takeSeq()
    applyTiers(tiers => addTierTo(tiers, null, seq))
  }

  function insertTierAt(rank: number) {
    if (!canManage.value || !canAddTier(form.value.tiers)) return
    const seq = takeSeq()
    applyTiers(tiers => insertTierInto(tiers, rank, null, seq))
  }

  function moveTier(id: string, delta: number) {
    applyTiers(tiers => moveTierIn(tiers, id, delta))
  }

  function restoreTier(id: string) {
    applyTiers(tiers => restoreTierIn(tiers, id))
  }

  function renameTier(id: string, value: string, target: 'zh' | 'en' = locale.value.toLowerCase().startsWith('zh') ? 'zh' : 'en') {
    applyTiers(tiers => renameTierIn(tiers, id, target, value))
  }

  /**
   * 图标：白名单只是**渲染候选**，不是写入闸门。
   * 图标选择器是 lucide + tabler 全量目录，候选之外的合法全名同样要能落进表单；
   * 保存期风险由 `setTierIcon` 的形状校验（全名语法 ∪ 16 默认短名）承担。
   */
  function setIcon(id: string, icon: string | null) {
    applyTiers(tiers => setTierIconIn(tiers, id, icon))
  }

  /** 颜色：合法值 = 推荐 token ∪ 自定义 `#rrggbb`（见 `isTierColorValue`），推荐色板不是闸门。 */
  function setColor(id: string, color: string | null) {
    applyTiers(tiers => setTierColorIn(tiers, id, color))
  }

  /* ────────────────────────── 停用 / 删除（D-N8 / D-C2） ────────────────────────── */

  /**
   * 已保存的停用档 id：删除的前置条件（只有落盘为停用的档才允许删除）。
   * 取 `initial` 而不是 `form` —— 判据必须是「服务端已经知道它停用了」。
   */
  const savedRetiredIds = computed(() => initial.value.tiers.filter(tier => tier.retired).map(tier => tier.id))

  const {
    retireRequest,
    requestRetire,
    confirmRetire,
    cancelRetire,
    deleteRequest,
    canDelete,
    requestDelete,
    confirmDelete,
    cancelDelete
  } = useTrustLevelTierIntents({
    form,
    savedRetiredIds,
    canManage,
    applyTiers
  })

  /* ────────────────────────── 阈值 / 解锁 / 保存 ────────────────────────── */

  /* ────────────────────────── 分档限额（D-N11） ────────────────────────── */

  /**
   * 限额是**三态**（继承 `null` / 逐字段覆盖对象 / `0` = 不限额），写入只能经这两个动作：
   * 组件只发意图，表单归本编排层，保存入口仍只有 footer 一个（D-N10）。
   */
  function setLimitsMode(tierId: string, mode: TrustLevelLimitMode) {
    applyTiers(tiers => setLimitsModeIn(tiers, tierId, mode))
  }

  function setLimit(tierId: string, key: TrustLevelLimitKey, value: string | number | null) {
    applyTiers(tiers => setLimitValueIn(tiers, tierId, key, value))
  }

  function setThreshold(tierId: string, suffix: string, value: string | number) {
    if (!canManage.value) return
    form.value = { tiers: setTierRequirement(form.value.tiers, tierId, suffix, value) }
  }

  /** 矩阵只发意图，表单归本编排层所有：切完的键按白名单序落在该档，不跨档复制（Permissions 语义）。 */
  function toggleEarnable(tierId: string, key: string) {
    if (!canManage.value || whitelistState.value.status !== 'ready') return
    form.value = toggleEarnableKey(form.value, tierId, key, whitelistState.value.keys)
  }

  /**
   * 档位结构是否变化（P1-4）：只有它会重排所有人的等级，所以保存前必须确认。
   * 只改阈值/名字/图标/能力解锁 → 静默保存（那是纯摩擦）。
   */
  const structureChanged = computed(() => tierStructureChanged(initial.value, form.value))
  const structureConfirmOpen = ref(false)

  /**
   * 字段级诊断（P2）：422 时把 `details.errors[]` 挂到具体档位。
   * 空数组 = 后端没给明细（或本次不是校验失败）→ 页面回落到统一码文案，不渲染空白错误条。
   */
  const saveErrors = ref<TrustLevelSaveError[]>([])
  const saveErrorBuckets = computed(() => bucketSaveErrors(saveErrors.value, form.value))

  /* ────────────────────────── 规则面板与保存闸门（M3b / D21） ────────────────────────── */

  /**
   * 条件目录（三态）。**失败不阻塞页面**：阈值、档位、限额照常可用，只有「写规则」这条
   * 分支被闸门拦住（见下面 `saveBlockedByCatalog`）。
   */
  const conditions = useAdminTrustLevelConditions()
  const conditionsState = computed(() => ({
    status: conditions.status.value,
    items: conditions.items.value,
    factCandidates: conditions.factCandidates.value,
    refCandidates: conditions.refCandidates.value
  }))

  /** D21 的唯一实现点：组件与按钮都读它，不各算一遍。 */
  const catalogUsability = computed(() => catalogUsabilityFor(
    conditionsState.value,
    form.value.tiers.map(tier => tier.rule ?? null)
  ))
  /** 本次会话是否真的动过规则字节（与「载荷里有没有 rule」不是一回事）。 */
  const tierRuleDirty = computed(() => anyTierRuleDirty(form.value.tiers, initial.value.tiers))
  const saveBlockedByCatalog = computed(() => tierRuleDirty.value && !catalogUsability.value.usable)

  /**
   * 单开手风琴：只记「哪一档展开」，**不进表单状态、不进 `trust.levels` JSON**
   * （进去会改变配置字节，破坏计划 §3.1 的回滚口径），刷新后全部折叠。
   */
  const expandedRuleTierId = ref<string | null>(null)

  function toggleRulePanel(tierId: string) {
    expandedRuleTierId.value = expandedRuleTierId.value === tierId ? null : tierId
  }

  /** 规则写入：整份 clone 后替换，避免像 `setThreshold` 那样只写 `{tiers}` 把墓碑抹掉。 */
  function setTierRule(tierId: string, rule: TrustLevelRuleDocument) {
    if (!canManage.value) return
    const current = cloneTrustLevelsForm(form.value)
    form.value = {
      ...current,
      tiers: current.tiers.map(tier => (tier.id === tierId ? { ...tier, rule } : tier))
    }
  }

  const retryConditions = () => { void conditions.load() }

  /* ────────────────────────── 影响面估算（保存前一步） ────────────────────────── */

  const ruleImpact = useTrustLevelRuleImpact()
  const impactOpen = ref(false)

  /**
   * 弹窗只列**带规则的档位**。
   *
   * 后端会为请求里的每一档返回结论（纯数值档也会给一个「按阈值估」的数），但运营点这个弹窗
   * 是为了确认「这次加/改的条件会影响多少人」——把 4 个没条件的档也列出来只是噪声，
   * 而且会让屏幕上出现 4 个 `≈`（copy-spec §3.3.2 的口径是「数字全屏只出现一次」）。
   */
  const impactRows = computed(() => {
    const ruleTierIds = new Set(form.value.tiers.filter(tier => tier.rule != null).map(tier => tier.id))
    return ruleImpact.results.value.filter(row => ruleTierIds.has(row.tierId))
  })
  /** 恢复默认是服务端动作（POST），与保存分开的忙碌态，避免 UI 提前报成功。 */
  const restoring = ref(false)

  /**
   * 写端点成功时返回**整份后台读面载荷**：一次往返就把「基线 / 表单 / 白名单 / 推荐值 /
   * 名称池 / 图标白名单」全部刷新，不用再补一次 GET，也不会出现「保存成功但脏状态还在」。
   */
  function applyAdminPayload(body: unknown) {
    const record = (body && typeof body === 'object' ? body : {}) as Record<string, unknown>
    if (typeof record.config === 'string' && record.config.length > 0) {
      const next = readTrustLevelsConfig(
        { [TRUST_LEVELS_OPTION]: { name: TRUST_LEVELS_OPTION, value: record.config } as never },
        recommended.value
      )
      initial.value = next
      form.value = cloneTrustLevelsForm(next)
      // 读面载荷与 web-options 是两条通道：同步本地缓存，切 tab / refresh 时不会回退成旧值。
      items.value = (items.value ?? []).map(item => (
        item.name === TRUST_LEVELS_OPTION ? { ...item, value: record.config as string } : item
      ))
    }
    whitelistState.value = resolveTrustLevelWhitelist({ ok: true, body })
    const payloadSeq = Number(record.nextTierSeq)
    localSeq.value = Number.isFinite(payloadSeq) && payloadSeq > 0
      ? Math.trunc(payloadSeq)
      : maxTierSeq(form.value.tiers) + 1
    saveErrors.value = []
  }

  /**
   * 保存入口（D21 / 计划 §4.4 的完整链路）：
   *
   *   结构变了？→ 结构确认 → 有规则？→ 影响面弹窗 → PUT
   *
   * 闸门只在「改过规则字节 ∧ 目录不可用」时拦；只改阈值的提交照常走 —— 那种提交不会写出
   * 任何 `rule` 字节，不存在「配置缺失时静默清空 rule」的路径。
   */
  async function save() {
    // 没有写权限时按钮已禁用；这里再挡一次，避免 Enter 提交绕过。
    if (!canManage.value) return
    if (saveBlockedByCatalog.value) return
    if (structureChanged.value) {
      structureConfirmOpen.value = true
      return
    }
    await beginImpactFlow()
  }

  function cancelStructureConfirm() {
    structureConfirmOpen.value = false
  }

  async function confirmStructureSave() {
    structureConfirmOpen.value = false
    await beginImpactFlow()
  }

  /** 有规则就估一次（草稿体，新档也估）；纯数值编辑不打扰运营。 */
  async function beginImpactFlow() {
    if (!hasRulePayload(form.value.tiers)) {
      await performSave()
      return
    }
    impactOpen.value = true
    await ruleImpact.estimate(form.value.tiers)
  }

  async function confirmImpact() {
    impactOpen.value = false
    await performSave()
  }

  function cancelImpact() {
    impactOpen.value = false
    ruleImpact.reset()
  }

  async function performSave() {
    if (!canManage.value) return
    await section.runSave({
      successTitle: t('admin.settings.saved'),
      failureTitle: t('admin.settings.saveFailed'),
      prepare: () => {
        form.value = normalizeTrustLevelsForm(form.value)
      },
      save: async () => {
        try {
          // 单键全量覆盖：一次 PUT 提交整份 v2 配置（阈值 + 限额 + 解锁 + 名字 + 图标 + 停用位）。
          const body = await request<unknown>(TRUST_LEVEL_ADMIN_WRITE_PATH, {
            method: 'PUT',
            body: { config: trustLevelsConfig(form.value, earnableWhitelistForSave(whitelistState.value)) }
          })
          applyAdminPayload(body)
        } catch (error) {
          // 明细挂在对应档位行上（阻塞错误贴表单）；Toast 只播一条非阻塞汇总。
          const errors = saveErrorsFromError(error)
          saveErrors.value = errors
          // id 冲突类错误通常来自「另一个管理员同时加了档」：重读后台载荷拿新的 nextTierSeq，
          // 让运营再点一次保存就能过，而不是卡在一个永远重复的 id 上。
          if (errors.some(error => error.reasonCode === 'id_duplicate' || error.reasonCode === 'id_derivation_conflict')) {
            void loadWhitelist()
          }
          // 规则域的错误要把那一档的面板自动展开（否则运营只看到行首一句「有 1 处需要修正」，
          // 却不知道该展开哪一档去修）。滚动由 shell 在 `expandedRuleTierId` 变化时做。
          const ruleTierId = firstRuleErrorTierId(bucketSaveErrors(errors, form.value))
          if (ruleTierId) expandedRuleTierId.value = ruleTierId
          if (errors.length > 0) {
            toast.add({
              color: 'warning',
              icon: 'i-lucide-triangle-alert',
              title: t('admin.forum.trustLevels.errors.summary', { count: errors.length }),
              duration: 5000
            })
          }
          throw error
        }
      }
    })
  }

  function resetChanges() {
    form.value = cloneTrustLevelsForm(initial.value)
    retireRequest.value = null
    deleteRequest.value = null
    saveErrors.value = []
    toast.add({
      color: 'neutral',
      icon: 'i-lucide-rotate-ccw',
      title: t('admin.forum.trustLevels.resetChanges'),
      duration: 5000
    })
  }

  /**
   * 恢复推荐默认（`POST /admin/trust-levels/restore-defaults`）：服务端用 `DefaultPolicy()`
   * 覆盖整份配置（档位、阈值、限额、能力解锁、名称与图标），返回值就是新的读面载荷。
   *
   * 为什么不本地套 `recommendedConfig`：推荐默认只有服务端一处定义；本地覆盖只能改前端看得见
   * 的那部分，且会立刻把「已保存值」和「表单值」分成两套。
   *
   * 语义对齐：这是会影响全站等级判定的显式动作，所以**先确认再 POST**；
   * 但它不叠加「档位结构变化」那条弹窗 —— 那是给普通保存用的闸门。
   */
  const restoreConfirmOpen = ref(false)

  function restoreThresholds() {
    if (!canManage.value) return
    restoreConfirmOpen.value = true
  }

  function cancelRestore() {
    restoreConfirmOpen.value = false
  }

  async function confirmRestore() {
    if (!canManage.value) return
    restoreConfirmOpen.value = false
    restoring.value = true
    try {
      const body = await request<unknown>(TRUST_LEVEL_ADMIN_RESTORE_PATH, { method: 'POST' })
      applyAdminPayload(body)
      toast.add({
        color: 'success',
        icon: 'i-lucide-rotate-ccw',
        title: t('admin.forum.trustLevels.restoreDefaults'),
        duration: 5000
      })
    } catch (error) {
      saveErrors.value = saveErrorsFromError(error)
      toast.add({
        color: 'error',
        icon: 'i-lucide-triangle-alert',
        title: apiErrorReason(error) || t('admin.settings.saveFailed')
      })
    } finally {
      restoring.value = false
    }
  }

  /**
   * overview 的「推荐默认 / 自定义」徽章看的是**已保存**的 `requirements`，不是正在编辑的 `form`。
   */
  const savedRequirements = computed<Record<string, Record<string, number>>>(() => {
    const result: Record<string, Record<string, number>> = {}
    for (const tier of initial.value.tiers) result[tier.id] = tier.requirements
    return result
  })

  return {
    // 数据与状态
    items,
    pending,
    error,
    refresh,
    form,
    tiers,
    activeTiers,
    tierCount,
    canAdd,
    canRetire,
    tierLimitReached,
    canManage,
    hasChanges,
    trustLevelsEnabled,
    saving: section.saving,
    // 五态（页面只读这两个值，不在 shell 里各算一套）
    pageState,
    loadErrorReason,
    hasLoaded,
    savedRequirements,
    recommended,
    usingFallbackRecommended,
    // 白名单四态 + 编辑辅助
    whitelistState,
    loadWhitelist,
    // 保存失败的字段级诊断（P2）
    saveErrors,
    saveErrorBuckets,
    // 档位结构确认（P1-4）
    structureChanged,
    structureConfirmOpen,
    confirmStructureSave,
    cancelStructureConfirm,
    // 档位动作
    addTier,
    insertTierAt,
    moveTier,
    renameTier,
    setIcon,
    setColor,
    restoreTier,
    savedRetiredIds,
    retireRequest,
    requestRetire,
    confirmRetire,
    cancelRetire,
    deleteRequest,
    canDelete,
    requestDelete,
    confirmDelete,
    cancelDelete,
    // 规则面板 + 影响面（M3b）
    conditions,
    catalogUsability,
    saveBlockedByCatalog,
    tierRuleDirty,
    expandedRuleTierId,
    toggleRulePanel,
    setTierRule,
    retryConditions,
    impactOpen,
    impactStatus: ruleImpact.status,
    impactResults: impactRows,
    confirmImpact,
    cancelImpact,
    // 阈值 / 限额 / 解锁 / 保存
    setThreshold,
    setLimitsMode,
    setLimit,
    toggleEarnable,
    save,
    resetChanges,
    restoreThresholds,
    restoreConfirmOpen,
    confirmRestore,
    cancelRestore,
    restoring
  }
}
