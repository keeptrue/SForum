<script setup lang="ts">
import { usePermissions } from '~/composables/identity/usePermissions'
import type { ForumSettings } from '~/utils/forum/forumTaxonomy'
import { useAdminForumSettingsTab } from '~/composables/admin/settings/useAdminForumSettingsTab'
import SFAdminForumTabFrame from '../SFAdminForumTabFrame.vue'

const props = defineProps<{ settings: ForumSettings }>()
const emit = defineEmits<{ saved: [settings: ForumSettings] }>()
const { t } = useI18n()
const { can } = usePermissions()
// 只认细粒度子键：settings.manage 是后端的展开来源，会话里始终存在，
// 把它当候选会让管理员 deny 掉的 forum.settings.manage 重新放行（task-24）。
const canManage = computed(() => can('forum.settings.manage'))
const keys = ['commentMinRunes', 'commentMaxRunes', 'commentMaxNestingDepth', 'treeDescendantsPerRoot', 'commentEditWindowMinutes', 'commentCooldownSeconds', 'dailyCommentLimit'] as const
const tab = useAdminForumSettingsTab(toRef(props, 'settings'), keys, () => canManage.value, settings => emit('saved', settings))
</script>

<template>
  <SFAdminForumTabFrame tab="comments" :dirty="tab.hasChanges.value" :validation-key="tab.validationKey.value" :saving="tab.saving.value" :restoring="tab.restoring.value" :can-save="tab.canSave.value" @save="tab.saveCurrent" @reset="tab.resetChanges" @restore="tab.restoreCurrent">
    <section class="grid gap-4 md:grid-cols-2">
      <UFormField :label="t('admin.forum.settings.commentMin')" name="comment-min"><UInputNumber v-model="tab.form.commentMinRunes" :min="0" :max="50000" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.commentMax')" name="comment-max"><UInputNumber v-model="tab.form.commentMaxRunes" :min="1" :max="50000" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.commentNesting')" name="comment-nesting"><UInputNumber v-model="tab.form.commentMaxNestingDepth" :min="0" :max="20" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.treeDescendantsPerRoot')" name="tree-descendants"><UInputNumber v-model="tab.form.treeDescendantsPerRoot" :min="1" :max="100" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.commentEditWindow')" name="comment-edit-window"><UInputNumber v-model="tab.form.commentEditWindowMinutes" :min="0" :max="10080" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.commentCooldown')" name="comment-cooldown"><UInputNumber v-model="tab.form.commentCooldownSeconds" :min="0" :max="86400" :disabled="!canManage" class="w-full" /></UFormField>
      <UFormField :label="t('admin.forum.settings.dailyCommentLimit')" name="daily-comment-limit"><UInputNumber v-model="tab.form.dailyCommentLimit" :min="0" :max="10000" :disabled="!canManage" class="w-full" /></UFormField>
    </section>
  </SFAdminForumTabFrame>
</template>
