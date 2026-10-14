// AI 面板的比较与归一化：它决定「提示词算不算自定义」与「面板算不算脏」。
//
// 为什么需要归一化：富文本编辑器会把 Markdown 重新排版（段落与列表之间补空行、
// 去掉行尾空白），所以「与内置默认逐字相同」在编辑器往返之后必然不成立。直接拿
// 原字符串比较，会让面板一打开就显示「已自定义 / 有未保存的改动」，而运营者什么
// 都没改。这里把空白差异视为相同，并把「空 / 等于默认」统一规范成同一个形状，
// 使服务端文档与表单副本可比。
//
// 服务端在提示词为空时会省略该字段（omitempty），表单侧则可能持有显式空串——
// 两者语义相同，必须在比较前对齐，否则一次「清空提示词」也会被当成未保存改动。

export type AIComparableSettings = {
  reply?: { systemPrompt?: string, triggerMode?: string }
  gates?: { toolCallsPerReply?: number | null }
  purposes?: Record<string, AIPurposeOverride>
}

// AIPurposeOverride 是某个用途的站点覆盖形状。两个字段都可省：缺省即沿用该用途
// 自己的声明，而不是「空覆盖」。
export type AIPurposeOverride = {
  tools?: { allowed?: string[] }
  failurePosture?: string
}

// 用途覆盖的三形态与「提示词是否使用默认」是同一类问题：字段存在与字段为空在服务端
// 是两种语义，界面必须能把它们区分开，否则一次「点了恢复默认」会因为 JSON.stringify
// 丢掉 undefined 而被判成「有未保存改动」。
export const AI_FAILURE_POSTURES = ['fail_open', 'fail_closed', 'skip_ai'] as const
export type AIFailurePosture = typeof AI_FAILURE_POSTURES[number]

// 站点覆盖里显式接管工具白名单：tools 字段存在即表示以它为准（空列表 = 不允许任何
// 工具），tools 缺失即表示沿用用途声明的默认列表。
export function purposeToolsAreOverridden(override: AIPurposeOverride | undefined): boolean {
  return Boolean(override && override.tools && Array.isArray(override.tools.allowed))
}

// purposeOverrideIsEmpty 判断一份覆盖是否什么都没覆盖。空覆盖与「没有这份配置」在
// 服务端归一化后是同一个东西，界面若保留它只会显示一个假的「已自定义」。
export function purposeOverrideIsEmpty(override: AIPurposeOverride | undefined): boolean {
  if (!override) {
    return true
  }
  const posture = typeof override.failurePosture === 'string' ? override.failurePosture.trim() : ''
  return !purposeToolsAreOverridden(override) && posture === ''
}

// prunePurposeOverrides 去掉所有空覆盖，并在结果为空时返回 undefined（而不是空对象）：
// 服务端用 omitempty 省略这个字段，两边形状必须一致，否则脏判定会永远为真。
export function prunePurposeOverrides(
  purposes: Record<string, AIPurposeOverride> | undefined
): Record<string, AIPurposeOverride> | undefined {
  if (!purposes) {
    return undefined
  }
  const kept: Record<string, AIPurposeOverride> = {}
  for (const [id, override] of Object.entries(purposes)) {
    const key = id.trim()
    if (!key || purposeOverrideIsEmpty(override)) {
      continue
    }
    const entry: AIPurposeOverride = {}
    if (purposeToolsAreOverridden(override)) {
      entry.tools = { allowed: normalizePurposeToolNames(override.tools?.allowed) }
    }
    const posture = typeof override.failurePosture === 'string' ? override.failurePosture.trim() : ''
    if (posture) {
      entry.failurePosture = posture
    }
    kept[key] = entry
  }
  return Object.keys(kept).length > 0 ? kept : undefined
}

/**
 * normalizePurposeToolNames 把界面上的工具名列表整理成服务端接受的形状：去空白、
 * 丢弃空条目、去重并按字母序稳定排序。排序让「同样的集合」永远序列化成同一个字符串，
 * 否则勾选顺序不同就会被当成未保存改动。
 */
export function normalizePurposeToolNames(names: readonly string[] | undefined): string[] {
  const seen = new Set<string>()
  for (const entry of names ?? []) {
    const name = typeof entry === 'string' ? entry.trim() : ''
    if (name) {
      seen.add(name)
    }
  }
  return [...seen].sort()
}

// MAX_TOOLS_PER_REQUEST 与后端 ai.MaxToolsPerRequest 一致：一次请求最多带这么多
// 工具声明，更长的白名单无法被兑现，服务端会直接拒绝保存。
export const MAX_TOOLS_PER_REQUEST = 16

// TOOL_NAME_PATTERN 与后端 ai.ValidToolName 同源：供应商只接受这套字符集。
export const TOOL_NAME_PATTERN = /^[a-z0-9_-]+$/

export function isValidPurposeToolName(name: string | undefined): boolean {
  const value = (name ?? '').trim()
  return value.length > 0 && value.length <= 64 && TOOL_NAME_PATTERN.test(value)
}

// purposeToolOptions 汇总一个用途可勾选的工具：声明默认与运营者已输入的自定义名
// 的并集，按字母序稳定排序。
export function purposeToolOptions(
  declared: readonly string[] | undefined,
  selected: readonly string[] | undefined
): string[] {
  return normalizePurposeToolNames([...(declared ?? []), ...(selected ?? [])])
}

