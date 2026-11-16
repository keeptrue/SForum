<script setup lang="ts">
import { mailDeliveryCodeKey, type MailDelivery } from '../model'
const { t, te } = useI18n()
const { request } = useApiClient()
const toast = useToast()
const pending = ref(true)
const items = ref<MailDelivery[]>([])
const errorMessage = ref('')
const replayingID = ref<number | null>(null)
onMounted(load)
defineExpose({ refresh: load, pending })
async function load() { pending.value = true; try { items.value = (await request<{ items: MailDelivery[] }>('/admin/mail/deliveries')).items } catch (error) { errorMessage.value = apiErrorMessage(error) || t('admin.mailSettings.loadFailed') } finally { pending.value = false } }
function label(group: 'deliveryStatus' | 'templates' | 'reasons', code: string) { const key = mailDeliveryCodeKey(group, code); return te(key) ? t(key) : code }
function result(item: MailDelivery) {
  const reason = item.reason?.trim() || ''
  const summary = item.errorSummary?.trim() || ''
  const reasonLabel = reason ? label('reasons', reason) : ''
  return reasonLabel && summary && summary !== reason && summary !== reasonLabel ? `${reasonLabel} (${summary})` : reasonLabel || summary || '-'
}
// 只有终态失败可重放：sent/skipped 后端会回 409，这里连按钮都不给，
// 免得用户点出一个必然失败的请求。dead 是 outbox 词表里的另一种终态失败。
function canReplay(status: string) { return status === 'failed' || status === 'dead' }
async function replay(item: MailDelivery) {
  replayingID.value = item.id
  try {
    await request(`/admin/mail/deliveries/${item.id}/replay`, { method: 'POST' })
    toast.add({ color: 'success', icon: 'i-lucide-check', title: t('admin.mailSettings.deliveryReplayQueued'), duration: 5000 })
    await load()
  } catch (error) {
    // 重放失败是阻塞性结论（用户刚点了按钮）：不自动消失，让他读原因。
    errorMessage.value = apiErrorMessage(error) || t('admin.mailSettings.deliveryReplayFailed')
  } finally {
    replayingID.value = null
  }
}
function color(status: string): 'success' | 'error' | 'warning' | 'neutral' | 'info' { return status === 'sent' ? 'success' : status === 'failed' ? 'error' : ['queued', 'sending'].includes(status) ? 'warning' : status === 'skipped' ? 'neutral' : 'info' }
</script>
<template>
  <UCard class="border-slate-200 bg-white dark:border-zinc-800 dark:bg-zinc-900">
    <template #header><div><h2 class="text-base font-bold">{{ t('admin.mailSettings.recentDeliveries') }}</h2><p class="mt-1 text-xs text-muted">{{ t('admin.mailSettings.deliveriesHelp') }}</p></div></template>
    <SFAlert v-if="errorMessage" variant="danger" :title="errorMessage" />
    <div class="overflow-x-auto"><table class="min-w-full text-sm"><thead class="text-left text-xs text-muted"><tr><th class="p-3">{{ t('admin.mailSettings.recipient') }}</th><th class="p-3">{{ t('admin.mailSettings.template') }}</th><th class="p-3">{{ t('admin.mailSettings.status') }}</th><th class="p-3">{{ t('admin.mailSettings.reason') }}</th><th class="p-3">{{ t('admin.mailSettings.actions') }}</th></tr></thead><tbody><tr v-for="item in items" :key="item.id" class="border-b border-slate-200 dark:border-zinc-800"><td class="p-3">{{ item.recipient }}</td><td class="p-3">{{ label('templates', item.templateKey) }}</td><td class="p-3"><UBadge :color="color(item.status)" variant="soft">{{ label('deliveryStatus', item.status) }}</UBadge></td><td class="p-3 text-xs">{{ result(item) }}<p v-if="item.reason === 'stale_swept'" class="mt-1 text-warning">{{ t('admin.mailSettings.staleSweptWarning') }}</p></td><td class="p-3"><UButton v-if="canReplay(item.status)" size="xs" variant="soft" color="neutral" icon="i-lucide-rotate-ccw" :loading="replayingID === item.id" :aria-label="t('admin.mailSettings.deliveryReplay')" :title="t('admin.mailSettings.deliveryReplay')" @click="replay(item)">{{ t('admin.mailSettings.deliveryReplay') }}</UButton></td></tr></tbody></table></div>
    <SFEmptyState v-if="!pending && items.length === 0" :title="t('admin.mailSettings.noDeliveries')" />
  </UCard>
</template>
