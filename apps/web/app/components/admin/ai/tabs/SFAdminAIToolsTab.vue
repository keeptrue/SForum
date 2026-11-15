<script setup lang="ts">
import type { AdminAIPurposeOverride, AdminAIToolAvailability, AdminAIValidatedPurpose } from '~/composables/admin/useAdminAI'
import type { AIToolFilterOrigin } from '~/utils/admin/adminAITools'
import { useAdminRoutes } from '~/composables/admin/useAdminRoutes'
import { ADMIN_AI_DRAFT_KEY } from '~/composables/admin/useAdminAISettingsForm'
import SFAdminAIToolReferencesBlock from '~/components/admin/ai/blocks/SFAdminAIToolReferencesBlock.vue'
import { toSettingsOverride, toSettingsOverrides } from '~/components/admin/ai/toolOverrides'
import { MAX_TOOLS_PER_REQUEST, purposeToolsAreOverridden, selectedPurposeTools } from '~/utils/admin/adminAI'
import {
  filterTools,
  purposeToolExposure,
  purposeToolSupportState,
  removeToolFromAllPurposes,
  resetPurposeTools,
  setPurposeTool,
  toolDisplayEnglish,
  toolOriginLabel
} from '~/utils/admin/adminAITools'

// 工具页签是「站内有哪些工具、谁能用」的一屏答案。布局沿用权限页的矩阵表：
// 行是工具，列是用途，单元格就是某个用途是否把该工具交给模型。大卡片在这里
// 是反模式——13 个工具会变成 13 张需要滚动的卡片，运营者反而看不出差异。
//
// 页签只渲染：清单与用途目录由页面壳持有，编辑写进注入的同一份草稿，保存仍由
// 页面壳统一发起。英文说明的编辑放在详情弹窗里，表格保持一行一个工具。
const { t, locale } = useI18n()
const toast = useToast()
const draft = inject(ADMIN_AI_DRAFT_KEY)!
const { form, ai, readOnly } = draft
const adminRoutes = useAdminRoutes()

const tools = computed<AdminAIToolAvailability[]>(() => ai.availableTools.value ?? [])
const catalog = computed<AdminAIValidatedPurpose[]>(() => ai.purposes.value ?? [])

// 表格的列顺序决定可读性：最常被讨论的回复用途放第一列，判定次之，其余按 id
// 稳定排序。登记表本身是字母序，直接照搬会让「AI 回复」排在第三列。
const displayPurposes = computed<AdminAIValidatedPurpose[]>(() => {
  const rank = (id: string) => id === 'forum.reply' ? 0 : id === 'forum.reply.triage' ? 1 : 2
  return [...catalog.value].sort((left, right) => {
    const delta = rank(left.id) - rank(right.id)
    return delta !== 0 ? delta : left.id.localeCompare(right.id)
  })
})

function localized(text: { default: string, 'en-US'?: string } | undefined) {
  if (!text) return ''
  return locale.value?.toLowerCase().startsWith('en') && text['en-US'] ? text['en-US'] : text.default
}

const query = ref('')
const origin = ref<AIToolFilterOrigin>('all')
const visibleTools = computed(() => filterTools(tools.value, query.value, origin.value))

const originOptions = computed(() => [
  { label: t('admin.ai.tools.filterAll'), value: 'all' },
  { label: t('admin.ai.tools.filterCore'), value: 'core' },
  { label: t('admin.ai.tools.filterPlugin'), value: 'plugin' }
])

function setOrigin(value: unknown) {
  const next = typeof value === 'string' ? value : 'all'
  origin.value = next === 'core' || next === 'plugin' ? next : 'all'
}

// 概览按「徽标怎么显示」计数：toolOriginLabel 是来源的唯一判定，避免徽标写着 Core、
// 统计却把它算进插件。总数与两项分项之和必须相等。
function isCore(tool: AdminAIToolAvailability) {
  return toolOriginLabel(tool.origin) === 'Core'
}
const coreCount = computed(() => tools.value.filter(isCore).length)
const pluginCount = computed(() => tools.value.length - coreCount.value)

