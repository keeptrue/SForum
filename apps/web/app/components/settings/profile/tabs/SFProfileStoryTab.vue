<script setup lang="ts">
/**
 * 资料设置 · 介绍分区（简介 / 个性签名）。
 *
 * 只做输入与本地计数；校验与提交都在 useProfileSettingsDraft，
 * 服务端字段错误通过 fieldErrors 回到这里就地展示。
 */
defineProps<{
  bioDraft: string
  signatureDraft: string
  busy: boolean
  fieldErrors: Record<string, string[]>
}>()

const emit = defineEmits<{
  'update:bioDraft': [value: string]
  'update:signatureDraft': [value: string]
}>()

const { t } = useI18n()
</script>

<template>
  <div class="sforum-settings-profile__panel-body">
    <p class="sforum-settings-profile__panel-description">
      {{ t('profileSettings.sections.storyDescription') }}
    </p>

    <div class="sforum-settings-profile__field">
      <div class="sforum-settings-profile__field-topline">
        <label for="profile-settings-bio">{{ t('profileSettings.bio') }}</label>
        <span>{{ bioDraft.length }} / 500</span>
      </div>
      <textarea
        id="profile-settings-bio"
        :value="bioDraft"
        rows="4"
        maxlength="500"
        :class="{ 'is-invalid': fieldErrors.bio }"
        :placeholder="t('profileSettings.bioPlaceholder')"
        :disabled="busy"
        :aria-invalid="fieldErrors.bio ? 'true' : undefined"
        :aria-describedby="fieldErrors.bio ? 'profile-settings-bio-error' : undefined"
        @input="emit('update:bioDraft', ($event.target as HTMLTextAreaElement).value)"
      />
      <p v-if="fieldErrors.bio" id="profile-settings-bio-error" class="sforum-settings-profile__field-error">
        {{ fieldErrors.bio.join(', ') }}
      </p>
    </div>

    <div class="sforum-settings-profile__field">
      <div class="sforum-settings-profile__field-topline">
        <label for="profile-settings-signature">{{ t('profileSettings.signature') }}</label>
        <span>{{ signatureDraft.length }} / 200</span>
      </div>
      <input
        id="profile-settings-signature"
        :value="signatureDraft"
        type="text"
        maxlength="200"
        :class="{ 'is-invalid': fieldErrors.signature }"
        :placeholder="t('profileSettings.signaturePlaceholder')"
        :disabled="busy"
        :aria-invalid="fieldErrors.signature ? 'true' : undefined"
        :aria-describedby="fieldErrors.signature ? 'profile-settings-signature-error' : undefined"
        @input="emit('update:signatureDraft', ($event.target as HTMLInputElement).value)"
      >
      <p v-if="fieldErrors.signature" id="profile-settings-signature-error" class="sforum-settings-profile__field-error">
        {{ fieldErrors.signature.join(', ') }}
      </p>
      <p>{{ t('profileSettings.signaturePublicHint') }}</p>
    </div>
  </div>
</template>
