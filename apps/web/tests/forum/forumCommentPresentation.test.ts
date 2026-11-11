import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'

import {
  commentBranchPresentation,
  countCommentDescendants
} from '../../app/utils/forum/forumCommentPresentation'

type CommentNode = {
  children?: CommentNode[]
}

const deepChildren: CommentNode[] = [
  {
    children: [
      { children: [{ children: [] }] }
    ]
  }
]

const commentComponent = () => readFileSync(
  new URL('../../app/components/forum/SFComment.vue', import.meta.url),
  'utf8'
)
// 作者身份行（昵称 + 楼主标 + 徽章 chip）已抽成独立组件。
const commentAuthorRow = () => readFileSync(
  new URL('../../app/components/forum/SFCommentAuthorRow.vue', import.meta.url),
  'utf8'
)
const commentCss = () => readFileSync(
  new URL('../../app/assets/css/sforum-comment.css', import.meta.url),
  'utf8'
)
const userPreviewComponent = () => readFileSync(
  new URL('../../app/components/forum/SFCommentUserPreview.vue', import.meta.url),
  'utf8'
)
const componentCss = () => readFileSync(
  new URL('../../app/assets/css/sforum-components.css', import.meta.url),
  'utf8'
)

describe('forum comment presentation', () => {
  test('counts every comment in a descendant branch', () => {
    expect(countCommentDescendants(deepChildren)).toBe(3)
    expect(countCommentDescendants([])).toBe(0)
  })

  test('keeps the root branch open with one semantic connection rail', () => {
    expect(commentBranchPresentation('tree', 0, deepChildren)).toMatchObject({
      connectionRail: true,
      collapsible: false,
      followUpCount: 3,
      indentation: 0
    })
  })

  test('collapses depth-two descendants without adding more indentation', () => {
    expect(commentBranchPresentation('tree', 1, deepChildren)).toMatchObject({
      connectionRail: false,
      collapsible: true,
      followUpCount: 3,
      indentation: 1
    })
    expect(commentBranchPresentation('tree', 2, deepChildren).collapsible).toBe(false)
    expect(commentBranchPresentation('tree', 7, deepChildren).indentation).toBe(1)
    expect(commentBranchPresentation('tree', 7, deepChildren).collapsible).toBe(false)
  })

  test('honors a custom collapse depth', () => {
    expect(commentBranchPresentation('tree', 1, deepChildren, 3).collapsible).toBe(false)
    expect(commentBranchPresentation('tree', 2, deepChildren, 3).collapsible).toBe(true)
    expect(commentBranchPresentation('tree', 3, deepChildren, 3).collapsible).toBe(false)
  })

  test('flat presentation never indents, connects, or recurses', () => {
    expect(commentBranchPresentation('flat', 7, deepChildren)).toMatchObject({
      connectionRail: false,
      collapsible: false,
      followUpCount: 3,
      indentation: 0
    })
  })
})

