<script setup lang="ts">
/**
 * 档位「外观设置」对话框（小设 spec §1 / §5）。
 *
 * 为什么从行内展开面板改成 modal：
 * 1. 行内面板把图标（lucide + tabler 全量目录）塞进列表行，一展开就把相邻档位顶出视口；
 * 2. 外观是**单档的局部编辑**，但预览需要并排两套模式 —— 行宽在 390 上容不下；
 * 3. 一次只该有一个外观编辑目标，dialog 天然保证这一点（不会有两套焦点目标）。
 *
 * 三条边界：
 * - **只发意图**：本组件不持表单，图标/颜色各自 emit 一次；写入与保存闸门都在 `useTrustLevelsAdminPage`。
 * - **「完成」只关闭**：外观改动即时进表单，与其它字段一样由页面级 footer 统一保存。
 * - **footer 吸底、body 是唯一滚动区**：图标段自己分页/滚动，这里**不再**嵌一层固定高滚动容器
 *   （两层滚动条是这版最容易踩的坑）。
 */
import type { TrustLevelColorChoice } from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { defaultTierIconName, tierIconClass, trustLevelName } from '~/composables/profile/useTrustLevel'
import { defaultTierColorName, isTierCustomColor, normalizeTierColor } from '~/utils/profile/trustLevelColors'
import type { TierForm } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import SFAdminTrustLevelAppearancePreview from '~/components/admin/forum/trust-levels/SFAdminTrustLevelAppearancePreview.vue'
import SFAdminTrustLevelColorField from '~/components/admin/forum/trust-levels/SFAdminTrustLevelColorField.vue'

