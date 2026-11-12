package badges

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 批量授予的单测：去重、上限、部分失败、已持有跳过、只允许手动徽章、权限。

// TestBatchGrantDeduplicatesAndSkipsHeld 断言重复 id 折叠、已持有算 skipped。
func TestBatchGrantDeduplicatesAndSkipsHeld(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	// 徽章 3（quality-answer）是手动徽章；用户 7 先手动持有一份。
	if _, err := store.GrantManual(ctx, GrantInput{UserID: 7, BadgeID: 3, GrantedBy: 3, GrantReason: "pre"}); err != nil {
		t.Fatal(err)
	}
	result, err := admin.BatchGrant(ctx, manageActor(), BatchGrantInput{
		BadgeID:   3,
		UserIDs:   []int64{7, 7, 8, 9, 0, -1, 8},
		Reason:    "  活动补发  ",
		GrantedBy: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 2 {
		t.Fatalf("granted = %d, want 2 (users 8 and 9)", result.Granted)
	}
	if result.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (user 7 already held it, duplicate ids collapsed)", result.Skipped)
	}
	if len(result.Failed) != 0 {
		t.Fatalf("failed = %v, want none", result.Failed)
	}
	// 授予行数 = 2 个新授予 + 1 个先前的预置授予。
	if len(store.lastGrants) != 3 {
		t.Fatalf("grant rows = %d, want 3", len(store.lastGrants))
	}
	// 理由被 trim 后写入（不做长度/内容校验，原样落审计）。
	for _, record := range store.lastGrants {
		if record.UserID != 7 && record.GrantReason != "活动补发" {
			t.Fatalf("user %d reason = %q, want 活动补发", record.UserID, record.GrantReason)
		}
	}
}

// TestBatchGrantRejectsInvalidInput 断言空列表与超上限都是 422。
func TestBatchGrantRejectsInvalidInput(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()

	if _, err := admin.BatchGrant(ctx, manageActor(), BatchGrantInput{BadgeID: 3, UserIDs: nil}); !errors.Is(err, ErrBadgeGrantBatchInvalid) {
		t.Fatalf("empty list: want ErrBadgeGrantBatchInvalid, got %v", err)
	}
	tooMany := make([]int64, 0, MaxBatchGrantUsers+1)
	for i := int64(1); i <= MaxBatchGrantUsers+1; i++ {
		tooMany = append(tooMany, i)
	}
	if _, err := admin.BatchGrant(ctx, manageActor(), BatchGrantInput{BadgeID: 3, UserIDs: tooMany}); !errors.Is(err, ErrBadgeGrantBatchInvalid) {
		t.Fatalf("over limit: want ErrBadgeGrantBatchInvalid, got %v", err)
	}
	// 全是非法 id 等价于空列表。
	if _, err := admin.BatchGrant(ctx, manageActor(), BatchGrantInput{BadgeID: 3, UserIDs: []int64{0, -3}}); !errors.Is(err, ErrBadgeGrantBatchInvalid) {
		t.Fatalf("all-invalid ids: want ErrBadgeGrantBatchInvalid, got %v", err)
	}
}

// TestBatchGrantRejectsAutomaticBadge 断言自动徽章不接受批量手动授予。
func TestBatchGrantRejectsAutomaticBadge(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	// 徽章 1 是自动徽章。
	if _, err := admin.BatchGrant(context.Background(), manageActor(), BatchGrantInput{BadgeID: 1, UserIDs: []int64{7}}); !errors.Is(err, ErrBadgeManualOnly) {
		t.Fatalf("want ErrBadgeManualOnly, got %v", err)
	}
}

// TestBatchGrantRecordsPartialFailure 断言单人失败被收集成 failed 行，其余照常授予。
func TestBatchGrantRecordsPartialFailure(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	store.usernames = map[int64]string{8: "alice"}
	store.failGrantFor = map[int64]error{8: ErrBadgeNotFound}

	result, err := admin.BatchGrant(context.Background(), manageActor(), BatchGrantInput{
		BadgeID: 3, UserIDs: []int64{8, 9}, GrantedBy: 3,
	})
	if err != nil {
		t.Fatalf("partial failure must not abort the batch: %v", err)
	}
	if result.Granted != 1 || result.Skipped != 0 {
		t.Fatalf("granted/skipped = %d/%d, want 1/0", result.Granted, result.Skipped)
	}
	if len(result.Failed) != 1 {
		t.Fatalf("failed = %v, want 1 row", result.Failed)
	}
	if result.Failed[0].UserID != 8 {
		t.Fatalf("failed user = %d, want 8", result.Failed[0].UserID)
	}
	// reason 是稳定错误码（可下发、可翻译），不是 err.Error() 的日志串。
	if result.Failed[0].Reason != CodeBadgeNotFound {
		t.Fatalf("reason = %q, want %q", result.Failed[0].Reason, CodeBadgeNotFound)
	}
	if result.Failed[0].Username != "alice" {
		t.Fatalf("username = %q, want alice", result.Failed[0].Username)
	}
}

// TestBatchGrantRequiresManagePermission 断言批量授予要 badge.manage。
func TestBatchGrantRequiresManagePermission(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	ctx := context.Background()
	for _, actor := range []identity.Actor{memberActor(), viewActor()} {
		if _, err := admin.BatchGrant(ctx, actor, BatchGrantInput{BadgeID: 3, UserIDs: []int64{7}}); !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("actor %d: want ErrPermissionDenied, got %v", actor.ID, err)
		}
	}
}

