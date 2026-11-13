<script setup lang="ts">
import { useProfileApi, type PublicProfile } from '~/composables/profile/useProfileApi'
import { apiErrorStatusCode } from '~/composables/useApiClient'
import { userDisplayName, type ForumUserRef } from '~/utils/forum/userDisplay'
import SFUserIdentityName from '~/components/forum/SFUserIdentityName.vue'
import SFBadgeChip from '~/components/forum/SFBadgeChip.vue'
import type { ForumBadgeRef } from '~/utils/forum/forumBadge'

const props = withDefaults(defineProps<{
  /** 列表场景已有的用户摘要：名字与头像直接消费，网络资料只做增强。 */
  user: ForumUserRef
  /** 传 user.username 之外的显式用户名（提及场景只有 username）。 */
  username?: string
  profilePath: string
  /** 评论载荷里已投影的佩戴徽章；缺省时等资料接口补齐。 */
  topBadge?: ForumBadgeRef | null
}>(), {
  topBadge: null
})

const { t } = useI18n()
const profileApi = useProfileApi()
const profileCache = useState<Record<string, PublicProfile>>('profile:comment-user-preview-cache', () => ({}))
const pending = ref(false)
const failed = ref(false)
// 查无此人（404）与「资料暂不可用」分开表达：@提及 常常指向拼错或不存在的用户名。
const notFound = ref(false)

const previewUsername = computed(() => props.username || props.user?.username || '')
const profile = computed(() => profileCache.value[previewUsername.value] || null)
// 名字与头像优先取调用方传入的 UserSummary（与评论行内展示一致，避免闪变）；
// 但调用方用 username 充当占位 displayName 时不算真昵称，让网络资料补齐。
// 网络资料仅在本地没有真 displayName 时生效（例如纯 @username 的提及占位）。
const localDisplayName = computed(() => {
  const name = props.user?.displayName?.trim()
  return name && name !== previewUsername.value ? name : ''
})
const displayName = computed(() => localDisplayName.value || profile.value?.displayName || previewUsername.value)
const displayAvatar = computed(() => props.user?.avatar || profile.value?.profile.avatar || null)
const bio = computed(() => profile.value?.profile.bio?.trim() || profile.value?.profile.signature?.trim() || '')
// 优先评论载荷：与用户点开预览卡的那颗 chip 同源，同页不会出现两枚不同的徽章；
// @提及 没有评论载荷，回落资料接口的 `topBadge`。
const badge = computed(() => props.topBadge ?? profile.value?.topBadge ?? null)

async function loadProfile() {
  if (!previewUsername.value || profile.value || pending.value) {
    return
  }
  pending.value = true
  failed.value = false
  notFound.value = false
  try {
    const result = await profileApi.getPublicProfile(previewUsername.value)
    profileCache.value = { ...profileCache.value, [previewUsername.value]: result }
  } catch (error) {
    // 公开资料可能被站点关闭、用户不存在或暂时不可用；保留已有的身份和主页入口。
    failed.value = true
    notFound.value = apiErrorStatusCode(error) === 404
  } finally {
    pending.value = false
  }
}

onMounted(() => {
  void loadProfile()
})
</script>

<template>
  <section
    class="sf-comment-user-preview"
    role="dialog"
    :aria-label="t('topicDetail.userPreview.ariaLabel', { name: displayName })"
    data-testid="comment-user-preview"
    :data-state="pending ? 'loading' : notFound ? 'not-found' : failed ? 'unavailable' : 'ready'"
  >
    <div class="sf-comment-user-preview__identity">
      <SFAvatar :name="displayName" :avatar="displayAvatar" size="md" />
      <SFUserIdentityName
        :user="{ username: previewUsername, displayName }"
        :to="profilePath"
        class="sf-comment-user-preview__names sf-comment-user-preview__identity-names"
      >
        <!-- 不传 username：卡片名字本身已是资料页链接，chip 再带链接就是嵌套 <a>。 -->
        <template #name-suffix>
          <SFBadgeChip v-if="badge" :badge="badge" />
        </template>
      </SFUserIdentityName>
    </div>

    <SFSkeleton v-if="pending" width="88%" height="0.75rem" class="sf-comment-user-preview__skeleton" />
    <p v-else-if="failed" class="sf-comment-user-preview__status">
      {{ notFound ? t('topicDetail.userPreview.notFound') : t('topicDetail.userPreview.unavailable') }}
    </p>
    <p v-else class="sf-comment-user-preview__bio">
      {{ bio || t('topicDetail.userPreview.noBio') }}
    </p>

    <div v-if="profile" class="sf-comment-user-preview__stats">
      <span>{{ t('topicDetail.userPreview.topics', { count: profile.topicCount }) }}</span>
      <span aria-hidden="true">·</span>
      <span>{{ t('topicDetail.userPreview.replies', { count: profile.commentCount }) }}</span>
    </div>

    <NuxtLink :to="profilePath" class="sf-comment-user-preview__primary">
      <UIcon name="i-lucide-user-round" class="size-4" aria-hidden="true" />
      <span>{{ t('topicDetail.userPreview.viewProfile') }}</span>
    </NuxtLink>
  </section>
</template>
