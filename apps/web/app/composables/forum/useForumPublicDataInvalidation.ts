const FORUM_HOME_STATE_KEYS = [
  'forum-home-loaded-topics',
  'forum-home-topic-total',
  'forum-home-loaded-feed-key',
  'forum-home-rendered-at'
]

export function invalidateForumPublicData() {
  clearNuxtData(key => key.startsWith('forum-home-topics:')
    // 分类目录只剩公开面那两条稳定 key（RV-35）：前缀一次覆盖「内参 / 公开读」两个变体。
    || key.startsWith('forum-category-groups')
    || key === 'forum-home-tags')
  for (const key of FORUM_HOME_STATE_KEYS) {
    clearNuxtState(key)
  }
}
