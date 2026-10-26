package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

// seedTestSessions 在 fakeStore 里为某用户预置若干会话行，返回 sid 列表（按插入顺序）。
func seedTestSessions(store *fakeStore, userID int64, count int) []string {
	sids := make([]string, 0, count)
	now := time.Now().UTC()
	for i := 0; i < count; i++ {
		sid := "sid-" + string(rune('a'+i))
		store.sessions = append(store.sessions, fakeSessionRow{
			userID:     userID,
			sid:        sid,
			deviceName: "Device " + string(rune('A'+i)),
			createdAt:  now.Add(-time.Duration(count-i) * time.Hour),
			lastSeenAt: now.Add(-time.Duration(count-i) * time.Minute),
		})
		sids = append(sids, sid)
	}
	return sids
}

func TestListSessionsReturnsOnlyOwnActiveByDefault(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 用户 1 有 2 个活跃会话，用户 2 有 1 个活跃会话。
	user1SIDs := seedTestSessions(store, 1, 2)
	seedTestSessions(store, 2, 1)

	result, err := service.ListSessions(ctx, 1, user1SIDs[0], false, 1, 20)
	if err != nil {
		t.Fatalf("ListSessions returned error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 sessions for user 1, got %d", len(result.Items))
	}
	// isCurrent 标记正确。
	foundCurrent := false
	for _, item := range result.Items {
		if item.ID == user1SIDs[0] {
			foundCurrent = item.IsCurrent
		}
	}
	if !foundCurrent {
		t.Fatal("expected current session to be marked isCurrent")
	}
}

func TestListSessionsIncludesHistoryWhenRequested(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	sids := seedTestSessions(store, 1, 2)
	// 下线其中一个。
	if err := store.RevokeSession(ctx, 1, sids[0], RevokeReasonDevice); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}

	// 默认只活跃：应只剩 1 条。
	active, err := service.ListSessions(ctx, 1, "", false, 1, 20)
	if err != nil {
		t.Fatalf("ListSessions active returned error: %v", err)
	}
	if len(active.Items) != 1 {
		t.Fatalf("expected 1 active session, got %d", len(active.Items))
	}

	// 含历史：应有 2 条。
	history, err := service.ListSessions(ctx, 1, "", true, 1, 20)
	if err != nil {
		t.Fatalf("ListSessions history returned error: %v", err)
	}
	if len(history.Items) != 2 {
		t.Fatalf("expected 2 sessions including history, got %d", len(history.Items))
	}
}

func TestRevokeSessionMarksOwnSession(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	sids := seedTestSessions(store, 1, 2)

	if err := service.RevokeSession(ctx, 1, sids[0]); err != nil {
		t.Fatalf("RevokeSession returned error: %v", err)
	}

	revoked, _ := store.IsSessionRevoked(ctx, 1, sids[0])
	if !revoked {
		t.Fatal("expected session to be revoked")
	}
	// 另一个保持活跃。
	otherRevoked, _ := store.IsSessionRevoked(ctx, 1, sids[1])
	if otherRevoked {
		t.Fatal("expected other session to remain active")
	}
}

func TestRevokeSessionReturnsNotFoundForOthersSID(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 用户 2 的会话；用户 1 试图下线它。
	user2SIDs := seedTestSessions(store, 2, 1)

	err := service.RevokeSession(ctx, 1, user2SIDs[0])
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound when revoking another user's session, got %v", err)
	}
	// 用户 2 的会话应保持活跃（未被越权下线）。
	revoked, _ := store.IsSessionRevoked(ctx, 2, user2SIDs[0])
	if revoked {
		t.Fatal("expected other user's session to remain active (no cross-user revoke)")
	}
}

func TestRevokeOtherSessionsKeepsCurrentAndReturnsCount(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	sids := seedTestSessions(store, 1, 3)
	currentSID := sids[0]

	count, err := service.RevokeOtherSessions(ctx, 1, currentSID)
	if err != nil {
		t.Fatalf("RevokeOtherSessions returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 other sessions revoked, got %d", count)
	}
	// 当前会话保持活跃。
	currentRevoked, _ := store.IsSessionRevoked(ctx, 1, currentSID)
	if currentRevoked {
		t.Fatal("expected current session to remain active")
	}
	// 其余两个已下线。
	for _, sid := range sids[1:] {
		revoked, _ := store.IsSessionRevoked(ctx, 1, sid)
		if !revoked {
			t.Fatalf("expected session %s to be revoked", sid)
		}
	}
}

func TestEnforceMaxSessionsKicksOldestExcess(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 3 个会话，max=2，应踢掉最旧的 1 个。
	sids := seedTestSessions(store, 1, 3)
	// 让 sids[0] 最旧（seedTestSessions 已按顺序设置递增的 lastSeenAt）。
	// currentSID 取最新的 sids[2]，它一定保留。
	currentSID := sids[2]

	count, err := service.EnforceMaxSessions(ctx, 1, currentSID, 2)
	if err != nil {
		t.Fatalf("EnforceMaxSessions returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 session kicked, got %d", count)
	}
	// 最旧的（sids[0]）应被下线。
	oldestRevoked, _ := store.IsSessionRevoked(ctx, 1, sids[0])
	if !oldestRevoked {
		t.Fatal("expected oldest session to be kicked")
	}
	// 最新的两个保持活跃。
	for _, sid := range sids[1:] {
		revoked, _ := store.IsSessionRevoked(ctx, 1, sid)
		if revoked {
			t.Fatalf("expected session %s to remain active", sid)
		}
	}
}

