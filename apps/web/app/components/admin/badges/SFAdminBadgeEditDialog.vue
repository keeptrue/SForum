<script setup lang="ts">
import type { AdminBadgeGroupRow, AdminBadgeTierRow } from '~/composables/admin/badges/useAdminBadges'
import { toAdminBadgeInput, type AdminBadgeRow } from '~/utils/admin/adminBadges'
import { forumBadgeTier, pickTierName } from '~/utils/forum/forumBadge'
import { tierColorVar } from '~/utils/theme/tierColor'
import SFAdminBadgeIconField from '~/components/admin/badges/SFAdminBadgeIconField.vue'
import SFAdminBadgeRuleSection from '~/components/admin/badges/SFAdminBadgeRuleSection.vue'
import SFAdminBadgeRuleImpactDialog from '~/components/admin/badges/rule/SFAdminBadgeRuleImpactDialog.vue'
import type { BadgeRefCandidateMap } from '~/utils/admin/badgeRefCandidates'
import type { BadgeConditionCatalogEntry, BadgeFactCandidate, BadgeRuleError } from '~/utils/admin/badgeRule'
import type { BadgeRuleHealthIssue, BadgeRuleHealthRow } from '~/composables/admin/badges/useAdminBadgeRuleHealth'
import { useBadgeRuleDraft } from '~/composables/admin/badges/useBadgeRuleDraft'
import { shouldLoadRuleImpact, useAdminBadgeRuleImpact } from '~/composables/admin/badges/useAdminBadgeRuleImpact'
import { useBadgeRuleTextPreview } from '~/composables/admin/badges/useBadgeRuleTextPreview'
import { apiErrorFields, apiErrorMessage } from '~/composables/useApiClient'
import { excludeBadgeSlug } from '~/utils/admin/badgeRefCandidates'
import { unmappedFieldMessages } from '~/utils/admin/badgeFieldSlots'

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
  /** objectRef 参数候选：编辑已有徽章时先剔掉它自己（自引用永不成立）。 */
  refCandidates?: BadgeRefCandidateMap
  /** 「响应有数组却缺目录已声明的 kind」= 真回归；规则区给可见诊断。 */
  refCandidatesMissingKinds?: string[]
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
  refCandidates: () => ({}),
  refCandidatesMissingKinds: () => [],
  ruleHealth: () => [],
  ruleHealthFailed: false
})

const emit = defineEmits<{ saved: [] }>()

const open = defineModel<boolean>('open', { required: true })

/**
 * 规则里不能引用**自身**徽章：自引用永不成立（授予链路本身就是在授予它）。
 * 过滤后 badge 类若为空，参数控件会退成文本输入——新建徽章时这就是正常状态。
 */
const ruleRefCandidates = computed(() => excludeBadgeSlug(props.refCandidates, props.badge?.slug ?? ''))

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
  enabled: true,
  position: 0
})

const submitting = ref(false)
const deleting = ref(false)
const confirmDelete = ref(false)
const errorMessage = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
// 展示位清单与模板里的 `fieldErrors.<key>` 一一对应（含 Section 的 `rule`）。

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
  reset: resetRuleImpact,
  markManual: markRuleImpactManual
} = useAdminBadgeRuleImpact()
const impactOpen = ref(false)

/**
 * 公开说明的**只读预览**：草稿规则一变就问服务端「这句话会怎么生成」。
 * 它不是字段、不进 `fieldErrors`、失败也不阻断保存（派生数据）。
 * 弹窗关闭时把规则置空 → 不发请求，也顺手清掉上一次的文案。
 */
const ruleTextPreview = useBadgeRuleTextPreview({
  rule: () => draftRule.value,
  enabled: () => open.value
})

watch(open, (value) => {
  if (!value) return
  // 换了一条徽章就别让上一位的公开说明先闪一下：清空后由草稿预览重新填。
  ruleTextPreview.setText(null)
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
    // 判据看**库里**这枚徽章的授予方式（`shouldLoadRuleImpact`），不是本次编辑后的
    // `form.grantMode`：「手动 → 自动」时库里仍是手动、`rule` 恒为 NULL，端点只能 422。
    // 已知没有规则就别问，直接给中性的「无法估算」。
    if (shouldLoadRuleImpact({
      isEdit: true,
      badgeId: props.badge.id,
      draftMode: form.grantMode,
      savedMode: props.badge.grantMode
    })) await loadRuleImpact(props.badge.id)
    else markRuleImpactManual()
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
      rule: rulePayload()
    })
    const saved = isEdit.value && props.badge
      ? await request<AdminBadgeRow>(`/admin/badges/${props.badge.id}`, { method: 'PATCH', body })
      : await request<AdminBadgeRow>('/admin/badges', { method: 'POST', body })
    // 落库结果优先：预览是草稿的近似，保存后以服务端返回的 requirementText 为准。
    // 旧后端没有这个字段（undefined）时保留当前预览，不把已显示的句子清空。
    if (saved?.requirementText !== undefined) ruleTextPreview.setText(saved.requirementText)
    toast.add({
      title: isEdit.value ? t('admin.badges.toast.updated') : t('admin.badges.toast.created'),
      color: 'success',
      icon: 'i-lucide-check',
      duration: 5000
    })
    emit('saved')
    close()
  } catch (error) {
    const fields = apiErrorFields(error)
    fieldErrors.value = fields
    const unmapped = unmappedFieldMessages(fields)
    if (unmapped.length > 0) {
      // 有落点的字段各自显示自己的错误；没有落点的合并到通用提示，一条都不许吞。
      errorMessage.value = unmapped.join(' · ')
    } else if (Object.keys(fields).length === 0) {
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
          <div>
            <UFormField :label="t('admin.badges.form.slug')" :error="fieldErrors.slug?.[0]">
              <UInput v-model="form.slug" :disabled="isEdit" class="w-full" data-field="slug" />
            </UFormField>
            <!-- 提示在控件下方单独一行：放进 label 行会把标题挤窄、两侧高度也不齐。 -->
            <p class="mt-1 text-xs text-slate-500 dark:text-zinc-400" data-field-hint data-field-hint-for="slug">
              {{ t('admin.badges.form.slugHint') }}
            </p>
          </div>

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
            :field-errors="fieldErrors"
            :catalog="catalog"
            :catalog-failed="catalogFailed"
            :fact-candidates="factCandidates"
            :fact-candidates-failed="factCandidatesFailed"
            :ref-candidates="ruleRefCandidates"
            :ref-candidates-missing-kinds="refCandidatesMissingKinds"
            :draft-rule="draftRule"
            :rule-missing="ruleMissing"
            :health-issues="healthIssues"
            :health-failed="ruleHealthFailed"
            :requirement-text="ruleTextPreview.text.value"
            :requirement-text-status="ruleTextPreview.status.value"
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