// 生效集合取草稿而不是已保存文档：勾掉某个用途里的这个工具后，「未被任何用途使用」
// 必须立刻变大，否则运营者会以为勾选没生效。
const exposure = computed(() => purposeToolExposure(catalog.value, form.value?.purposes))
const usedNames = computed(() => {
  const used = new Set<string>()
  for (const names of exposure.value.values()) {
    for (const name of names) used.add(name)
  }
  return used
})
const unusedCount = computed(() => tools.value.filter(tool => !usedNames.value.has(tool.name)).length)

// 三种「工具调用根本不会发生」的原因各有不同的修复动作（改闸门、绑供应商、换供应商），
// 因此分开报，不合并成一句「工具不可用」。
const diagnostics = computed(() => {
  const items: { key: string, text: string }[] = []
  const settings = form.value
  if (!settings) return items
  // nil 表示「按服务端默认生效」，而默认值不是 0：只有运营者显式写下的 0 才关闭工具
  // 循环。把 nil 当 0 会让每个新站点一进页面就看到一条假的诊断。
  if (settings.gates?.toolCallsPerReply === 0) {
    items.push({ key: 'gate-off', text: t('admin.ai.tools.diagnosticsGateOff') })
  }
  const noProfile: string[] = []
  const noToolSupport: string[] = []
  for (const purpose of catalog.value) {
    const state = purposeToolSupportState(purpose, settings)
    if (state === 'no-profile') noProfile.push(localized(purpose.label))
    else if (state === 'no-tool-support') noToolSupport.push(localized(purpose.label))
  }
  if (noProfile.length > 0) {
    items.push({ key: 'no-profile', text: `${t('admin.ai.tools.diagnosticsNoProfile')} ${noProfile.join(' · ')}` })
  }
  if (noToolSupport.length > 0) {
    items.push({ key: 'no-tool-support', text: `${t('admin.ai.tools.diagnosticsNoToolSupport')} ${noToolSupport.join(' · ')}` })
  }
  return items
})

function overrideFor(id: string): AdminAIPurposeOverride | undefined {
  return form.value?.purposes?.[id]
}

function effectiveFor(purpose: AdminAIValidatedPurpose, tool: AdminAIToolAvailability) {
  return selectedPurposeTools(purpose.declaredTools, overrideFor(purpose.id)).includes(tool.name)
}

// 与复选框的生效集合共用同一个判定：`{"tools":{}}` 也算接管（显式空列表），
// 否则列头会说「已自定义」而单元格又说「跟随声明」。
function purposeOverridden(purpose: AdminAIValidatedPurpose) {
  return purposeToolsAreOverridden(overrideFor(purpose.id))
}

// 恢复只清掉工具覆盖，失败姿态原样保留：两个决定互不隶属，顺手清掉另一项会让
// 运营者以为自己在改工具时丢了降级策略。
function resetPurpose(purpose: AdminAIValidatedPurpose) {
  const current = form.value
  if (!current || readOnly.value) return
  const purposes: Record<string, AdminAIPurposeOverride> = { ...(current.purposes ?? {}) }
  const converted = toSettingsOverride(resetPurposeTools(overrideFor(purpose.id)))
  if (converted) purposes[purpose.id] = converted
  else delete purposes[purpose.id]
  current.purposes = purposes
  toast.add({
    color: 'success',
    icon: 'i-lucide-rotate-ccw',
    title: t('admin.ai.tools.resetPurposeDone', { purpose: localized(purpose.label) }),
    duration: 5000
  })
}

// 勾选一个用途就是往它的白名单里加这个名字。上限只在「新增」时拦：取消勾选永远
// 允许，否则一个已经超限的配置会把运营者锁死在界面里。
function toggle(purpose: AdminAIValidatedPurpose, tool: AdminAIToolAvailability, checked: boolean) {
  const current = form.value
  if (!current || readOnly.value) return
  const effective = selectedPurposeTools(purpose.declaredTools, overrideFor(purpose.id))
  if (checked && !effective.includes(tool.name) && effective.length >= MAX_TOOLS_PER_REQUEST) {
    toast.add({
      color: 'warning',
      icon: 'i-lucide-triangle-alert',
      title: t('admin.ai.tools.tooMany', { max: MAX_TOOLS_PER_REQUEST }),
      duration: 5000
    })
    return
  }
  const next = setPurposeTool(purpose.declaredTools, overrideFor(purpose.id), tool.name, checked)
  const purposes: Record<string, AdminAIPurposeOverride> = { ...(current.purposes ?? {}) }
  const converted = toSettingsOverride(next)
  if (converted) purposes[purpose.id] = converted
  else delete purposes[purpose.id]
  current.purposes = purposes
}

