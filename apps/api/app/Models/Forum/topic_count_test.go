package forum

import "testing"

// P2-5：categories.topic_count 的口径必须与公开列表过滤一致 —— **active + locked**。
//
// 这里的三条用例分别钉住三个曾经只判 active 的写路径：删除、编辑触发预审（requeue）、
// 换分类。每条都在同一步同时断言两个来源：
//   - 冗余计数 categories.topic_count（列表 total 就是读它）；
//   - 权威口径 COUNT(*) WHERE status IN ('active','locked')（列表过滤用的是它）。
//
// 只断言 ListTopics.Total 会变成自证（total 本身就来自 categories.topic_count），
// 所以权威 COUNT 是判据，total 是「用户看到的口径也一致」的第二层证据。

// ensureTopicLifecycleColumns 补齐 fixture 极简 topics 表缺的生命周期列：
// ApplyTopicAction 会写 locked_at / pinned_at。补在这里而不是改共享 fixture，
// 避免影响其它集成测试的形状。
func ensureTopicLifecycleColumns(t *testing.T, fixture *revisionLedgerPGFixture) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		ALTER TABLE topics ADD COLUMN IF NOT EXISTS locked_at TIMESTAMPTZ;
		ALTER TABLE topics ADD COLUMN IF NOT EXISTS pinned_at TIMESTAMPTZ;
	`); err != nil {
		t.Fatalf("prepare topic lifecycle columns: %v", err)
	}
}

// categoryTopicCount 读冗余计数。
func categoryTopicCount(t *testing.T, fixture *revisionLedgerPGFixture, slug string) int64 {
	t.Helper()
	var count int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT topic_count FROM categories WHERE slug = $1
	`, slug).Scan(&count); err != nil {
		t.Fatalf("read category topic_count %s: %v", slug, err)
	}
	return count
}

// publicTopicCount 是权威口径：公开可见 = active + locked。
func publicTopicCount(t *testing.T, fixture *revisionLedgerPGFixture, slug string) int64 {
	t.Helper()
	var count int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM topics
		JOIN categories ON categories.id = topics.category_id
		WHERE categories.slug = $1 AND topics.status IN ('active', 'locked')
	`, slug).Scan(&count); err != nil {
		t.Fatalf("count public topics in %s: %v", slug, err)
	}
	return count
}

// assertCategoryCountMatchesPublicList 断言两个来源一致，并把实际数字打进失败信息。
func assertCategoryCountMatchesPublicList(t *testing.T, fixture *revisionLedgerPGFixture, store *PostgresStore, slug string) {
	t.Helper()
	redundant := categoryTopicCount(t, fixture, slug)
	authoritative := publicTopicCount(t, fixture, slug)
	if redundant != authoritative {
		t.Fatalf("%s: categories.topic_count=%d 与公开主题数=%d 不一致（计数漂移）", slug, redundant, authoritative)
	}
	list, err := store.ListTopics(fixture.ctx, TopicListInput{Page: 1, PerPage: 20, CategorySlug: slug})
	if err != nil {
		t.Fatalf("ListTopics(%s): %v", slug, err)
	}
	if list.Total != authoritative {
		t.Fatalf("%s: ListTopics.total=%d 与公开主题数=%d 不一致", slug, list.Total, authoritative)
	}
}

func TestDeleteLockedTopicRollsBackCategoryCount(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "count_delete_author")
	topic := fixture.insertBareTopic(t, authorID, "locked-delete")
	ensureTopicLifecycleColumns(t, fixture)

	// 建好计数：active 主题 +1。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = topic_count + 1 WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 1 {
		t.Fatalf("seed topic_count=%d want 1", got)
	}

	// 锁定：locked 仍在公开口径内，计数不变。
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionLock}); err != nil {
		t.Fatalf("lock topic: %v", err)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")

	// 删除 locked 主题：必须回滚计数，否则永久虚高。
	if _, err := store.DeleteTopic(fixture.ctx, topic.id); err != nil {
		t.Fatalf("delete locked topic: %v", err)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")
	if got := categoryTopicCount(t, fixture, "general"); got != 0 {
		t.Fatalf("删除 locked 主题后 topic_count=%d want 0", got)
	}
}

func TestRequeueLockedTopicDecrementsCategoryCount(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "count_requeue_author")
	topic := fixture.insertBareTopic(t, authorID, "locked-requeue")
	ensureTopicLifecycleColumns(t, fixture)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = topic_count + 1 WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionLock}); err != nil {
		t.Fatalf("lock topic: %v", err)
	}

	// locked 主题被编辑触发预审：洗成 pending 时必须 -1，否则审核通过时再 +1 会双计。
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.id, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 0,
		Operation: RevisionOperationEdit, Origin: RevisionOriginStaff,
		Title: "locked requeue edited", Slug: "locked-requeue-edited",
		TagCreationMode: TagCreationModeControlled, RequeuePending: true,
		ModerationTriggers: []string{"link_spam"},
	}); err != nil {
		t.Fatalf("requeue locked topic: %v", err)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")
	if got := categoryTopicCount(t, fixture, "general"); got != 0 {
		t.Fatalf("locked 主题洗成 pending 后 topic_count=%d want 0", got)
	}

	// 审核通过回到 active：补回一次，正好回到 1（不是 2）。
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = topic_count + 1 WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE topics SET status = 'active' WHERE id = $1
	`, topic.id); err != nil {
		t.Fatal(err)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")
	if got := categoryTopicCount(t, fixture, "general"); got != 1 {
		t.Fatalf("审核通过后 topic_count=%d want 1（双计会变成 2）", got)
	}
}

