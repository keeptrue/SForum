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

// 计数器必须与后端同口径：`Array.from` 数的是 Unicode 码点，
// 而 JS 的 `length` 数 UTF-16 码元，会把汉字之外的字符（emoji 等）数成 2。
const characterCount = (value: string) => Array.from(value).length
</script>

<template>
  <div class="sforum-settings-profile__panel-body">
    <p class="sforum-settings-profile__panel-description">
      {{ t('profileSettings.sections.storyDescription') }}
    </p>

    <div class="sforum-settings-profile__field">
      <div class="sforum-settings-profile__field-topline">
        <label for="profile-settings-bio">{{ t('profileSettings.bio') }}</label>
        <span>{{ characterCount(bioDraft) }} / 160</span>
      </div>
      <!-- HTML maxlength 数 UTF-16 码元，一个码点最多占 2 码元，所以 160/200 码点的输入框取 2× 才不会被提前截断；真正的上限由服务端与上面的计数器负责。 -->
      <textarea
        id="profile-settings-bio"
        :value="bioDraft"
        rows="4"
        :maxlength="320"
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
        <span>{{ characterCount(signatureDraft) }} / 200</span>
      </div>
      <input
        id="profile-settings-signature"
        :value="signatureDraft"
        type="text"
        :maxlength="400"
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
