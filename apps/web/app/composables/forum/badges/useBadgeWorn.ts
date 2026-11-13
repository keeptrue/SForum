import { computed, ref } from 'vue'
import { apiErrorReason } from '~/composables/useApiClient'

/**
 * 资料页「佩戴徽章」的数据层（`GET/PUT/DELETE /badges/worn`）。
 *
 * 服务端契约：响应恒为 `{ mode, worn, automatic, upgrade }`。
 * - `mode`：`manual` = 用户选过，`auto` = 由规则自动推导，`hidden` = 用户选择不佩戴
 *   任何徽章（此时 `worn` 为空，`automatic` 仍返回，`upgrade` 恒为空）。
 * - `worn`：当前展示的徽章；`automatic`：自动口径下的最佳徽章；`upgrade`：用户选的那枚
 *   已经不是最佳时给出的「可升级」候选。
 *
 * 三件事在这里定死：
 * 1. **只认 slug**。服务端给对象还是裸字符串都归一成 slug（名字由徽章列表提供，
 *    避免同一条徽章在佩戴层与列表层出现两套文案）。
 * 2. **乐观更新**：点下去立刻改本地，失败回滚到点击前的快照并抛出可提示的错误。
 * 3. **不覆盖用户选择**：佩戴后不回退成 automatic，清空后按服务端返回的 auto 口径走。
 *
 * 三个出口对应三种 mode，**不要合并**：`wear()` = 佩戴（manual）、`hide()` = 取消佩戴 /
 * 不佩戴任何徽章（hidden）、`clear()` = 底部「自动展示徽章」（auto，DELETE）。
 */
export type BadgeWornMode = 'auto' | 'manual' | 'hidden'

export type BadgeWornState = {
  mode: BadgeWornMode
  wornSlug: string
  automaticSlug: string
  upgradeSlug: string
}

export type BadgeWornWireResponse = {
  mode?: string
  worn?: unknown
  automatic?: unknown
  upgrade?: unknown
}

export function emptyBadgeWornState(): BadgeWornState {
  return { mode: 'auto', wornSlug: '', automaticSlug: '', upgradeSlug: '' }
}

/** 服务端可能给 `{ slug, name, ... }` 也可能给裸 slug；两种都归一成 slug。 */
export function wornSlugOf(value: unknown): string {
  if (typeof value === 'string') return value.trim()
  if (!value || typeof value !== 'object') return ''
  const slug = (value as { slug?: unknown }).slug
  return typeof slug === 'string' ? slug.trim() : ''
}

export function badgeWornStateFrom(response: BadgeWornWireResponse | null | undefined): BadgeWornState {
  const mode = response?.mode === 'manual' ? 'manual' : response?.mode === 'hidden' ? 'hidden' : 'auto'
  return {
    mode,
    wornSlug: mode === 'hidden' ? '' : wornSlugOf(response?.worn),
    automaticSlug: wornSlugOf(response?.automatic),
    upgradeSlug: wornSlugOf(response?.upgrade)
  }
}

/**
 * 失败提示的分类：未持有/非法 slug（422）说「这枚徽章不在你的徽章墙里」，
 * 其余（网络、登录失效、服务端错误）统一说保存失败。错误码由服务端 reason 判定。
 */
export function badgeWornErrorKey(error: unknown): string {
  const reason = apiErrorReason(error) || ''
  if (reason.includes('not_owned') || reason.includes('not_owned_badge')) {
    return 'badges.worn.notOwned'
  }
  if (errorStatus(error) === 422) return 'badges.worn.notOwned'
  return 'badges.worn.saveFailed'
}

function errorStatus(error: unknown): number | null {
  if (!error || typeof error !== 'object') return null
  const record = error as { status?: unknown, statusCode?: unknown, response?: { status?: unknown } }
  for (const candidate of [record.status, record.statusCode, record.response?.status]) {
    if (typeof candidate === 'number') return candidate
  }
  return null
}

export function useBadgeWorn() {
  const { request } = useApiClient()
  const state = useState<BadgeWornState>('badge-worn', emptyBadgeWornState)
  const loading = useState<boolean>('badge-worn-loading', () => false)
  const failed = useState<boolean>('badge-worn-failed', () => false)
  const loaded = useState<boolean>('badge-worn-loaded', () => false)

  const wornSlug = computed(() => state.value.wornSlug)
  const upgradeSlug = computed(() => (state.value.mode === 'manual' ? state.value.upgradeSlug : ''))
  const isWorn = (slug: string) => Boolean(slug) && state.value.wornSlug === slug

  function apply(response: BadgeWornWireResponse) {
    state.value = badgeWornStateFrom(response)
    loaded.value = true
    failed.value = false
  }

  async function load() {
    loading.value = true
    failed.value = false
    try {
      apply(await request<BadgeWornWireResponse>('/badges/worn'))
    } catch {
      // 失败不清空已加载的佩戴态：资料页其余的徽章仍要正常展示。
      failed.value = true
    } finally {
      loading.value = false
    }
  }

  async function ensureLoaded() {
    if (loaded.value || loading.value) return
    await load()
  }

  /** 乐观佩戴：先改本地，失败回滚并抛出（由调用方决定怎么提示）。 */
  async function wear(slug: string) {
    const target = String(slug || '').trim()
    if (!target) return
    const snapshot = { ...state.value }
    state.value = { ...snapshot, mode: 'manual', wornSlug: target, upgradeSlug: '' }
    try {
      apply(await request<BadgeWornWireResponse>('/badges/worn', {
        method: 'PUT',
        body: { slug: target }
      }))
    } catch (error) {
      state.value = snapshot
      throw error
    }
  }

  /** 自动展示徽章（DELETE）：本地先按自动口径回显，服务端回包为准。只服务底部「自动展示徽章」。 */
  async function clear() {
    const snapshot = { ...state.value }
    state.value = { ...snapshot, mode: 'auto', wornSlug: snapshot.automaticSlug, upgradeSlug: '' }
    try {
      apply(await request<BadgeWornWireResponse>('/badges/worn', { method: 'DELETE' }))
    } catch (error) {
      state.value = snapshot
      throw error
    }
  }

  /** 取消佩戴 = 不佩戴任何徽章（PUT {hidden:true}）：乐观置 hidden，失败回滚并抛出；wear/clear 成功即自然解除隐藏。 */
  async function hide() {
    const snapshot = { ...state.value }
    state.value = { ...snapshot, mode: 'hidden', wornSlug: '', upgradeSlug: '' }
    try {
      apply(await request<BadgeWornWireResponse>('/badges/worn', {
        method: 'PUT',
        body: { hidden: true }
      }))
    } catch (error) {
      state.value = snapshot
      throw error
    }
  }

  return {
    state,
    wornSlug,
    upgradeSlug,
    mode: computed(() => state.value.mode),
    isHidden: computed(() => state.value.mode === 'hidden'),
    loading,
    failed,
    loaded,
    isWorn,
    load,
    ensureLoaded,
    wear,
    clear,
    hide
  }
}
