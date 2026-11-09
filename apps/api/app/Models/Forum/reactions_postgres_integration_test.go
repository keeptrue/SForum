package forum

import (
	"context"
	"sync"
	"testing"
	"time"
)

// reactionsPGFixture 在 revision ledger fixture 之上补齐点赞切片的 schema。
//
// 复用它的三件事：独立 schema + search_path 隔离、insertUser / insertBareTopic
// helper、以及「base schema + 迁移 202607220052」这条既有基线。base schema 里
// 没有 content_reactions 与 like_count，所以这里再叠一次与真实迁移同形的 DDL。
type reactionsPGFixture struct {
	*revisionLedgerPGFixture
}

func newReactionsPGFixture(t *testing.T) *reactionsPGFixture {
	t.Helper()
	fixture := &reactionsPGFixture{revisionLedgerPGFixture: newRevisionLedgerPGFixture(t)}
	if _, err := fixture.pool.Exec(fixture.ctx, reactionsFixtureSchemaSQL); err != nil {
		t.Fatalf("apply reactions fixture DDL: %v", err)
	}
	return fixture
}

// reactionsFixtureSchemaSQL 是 database/migrations/202610280001_content_reactions.sql
// 的 Up 段最小镜像：content_reactions 表 + 三个 CHECK + 唯一约束 + 两个索引，
// topics/comments 的 like_count 列与非负约束（NOT VALID + VALIDATE 的写法一致）。
//
// 刻意不复制该迁移的稳定读视图 sforum_core_v1.forum_reactions 与通知描述符种子：
// 前者依赖 sforum_core_v1 schema 与 topics/comments 的 deleted_at 可见性谓词，属于
// 插件数据面的另一个切片；后者由 database/migrations 的测试固定。迁移结构本身
// （约束名、列类型、索引列序）由 database/migrations 测试守护，这里只保证本包
// 隔离 schema 里的形状与之同形，改动迁移时必须同步这里。
//
// comments.deleted_at 来自 202607060003_forum_topics_comments_posts.sql 的规范建表
// 语句；fixture base schema 没建它，而 store 的评论软删路径会写这一列。
const reactionsFixtureSchemaSQL = `
CREATE TABLE content_reactions (
  id BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL,
  target_id BIGINT NOT NULL,
  reaction TEXT NOT NULL DEFAULT 'like',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT content_reactions_target_type_check CHECK (target_type IN ('topic', 'comment')),
  CONSTRAINT content_reactions_reaction_check CHECK (reaction IN ('like')),
  CONSTRAINT content_reactions_target_positive CHECK (target_id > 0),
  CONSTRAINT content_reactions_unique UNIQUE (user_id, target_type, target_id, reaction)
);

CREATE INDEX content_reactions_roster_idx
  ON content_reactions (target_type, target_id, reaction, created_at DESC, id DESC);

CREATE INDEX content_reactions_user_idx
  ON content_reactions (user_id, created_at DESC);

ALTER TABLE topics ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE comments ADD COLUMN IF NOT EXISTS like_count BIGINT NOT NULL DEFAULT 0;

ALTER TABLE topics ADD CONSTRAINT topics_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE comments ADD CONSTRAINT comments_like_count_nonnegative CHECK (like_count >= 0) NOT VALID;
ALTER TABLE topics VALIDATE CONSTRAINT topics_like_count_nonnegative;
ALTER TABLE comments VALIDATE CONSTRAINT comments_like_count_nonnegative;

ALTER TABLE comments ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;
`

