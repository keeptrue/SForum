import type { ProfileExtensionTab, PublicProfile } from '~/composables/profile/useProfileApi'
import {
  normalizePublicFrontendComponentRefs,
  type PublicFrontendComponentRef
} from '~/runtime/public-extensions/pagePolicy'

/**
 * 资料页 tab 栏里由扩展贡献、由宿主原地挂载的 widget tab。
 * `componentId` 已归一为非空字符串：缺它的贡献无法挂载，直接不进入 tab 栏。
 */
export type ProfileWidgetTab = ProfileExtensionTab & { componentId: string }

/**
 * 扩展 tab 的显示名：当前语言 → zh-CN → en-US → 任意一条 → 稳定 id。
 * 降级顺序与右栏入口一致，两处必须同一口径，否则同一入口会出现两个名字。
 */
export function extensionTabLabel(
  tab: Pick<ProfileExtensionTab, 'id' | 'label'>,
  locale: string
): string {
  const labels = tab.label || {}
  return labels[locale] || labels['zh-CN'] || labels['en-US'] || Object.values(labels)[0] || tab.id
}

/** tab 栏内部值：扩展与 id 组合，避免不同扩展的同名 tab 互相抢占激活态。 */
export function profileWidgetTabValue(tab: Pick<ProfileExtensionTab, 'extensionId' | 'id'>): string {
  return `${tab.extensionId}:${tab.id}`
}

/**
 * 与后端逐字对齐的字符串比较：Go 的 `<` 是字节序，JS 的 `<` 是 UTF-16 code unit 序，
 * 对 ID_PATTERN 允许的 ASCII 字符集（`[a-z0-9._-]`）两者完全一致。
 *
 * 不要换回 `localeCompare`：它按语言排序（`a_b` 会排在 `a-b` 前面，字节序相反），
 * 且未指定 locale 时 Node 与浏览器默认 locale 不同还会带来 hydration 抖动。
 */
function compareCodeUnits(left: string, right: string): number {
  if (left === right) {
    return 0
  }
  return left < right ? -1 : 1
}

/**
 * 合并进 tab 栏的 widget tab，排序与后端 effective-contribution 排序同键同序：
 * `order` 升序 → `extensionId` 字节序 → `id` 字节序（`Extensions/service.go` 的
 * `sort.SliceStable`）。后端按同一比较器截断到 8 个，所以这里排出来的前 N 个
 * 必须就是后端活下来的那 N 个，不能有第二种顺序。
 */
export function profileWidgetTabs(extensionTabs: ProfileExtensionTab[] | undefined): ProfileWidgetTab[] {
  const widgets: ProfileWidgetTab[] = []
  for (const tab of extensionTabs || []) {
    if (tab.kind !== 'widget') {
      continue
    }
    const componentId = (tab.componentId || '').trim()
    if (!componentId) {
      continue
    }
    widgets.push({ ...tab, componentId })
  }
  return widgets.sort((left, right) => left.order - right.order
    || compareCodeUnits(left.extensionId, right.extensionId)
    || compareCodeUnits(left.id, right.id))
}

/**
 * 资料页 widget tab 的 L2 组件引用，供页面文档 CSP 单点聚合
 * （`SFPageOutletResolver` → `SFThemeTemplate` → `applyPublicPageDocumentPolicy`）。
 *
 * 粒度**刻意**不同于 `collectRegionWidgetRefs` 的 all-or-nothing：这里逐条丢弃非法 ref，
 * 合法 tab 的 CSP 照常成立。理由是最小爆炸半径——一个插件的坏贡献不该让整页 L2 策略失效，
 * 被丢掉的那条自己挂不上就是它的失败关闭。只有整体归一化抛错（如超过组件上限）才返回 []。
 */
export function collectProfileWidgetRefs(
  profile: PublicProfile | null | undefined
): PublicFrontendComponentRef[] {
  const refs: PublicFrontendComponentRef[] = []
  for (const tab of profileWidgetTabs(profile?.extensionTabs)) {
    try {
      const [normalized] = normalizePublicFrontendComponentRefs([{
        extensionId: tab.extensionId,
        componentId: tab.componentId
      }])
      if (normalized) {
        refs.push(normalized)
      }
    } catch {
      // 非法 extensionId/componentId：丢弃这一条，不让它拖垮整页的 L2 策略。
      // dev 下留线索，否则只能看到一个「没挂上」的面板而不知道是谁被丢了；生产静默。
      if (import.meta.dev) {
        console.warn('[sforum] profile widget ref dropped', {
          extensionId: tab.extensionId,
          componentId: tab.componentId
        })
      }
    }
  }
  if (!refs.length) {
    return []
  }
  try {
    return normalizePublicFrontendComponentRefs(refs)
  } catch {
    if (import.meta.dev) {
      console.warn('[sforum] profile widget refs rejected', { count: refs.length })
    }
    return []
  }
}
