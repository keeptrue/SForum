import type { Ref } from 'vue'
import type { ForumUserSummary } from '~/utils/forum/forumTaxonomy'
import { forumReactionApplyToTopic } from '~/utils/forum/forumReaction'
import type { BookmarkState } from '~/composables/forum/useBookmark'

type ReactableTopic = {
  likeCount: number
  likedByMe?: boolean
  likedBy?: ForumUserSummary[]
  bookmarkedByMe?: boolean
}

/**
 * 主题级点赞状态：控件 settle 后把权威值写回页面持有的 topic。
 *
 * 不写回的话，页面任何一次按 props 重渲染都会把计数与选中态弹回服务端初值。
 */
export function useTopicReaction(topic: Ref<ReactableTopic | null>) {
  function applyReactionChange(liked: boolean, count: number, likedBy?: ForumUserSummary[]) {
    if (topic.value) {
      forumReactionApplyToTopic(topic.value, liked, count, likedBy)
    }
  }

  /** 收藏控件 settle 后回写主题：父层重渲染不能把书签态弹回服务端初值。 */
  function applyBookmarkChange(state: BookmarkState) {
    if (topic.value) {
      topic.value.bookmarkedByMe = state.bookmarked
    }
  }

  return { applyReactionChange, applyBookmarkChange }
}
