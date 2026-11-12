<script setup lang="ts">
import type { AdminBadgeGroupRow, AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import { toAdminBadgeInput, type AdminBadgeRow } from '~/utils/admin/adminBadges'
import { forumBadgeTier, pickTierName } from '~/utils/forum/forumBadge'
import { tierColorVar } from '~/utils/theme/tierColor'
import SFAdminBadgeIconField from '~/components/admin/badges/SFAdminBadgeIconField.vue'
import SFAdminBadgeRuleSection from '~/components/admin/badges/SFAdminBadgeRuleSection.vue'
import SFAdminBadgeRuleImpactDialog from '~/components/admin/badges/rule/SFAdminBadgeRuleImpactDialog.vue'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRuleError } from '~/utils/admin/badgeRule'
import type { BadgeRuleHealthIssue, BadgeRuleHealthRow } from '~/composables/admin/badges/useAdminBadgeRuleHealth'
import { useBadgeRuleDraft } from '~/composables/admin/badges/useBadgeRuleDraft'
import { useAdminBadgeRuleImpact } from '~/composables/admin/badges/useAdminBadgeRuleImpact'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'

/**
 * 新建 / 编辑徽章弹窗。
 *
 * 自己负责校验与提交（与用户创建弹窗同一分工），成功后 emit `saved` 让页面重拉列表。
 * 删除走两步确认，不嵌套第二个 Modal：第二次点击才真正发 DELETE。
 */
const props = withDefaults(defineProps<{
  groups: AdminBadgeGroupRow[]
  /** 服务端等级表：等级下拉的唯一来源（前端不再写死三档）。 */
  tiers: AdminBadgeTierRow[]
  badge: AdminBadgeRow | null
  /**
   * 条件目录。M1.4 的 `GET /admin/badges/conditions` 落地后由页面注入；
   * 未接线时为空数组，编辑器会显示「目录未加载」，不编造条件。
   */
  catalog?: BadgeConditionCatalogEntry[]
  /** 插件事实候选：只影响 fact.count 的两级下拉与失效提示。 */
  factCandidates?: BadgeFactCandidate[]
  factCandidatesFailed?: boolean
  /** 条件目录加载失败（端点未上线/网络错）：条件区给可见提示，不静默降级。 */
  catalogFailed?: boolean
  /** 规则健康清单：只含引用/条件失效的徽章，空数组表示全站健康（不渲染任何提示）。 */
  ruleHealth?: BadgeRuleHealthRow[]
  ruleHealthFailed?: boolean
}>(), {
  catalog: () => [],
  catalogFailed: false,
  factCandidates: () => [],
  factCandidatesFailed: false,
  ruleHealth: () => [],
  ruleHealthFailed: false
})

const emit = defineEmits<{ saved: [] }>()

const open = defineModel<boolean>('open', { required: true })

const { t, te, locale } = useI18n()
const { request } = useApiClient()
const toast = useToast()

const form = reactive({
  slug: '',
  name: '',
  description: '',
  groupId: null as number | null,
  tier: 'bronze' as string,
  icon: 'i-lucide-medal',
  grantMode: 'manual' as 'automatic' | 'manual',
  metric: 'topic_count',
  threshold: 1 as number | null,
  /** 公开说明双语（成对写回；读不到现值时整体不发，见 canWriteRequirementSummary）。 */
  requirementSummaryZh: '',
  requirementSummaryEn: '',
  enabled: true,
  position: 0
})

const submitting = ref(false)
const deleting = ref(false)
const confirmDelete = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})

const isEdit = computed(() => Boolean(props.badge?.id))
const title = computed(() => (isEdit.value ? t('admin.badges.form.editTitle') : t('admin.badges.form.createTitle')))
const automatic = computed(() => form.grantMode === 'automatic')

const groupItems = computed(() => [
  { value: null as number | null, label: '—' },
  ...props.groups.map(group => ({ value: group.id as number | null, label: group.name }))
])

/**
 * 等级下拉：只列**启用中**的等级，外加「当前值」例外。
 *
 * 例外是必须的：把一枚老徽章从停用档改到别的档，如果下拉里没有当前值，打开弹窗时
 * Select 会显示空 —— 运营会以为等级丢了，随手保存就把徽章挪到了别的档。
 */
const tierItems = computed(() => {
  const current = forumBadgeTier(props.badge?.tier)
  return props.tiers
    .filter(tier => tier.enabled || tier.key === current)
    .slice()
    .sort((left, right) => left.position - right.position)
    .map(tier => ({
      value: tier.key,
      label: pickTierName(tier.name, locale.value),
      // icon/color 只给下拉项与触发器用（见模板里的 #item-leading / #default），
      // 不参与提交——tier 的落库形状仍然只是一个 key。
      icon: tier.icon || 'i-lucide-award',
      color: tier.color
    }))
})

