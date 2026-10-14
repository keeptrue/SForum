import type { InjectionKey } from 'vue'
import type { AdminAIContext, AdminAIProfile, AdminAISettings } from '~/composables/admin/useAdminAI'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { normalizedAISettingsForComparison } from '~/utils/admin/adminAI'

// 页面壳创建这份草稿，所有页签注入同一份：回复提示词与供应商是同一次保存的内容，
// 拆到两个页签后仍然必须共享同一个 form、同一次 dirty 判定与同一个保存动作。
// 否则在提示词页签里改完，切回供应商页签会看到一份「没有改动」的副本。
export type AdminAISettingsDraft = ReturnType<typeof useAdminAISettingsForm>
export const ADMIN_AI_DRAFT_KEY: InjectionKey<AdminAISettingsDraft> = Symbol('sforum.admin.ai.draft')

export const CUSTOM_PROFILE_ID = 'custom'

/**
 * useAdminAISettingsForm 持有 AI 配置的可编辑草稿：表单副本、单选供应商、脏判定
 * 与保存。它不负责渲染，也不重复拉取数据——数据仍由 useAdminAI 持有。
 */
export function useAdminAISettingsForm(ai: AdminAIContext) {
  const { t } = useI18n()
  const { can } = useAuthSession()
  const canManage = computed(() => can('ai.manage'))
  // 只读态供各页签与子区块复用：权限不足就意味着不能改任何设置。
  const readOnly = computed(() => !canManage.value)

  const form = ref<AdminAISettings | null>(null)
  // selectedProviderId 是界面选择，profile.enabled 才是实际生效：两者分开，
  // 是为了让「选了自定义但还没填完」这种中间状态可以保存而不被拒绝。
  const selectedProviderId = ref('')

  watch(ai.settings, (next) => {
    // 载荷未就绪（或读取失败）时保持 form=null：用空兜底值落草稿会让「已保存基准」
    // 与草稿在 SSR/客户端两处不一致——SSR 上 watch(immediate) 先跑、客户端水合后
    // 载荷才到位，dirty 提示于是只在一侧渲染，直接造成 hydration mismatch。
    // 各页签的 v-if="form" 与页面壳的加载态本来就该在无载荷时不渲染表单。
    if (ai.loading.value || ai.loadFailed.value) {
      form.value = null
      return
    }
    form.value = JSON.parse(JSON.stringify(next)) as AdminAISettings
    // 已保存的文档可能来自引入 reply 之前的版本，那时这个字段不存在。
    // 先补全再绑定，否则任何 .trim() 都会炸在 undefined 上。
    if (form.value && (typeof form.value.reply?.systemPrompt !== 'string')) {
      form.value.reply = { systemPrompt: '', triggerMode: 'mention' }
    }
    if (form.value && !form.value.reply.triggerMode) {
      form.value.reply.triggerMode = 'mention'
    }
    // 没有自定义时在编辑器里预填内置默认：运营者要能看见默认值是什么，
    // 而不是面对一个空框去猜。「使用默认」的语义由保存时的归一化维持。
    if (form.value && !form.value.reply.systemPrompt.trim()) {
      form.value.reply.systemPrompt = ai.defaultSystemPrompt.value
    }
    const active = next.profiles.find(profile => profile.enabled)
    selectedProviderId.value = next.enabled ? (active?.id ?? '') : ''
  }, { immediate: true })

  const providerOptions = computed(() => {
    const builtins = (form.value?.profiles ?? [])
      .filter(profile => profile.id !== CUSTOM_PROFILE_ID)
      .map(profile => ({
        id: profile.id,
        label: profile.label,
        hint: `${profile.protocol} · ${profile.model || t('admin.ai.modelMissing')}`
      }))
    return [
      { id: '', label: t('admin.ai.providerNone'), hint: t('admin.ai.providerNoneHint') },
      ...builtins,
      { id: CUSTOM_PROFILE_ID, label: t('admin.ai.providerCustom'), hint: t('admin.ai.providerCustomHint') }
    ]
  })

  const selectedProfile = computed<AdminAIProfile | null>(() =>
    form.value?.profiles.find(profile => profile.id === selectedProviderId.value) ?? null
  )
  const isCustom = computed(() => selectedProviderId.value === CUSTOM_PROFILE_ID)
  const profileIncomplete = computed(() =>
    selectedProfile.value ? !isProfileComplete(selectedProfile.value) : false
  )

  // 与内置默认相同的文本不算自定义：空白差异（编辑器会重排 Markdown）与「空串 /
  // 字段被省略」两种写法都视为「使用默认」。比较与保存都走这里，避免「预填了默认
  // 就被当成已修改」——那会让保存按钮一直亮着，也会把默认值固化成自定义内容。
  function normalizedForComparison(settings: AdminAISettings) {
    return normalizedAISettingsForComparison(settings, ai.defaultSystemPrompt.value)
  }

  const dirty = computed(() => {
    // 载荷未就绪时没有「已保存基准」可比：此时草稿是空兜底的复制，任何 dirty=true
    // 都是假的。它还会造成 hydration mismatch——SSR 渲染出「有未保存的改动」，
    // 客户端水合后载荷到位、dirty 变 false，同一节点两种结果。
    if (!form.value || ai.loading.value) return false
    return JSON.stringify(normalizedForComparison(form.value)) !== JSON.stringify(normalizedForComparison(ai.settings.value))
  })

  function isProfileComplete(profile: AdminAIProfile) {
    return Boolean(profile.label.trim() && profile.baseUrl.trim() && profile.model.trim())
  }

  // 只有一个提供商生效。选中项配置不完整时保持未启用——启用一份不完整的配置
  // 只会让每次调用都失败，而那正是后端拒绝 422 的条件。
  function syncEnabledProfiles() {
    const current = form.value
    if (!current) return
    for (const profile of current.profiles) {
      profile.enabled = profile.id === selectedProviderId.value && isProfileComplete(profile)
    }
    current.enabled = selectedProviderId.value !== ''
    // 单选模式下成本等级必然指向唯一启用的提供商。不同步的话，界面选了 A 而
    // 绑定仍写着 B，实际调用会走 B——界面就在撒谎。
    if (selectedProviderId.value) {
      for (const costClass of ['economy', 'standard', 'premium']) {
        current.costClassProfiles[costClass] = selectedProviderId.value
      }
    }
  }

  function selectProvider(id: string) {
    selectedProviderId.value = id
    if (id === CUSTOM_PROFILE_ID) ensureCustomProfile()
    syncEnabledProfiles()
  }

  // 自定义提供商在首次选中时创建，之后一直保留：切走再切回来不该丢掉已填内容。
  function ensureCustomProfile() {
    const current = form.value
    if (!current || current.profiles.some(profile => profile.id === CUSTOM_PROFILE_ID)) return
    current.profiles.push({
      id: CUSTOM_PROFILE_ID,
      label: t('admin.ai.providerCustom'),
      protocol: 'openai-chat',
      baseUrl: '',
      apiKeyRef: 'sforum.secret://core/ai.custom.api_key',
      model: '',
      costClass: 'standard',
      enabled: false,
      price: { inputPerMillionMicros: 0, outputPerMillionMicros: 0 },
      defaults: { maxTokens: 2048, timeoutMs: 30000 }
    })
  }

  async function save() {
    if (!form.value) return
    syncEnabledProfiles()
    // 归一化后再提交：与默认逐字相同的内容以空值存库，保持「使用默认」语义，
    // 这样以后升级内置提示词时这部分站点会跟着更新。
    await ai.saveSettings(normalizedForComparison(form.value))
  }

  return {
    ai,
    canManage,
    readOnly,
    form,
    dirty,
    selectedProviderId,
    selectedProfile,
    isCustom,
    profileIncomplete,
    providerOptions,
    selectProvider,
    save,
    normalizedForComparison
  }
}
