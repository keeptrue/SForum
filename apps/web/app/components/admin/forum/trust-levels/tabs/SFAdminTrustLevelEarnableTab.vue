<script setup lang="ts">
/**
 * 后台「信任等级 · 能力解锁」分区（spec §5）：矩阵整块 + 功能关闭兜底。
 *
 * 只做两件事：包一层面板根（`data-sf-trust-levels-earnable`）与 feature-off 锁定块。
 * 矩阵的既有语义**逐条继承、不重写**：累积解锁（勾 TL2 = TL2 及以上）、失败 ≠ 空
 * （failed 时整块锁定 + 错误条，绝不画空矩阵）、键 → 人话复用 `admin.permissionCatalog.*`。
 *
 * 本面板**不渲染**「恢复推荐默认」（推荐默认的 earnable 是 ∅，放在这里等于一键清空运营策略），
 * 也**不渲染**保存/重置（全部归页级唯一 footer）。`canManage === false` 时矩阵自带的
 * `data-sf-earnable-readonly` 兜底仍在：D-B 之后本页整体对无 `trust.manage` 的账号渲染 forbidden，
 * 这条是第二道闸，防未来门控放宽时静默变成「能填不能存」。
 */
import SFAdminTrustLevelEarnableMatrix from '~/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableMatrix.vue'
import type {
  TrustLevelWhitelistState,
  TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'

defineProps<{
  form: TrustLevelsForm
  whitelist: TrustLevelWhitelistState
  canManage: boolean
  trustLevelsEnabled: boolean
}>()

const emit = defineEmits<{
  toggle: [tierId: string, key: string]
  retry: []
}>()

const { t } = useI18n()
</script>

<template>
  <div class="grid min-w-0 gap-6" data-sf-trust-levels-earnable>
    <!--
      开关关闭时**不读**白名单（`shouldLoadWhitelist` 在编排层短路）：`GET /admin/trust-levels`
      的 requireAdmin 先判开关，关闭时必然 404，发了只会多一条与「功能已关闭」重复的错误条。
      所以这里给一块与矩阵锁定块同尺寸的占位，而不是让矩阵停在 idle。
    -->
    <div v-if="!trustLevelsEnabled" class="sf-trust-levels-locked" data-sf-trust-levels-earnable-feature-off>
      <UIcon name="i-lucide-power-off" class="size-4" aria-hidden="true" />
      <span>{{ t('admin.forum.trustLevels.earnable.featureOff') }}</span>
    </div>

    <SFAdminTrustLevelEarnableMatrix
      v-else
      :form="form"
      :whitelist="whitelist"
      :can-manage="canManage"
      @toggle="(tierId: string, key: string) => emit('toggle', tierId, key)"
      @retry="emit('retry')"
    />
  </div>
</template>

<style scoped>
/* 与矩阵锁定块同尺寸（min-height:180px），切态不跳动；虚线框、无微光（视觉规范禁动效）。 */
.sf-trust-levels-locked {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 180px;
  padding: 16px;
  border: 1px dashed var(--sf-border);
  border-radius: 8px;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  text-align: center;
}
</style>