// TestEnforceMaxSessionsNeverKicksCurrent 验证当前登录设备永不被踢：
// 即使把 currentSID 设为最旧的会话（lastSeenAt 最早），它也应保留，
// 而是从其余会话中踢出最旧的。
func TestEnforceMaxSessionsNeverKicksCurrent(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 3 个会话，max=2。把 currentSID 设为最旧的 sids[0]。
	sids := seedTestSessions(store, 1, 3)
	currentSID := sids[0]

	count, err := service.EnforceMaxSessions(ctx, 1, currentSID, 2)
	if err != nil {
		t.Fatalf("EnforceMaxSessions returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 session kicked, got %d", count)
	}
	// 当前会话（最旧）仍保留——这是本次修复的核心保证。
	currentRevoked, _ := store.IsSessionRevoked(ctx, 1, currentSID)
	if currentRevoked {
		t.Fatal("expected current session to NEVER be kicked even if oldest")
	}
	// 其余两个里应被踢掉一个（次旧的 sids[1]）。
	kickedCount := 0
	for _, sid := range sids[1:] {
		revoked, _ := store.IsSessionRevoked(ctx, 1, sid)
		if revoked {
			kickedCount++
		}
	}
	if kickedCount != 1 {
		t.Fatalf("expected 1 of the other sessions kicked, got %d", kickedCount)
	}
}

