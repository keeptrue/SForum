<script setup lang="ts">
import SFAdminFixedTabNav from '~/components/admin/settings/shared/SFAdminFixedTabNav.vue'
import SFAdminTrustLevelTierConfirmDialog from '~/components/admin/forum/trust-levels/SFAdminTrustLevelTierConfirmDialog.vue'
import SFAdminFormFooter from '~/components/admin/SFAdminFormFooter.vue'
import SFAdminTrustLevelEarnableTab from '~/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelEarnableTab.vue'
import SFAdminTrustLevelTiersTab from '~/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelTiersTab.vue'
import SFAdminTrustLevelThresholdsTab from '~/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelThresholdsTab.vue'
import SFAdminTrustLevelsOverviewTab from '~/components/admin/forum/trust-levels/tabs/SFAdminTrustLevelsOverviewTab.vue'
import { useAdminPage } from '~/composables/admin/useAdminPage'
import { useTrustLevelsAdminPage } from '~/composables/admin/forum/trustLevels/useTrustLevelsAdminPage'

/**
 * 信任等级独立页（原 `/control-panel/settings?tab=trustLevels`）。
 *
 * 本文件只做 route shell：页头、工具栏、tab 选择（与 `?tab=` 同步）、活动面板、五态判定、
 * 页级唯一吸底 footer，以及**停用档位的二次确认条**（D-N8：常驻 UAlert，不进 Toast）。
 * 表单状态、白名单四态、保存/重置/恢复默认、五态都在 `useTrustLevelsAdminPage()`；
 * 四个分区在 `components/admin/forum/trust-levels/tabs/**`。
 *
 * 三处不可让渡：
 * 1. **footer 是唯一保存入口**：阈值、限额、能力解锁同在单个 JSON option `trust.levels` 里，
 *    一次保存 = 整键覆盖；三个分区各一个保存按钮 = 三个写者对同一键全量覆盖（D-C）。
 * 2. **forbidden 态不渲染任何表单像素**：没有 `trust.manage` 时载荷里没有 `trust.levels` 键，
 *    回填出来的推荐默认值就是假现值（D-B）。
 * 3. **面板不写 `role="tabpanel"`**：`SFAdminFixedTabNav` 的按钮没有 `id`/`aria-controls`，
 *    只给面板加 role 是半套 ARIA；与 settings / forum settings 两页保持一致。
 */
definePageMeta({
  middleware: 'admin',
  layout: 'admin'
})

defineOptions({
  name: 'AdminForumTrustLevels'
})

type TrustLevelsTab = 'overview' | 'tiers' | 'thresholds' | 'earnable'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const adminPage = useAdminPage('/forum/trust-levels')
const {
  pending,
  refresh,
  form,
  tiers,
  canManage,
  hasChanges,
  trustLevelsEnabled,
  saving,
  whitelistState,
  loadWhitelist,
  canAdd,
  canRetire,
  addTier,
  insertTierAt,
  moveTier,
  renameTier,
  setIcon,
  setColor,
  restoreTier,
  savedRetiredIds,
  retireRequest,
  requestRetire,
  confirmRetire,
  cancelRetire,
  deleteRequest,
  requestDelete,
  confirmDelete,
  cancelDelete,
  saveErrorBuckets,
  restoreConfirmOpen,
  confirmRestore,
  cancelRestore,
  restoring,
  structureChanged,
  structureConfirmOpen,
  confirmStructureSave,
  cancelStructureConfirm,
  setThreshold,
  toggleEarnable,
  save,
  resetChanges,
  restoreThresholds,
  recommended,
  usingFallbackRecommended,
  pageState,
  loadErrorReason,
  savedRequirements
} = useTrustLevelsAdminPage()

const tabIds: TrustLevelsTab[] = ['overview', 'tiers', 'thresholds', 'earnable']
/** 恢复默认会覆盖的五类内容（与文案一一对应，顺序即严重性）。 */
const restoreLossItems = ['tiers', 'thresholds', 'limits', 'earnable', 'appearance'] as const
const activeTab = ref<TrustLevelsTab>(normalizeTab(route.query.tab))

