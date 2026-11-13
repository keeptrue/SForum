import { apiErrorMessage, apiErrorFields } from '~/composables/useApiClient'
import { useProfileApi, type PublicProfile } from '~/composables/profile/useProfileApi'
import {
  composeProfileSettingsTabs,
  firstProfileSettingsErrorTab,
  normalizeProfileSettingsSections,
  profileSettingsDraftValuesAsSettings,
  profileSettingsErrorBuckets,
  profileSettingsMetaDirty,
  profileSettingsMetaPayload,
  profileSettingsMetaValues,
  type ProfileSettingsDraftValue,
  type ProfileSettingsSection,
  type ProfileSettingsTabView
} from '~/utils/settings/profileSettings'

/**
 * 资料设置页的草稿所有者：核心字段 + 插件声明式字段（meta）+ 服务端字段错误。
 *
 * 铁律：一次保存只发一个 `PUT /profile`（核心字段与 meta 同体提交），
 * 昵称只在真的改过时才带（服务端把「提交了 displayName」当成一次改名意图），
 * 语言偏好不是资料行字段，单独走 updateLanguage。
 *
 * 这里不持有 tab 选中态：tab 属于 URL（`?tab=`），由页面壳与 TabNav 决定；
 * 本 composable 只负责把字段错误折算成「哪个分区有几处」，供 tab 错误点与页脚汇总消费。
 */
export type ProfileSettingsCoreDraft = {
  displayName: string
  bio: string
  signature: string
  location: string
  websiteUrl: string
  locale: string
}

export type UseProfileSettingsDraftOptions = {
  profile: Ref<PublicProfile | null>
  /** 账号的常用语言（会话/服务端偏好），用作 locale 草稿的基线。 */
  currentLanguage: Ref<string>
  /** 语言偏好保存回调：resolve 即视为已落库（返回值不参与状态）。 */
  updateLanguage: (locale: string) => Promise<unknown>
}

type SaveState = 'idle' | 'saving' | 'error' | 'success'

function emptyCoreDraft(locale = ''): ProfileSettingsCoreDraft {
  return { displayName: '', bio: '', signature: '', location: '', websiteUrl: '', locale }
}