function removeEverywhere(tool: AdminAIToolAvailability) {
  const current = form.value
  if (!current || readOnly.value) return
  current.purposes = toSettingsOverrides(removeToolFromAllPurposes(catalog.value, current.purposes, tool.name))
  toast.add({
    color: 'success',
    icon: 'i-lucide-check',
    title: t('admin.ai.tools.removeEverywhereDone', { name: tool.name }),
    duration: 5000
  })
  detailOpen.value = false
}

async function copyName(tool: AdminAIToolAvailability) {
  try {
    if (!import.meta.client || !navigator.clipboard?.writeText) throw new Error('clipboard unavailable')
    await navigator.clipboard.writeText(tool.name)
    toast.add({
      color: 'success',
      icon: 'i-lucide-check',
      title: t('admin.ai.tools.copied', { name: tool.name }),
      duration: 5000
    })
  } catch {
    toast.add({
      color: 'error', duration: 0,
      icon: 'i-lucide-triangle-alert',
      title: t('admin.ai.tools.copyFailed')
    })
  }
}

// 详情弹窗：表格保持一行一个工具，完整说明与英文编辑放在弹窗里。这与角色页的
// 列表 + 弹窗同一种交互，不在表格里塞一个会撑高整行的编辑器。
const detailOpen = ref(false)
const detailTool = ref<AdminAIToolAvailability | null>(null)
const englishDraft = ref('')

const detailEnglish = computed(() => detailTool.value
  ? toolDisplayEnglish(detailTool.value, form.value?.toolDisplayDescriptions)
  : { text: '', source: 'none' as const })
const detailOperatorText = computed(() => {
  const tool = detailTool.value
  if (!tool) return ''
  return form.value?.toolDisplayDescriptions?.[tool.name]?.['en-US']?.trim() ?? ''
})
const detailUsedBy = computed(() => {
  const tool = detailTool.value
  if (!tool) return []
  return displayPurposes.value.filter(purpose => effectiveFor(purpose, tool))
})
const detailTimeout = computed(() => {
  const value = detailTool.value?.timeoutMs
  return typeof value === 'number' && value > 0 ? value : 0
})

function closeDetail() {
  detailOpen.value = false
}

function englishSourceLabel() {
  if (detailEnglish.value.source === 'operator') return t('admin.ai.tools.descriptionEnOperator')
  if (detailEnglish.value.source === 'declared') return t('admin.ai.tools.descriptionEnDeclared')
  return ''
}

function openDetail(tool: AdminAIToolAvailability) {
  detailTool.value = tool
  englishDraft.value = detailOperatorText.value || toolDisplayEnglish(tool, form.value?.toolDisplayDescriptions).text
  detailOpen.value = true
}

// 写入只动 en-US 这一个键：其它语言的覆盖由别的入口维护，这里不该顺手删掉。
// trim 后为空表示「没有覆盖」——键与空条目一起删掉，否则脏判定会永远成立。
function writeEnglish(value: string) {
  const current = form.value
  const tool = detailTool.value
  if (!current || !tool) return
  const overrides = { ...(current.toolDisplayDescriptions ?? {}) }
  const locales = { ...(overrides[tool.name] ?? {}) }
  const text = value.trim()
  if (text) locales['en-US'] = text
  else delete locales['en-US']
  if (Object.keys(locales).length > 0) overrides[tool.name] = locales
  else delete overrides[tool.name]
  current.toolDisplayDescriptions = Object.keys(overrides).length > 0 ? overrides : undefined
}

// 这里只改草稿：真正的保存是页面壳的那一次保存。因此提示的是「尚未保存」，而不是
// 一句会被误读成已入库的「已保存」。
function saveEnglish() {
  if (readOnly.value) return
  writeEnglish(englishDraft.value)
  detailOpen.value = false
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-pencil',
    title: t('admin.ai.tools.dirtyHint'),
    duration: 5000
  })
}

