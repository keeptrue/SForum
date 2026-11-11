import type { ProfileExtensionTab } from '~/composables/profile/useProfileApi'
import { extensionTabLabel, profileWidgetTabValue, profileWidgetTabs } from '~/utils/profile/profileExtensionTab'

export type ProfileTabOption = {
  value: string
  label: string
  count?: number
}

export type ProfileTabsInput = {
  t: (key: string) => string
  locale: string
  topicCount: number
  commentCount: number
  bookmarksEnabled: boolean
  badgesEnabled: boolean
  extensionTabs?: ProfileExtensionTab[]
}

/**
 * 资料页 tab 栏的唯一拼装点。
 *
 * 顺序是产品约定：内置 tab（topics / replies / bookmarks / badges / about）全部排在
 * 「关于」之前，扩展 widget tab 一律在「关于」之后 —— 内置项与扩展项不能混成一段。
 * 收藏与徽章在各自开关关闭时整项消失，不留占位。
 */
export function buildProfileTabs(input: ProfileTabsInput): ProfileTabOption[] {
  return [
    { value: 'topics', label: input.t('profile.tabs.topics'), count: input.topicCount },
    { value: 'replies', label: input.t('profile.tabs.replies'), count: input.commentCount },
    ...(input.bookmarksEnabled ? [{ value: 'bookmarks', label: input.t('profile.tabs.bookmarks') }] : []),
    ...(input.badgesEnabled ? [{ value: 'badges', label: input.t('profile.tabs.badges') }] : []),
    { value: 'about', label: input.t('profile.tabs.about') },
    ...profileWidgetTabs(input.extensionTabs).map(tab => ({
      value: profileWidgetTabValue(tab),
      label: extensionTabLabel(tab, input.locale)
    }))
  ]
}
