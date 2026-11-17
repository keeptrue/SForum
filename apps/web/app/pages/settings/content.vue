<script setup lang="ts">
import SFContentReviewPage from '~/components/settings/SFContentReviewPage.vue'
import { useSForumSeo } from '~/composables/seo/useSForumSeo'

/**
 * 账号设置 · 我的内容（待审 / 被退回）路由壳。
 *
 * 位置说明：作者侧审核状态是**会话私有**的账号信息，因此挂在账号设置下
 * （`/settings/content`），而不是复活 2026-07-21 有意删掉的 `/my/*` 第一方页面
 * （见 `knowledge/sessions/archive/2026-07/2026-07-21-remove-my-pages-handoff.md`；
 * 后端 API 保留给 clients，第一方不再有 `/my` 页面）。
 *
 * 它是 Page Registry 页面，但 **host-owned**（`Replaceable: false`）：
 * 审核状态没有可主题化的呈现面 —— 任何主题都不该改写「被退回 + 审核意见」的语义。
 * 因此 catalog 里不给它内置主题模板，页面走 core 回退壳，chrome（navbar/footer）
 * 由 `SFPageOutlet` 统一提供；页面自己只负责 settings 几何（`SFSettingsShell`），
 * 绝不在这里再挂一份 navbar/footer。
 */
definePageMeta({ requiresAuth: true })

const { t } = useI18n()

// head 由本页的 useSForumSeo 独占（本页是唯一调用它的 settings 页）：
// 同一份 noindex 也在 `PageViewModels` 的 `forum.settings.content` 分支里声明，
// 但 ViewModel 的 SEO 只在主题模板路径生效；本页是 host-owned + core 回退，
// 回退壳不渲染 ViewModel 的 SEO，所以不会出现重复标签。
// 改 robots 只需改这里；那边是给主题模板路径留的兜底，不要以为要同时改两处。
useSForumSeo({
  title: () => t('moderation.authorStatus.title'),
  description: () => t('moderation.authorStatus.description'),
  type: 'website',
  noindex: true
})
</script>

<template>
  <SFPageOutlet page="forum.settings.content">
    <SFContentReviewPage />
  </SFPageOutlet>
</template>
