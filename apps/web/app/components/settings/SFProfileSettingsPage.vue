<script setup lang="ts">
import { useSForumSeo } from '~/composables/seo/useSForumSeo'
import { userDisplayName } from '~/utils/forum/userDisplay'
import { useProfileApi, type ProfileData, type PublicProfile } from '~/composables/profile/useProfileApi'
import { useAuthSession } from '~/composables/identity/useAuthSession'
import { useUserLanguage } from '~/composables/identity/useUserLanguage'
import { useProfileSettingsDraft } from '~/composables/settings/useProfileSettingsDraft'
import {
  PROFILE_SETTINGS_DEFAULT_TAB,
  profileSettingsPanelDomId,
  profileSettingsTabDomId,
  resolveProfileSettingsTab
} from '~/utils/settings/profileSettings'
import { safeUrl } from '~/utils/sfUrl'
import SFSettingsShell from '~/components/settings/SFSettingsShell.vue'
import SFProfileSettingsPreview from '~/components/settings/SFProfileSettingsPreview.vue'
import SFProfileSettingsTabNav from '~/components/settings/SFProfileSettingsTabNav.vue'
import SFProfileSettingsFooter from '~/components/settings/profile/SFProfileSettingsFooter.vue'
import SFProfileIdentityTab from '~/components/settings/profile/tabs/SFProfileIdentityTab.vue'
import SFProfileStoryTab from '~/components/settings/profile/tabs/SFProfileStoryTab.vue'
import SFProfileLinksTab from '~/components/settings/profile/tabs/SFProfileLinksTab.vue'
import SFProfileExtensionTabPanel from '~/components/settings/profile/SFProfileExtensionTabPanel.vue'
/**
 * 宿主 body 岛：forum.settings.profile。主题 L1 挂载；路由页仅 outlet + fail-closed 回退。
 *
 * 本文件只是 tab 宿主：页头、tab 导航、`?tab=` 同步、唯一可见的分区面板、页脚统一保存、
 * 右栏预览。分区内容各自一个文件（components/settings/profile/tabs/），草稿状态归
 * useProfileSettingsDraft，插件分区归 SFProfileExtensionTabPanel。
 */
const { t } = useI18n()
const localePath = useLocalePath()
const route = useRoute()
const router = useRouter()
const { siteDomain, avatarSettings, nicknameSettings } = useWebOptions()
const { user: authUser, setUser } = useAuthSession()
const { currentLanguage, languageOptions, updateLanguage } = useUserLanguage()
const { formatDateOnly } = useSiteDateTime()
const profileApi = useProfileApi()

useSForumSeo({
  title: () => t('profileSettings.metaTitle'),
  description: () => t('profileSettings.metaDescription'),
  type: 'website',
  noindex: true
})

const { data: profile, pending } = await useAsyncData(
  'my-profile',
  () => profileApi.getMyProfile(),
  { default: () => null as PublicProfile | null }
)

const {
  draft,
  metaDraft,
  tabs,
  fieldErrors,
  errorCounts,
  firstErrorTab,
  saveState,
  saving,
  errorMessage,
  globalErrorCount,
  formReady,
  isDirty,
  hydrate,
  dismissError,
  resetDraft,
  save,
  clearFieldError,
  setMetaValue
} = useProfileSettingsDraft({
  profile,
  currentLanguage,
  updateLanguage
})

const activeTab = ref<string>(PROFILE_SETTINGS_DEFAULT_TAB)
const activeSection = computed(() => tabs.value.find(tab => tab.id === activeTab.value) || null)
// 组件型插件分区自带保存按钮：此时页脚的统一保存必须禁用并说明原因。
const pluginManagedTab = computed(() => activeSection.value?.source === 'extension' && activeSection.value.type === 'component')
const saveDisabledReason = computed(() => {
  if (pluginManagedTab.value) return t('profileSettings.extension.saveManagedByExtension')
  return ''
})
const canSave = computed(() => isDirty.value && !saving.value && !pending.value && !pluginManagedTab.value)
const errorSummary = computed(() => {
  const first = firstErrorTab.value
  if (!first) return null
  const label = tabs.value.find(tab => tab.id === first.id)?.label || first.id
  return { label, count: first.count }
})