/** 下拉项与触发器共用的取色/取图标：slot 里的 item 类型由 Nuxt UI 泛型推导，这里收窄一次。 */
function tierItemIcon(item: unknown): string {
  return (item as { icon?: string } | null)?.icon || 'i-lucide-award'
}

function tierItemColor(item: unknown): string {
  return tierColorVar((item as { color?: string } | null)?.color ?? '')
}

/** 触发器里显示的当前等级（下拉关闭时也要带图标与颜色，不能只剩一行文字）。 */
const selectedTier = computed(() => props.tiers.find(tier => tier.key === form.tier) ?? null)

const grantModeItems = computed(() => (['automatic', 'manual'] as const).map(mode => ({
  value: mode,
  label: t(`admin.badges.grantMode.${mode}`)
})))

/**
 * 图标选中态要用的颜色 = 当前选中等级的识别色。
 *
 * 等级下拉一改，图标区的选中态跟着变色：运营在弹窗里就能看到「这枚徽章会是什么颜色」，
 * 不必保存后再回列表确认。等级没加载出来时回退 slate（`tierColorVar('')` 的兜底）。
 */
const iconAccent = computed(() => {
  const tier = props.tiers.find(item => item.key === form.tier)
  return tierColorVar(tier?.color ?? '')
})

/** 规则错误码 → 后台文案；未知码回退一句带码的通用提示，不显示裸 i18n key。 */
function ruleErrorText(error: BadgeRuleError) {
  const key = `admin.badges.rule.errors.${error.code.replace('rule.', '')}`
  return te(key) ? t(key, error.params ?? {}) : t('admin.badges.rule.errors.generic', { code: error.code })
}

/** 当前徽章的健康问题：只取本枚徽章的条目；空 = 健康，不渲染任何提示。 */
const healthIssues = computed<BadgeRuleHealthIssue[]>(() => {
  const id = props.badge?.id
  if (!id) return []
  return props.ruleHealth.find(row => row.badgeId === id)?.issues ?? []
})

/** 规则草稿、提交三态与保存前校验都收在 composable 里，弹窗只做渲染与提交。 */
const {
  draftRule,
  touched: ruleTouched,
  missing: ruleMissing,
  reset: resetRuleDraft,
  ensureRule,
  onUpdate: onRuleUpdate,
  payload: rulePayload,
  validate: validateRule
} = useBadgeRuleDraft({
  catalog: computed(() => props.catalog),
  automatic,
  badge: computed(() => props.badge),
  fieldErrors,
  errorText: ruleErrorText
})

const canSubmit = computed(() => {
  if (submitting.value || deleting.value) return false
  if (!form.slug.trim() || !form.name.trim()) return false
  return true
})

/**
 * 规则影响面（保存既有自动徽章前确认一次）。
 *
 * 端点估的是**库里已保存的规则**，所以只有 `isEdit && automatic` 才拉：新建徽章没有 id，
 * 草稿也还没有身份，端点无从估算 —— 那种情况不弹窗、不编数字，直接保存。
 */
const {
  status: ruleImpactStatus,
  impact: ruleImpact,
  load: loadRuleImpact,
  reset: resetRuleImpact
} = useAdminBadgeRuleImpact()
const impactOpen = ref(false)

/**
 * `requirementSummary` 是整体替换语义（服务端两语言一起落库）。
 * 只有「读到过现值」或「新建」才允许写回：字段缺失（旧后端）时读不到现值，
 * 这时发 `{zh:'',en:''}` 会把线上已有的公开文案抹掉，所以干脆不发。
 */
const canWriteRequirementSummary = computed(() => !props.badge || props.badge.requirementSummary !== undefined)

/** 公开说明的两语言：要么整对给，要么完全不给（不给 = 不改）。 */
function requirementSummaryPayload() {
  if (!canWriteRequirementSummary.value) return undefined
  return {
    zh: form.requirementSummaryZh.trim(),
    en: form.requirementSummaryEn.trim()
  }
}