// TestPostgresApplyReactionConcurrentLikesCollapseToOneRow 是点赞的唯一性口径：
// 同一用户对同一主题并发点赞只能留下一条关系行，计数列只能 +1。
//
// 允许的中间态只有「已经是 1」：任一 goroutine 返回 >1 就说明计数与关系行不在
// 同一个事务口径里（唯一约束赢了、计数加了两次）。取消点赞对称验证。
func TestPostgresApplyReactionConcurrentLikesCollapseToOneRow(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "concurrent_like_author")
	likerID := fixture.insertUser(t, "concurrent_liker")
	topic := fixture.insertBareTopic(t, authorID, "concurrent-like-target")

	const workers = 10

	states := make([]ReactionState, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			states[index], errs[index] = store.ApplyReaction(context.Background(), ApplyReactionRecord{
				ActorUserID:     likerID,
				TargetType:      ReactionTargetTopic,
				TargetID:        topic.id,
				TopicID:         topic.id,
				RecipientUserID: authorID,
				Reaction:        ReactionLike,
				Add:             true,
			})
		}(i)
	}
	wg.Wait()

	for i := range states {
		if errs[i] != nil {
			t.Fatalf("concurrent like #%d: %v", i, errs[i])
		}
		if !states[i].Liked {
			t.Fatalf("concurrent like #%d returned liked=false", i)
		}
		if states[i].LikeCount != 1 {
			t.Fatalf("concurrent like #%d likeCount=%d, want 1 (a value above 1 means the counter drifted)", i, states[i].LikeCount)
		}
	}
	assertReactionRowCount(t, fixture, ReactionTargetTopic, topic.id, 1)
	assertTopicLikeCount(t, fixture, topic.id, 1)

	unlikeStates := make([]ReactionState, workers)
	unlikeErrs := make([]error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wg.Done()
			unlikeStates[index], unlikeErrs[index] = store.ApplyReaction(context.Background(), ApplyReactionRecord{
				ActorUserID:     likerID,
				TargetType:      ReactionTargetTopic,
				TargetID:        topic.id,
				TopicID:         topic.id,
				RecipientUserID: authorID,
				Reaction:        ReactionLike,
				Add:             false,
			})
		}(i)
	}
	wg.Wait()

	for i := range unlikeStates {
		if unlikeErrs[i] != nil {
			t.Fatalf("concurrent unlike #%d: %v", i, unlikeErrs[i])
		}
		if unlikeStates[i].Liked {
			t.Fatalf("concurrent unlike #%d returned liked=true", i)
		}
		if unlikeStates[i].LikeCount != 0 {
			t.Fatalf("concurrent unlike #%d likeCount=%d, want 0", i, unlikeStates[i].LikeCount)
		}
	}
	assertReactionRowCount(t, fixture, ReactionTargetTopic, topic.id, 0)
	assertTopicLikeCount(t, fixture, topic.id, 0)
}

// TestPostgresApplyReactionKeepsLikeCountEqualToRelationRows 是计数与关系行的
// 不变量：每一步之后 like_count 都必须等于该目标的关系行数，返回状态必须与库内
// 一致。like→like（幂等）→unlike→unlike（幂等）→like 的主题与评论各跑一遍。
func TestPostgresApplyReactionKeepsLikeCountEqualToRelationRows(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "relation_rows_author")
	likerID := fixture.insertUser(t, "relation_rows_liker")
	topic := fixture.insertBareTopic(t, authorID, "relation-rows-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "计数不变量评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// 每一步的 wantRows 同时是关系行数、计数列值、返回状态里的 likeCount。
	steps := []struct {
		name     string
		add      bool
		wantRows int64
	}{
		{"first like", true, 1},
		{"idempotent like", true, 1},
		{"first unlike", false, 0},
		{"idempotent unlike", false, 0},
		{"re-like", true, 1},
	}

	topicTarget := reactionTarget{label: "topic", targetType: ReactionTargetTopic, targetID: topic.id, topicID: topic.id}
	for _, step := range steps {
		t.Run("topic/"+step.name, func(t *testing.T) {
			assertReactionStep(t, fixture, store, likerID, topicTarget, step.add, step.wantRows)
		})
	}
	commentTarget := reactionTarget{label: "comment", targetType: ReactionTargetComment, targetID: comment.ID, topicID: topic.id}
	for _, step := range steps {
		t.Run("comment/"+step.name, func(t *testing.T) {
			assertReactionStep(t, fixture, store, likerID, commentTarget, step.add, step.wantRows)
		})
	}

	// 名单读路径与计数列必须说同一件事：行数 = like_count = roster total。
	roster, err := store.ListReactionUsers(fixture.ctx, ReactionTargetTopic, topic.id, ReactionRosterDefaultLimit, 0)
	if err != nil {
		t.Fatalf("ListReactionUsers topic: %v", err)
	}
	if roster.Total != 1 || len(roster.Items) != 1 || roster.Items[0].ID != likerID {
		t.Fatalf("topic roster = %#v, want exactly the liker", roster)
	}
	commentRoster, err := store.ListReactionUsers(fixture.ctx, ReactionTargetComment, comment.ID, ReactionRosterDefaultLimit, 0)
	if err != nil {
		t.Fatalf("ListReactionUsers comment: %v", err)
	}
	if commentRoster.Total != 1 || len(commentRoster.Items) != 1 || commentRoster.Items[0].ID != likerID {
		t.Fatalf("comment roster = %#v, want exactly the liker", commentRoster)
	}
}

