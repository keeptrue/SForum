import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

const source = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('unified avatar rendering contract', () => {
  test('public and admin user chrome render through SFAvatar', () => {
    const navbar = source('../../app/components/SFNavbar.vue')
    const adminLayout = source('../../app/layouts/admin.vue')

    expect(navbar).toContain('<SFAvatar')
    expect(navbar).toContain(':avatar="user.avatar"')
    expect(navbar).not.toContain('navbar__avatar">{{ avatarLetter }}')

    expect(adminLayout).toContain('<SFAvatar')
    expect(adminLayout).toContain(':avatar="user?.avatar"')
    expect(adminLayout).not.toContain('<UAvatar')
  })

  test('forum surfaces that show avatars pass AvatarView into SFAvatar', () => {
    const avatar = source('../../app/components/SFAvatar.vue')
    const homepageRow = source('../../app/components/forum/SFHomeTopicRow.vue')
    const topicPage = source('../../app/components/forum/SFTopicShowPage.vue')
    const topicHeading = source('../../app/components/forum/SFTopicHeading.vue')
    const feedRow = source('../../app/components/forum/SFFeedRow.vue')
    const comment = source('../../app/components/forum/SFComment.vue')

    expect(homepageRow).toContain('<SFAvatar')
    expect(homepageRow).toContain(':avatar="topic.author?.avatar"')
    expect(homepageRow).toContain('size="list"')
    // 列表不得绕过 AvatarView（禁止 prefer-initials 强制字头、局部改尺寸）
    expect(homepageRow).not.toContain('prefer-initials')
    expect(homepageRow).not.toContain('preferInitials')
    expect(homepageRow).not.toContain('!size-9')
    expect(homepageRow).not.toContain('topic.excerpt')
    expect(homepageRow).not.toContain('participants')
    // 尺寸/字头色板集中在全局 SFAvatar；URL 策略由后端 AvatarView 决定
    expect(avatar).toContain("'list'")
    expect(avatar).toContain('forumAvatarToneClass')
    expect(avatar).toContain('isRemoteImage')
    expect(avatar).toContain("props.avatar?.kind === 'uploaded' || isRemoteImage.value")
    expect(avatar).toContain('showImage && imageSrc && bypassImageOptimization')
    expect(avatar).toContain('Boolean(imageSrc.value) && !imageFailed.value')
    expect(avatar).toContain('上传头像由后端预处理')
    expect(avatar).not.toContain('new Image()')
    expect(avatar).not.toContain('import.meta.client')
    expect(avatar).toContain('props.alt ?? props.avatar?.alt ?? props.name')
    expect(avatar).toContain(":aria-hidden=\"isDecorative ? 'true' : undefined\"")
    expect(avatar).not.toContain('preferInitials')
    expect(avatar).not.toContain('identicon')
    // 详情页 byline 不得用 text ellipsis 误伤 .sf-avatar
    const topicCss = source('../../app/assets/css/sforum-topic.css')
    expect(topicCss).toContain('.sf-topic-heading__author > span:not(.sf-avatar)')
    expect(topicCss).not.toMatch(/\.sf-topic-heading__author > span\s*\{/)
    // 详情页标题 / 评论 / 侧卡同样只走 SFAvatar + AvatarView
    const authorBlock = source('../../app/components/forum/SFTopicAuthorBlock.vue')
    expect(topicHeading).toContain('<SFTopicAuthorBlock')
    expect(authorBlock).toContain('<SFAvatar')
    expect(authorBlock).toContain(':avatar="author?.avatar"')
    expect(topicPage).toContain(':avatar="comment.author?.avatar"')
    expect(topicPage).toContain('<SFTopicSideCard')
    const sideCard = source('../../app/components/forum/SFTopicSideCard.vue')
    const avatarGroup = source('../../app/components/SFAvatarGroup.vue')
    expect(sideCard).toContain('<SFAvatarGroup')
    expect(avatarGroup).toContain('<SFAvatar')
    expect(avatarGroup).toContain('avatar?: AvatarView | null')
    expect(feedRow).toContain('avatar?: AvatarView | null')
    expect(feedRow).toContain(':avatar="avatar"')
    expect(comment).toContain('<SFAvatar')
    expect(comment).toContain('avatar?: AvatarView | null')
    expect(comment).toContain(':avatar="avatar"')
    expect(comment).toContain(':avatar="child.author?.avatar"')
  })
})