// selectedPurposeTools 返回界面上已勾选的工具：沿用声明时即声明默认（因为没有
// 覆盖，声明就是生效值）。
export function selectedPurposeTools(
  declared: readonly string[] | undefined,
  override: AIPurposeOverride | undefined
): string[] {
  if (!purposeToolsAreOverridden(override)) {
    return normalizePurposeToolNames(declared)
  }
  return normalizePurposeToolNames(override?.tools?.allowed)
}


// DEFAULT_TOOL_CALLS_PER_REPLY 与后端 ai.DefaultToolCallsPerReply 一致，仅用于
// 界面展示：真正的缺省仍然由服务端的 nil 语义决定。
export const DEFAULT_TOOL_CALLS_PER_REPLY = 3

// toolCallsPerReplyUsesDefault 判断工具步数是否等价于「未设置」。
// 服务端用指针字段区分 nil（按默认 3 生效）与 0（停用工具调用）：一旦运营者碰过
// 这个输入框，nil 就会被固化成一个数字，之后升级默认也影响不到这个站点。
export function toolCallsPerReplyUsesDefault(value: number | null | undefined): boolean {
  return value === undefined || value === null || Number.isNaN(value)
}

// promptUsesDefault 判断提示词是否等价于「使用内置默认」：空字符串，或忽略空白
// 差异后与默认提示词相同。
export function promptUsesDefault(prompt: string | undefined, defaultPrompt: string): boolean {
  const normalize = (value: string) => value.replace(/\s+/g, ' ').trim()
  const current = normalize(prompt ?? '')
  if (current === '') {
    return true
  }
  return current === normalize(defaultPrompt ?? '')
}

export type AIModelOption = { label: string, value: string }

export type AIModelSelection = {
  options: AIModelOption[]
  /** 当前模型是否出现在供应商返回的列表里。 */
  currentListed: boolean
}

// modelOptionsForSelect 把供应商返回的模型列表变成下拉选项，并保证「当前值」始终
// 在选项里：供应商可能已经下线了站点正在使用的模型，把它过滤掉会让下拉框看起来
// 「什么都没选中」，而配置里明明写着一个模型。currentListed 让调用方能就这一点
// 单独提示，而不是把差异藏起来。
//
// 顺序保持供应商给出的顺序（多数供应商把最新或最常用的排在前面），并去掉重复与
// 空白条目——重复只会让下拉框变长，不会带来新信息。
export function modelOptionsForSelect(models: readonly string[], current: string): AIModelSelection {
  const seen = new Set<string>()
  const options: AIModelOption[] = []
  for (const entry of models ?? []) {
    const value = typeof entry === 'string' ? entry.trim() : ''
    if (!value || seen.has(value)) continue
    seen.add(value)
    options.push({ label: value, value })
  }
  const selected = (current ?? '').trim()
  const currentListed = selected !== '' && seen.has(selected)
  if (selected !== '' && !currentListed) {
    options.push({ label: selected, value: selected })
  }
  return { options, currentListed }
}

// normalizedAISettingsForComparison 返回可逐字比较的副本：把「使用默认」的提示词
// 统一写成空串，其余字段原样保留。
//
// toolCallsPerReply 必须显式对齐：JSON.stringify 会丢掉 undefined，nil（未设置）
// 与 3（显式等于默认）在字符串层面不可区分，而它们对服务端是两种语义。
export function normalizedAISettingsForComparison<T extends AIComparableSettings>(
  settings: T,
  defaultPrompt: string
): T {
  const copy = JSON.parse(JSON.stringify(settings)) as T
  const prompt = typeof copy.reply?.systemPrompt === 'string' ? copy.reply.systemPrompt : ''
  if (promptUsesDefault(prompt, defaultPrompt)) {
    copy.reply = { ...copy.reply, systemPrompt: '' }
  }
  // triggerMode 同样要按服务端归一化对齐：缺省 / 未知值 / 'mention' 在服务端是
  // 同一语义（mention），不统一会让面板在未改动时误报 dirty。
  if (copy.reply && 'triggerMode' in copy.reply) {
    const mode = (copy.reply as { triggerMode?: string }).triggerMode
    if (mode === undefined || mode === null || mode === '' || mode === 'mention' || !['mention', 'autonomous', 'off'].includes(mode)) {
      delete (copy.reply as { triggerMode?: string }).triggerMode
    }
  }
  if (copy.gates && 'toolCallsPerReply' in copy.gates) {
    if (toolCallsPerReplyUsesDefault(copy.gates.toolCallsPerReply)) {
      delete copy.gates.toolCallsPerReply
    } else {
      // 显式值统一成数字，避免 "3" 与 3 被当成改动。
      copy.gates.toolCallsPerReply = Number(copy.gates.toolCallsPerReply)
    }
  }
  // 用途覆盖同样要按服务端归一化后的形状对齐：空覆盖会被服务端丢弃，整个 map 为空
  // 时字段被省略。不在这里对齐，界面就会在「什么都没改」时判定为脏。
  const purposes = prunePurposeOverrides(copy.purposes)
  if (purposes) {
    copy.purposes = purposes
  } else {
    delete copy.purposes
  }
  return copy
}
