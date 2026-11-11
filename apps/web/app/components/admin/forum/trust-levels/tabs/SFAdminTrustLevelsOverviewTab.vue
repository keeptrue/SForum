<script setup lang="ts">
/**
 * 后台「信任等级 · 等级总览」分区（spec §3）。
 *
 * 纯展示：**没有任何表单控件**，不持有表单状态、不渲染页头/toolbar/tablist/footer。
 * 数据全由 `useTrustLevelsAdminPage()` 注入；两个「去配置」按钮只发 `jump` 意图，
 * 由 route shell 走 `setActiveTab`（同时写 `?tab=`），所以这里不用 `NuxtLink` —— tab 是 query 状态
 * 而不是路由，链接化会产生一条可被新标签页打开、却没有页面态的快照。
 *
 * 缺省 tab 就是本面板（spec §1.2）：第一次进页面的人先要知道「这是什么、现在是什么状态、下一步去哪」，
 * 而不是迎面一张 4×6 数字表。
 */
import {
  TRUST_LEVEL_THRESHOLD_METRICS,
  type TrustLevelsForm
} from '~/composables/admin/forum/trustLevels/useTrustLevelsForm'
import { TRUST_LEVEL_TIER_MAX } from '~/composables/admin/forum/trustLevels/useTrustLevelsTiers'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import {
  activeTiers,
  trustLevelIconOf,
  trustLevelLadder,
  trustLevelName,
  type TrustLevelTier
} from '~/composables/profile/useTrustLevel'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { tierAccentStyle, tierColorOf } from '~/utils/profile/trustLevelColors'

const props = defineProps<{
  form: TrustLevelsForm
  /** **已保存**的 `requirements`，按档位 id 索引（不是正在编辑的 `form`）：徽章回答「站点现在用什么」。 */
  savedRequirements: Record<string, Record<string, number>>
  /** 推荐默认值（D-N9 的唯一权威：读面 `recommendedConfig`，读不到才回落本地兜底）。 */
  recommended: TrustLevelsForm
  trustLevelsEnabled: boolean
}>()

const emit = defineEmits<{
  jump: [tab: 'tiers' | 'thresholds' | 'earnable']
}>()

const { t, locale } = useI18n()
const { can } = useAuthSession()
const adminRoutes = useAdminRoutes()

/**
 * 配置状态只看 `requirements`：`limits` 本页不可编辑（D-D），`earnable` 是另一维配置。
 * 把 `earnable` 算进「是否推荐默认」，会让一个刚勾了一项能力解锁的站点显示「自定义门槛」，
 * 而门槛分毫未动（spec §3.3）。
 */
const configState = computed<'recommended' | 'custom'>(() => {
  const recommendedById = new Map(props.recommended.tiers.map(tier => [tier.id, tier.requirements]))
  // 档位集合本身与推荐值不同（多一档/少一档/改了 id）也算「自定义」：
  // 只比阈值会让「运营加了一档但没改任何阈值」显示成推荐默认。
  const savedIds = Object.keys(props.savedRequirements)
  const recommendedIds = props.recommended.tiers.filter(tier => !tier.retired).map(tier => tier.id)
  if (savedIds.length !== recommendedIds.length) return 'custom'
  for (const id of recommendedIds) {
    if (!(id in props.savedRequirements)) return 'custom'
    const recommendedRow = recommendedById.get(id) ?? {}
    for (const metric of TRUST_LEVEL_THRESHOLD_METRICS) {
      if ((props.savedRequirements[id]?.[metric.suffix] ?? 0) !== (recommendedRow[metric.suffix] ?? 0)) {
        return 'custom'
      }
    }
  }
  return 'recommended'
})

/** 展示阶梯：rank 0 = 虚拟 TL0（不在 tiers 数组里），其后是 active 档位。 */
const ladder = computed(() => trustLevelLadder(props.form.tiers))
const activeCount = computed(() => activeTiers(props.form.tiers).length)
const retiredCount = computed(() => props.form.tiers.length - activeCount.value)

const featuresPath = computed(() => adminRoutes.path('/settings/features'))

/** 档位名走 D-N3 的四层回退链：name[locale] → name[另一语言] → 内置 i18n → TL{rank}。 */
function levelLabel(level: number) {
  return trustLevelName(ladder.value[level] ?? null, locale.value, level, t)
}

function levelIcon(level: number) {
  return trustLevelIconOf(ladder.value[level] ?? null, level)
}

function tierAt(level: number): TrustLevelTier | undefined {
  return ladder.value[level]
}

/** 非 0 指标摘要：按 `TRUST_LEVEL_THRESHOLD_METRICS` 的声明顺序（= Go `Metrics` 顺序）过滤。 */
function thresholdItems(level: number) {
  const tier = tierAt(level)
  const row = (tier ? props.form.tiers.find(entry => entry.id === tier.id)?.requirements : undefined) ?? {}
  return TRUST_LEVEL_THRESHOLD_METRICS
    .map(metric => ({ metric, value: Number(row[metric.suffix] ?? 0) }))
    .filter(item => item.value > 0)
}