const currentAvatar = computed(() => profile.value?.profile.avatar || null)
// 预览跟着草稿走：改了昵称但没保存时，右侧应该显示改后的名字。
const displayName = computed(() => draft.displayName.trim() || userDisplayName(profile.value))
const publicProfilePath = computed(() => profile.value ? localePath(`/u/${profile.value.username}`) : undefined)
const publicProfilePrefix = computed(() => `${siteDomain.value}/u/`)
const joinedLabel = computed(() => profile.value ? formatDateOnly(profile.value.joinedAt) : '')
const publicWebsiteText = computed(() => {
  const value = draft.websiteUrl.trim()
  return value ? value.replace(/^https?:\/\//i, '') : ''
})
const publicWebsiteHref = computed(() => safeUrl(draft.websiteUrl))

watch(profile, (value) => {
  if (!value || formReady.value) return
  hydrate(value)
}, { immediate: true })

// 分区清单依赖 profile（插件分区随 GET /profile 下发）：必须等资料行落地再归一化
// `?tab=`，否则深链里的插件分区 id 会被当成未知值，连带把地址栏改掉。
watch(tabs, (list) => {
  if (!formReady.value) return
  activeTab.value = resolveProfileSettingsTab(route.query.tab, list, activeTab.value)
}, { immediate: true })

watch(() => route.query.tab, (value) => {
  if (!formReady.value) return
  activeTab.value = resolveProfileSettingsTab(value, tabs.value, activeTab.value)
})

// 只在 tab 真的变了时 replace（不用 push：Back 不该变成 tab 历史）。
watch(activeTab, (tab) => {
  if (route.query.tab === tab) return
  const query = { ...route.query }
  if (tab === PROFILE_SETTINGS_DEFAULT_TAB) {
    delete query.tab
  } else {
    query.tab = tab
  }
  void router.replace({ query })
})

watch(() => draft.displayName, () => clearFieldError('displayName'))

function syncAuthUser(patch: { displayName?: string, avatar?: ProfileData['avatar'] }) {
  if (!authUser.value) return
  setUser({ ...authUser.value, ...patch })
}

async function onSubmit() {
  const updated = await save()
  if (!updated) return
  profile.value = updated
  syncAuthUser({ displayName: updated.displayName })
}

// 头像上传是即时写操作：直接合并资料行，顺带更新导航里的头像。
function onAvatarChanged(updated: ProfileData) {
  if (profile.value) {
    profile.value = { ...profile.value, profile: updated }
  }
  syncAuthUser({ avatar: updated.avatar })
}

function jumpToErrorTab() {
  const first = firstErrorTab.value
  if (first) activeTab.value = first.id
}

function selectTab(tabId: string) {
  activeTab.value = tabId
}
</script>

<template>
  <SFSettingsShell
    class="sforum-settings-profile"
    data-sforum-island-body="forum.component.settings_profile"
    active="profile"
    title-id="profile-settings-title"
    :title="t('profileSettings.canvasTitle')"
    :description="t('profileSettings.canvasDescription')"
    :rail-label="t('profileSettings.preview.ariaLabel')"
    :public-profile-path="publicProfilePath"
    :show-rail="Boolean(profile)"
  >
    <template v-if="pending && !profile">
      <div class="sforum-settings-profile__loading">
        <SFSkeleton class="h-16 w-16 rounded-full" />
        <div class="min-w-0 flex-1 space-y-3">
          <SFSkeleton class="h-7 w-2/3" />
          <SFSkeleton class="h-4 w-full" />
          <SFSkeleton class="h-4 w-3/5" />
        </div>
      </div>
    </template>

    <template v-else-if="profile">
      <SFAlert
        v-if="errorMessage"
        variant="danger"
        :title="errorMessage"
        closable
        class="sforum-settings-profile__alert"
        @close="dismissError"
      />

      <SFProfileSettingsTabNav
        :tabs="tabs"
        :model-value="activeTab"
        :error-counts="errorCounts"
        @update:model-value="selectTab"
      />

      <form class="sforum-settings-profile__form" novalidate @submit.prevent="onSubmit">
        <div
          :id="profileSettingsPanelDomId(activeTab)"
          class="sforum-settings-profile__panel"
          role="tabpanel"
          :aria-labelledby="profileSettingsTabDomId(activeTab)"
        >
          <SFProfileIdentityTab
            v-if="activeTab === 'identity'"
            :profile="profile"
            :display-name-draft="draft.displayName"
            :locale-draft="draft.locale"
            :avatar="currentAvatar"
            :avatar-settings="avatarSettings"
            :nickname-settings="nicknameSettings"
            :language-options="languageOptions"
            :public-profile-prefix="publicProfilePrefix"
            :busy="saving"
            :field-errors="fieldErrors"
            @update:display-name-draft="draft.displayName = $event"
            @update:locale-draft="draft.locale = $event"
            @avatar-changed="onAvatarChanged"
          />
          <SFProfileStoryTab
            v-else-if="activeTab === 'story'"
            :bio-draft="draft.bio"
            :signature-draft="draft.signature"
            :busy="saving"
            :field-errors="fieldErrors"
            @update:bio-draft="draft.bio = $event"
            @update:signature-draft="draft.signature = $event"
          />
          <SFProfileLinksTab
            v-else-if="activeTab === 'links'"
            :location-draft="draft.location"
            :website-draft="draft.websiteUrl"
            :busy="saving"
            :field-errors="fieldErrors"
            @update:location-draft="draft.location = $event"
            @update:website-draft="draft.websiteUrl = $event"
          />
          <SFProfileExtensionTabPanel
            v-else-if="activeSection"
            :section="activeSection"
            :values="metaDraft"
            :field-errors="fieldErrors"
            :busy="saving"
            @update:value="setMetaValue"
            @change="clearFieldError"
          />
        </div>

        <SFProfileSettingsFooter
          :dirty="isDirty"
          :saving="saving"
          :can-save="canSave"
          :save-disabled-reason="saveDisabledReason"
          :error-summary="errorSummary"
          :global-error-count="globalErrorCount"
          @reset="resetDraft"
          @jump-error="jumpToErrorTab"
        />
      </form>
    </template>

    <template #rail>
      <SFProfileSettingsPreview
        v-if="profile"
        :profile="profile"
        :display-name="displayName"
        :avatar="currentAvatar"
        :bio="draft.bio"
        :location="draft.location"
        :website-text="publicWebsiteText"
        :website-href="publicWebsiteHref"
        :joined-label="joinedLabel"
        :dirty="isDirty"
        :public-profile-path="publicProfilePath || localePath('/')"
        show-scope
      />
    </template>
  </SFSettingsShell>
</template>

<style src="~/assets/css/sforum-profile-settings.css" lang="css"></style>
