/**
 * 站内庆祝（celebration）的呈现映射：纯函数，不碰 Vue、不发请求。
 *
 * wire 契约由后端冻结（`GET /notifications/celebrations`，见 task-2 汇报）：
 * `titleKey` / `bodyKey` 是服务端声明的 i18n 键，`label` / `body` 是逐语言兜底文案，
 * `params` 是这些模板的参数。前端只决定「用键还是用兜底」，不改写文案语义。
 *
 * 四条硬口径：
 * - 键命中（`te` 为真）才用 `t(key, params)`；未命中按当前 locale 选 label/body，选中为空回落另一种。
 * - 模板里没有被 `params` 满足的 `{name}` 不以字面量出现在界面上（见 formatCelebrationTemplate）。
 * - 站内绝对路径才当跳转目标；没有可靠目标就不渲染成链接（不猜路由）。
 * - `total` 大于实际条目数时用聚合文案；弹窗列出全部条目，数量口径由服务端给。
 */

export type CelebrationLocaleText = {
  zh?: string
  en?: string
}

/**
 * 服务端庆祝条目。字段名与后端逐字一致（`Notifications/celebrations.go`），
 * 不要在这里改别名：`titleKey` / `bodyKey` / `targetPath` 在插件类型上是 JSON null，
 * `label` / `body` 是 `{zh,en}` 双语言对象，`params` 恒有但可能为空。
 */
export type CelebrationItem = {
  id: number
  type: string
  icon?: string | null
  label?: CelebrationLocaleText | string | null
  body?: CelebrationLocaleText | string | null
  titleKey?: string | null
  bodyKey?: string | null
  params?: Record<string, string | number> | null
  targetPath?: string | null
  createdAt?: string
}

/** `GET /notifications/celebrations` 的响应形状。 */
export type CelebrationPage = {
  initialized: boolean
  cursorId: number
  total?: number
  items: CelebrationItem[]
}

export type CelebrationPresentation = {
  id: number
  type: string
  icon: string
  title: string
  body: string
  actionLabel: string
  targetPath: string
  /** 聚合口径：`total` 与 items 数量取大者；> 1 时标题换成聚合文案。 */
  count: number
}

export type CelebrationTranslator = (key: string, params?: Record<string, string | number>) => string
export type CelebrationKeyProbe = (key: string) => boolean

export type CelebrationPresentationOptions = {
  locale: string
  t: CelebrationTranslator
  te: CelebrationKeyProbe
}

/** 弹窗里的一行：`text` 已经是「人话」，组件不再拼徽章名。 */
export type CelebrationDialogRow = {
  id: number
  type: string
  icon: string
  text: string
}

/**
 * 居中庆祝弹窗的完整模型。宿主只按字段渲染，不再自己算聚合、回落或按钮文案。
 *
 * 与浮卡的口径差异只有一处：浮卡一次只画一条（第一条），弹窗**列出全部条目**。
 * 文案回落链完全复用 `celebrationPresentation`，不复制第二份。
 */
export type CelebrationDialogModel = {
  count: number
  title: string
  description: string
  rows: CelebrationDialogRow[]
  actionLabel: string
  targetPath: string
}

const DEFAULT_CELEBRATION_ICON = 'i-tabler-award'
const PLACEHOLDER_PATTERN = /\{(\w+)\}/g
const UNRESOLVED_PLACEHOLDER_PATTERN = /\{\w+\}/

/**
 * 填模板参数：缺参的占位符整段消失，而不是留成 `{badgeName}` 字面量。
 * 顺带收拾缺参留下的空引号与多余空白（「你获得了「」」→「你获得了」）。
 */
export function formatCelebrationTemplate(template: string, params: Record<string, string | number> = {}): string {
  const filled = String(template ?? '').replace(PLACEHOLDER_PATTERN, (_match, name: string) => {
    const value = params[name]
    if (value === undefined || value === null) return ''
    const text = String(value).trim()
    return text
  })
  return filled
    .replace(/[「『“"][\s]*[」』”"]/g, '')
    .replace(/[ \t]{2,}/g, ' ')
    .replace(/\s+([，。、；：！？,.!?;:])/g, '$1')
    .trim()
}

/** 逐语言选文案：zh 前缀取 zh，其余取 en；选中为空回落另一种。 */
export function pickCelebrationText(
  text: CelebrationLocaleText | string | null | undefined,
  locale: string
): string {
  if (!text) return ''
  if (typeof text === 'string') return text.trim()
  const isChinese = String(locale || '').toLowerCase().startsWith('zh')
  const primary = isChinese ? text.zh : text.en
  const secondary = isChinese ? text.en : text.zh
  return String(primary ?? '').trim() || String(secondary ?? '').trim()
}

/** 渲染结果里还留着 `{name}`：说明 params 满足不了这个模板。 */
export function hasUnresolvedPlaceholder(text: string): boolean {
  return UNRESOLVED_PLACEHOLDER_PATTERN.test(String(text ?? ''))
}

