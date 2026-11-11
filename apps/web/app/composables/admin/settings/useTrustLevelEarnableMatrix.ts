import {
  earnableKeys,
  TRUST_LEVEL_EDITABLE_LEVELS,
  unknownEarnableKeys,
  type TrustLevelWhitelistState,
  type TrustLevelsForm
} from './useTrustLevelsThresholds'

/**
 * 能力解锁矩阵的派生状态（纯函数，无 Vue、无 i18n、无 DOM）。
 *
 * 住在 composable 而不是 `.vue` 里，是为了能被 bun test 直接断言：
 * 组件只剩模板与绑定，几何/计数/余数/残留这些会算错的地方都在这里。
 * 这里**不**产生任何展示文案（档位名、键展示名由组件用 i18n 组合），
 * 因为文案是产品口径，状态是契约。
 */
export type TrustLevelEarnableOrphan = {
  level: number
  key: string
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
  columnCount: number
  /**
   * 跨列判据是**余数**（规范 §2.3）：`项数 % 列数 === 1` 时该列数下末项跨满。
   * 7 项 → 2 列跨、3 列跨、7 列不跨。白名单是动态的，所以两个布尔都要算出来，
   * 不能把「7 是奇数」写成规则。
   */
  spanTwo: boolean
  spanThree: boolean
  /** 已勾选格数（TL1–TL4，只算可操作键；门控键没有控件）。 */
  checkedCount: number
  /**
   * 计数徽标分母 = **可操作键数** × 4；TL0 不计入（算进去会得 35，那是错的）。
   * 门控键不计入：7 键里有 2 个是系统门控 ⇒ 5×4 = 20，不是 28。
   */
  totalCells: number
  /** 已存配置里不在白名单内的键，按档位升序、档内保持存储顺序。 */
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
  const orphans: TrustLevelEarnableOrphan[] = []
  let checkedCount = 0
  for (const level of TRUST_LEVEL_EDITABLE_LEVELS) {
    const stored = earnableKeys(input.form, level)
    // 门控键的存量勾选**原样保留在表单里**（保存时逐字节回写），但不计入「新增项」：
    // 它不是运营能改的项，算进分子会让 3/20 这种数字指向一个没有控件的格子。
    checkedCount += stored.filter(key => !baselineKeys.includes(key)).length
    if (!ready) continue
    for (const key of unknownEarnableKeys(stored, keys)) orphans.push({ level, key })
  }
  return {
    ready,
    failed: input.whitelist.status === 'failed',
    keys,
    baselineKeys,
    operableKeys,
    editable: ready && input.canManage,
    columnCount,
    spanTwo: columnCount % 2 === 1,
    spanThree: columnCount % 3 === 1,
    checkedCount,
    totalCells: TRUST_LEVEL_EDITABLE_LEVELS.length * operableKeys.length,
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

/** 残留键的人话串：`TL2 · user.ban`，档位升序、档内按存储顺序（规范 §5.3）。 */
export function earnableOrphanText(orphans: readonly TrustLevelEarnableOrphan[]): string {
  return orphans.map(entry => `TL${entry.level} · ${entry.key}`).join('，')
}
