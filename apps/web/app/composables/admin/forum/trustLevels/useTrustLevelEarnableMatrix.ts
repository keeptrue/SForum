import { activeTiersOf, type TierForm } from './useTrustLevelsTiers'
import {
  earnableKeys,
  unknownEarnableKeys,
  type TrustLevelWhitelistState,
  type TrustLevelsForm
} from './useTrustLevelsForm'

/**
 * 能力解锁矩阵的派生状态（纯函数，无 Vue、无 i18n、无 DOM）。
 *
 * 住在 composable 而不是 `.vue` 里，是为了能被 bun test 直接断言：
 * 组件只剩模板与绑定，几何/计数/余数/残留这些会算错的地方都在这里。
 * 这里**不**产生任何展示文案（档位名、键展示名由组件用 i18n 组合），
 * 因为文案是产品口径，状态是契约。
 *
 * 几何口径（D-N12）：
 * - 每档一行（表格）或一张卡（卡片），行内是 7 个勾选格 —— 所以**档数**决定布局，
 *   **键数**决定每行/每卡的格数与跨列余数；
 * - ≤6 档用表格，7–8 档切成按档分组卡片（390px 下 8 列每列约 44px，表头已经读不了）。
 */

/** 矩阵布局：≤6 档表格，7–8 档卡片（D-N12）。 */
export type TrustLevelEarnableLayout = 'table' | 'cards'

export const TRUST_LEVEL_EARNABLE_TABLE_MAX_TIERS = 6

export type TrustLevelEarnableOrphan = {
  /** 档位 id（不是 rank：停用/重排都不该让残留披露指向别的档）。 */
  tierId: string
  key: string
}

export type TrustLevelEarnableRow = {
  id: string
  /** active 位次（1 起）；TL0 不在矩阵里。 */
  rank: number
  tier: TierForm
}

export type TrustLevelEarnableMatrixState = {
  /** `whitelist.status === 'ready'`：唯一可以画格子的状态。 */
  ready: boolean
  failed: boolean
  /** 仅在 ready 时非空；failed 时同样是 `[]`，所以禁止用长度判断「没有可勾的键」。 */
  keys: string[]
  /**
   * 系统基线键（白名单子集）：默认安装下 TL0 不生效、TL1 起生效。
   * 只来自后端 `baselinePermissions`，**非空才展示标注**；空 = 无可披露的基线，不是「没有基线」的断言。
   */
  baselineKeys: string[]
  /** 可操作键 = 白名单 − 系统基线键。S2 没有控件，所以它不进分母也不进分子（规范 §6.2）。 */
  operableKeys: string[]
  /** 只有「白名单就绪 + 有写权限」才允许勾选。 */
  editable: boolean
  /** 参与矩阵的 active 档位（顺序 = 判定与展示顺序）。 */
  rows: TrustLevelEarnableRow[]
  /** active 档数（= 行数 = 列数的历史口径，见文件头）。 */
  tierCount: number
  /** 每行/每卡的勾选格数 = 白名单键数。 */
  columnCount: number
  layout: TrustLevelEarnableLayout
  /**
   * 跨列判据是**余数**（规范 §2.3）：`格数 % 列数 === 1` 时该列数下末项跨满。
   * 7 格 → 2 列跨、3 列跨、7 列不跨。白名单是动态的，所以两个布尔都要算出来，
   * 不能把「7 是奇数」写成规则。
   */
  spanTwo: boolean
  spanThree: boolean
  /** 已勾选格数（active 档，只算可操作键；门控键没有控件）。 */
  checkedCount: number
  /**
   * 计数徽标分母 = **可操作键数** × **active 档数**；TL0 不计入（它不在 tiers 数组里）。
   * 门控键不计入：7 键里有 2 个是系统门控 ⇒ 5×N。
   */
  totalCells: number
  /** 已存配置里不在白名单内的键，按档位顺序、档内保持存储顺序。 */
  orphans: TrustLevelEarnableOrphan[]
}

export function earnableMatrixState(input: {
  form: TrustLevelsForm
  whitelist: TrustLevelWhitelistState
  canManage: boolean
}): TrustLevelEarnableMatrixState {
  const ready = input.whitelist.status === 'ready'
  const keys = ready ? input.whitelist.keys : []
  const baselineKeys = ready ? input.whitelist.baselineKeys : []
  const operableKeys = keys.filter(key => !baselineKeys.includes(key))
  const columnCount = keys.length
  const rows: TrustLevelEarnableRow[] = activeTiersOf(input.form.tiers)
    .map((tier, index) => ({ id: tier.id, rank: index + 1, tier }))
  const orphans: TrustLevelEarnableOrphan[] = []
  let checkedCount = 0
  for (const row of rows) {
    const stored = earnableKeys(input.form, row.id)
    // 门控键的存量勾选**原样保留在表单里**（保存时逐字节回写），但不计入「新增项」：
    // 它不是运营能改的项，算进分子会让 3/20 这种数字指向一个没有控件的格子。
    checkedCount += stored.filter(key => !baselineKeys.includes(key)).length
    if (!ready) continue
    for (const key of unknownEarnableKeys(stored, keys)) orphans.push({ tierId: row.id, key })
  }
  return {
    ready,
    failed: input.whitelist.status === 'failed',
    keys,
    baselineKeys,
    operableKeys,
    editable: ready && input.canManage,
    rows,
    tierCount: rows.length,
    columnCount,
    layout: rows.length <= TRUST_LEVEL_EARNABLE_TABLE_MAX_TIERS ? 'table' : 'cards',
    spanTwo: columnCount % 2 === 1,
    spanThree: columnCount % 3 === 1,
    checkedCount,
    totalCells: rows.length * operableKeys.length,
    orphans
  }
}

/**
 * 某键是否是系统基线键（TL0 不生效、TL1 起生效）。
 *
 * 判定只读 `baselineKeys`：组件里出现键名字面量就是把「哪两个键」变成第二份副本，
 * 后端换基线时 UI 会继续给旧键贴标签。
 */
export function isBaselineKey(state: TrustLevelEarnableMatrixState, key: string): boolean {
  return state.baselineKeys.includes(key)
}

/**
 * 残留键的人话串：`正式用户 · user.ban`，档位顺序、档内按存储顺序（规范 §5.3）。
 * 档位名由调用方用 i18n 解析（这里不引入文案）。
 */
export function earnableOrphanText(
  orphans: readonly TrustLevelEarnableOrphan[],
  tierLabel: (tierId: string) => string
): string {
  return orphans.map(entry => `${tierLabel(entry.tierId)} · ${entry.key}`).join('，')
}