describe('SFComment presentation contract', () => {
  test('declares and forwards explicit recursive presentation props', () => {
    const source = commentComponent()

    expect(source).toContain('presentation?: CommentPresentationMode')
    expect(source).toContain('depth?: number')
    expect(source).toContain('collapseFromDepth?: number')
    expect(source).toContain('floorLabel?: string')
    expect(source).toContain("presentation: 'flat'")
    expect(source).toContain('depth: 0')
    expect(source).toContain('collapseFromDepth: 2')
    expect(source).toContain(':presentation="presentation"')
    expect(source).toContain(':depth="depth + 1"')
    expect(source).toContain(':collapse-from-depth="collapseFromDepth"')
    expect(source).toContain(':comment-meta-builder="commentMetaBuilder"')
    expect(source).toContain(':comment-author-link-builder="commentAuthorLinkBuilder"')
    expect(source).toContain(':comment-actions-builder="commentActionsBuilder"')
    expect(source).toContain('@action-comment="forwardChildAction"')
    expect(source).toContain("emit('actionComment', comment, value)")
  })

  test('uses an accessible disclosure backed by the descendant count', () => {
    const source = commentComponent()

    expect(source).toContain('branchPresentation.followUpCount')
    expect(source).toContain(':aria-expanded="branchExpanded"')
    expect(source).toContain(':aria-controls="branchId"')
    expect(source).toContain(':id="branchId"')
  })

  test('links reply context to its source and keeps child branches outside the entry', () => {
    const source = commentComponent()
    const entryClose = source.indexOf('</article>')
    const branchStart = source.indexOf('class="sf-comment__branch"')
    const replyContextTag = source.match(/<a[\s\S]*?class="sf-comment__reply-to"[\s\S]*?>/)?.[0]

    expect(source).toContain("t('topicDetail.reply')")
    expect(replyContextTag).toBeDefined()
    expect(replyContextTag).toContain('#comment-')
    expect(source).toContain('class="sf-comment__floor"')
    expect(source).toContain('displayFloorLabel')
    expect(source).not.toContain('#{{ comment.id }}')
    expect(entryClose).toBeGreaterThan(-1)
    expect(branchStart).toBeGreaterThan(entryClose)
    expect(source).not.toContain('sf-comment__children')
  })

  test('does not recurse in flat presentation', () => {
    const source = commentComponent()

    expect(source).toContain("presentation === 'tree'")
    expect(source).toContain('branchPresentation.collapsible')
  })

  test('renders a deletion notice instead of an empty comment body', () => {
    const source = commentComponent()
    const styles = commentCss()

    expect(source).toContain("props.comment?.status === 'deleted'")
    expect(source).toContain('v-if="isDeletedComment"')
    expect(source).toContain("t('topicDetail.commentDeletedNotice')")
    expect(source).toContain("t('topicDetail.commentDeletedDescription')")
    expect(source).toContain('v-else-if="showHtml"')
    expect(styles).toContain('.sf-comment__deleted')
    expect(styles).toContain('border: 1px dashed var(--sf-public-border, var(--sf-border))')
    expect(styles).toContain('.sf-comment__deleted-copy strong')
  })

  test('opens an anchored public-profile preview before navigating', () => {
    const comment = commentComponent()
    const preview = userPreviewComponent()
    const styles = commentCss()

    expect(comment).toContain('canPreviewUser')
    expect(comment).toContain('toggleUserPreview')
    expect(comment).toContain("document.addEventListener('pointerdown', onDocumentPointerDown)")
    expect(comment).toContain("event.key === 'Escape'")
    expect(comment).toContain('<SFCommentUserPreview')
    expect(preview).toContain('profileApi.getPublicProfile(previewUsername.value)')
    expect(preview).toContain("useState<Record<string, PublicProfile>>('profile:comment-user-preview-cache'")
    expect(preview).toContain(':to="profilePath"')
    expect(preview).not.toContain('follow')
    expect(preview).not.toContain('message')
    expect(styles).toContain('.sf-comment__user-preview-layer')
    expect(styles).toMatch(/\.sf-comment \{[\s\S]*?position: relative/)
    expect(styles).toContain('position: absolute')
    expect(styles).toContain('width: min(340px, calc(100% - 4px))')
  })
})

describe('SFComment responsive CSS contract', () => {
  test('renders top-level replies as open chronological rows', () => {
    const source = commentCss()
    const flatOverride = source.slice(source.indexOf('Default flat discussion stream'))

    expect(flatOverride).toContain('.sf-comment-list > .sf-comment')
    expect(flatOverride).toContain('border-radius: 0')
    expect(flatOverride).toContain('background: transparent')
    expect(flatOverride).toContain('box-shadow: none')
  })

  test('loads a focused comment stylesheet with one branch rail', () => {
    const source = commentCss()

    expect(componentCss()).toContain("@import './sforum-comment.css';")
    expect(source).toContain('.sf-comment__entry')
    expect(source).toContain('.sf-comment__branch--connected')
    expect(source).toContain('border-left: 1px solid var(--sf-comment-branch)')
    expect(source).not.toContain('.sf-comment__children')
  })

  test('contains rich comment content without making the page wider', () => {
    const source = commentCss()

    expect(source).toContain('overflow-wrap: anywhere')
    expect(source).toContain('.sf-comment__content pre')
    expect(source).toContain('overflow-x: auto')
    expect(source).toContain('.sf-comment__content img')
    expect(source).toContain('max-width: 100%')
  })

  test('flattens every visual depth and keeps touch actions usable on mobile', () => {
    const source = commentCss()
    const mobileStart = source.indexOf('@media (max-width: 640px)')
    const mobileSource = source.slice(mobileStart)

    expect(mobileStart).toBeGreaterThan(-1)
    expect(mobileSource).toContain('.sf-comment__branch--connected')
    expect(mobileSource).toContain('margin-left: 0')
    expect(mobileSource).toContain('padding-left: 0')
    expect(mobileSource).toContain('border-left: 0')
    // 移动端操作区触达高度：34px（紧凑信息流操作行；高于 WCAG 2.2 最小 24px）。
    // 原断言写的是 40px，随移动端操作区收紧为 34px 同步更新。
    expect(mobileSource).toContain('min-height: 34px')
  })

  test('keeps reply source links visually restrained', () => {
    const source = commentCss()
    const replyRule = source.match(/\.sf-comment__reply-to\s*\{([^}]*)\}/)?.[1]

    expect(replyRule).toBeDefined()
    expect(replyRule).not.toContain('cursor: pointer')
  })

  test('highlights deep-linked target comments for a few seconds', () => {
    const source = commentCss()
    const comment = commentComponent()
    const topic = readFileSync(new URL('../../app/components/forum/SFTopicShowPage.vue', import.meta.url), 'utf8')
    const anchor = readFileSync(new URL('../../app/composables/forum/useTopicCommentAnchor.ts', import.meta.url), 'utf8')

    expect(source).toContain('.sf-comment-list > .sf-comment:target')
    expect(source).toContain('.sf-comment-list > .sf-comment.sf-comment--flash')
    expect(source).toContain('sf-comment-target-glow')
    expect(source).toContain('prefers-reduced-motion: reduce')
    expect(comment).toContain("'sf-comment--flash': flash")
    expect(topic).toContain(':flash="flashCommentId === comment.id"')
    expect(anchor).toContain('flashTargetComment')
    expect(anchor).toContain('COMMENT_FLASH_MS')
  })

  test('never clips the author preview card from the deep-link highlight', () => {
    const source = commentCss()
    const targetRule = source.match(
      /\.sf-comment-list > \.sf-comment:target,\s*\.sf-comment-list > \.sf-comment\.sf-comment--flash \{([^}]*)\}/
    )?.[1]

    expect(targetRule).toBeDefined()
    // 扫光改为盒内 background-position 动画后，定位评论不再需要裁剪：
    // 一旦恢复 overflow: hidden，评论体内的 @作者资料卡又会在评论下沿被切断
    // （:target 会随 hash 长期生效，不是只闪 3.2s）。
    expect(targetRule).not.toContain('overflow')
    expect(source).toContain('background-position: -66.7% 0')
    expect(source).toContain('background-size: 40% 100%')
  })
})