function thresholdSummary(level: number) {
  return thresholdItems(level)
    .map(item => t('admin.forum.trustLevels.overview.ladder.metricItem', {
      metric: t(`admin.forum.trustLevels.metric.${item.metric.i18nKey}`),
      value: item.value
    }))
    .join(t('admin.forum.trustLevels.overview.ladder.metricsSeparator'))
}

/** 已保存 `earnable` 的长度；`0` 时徽章不渲染（不写「无」）。 */
function earnableCount(level: number) {
  const tier = tierAt(level)
  if (!tier) return 0
  return props.form.tiers.find(entry => entry.id === tier.id)?.earnable.length ?? 0
}
</script>

<template>
  <!--
    分区名由卡片 header 的 `h3#sf-trust-levels-panel-title` 承担（与「论坛设置」页同构），
    面板自己**不再画第二个 h3**：同名标题出现两次，读屏的「跳转标题」会出现两条一样的条目
    （与 §15.1 第 5 条处理矩阵 `h3` 同一条理由）。`aria-labelledby` 直接指向卡头那个 id。
  -->
  <section class="grid min-w-0 gap-6" data-sf-trust-levels-overview aria-labelledby="sf-trust-levels-panel-title">
    <div class="grid min-w-0 gap-3" data-sf-trust-levels-state-cluster>
      <!-- 状态徽章第一行：功能开关状态 + 一行说明（状态不靠颜色单独承担）。 -->
      <div
        class="flex flex-wrap items-start justify-between gap-3"
        :data-sf-trust-levels-feature-state="trustLevelsEnabled ? 'on' : 'off'"
      >
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <UBadge :color="trustLevelsEnabled ? 'neutral' : 'warning'" variant="soft">
            {{ trustLevelsEnabled
              ? t('admin.forum.trustLevels.overview.feature.enabled')
              : t('admin.forum.trustLevels.overview.feature.disabled') }}
          </UBadge>
          <span class="text-xs text-muted">
            {{ trustLevelsEnabled
              ? t('admin.forum.trustLevels.overview.feature.enabledHint')
              : t('admin.forum.trustLevels.overview.feature.disabledHint') }}
          </span>
        </div>
        <!-- 入口权限是 settings.site.manage，与本页的 trust.manage 无关（spec §0.2）。 -->
        <UButton
          v-if="!trustLevelsEnabled && can('settings.site.manage')"
          size="xs"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-toggle-left"
          :to="featuresPath"
          data-sf-trust-levels-open-features
        >
          {{ t('admin.forum.trustLevels.overview.feature.openFeatures') }}
        </UButton>
      </div>

      <!-- 配置状态：取已保存值，正在编辑的改动由 footer 的未保存块承担。 -->
      <div class="flex flex-wrap items-start gap-2" :data-sf-trust-level-state="configState">
        <UBadge :color="configState === 'recommended' ? 'primary' : 'neutral'" variant="soft">
          {{ configState === 'recommended'
            ? t('admin.forum.trustLevels.overview.state.recommended')
            : t('admin.forum.trustLevels.overview.state.custom') }}
        </UBadge>
        <p class="min-w-0 text-xs text-muted">
          {{ configState === 'recommended'
            ? t('admin.forum.trustLevels.overview.state.recommendedHint')
            : t('admin.forum.trustLevels.overview.state.customHint') }}
        </p>
      </div>

      <!-- 三条定性说明（措辞已核，不许改写：spec §3.1）。 -->
      <ul class="m-0 list-none space-y-1 p-0 text-xs text-muted" data-sf-trust-levels-scope>
        <li class="flex items-start gap-1.5">
          <UIcon name="i-lucide-info" class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.scope.tl0') }}</span>
        </li>
        <li class="flex items-start gap-1.5">
          <UIcon name="i-lucide-info" class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.scope.levels') }}</span>
        </li>
        <li class="flex items-start gap-1.5">
          <UIcon name="i-lucide-info" class="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.scope.stack') }}</span>
        </li>
      </ul>
    </div>

    <div data-sf-trust-levels-ladder>
      <h4 class="text-sm font-semibold">{{ t('admin.forum.trustLevels.overview.ladder.title') }}</h4>
      <!--
        行式清单（§15.3）：与「论坛设置」的字段行同一视觉重量 —— 左图标 + 名称 + TL 编号，右侧指标摘要与解锁徽章。
        分隔只由容器上的 `divide-y` 承担，不再叠 border/margin（那是阈值行的分隔语言）。
        顺序语义由 ul/li 承担；档位图标 aria-hidden，TL 编号是文本不是图片替代。
      -->
      <ul class="m-0 mt-2 list-none divide-y divide-slate-200 p-0 dark:divide-zinc-800">
        <li
          v-for="(row, level) in ladder"
          :key="row.id"
          class="min-w-0 py-3 first:pt-0 last:pb-0"
          data-sf-tl-accent
          :style="tierAccentStyle(tierColorOf(row, level))"
          :data-level="String(level)"
          :data-metrics="String(thresholdItems(level).length)"
          :data-retired="row.retired ? 'true' : 'false'"
        >
          <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-2">
            <div class="flex min-w-0 items-center gap-2">
              <span class="sf-trust-levels-ladder__icon sf-tl-mark" :data-level="String(level)" :data-step="String(Math.min(4, level))" aria-hidden="true">
                <UIcon :name="levelIcon(level)" class="size-3.5" />
              </span>
              <strong class="text-sm font-semibold">{{ levelLabel(level) }}</strong>
              <span class="text-[11px] font-semibold tabular-nums text-muted">
                {{ t('profile.trustLevel.code', { level }) }}
              </span>
            </div>

            <!-- ≥640 与左组两端对齐；<640 整行换行、左对齐，摘要不截断、徽章跟在摘要后。 -->
            <div v-if="level > 0" class="flex w-full flex-wrap items-center gap-2 sm:w-auto">
              <span class="min-w-0 text-xs text-muted sf-trust-levels-overview__wrap">
                {{ thresholdItems(level).length > 0
                  ? thresholdSummary(level)
                  : t('admin.forum.trustLevels.overview.ladder.noThresholds') }}
              </span>
              <UBadge
                v-if="earnableCount(level) > 0"
                color="neutral"
                variant="soft"
                :data-sf-trust-levels-earnable-count="String(earnableCount(level))"
              >
                {{ t('admin.forum.trustLevels.overview.ladder.earnable', { count: earnableCount(level) }) }}
              </UBadge>
            </div>
          </div>

          <!-- TL0 没有门槛可摘要：判定权在「新人限制」，说明放在其它四行摘要的同一行位置。 -->
          <p v-if="level === 0" class="mt-1 min-w-0 text-xs text-muted sf-trust-levels-overview__wrap">
            {{ t('admin.forum.trustLevels.tl0Hint') }}
          </p>
        </li>
      </ul>
      <p class="mb-0 mt-2 text-xs text-muted" data-sf-trust-levels-tier-summary>
        {{ t('admin.forum.trustLevels.overview.tierSummary', {
          active: activeCount,
          retired: retiredCount,
          max: TRUST_LEVEL_TIER_MAX
        }) }}
      </p>
    </div>

    <div data-sf-trust-levels-guide>
      <h4 class="text-sm font-semibold">{{ t('admin.forum.trustLevels.overview.guide.title') }}</h4>
      <ol class="m-0 mt-2 grid list-none gap-3 p-0 sm:grid-cols-3 sm:gap-x-4">
        <li class="flex items-start gap-1.5 text-xs text-muted">
          <span class="font-semibold tabular-nums">1</span>
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.guide.step1') }}</span>
        </li>
        <li class="flex items-start gap-1.5 text-xs text-muted">
          <span class="font-semibold tabular-nums">2</span>
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.guide.step2') }}</span>
        </li>
        <li class="flex items-start gap-1.5 text-xs text-muted">
          <span class="font-semibold tabular-nums">3</span>
          <span class="min-w-0">{{ t('admin.forum.trustLevels.overview.guide.step3') }}</span>
        </li>
      </ol>
      <div class="mb-0 mt-4 flex flex-wrap justify-end gap-2" data-sf-trust-levels-jump>
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-layers"
          data-sf-trust-levels-jump-tiers
          @click="emit('jump', 'tiers')"
        >
          {{ t('admin.forum.trustLevels.overview.jump.tiers') }}
        </UButton>
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-gauge"
          data-sf-trust-levels-jump-thresholds
          @click="emit('jump', 'thresholds')"
        >
          {{ t('admin.forum.trustLevels.overview.jump.thresholds') }}
        </UButton>
        <UButton
          type="button"
          color="neutral"
          variant="outline"
          leading-icon="i-lucide-key-round"
          data-sf-trust-levels-jump-earnable
          @click="emit('jump', 'earnable')"
        >
          {{ t('admin.forum.trustLevels.overview.jump.earnable') }}
        </UButton>
      </div>
    </div>

    <p class="mb-0 border-t border-slate-200 pt-4 text-xs text-muted dark:border-zinc-800" data-sf-trust-levels-limits-note>
      {{ t('admin.forum.trustLevels.overview.limitsNote') }}
    </p>
  </section>
</template>

<style scoped>
/* 图标块几何走共享 .sf-tl-mark（assets/css/sforum-trust-colors.css）：这里只保留本分区的尺寸。
   色相来自 li 上的内联 --tl-accent —— 不要在 span 上再声明它，那会遮蔽祖先值（D-C6）。 */
.sf-trust-levels-ladder__icon {
  width: 20px;
  --sf-tl-mark-radius: 7px;

  height: 20px;
}

/* TL0 是虚拟档：虚线表达「未入门」，与档位色的实线/粗环区分开。 */
.sf-trust-levels-ladder__icon[data-level="0"] {
  border-style: dashed;
}

/* 长指标摘要不撑破 390 窄屏（spec §7.2 第 5 条）。 */
.sf-trust-levels-overview__wrap {
  overflow-wrap: anywhere;
}
</style>