// TestPostgresTopicHotScoreIncludesLikeContribution 固定口径：只有主题点赞进入
// hot_score（+3），评论点赞不改变所属主题的热度。
//
// 公式的唯一权威是 TopicHotScoreSQL：comment_count*5 + view_count + like_count*3。
// 这里每一步同时断言具体数值与公式重算值，任何一处写点漏掉 like 项都会在这里断。
func TestPostgresTopicHotScoreIncludesLikeContribution(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "hot_score_author")
	likerID := fixture.insertUser(t, "hot_score_liker")
	topic := fixture.insertBareTopic(t, authorID, "hot-score-topic")

	// 浏览量走生产写点（ApplyViewCountDeltas 同步 +delta 到 hot_score），
	// 让 view_count 这一项在公式里真实参与计算。
	if _, err := store.ApplyViewCountDeltas(fixture.ctx, map[int64]int64{topic.id: 7}); err != nil {
		t.Fatalf("ApplyViewCountDeltas: %v", err)
	}
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "热度口径评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	// 基线：1 条评论、7 次浏览、0 个赞 → 1*5 + 7 + 0*3 = 12。
	assertTopicHotScore(t, fixture, topic.id, 12)

	// 点赞：like_count=1 → 1*5 + 7 + 1*3 = 15。
	state := applyTopicReaction(t, store, likerID, topic.id, authorID, true)
	if state.LikeCount != 1 {
		t.Fatalf("topic like returned likeCount=%d, want 1", state.LikeCount)
	}
	assertTopicHotScore(t, fixture, topic.id, 15)

	// 取消：回到 12。
	state = applyTopicReaction(t, store, likerID, topic.id, authorID, false)
	if state.LikeCount != 0 {
		t.Fatalf("topic unlike returned likeCount=%d, want 0", state.LikeCount)
	}
	assertTopicHotScore(t, fixture, topic.id, 12)

	// 评论点赞不进入主题热度：点赞前后主题 hot_score 必须同值。
	before := loadTopicReactionCounters(t, fixture, topic.id).hotScore
	commentState := applyCommentReaction(t, store, likerID, comment.ID, topic.id, authorID, true)
	if commentState.LikeCount != 1 {
		t.Fatalf("comment like returned likeCount=%d, want 1", commentState.LikeCount)
	}
	after := loadTopicReactionCounters(t, fixture, topic.id).hotScore
	if after != before {
		t.Fatalf("comment like changed host topic hot_score: before=%d after=%d", before, after)
	}
	assertTopicHotScore(t, fixture, topic.id, 12)
}

// TestPostgresTopicHotScoreSurvivesCommentLifecycleAfterLike 是 P2-8 的另一半：
// 点赞贡献 +3 必须活过评论创建/删除两条重算路径（它们各自重算整条公式）。
func TestPostgresTopicHotScoreSurvivesCommentLifecycleAfterLike(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "lifecycle_hot_author")
	likerID := fixture.insertUser(t, "lifecycle_hot_liker")
	topic := fixture.insertBareTopic(t, authorID, "lifecycle-hot-topic")

	// 先点赞：0 条评论、0 次浏览 → 0*5 + 0 + 1*3 = 3。
	applyTopicReaction(t, store, likerID, topic.id, authorID, true)
	assertTopicHotScore(t, fixture, topic.id, 3)

	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "点赞后创建的评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	// +1 评论 → 1*5 + 0 + 1*3 = 8，点赞贡献仍在。
	assertTopicHotScore(t, fixture, topic.id, 8)

	if _, err := store.DeleteComment(fixture.ctx, comment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	// 评论删除回滚 -1 评论 → 0*5 + 0 + 1*3 = 3，点赞贡献仍未被抹掉。
	assertTopicHotScore(t, fixture, topic.id, 3)
	assertTopicLikeCount(t, fixture, topic.id, 1)

	// 收尾对照：取消点赞后只剩公式的零项。
	applyTopicReaction(t, store, likerID, topic.id, authorID, false)
	assertTopicHotScore(t, fixture, topic.id, 0)
}

