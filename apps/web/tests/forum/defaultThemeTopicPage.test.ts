import { describe, expect, test } from 'bun:test'
import { existsSync, readFileSync } from 'node:fs'

const topicPage = () => readFileSync(
  new URL('../../app/components/forum/SFTopicShowPage.vue', import.meta.url),
  'utf8'
)

const sourceFile = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const themeFile = (path: string) => readFileSync(
  new URL(`../../${path}`, import.meta.url),
  'utf8'
)

const topicComponents = [
  ['SFTopicHeading', 'forum'],
  ['SFTopicSideCard', 'forum'],
  ['SFTopicActionMenu', 'forum'],
  ['SFTopicReplyComposer', 'forum'],
  ['SFTopicCommentComposerDrawer', 'forum'],
  ['SFReportDialog', 'moderation']
] as const

describe('default theme V32 topic page contract', () => {
  test('keeps the core reading path eager with global navigation feedback', () => {
    const app = sourceFile('../../app/app.vue')
    const themeTemplate = sourceFile('../../app/components/SFThemeTemplate.vue')
    const avatar = sourceFile('../../app/components/SFAvatar.vue')
    const heading = sourceFile('../../app/components/forum/SFTopicHeading.vue')
    const comment = sourceFile('../../app/components/forum/SFComment.vue')

    expect(app).toContain('<NuxtLoadingIndicator')
    expect(app).toContain('color="var(--sf-accent)"')
    expect(themeTemplate).toContain("import SFTopicShowPage from './forum/SFTopicShowPage.vue'")
    expect(themeTemplate).toContain("'forum.component.topic_show': SFTopicShowPage")
    expect(themeTemplate).not.toContain("import('./forum/SFTopicShowPage.vue')")
    expect(avatar).toContain("loading: 'lazy'")
    expect(heading).toContain('loading="eager"')
    expect(comment).not.toContain('loading="eager"')
  })

  test('registers the highlight directive during SSR for rendered forum content', () => {
    const renderedContentSources = [
      topicPage(),
      sourceFile('../../app/components/forum/SFComment.vue'),
      sourceFile('../../app/components/SFEditor.vue')
    ]
    const serverPluginPath = new URL('../../app/plugins/highlight.server.ts', import.meta.url)

    expect(renderedContentSources.some(source => source.includes('v-highlight'))).toBe(true)
    expect(existsSync(serverPluginPath)).toBe(true)

    const serverPluginSource = readFileSync(serverPluginPath, 'utf8')
    expect(serverPluginSource).toContain("vueApp.directive('highlight'")
    expect(serverPluginSource).toContain('getSSRProps')
  })

  test('composes focused presentational components without moving route policy into them', () => {
    const source = topicPage()

    for (const [name, domain] of topicComponents) {
      const path = `app/components/${domain}/${name}.vue`
      expect(existsSync(new URL(`../../${path}`, import.meta.url))).toBe(true)
      expect(source).toContain(`<${name}`)

      const component = themeFile(path)
      expect(component).not.toContain('useForumApi(')
      expect(component).not.toContain('useModerationApi(')
      expect(component).not.toContain('usePermissions(')
    }
  })

  test('preserves routing, SEO, rich-content, editor, and extension boundaries', () => {
    const source = topicPage()
    const commentComposer = sourceFile('../../app/composables/forum/useTopicCommentComposerDrawer.ts')

    expect(source).toContain('topicPathLookupCandidates(')
    expect(source).toContain("useRequestHeaders(['accept']).accept")
    expect(source).toContain("includes('text/html')")
    expect(source).toContain('if (!topic.value || !canNormalizeTopicURL)')
    expect(source).toContain('navigateTo(target, { redirectCode: 301 })')
    // 通知深链 /t/{id}/page/{N}#comment-{id} 在 id_slug/slug 模式下走 slug 规范化重定向；
    // 重定向目标必须携带 page 段，否则跨页通知丢失页码、退化为只定位第 1 页。
    expect(source).toContain('forumTopicPath(topic.value, topicUrlMode.value, commentPage.value)')
    expect(source).toContain('useSForumSeo(computed(')
    expect(source).toContain('sanitizeHtml(topic.content.htmlContent)')
    expect(source).toContain('v-highlight')
    // 编辑迁到独立页 /topics/:id/edit（forum.topic.edit）；详情页只负责跳转。
    // 编辑页复用发帖页三栏壳与控件（sforum-home__* + SFTopicComposerPage.css）。
    expect(source).toContain('forumTopicEditPath(')
    expect(source).not.toContain('<SFTopicEditor')
    const editPage = sourceFile('../../app/components/forum/SFTopicEditPage.vue')
    const editTheme = sourceFile('../../../../extensions/builtin/themes/sforum-default/templates/topic-edit.html')
    expect(editPage).toContain('forumApi.updateTopic(')
    expect(editPage).toContain('data-layout="fullwidth-3col"')
    expect(editPage).toContain('SFTopicComposerLeftRail')
    expect(editPage).toContain('SFTopicComposerRightRail')
    expect(editPage).toContain('SFTopicComposerPage.css')
    // 主题壳与发帖页一致：fullwidth-3col，避免侧栏/topbar 错位
    expect(editTheme).toContain('sf-theme-shell--fullwidth-3col')
    expect(editTheme).toContain('data-layout="fullwidth-3col"')
    expect(editTheme).toContain('<sf-topic-editor>')
    expect(source).toContain('applyTopicExtensionAction')
    // E2.2：评论行扩展动作与主题动作同一代理边界
    expect(source).toContain('applyCommentExtensionAction')
    expect(source).toContain('extensionActions')
    // 评论编辑：editor-document 经 initialContent 还原，提交走 forumContentFromEditorPayload
    expect(source).toContain('useTopicCommentComposerDrawer({')
    expect(commentComposer).toContain('forumEditorInitialContent')
    expect(commentComposer).toContain('forumApi.getCommentEditSource(comment.id)')
    expect(commentComposer).toContain('editingRevision.value = source.currentRevision')
    expect(commentComposer).toContain('editingInitialContent')
    expect(source).toContain(':initial-content="composerInitialContent"')
    expect(commentComposer).not.toContain('editingMarkdown.value = comment.content.rawContent')
    expect(commentComposer).toContain('forumContentFromEditorPayload({')
    expect(commentComposer).toContain('saveCommentEdit(editingComment.value, payload)')
    const commentDrawer = sourceFile('../../app/components/forum/SFTopicCommentComposerDrawer.vue')
    expect(commentDrawer).toContain(':initial-content="initialContent"')
    expect(commentDrawer).toContain('<USlideover')
  })

  test('uses a full-width three-column shell with left nav and topic side card', () => {
    const source = topicPage()
    const themeTopicTpl = sourceFile('../../../../extensions/builtin/themes/sforum-default/templates/topic-show.html')

    expect(source).toContain('data-layout="fullwidth-3col"')
    expect(source).toContain('sforum-topic-page__layout')
    expect(source).toContain('sforum-topic-page__layout--with-side')
    expect(source).toContain('sforum-topic-page__sidebar')
    expect(source).toContain('<SFHomeNavigation')
    expect(source).toContain('navigation-mode="route"')
    expect(source).toContain('sforum-topic-page__shell')
    expect(source).toContain('sforum-topic-page__reading')
    expect(source).toContain('sforum-topic-page__post-card')
    expect(source).toContain('sforum-topic-page__actions')
    expect(source).toContain('<SFTopicSideCard')
    expect(source).toContain(':first-comment-id="comments[0]?.id"')
    expect(source).toContain(':latest-comment-id="comments[comments.length - 1]?.id"')
    // 侧栏贡献者：作者+编辑 avatar group + 公开时间线 modal
    const sideCard = sourceFile('../../app/components/forum/SFTopicSideCard.vue')
    expect(sideCard).toContain("t('topicDetail.side.contributors')")
    expect(sideCard).toContain('<SFAvatarGroup')
    expect(sideCard).toContain('<SFTopicContributorsModal')
    expect(sideCard).not.toContain("t('topicDetail.side.participants')")
    expect(sourceFile('../../app/composables/forum/useForumApi.ts')).toContain('listTopicContributionTimeline')
    expect(sourceFile('../../app/utils/forum/forumTaxonomy.ts')).toContain('ForumTopicContributionTimeline')
    expect(source).toContain('sf-comment-stream-controls__latest')
    const replyComposer = sourceFile('../../app/components/forum/SFTopicReplyComposer.vue')
    expect(source).toContain('<SFTopicReplyComposer')
    expect(replyComposer).toContain('<LazySFEditor')
    expect(replyComposer).toContain(' compact')
    expect(replyComposer).toContain('@submit="submitReply"')
    expect(replyComposer).not.toContain('sforum-topic-comments__reply-launcher')
    expect(replyComposer).toContain("emit('open', replyMarkdown)")
    // 简易评论留在页面内；高级回复、回复评论和编辑评论共用完整编辑抽屉。
    expect(replyComposer).toContain("t('topicDetail.advancedReply')")
    expect(replyComposer).toContain('sforum-topic-comments__reply-advanced')
    expect(source).toContain('<SFTopicCommentComposerDrawer')
    expect(source).not.toContain('forumTopicAdvancedReplyPath')
    expect(source).not.toContain(':advanced-to="advancedReplyTo"')
    expect(sourceFile('../../app/pages/topics/reply.vue')).toContain('forum.topic.reply')
    const advancedReplyPage = sourceFile('../../app/components/forum/SFTopicReplyPage.vue')
    expect(advancedReplyPage).toContain("compose: 'advanced'")
    expect(advancedReplyPage).toContain('await navigateTo({')
    const commentDrawer = sourceFile('../../app/components/forum/SFTopicCommentComposerDrawer.vue')
    expect(commentDrawer).toContain('<LazySFEditor')
    expect(commentDrawer).toContain('side="bottom"')
    expect(commentDrawer).toContain('sf-topic-composer-drawer__resize')
    expect(commentDrawer).toContain("window.addEventListener('pointermove', onResizePointerMove)")
    expect(commentDrawer).toContain(':rows="6"')
    expect(commentDrawer).toContain('flex: 1 0 268px')
    expect(commentDrawer).toContain('flex-basis: 328px')
    expect(commentDrawer).toContain('grid-template-rows: auto minmax(180px, 1fr) auto')
    expect(commentDrawer).toContain('.sf-topic-composer-drawer .sf-editor__content,')
    expect(commentDrawer).toContain(':submit-visible="false"')
    expect(commentDrawer).toContain("props.mode !== 'edit' || dirty.value")
    expect(commentDrawer).toContain(':aria-disabled="mode === \'edit\' && !canSubmit ? \'true\' : undefined"')
    expect(commentDrawer).toContain("t('composer.editValidation.noChanges')")
    expect(commentDrawer).toContain("t('topicDetail.composerDrawer.editReasonRequired')")
    expect(commentDrawer).not.toContain(' compact')
    expect(sourceFile('../../app/components/SFEditor.vue')).toContain('sf-editor--compact')
    expect(sourceFile('../../app/components/SFEditor.vue')).toContain("emit('cancel')")
    expect(source).toContain('showTopicSide')
    expect(source).toContain('shareTopic')
    expect(source).toContain('listCategoryGroups')
    expect(source).toContain('lazy: true')
    expect(themeTopicTpl).toContain('data-layout="fullwidth-3col"')
    expect(themeTopicTpl).toContain('sf-theme-shell--fullwidth-3col')
    expect(source).not.toContain('SFTopicProgressRail')
    expect(source).not.toContain('sforum-topic-page__action-rail')
    expect(source).not.toContain('statsTitle')
    // 锚点反查 + 深链高亮后页面略增；硬警告抬到约 1500，再涨应拆文件。
    expect(source.split('\n').length).toBeLessThan(1500)
  })

  test('keeps mutation errors persistent and fixes the default discussion to a flat stream', () => {
    const source = topicPage()
    const commentComposer = sourceFile('../../app/composables/forum/useTopicCommentComposerDrawer.ts')

    expect(source).not.toContain('watch(showActionError')
    expect(source).toContain(':presentation="commentView"')
    expect(source).toContain(':depth="0"')
    expect(source).toContain(':collapse-from-depth="2"')
    expect(source).toContain(':floor-label="commentFloor(comment)"')
    expect(source).toContain('commentFloorLabelsById')
    expect(source).not.toContain('#{{ replyingTo.id }}')
    expect(source).toContain("const commentView = ref<'flat'>('flat')")
    expect(source).not.toContain('<SFCommentStreamControls')
    const commentRequest = source.slice(source.indexOf('const commentQuery'), source.indexOf('function emptyCommentList'))
    expect(commentRequest).not.toContain('perPage')
    expect(source).toContain('commentData.value.perPage')
    expect(commentComposer).toContain('replyParentId.value')
    expect(source).toContain('copyCommentLink')
  })

  // 评论流是时间流：新回复落在列表末尾，页面必须把视口带到新评论（必要时先跳到所在页），
  // 否则用户在原来的阅读位置看不到自己的回复。
  test('lands on the created comment because the flat stream is chronological', () => {
    const source = topicPage()
    const anchor = sourceFile('../../app/composables/forum/useTopicCommentAnchor.ts')
    const submission = sourceFile('../../app/composables/forum/useTopicCommentSubmission.ts')
    const drawer = sourceFile('../../app/composables/forum/useTopicCommentComposerDrawer.ts')
    const quickReply = sourceFile('../../app/components/forum/SFTopicReplyComposer.vue')

    // 页面声明时间流契约并把锚点/定位职责交给 composable。
    expect(source).toContain('created_at ASC, id ASC')
    expect(source).toContain('useTopicCommentAnchor({')
    expect(source).toContain('focusCreatedComment')
    expect(source).toContain(':focus-created-comment="focusCreatedComment"')
    expect(source).toContain(':flash="flashCommentId === comment.id"')
    // 定位实现：按服务端反查的页码跳页 + #comment-<id> 锚点滚动高亮。
    expect(anchor).toContain('async function focusCreatedComment(comment: ForumComment)')
    expect(anchor).toContain('await forumApi.resolveCommentPage(id, comment.id)')
    expect(anchor).toContain('hash: `#comment-${comment.id}`')
    // 同页深链兜底：目标评论应在当前页却不在旧列表（新回复未刷新）时，
    // 页码不变不会触发重取，必须强制刷新一次再让 watch 完成滚动/高亮。
    expect(anchor).toContain('refreshComments: () => Promise<unknown>')
    expect(anchor).toContain('await options.refreshComments()')
    expect(source).toContain('refreshComments,')
    // 定位只在发布成功后发生：待审回复不在列表里，不能跳。
    expect(submission).toContain('await options.focusCreatedComment?.(created)')
    const pendingStart = submission.indexOf("created.status === 'pending'")
    const pendingBranch = submission.slice(
      pendingStart,
      submission.indexOf('} else {', pendingStart)
    )
    expect(pendingBranch).not.toContain('focusCreatedComment')
    expect(pendingBranch).not.toContain('refreshComments')
    // 高级回复抽屉与快速回复共用同一实现。
    expect(drawer).toContain('focusCreatedComment: options.focusCreatedComment')
    expect(quickReply).toContain('focusCreatedComment: comment => props.focusCreatedComment?.(comment)')
  })

  test('uses API edit marks and the complete author lock policy', () => {
    const source = topicPage()
    const heading = themeFile('app/components/forum/SFTopicHeading.vue')
    const contentTime = sourceFile('../../app/composables/forum/useForumContentTime.ts')
    const commentMeta = contentTime.slice(contentTime.indexOf('function commentMeta'), contentTime.indexOf('return { publishedTime'))

    expect(source).toContain('useForumContentTime')
    expect(commentMeta).toContain('comment.edited && comment.editedAt')
    expect(commentMeta).toContain('updatedTime(comment.editedAt)')
    expect(heading).toContain('v-if="topic.edited"')
    expect(heading).toContain('topic.editedAt && updatedLabel')
    expect(source).toContain("webOption('forum.topics.allow_author_close_replies', 'enabled')")
    expect(source).toContain('can(FORUM_PERMISSIONS.topicEditOwn)')
    expect(source).toContain('topic.value?.authorUserId === reportUser.value?.id')
    expect(source).toContain('<SFContentColumnFooter')
  })

  test('registers the full-width three-column topic stylesheet', () => {
    const config = themeFile('nuxt.config.ts')
    const css = themeFile('app/assets/css/sforum-topic.css')
    const themePkgCss = sourceFile('../../../../extensions/builtin/themes/sforum-default/assets/theme.css')

    expect(config).toContain('sforum-topic.css')
    expect(css).toContain('.sforum-topic-page__layout--with-side')
    expect(css).toContain('var(--sf-public-sidebar-width)')
    expect(css).toContain('var(--sf-public-right-rail-width)')
    expect(css).toContain('align-items: stretch')
    expect(css).toContain('.sforum-topic-page__sidebar')
    expect(css).toContain('.sforum-topic-page__post-card')
    // 三栏左右内缩只由宿主壳 / 主题壳的 --sf-public-edge-inset 单一来源决定，
    // 页面级 CSS 不得再硬编码 18px，否则 Core/Host chrome 回退路径会与顶栏轨道错位。
    expect(css).not.toContain('padding: 0 18px;')
    expect(css).toContain('border-right: 1px solid var(--sf-public-border)')
    expect(css).toContain('border-left: 1px solid var(--sf-public-border)')
    expect(css).toContain('.sforum-topic-page .sf-topic-heading__title')
    // 标题行不得为操作菜单预留整列，否则 byline 分隔线比正文卡片短一截、右上角留空白缺口。
    expect(css).toContain('.sforum-topic-page .sforum-topic-page__heading-row {\n  position: relative;\n  display: block;\n}')
    expect(css).toContain('.sforum-topic-page .sforum-topic-page__heading-row > .sf-topic-action-menu {\n  position: absolute;')
    expect(css).toContain('.sf-topic-side-card')
    expect(css).toContain('background: var(--sf-public-surface)')
    expect(css).toContain('overflow-wrap: anywhere')
    expect(css).toContain('.sforum-topic-page__main {\n    position: sticky;\n    top: var(--sf-public-topbar-height);\n    height: calc(100vh - var(--sf-public-topbar-height));\n    min-height: 0;\n    overflow-y: auto;')
    expect(css).toContain('.sforum-topic-page__sidebar,\n  .sforum-topic-page__side {\n    position: sticky;\n    top: var(--sf-public-topbar-height);\n    height: calc(100vh - var(--sf-public-topbar-height));\n    min-height: 0;\n    overflow-y: auto;')
    expect(css).not.toContain('.sforum-topic-page__side {\n    position: relative;')
    // 列线由栏自身 1px 边框承担；旧的 ±12px 伪元素线在宿主壳路径被关闭。
    expect(css).toContain('.sforum-topic-page__sidebar::after')
    expect(css).toContain('content: none')
    expect(css).not.toContain('right: -12px')
    expect(css).not.toContain('left: -12px')
    // 皮肤链可能被重挂到 head 末尾（管理端↔公开端往返），列线必须赢在特异性上。
    expect(css).toContain('.sforum-topic-page .sforum-topic-page__sidebar {\n  border-right: 1px solid var(--sf-public-border);')
    expect(css).toContain('.sforum-topic-page .sforum-topic-page__side {\n  border-left: 1px solid var(--sf-public-border);')
    expect(css).toContain('@media (max-width: 1180px)')
    expect(css).toContain('@media (max-width: 960px)')
    expect(themePkgCss).toContain('.sforum-topic-page__layout--with-side')
    expect(themePkgCss).toContain('align-items: stretch')
    expect(themePkgCss).toContain('.sforum-topic-page__reading {\n  width: 100%;\n  max-width: var(--sf-public-reading-max, 48rem);\n  margin-inline: auto;')
    // 主题包不得再与宿主争夺列线：清零边框 + ±12px 伪元素线都已被移除。
    expect(themePkgCss).not.toContain('.sforum-topic-page__sidebar::after')
    expect(themePkgCss).not.toContain('.sforum-topic-page__side::before')
    expect(themePkgCss).not.toContain('.sforum-topic-page__sidebar {\n  border-right: 0;')
    expect(themePkgCss).not.toContain('.sforum-topic-page__side {\n  min-width: 0;\n  padding: 12px 12px 24px;\n  border-left: 0;')
    expect(themePkgCss).toContain('fullwidth-3col')
    expect(css).not.toContain('.sforum-topic-page__action-rail')
    expect(css).not.toContain('.sforum-topic-page__summary')
  })
})
