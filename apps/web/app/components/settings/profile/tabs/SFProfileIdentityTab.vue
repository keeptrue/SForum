<script setup lang="ts">
import type { AvatarView, ProfileData, PublicProfile } from '~/composables/profile/useProfileApi'
import type { AvatarSettings, NicknameSettings } from '~/composables/useWebOptions'
import type { UserLanguageOption } from '~/composables/identity/useUserLanguage'
import { FORUM_PERMISSIONS, usePermissions } from '~/composables/identity/usePermissions'
import { useProfileApi } from '~/composables/profile/useProfileApi'
import SFProfileNicknameField from '~/components/settings/SFProfileNicknameField.vue'
/**
 * 资料设置 · 身份分区（头像 / 昵称 / 用户名 / 常用语言）。
 *
 * 头像上传是即时写操作（上传即生效），其余字段由页脚统一保存；所以这里只把
 * 头像结果 emit 给宿主去同步会话，不参与草稿脏检查。
 * 不再渲染与 tab 标签重复的 h2，只保留 tab 自带标签对应的一段说明。
 */
const props = defineProps<{
  profile: PublicProfile
  displayNameDraft: string
  localeDraft: string
  avatar: AvatarView | null
  avatarSettings: AvatarSettings
  nicknameSettings: NicknameSettings
  languageOptions: UserLanguageOption[]
  publicProfilePrefix: string
  busy: boolean
  fieldErrors: Record<string, string[]>
}>()

const emit = defineEmits<{
  'update:displayNameDraft': [value: string]
  'update:localeDraft': [value: string]
  'avatar-changed': [value: ProfileData]
}>()

const { t } = useI18n()
const toast = useToast()
const profileApi = useProfileApi()
const { can } = usePermissions()

const avatarInput = ref<HTMLInputElement | null>(null)
const avatarBusy = ref(false)
const avatarError = ref('')

const canUploadAvatar = computed(() => props.avatarSettings.allowUpload && can(FORUM_PERMISSIONS.attachmentUpload))
const avatarAccept = computed(() => props.avatarSettings.allowGif ? 'image/jpeg,image/png,image/gif' : 'image/jpeg,image/png')
const avatarCapabilityHint = computed(() => {
  if (!props.avatarSettings.allowUpload) {
    return t('profileSettings.avatarUploadDisabled')
  }
  if (!canUploadAvatar.value) {
    return t('profileSettings.avatarUploadPermissionDenied')
  }
  const types = props.avatarSettings.allowGif ? 'JPG / PNG / GIF' : 'JPG / PNG'
  return t('profileSettings.avatarHint', {
    types,
    size: props.avatarSettings.maxSizeKb,
    dimension: props.avatarSettings.maxDimension
  })
})
const hasUploadedAvatar = computed(() => Boolean(
  props.profile.profile.avatarAttachmentId
  || props.profile.profile.avatar.attachmentId
  || props.profile.profile.avatar.kind === 'uploaded'
))
const usernameError = computed(() => props.fieldErrors.username?.[0] || '')

function openAvatarPicker() {
  if (avatarBusy.value || !canUploadAvatar.value) {
    return
  }
  avatarError.value = ''
  avatarInput.value?.click()
}

async function uploadAvatar(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || avatarBusy.value) {
    return
  }
  avatarError.value = ''
  if (!canUploadAvatar.value) {
    avatarError.value = t('profileSettings.avatarUploadPermissionDenied')
    return
  }
  if (file.size > props.avatarSettings.maxSizeKb * 1024) {
    avatarError.value = t('profileSettings.avatarTooLarge', { size: props.avatarSettings.maxSizeKb })
    return
  }
  if (!props.avatarSettings.allowGif && file.type === 'image/gif') {
    avatarError.value = t('profileSettings.avatarGifDisabled')
    return
  }
  avatarBusy.value = true
  try {
    const updated = await profileApi.uploadAvatar(file)
    emit('avatar-changed', updated)
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: t('profileSettings.avatarUploaded'),
      duration: 5000
    })
  } catch (error) {
    avatarError.value = apiErrorMessage(error) || t('profileSettings.avatarUploadFailed')
  } finally {
    avatarBusy.value = false
  }
}