func TestNormalizeMaxDevices(t *testing.T) {
	cases := []struct {
		input int
		want  int
	}{
		{0, RecommendedMaxDevices},
		{-1, RecommendedMaxDevices},
		{1, 1},
		{5, 5},
		{20, 20},
		{21, RecommendedMaxDevices},
		{100, RecommendedMaxDevices},
	}
	for _, tc := range cases {
		got := NormalizeMaxDevices(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeMaxDevices(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// 确保 authsession.SessionRecordInput 字段与 identity 期望一致（编译期契约）。
func TestSessionRecordInputShape(t *testing.T) {
	_ = authsession.SessionRecordInput{
		UserID: 1, SID: "x", SessionHash: "h",
		DeviceName: "Chrome", Browser: "Chrome", OS: "macOS",
		UserAgentRaw: "ua", IPAddress: "1.2.3.4", IPPrefix: "1.2.3.*",
	}
	_ = context.Background()
}

// userManageActor 构造持有 user.manage 权限的管理员 actor。
func userManageActor(id int64) Actor {
	return Actor{
		ID: id, Status: UserStatusActive,
		Permissions: map[string]bool{PermissionUserManage: true},
	}
}

func TestAdminRevokeUserSessionsRequiresPermission(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	seedTestSessions(store, 2, 1)

	// 无 user.manage 的 actor 应被拒绝。
	_, err := service.AdminRevokeUserSessions(ctx, Actor{ID: 9, Permissions: map[string]bool{}}, 2)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected ErrPermissionDenied without user.manage, got %v", err)
	}
}

func TestClearUserClientIPsRequiresUserManage(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	store.users[2] = CurrentUser{ID: 2, Username: "target", Status: UserStatusActive}
	// 登记带 IP 的会话。
	_ = store.CreateSession(ctx, authsession.SessionRecordInput{
		UserID: 2, SID: "sid-clear-1", IPAddress: "203.0.113.9", IPPrefix: "203.0.113.*",
	})

	// 无 user.manage → 拒绝。
	_, err := service.ClearUserClientIPs(ctx, Actor{ID: 1, Status: UserStatusActive}, 2)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("expected permission denied, got %v", err)
	}

	result, err := service.ClearUserClientIPs(ctx, userManageActor(1), 2)
	if err != nil {
		t.Fatalf("ClearUserClientIPs: %v", err)
	}
	if result.SessionsCleared != 1 {
		t.Fatalf("expected 1 session cleared, got %+v", result)
	}
	for _, row := range store.sessions {
		if row.userID == 2 && (row.ipAddress != "" || row.ipPrefix != "") {
			t.Fatalf("expected session IPs cleared, got %+v", row)
		}
	}
}

func TestAdminRevokeUserSessionsDeniesSelf(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)
	seedTestSessions(store, 1, 1)

	// 管理员不能强制下线自己（应改用 logout）。
	_, err := service.AdminRevokeUserSessions(ctx, userManageActor(1), 1)
	if !errors.Is(err, ErrSelfSessionRevoke) {
		t.Fatalf("expected ErrSelfSessionRevoke, got %v", err)
	}
}

func TestAdminRevokeUserSessionsRevokesAllTargetDevices(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 目标用户 2 有 3 个活跃设备；用户 1 也有设备。
	seedTestSessions(store, 2, 3)
	seedTestSessions(store, 1, 2)
	// 让目标用户 2 存在（普通会员），否则 GetAdminUser 会报 user not found。
	store.users[2] = CurrentUser{ID: 2, Username: "target", Status: UserStatusActive}

	count, err := service.AdminRevokeUserSessions(ctx, userManageActor(1), 2)
	if err != nil {
		t.Fatalf("AdminRevokeUserSessions returned error: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 sessions revoked, got %d", count)
	}
	// 目标用户所有设备已下线。
	for _, row := range store.sessions {
		if row.userID == 2 && row.revokedAt == nil {
			t.Fatal("expected all target user sessions revoked")
		}
	}
	// 管理员自己的设备不受影响。
	for _, row := range store.sessions {
		if row.userID == 1 && row.revokedAt != nil {
			t.Fatal("expected admin's own sessions to remain active")
		}
	}
}

// TestAdminRevokeUserSessionsProtectsSuperAdmin 验证非超管管理员不能强制下线超管设备。
// 只有同样是 super_admin 的管理员才能操作超管目标（与 ReplaceUserRoles 的保护对称）。
func TestAdminRevokeUserSessionsProtectsSuperAdmin(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 目标用户 2 是 super_admin。
	seedTestSessions(store, 2, 2)
	store.users[2] = CurrentUser{ID: 2, Username: "sa", Status: UserStatusActive}
	store.userRoleIDs[2] = []int64{1} // roleID 1 = super_admin（newTestService 中 seed）

	// 非 super_admin 的 user.manage 管理员试图下线超管设备。
	_, err := service.AdminRevokeUserSessions(ctx, userManageActor(1), 2)
	if !errors.Is(err, ErrSuperAdminSessionLocked) {
		t.Fatalf("expected ErrSuperAdminSessionLocked, got %v", err)
	}
	// 超管设备未被下线。
	for _, row := range store.sessions {
		if row.userID == 2 && row.revokedAt != nil {
			t.Fatal("expected super admin sessions to remain active")
		}
	}

	// super_admin 管理员可以下线超管目标。
	superAdminActor := Actor{
		ID: 3, Status: UserStatusActive,
		RoleKeys: []string{RoleSuperAdmin},
		// super_admin 通过 IsSuperAdmin() 绕过 Permissions 检查。
	}
	count, err := service.AdminRevokeUserSessions(ctx, superAdminActor, 2)
	if err != nil {
		t.Fatalf("super admin should be able to revoke super admin sessions, got error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 sessions revoked by super admin, got %d", count)
	}
}

func TestCleanupRevokedSessionsDeletesOldHistory(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 一个活跃、一个刚下线、一个 60 天前下线。
	seedTestSessions(store, 1, 1)
	old := fakeSessionRow{userID: 1, sid: "old", createdAt: time.Now().AddDate(0, 0, -60)}
	oldRevoked := time.Now().AddDate(0, 0, -60)
	old.revokedAt = &oldRevoked
	store.sessions = append(store.sessions, old)
	recent := fakeSessionRow{userID: 1, sid: "recent", createdAt: time.Now().AddDate(0, 0, -1)}
	recentRevoked := time.Now().AddDate(0, 0, -1)
	recent.revokedAt = &recentRevoked
	store.sessions = append(store.sessions, recent)

	// 默认保留 30 天：应删除 60 天前的，保留最近 1 天的与活跃的。
	deleted, err := service.CleanupRevokedSessions(ctx, 30)
	if err != nil {
		t.Fatalf("CleanupRevokedSessions returned error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 old session deleted, got %d", deleted)
	}
	// 活跃会话与近期历史仍在。
	hasActive := false
	for _, row := range store.sessions {
		if row.revokedAt == nil {
			hasActive = true
		}
	}
	if !hasActive {
		t.Fatal("expected active session to be preserved")
	}
}

func TestHasKnownDeviceDetectsNewDevice(t *testing.T) {
	service, store := newTestService(t)
	ctx := testContext(t)

	// 用户 1 有一个已知 UA 的活跃会话。
	store.sessions = append(store.sessions, fakeSessionRow{
		userID: 1, sid: "s1", userAgentRaw: "Mozilla/5.0 Chrome", lastSeenAt: time.Now(),
	})

	known, err := service.HasKnownDevice(ctx, 1, "Mozilla/5.0 Chrome")
	if err != nil {
		t.Fatalf("HasKnownDevice returned error: %v", err)
	}
	if !known {
		t.Fatal("expected known device to be recognized")
	}

	// 新 UA 视为新设备。
	known, err = service.HasKnownDevice(ctx, 1, "Mozilla/5.0 Firefox")
	if err != nil {
		t.Fatalf("HasKnownDevice returned error: %v", err)
	}
	if known {
		t.Fatal("expected unknown UA to be treated as new device")
	}

	// 空指纹视为已知（跳过风险检查）。
	known, _ = service.HasKnownDevice(ctx, 1, "")
	if !known {
		t.Fatal("expected empty fingerprint to be treated as known (skip risk check)")
	}
}