// TestPostgresReactionRowsCascadeOnUserDelete 只固定迁移的 ON DELETE CASCADE：
// 删掉用户后该用户的关系行清零，别人的关系行不受影响。
//
// 刻意不断言 like_count 回填：生产没有硬删用户路径，计数回填不在本切片，
// 断言它会把「当前已知的空白」固化成契约。
func TestPostgresReactionRowsCascadeOnUserDelete(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "cascade_author")
	likerID := fixture.insertUser(t, "cascade_liker")
	topic := fixture.insertBareTopic(t, authorID, "cascade-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "级联删除评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	applyTopicReaction(t, store, likerID, topic.id, authorID, true)
	applyCommentReaction(t, store, likerID, comment.ID, topic.id, authorID, true)
	// 另一用户的反应行：用来证明级联只清被删用户的行。
	applyTopicReaction(t, store, authorID, topic.id, authorID, true)
	assertReactionRowCount(t, fixture, ReactionTargetTopic, topic.id, 2)
	assertReactionRowCount(t, fixture, ReactionTargetComment, comment.ID, 1)

	if _, err := fixture.pool.Exec(fixture.ctx, `DELETE FROM users WHERE id = $1`, likerID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	assertReactionRowCount(t, fixture, ReactionTargetTopic, topic.id, 1)
	assertReactionRowCount(t, fixture, ReactionTargetComment, comment.ID, 0)
	var remainingUserID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT user_id FROM content_reactions
	`).Scan(&remainingUserID); err != nil {
		t.Fatalf("load remaining reaction row: %v", err)
	}
	if remainingUserID != authorID {
		t.Fatalf("remaining reaction row user_id=%d, want author %d", remainingUserID, authorID)
	}
}

// TestPostgresReactionWriteDoesNotBumpContentUpdatedAt 是 P2-8 回归：
// 点赞/取消只动 like_count 与 hot_score，不得碰 topics.updated_at /
// comments.updated_at（它们表达内容被编辑，而不是被赞）。
func TestPostgresReactionWriteDoesNotBumpContentUpdatedAt(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "updated_at_author")
	likerID := fixture.insertUser(t, "updated_at_liker")
	topic := fixture.insertBareTopic(t, authorID, "updated-at-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "时间戳评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	topicUpdatedAt := loadTopicUpdatedAt(t, fixture, topic.id)
	commentUpdatedAt := loadCommentUpdatedAt(t, fixture, comment.ID)

	// 四步都真的写库：like_count 先 1 后 0。
	applyTopicReaction(t, store, likerID, topic.id, authorID, true)
	assertTopicLikeCount(t, fixture, topic.id, 1)
	applyTopicReaction(t, store, likerID, topic.id, authorID, false)
	assertTopicLikeCount(t, fixture, topic.id, 0)
	applyCommentReaction(t, store, likerID, comment.ID, topic.id, authorID, true)
	assertCommentLikeCount(t, fixture, comment.ID, 1)
	applyCommentReaction(t, store, likerID, comment.ID, topic.id, authorID, false)
	assertCommentLikeCount(t, fixture, comment.ID, 0)

	if after := loadTopicUpdatedAt(t, fixture, topic.id); !after.Equal(topicUpdatedAt) {
		t.Fatalf("topic updated_at moved on reaction writes: before=%s after=%s", topicUpdatedAt, after)
	}
	if after := loadCommentUpdatedAt(t, fixture, comment.ID); !after.Equal(commentUpdatedAt) {
		t.Fatalf("comment updated_at moved on reaction writes: before=%s after=%s", commentUpdatedAt, after)
	}
}

// TestPostgresReactionSelfLikeSkipsNotificationProjection 固定通知投影口径：
// 收件人是内容作者（与 ReactionService 解析一致），自己赞自己不留投影；
// 他人点赞恰好一条，且只在「这次真的建立了关系」时写。
func TestPostgresReactionSelfLikeSkipsNotificationProjection(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	writer := &recordingNotificationWriter{}
	store := NewPostgresStore(fixture.pool).WithCommentNotifications(writer)
	authorID := fixture.insertUser(t, "projection_author")
	otherID := fixture.insertUser(t, "projection_liker")
	topic := fixture.insertBareTopic(t, authorID, "projection-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "投影评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// 收件人按内容作者解析，与服务层同口径；同时验证作者确实是这两个 ID。
	topicAuthor := loadTopicAuthorUserID(t, fixture, topic.id)
	commentAuthor := loadCommentAuthorUserID(t, fixture, comment.ID)
	if topicAuthor != authorID || commentAuthor != authorID {
		t.Fatalf("fixture authors topic=%d comment=%d, want %d", topicAuthor, commentAuthor, authorID)
	}

	applyTopicReaction(t, store, authorID, topic.id, topicAuthor, true)
	applyCommentReaction(t, store, authorID, comment.ID, topic.id, commentAuthor, true)
	if len(writer.reactions) != 0 {
		t.Fatalf("self-like produced reaction notifications: %#v", writer.reactions)
	}

	applyTopicReaction(t, store, otherID, topic.id, topicAuthor, true)
	if len(writer.reactions) != 1 {
		t.Fatalf("other-user like produced %d reaction notifications, want 1", len(writer.reactions))
	}
	first := writer.reactions[0]
	if first.TargetType != ReactionTargetTopic || first.TargetID != topic.id || first.TopicID != topic.id {
		t.Fatalf("topic reaction notification target = %#v", first)
	}
	if first.RecipientUserID != topicAuthor || first.ActorUserID != otherID {
		t.Fatalf("topic reaction notification actor/recipient = %d/%d, want %d/%d", first.ActorUserID, first.RecipientUserID, otherID, topicAuthor)
	}

	// 幂等重复点赞不重复投影。
	applyTopicReaction(t, store, otherID, topic.id, topicAuthor, true)
	if len(writer.reactions) != 1 {
		t.Fatalf("idempotent like duplicated reaction notification: %#v", writer.reactions)
	}

	applyCommentReaction(t, store, otherID, comment.ID, topic.id, commentAuthor, true)
	if len(writer.reactions) != 2 {
		t.Fatalf("comment like produced %d notifications total, want 2", len(writer.reactions))
	}
	second := writer.reactions[1]
	if second.TargetType != ReactionTargetComment || second.TargetID != comment.ID || second.TopicID != topic.id {
		t.Fatalf("comment reaction notification target = %#v", second)
	}
	if second.RecipientUserID != commentAuthor || second.ActorUserID != otherID {
		t.Fatalf("comment reaction notification actor/recipient = %d/%d, want %d/%d", second.ActorUserID, second.RecipientUserID, otherID, commentAuthor)
	}

	// 取消点赞不产生投影。
	applyTopicReaction(t, store, otherID, topic.id, topicAuthor, false)
	applyCommentReaction(t, store, otherID, comment.ID, topic.id, commentAuthor, false)
	if len(writer.reactions) != 2 {
		t.Fatalf("unlike produced extra reaction notifications: %#v", writer.reactions)
	}
}

// reactionTarget 把「关系行定位」与「主题计数定位」放在一起：评论点赞的计数在
// comments 上，但 TopicID 仍指向宿主主题（通知与 hot_score 口径需要它）。
type reactionTarget struct {
	label      string
	targetType string
	targetID   int64
	topicID    int64
}

type topicReactionCounters struct {
	commentCount int64
	viewCount    int64
	likeCount    int64
	hotScore     int64
}

// formulaHotScore 是 hot_score.go 里 TopicHotScoreSQL 的算术等价形式。
func (c topicReactionCounters) formulaHotScore() int64 {
	return c.commentCount*5 + c.viewCount + c.likeCount*3
}

func applyTopicReaction(t *testing.T, store *PostgresStore, actorID, topicID, recipientID int64, add bool) ReactionState {
	t.Helper()
	state, err := store.ApplyReaction(context.Background(), ApplyReactionRecord{
		ActorUserID:     actorID,
		TargetType:      ReactionTargetTopic,
		TargetID:        topicID,
		TopicID:         topicID,
		RecipientUserID: recipientID,
		Reaction:        ReactionLike,
		Add:             add,
	})
	if err != nil {
		t.Fatalf("apply topic reaction actor=%d topic=%d add=%v: %v", actorID, topicID, add, err)
	}
	return state
}

func applyCommentReaction(t *testing.T, store *PostgresStore, actorID, commentID, topicID, recipientID int64, add bool) ReactionState {
	t.Helper()
	state, err := store.ApplyReaction(context.Background(), ApplyReactionRecord{
		ActorUserID:     actorID,
		TargetType:      ReactionTargetComment,
		TargetID:        commentID,
		TopicID:         topicID,
		RecipientUserID: recipientID,
		Reaction:        ReactionLike,
		Add:             add,
	})
	if err != nil {
		t.Fatalf("apply comment reaction actor=%d comment=%d add=%v: %v", actorID, commentID, add, err)
	}
	return state
}

// assertReactionStep 断言一次切换之后的四件事：返回状态自洽、返回的 likeCount
// 等于关系行数、库内计数列等于关系行数。
func assertReactionStep(t *testing.T, fixture *reactionsPGFixture, store *PostgresStore, actorID int64, target reactionTarget, add bool, wantRows int64) {
	t.Helper()
	var state ReactionState
	if target.targetType == ReactionTargetTopic {
		state = applyTopicReaction(t, store, actorID, target.targetID, 0, add)
	} else {
		state = applyCommentReaction(t, store, actorID, target.targetID, target.topicID, 0, add)
	}
	if state.TargetType != target.targetType || state.TargetID != target.targetID || state.Reaction != ReactionLike {
		t.Fatalf("%s state = %#v, want target %s/%d reaction like", target.label, state, target.targetType, target.targetID)
	}
	if state.Liked != add {
		t.Fatalf("%s liked=%v, want %v", target.label, state.Liked, add)
	}
	if state.LikeCount != wantRows {
		t.Fatalf("%s likeCount=%d, want %d", target.label, state.LikeCount, wantRows)
	}
	assertReactionRowCount(t, fixture, target.targetType, target.targetID, wantRows)
	if target.targetType == ReactionTargetTopic {
		assertTopicLikeCount(t, fixture, target.targetID, wantRows)
	} else {
		assertCommentLikeCount(t, fixture, target.targetID, wantRows)
	}
}

func assertReactionRowCount(t *testing.T, fixture *reactionsPGFixture, targetType string, targetID, want int64) {
	t.Helper()
	var rows int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT COUNT(*) FROM content_reactions WHERE target_type = $1 AND target_id = $2
	`, targetType, targetID).Scan(&rows); err != nil {
		t.Fatalf("count reaction rows for %s %d: %v", targetType, targetID, err)
	}
	if rows != want {
		t.Fatalf("content_reactions rows for %s %d = %d, want %d", targetType, targetID, rows, want)
	}
}