async function removeAvatar() {
  if (avatarBusy.value || !hasUploadedAvatar.value) {
    return
  }
  avatarError.value = ''
  avatarBusy.value = true
  try {
    const updated = await profileApi.deleteAvatar()
    emit('avatar-changed', updated)
    toast.add({
      color: 'primary',
      icon: 'i-lucide-check',
      title: t('profileSettings.avatarRemoved'),
      duration: 5000
    })
  } catch (error) {
    avatarError.value = apiErrorMessage(error) || t('profileSettings.avatarRemoveFailed')
  } finally {
    avatarBusy.value = false
  }
}
</script>

<template>
  <div class="sforum-settings-profile__panel-body">
    <p class="sforum-settings-profile__panel-description">
      {{ t('profileSettings.sections.identityDescription') }}
    </p>

    <div class="sforum-settings-profile__avatar-editor">
      <SFAvatar :name="displayNameDraft || profile.username" :avatar="avatar" size="lg" />
      <div class="sforum-settings-profile__avatar-copy">
        <strong>{{ t('profileSettings.avatarCurrent') }}</strong>
        <p>{{ avatarCapabilityHint }}</p>
        <p v-if="avatarError" class="sforum-settings-profile__field-error">
          {{ avatarError }}
        </p>
        <div class="sforum-settings-profile__actions">
          <input ref="avatarInput" class="hidden" type="file" :accept="avatarAccept" @change="uploadAvatar">
          <SFButton
            variant="ghost"
            size="sm"
            :disabled="!canUploadAvatar || avatarBusy"
            :loading="avatarBusy"
            @click="openAvatarPicker"
          >
            <template #leading>
              <UIcon name="i-lucide-image-plus" />
            </template>
            {{ t('profileSettings.avatarUpload') }}
          </SFButton>
          <SFButton
            v-if="hasUploadedAvatar"
            variant="ghost"
            size="sm"
            :disabled="avatarBusy"
            @click="removeAvatar"
          >
            <template #leading>
              <UIcon name="i-lucide-trash-2" />
            </template>
            {{ t('profileSettings.avatarRemove') }}
          </SFButton>
        </div>
      </div>
    </div>

    <SFProfileNicknameField
      :model-value="displayNameDraft"
      class="sforum-settings-profile__field--spaced"
      :username="profile.username"
      :policy="nicknameSettings"
      :state="profile.nickname || null"
      :busy="busy"
      :server-error="fieldErrors.displayName?.[0] || ''"
      @update:model-value="emit('update:displayNameDraft', $event)"
    />

    <div class="sforum-settings-profile__field sforum-settings-profile__field--spaced">
      <label for="profile-settings-username">{{ t('profileSettings.username') }}</label>
      <div class="sforum-settings-profile__prefix-field">
        <span>{{ publicProfilePrefix }}</span>
        <input
          id="profile-settings-username"
          :value="profile.username"
          type="text"
          readonly
          :aria-invalid="usernameError ? 'true' : undefined"
          :aria-describedby="usernameError ? 'profile-settings-username-error' : undefined"
        >
      </div>
      <p>{{ t('profileSettings.usernameHint') }}</p>
      <p v-if="usernameError" id="profile-settings-username-error" class="sforum-settings-profile__field-error">
        {{ usernameError }}
      </p>
    </div>

    <div class="sforum-settings-profile__field">
      <label for="profile-settings-locale">{{ t('profileSettings.defaultLanguage') }}</label>
      <USelect
        id="profile-settings-locale"
        :model-value="localeDraft"
        :items="languageOptions"
        value-key="value"
        label-key="label"
        class="w-full"
        :disabled="busy"
        @update:model-value="emit('update:localeDraft', String($event ?? ''))"
      />
      <p>{{ t('profileSettings.defaultLanguageHint') }}</p>
    </div>
  </div>
</template>