const tabIcons: Record<TrustLevelsTab, string> = {
  overview: 'i-lucide-list',
  tiers: 'i-lucide-layers',
  thresholds: 'i-lucide-gauge',
  earnable: 'i-lucide-key-round'
}
/** 卡头取键：分区名与说明来自 §15.1 的固定表，不跟 tab 标签混用（thresholds 的 h3 用 `title`）。 */
const panelTitleKey: Record<TrustLevelsTab, string> = {
  overview: 'admin.forum.trustLevels.overview.title',
  tiers: 'admin.forum.trustLevels.tiers.title',
  thresholds: 'admin.forum.trustLevels.title',
  earnable: 'admin.forum.trustLevels.earnable.title'
}
const panelDescriptionKey: Record<TrustLevelsTab, string> = {
  overview: 'admin.forum.trustLevels.sections.overview.description',
  tiers: 'admin.forum.trustLevels.sections.tiers.description',
  thresholds: 'admin.forum.trustLevels.sections.thresholds.description',
  earnable: 'admin.forum.trustLevels.sections.earnable.description'
}
const panelTitle = computed(() => t(panelTitleKey[activeTab.value]))
const panelDescription = computed(() => t(panelDescriptionKey[activeTab.value]))

const tabs = computed(() => tabIds.map(id => ({
  id,
  label: t(`admin.forum.trustLevels.tabs.${id}`),
  icon: tabIcons[id]
})))

/** 只有 ready 与 feature-off 两态有卡片与 footer；loading 只有占位，failed / forbidden 什么都没有。 */
const showCard = computed(() => pageState.value === 'ready' || pageState.value === 'feature-off' || pageState.value === 'loading')
const showFooter = computed(() => pageState.value === 'ready' || pageState.value === 'feature-off')
/** forbidden 态不渲染 tablist：没有权限时连分区都不该出现。 */
const showTabs = computed(() => pageState.value !== 'forbidden')

watch(() => route.query.tab, value => {
  const normalized = normalizeTab(value)
  if (normalized !== activeTab.value) activeTab.value = normalized
})

watch(activeTab, async tab => {
  if (route.query.tab === tab) return
  await router.replace({ query: { ...route.query, tab } })
})

useSeoMeta({
  title: t('admin.forum.trustLevels.metaTitle')
})

function normalizeTab(value: unknown): TrustLevelsTab {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' && tabIds.includes(raw as TrustLevelsTab) ? raw as TrustLevelsTab : 'overview'
}

function setActiveTab(value: string) {
  activeTab.value = normalizeTab(value)
}

</script>

