<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import type { ForumSettings } from '~/utils/forum/forumTaxonomy'
import { useAdminForumSettingsTab } from '~/composables/admin/settings/useAdminForumSettingsTab'
import SFAdminForumTabFrame from '../SFAdminForumTabFrame.vue'
import SFNativeSelect from '~/components/forms/SFNativeSelect.vue'

const props = defineProps<{ settings: ForumSettings }>()
const emit = defineEmits<{ saved: [settings: ForumSettings] }>()
const { t } = useI18n()
const { can } = usePermissions()
// 只认细粒度子键：settings.manage 是后端的展开来源，会话里始终存在，
// 把它当候选会让管理员 deny 掉的 forum.settings.manage 重新放行（task-24）。
const canManage = computed(() => can('forum.settings.manage'))
const keys = ['allowAuthorCloseReplies', 'allowAuthorDelete', 'autoLockIdleDays', 'showTopicEditMark', 'duplicateTitlePolicy', 'showCommentEditMark', 'softDeleteVisibility', 'mentionsEnabled', 'mentionsMaxPerPost'] as const
const tab = useAdminForumSettingsTab(toRef(props, 'settings'), keys, () => canManage.value, settings => emit('saved', settings))
const checks = [
  ['allowAuthorCloseReplies', 'allowAuthorCloseReplies'],
  ['allowAuthorDelete', 'allowAuthorDelete'],
  ['showTopicEditMark', 'showTopicEditMark'],
  ['showCommentEditMark', 'showCommentEditMark'],
  ['mentionsEnabled', 'mentionsEnabled']
] as const
</script>

<template>
  <SFAdminForumTabFrame tab="behavior" :dirty="tab.hasChanges.value" :validation-key="tab.validationKey.value" :saving="tab.saving.value" :restoring="tab.restoring.value" :can-save="tab.canSave.value" @save="tab.saveCurrent" @reset="tab.resetChanges" @restore="tab.restoreCurrent">
    <section class="grid gap-3 md:grid-cols-2">
      <label v-for="check in checks" :key="check[0]" class="flex items-start gap-3 rounded-md border border-slate-200 p-3 text-sm dark:border-zinc-800">
        <input v-model="tab.form[check[0]]" type="checkbox" :disabled="!canManage" class="mt-1 size-4"><strong>{{ t(`admin.forum.settings.${check[1]}`) }}</strong>
      </label>
    </section>
    <section class="grid gap-4 border-t border-slate-200 pt-5 dark:border-zinc-800 md:grid-cols-2">
      <UFormField :label="t('admin.forum.settings.autoLockIdleDays')" name="auto-lock"><UInputNumber v-model="tab.form.autoLockIdleDays" :min="0" :max="3650" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.mentionsMaxPerPost')" name="mentions-max"><UInputNumber v-model="tab.form.mentionsMaxPerPost" :min="0" :max="50" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.duplicateTitlePolicy')" name="dup-title"><SFNativeSelect v-model="tab.form.duplicateTitlePolicy" :disabled="!canManage" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option value="off">{{ t('admin.forum.settings.duplicateOff') }}</option><option value="block">{{ t('admin.forum.settings.duplicateBlock') }}</option></SFNativeSelect></UFormField>
      <UFormField :label="t('admin.forum.settings.softDeleteVisibility')" name="soft-delete"><SFNativeSelect v-model="tab.form.softDeleteVisibility" :disabled="!canManage" class="h-11 w-full rounded-md border border-slate-200 bg-white px-3 dark:border-zinc-700 dark:bg-zinc-950"><option value="author_and_staff">{{ t('admin.forum.settings.softDeleteAuthorStaff') }}</option><option value="staff_only">{{ t('admin.forum.settings.softDeleteStaffOnly') }}</option><option value="hidden">{{ t('admin.forum.settings.softDeleteHidden') }}</option></SFNativeSelect></UFormField>
    </section>
  </SFAdminForumTabFrame>
</template>