function clearEnglish() {
  if (readOnly.value) return
  writeEnglish('')
  englishDraft.value = ''
  toast.add({
    color: 'neutral',
    icon: 'i-lucide-pencil',
    title: t('admin.ai.tools.dirtyHint'),
    duration: 5000
  })
}

function parameterNames(tool: AdminAIToolAvailability) {
  return tool.parameters?.names ?? []
}

function requiredNames(tool: AdminAIToolAvailability) {
  return new Set(tool.parameters?.required ?? [])
}
</script>

<template>
  <div v-if="form" class="flex min-w-0 flex-col gap-4">
    <div v-if="diagnostics.length > 0" class="flex min-w-0 flex-col gap-2">
      <p class="text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ t('admin.ai.tools.diagnosticsTitle') }}</p>
      <UAlert
        v-for="item in diagnostics"
        :key="item.key"
        color="warning"
        variant="soft"
        icon="i-lucide-triangle-alert"
        :description="item.text"
      />
    </div>

    <UAlert
      v-if="tools.length === 0"
      color="neutral"
      variant="soft"
      icon="i-lucide-info"
      :title="t('admin.ai.tools.empty')"
      :description="t('admin.ai.tools.emptyHint')"
    />

    <UCard v-else class="border-slate-200 bg-white text-slate-900 dark:border-zinc-800 dark:bg-zinc-900 dark:text-zinc-100">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="flex min-w-0 flex-wrap items-center gap-2">
          <UInput
            v-model="query"
            icon="i-lucide-search"
            size="md"
            class="w-56 max-w-full sm:w-64"
            :placeholder="t('admin.ai.tools.searchPlaceholder')"
            :aria-label="t('admin.ai.tools.searchPlaceholder')"
          />
          <USelect
            :model-value="origin"
            size="md"
            class="w-32"
            value-key="value"
            label-key="label"
            :items="originOptions"
            :aria-label="t('admin.ai.tools.filterAll')"
            @update:model-value="setOrigin"
          />
          <UBadge color="neutral" variant="soft" class="border border-slate-200 dark:border-zinc-800">
            {{ visibleTools.length }} / {{ tools.length }}
          </UBadge>
        </div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-slate-500 dark:text-zinc-400">
          <span>{{ t('admin.ai.tools.originCore') }} <strong class="text-slate-900 dark:text-zinc-100">{{ coreCount }}</strong></span>
          <span>{{ t('admin.ai.tools.originPlugin') }} <strong class="text-slate-900 dark:text-zinc-100">{{ pluginCount }}</strong></span>
          <span>{{ t('admin.ai.tools.unusedCount') }} <strong class="text-slate-900 dark:text-zinc-100">{{ unusedCount }}</strong></span>
        </div>
      </div>
      <p class="mt-2 text-sm text-slate-400 dark:text-zinc-500">
        {{ t('admin.ai.tools.intro') }}
      </p>

      <div class="mt-3 overflow-x-auto rounded-lg border border-slate-200 dark:border-zinc-800">
      <table class="w-full min-w-[1120px] table-fixed divide-y divide-slate-200 text-sm dark:divide-zinc-800">
        <colgroup>
          <col class="w-52">
          <col class="w-72">
          <col class="w-32">
          <col class="w-24">
          <col v-for="purpose in displayPurposes" :key="purpose.id" class="w-20">
        </colgroup>
        <thead class="bg-slate-50 dark:bg-zinc-950">
          <tr>
            <th scope="col" class="sticky left-0 z-10 bg-slate-50 px-3 py-2.5 text-left text-xs font-semibold text-slate-500 dark:bg-zinc-950 dark:text-zinc-400">
              {{ t('admin.ai.tools.columns.tool') }}
            </th>
            <th scope="col" class="px-3 py-2.5 text-left text-xs font-semibold text-slate-500 dark:text-zinc-400">
              {{ t('admin.ai.tools.columns.description') }}
            </th>
            <th scope="col" class="px-3 py-2.5 text-left text-xs font-semibold text-slate-500 dark:text-zinc-400">
              {{ t('admin.ai.tools.columns.parameters') }}
            </th>
            <th scope="col" class="px-3 py-2.5 text-left text-xs font-semibold text-slate-500 dark:text-zinc-400">
              {{ t('admin.ai.tools.columns.timeout') }}
            </th>
            <th
              v-for="purpose in displayPurposes"
              :key="purpose.id"
              scope="col"
              class="px-2 py-2.5 text-center text-xs font-semibold leading-tight text-slate-500 dark:text-zinc-400"
              :title="purpose.id"
            >
              {{ localized(purpose.label) }}
              <!-- 有覆盖时列头给出恢复入口：只清工具白名单，失败姿态保留。 -->
              <UButton
                v-if="purposeOverridden(purpose)"
                size="xs"
                color="neutral"
                variant="ghost"
                icon="i-lucide-rotate-ccw"
                class="ml-1 size-5 p-0"
                :disabled="readOnly"
                :aria-label="t('admin.ai.tools.resetPurpose', { purpose: localized(purpose.label) })"
                :title="t('admin.ai.tools.resetPurpose', { purpose: localized(purpose.label) })"
                @click="resetPurpose(purpose)"
              />
            </th>
          </tr>
        </thead>
        <tbody class="divide-y divide-slate-200 bg-white dark:divide-zinc-800 dark:bg-zinc-900">
          <tr v-for="tool in visibleTools" :key="tool.name" class="align-top">
            <td class="sticky left-0 z-10 bg-white px-3 py-2.5 dark:bg-zinc-900">
              <div class="flex items-start justify-between gap-2">
                <div class="min-w-0">
                  <button
                    type="button"
                    class="block w-full truncate text-left font-mono text-sm font-semibold text-slate-900 hover:underline dark:text-zinc-100"
                    :title="tool.name"
                    @click="openDetail(tool)"
                  >
                    {{ tool.name }}
                  </button>
                  <div class="mt-1 flex flex-wrap items-center gap-1">
                    <UBadge color="neutral" variant="soft" size="sm" class="font-mono text-xs">
                      {{ toolOriginLabel(tool.origin) }}
                    </UBadge>
                    <UBadge v-if="tool.origin?.version" color="neutral" variant="outline" size="sm" class="font-mono text-xs">
                      {{ tool.origin.version }}
                    </UBadge>
                    <NuxtLink
                      v-if="tool.settingsPath"
                      :to="adminRoutes.path(tool.settingsPath)"
                      class="inline-flex size-6 items-center justify-center rounded text-slate-400 transition-colors hover:text-slate-700 dark:text-zinc-500 dark:hover:text-zinc-200"
                      :aria-label="t('admin.attachments.openPluginSettings')"
                      :title="t('admin.attachments.openPluginSettings')"
                    >
                      <UIcon name="i-lucide-arrow-up-right" class="size-3.5" />
                    </NuxtLink>
                  </div>
                </div>
                <UButton
                  size="xs"
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-copy"
                  :aria-label="t('admin.ai.tools.copyName')"
                  :title="t('admin.ai.tools.copyName')"
                  @click="copyName(tool)"
                />
              </div>
            </td>

            <td class="px-3 py-2.5">
              <p
                v-if="tool.description"
                class="line-clamp-2 text-sm text-slate-700 dark:text-zinc-200"
                :title="tool.description"
              >
                {{ tool.description }}
              </p>
              <div class="mt-1 flex items-center gap-1.5">
                <p
                  v-if="toolDisplayEnglish(tool, form.toolDisplayDescriptions).text"
                  class="min-w-0 flex-1 truncate text-sm text-slate-500 dark:text-zinc-400"
                  :title="toolDisplayEnglish(tool, form.toolDisplayDescriptions).text"
                >
                  {{ toolDisplayEnglish(tool, form.toolDisplayDescriptions).text }}
                </p>
                <p v-else class="min-w-0 flex-1 text-sm text-slate-400 dark:text-zinc-500">
                  {{ t('admin.ai.tools.descriptionEnMissing') }}
                </p>
                <UBadge
                  v-if="toolDisplayEnglish(tool, form.toolDisplayDescriptions).source !== 'none'"
                  :color="toolDisplayEnglish(tool, form.toolDisplayDescriptions).source === 'operator' ? 'primary' : 'neutral'"
                  variant="soft"
                  size="sm"
                  class="shrink-0 text-xs"
                >
                  {{ toolDisplayEnglish(tool, form.toolDisplayDescriptions).source === 'operator' ? t('admin.ai.tools.descriptionEnOperator') : t('admin.ai.tools.descriptionEnDeclared') }}
                </UBadge>
                <UButton
                  size="xs"
                  color="neutral"
                  variant="ghost"
                  icon="i-lucide-pencil"
                  class="shrink-0"
                  :disabled="readOnly"
                  :aria-label="t('admin.ai.tools.descriptionEnEdit')"
                  :title="t('admin.ai.tools.descriptionEnEdit')"
                  @click="openDetail(tool)"
                />
              </div>
            </td>

            <td class="px-3 py-2.5">
              <div v-if="parameterNames(tool).length > 0" class="flex flex-wrap gap-1">
                <UBadge
                  v-for="name in parameterNames(tool).slice(0, 3)"
                  :key="name"
                  color="neutral"
                  variant="soft"
                  size="sm"
                  class="font-mono text-xs"
                >
                  {{ name }}<span v-if="requiredNames(tool).has(name)">*</span>
                </UBadge>
                <span v-if="parameterNames(tool).length > 3" class="text-xs text-slate-400 dark:text-zinc-500">
                  +{{ parameterNames(tool).length - 3 }}
                </span>
              </div>
              <span v-else class="text-sm text-slate-400 dark:text-zinc-500">—</span>
              <p v-if="tool.parameters?.truncated" class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
                {{ t('admin.ai.tools.parametersTruncated') }}
              </p>
            </td>

            <td class="px-3 py-2.5 text-sm text-slate-600 dark:text-zinc-300">
              <span v-if="tool.timeoutMs">{{ tool.timeoutMs }} ms</span>
              <span v-else class="text-slate-400 dark:text-zinc-500">—</span>
            </td>

            <td v-for="purpose in displayPurposes" :key="purpose.id" class="px-2 py-2.5">
              <div class="flex flex-col items-center gap-1">
                <UCheckbox
                  :model-value="effectiveFor(purpose, tool)"
                  :disabled="readOnly"
                  :aria-label="`${tool.name} · ${localized(purpose.label)}`"
                  @update:model-value="(checked: boolean | 'indeterminate') => toggle(purpose, tool, checked === true)"
                />

              </div>
            </td>

          </tr>
        </tbody>
        </table>
      </div>
    </UCard>

    <UAlert
      v-if="tools.length > 0 && visibleTools.length === 0"
      color="neutral"
      variant="soft"
      icon="i-lucide-info"
      :title="t('admin.ai.tools.noResults')"
    />

    <!-- 白名单比工具活得久：未注册名字与「先配策略、后装插件」的手动点名都在这里。 -->
    <SFAdminAIToolReferencesBlock />

    <p v-if="draft.dirty.value" class="text-sm text-amber-600 dark:text-amber-400">
      {{ t('admin.ai.tools.dirtyHint') }}
    </p>
  </div>
  <p v-else class="text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.ai.loading') }}</p>

  <UModal v-model:open="detailOpen" :ui="{ content: 'sm:max-w-2xl' }">
    <template #content>
      <div v-if="detailTool" class="flex max-h-[85vh] flex-col">
        <div class="flex items-start justify-between gap-3 border-b border-slate-200 px-5 py-4 dark:border-zinc-800">
          <div class="min-w-0">
            <p class="truncate font-mono text-sm font-semibold text-slate-900 dark:text-zinc-100">{{ detailTool.name }}</p>
            <div class="mt-1 flex flex-wrap items-center gap-1">
              <UBadge color="neutral" variant="soft" size="sm" class="font-mono text-xs">{{ toolOriginLabel(detailTool.origin) }}</UBadge>
              <UBadge v-if="detailTool.origin?.version" color="neutral" variant="outline" size="sm" class="font-mono text-xs">
                {{ detailTool.origin.version }}
              </UBadge>
            </div>
          </div>
          <UButton
            size="sm"
            color="neutral"
            variant="ghost"
            icon="i-lucide-x"
            :aria-label="t('admin.ai.tools.detailClose')"
            :title="t('admin.ai.tools.detailClose')"
            @click="closeDetail"
          />
        </div>

        <div class="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <div v-if="detailTool.description">
            <p class="text-sm font-medium text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.descriptionModel') }}</p>
            <p class="mt-1 text-sm text-slate-700 dark:text-zinc-200">{{ detailTool.description }}</p>
          </div>

          <div>
            <div class="flex flex-wrap items-center gap-2">
              <p class="text-sm font-medium text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.descriptionEn') }}</p>
              <UBadge v-if="detailEnglish.source !== 'none'" :color="detailEnglish.source === 'operator' ? 'primary' : 'neutral'" variant="soft" size="sm">
                {{ englishSourceLabel() }}
              </UBadge>
            </div>
            <p v-if="detailEnglish.text" class="mt-1 text-sm text-slate-700 dark:text-zinc-200">{{ detailEnglish.text }}</p>
            <p v-else class="mt-1 text-sm text-slate-400 dark:text-zinc-500">{{ t('admin.ai.tools.descriptionEnMissing') }}</p>
            <UTextarea
              v-model="englishDraft"
              class="mt-2 w-full"
              :rows="3"
              :disabled="readOnly"
              :placeholder="t('admin.ai.tools.descriptionEnPlaceholder')"
            />
            <div class="mt-2 flex flex-wrap items-center gap-2">
              <UButton size="sm" :disabled="readOnly" @click="saveEnglish">{{ t('admin.common.save') }}</UButton>
              <UButton size="sm" color="neutral" variant="outline" :disabled="readOnly || !detailOperatorText" @click="clearEnglish">
                {{ t('admin.ai.tools.descriptionEnClear') }}
              </UButton>
            </div>
          </div>

          <div class="grid gap-4 sm:grid-cols-2">
            <div>
              <p class="text-sm font-medium text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.parameters') }}</p>
              <div v-if="parameterNames(detailTool).length > 0" class="mt-1 flex flex-wrap gap-1">
                <UBadge
                  v-for="name in parameterNames(detailTool)"
                  :key="name"
                  color="neutral"
                  variant="soft"
                  size="sm"
                  class="font-mono text-xs"
                >
                  {{ name }}<span v-if="requiredNames(detailTool).has(name)">*</span>
                </UBadge>
              </div>
              <p v-else class="mt-1 text-sm text-slate-400 dark:text-zinc-500">{{ t('admin.ai.tools.parametersEmpty') }}</p>
              <p v-if="requiredNames(detailTool).size > 0" class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
                * {{ t('admin.ai.tools.parametersRequired') }}
              </p>
              <p v-if="detailTool.parameters?.truncated" class="mt-1 text-xs text-slate-400 dark:text-zinc-500">
                {{ t('admin.ai.tools.parametersTruncated') }}
              </p>
            </div>
            <div>
              <p class="text-sm font-medium text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.columns.timeout') }}</p>
              <p v-if="detailTimeout > 0" class="mt-1 text-sm text-slate-700 dark:text-zinc-200">
                {{ t('admin.ai.tools.timeout', { ms: detailTimeout }) }}
              </p>
              <p v-else class="mt-1 text-sm text-slate-400 dark:text-zinc-500">—</p>
              <p class="mt-1 text-xs text-slate-400 dark:text-zinc-500">{{ t('admin.ai.tools.timeoutHint') }}</p>
            </div>
          </div>

          <div>
            <p class="text-sm font-medium text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.usedBy') }}</p>
            <div v-if="detailUsedBy.length > 0" class="mt-1 flex flex-wrap gap-1.5">
              <UBadge v-for="purpose in detailUsedBy" :key="purpose.id" color="primary" variant="soft" size="sm">
                {{ localized(purpose.label) }}
              </UBadge>
            </div>
            <p v-else class="mt-1 text-sm text-slate-500 dark:text-zinc-400">{{ t('admin.ai.tools.usedByNone') }}</p>
          </div>
        </div>

        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-slate-200 px-5 py-3 dark:border-zinc-800">
          <UButton
            size="sm"
            color="neutral"
            variant="outline"
            icon="i-lucide-x"
            :disabled="readOnly || detailUsedBy.length === 0"
            @click="removeEverywhere(detailTool)"
          >
            {{ t('admin.ai.tools.removeEverywhere') }}
          </UButton>
          <UButton size="sm" color="neutral" variant="ghost" @click="closeDetail">
            {{ t('admin.ai.tools.detailClose') }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