func TestLockedTopicCategoryChangeMovesCategoryCount(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "count_move_author")
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO categories (slug, name, visibility) VALUES ('support', 'Support', 'public')
	`); err != nil {
		t.Fatal(err)
	}
	topic := fixture.insertBareTopic(t, authorID, "locked-move")
	ensureTopicLifecycleColumns(t, fixture)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = topic_count + 1 WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionLock}); err != nil {
		t.Fatalf("lock topic: %v", err)
	}

	// locked 主题换分类：计数必须跟着搬走，否则旧分类虚高、新分类漏计。
	if _, err := store.UpdateTopic(fixture.ctx, UpdateTopicRecord{
		TopicID: topic.id, EditorUserID: authorID, AuthorUserID: authorID, ExpectedRevision: 0,
		Operation: RevisionOperationEdit, Origin: RevisionOriginStaff,
		Title: "locked move edited", Slug: "locked-move-edited",
		CategorySlug: "support", TagCreationMode: TagCreationModeControlled,
	}); err != nil {
		t.Fatalf("move locked topic category: %v", err)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")
	assertCategoryCountMatchesPublicList(t, fixture, store, "support")
	if got := categoryTopicCount(t, fixture, "general"); got != 0 {
		t.Fatalf("换出后旧分类 topic_count=%d want 0", got)
	}
	if got := categoryTopicCount(t, fixture, "support"); got != 1 {
		t.Fatalf("换入后新分类 topic_count=%d want 1", got)
	}
}

// TestTopicLifecycleHideRestoreSyncsCategoryCount 固定「版主 hide/restore 也改变
// 公开可见性」这条口径（P2-5 补刀）。
//
// 之前 ApplyTopicAction 完全不碰 categories.topic_count：版主 hide 一个 active 主题
// 不减、restore 一个 hidden 主题不加，而举报处置那条路径（applyReportedTopicState）
// 是减的 —— 同一个「隐藏」动作两条路径两种结果。
//
// 变异要求：去掉 ApplyTopicAction 里的计数同步，前两条必须红。
func TestTopicLifecycleHideRestoreSyncsCategoryCount(t *testing.T) {
	fixture := newRevisionLedgerPGFixture(t)
	store := NewPostgresStore(fixture.pool)
	authorID := fixture.insertUser(t, "count_hide_author")
	topic := fixture.insertBareTopic(t, authorID, "hide-restore")
	ensureTopicLifecycleColumns(t, fixture)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = topic_count + 1 WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 1 {
		t.Fatalf("seed topic_count=%d want 1", got)
	}

	// hide(active) → 离开公开集：-1。
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionHide}); err != nil {
		t.Fatalf("hide active topic: %v", err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 0 {
		t.Fatalf("hide active 后 topic_count=%d want 0", got)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")

	// restore(hidden) → 回到公开集：+1。
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionRestore}); err != nil {
		t.Fatalf("restore hidden topic: %v", err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 1 {
		t.Fatalf("restore 后 topic_count=%d want 1", got)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")

	// lock/unlock 两端都在公开集：计数不变。
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionLock}); err != nil {
		t.Fatalf("lock topic: %v", err)
	}
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionUnlock}); err != nil {
		t.Fatalf("unlock topic: %v", err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 1 {
		t.Fatalf("lock/unlock 后 topic_count=%d want 1（两端都是公开状态）", got)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")

	// hide(pending) → 本来就不在公开集：计数不变（restore 到 pending 也不加，
	// 但 restore 只产出 active，这里覆盖 pending 被 hide 的一侧）。
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE topics SET status = 'pending' WHERE id = $1`, topic.id); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0) WHERE slug = 'general'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyTopicAction(fixture.ctx, TopicLifecycleInput{TopicID: topic.id, Action: TopicActionHide}); err != nil {
		t.Fatalf("hide pending topic: %v", err)
	}
	if got := categoryTopicCount(t, fixture, "general"); got != 0 {
		t.Fatalf("hide pending 后 topic_count=%d want 0（pending 从未计入）", got)
	}
	assertCategoryCountMatchesPublicList(t, fixture, store, "general")
}
