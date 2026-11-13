import { badgeWornErrorKey, useBadgeWorn } from '~/composables/forum/badges/useBadgeWorn'

/**
 * 佩戴徽章的动作层（资料页徽章墙与全局徽章说明弹窗共用一份）。
 *
 * 语义与原先 `SFProfileBadgeTab.toggleWear` 完全一致，抽出来只为让弹窗不必复制 toast：
 * - 已佩戴当前这枚 → `hide()`（= 不佩戴任何徽章，`PUT {hidden:true}`），toast `hideSuccess`；
 * - 未佩戴 → `wear(slug)`，toast `wearSuccess`（名字来自徽章墙，佩戴层只认 slug）；
 * - 失败回滚由 `useBadgeWorn` 负责，这里只把 `badgeWornErrorKey` 翻成常驻错误 toast。
 *
 * 「回到自动展示」（`clear()`）不在这里：它是资料页底部的独立出口，不属于单枚徽章的开关。
 */
export type WearableBadge = {
  slug: string
  name: string
}

export function useBadgeWearToggle() {
  const { t } = useI18n()
  const toast = useToast()
  const worn = useBadgeWorn()

  async function wearBadge(badge: WearableBadge) {
    try {
      await worn.wear(badge.slug)
      toast.add({
        color: 'success',
        icon: 'i-lucide-check',
        title: t('badges.worn.wearSuccess', { name: badge.name })
      })
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-alert-triangle',
        title: t(badgeWornErrorKey(error)),
        duration: 0
      })
    }
  }

  /** 不佩戴任何徽章（`PUT {hidden:true}`）：已经是 hidden 时不重复发请求。 */
  async function hideWorn() {
    if (worn.isHidden.value) return
    try {
      await worn.hide()
      toast.add({
        color: 'success',
        icon: 'i-lucide-eye-off',
        title: t('badges.worn.hideSuccess')
      })
    } catch (error) {
      toast.add({
        color: 'error',
        icon: 'i-lucide-alert-triangle',
        title: t(badgeWornErrorKey(error)),
        duration: 0
      })
    }
  }

  async function toggleWear(badge: WearableBadge) {
    if (worn.isWorn(badge.slug)) {
      await hideWorn()
      return
    }
    await wearBadge(badge)
  }

  return { worn, toggleWear, wearBadge, hideWorn }
}