export function useProfileSettingsDraft(options: UseProfileSettingsDraftOptions) {
  const { t, locale } = useI18n()
  const toast = useToast()
  const profileApi = useProfileApi()

  const draft = reactive<ProfileSettingsCoreDraft>(emptyCoreDraft(options.currentLanguage.value))
  const baseline = ref<ProfileSettingsCoreDraft>(emptyCoreDraft(options.currentLanguage.value))
  const metaDraft = ref<Record<string, ProfileSettingsDraftValue>>({})
  const metaBaseline = ref<Record<string, ProfileSettingsDraftValue>>({})
  const fieldErrors = ref<Record<string, string[]>>({})
  const saveState = ref<SaveState>('idle')
  const errorMessage = ref('')
  const formReady = ref(false)

  // 线上分区的展示文案可能是字符串或 locale 映射，字段键可能是 key/fieldKey：
  // 统一在这里归一化，组件只消费一种形状。
  function sectionsOf(data: PublicProfile | null): ProfileSettingsSection[] {
    return normalizeProfileSettingsSections(data?.settingsSections, locale.value)
  }

  const sections = computed<ProfileSettingsSection[]>(() => sectionsOf(options.profile.value))
  const tabs = computed<ProfileSettingsTabView[]>(() => composeProfileSettingsTabs(sections.value, {
    identity: t('profileSettings.tabs.identity'),
    story: t('profileSettings.tabs.story'),
    links: t('profileSettings.tabs.links')
  }))

  const saving = computed(() => saveState.value === 'saving')
  const coreDirty = computed(() => (
    draft.displayName !== baseline.value.displayName
    || draft.bio !== baseline.value.bio
    || draft.signature !== baseline.value.signature
    || draft.location !== baseline.value.location
    || draft.websiteUrl !== baseline.value.websiteUrl
    || draft.locale !== baseline.value.locale
  ))
  const isDirty = computed(() => coreDirty.value || profileSettingsMetaDirty(metaDraft.value, metaBaseline.value))
  const errorBuckets = computed(() => profileSettingsErrorBuckets(fieldErrors.value, sections.value))
  const errorCounts = computed(() => errorBuckets.value.byTab)
  // 不属于任何分区的错误（后端新增键、运营手建字段）单独计数，页脚必须说出来。
  const globalErrorCount = computed(() => errorBuckets.value.global)
  const firstErrorTab = computed(() => firstProfileSettingsErrorTab(errorCounts.value, tabs.value))

  function coreValues(data: PublicProfile, locale: string): ProfileSettingsCoreDraft {
    return {
      displayName: data.displayName || '',
      bio: data.profile.bio || '',
      signature: data.profile.signature || '',
      location: data.profile.location || '',
      websiteUrl: data.profile.websiteUrl || '',
      locale
    }
  }

  function hydrate(data: PublicProfile) {
    const values = coreValues(data, options.currentLanguage.value)
    Object.assign(draft, values)
    baseline.value = { ...values }
    const meta = profileSettingsMetaValues(sectionsOf(data), data.settingsValues)
    metaDraft.value = { ...meta }
    metaBaseline.value = { ...meta }
    formReady.value = true
  }

  /**
   * 保存成功后把「已保存值」当成新基线，表单不再显示未保存。
   *
   * `PUT /profile` 只回显资料行（不回显 settingsSections/settingsValues），所以：
   * - 分区清单沿用当前已知的那份，否则插件 tab 会在保存后凭空消失；
   * - 插件字段基线用「刚提交成功的草稿」，否则刚存的值会被当成默认值从表单里抹掉。
   */
  function resetToSnapshot(data: PublicProfile, submittedMeta?: Record<string, ProfileSettingsDraftValue>) {
    const values = coreValues(data, options.currentLanguage.value)
    Object.assign(draft, values)
    baseline.value = { ...values }
    const sections = data.settingsSections ? sectionsOf(data) : sectionsOf(options.profile.value)
    const settingValues = data.settingsValues ?? (
      submittedMeta ? profileSettingsDraftValuesAsSettings(submittedMeta) : undefined
    )
    const meta = profileSettingsMetaValues(sections, settingValues)
    metaDraft.value = { ...meta }
    metaBaseline.value = { ...meta }
    fieldErrors.value = {}
  }

  /**
   * 保存后的快照：把 PUT 回显的资料行合并到当前 profile 上，保留服务端没回显的
   * settingsSections（插件 tab 依赖它）。调用方拿它替换 profile 引用即可。
   */
  function mergeSnapshot(data: PublicProfile, submittedMeta?: Record<string, ProfileSettingsDraftValue>): PublicProfile {
    const previous = options.profile.value
    return {
      ...(previous || data),
      ...data,
      settingsSections: data.settingsSections ?? previous?.settingsSections,
      settingsValues: data.settingsValues ?? (
        previous?.settingsValues && submittedMeta
          ? { ...previous.settingsValues, ...profileSettingsDraftValuesAsSettings(submittedMeta) }
          : previous?.settingsValues
      )
    }
  }

  function clearFieldError(fieldKey: string) {
    if (!fieldErrors.value[fieldKey]) {
      return
    }
    const { [fieldKey]: _removed, ...rest } = fieldErrors.value
    fieldErrors.value = rest
  }

  function clearFieldErrors() {
    fieldErrors.value = {}
  }

  function dismissError() {
    errorMessage.value = ''
  }

  /** 插件声明式字段的草稿写入（值与 baseline 比较时才提交）。 */
  function setMetaValue(fieldKey: string, value: ProfileSettingsDraftValue) {
    metaDraft.value = { ...metaDraft.value, [fieldKey]: value }
  }

  function resetDraft() {
    if (!isDirty.value) {
      return
    }
    Object.assign(draft, baseline.value)
    metaDraft.value = { ...metaBaseline.value }
    fieldErrors.value = {}
    errorMessage.value = ''
    saveState.value = 'idle'
    toast.add({
      color: 'neutral',
      icon: 'i-lucide-rotate-ccw',
      title: t('profileSettings.resetDone'),
      duration: 5000
    })
  }

  /** 一次 PUT 提交核心字段 + meta；返回最新快照供调用方同步会话。 */
  async function save(): Promise<PublicProfile | null> {
    if (!isDirty.value || saving.value) {
      return null
    }
    saveState.value = 'saving'
    errorMessage.value = ''
    fieldErrors.value = {}
    try {
      const nicknameChanged = draft.displayName !== baseline.value.displayName
      const profileChanged = nicknameChanged
        || draft.bio !== baseline.value.bio
        || draft.signature !== baseline.value.signature
        || draft.location !== baseline.value.location
        || draft.websiteUrl !== baseline.value.websiteUrl
      const meta = profileSettingsMetaPayload(metaDraft.value, metaBaseline.value)
      const metaChanged = Object.keys(meta).length > 0
      let updated: PublicProfile | null = null
      if (profileChanged || metaChanged) {
        updated = await profileApi.updateMyProfile({
          bio: draft.bio,
          signature: draft.signature,
          location: draft.location,
          websiteUrl: draft.websiteUrl,
          // 只在真的改过时提交昵称：服务端把「提交了 displayName」当作一次改名意图。
          ...(nicknameChanged ? { displayName: draft.displayName } : {}),
          ...(metaChanged ? { meta } : {})
        })
      }
      if (draft.locale !== baseline.value.locale) {
        await options.updateLanguage(draft.locale)
        baseline.value = { ...baseline.value, locale: draft.locale }
      }
      const snapshot = updated ? mergeSnapshot(updated, metaDraft.value) : null
      if (snapshot) {
        // 快照归属调用方（会话、预览都要用）；这里只刷新自己的基线。
        resetToSnapshot(snapshot, metaDraft.value)
      }
      saveState.value = 'success'
      // 成功只走 Toast：同一次保存再配一条内联 alert 就是两处同文案的重复提示。
      toast.add({
        color: 'primary',
        icon: 'i-lucide-check',
        title: t('profileSettings.saved'),
        duration: 5000
      })
      return snapshot
    } catch (error) {
      saveState.value = 'error'
      errorMessage.value = apiErrorMessage(error) || t('profileSettings.saveFailed')
      // 字段错误只提示，不自动跳 tab：用户可能正在另一个分区里输入。
      fieldErrors.value = apiErrorFields(error)
      return null
    }
  }

  return {
    draft,
    baseline,
    metaDraft,
    sections,
    tabs,
    fieldErrors,
    errorCounts,
    globalErrorCount,
    firstErrorTab,
    saveState,
    saving,
    errorMessage,
    formReady,
    coreDirty,
    isDirty,
    hydrate,
    resetToSnapshot,
    mergeSnapshot,
    resetDraft,
    save,
    clearFieldError,
    clearFieldErrors,
    dismissError,
    setMetaValue
  }
}