describe('SFComment identity layout contract', () => {
  test('stacks @username under the display name', () => {
    const source = commentAuthorRow()
    const styles = commentCss()
    const identityTag = source.match(/<SFUserIdentityName\b[\s\S]*?>/)?.[0]

    expect(identityTag).toBeDefined()
    expect(identityTag).not.toContain('inline')
    // 双行身份常驻：身份组件的窄屏隐藏规则只服务卡片场景，评论区要显式放开。
    expect(styles).toContain('.sf-comment__header .sf-comment__author .sf-user-identity-name__handle')
    // 楼主徽标与时间/楼层跟随昵称基线，不落到 @username 行。
    expect(styles).toMatch(/\.sf-comment__header\s*\{[^}]*align-items: baseline/)
  })

  test('keeps the OP badge on the name line instead of after the wider handle', () => {
    const source = commentAuthorRow()

    // 徽标经 name-suffix 插槽渲染在昵称行；否则它会被更长的 @username 顶到远处。
    expect(source).toContain('#name-suffix')
    expect(source).toContain('props.isOpAuthor && !props.interactive')
  })

  test('left aligns the two-line identity inside the trigger button', () => {
    const authorRule = commentCss().match(/\.sf-comment__author\s*\{([^}]*)\}/)?.[1]

    // 触发按钮的 UA 默认 text-align:center 会把较短的名字居中在较长的 @username 上方。
    expect(authorRule).toBeDefined()
    expect(authorRule).toContain('text-align: start')
  })
})
