import { apiErrorMessage } from '~/composables/useApiClient'
import type { AdminContributionPointDefinition, AdminEffectiveContribution } from '~/utils/admin/adminExtensions'
import { contributionCatalogViewState } from '~/utils/admin/adminContributions'

/**
 * 扩展点治理页的加载器：把「扩展点目录 + 生效贡献项」读成显式状态。
 *
 * 为什么不再复用 `useAdminExtensionsManager` 的 `loadContributionPoints/loadContributions`：
 * 那两个函数失败时 `catch → []`，页面只能看到「0 个扩展点」这种和「真的为空」
 * 一模一样的空态，运营没有任何恢复入口。这里区分四种状态，失败保留已有数据并
 * 暴露错误文案，由页面渲染可见错误 + 重试；错误不自动消失。
 */
export type ContributionCatalogStatus = 'idle' | 'loading' | 'ready' | 'error'

export function useAdminContributionCatalog() {
  const { request } = useApiClient()

  const points = ref<AdminContributionPointDefinition[]>([])
  const contributions = ref<AdminEffectiveContribution[]>([])
  const status = ref<ContributionCatalogStatus>('idle')
  const errorMessage = ref('')

  const loading = computed(() => status.value === 'loading')
  const failed = computed(() => status.value === 'error')

  async function load() {
    status.value = 'loading'
    errorMessage.value = ''
    try {
      const [pointList, contributionList] = await Promise.all([
        request<AdminContributionPointDefinition[]>('/admin/extensions/contribution-points'),
        request<AdminEffectiveContribution[]>('/admin/extensions/contributions')
      ])
      points.value = Array.isArray(pointList) ? pointList : []
      contributions.value = Array.isArray(contributionList) ? contributionList : []
      status.value = 'ready'
    } catch (error) {
      // 失败不清空上一次成功的数据：能看旧数据 + 明确报错，比白屏更有用。
      status.value = 'error'
      errorMessage.value = apiErrorMessage(error)
    }
  }

  const viewState = computed(() => contributionCatalogViewState(status.value, contributions.value.length))

  return {
    points,
    contributions,
    status,
    loading,
    failed,
    errorMessage,
    viewState,
    load,
    retry: load
  }
}
