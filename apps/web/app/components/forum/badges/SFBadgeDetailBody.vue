<script setup lang="ts">
/**
 * 徽章信息块：徽章详情弹窗与站内庆祝弹窗**共用的同一份**展示。
 *
 * 两个入口回答的是同一个问题「这枚徽章是什么」，所以信息项必须完全一致：
 * 等级色图标砖 → 名称 → 等级徽标 + 分组 → 徽章说明 → 完整获得条件 → 进度 / 获得时间。
 * 缺哪块就不渲染哪块（不画空标题、不留空段落）。
 *
 * **纯展示**：不取数、不发请求、不 emit 业务动作，也不持有加载态。
 * 徽章对象从宿主进来（详情弹窗拿卡片上的条目，庆祝弹窗拿徽章墙映射），
 * 佩戴/关闭这类动作留在各自宿主的底栏里 —— 否则两个弹窗会长出两套加载与失败状态。
 */
import {
  forumBadgeHasProgress,
  forumBadgeProgressPercent,
  forumBadgeTierIcon,
  forumBadgeTierLabel,
  forumBadgeTierStyle,
  type ProfileBadgeEntry
} from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  badge: ProfileBadgeEntry
  /** 分组名由宿主给（wire 上的条目不带分组，避免同一段文案重复下发）。 */
  groupName?: string
  /** 名称的 DOM id：弹窗用它做 `aria-labelledby`；庆祝弹窗不需要，留空即不渲染 id。 */
  titleId?: string
  /**
   * 密度档位：`modal` = 详情弹窗（大图标、宽松行距）；`celebration` = 庆祝弹窗里的紧凑块。
   * 只影响字号/间距，**信息项完全一致**。
   */
  variant?: 'modal' | 'celebration'
}>(), {
  groupName: '',
  titleId: '',
  variant: 'modal'
})

const { t, te, locale } = useI18n()
const { formatDateOnly } = useSiteDateTime()

const isCompact = computed(() => props.variant === 'celebration')
// 标题层级跟着宿主走，且**两级一起**走，不能跳级：
// - modal：徽章名 h2 → 字段标签 h3（弹窗里没有可见的 h1）
// - celebration：弹窗标题 h2 → 条目徽章名 h3 → 字段标签 h4
const headingTag = computed(() => (isCompact.value ? 'h3' : 'h2'))
const labelTag = computed(() => (isCompact.value ? 'h4' : 'h3'))

// 等级名/色都优先取载荷（运营可增删等级），缺失才回退内置三档文案与中性色。
const tierName = computed(() => forumBadgeTierLabel(props.badge, locale.value, t, te))
const tierStyle = computed(() => forumBadgeTierStyle(props.badge))

/** 完整获得条件：服务端已按语言回落，这里只选语言，不拼句子。 */
const requirement = computed(() => {
  const text = props.badge.requirementText
  if (!text) return ''
  const zhPreferred = String(locale.value || '').toLowerCase().startsWith('zh')
  const preferred = zhPreferred ? text.zh : text.en
  const fallback = zhPreferred ? text.en : text.zh
  return (preferred || fallback || '').trim()
})

const description = computed(() => String(props.badge.description ?? '').trim())
const hasProgress = computed(() => forumBadgeHasProgress(props.badge))
const progressPercent = computed(() => forumBadgeProgressPercent(props.badge.current, props.badge.threshold))
const progressLabel = computed(() => t('badges.detail.progress', {
  current: props.badge.current ?? 0,
  threshold: props.badge.threshold ?? 0,
  percent: progressPercent.value
}))
const earnedDate = computed(() => (props.badge.grantedAt ? formatDateOnly(props.badge.grantedAt) : ''))
</script>