watch(open, (value) => {
  if (!value) return
  errorMessage.value = ''
  fieldErrors.value = {}
  confirmDelete.value = false
  const badge = props.badge
  form.slug = badge?.slug ?? ''
  form.name = badge?.name ?? ''
  form.description = badge?.description ?? ''
  form.groupId = badge?.groupId ?? null
  form.tier = forumBadgeTier(badge?.tier)
  form.icon = badge?.icon || 'i-lucide-medal'
  form.grantMode = badge?.grantMode ?? 'manual'
  form.metric = badge?.metric ?? 'topic_count'
  form.threshold = badge?.threshold ?? 1
  form.requirementSummaryZh = badge?.requirementSummary?.zh ?? ''
  form.requirementSummaryEn = badge?.requirementSummary?.en ?? ''
  form.enabled = badge?.enabled ?? true
  form.position = badge?.position ?? 0
  resetRuleDraft()
  resetRuleImpact()
  impactOpen.value = false
})

// 从 manual 切到 automatic：没有规则就没有「自动授予」可言，补一枚默认规则。
// 打开既有 automatic 徽章不走这里 —— 服务端没规则就显示空树 + 提示，不替它编一条。
watch(() => form.grantMode, (mode) => {
  if (mode === 'automatic') ensureRule()
})

function close() {
  open.value = false
}

async function submit() {
  if (!canSubmit.value) return
  errorMessage.value = ''
  fieldErrors.value = {}

  // 保存前拦一道：后端仍会再校验（422 才是准的），这里只避免用户白等一次往返。
  if (!validateRule()) return

  // 仅「编辑既有自动徽章」才谈得上影响面：先取估算再弹确认，确认后才发 PATCH。
  // 新建徽章没有 id → 端点无法估算 → 不弹窗、不编假数字，直接保存。
  if (isEdit.value && automatic.value && props.badge?.id) {
    await loadRuleImpact(props.badge.id)
    impactOpen.value = true
    return
  }

  await persist()
}

async function confirmImpactSave() {
  impactOpen.value = false
  await persist()
}

/** 取消/关闭只是不发 PATCH：估算本身不改任何状态。 */
function cancelImpactSave() {
  impactOpen.value = false
  resetRuleImpact()
}