<template>
  <div class="w-full min-w-0" data-sf-trust-levels-page :data-sf-page-state="pageState">
    <div class="mb-4 flex flex-col gap-1">
      <h2 class="flex items-center gap-2 text-xl font-bold text-slate-900 dark:text-zinc-100">
        <UIcon :name="adminPage.icon" class="size-5 text-[var(--sf-accent)] dark:text-[var(--sf-accent-dark)]" />
        {{ t('admin.forum.trustLevels.page.title') }}
      </h2>
    </div>

    <div data-sf-trust-levels-toolbar>
      <!--
        窄屏（390）必须让说明可收缩、命令不被挤出视口：左槽 `min-w-0 flex-1` 让 `truncate` 生效，
        右槽 `shrink-0` 保住命令。`UDashboardToolbar` 根节点自带 `overflow-x-auto`，
        左槽不收缩时内层就会出现横向滚动、刷新按钮被推到视口外（AGENTS.md：命令可折叠但必须可达）。
      -->
      <UDashboardToolbar
        class="mb-6 rounded-lg border border-slate-200 bg-white px-4 py-2.5 text-slate-500 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-400"
        :ui="{ left: 'min-w-0 flex-1', right: 'shrink-0' }"
      >
        <template #left>
          <div class="flex min-w-0 items-center gap-2 text-sm">
            <UIcon name="i-lucide-gauge" class="size-4 shrink-0" />
            <span class="truncate">{{ t('admin.forum.trustLevels.page.description') }}</span>
          </div>
        </template>
        <template #right>
          <UButton
            color="neutral"
            variant="outline"
            leading-icon="i-lucide-refresh-cw"
            :loading="pending"
            :aria-label="t('admin.common.refresh')"
            :title="t('admin.common.refresh')"
            data-sf-trust-levels-refresh
            @click="refresh()"
          >
            {{ t('admin.common.refresh') }}
          </UButton>
        </template>
      </UDashboardToolbar>
    </div>

    <div class="flex w-full min-w-0 flex-col gap-4">
      <!--
        P2：保存被拒时，归不到具体档位的错误（全局字段 / JSON 语法错 / 档位 id 已不存在）
        渲染成页面级错误块。有明细就逐条列，没有就完全不出现在这里 —— 统一码由 Toast 承担，
        这里绝不渲染空白的「配置无效」条。错误条不自动关闭。
      -->
      <UAlert
        v-if="saveErrorBuckets.pageLevel.length > 0"
        color="error"
        variant="soft"
        icon="i-lucide-circle-alert"
        :title="t('admin.forum.trustLevels.errors.pageTitle')"
        data-sf-trust-levels-save-errors
      >
        <template #description>
          <ul class="mt-1 grid gap-1">
            <li v-for="(error, index) in saveErrorBuckets.pageLevel" :key="`${error.field}-${index}`">
              <span class="font-semibold">{{ error.field || t('admin.forum.trustLevels.errors.field.unknown') }}</span>
              <span>：{{ error.reason || t('admin.forum.trustLevels.errors.generic') }}</span>
            </li>
          </ul>
        </template>
      </UAlert>

      <!-- failed：错误条不自动关闭（AGENTS.md），且必须给「重试」这一条出路。 -->
      <UAlert
        v-if="pageState === 'failed'"
        color="error"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :title="t('admin.forum.trustLevels.loadFailed')"
        :description="loadErrorReason
          ? t('admin.forum.trustLevels.page.loadFailedReason', { reason: loadErrorReason })
          : t('admin.forum.trustLevels.page.loadFailedDescription')"
        data-sf-trust-levels-load-error
      >
        <template #actions>
          <UButton
            size="xs"
            color="neutral"
            variant="outline"
            type="button"
            leading-icon="i-lucide-refresh-cw"
            data-sf-trust-levels-retry
            @click="refresh()"
          >
            {{ t('admin.forum.trustLevels.page.retry') }}
          </UButton>
        </template>
      </UAlert>

      <!-- forbidden：没有 trust.manage 时载荷缺 trust.levels 键，渲染出来就是假现值，一个表单像素都不给。 -->
      <UAlert
        v-if="pageState === 'forbidden'"
        color="neutral"
        variant="soft"
        icon="i-lucide-lock"
        :title="t('admin.forum.trustLevels.page.forbidden.title')"
        :description="t('admin.forum.trustLevels.page.forbidden.description')"
        data-sf-trust-levels-forbidden
      />

      <!--
        feature-off：全页只允许这一条**同类警告**（同因同屏两条同名警告会让运营以为指向别的问题）。
        注意这不是「整页只准有一条 alert」：软停用态仍保留下方 :277 的「推荐配置」info 条、
        四个 tab 与表单（见 showCard / showFooter 的 feature-off 分支）。删掉那条 info 条，
        运营就失去「为什么停用 / 停用后该怎么配」的上下文 —— 它不是冗余。
      -->
      <UAlert
        v-if="pageState === 'feature-off'"
        color="warning"
        variant="soft"
        icon="i-lucide-power-off"
        :title="t('admin.forum.trustLevels.disabled.title')"
        :description="t('admin.forum.trustLevels.disabled.description')"
        data-sf-trust-levels-disabled
      />

      <!--
        推荐配置：与「论坛设置」页同一条 alert 同位同款（工具栏 → 状态条 → tabs）。
        `forbidden` 态不渲染：无权限时页面上只剩一句「看不到」，再推荐一套配置是自相矛盾的噪声。
      -->
      <UAlert
        v-if="pageState === 'ready' || pageState === 'feature-off'"
        color="primary"
        variant="soft"
        icon="i-lucide-sparkles"
        :title="t('admin.forum.trustLevels.recommended.title')"
        :description="t('admin.forum.trustLevels.recommended.description')"
        data-sf-trust-levels-recommended
      />

      <!--
        D-N8：停用是软停用，会撤回该档贡献的 earnable（可能降权）。
        确认从「页面顶部的常驻条」改成**阻塞式 modal**：列表第 6 档按下停用时，
        顶部那条常驻条早已滚出视口，等于没有披露；modal 保证运营在确认那一刻读到后果。
        Toast 只报保存结果，不承担披露。
      -->
      <SFAdminTrustLevelTierConfirmDialog
        mode="retire"
        :request="retireRequest"
        :open="Boolean(retireRequest)"
        @confirm="confirmRetire"
        @cancel="cancelRetire"
      />

      <!--
        D-C2：删除不可恢复，必须阻塞式确认（红按钮），且只在「已保存为停用」的档位上可用。
        modal 文案明确「不占位次、不改任何人的等级」与「id 记入墓碑、永不复用」。
      -->
      <SFAdminTrustLevelTierConfirmDialog
        mode="delete"
        :request="deleteRequest"
        :open="Boolean(deleteRequest)"
        @confirm="confirmDelete"
        @cancel="cancelDelete"
      />

      <!--
        恢复默认是服务端动作（POST restore-defaults），会覆盖整份配置并立即影响全站等级判定，
        所以先确认再发请求。它**不**叠加「档位结构变化」那条弹窗：那是普通保存的闸门，
        恢复默认本身就是显式动作，再叠一层只会让人闭眼点确定。
      -->
      <UModal
        v-model:open="restoreConfirmOpen"
        :ui="{ content: 'sm:max-w-lg' }"
        data-sf-trust-levels-restore-confirm
        @update:open="(value: boolean) => { if (!value) cancelRestore() }"
      >
        <template #content>
          <!-- 自建内容根：补 `role="dialog"` + `aria-modal="true"`，否则屏幕阅读器只看到一堆 aria-hidden 的背景。 -->
          <div
            class="p-5 sm:p-6"
            role="dialog"
            aria-modal="true"
            aria-labelledby="sf-trust-levels-restore-title"
            data-sf-trust-levels-restore-dialog
          >
            <div class="flex items-start gap-3">
              <div class="flex size-9 shrink-0 items-center justify-center rounded-md bg-amber-50 text-amber-600 dark:bg-amber-950/40 dark:text-amber-300">
                <UIcon name="i-lucide-rotate-ccw" class="size-4" aria-hidden="true" />
              </div>
              <div class="min-w-0">
                <h3 id="sf-trust-levels-restore-title" class="text-base font-bold text-slate-900 dark:text-white">
                  {{ t('admin.forum.trustLevels.restoreConfirm.title') }}
                </h3>
                <p class="mt-1 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                  {{ t('admin.forum.trustLevels.restoreConfirm.description') }}
                </p>
                <!--
                  逐项列出会丢的东西：只说「整份配置」时，手工配了半天 earnable 的运营并不知道
                  自己的解锁会被清空。五类逐条可读，不用折行糊成一段。
                -->
                <ul class="mt-3 grid list-disc gap-1 pl-5 text-sm leading-6 text-slate-600 dark:text-zinc-300" data-sf-trust-levels-restore-items>
                  <li v-for="item in restoreLossItems" :key="item">
                    {{ t(`admin.forum.trustLevels.restoreConfirm.items.${item}`) }}
                  </li>
                </ul>
              </div>
            </div>
            <div class="mt-5 flex flex-wrap justify-end gap-2">
              <UButton
                type="button"
                color="neutral"
                variant="outline"
                data-sf-trust-levels-restore-cancel
                @click="cancelRestore"
              >
                {{ t('admin.forum.trustLevels.restoreConfirm.cancel') }}
              </UButton>
              <UButton
                type="button"
                color="warning"
                variant="solid"
                leading-icon="i-lucide-rotate-ccw"
                :loading="restoring"
                data-sf-trust-levels-restore-accept
                @click="confirmRestore"
              >
                {{ t('admin.forum.trustLevels.restoreConfirm.confirm') }}
              </UButton>
            </div>
          </div>
        </template>
      </UModal>

      <!--
        P1-4：档位结构变化（新增/停用/恢复/插序/上移下移）会让所有人按新阶梯重新判定，
        所以保存前必须阻塞式确认。只改阈值/名字/图标/能力解锁不弹 —— 那些不改变谁落在哪一档。
        Toast 不能代替这条披露：它是「保存后会发生什么」的唯一告知点。
      -->
      <UModal
        v-model:open="structureConfirmOpen"
        :ui="{ content: 'sm:max-w-lg' }"
        data-sf-trust-levels-structure-confirm
        @update:open="(value: boolean) => { if (!value) cancelStructureConfirm() }"
      >
        <template #content>
          <div
            class="p-5 sm:p-6"
            role="dialog"
            aria-modal="true"
            aria-labelledby="sf-trust-levels-structure-title"
            data-sf-trust-levels-structure-dialog
          >
            <div class="flex items-start gap-3">
              <div class="flex size-9 shrink-0 items-center justify-center rounded-md bg-amber-50 text-amber-600 dark:bg-amber-950/40 dark:text-amber-300">
                <UIcon name="i-lucide-layers" class="size-4" aria-hidden="true" />
              </div>
              <div class="min-w-0">
                <h3 id="sf-trust-levels-structure-title" class="text-base font-bold text-slate-900 dark:text-white">
                  {{ t('admin.forum.trustLevels.structureConfirm.title') }}
                </h3>
                <p class="mt-1 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                  {{ t('admin.forum.trustLevels.structureConfirm.reevaluate') }}
                </p>
                <p class="mt-2 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                  {{ t('admin.forum.trustLevels.structureConfirm.noMigration') }}
                </p>
              </div>
            </div>
            <div class="mt-5 flex flex-wrap justify-end gap-2">
              <UButton
                type="button"
                color="neutral"
                variant="outline"
                data-sf-trust-levels-structure-cancel
                @click="cancelStructureConfirm"
              >
                {{ t('admin.forum.trustLevels.structureConfirm.cancel') }}
              </UButton>
              <UButton
                type="button"
                color="warning"
                variant="solid"
                leading-icon="i-lucide-save"
                data-sf-trust-levels-structure-accept
                @click="confirmStructureSave"
              >
                {{ t('admin.forum.trustLevels.structureConfirm.confirm') }}
              </UButton>
            </div>
          </div>
        </template>
      </UModal>

      <div v-if="showTabs" data-sf-trust-levels-tabs>
        <SFAdminFixedTabNav
          :items="tabs"
          :model-value="activeTab"
          :ariaLabel="t('admin.forum.trustLevels.tabs.label')"
          @update:model-value="setActiveTab"
        />
      </div>

      <form v-if="showCard" class="flex flex-col" @submit.prevent="save">
        <UCard class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100" :ui="{ footer: 'sticky bottom-0 z-20 bg-white/95 dark:bg-zinc-900/95 backdrop-blur-sm border-t border-slate-200 dark:border-zinc-800 p-4 sm:px-6' }">
          <!--
            与 SFAdminForumTabFrame 同位同款：分区名 + 一句说明 + 键前缀徽章。
            卡头由 route shell 渲染（面板不画），所以它不构成第二个页头：页头三件套仍各一处。
            `#sf-trust-levels-panel-title` 是矩阵 `aria-labelledby` 的锚点，id 值冻结。
          -->
          <template #header>
            <div class="flex items-center justify-between gap-3" data-sf-trust-levels-card-header>
              <div class="min-w-0">
                <h3 id="sf-trust-levels-panel-title" class="text-base font-bold">
                  {{ panelTitle }}
                </h3>
                <p class="mt-1 text-xs text-muted">{{ panelDescription }}</p>
              </div>
              <UBadge color="neutral" variant="soft" class="font-mono">trust.levels</UBadge>
            </div>
          </template>

          <div class="grid max-w-5xl gap-6" data-sf-trust-levels-panel :data-tab="activeTab">
            <!-- loading：虚线占位（无微光），尺寸与矩阵锁定块一致，切态不跳动。 -->
            <div
              v-if="pageState === 'loading'"
              class="sf-trust-levels-loading"
              data-sf-trust-levels-loading
              aria-busy="true"
            >
              <span>{{ t('admin.forum.trustLevels.page.loading') }}</span>
            </div>

            <!--
              表单状态在 `useTrustLevelsAdminPage()` 里，切 tab 本就不丢改动；
              KeepAlive 只是别让矩阵每次重算列宽。三个面板互斥渲染，任何时刻只有一个活动面板。
            -->
            <KeepAlive v-else>
              <SFAdminTrustLevelsOverviewTab
                v-if="activeTab === 'overview'"
                key="overview"
                :form="form"
                :saved-requirements="savedRequirements"
                :recommended="recommended"
                :trust-levels-enabled="trustLevelsEnabled"
                @jump="setActiveTab"
              />
              <SFAdminTrustLevelTiersTab
                v-else-if="activeTab === 'tiers'"
                key="tiers"
                :tiers="tiers"
                :can-manage="canManage"
                :can-add="canAdd"
                :can-retire="canRetire"
                :name-pool="whitelistState.namePool"
                :icon-choices="whitelistState.iconChoices"
                :color-choices="whitelistState.colorChoices"
                :saved-retired-ids="savedRetiredIds"
                :deleted-tier-ids="form.deletedTierIds ?? []"
                :save-errors="saveErrorBuckets.byTierId"
                @add="addTier"
                @insert="insertTierAt"
                @move="moveTier"
                @rename="renameTier"
                @set-icon="setIcon"
                @set-color="setColor"
                @retire="requestRetire"
                @restore="restoreTier"
                @delete="requestDelete"
              />
              <SFAdminTrustLevelThresholdsTab
                v-else-if="activeTab === 'thresholds'"
                key="thresholds"
                :form="form"
                :can-manage="canManage"
                :using-fallback-recommended="usingFallbackRecommended"
                :save-errors="saveErrorBuckets.byTierId"
                :restoring="restoring"
                @threshold="setThreshold"
                @restore="restoreThresholds"
              />
              <SFAdminTrustLevelEarnableTab
                v-else
                key="earnable"
                :form="form"
                :whitelist="whitelistState"
                :can-manage="canManage"
                :trust-levels-enabled="trustLevelsEnabled"
                @toggle="toggleEarnable"
                @retry="loadWhitelist"
              />
            </KeepAlive>
          </div>

          <template v-if="showFooter" #footer>
            <!--
              `#left` / `#actions` 两个槽都由本页提供，不用共享 footer 的默认按钮：
              `data-sf-trust-levels-save` 必须落在保存按钮本身，而共享组件不该为单个页面长一个钩子属性。
              按钮类串与 `SFAdminFormFooter` 的默认实现逐字一致，几何不分叉。
            -->
            <div class="w-full" data-sf-trust-levels-footer>
              <SFAdminFormFooter>
                <template #left>
                  <div v-if="hasChanges" class="flex flex-wrap items-center gap-2" data-sf-trust-levels-unsaved>
                    <UIcon name="i-lucide-circle-alert" class="size-4 text-amber-500" />
                    <span class="font-medium text-amber-600 dark:text-amber-400">
                      {{ t('admin.forum.trustLevels.page.unsaved') }}
                    </span>
                    <span class="text-xs text-slate-500 dark:text-zinc-400">
                      {{ t('admin.forum.trustLevels.saveScope') }}
                    </span>
                  </div>
                </template>
                <template #actions>
                  <UButton
                    type="button"
                    color="neutral"
                    variant="outline"
                    leading-icon="i-lucide-rotate-ccw"
                    :disabled="!canManage || saving"
                    class="border-slate-200 font-medium dark:border-zinc-700"
                    data-sf-trust-levels-reset
                    @click="resetChanges"
                  >
                    {{ t('admin.forum.trustLevels.reset') }}
                  </UButton>
                  <UButton
                    type="submit"
                    leading-icon="i-lucide-save"
                    :loading="saving"
                    :disabled="!canManage || !hasChanges"
                    class="bg-[var(--sf-accent)] font-semibold text-white hover:bg-[var(--sf-accent-hover)]"
                    data-sf-trust-levels-save
                  >
                    {{ t('admin.common.save') }}
                  </UButton>
                </template>
              </SFAdminFormFooter>
            </div>
          </template>
        </UCard>
      </form>
    </div>
  </div>
</template>

<style scoped>
/* 与矩阵锁定块同尺寸（min-height:180px）：切态不跳动；虚线框、无骨架微光（视觉规范禁动效）。 */
.sf-trust-levels-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 180px;
  padding: 16px;
  border: 1px dashed var(--sf-border);
  border-radius: 8px;
  color: var(--sf-fg-tertiary);
  font-size: 12px;
}
</style>