<template>
  <div
    class="sf-badge-detail-body"
    :class="`sf-badge-detail-body--${props.variant}`"
    :data-badge-slug="props.badge.slug"
    :style="tierStyle"
  >
    <header class="sf-badge-detail-body__head">
      <span class="sf-badge-detail-body__icon" aria-hidden="true">
        <UIcon
          :name="props.badge.icon || forumBadgeTierIcon(props.badge.tier)"
          class="sf-badge-detail-body__glyph"
        />
      </span>

      <div class="sf-badge-detail-body__identity">
        <component :is="headingTag" :id="props.titleId || undefined" class="sf-badge-detail-body__name">
          {{ props.badge.name }}
        </component>
        <div class="sf-badge-detail-body__meta">
          <span class="sf-badge-detail-body__tier">{{ tierName }}</span>
          <span v-if="props.groupName" class="sf-badge-detail-body__group">{{ props.groupName }}</span>
        </div>
      </div>
    </header>

    <div class="sf-badge-detail-body__body">
      <section v-if="description" class="sf-badge-detail-body__section">
        <component :is="labelTag" class="sf-badge-detail-body__label">{{ t('badges.detail.descriptionLabel') }}</component>
        <p class="sf-badge-detail-body__text">{{ description }}</p>
      </section>

      <section v-if="requirement" class="sf-badge-detail-body__section">
        <component :is="labelTag" class="sf-badge-detail-body__label">{{ t('badges.detail.requirementLabel') }}</component>
        <p class="sf-badge-detail-body__text">{{ requirement }}</p>
      </section>

      <section v-if="hasProgress" class="sf-badge-detail-body__section">
        <component :is="labelTag" class="sf-badge-detail-body__label">{{ t('badges.detail.progressLabel') }}</component>
        <div class="sf-badge-detail-body__progress">
          <div
            class="sf-badge-detail-body__track"
            role="progressbar"
            aria-valuemin="0"
            :aria-valuenow="props.badge.current ?? 0"
            :aria-valuemax="props.badge.threshold ?? 0"
            :aria-valuetext="progressLabel"
          >
            <span class="sf-badge-detail-body__fill" :style="{ width: `${progressPercent}%` }" />
          </div>
          <span class="sf-badge-detail-body__progress-text">{{ progressLabel }}</span>
        </div>
      </section>

      <p v-if="earnedDate" class="sf-badge-detail-body__earned">
        {{ t('badges.card.earnedOn', { date: earnedDate }) }}
      </p>
    </div>
  </div>
</template>

<style scoped>
.sf-badge-detail-body {
  display: block;
  /* 与徽章卡同一套色变量：hue = 等级色，按主题挑明/暗那一份。 */
  --sf-tier-hue: var(--sf-tier-hue-light);
}

.dark .sf-badge-detail-body {
  --sf-tier-hue: var(--sf-tier-hue-dark);
}

/* 弹窗右上角的关闭按钮会压过来：由宿主用这个变量预留顶部右侧的内缩（庆祝弹窗不设 = 0）。 */
.sf-badge-detail-body__head {
  display: flex;
  align-items: flex-start;
  gap: 14px;
  padding-right: var(--sf-badge-detail-head-inset, 0);
}

.sf-badge-detail-body__icon {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 56px;
  border: 1px solid color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  border-radius: 16px;
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  color: var(--sf-tier-hue);
}

.sf-badge-detail-body__glyph {
  width: 30px;
  height: 30px;
}

.sf-badge-detail-body__identity {
  flex: 1 1 auto;
  min-width: 0;
}

.sf-badge-detail-body__name {
  margin: 0;
  color: var(--sf-fg);
  font-size: 17px;
  font-weight: 700;
  line-height: 1.3;
  overflow-wrap: anywhere;
}

.sf-badge-detail-body__meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 6px;
  font-size: 12px;
}

.sf-badge-detail-body__tier {
  padding: 2px 8px;
  border: 1px solid color-mix(in srgb, var(--sf-tier-hue) 34%, transparent);
  border-radius: 999px;
  background: color-mix(in srgb, var(--sf-tier-hue) 12%, transparent);
  color: var(--sf-tier-hue);
  font-weight: 600;
}

.sf-badge-detail-body__group {
  color: var(--sf-fg-tertiary);
}

.sf-badge-detail-body__body {
  display: grid;
  gap: 14px;
  margin-top: 18px;
}

.sf-badge-detail-body__section {
  display: grid;
  gap: 4px;
}

.sf-badge-detail-body__label {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.sf-badge-detail-body__text {
  margin: 0;
  color: var(--sf-fg-secondary);
  font-size: 13px;
  line-height: 1.55;
  overflow-wrap: anywhere;
}

.sf-badge-detail-body__progress {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 2px;
}

.sf-badge-detail-body__track {
  flex: 1 1 auto;
  height: 6px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--sf-muted);
}

.sf-badge-detail-body__fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--sf-accent);
}

.sf-badge-detail-body__progress-text {
  flex: 0 0 auto;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.sf-badge-detail-body__earned {
  margin: 0;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

/* 庆祝弹窗：同一批信息项，收紧字号与间距；不改变顺序、不隐藏任何一项。 */
.sf-badge-detail-body--celebration .sf-badge-detail-body__head {
  gap: 10px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__icon {
  width: 40px;
  height: 40px;
  border-radius: 12px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__glyph {
  width: 22px;
  height: 22px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__name {
  font-size: 14px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__meta {
  gap: 6px;
  margin-top: 4px;
  font-size: 11.5px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__body {
  gap: 10px;
  margin-top: 12px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__text {
  font-size: 12.5px;
  line-height: 1.5;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__track {
  height: 5px;
}

.sf-badge-detail-body--celebration .sf-badge-detail-body__progress-text,
.sf-badge-detail-body--celebration .sf-badge-detail-body__earned {
  font-size: 11.5px;
}
</style>
