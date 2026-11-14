import type { ForumComment } from './forumTaxonomy'

/**
 * 评论树的纯函数：树视图截断后，用 ListCommentReplies 拉到的直系回复
 * 合并回本地树（保留本地已有的深层节点，因为 path 序可能被 cap 截断过）。
 *
 * 只有结构计算，没有副作用，因此放在 utils 里被页面直接调用。
 */
export function mergeCommentReplies(
  items: ForumComment[],
  parentId: number,
  replies: ForumComment[]
): ForumComment[] {
  return items.map((item) => {
    if (item.id === parentId) {
      const existing = new Map((item.children || []).map(child => [child.id, child]))
      const merged: ForumComment[] = []
      for (const reply of replies) {
        const prev = existing.get(reply.id)
        merged.push(prev ? { ...reply, children: prev.children, hasMoreChildren: prev.hasMoreChildren } : reply)
        existing.delete(reply.id)
      }
      // 保留 API 未返回、但本地已有的深层节点（path 序可能被 cap 截断过）。
      for (const leftover of existing.values()) {
        merged.push(leftover)
      }
      return { ...item, children: merged, hasMoreChildren: false }
    }
    if (item.children?.length) {
      return { ...item, children: mergeCommentReplies(item.children, parentId, replies) }
    }
    return item
  })
}
