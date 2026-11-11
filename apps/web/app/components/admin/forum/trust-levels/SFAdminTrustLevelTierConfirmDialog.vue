<script setup lang="ts">
/**
 * 档位的破坏性意图确认框（停用 / 删除），一个组件两种 mode。
 *
 * 为什么从 shell 的常驻 UAlert 换成 modal：
 * 1. 停用「常驻可读」的诉求其实是「**在确认那一刻**必须读到后果」。常驻条在列表上方，
 *    运营滚到第 6 档按下停用后，确认条出现在视口外，等于没披露（旧实现的漏点）；
 * 2. 删除是不可恢复的动作，必须阻塞（modal 挡住其它操作，Enter/取消的语义由对话框承担）；
 * 3. 两种 mode 共用同一套内容骨架，文案与确认色分开 —— 这样「红按钮 = 不可恢复」始终成立。
 *
 * 几何与 shell 里 restore / structure 两个弹窗逐字同款：图标块 + h3 + 描述 + 右对齐按钮组。
 * 自建内容根补 `role="dialog"` + `aria-modal`（UModal 的内容根不带这两个属性）。
 */
import type { TrustLevelTierIntentRequest } from '~/composables/admin/forum/trustLevels/useTrustLevelTierIntents'

const props = withDefaults(defineProps<{
  /** `null` = 没有任何意图（弹窗关闭）；决定渲染哪套文案与确认色。 */
  mode: 'retire' | 'delete' | null
  request: TrustLevelTierIntentRequest | null
  open: boolean
}>(), {
  mode: null,
  request: null
})

const emit = defineEmits<{
  confirm: []
  cancel: []
  'update:open': [value: boolean]
}>()

const { t, locale } = useI18n()

/**
 * `UModal` 的 v-model 桥：外部只管传 `open`，关闭统一走 `cancel`，
 * 这样「按 Esc / 点遮罩」与「点取消」是同一条路径（不会出现弹窗关了但意图还挂着）。
 */
const modalOpen = computed<boolean>({
  get: () => props.open,
  set: (value) => {
    if (value) emit('update:open', true)
    else emit('cancel')
  }
})

/** 标题里的档位名：当前语言优先，另一语言兜次，最后 TL{rank}。 */
const displayName = computed(() => {
  const name = props.request?.name
  if (!name) return ''
  const preferZh = locale.value.toLowerCase().startsWith('zh')
  const primary = (preferZh ? name.zh : name.en).trim()
  const other = (preferZh ? name.en : name.zh).trim()
  return primary || other || `TL${props.request?.rank ?? 0}`
})

const title = computed(() => {
  if (props.mode === 'delete') return t('admin.forum.trustLevels.tierDelete.title', { name: displayName.value })
  return t('admin.forum.trustLevels.tiers.retireConfirm.title', { name: displayName.value })
})

const titleId = computed(() => `sf-trust-levels-tier-${props.mode === 'delete' ? 'delete' : 'retire'}-title`)
/** 删除会永久消失的六类东西：逐条列出，避免「整档删掉」被读成「只是隐藏」。 */
const deleteLossKeys = ['thresholds', 'limits', 'earnable', 'name', 'icon', 'color'] as const
const impactKeys = computed(() => props.request?.impactKeys ?? [])
</script>

<template>
  <UModal v-model:open="modalOpen" :ui="{ content: 'sm:max-w-lg' }" data-sf-trust-levels-tier-dialog>
    <template #content>
      <div
        class="p-5 sm:p-6"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        :data-sf-trust-levels-tier-dialog-root="mode ?? ''"
      >
        <div class="flex items-start gap-3">
          <div
            class="flex size-9 shrink-0 items-center justify-center rounded-md"
            :class="mode === 'delete'
              ? 'bg-red-50 text-red-600 dark:bg-red-950/40 dark:text-red-300'
              : 'bg-amber-50 text-amber-600 dark:bg-amber-950/40 dark:text-amber-300'"
          >
            <UIcon :name="mode === 'delete' ? 'i-lucide-trash-2' : 'i-lucide-ban'" class="size-4" aria-hidden="true" />
          </div>
          <div class="min-w-0">
            <h3 :id="titleId" class="text-base font-bold text-slate-900 dark:text-white">{{ title }}</h3>

            <!-- ───────────────────────── 停用（软停用，可恢复） ───────────────────────── -->
            <template v-if="mode === 'delete'">
              <p class="mt-1 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                {{ t('admin.forum.trustLevels.tierDelete.description') }}
              </p>
              <ul class="mt-3 grid list-disc gap-1 pl-5 text-sm leading-6 text-slate-600 dark:text-zinc-300">
                <li v-for="key in deleteLossKeys" :key="key">
                  {{ t(`admin.forum.trustLevels.tierDelete.items.${key}`) }}
                </li>
              </ul>
              <p class="mt-2 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                {{ t('admin.forum.trustLevels.tierDelete.rankSafe') }}
              </p>
              <p class="mt-2 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300" data-sf-tier-delete-id-note>
                {{ t('admin.forum.trustLevels.tierDelete.idNote') }}
              </p>
              <p class="mt-2 break-words text-sm leading-6 text-slate-500 dark:text-zinc-400">
                {{ t('admin.forum.trustLevels.tierDelete.irreversible') }}
              </p>
            </template>

            <template v-else>
              <p class="mt-1 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300">
                {{ t('admin.forum.trustLevels.tiers.retireConfirm.description') }}
              </p>
              <p v-if="impactKeys.length > 0" class="mt-2 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300" data-sf-tier-retire-impact>
                {{ t('admin.forum.trustLevels.tiers.retireConfirm.impact', { keys: impactKeys.join('、') }) }}
              </p>
              <p v-else class="mt-2 break-words text-sm leading-6 text-slate-600 dark:text-zinc-300" data-sf-tier-retire-no-impact>
                {{ t('admin.forum.trustLevels.tiers.retireConfirm.noImpact') }}
              </p>
            </template>
          </div>
        </div>

        <div class="mt-5 flex flex-wrap justify-end gap-2">
          <UButton
            type="button"
            color="neutral"
            variant="outline"
            :data-sf-tier-dialog-cancel="mode ?? ''"
            @click="emit('cancel')"
          >
            {{ t(`admin.forum.trustLevels.${mode === 'delete' ? 'tierDelete' : 'tiers.retireConfirm'}.cancel`) }}
          </UButton>
          <UButton
            type="button"
            :color="mode === 'delete' ? 'error' : 'warning'"
            variant="solid"
            :leading-icon="mode === 'delete' ? 'i-lucide-trash-2' : 'i-lucide-ban'"
            :data-sf-tier-dialog-accept="mode ?? ''"
            @click="emit('confirm')"
          >
            {{ t(`admin.forum.trustLevels.${mode === 'delete' ? 'tierDelete' : 'tiers.retireConfirm'}.confirm`) }}
          </UButton>
        </div>
      </div>
    </template>
  </UModal>
</template>