func loadTopicReactionCounters(t *testing.T, fixture *reactionsPGFixture, topicID int64) topicReactionCounters {
	t.Helper()
	var counters topicReactionCounters
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT comment_count, view_count, like_count, hot_score FROM topics WHERE id = $1
	`, topicID).Scan(&counters.commentCount, &counters.viewCount, &counters.likeCount, &counters.hotScore); err != nil {
		t.Fatalf("load topic counters %d: %v", topicID, err)
	}
	return counters
}

// assertTopicHotScore 同时钉住具体数值与公式重算值：hot_score 必须等于
// comment_count*5 + view_count + like_count*3，也必须等于调用方给的口径值。
func assertTopicHotScore(t *testing.T, fixture *reactionsPGFixture, topicID, want int64) {
	t.Helper()
	counters := loadTopicReactionCounters(t, fixture, topicID)
	if counters.hotScore != want {
		t.Fatalf("topic %d hot_score=%d, want %d (comment_count=%d view_count=%d like_count=%d)",
			topicID, counters.hotScore, want, counters.commentCount, counters.viewCount, counters.likeCount)
	}
	if formula := counters.formulaHotScore(); counters.hotScore != formula {
		t.Fatalf("topic %d hot_score=%d, formula comment_count*5+view_count+like_count*3=%d (comment_count=%d view_count=%d like_count=%d)",
			topicID, counters.hotScore, formula, counters.commentCount, counters.viewCount, counters.likeCount)
	}
}

func assertTopicLikeCount(t *testing.T, fixture *reactionsPGFixture, topicID, want int64) {
	t.Helper()
	if got := loadTopicReactionCounters(t, fixture, topicID).likeCount; got != want {
		t.Fatalf("topic %d like_count=%d, want %d", topicID, got, want)
	}
}

func assertCommentLikeCount(t *testing.T, fixture *reactionsPGFixture, commentID, want int64) {
	t.Helper()
	if got := loadCommentLikeCount(t, fixture, commentID); got != want {
		t.Fatalf("comment %d like_count=%d, want %d", commentID, got, want)
	}
}

func loadCommentLikeCount(t *testing.T, fixture *reactionsPGFixture, commentID int64) int64 {
	t.Helper()
	var likeCount int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT like_count FROM comments WHERE id = $1`, commentID).Scan(&likeCount); err != nil {
		t.Fatalf("load comment like_count %d: %v", commentID, err)
	}
	return likeCount
}