const props = withDefaults(defineProps<{
  open: boolean
  /** `null` = 没有编辑目标（弹窗关闭）。 */
  tier: TierForm | null
  /** 该档位次（停用档由调用方传数组位置）。决定默认图标/默认色与预览取色。 */
  rank: number
  canManage: boolean
  /** 服务端推荐色板；空 = 后端未下发（自定义色仍可用）。 */
  colorChoices?: TrustLevelColorChoice[]
}>(), {
  tier: null,
  colorChoices: () => []
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  setIcon: [id: string, icon: string | null]
  setColor: [id: string, color: string | null]
}>()

const { t, te, locale } = useI18n()

/**
 * `UModal` 的 v-model 桥：外部只管传 `open`，关闭统一走 `update:open(false)`，
 * 这样「按 Esc / 点遮罩 / 点完成」是同一条路径（不会出现弹窗关了但编辑目标还挂着）。
 */
const modalOpen = computed<boolean>({
  get: () => props.open,
  set: (value) => emit('update:open', value)
})

const titleId = 'sf-trust-level-appearance-title'
const name = computed(() => (props.tier ? trustLevelName(props.tier, locale.value, props.rank, t) : ''))
const defaultColorToken = computed(() => defaultTierColorName(props.rank))

/** 存储图标可能是 16 个默认短名（历史数据）：渲染与选择器都要 `i-lucide-*` 全名。 */
const iconValue = computed(() => (props.tier?.icon ? tierIconClass(props.tier.icon) : ''))
/** 预览必须始终有图标可画：未选图标时用该位次的默认图标。 */
const previewIcon = computed(() => iconValue.value || tierIconClass(defaultTierIconName(props.rank)))

const storedColor = computed(() => normalizeTierColor(props.tier?.color))

/** 预览卡里的色值文案：自定义色显示大写 hex，token 走 i18n 色名，`null` = 按位次自动。 */
const colorLabel = computed(() => {
  const color = storedColor.value
  if (!color) return t('admin.forum.trustLevels.colors.auto')
  if (isTierCustomColor(color)) return color.toUpperCase()
  const key = `admin.forum.trustLevels.colors.${color}`
  return te(key) ? t(key) : color
})
const colorAriaLabel = computed(() => t('admin.forum.trustLevels.tiers.color', { name: name.value }))

function selectIcon(value: string) {
  if (!props.tier || !props.canManage) return
  emit('setIcon', props.tier.id, value || null)
}

function resetIcon() {
  if (!props.tier || !props.canManage) return
  emit('setIcon', props.tier.id, null)
}

function selectColor(value: string | null) {
  if (!props.tier || !props.canManage) return
  emit('setColor', props.tier.id, value)
}

/** 恢复默认外观 = 图标与颜色一起回到「按位次自动」（两次意图，字段各自可撤销）。 */
function resetAppearance() {
  if (!props.tier || !props.canManage) return
  emit('setIcon', props.tier.id, null)
  emit('setColor', props.tier.id, null)
}

function close() {
  emit('update:open', false)
}
</script>

<template>
  <UModal v-model:open="modalOpen" :ui="{ content: 'sm:max-w-2xl overflow-hidden' }" data-sf-tl-appearance-modal>
    <template #content>
      <div
        v-if="tier"
        class="sf-tl-appearance"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        data-sf-tl-appearance-dialog
      >
        <header class="sf-tl-appearance__header">
          <h3 :id="titleId" class="sf-tl-appearance__title">
            {{ t('admin.forum.trustLevels.appearanceDialog.title') }}
          </h3>
          <p class="sf-tl-appearance__subtitle">
            {{ t('admin.forum.trustLevels.appearanceDialog.subtitle') }}
          </p>
        </header>

        <div class="sf-tl-appearance__body" data-sf-tl-appearance-body>
          <SFAdminTrustLevelAppearancePreview
            :value="storedColor"
            :rank="rank"
            :name="name"
            :icon="previewIcon"
            :color-label="colorLabel"
          />

          <section class="sf-tl-appearance__section">
            <LazySFIconPicker
              :model-value="iconValue"
              :label="t('admin.forum.trustLevels.appearanceDialog.iconTitle')"
              :hint="t('admin.forum.trustLevels.appearanceDialog.iconHint')"
              :show-custom-input="false"
              :disabled="!canManage"
              @update:model-value="selectIcon"
            />
            <UButton
              size="xs"
              color="neutral"
              variant="ghost"
              :disabled="!canManage || !iconValue"
              data-sf-appearance-icon-reset
              @click="resetIcon"
            >
              {{ t('admin.forum.trustLevels.appearanceDialog.iconReset') }}
            </UButton>
          </section>

          <SFAdminTrustLevelColorField
            :model-value="storedColor"
            :choices="colorChoices"
            :default-color="defaultColorToken"
            :label="colorAriaLabel"
            :disabled="!canManage"
            @update:model-value="selectColor"
          />
        </div>

        <footer class="sf-tl-appearance__footer">
          <UButton
            size="sm"
            color="neutral"
            variant="ghost"
            :disabled="!canManage"
            data-sf-appearance-reset
            @click="resetAppearance"
          >
            {{ t('admin.forum.trustLevels.appearanceDialog.footerReset') }}
          </UButton>
          <UButton size="sm" data-sf-appearance-done @click="close">
            {{ t('admin.forum.trustLevels.appearanceDialog.footerDone') }}
          </UButton>
        </footer>
      </div>
    </template>
  </UModal>
</template>

<style scoped>
/*
 * 三段式：header / body（唯一滚动区）/ footer（吸底）。
 * 高度用 `dvh` 而不是 `vh`：390 键盘弹起时 `dvh` 会收缩，footer 才不会被顶出可视区。
 */
.sf-tl-appearance {
  display: flex;
  flex-direction: column;
  max-height: 85vh;
  max-height: min(86dvh, 720px);
  min-height: 0;
}

@media (max-width: 639px) {
  .sf-tl-appearance {
    max-height: calc(100dvh - 48px);
  }
}

.sf-tl-appearance__header {
  flex: 0 0 auto;
  padding: 16px 18px 12px;
  border-bottom: 1px solid var(--sf-border);
}

.sf-tl-appearance__title {
  margin: 0;
  color: var(--sf-fg);
  font-size: 16px;
  font-weight: 700;
}

.sf-tl-appearance__subtitle {
  margin: 4px 0 0;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  line-height: 1.5;
}

.sf-tl-appearance__body {
  display: grid;
  flex: 1 1 auto;
  gap: 16px;
  min-height: 0;
  padding: 16px 18px 24px;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.sf-tl-appearance__section {
  display: grid;
  gap: 6px;
  min-width: 0;
}

.sf-tl-appearance__footer {
  position: sticky;
  bottom: 0;
  z-index: 1;
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 12px 18px;
  border-top: 1px solid var(--sf-border);
  background: var(--sf-card);
}

@media (max-width: 639px) {
  .sf-tl-appearance__header,
  .sf-tl-appearance__body,
  .sf-tl-appearance__footer {
    padding-right: 14px;
    padding-left: 14px;
  }
}
</style>