async function persist() {
  submitting.value = true
  try {
    const body = toAdminBadgeInput({
      ...form,
      tier: forumBadgeTier(form.tier),
      metric: automatic.value ? form.metric : null,
      threshold: automatic.value ? form.threshold : null,
      rule: rulePayload(),
      requirementSummary: requirementSummaryPayload()
    })
    if (isEdit.value && props.badge) {
      await request(`/admin/badges/${props.badge.id}`, { method: 'PATCH', body })
      toast.add({ title: t('admin.badges.toast.updated'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    } else {
      await request('/admin/badges', { method: 'POST', body })
      toast.add({ title: t('admin.badges.toast.created'), color: 'success', icon: 'i-lucide-check', duration: 5000 })
    }
    emit('saved')
    close()
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    if (Object.keys(fields).length === 0) {
      errorMessage.value = apiErrorMessage(error) || t('admin.badges.form.saveFailed')
    }
  } finally {
    submitting.value = false
  }
}

async function removeBadge() {
  if (!props.badge || deleting.value) return
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  deleting.value = true
  errorMessage.value = ''
  try {
    await request(`/admin/badges/${props.badge.id}`, { method: 'DELETE' })
    // 删除把 top_badge_id 置空（外键 SET NULL）且不会自动挑次高级：提示重算，
    // 否则用户帖子旁的徽章 chip 会消失到下一次评估/重算（失真窗口 #2）。
    toast.add({
      title: t('admin.badges.toast.deleted'),
      description: t('admin.badges.toast.deletedRecomputeHint'),
      color: 'success',
      icon: 'i-lucide-trash-2',
      duration: 5000
    })
    emit('saved')
    close()
  } catch (error) {
    errorMessage.value = apiErrorMessage(error) || t('admin.badges.toast.deleteFailed')
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :ui="{ content: 'sm:max-w-xl' }" @update:open="(value) => { if (!value) close() }">
    <template #content>
      <div class="flex max-h-[90vh] flex-col">
        <div class="border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ title }}</h3>
        </div>

        <div class="min-h-0 flex-1 space-y-3 overflow-y-auto px-5 py-4">
          <UFormField :label="t('admin.badges.form.slug')" :error="fieldErrors.slug?.[0]">
            <UInput v-model="form.slug" :disabled="isEdit" class="w-full" data-field="slug" />
            <template #hint>
              <span class="text-xs text-slate-500 dark:text-zinc-400">{{ t('admin.badges.form.slugHint') }}</span>
            </template>
          </UFormField>

          <UFormField :label="t('admin.badges.form.name')" :error="fieldErrors.name?.[0]">
            <UInput v-model="form.name" class="w-full" data-field="name" />
          </UFormField>

          <UFormField :label="t('admin.badges.form.description')" :error="fieldErrors.description?.[0]">
            <UTextarea v-model="form.description" :rows="2" class="w-full" />
          </UFormField>

          <div class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.form.group')" :error="fieldErrors.groupId?.[0]">
              <USelect v-model="form.groupId" :items="groupItems" class="w-full" />
            </UFormField>
            <UFormField :label="t('admin.badges.form.tier')" :error="fieldErrors.tier?.[0]">
              <USelect v-model="form.tier" :items="tierItems" class="w-full">
                <!-- 等级的身份是「图标 + 颜色 + 名字」：下拉里只给文字等于让人靠记性选档。 -->
                <template #default>
                  <span class="flex min-w-0 items-center gap-2">
                    <UIcon
                      v-if="selectedTier"
                      :name="tierItemIcon(selectedTier)"
                      class="size-4 shrink-0"
                      :style="{ color: tierItemColor(selectedTier) }"
                      aria-hidden="true"
                    />
                    <span class="truncate">{{ selectedTier ? pickTierName(selectedTier.name, locale) : form.tier }}</span>
                  </span>
                </template>
                <template #item-leading="{ item }">
                  <UIcon
                    :name="tierItemIcon(item)"
                    class="size-4 shrink-0"
                    :style="{ color: tierItemColor(item) }"
                    aria-hidden="true"
                  />
                </template>
              </USelect>
            </UFormField>
          </div>

          <SFAdminBadgeIconField
            v-model="form.icon"
            :label="t('admin.badges.form.icon')"
            :more-label="t('admin.badges.icon.more')"
            :less-label="t('admin.badges.icon.less')"
            :accent="iconAccent"
            :error="fieldErrors.icon?.[0]"
          />

          <div class="grid gap-3 sm:grid-cols-2">
            <UFormField :label="t('admin.badges.form.grantMode')" :error="fieldErrors.grantMode?.[0]">
              <USelect v-model="form.grantMode" :items="grantModeItems" class="w-full" data-field="grantMode" />
            </UFormField>
            <UFormField :label="t('admin.badges.form.enabled')">
              <USwitch v-model="form.enabled" :aria-label="t('admin.badges.form.enabled')" />
            </UFormField>
          </div>

          <SFAdminBadgeRuleSection
            v-if="automatic"
            :metric="form.metric"
            :threshold="form.threshold"
            :field-errors="fieldErrors"
            :catalog="catalog"
            :catalog-failed="catalogFailed"
            :fact-candidates="factCandidates"
            :fact-candidates-failed="factCandidatesFailed"
            :draft-rule="draftRule"
            :rule-missing="ruleMissing"
            :health-issues="healthIssues"
            :health-failed="ruleHealthFailed"
            v-model:requirement-summary-zh="form.requirementSummaryZh"
            v-model:requirement-summary-en="form.requirementSummaryEn"
            @update:rule="onRuleUpdate"
          />

          <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
          <p v-if="confirmDelete" class="text-sm text-red-600 dark:text-red-400">
            {{ t('admin.badges.confirmDeleteBody') }}
          </p>
          <!-- 两步确认第二步追加失真提示：删除清空 top_badge_id，chip 消失到重算为止。 -->
          <p v-if="confirmDelete" class="text-sm text-amber-600 dark:text-amber-400">
            {{ t('admin.badges.confirmDeleteTopBadgeHint') }}
          </p>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton
            v-if="isEdit"
            color="error"
            variant="ghost"
            icon="i-lucide-trash-2"
            :loading="deleting"
            @click="removeBadge"
          >
            {{ confirmDelete ? t('admin.badges.confirmDeleteTitle') : t('admin.badges.actions.delete') }}
          </UButton>
          <span v-else />

          <div class="flex items-center gap-2">
            <UButton color="neutral" variant="ghost" @click="close">{{ t('admin.badges.actions.cancel') }}</UButton>
            <UButton color="primary" :loading="submitting" :disabled="!canSubmit" @click="submit">
              {{ t('admin.badges.actions.save') }}
            </UButton>
          </div>
        </div>
      </div>
    </template>
  </UModal>

  <SFAdminBadgeRuleImpactDialog
    v-model:open="impactOpen"
    :impact="ruleImpact"
    :status="ruleImpactStatus"
    :rule-touched="ruleTouched"
    :catalog="catalog"
    @confirm="confirmImpactSave"
    @cancel="cancelImpactSave"
  />
</template>