func loadTopicUpdatedAt(t *testing.T, fixture *reactionsPGFixture, topicID int64) time.Time {
	t.Helper()
	var updatedAt time.Time
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT updated_at FROM topics WHERE id = $1`, topicID).Scan(&updatedAt); err != nil {
		t.Fatalf("load topic updated_at %d: %v", topicID, err)
	}
	return updatedAt
}

func loadCommentUpdatedAt(t *testing.T, fixture *reactionsPGFixture, commentID int64) time.Time {
	t.Helper()
	var updatedAt time.Time
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT updated_at FROM comments WHERE id = $1`, commentID).Scan(&updatedAt); err != nil {
		t.Fatalf("load comment updated_at %d: %v", commentID, err)
	}
	return updatedAt
}

func loadTopicAuthorUserID(t *testing.T, fixture *reactionsPGFixture, topicID int64) int64 {
	t.Helper()
	var authorID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT author_user_id FROM topics WHERE id = $1`, topicID).Scan(&authorID); err != nil {
		t.Fatalf("load topic author %d: %v", topicID, err)
	}
	return authorID
}

func loadCommentAuthorUserID(t *testing.T, fixture *reactionsPGFixture, commentID int64) int64 {
	t.Helper()
	var authorID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT author_user_id FROM comments WHERE id = $1`, commentID).Scan(&authorID); err != nil {
		t.Fatalf("load comment author %d: %v", commentID, err)
	}
	return authorID
}