/**
 * 键命中且**参数齐备**才返回译文。
 *
 * 两种「不可用」都当作未命中，继续往下回落而不是硬渲染：
 * - `t()` 返回空串（locale 里是空值）：绝不能拿它当标题/正文。
 * - 译完还留着 `{badgeName}`：参数不全，交给服务端下发的纯文本兜底
 *   （`item.label` / `item.body`），这才是主路径。
 */
function translateComplete(
  key: string | null | undefined,
  params: Record<string, string | number>,
  options: CelebrationPresentationOptions
): string {
  const trimmed = String(key ?? '').trim()
  if (!trimmed || !options.te(trimmed)) return ''
  const rendered = String(options.t(trimmed, params) ?? '').trim()
  if (!rendered || hasUnresolvedPlaceholder(rendered)) return ''
  return rendered
}

/**
 * 最后一道防线：服务端连兜底文本都没给时，才用「吃掉占位符」的结果
 * （`formatCelebrationTemplate`），宁可断句也不把 `{badgeName}` 画到页面上。
 */
function translateEaten(
  key: string | null | undefined,
  params: Record<string, string | number>,
  options: CelebrationPresentationOptions
): string {
  const trimmed = String(key ?? '').trim()
  if (!trimmed || !options.te(trimmed)) return ''
  return formatCelebrationTemplate(options.t(trimmed, params), params)
}

function typeFallbackKey(item: CelebrationItem): string {
  return `celebrations.types.${String(item.type ?? '').trim()}`
}

/** 单条庆祝条目 → 卡片文案。 */
export function celebrationPresentation(
  item: CelebrationItem,
  options: CelebrationPresentationOptions
): CelebrationPresentation {
  const params = item.params ?? {}
  const targetPath = typeof item.targetPath === 'string' && item.targetPath.startsWith('/')
    ? item.targetPath
    : ''

  // 标题链：参数齐备的键 → 服务端逐语言 label → 类型名 → 通用兜底 → 吃掉占位符的报文。
  const title = translateComplete(item.titleKey, params, options)
    || formatCelebrationTemplate(pickCelebrationText(item.label, options.locale), params)
    || translateComplete(typeFallbackKey(item), params, options)
    || translateComplete('celebrations.plugin.title', params, options)
    || translateEaten(item.titleKey, params, options)

  // 正文链：参数齐备的键 → 服务端逐语言 body → 通用兜底 → 吃掉占位符的报文。
  const body = translateComplete(item.bodyKey, params, options)
    || formatCelebrationTemplate(pickCelebrationText(item.body, options.locale), params)
    || translateComplete('celebrations.plugin.body', params, options)
    || translateEaten(item.bodyKey, params, options)

  // 单条口径恒为 1；聚合数量在 celebrationDialogModel 里按 total/条目数取大者改写。
  const count = 1

  return {
    id: Number(item.id) || 0,
    type: String(item.type ?? ''),
    icon: String(item.icon ?? '').trim() || DEFAULT_CELEBRATION_ICON,
    title,
    body,
    actionLabel: targetPath ? options.t('celebrations.view') : '',
    targetPath,
    count
  }
}

/**
 * 居中庆祝弹窗的模型：**列出全部条目**，数量取 `max(total, 实际条目数)`。
 *
 * 三条口径与浮卡一致（所以直接复用 `celebrationPresentation`，不复制回落链）：
 * - 标题：多条时用聚合文案（数量取 total），单条时用该条标题，都空才用通用标题。
 * - 正文：多条时是聚合说明（逐条差异在行里），单条时是该条正文，不重复。
 * - 目标：取第一条有站内绝对路径的条目；一个都没有就只保留关闭（不猜路由）。
 *
 * 没有可用条目（或每条都渲染不出任何文字）时返回 null —— 宿主据此不渲染任何 DOM。
 */
export function celebrationDialogModel(
  items: CelebrationItem[],
  total: number | undefined,
  options: CelebrationPresentationOptions
): CelebrationDialogModel | null {
  const available = Array.isArray(items) ? items.filter(item => item && typeof item === 'object') : []
  if (available.length === 0) return null

  const presented = available.map(item => celebrationPresentation(item, options))
  // 行文案取 body（它才是「拿到了哪枚」），body 空才退标题；两者都空的行整条丢掉。
  const rows: CelebrationDialogRow[] = presented
    .map(row => ({ id: row.id, type: row.type, icon: row.icon, text: row.body || row.title }))
    .filter(row => row.text !== '')
  if (rows.length === 0) return null

  const requested = Math.max(0, Math.trunc(Number(total) || 0))
  const count = Math.max(requested, rows.length)
  const first = presented[0]!
  const aggregate = count > 1
  const targetPath = presented.find(row => row.targetPath)?.targetPath ?? ''

  return {
    count,
    title: aggregate
      ? options.t('celebrations.dialog.aggregate', { count })
      : first.title || options.t('celebrations.dialog.title'),
    description: aggregate ? options.t('celebrations.dialog.aggregateBody', { count }) : '',
    rows,
    actionLabel: targetPath ? options.t('celebrations.dialog.view') : '',
    targetPath
  }
}
