<script setup lang="ts">
/**
 * 徽章详情弹窗：点卡片后看「这枚徽章到底是什么」。
 *
 * 信息块本身（图标/名称/等级/分组/说明/条件/进度/获得时间）在 `SFBadgeDetailBody.vue`，
 * 与站内庆祝弹窗**同源**；本文件只负责弹窗外壳：UModal、关闭按钮、底部的佩戴/关闭。
 *
 * 纯展示：不取数、不发请求、不改佩戴态。佩戴动作只 `emit('toggle-wear')`，
 * 由 `SFProfileBadgeTab` 复用它的 `toggleWear`（乐观更新 + toast 都在那边，只有一份）。
 */
import SFBadgeDetailBody from '~/components/forum/badges/SFBadgeDetailBody.vue'
import type { ProfileBadgeEntry } from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  /** 弹窗开关（父级用 `v-model:open` 绑定；本组件只回发，不自己决定开关）。 */
  open: boolean
  badge: ProfileBadgeEntry | null
  /** 分组名由容器给（wire 上的条目不带分组，避免同一段文案重复下发）。 */
  groupName?: string
  /** 徽章上下文：`catalog` 是公共目录（不佩戴），`profile` 是资料页。 */
  variant?: 'profile' | 'catalog'
  /** 本人视角才出现佩戴控件；他人页与目录页只读。 */
  self?: boolean
  /** 当前是否佩戴中（佩戴态的唯一来源是 `useBadgeWorn`，这里只回显）。 */
  worn?: boolean
}>(), {
  groupName: '',
  variant: 'profile',
  self: false,
  worn: false
})

const emit = defineEmits<{
  'update:open': [value: boolean]
  'toggle-wear': []
}>()

const { t } = useI18n()

const titleId = 'sf-badge-detail-title'
const isCatalog = computed(() => props.variant === 'catalog')
const canWear = computed(() => Boolean(props.self && !isCatalog.value && props.badge?.earned))

function close() {
  emit('update:open', false)
}
</script>

<template>
  <UModal
    :open="props.open"
    :title="props.badge?.name || t('badges.detail.title')"
    :ui="{ content: 'sf-badge-detail-modal' }"
    @update:open="emit('update:open', $event)"
  >
    <template #content>
      <section v-if="props.badge" class="sf-badge-detail" :aria-labelledby="titleId">
        <!-- 信息块与庆祝弹窗同源；这里固定用弹窗密度（`variant` 是徽章上下文，别混）。 -->
        <SFBadgeDetailBody
          :badge="props.badge"
          :group-name="props.groupName"
          :title-id="titleId"
          variant="modal"
        />

        <button
          type="button"
          class="sf-badge-detail__close"
          :aria-label="t('badges.detail.close')"
          :title="t('badges.detail.close')"
          @click="close"
        >
          <UIcon name="i-lucide-x" class="sf-badge-detail__close-glyph" aria-hidden="true" />
        </button>

        <footer class="sf-badge-detail__footer">
          <!-- 可见文字就是动作：已佩戴时写「取消佩戴」，不写「佩戴中」这种状态词。
               状态只由 aria-pressed 表达，按钮说状态就等于让读屏用户猜它会做什么。 -->
          <button
            v-if="canWear"
            type="button"
            class="sf-badge-detail__wear"
            :class="{ 'sf-badge-detail__wear--active': props.worn }"
            :aria-pressed="props.worn ? 'true' : 'false'"
            :title="props.worn ? t('badges.worn.unwear') : t('badges.worn.wear')"
            @click="emit('toggle-wear')"
          >
            {{ props.worn ? t('badges.worn.unwear') : t('badges.worn.wear') }}
          </button>
          <button type="button" class="sf-badge-detail__done" @click="close">
            {{ t('common.close') }}
          </button>
        </footer>
      </section>
    </template>
  </UModal>
</template>

<style scoped>
.sf-badge-detail {
  position: relative;
  display: block;
  box-sizing: border-box;
  padding: 20px;
  /* 关闭按钮压在右上角：给信息块头部预留内缩，长名字不会钻到按钮底下。 */
  --sf-badge-detail-head-inset: 36px;
}

.sf-badge-detail__close {
  position: absolute;
  top: 12px;
  right: 12px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  color: var(--sf-fg-tertiary);
}

.sf-badge-detail__close:hover {
  background: var(--sf-muted);
  color: var(--sf-fg);
}

.sf-badge-detail__close:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-badge-detail__close-glyph {
  width: 16px;
  height: 16px;
}

.sf-badge-detail__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}

.sf-badge-detail__wear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 14px;
  border: 1px solid var(--sf-border);
  border-radius: 999px;
  background: transparent;
  color: var(--sf-fg-secondary);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
}

.sf-badge-detail__wear:hover {
  border-color: var(--sf-accent);
  color: var(--sf-accent);
}

.sf-badge-detail__wear:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}

.sf-badge-detail__wear--active {
  border-color: var(--sf-accent);
  background: var(--sf-accent);
  color: var(--sf-public-surface);
}

.sf-badge-detail__wear--active:hover {
  color: var(--sf-public-surface);
  opacity: 0.9;
}

.sf-badge-detail__done {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 6px 14px;
  border: 1px solid var(--sf-border);
  border-radius: 999px;
  color: var(--sf-fg);
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
}

.sf-badge-detail__done:hover {
  border-color: var(--sf-accent);
  color: var(--sf-accent);
}

.sf-badge-detail__done:focus-visible {
  outline: 2px solid var(--sf-accent);
  outline-offset: 2px;
}
</style>