// insertReactionPreviewRow 直接写一条带 created_at 的点赞关系行，控制「最早优先」
// 的排序输入；ApplyReaction 的 now() 无法表达历史时刻。
func insertReactionPreviewRow(t *testing.T, fixture *reactionsPGFixture, userID int64, targetType string, targetID int64, createdAt time.Time) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO content_reactions (user_id, target_type, target_id, reaction, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, userID, targetType, targetID, ReactionLike, createdAt); err != nil {
		t.Fatalf("insert reaction row: %v", err)
	}
}

// setInactiveUser 把用户标记为非 active：预览必须排除停用用户。
func setInactiveUser(t *testing.T, fixture *reactionsPGFixture, userID int64) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE users SET status = 'banned' WHERE id = $1`, userID); err != nil {
		t.Fatalf("ban user: %v", err)
	}
}

// previewUserIDs 把预览解成 user id 序列，便于断言顺序。
func previewUserIDs(previews map[int64][]UserSummary, targetID int64) []int64 {
	ids := make([]int64, 0, len(previews[targetID]))
	for _, item := range previews[targetID] {
		ids = append(ids, item.ID)
	}
	return ids
}

func assertUserIDsEqual(t *testing.T, label string, got []int64, want ...int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

// TestPostgresReactionPreviewsLimitSelfFirstEarliestExcludeInactive 固定预览查询
// 的四个口径：每目标 ≤3、自己优先、其余最早、排除非 active 用户。
func TestPostgresReactionPreviewsLimitSelfFirstEarliestExcludeInactive(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "preview_author")
	viewerID := fixture.insertUser(t, "preview_viewer")
	inactiveID := fixture.insertUser(t, "preview_inactive")
	setInactiveUser(t, fixture, inactiveID)

	earliest := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	// 最早两位（earliest 顺序）+ viewer（更晚）+ 非 active 用户 + 超额的第 4 位。
	firstID := fixture.insertUser(t, "preview_first")
	secondID := fixture.insertUser(t, "preview_second")
	lateID := fixture.insertUser(t, "preview_late")
	topic := fixture.insertBareTopic(t, authorID, "preview-topic")

	insertReactionPreviewRow(t, fixture, viewerID, ReactionTargetTopic, topic.id, earliest.Add(3*time.Hour))
	insertReactionPreviewRow(t, fixture, firstID, ReactionTargetTopic, topic.id, earliest)
	insertReactionPreviewRow(t, fixture, secondID, ReactionTargetTopic, topic.id, earliest.Add(time.Hour))
	insertReactionPreviewRow(t, fixture, inactiveID, ReactionTargetTopic, topic.id, earliest.Add(30*time.Minute))
	insertReactionPreviewRow(t, fixture, lateID, ReactionTargetTopic, topic.id, earliest.Add(4*time.Hour))

	previews, err := store.ReactionPreviews(fixture.ctx, ReactionTargetTopic, []int64{topic.id}, viewerID, ReactionPreviewLimit)
	if err != nil {
		t.Fatalf("ReactionPreviews: %v", err)
	}
	assertUserIDsEqual(t, "topic preview (self first, then earliest, capped at 3, no inactive)",
		previewUserIDs(previews, topic.id), viewerID, firstID, secondID)

	// 同一批数据、匿名视角：没有自己优先，退化为最早优先。
	anonPreviews, err := store.ReactionPreviews(fixture.ctx, ReactionTargetTopic, []int64{topic.id}, 0, ReactionPreviewLimit)
	if err != nil {
		t.Fatalf("anonymous ReactionPreviews: %v", err)
	}
	assertUserIDsEqual(t, "anonymous preview (earliest first)", previewUserIDs(anonPreviews, topic.id), firstID, secondID, viewerID)
}

// TestPostgresReactionPreviewsBatchTopicsAndCommentsInOneQuery 固定主题+评论
// 混合 id 一次批量查询，且空目标 / 无点赞目标不出现在返回 map 里。
func TestPostgresReactionPreviewsBatchTopicsAndCommentsInOneQuery(t *testing.T) {
	fixture := newReactionsPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "batch_preview_author")
	likerID := fixture.insertUser(t, "batch_preview_liker")
	topic := fixture.insertBareTopic(t, authorID, "batch-preview-topic")
	comment, err := store.CreateComment(fixture.ctx, CreateCommentRecord{
		TopicID:      topic.id,
		AuthorUserID: authorID,
		Content:      renderedFixtureContent(t, "预览批量评论"),
		Status:       CommentStatusActive,
	})
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	earliest := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	insertReactionPreviewRow(t, fixture, likerID, ReactionTargetTopic, topic.id, earliest)
	insertReactionPreviewRow(t, fixture, likerID, ReactionTargetComment, comment.ID, earliest.Add(time.Minute))

	targetIDs := []int64{topic.id, comment.ID, 987654321} // 最后一个无点赞
	previews, err := store.ReactionPreviews(fixture.ctx, ReactionTargetComment, []int64{}, likerID, ReactionPreviewLimit)
	if err != nil {
		t.Fatalf("empty targetIDs: %v", err)
	}
	if len(previews) != 0 {
		t.Fatalf("empty targetIDs must return empty map, got %v", previews)
	}

	mixed, err := store.ReactionPreviews(fixture.ctx, ReactionTargetTopic, targetIDs, 0, ReactionPreviewLimit)
	if err != nil {
		t.Fatalf("mixed batch: %v", err)
	}
	// 混合批次里主题类型匹配、评论 id 不匹配 topic 类型：map 里只应有主题一个
	// 键（fixture 里 topic/comment 各自序列可能撞同一 target_id，因此按键数断言）。
	assertUserIDsEqual(t, "topic in mixed batch", previewUserIDs(mixed, topic.id), likerID)
	if len(mixed) != 1 {
		t.Fatalf("mixed batch must contain only the topic target: %v", mixed)
	}
	if _, ok := mixed[987654321]; ok {
		t.Fatalf("unliked target must be absent from map: %v", mixed)
	}

	commentPreviews, err := store.ReactionPreviews(fixture.ctx, ReactionTargetComment, targetIDs, likerID, ReactionPreviewLimit)
	if err != nil {
		t.Fatalf("comment batch: %v", err)
	}
	assertUserIDsEqual(t, "comment preview", previewUserIDs(commentPreviews, comment.ID), likerID)
	if len(commentPreviews) != 1 {
		t.Fatalf("comment batch must contain only the comment target: %v", commentPreviews)
	}
}
