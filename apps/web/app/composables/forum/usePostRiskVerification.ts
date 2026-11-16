import { computed } from 'vue'
import { createPostRiskVerification } from '~/utils/forum/postRiskVerification'

/**
 * 发帖 / 回帖的人机验证（post_risk 场景）控制器。
 *
 * 形态与登录侧 `SFLoginFormPage.vue` 完全一致：同一个 challenge 端点（只换 purpose）、
 * 同一组 `altchaWidgetSettings`、同一套 `@verified` / `@statechange` / `reset` 处理。
 * 三个发帖/回帖表面（发帖页、快捷回复、高级回复抽屉）共用这一个 composable，
 * 不再各自复制一份 token 状态。
 */
export function usePostRiskVerification() {
  const { t, locale } = useI18n()
  const { apiBaseUrl } = useApiClient()
  const { humanVerificationEnabledFor, altchaWidgetSettings } = useWebOptions()

  return createPostRiskVerification({
    // 运营开关是响应式的：后台改完选项后无需刷新页面即可生效。
    scenarioEnabled: computed(() => humanVerificationEnabledFor('post_risk')),
    widgetSettings: altchaWidgetSettings,
    apiBaseUrl,
    locale,
    translate: key => t(key)
  })
}
