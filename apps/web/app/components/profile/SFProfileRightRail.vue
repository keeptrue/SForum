<script setup lang="ts">
import type { PublicProfile } from '~/composables/profile/useProfileApi'
import SFTrustLevelCard from '~/components/profile/SFTrustLevelCard.vue'
import type { TrustLevelTier, TrustLevelView } from '~/composables/profile/useTrustLevel'
import { forumTopicPath, type ForumTopicSummary } from '~/utils/forum/forumTaxonomy'
import { shouldShowUserHandle } from '~/utils/forum/userDisplay'
import { extensionTabLabel } from '~/utils/profile/profileExtensionTab'
import { safeUrl } from '~/utils/sfUrl'

const props = defineProps<{
  profile: PublicProfile
  displayName: string
  hasPublicDetails: boolean
  recentTopics: ForumTopicSummary[]
  /** 公开等级（M4）；为 null 时整卡不渲染，不留占位。 */
  trustLevel?: TrustLevelView | null
  /** 档位阶梯（运营自定义的 N 档）；不传时卡片回落内置五档展示兜底。 */
  trustLevelLadder?: TrustLevelTier[]
  /** true = 等级卡整卡可点（覆盖层按钮），点击回发 `open-trust-detail`。 */
  trustLevelInteractive?: boolean
  drawer?: boolean
}>()

const emit = defineEmits<{ 'open-trust-detail': [] }>()

const { t, locale } = useI18n()
const localePath = useLocalePath()
const { formatDateOnly } = useSiteDateTime()
const { seoSettings } = useWebOptions()
const topicUrlMode = computed(() => seoSettings.value.topicUrlMode)

function topicTo(topic: ForumTopicSummary) {
  return localePath(forumTopicPath(topic, topicUrlMode.value))
}

// 与 SFProfileShowPage 同口径：safeUrl 拒绝非法 scheme 时返回 ''，
// 显隐必须以净化结果为准，否则会渲染出 href 为空的死链接。
const websiteHref = computed(() => {
  const raw = props.profile.profile.websiteUrl?.trim() || ''
  return raw ? safeUrl(raw) : ''
})

// 曾用名是可选公开面：只有运营开启 history_public 时接口才会返回，最多一条。
const previousDisplayName = computed(() => props.profile.previousDisplayNames?.[0] || '')

// 身份小字：未自定义昵称时不重复 @username，整条只剩 `UID n`（不留前导 `·` 或多余空格）。
// 判定与主页头部、SFUserIdentityName 共用 shouldShowUserHandle，比较对象是已解析的 displayName。
const identityMeta = computed(() => {
  const uid = `UID ${props.profile.userId}`
  if (!shouldShowUserHandle(props.displayName, props.profile.username)) {
    return uid
  }
  return `@${props.profile.username?.trim()} · ${uid}`
})

// widget tab 已在主 tab 栏原地挂载；这里再列一次会让同一个扩展入口出现两处。
const extensionLinks = computed(() => (props.profile.extensionTabs || [])
  .filter(tab => tab.kind !== 'widget'))

function extensionTabTo(tab: NonNullable<PublicProfile['extensionTabs']>[number]) {
  if (tab.kind === 'hostLink') {
    return localePath(tab.url)
  }
  return tab.url.startsWith('/') ? tab.url : `/${tab.url}`
}
</script>