// TestSearchGrantCandidatesRequiresBadgeViewOnly 断言候选搜索只要 badge.view。
//
// 要求 user.view 会让「能管徽章但不能看用户目录」的运营角色用不了批量授予，
// 而它本来就是徽章功能的子操作。
func TestSearchGrantCandidatesRequiresBadgeViewOnly(t *testing.T) {
	store, _ := seedCatalog(t)
	store.candidates = []GrantCandidate{
		{UserID: 8, Username: "alice", DisplayName: "Alice", Status: "active"},
	}
	admin := NewAdminService(store)
	ctx := context.Background()

	items, err := admin.SearchGrantCandidates(ctx, viewActor(), GrantCandidateInput{Query: "ali", BadgeID: 3})
	if err != nil {
		t.Fatalf("badge.view must be enough: %v", err)
	}
	if len(items) != 1 || items[0].Username != "alice" {
		t.Fatalf("candidates = %v", items)
	}
	if _, err := admin.SearchGrantCandidates(ctx, memberActor(), GrantCandidateInput{}); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("member: want ErrPermissionDenied, got %v", err)
	}
}

// TestSearchGrantCandidatesClampsLimit 断言 limit 上限 20。
func TestSearchGrantCandidatesClampsLimit(t *testing.T) {
	store, _ := seedCatalog(t)
	admin := NewAdminService(store)
	candidates := make([]GrantCandidate, 0, 30)
	for i := int64(1); i <= 30; i++ {
		candidates = append(candidates, GrantCandidate{UserID: i, Username: "u", DisplayName: "U", Status: "active"})
	}
	store.candidates = candidates

	items, err := admin.SearchGrantCandidates(context.Background(), viewActor(), GrantCandidateInput{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) > MaxGrantCandidates {
		t.Fatalf("candidates = %d, want <= %d", len(items), MaxGrantCandidates)
	}
}

// TestEscapeLikePattern 断言 LIKE 元字符被转义（否则 "_" 会变成单字符通配）。
func TestEscapeLikePattern(t *testing.T) {
	cases := map[string]string{
		"alice": "alice",
		"a_b":   `a\_b`,
		"100%":  `100\%`,
		`a\b`:   `a\\b`,
		`\%_\`:  `\\\%\_\\`,
	}
	for input, want := range cases {
		if got := escapeLikePattern(input); got != want {
			t.Fatalf("escapeLikePattern(%q) = %q, want %q", input, got, want)
		}
	}
}