<template>
  <div class="sf-profile-right-content" :class="{ 'sf-profile-right-content--drawer': drawer }">
    <section
      class="sf-profile-side-section sf-profile-side-identity"
      :class="{ 'sf-profile-side-identity--card-follows': props.trustLevel }"
    >
      <div class="sf-profile-side-identity__main">
        <SFAvatar :name="displayName" :avatar="profile.profile.avatar" size="md" />
        <span>
          <strong>{{ displayName }}</strong>
          <small>{{ identityMeta }}</small>
        </span>
      </div>
      <div class="sf-profile-side-stats">
        <div>
          <strong>{{ profile.topicCount }}</strong>
          <span>{{ t('profile.topicCount') }}</span>
        </div>
        <div>
          <strong>{{ profile.commentCount }}</strong>
          <span>{{ t('profile.commentCount') }}</span>
        </div>
      </div>
    </section>

    <!-- 等级是身份元数据，不是 tab 内容：放右栏让每个 tab 下都可见。
         可点性由页面决定（弹窗是页面级唯一一份，右栏只回发意图）。 -->
    <SFTrustLevelCard
      v-if="props.trustLevel"
      :level="props.trustLevel.level"
      :metrics="props.trustLevel.metrics"
      :ladder="props.trustLevelLadder"
      :interactive="Boolean(props.trustLevelInteractive)"
      @open="emit('open-trust-detail')"
    />

    <section class="sf-profile-side-section">
      <header class="sf-profile-side-section__head">
        <h3>{{ t('profile.publicDetails') }}</h3>
        <span>{{ t('profile.memberInfo') }}</span>
      </header>
      <!-- 与 demo B1 右侧一致：位置 / 网站 / 加入时间优先；签名可选 -->
      <ul class="sf-profile-detail-list">
        <li v-if="profile.profile.location">
          <UIcon name="i-lucide-map-pin" class="size-4" aria-hidden="true" />
          <span>{{ profile.profile.location }}</span>
        </li>
        <li v-if="websiteHref">
          <UIcon name="i-lucide-link" class="size-4" aria-hidden="true" />
          <a :href="websiteHref" target="_blank" rel="noopener noreferrer nofollow">
            {{ (profile.profile.websiteUrl || '').replace(/^https?:\/\//i, '').replace(/\/$/, '') }}
          </a>
        </li>
        <li>
          <UIcon name="i-lucide-calendar-days" class="size-4" aria-hidden="true" />
          <span>{{ t('profile.joinedOn', { date: formatDateOnly(profile.joinedAt) }) }}</span>
        </li>
        <li v-if="previousDisplayName">
          <UIcon name="i-lucide-history" class="size-4" aria-hidden="true" />
          <span>{{ t('profile.previousNames') }}：{{ previousDisplayName }}</span>
        </li>
        <li v-if="profile.profile.signature">
          <UIcon name="i-lucide-quote" class="size-4" aria-hidden="true" />
          <span>{{ profile.profile.signature }}</span>
        </li>
      </ul>
    </section>

    <section class="sf-profile-side-section">
      <header class="sf-profile-side-section__head">
        <h3>{{ t('profile.recentTopics') }}</h3>
        <span>{{ t('profile.publicContent') }}</span>
      </header>
      <ol v-if="recentTopics.length" class="sf-profile-recent-list">
        <li v-for="(topic, index) in recentTopics" :key="topic.id">
          <span class="sf-profile-recent-list__rank">{{ String(index + 1).padStart(2, '0') }}</span>
          <a :href="topicTo(topic)">{{ topic.title }}</a>
          <span>{{ topic.commentCount }}</span>
        </li>
      </ol>
      <p v-else class="sf-profile-side-empty">{{ t('profile.recentTopicsEmpty') }}</p>
    </section>

    <section v-if="extensionLinks.length" class="sf-profile-side-section">
      <header class="sf-profile-side-section__head">
        <h3>{{ t('profile.extensionLinks') }}</h3>
        <span>{{ t('profile.publicContent') }}</span>
      </header>
      <div class="sf-profile-extension-links">
        <template v-for="tab in extensionLinks" :key="`${tab.extensionId}:${tab.id}`">
          <NuxtLink v-if="tab.kind === 'hostLink'" :to="extensionTabTo(tab)">
            <UIcon v-if="tab.icon" :name="tab.icon" class="size-4" aria-hidden="true" />
            <span>{{ extensionTabLabel(tab, String(locale)) }}</span>
          </NuxtLink>
          <a v-else :href="extensionTabTo(tab)">
            <UIcon v-if="tab.icon" :name="tab.icon" class="size-4" aria-hidden="true" />
            <span>{{ extensionTabLabel(tab, String(locale)) }}</span>
          </a>
        </template>
      </div>
    </section>
  </div>
</template>
