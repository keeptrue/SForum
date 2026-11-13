package badges

import (
	"context"
	"errors"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 授予通知的三条运营路径（手动 / 批量 / 重算）与它们最容易错的四个地方：
//  1. 手动授予**必须**恰好 1 条（重复实现会变成「每枚徽章一条 + 每用户一条」）；
//  2. 批量授予只对**新授予**的人发（skipped 是「本来就持有」，发就是刷屏）；
//  3. 重算在 ≤200 条时逐条发、>200 条时整体不发（部分发送不可解释）；
//  4. 未装配通知出口时授予仍然成功（授予与通知解耦）。
//
// 这里用 fake notifier 断言「调用了几次、带了什么」，真实通知行的形状由
// `notifications` 包的投影测试与 bootstrap 的端到端测试覆盖。

type grantNotificationCall struct {
	userID  int64
	granted []GrantedBadge
}

type recordingGrantNotifier struct {
	calls []grantNotificationCall
	err   error
}

func (n *recordingGrantNotifier) NotifyBadgeGranted(_ context.Context, userID int64, granted []GrantedBadge) error {
	n.calls = append(n.calls, grantNotificationCall{userID: userID, granted: granted})
	return n.err
}

// seedNotifyCatalog 造一个「一枚自动 + 一枚手动」的最小目录。
func seedNotifyCatalog(t *testing.T) (*fakeStore, Badge, Badge) {
	t.Helper()
	store := newFakeStore()
	seven := int64(7)
	metricDays := MetricDaysVisited
	store.badges = []Badge{
		{ID: 1, Slug: "regular-visitor", Name: "常客", Tier: TierBronze,
			Icon: "i-lucide-calendar", GrantMode: GrantAutomatic,
			Metric: &metricDays, Threshold: &seven, Rule: builtinRule(MetricDaysVisited, 7),
			Enabled: true, Position: 0},
		{ID: 2, Slug: "quality-answer", Name: "优质解答", Tier: TierSilver,
			Icon: "i-lucide-circle-check-big", GrantMode: GrantManual,
			Enabled: true, Position: 1},
	}
	automatic, _ := store.GetBadge(context.Background(), 1)
	manual, _ := store.GetBadge(context.Background(), 2)
	return store, automatic, manual
}

// TestGrantNotifiesExactlyOnce 断言手动授予恰好发 1 条通知，且带徽章身份三件套。
func TestGrantNotifiesExactlyOnce(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	if err := admin.Grant(context.Background(), manageActor(), GrantInput{
		UserID: 7, BadgeID: manual.ID, GrantedBy: 9, GrantReason: "社区贡献",
	}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(notifier.calls))
	}
	call := notifier.calls[0]
	if call.userID != 7 {
		t.Fatalf("notify user = %d, want 7", call.userID)
	}
	if len(call.granted) != 1 {
		t.Fatalf("notify badges = %d, want 1", len(call.granted))
	}
	// id 是通知目标（TargetType="badge"）的唯一来源：只对 slug 会让收件箱失去链接。
	if call.granted[0].ID != manual.ID || call.granted[0].Slug != "quality-answer" || call.granted[0].Name != "优质解答" {
		t.Fatalf("notified badge = %+v, want id/slug/name of quality-answer", call.granted[0])
	}
}

// TestGrantSkipsNotificationWhenAlreadyGranted 断言重复授予不产生第二条通知。
func TestGrantSkipsNotificationWhenAlreadyGranted(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)
	input := GrantInput{UserID: 7, BadgeID: manual.ID, GrantedBy: 9}

	if err := admin.Grant(context.Background(), manageActor(), input); err != nil {
		t.Fatal(err)
	}
	if err := admin.Grant(context.Background(), manageActor(), input); !errors.Is(err, ErrBadgeAlreadyGranted) {
		t.Fatalf("second Grant err = %v, want ErrBadgeAlreadyGranted", err)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1 (已持有不得再发)", len(notifier.calls))
	}
}

// TestBatchGrantNotifiesOnlyNewGrants 断言批量授予只对真实新插入的人发通知。
//
// 50 人里 48 人新授予、2 人已持有 → 48 次调用、每次 1 枚。这个数字是运营
// 视角的验收口径（「48 新授予 = 48 条通知，skipped 不发」）。
func TestBatchGrantNotifiesOnlyNewGrants(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	userIDs := make([]int64, 0, MaxBatchGrantUsers)
	for i := int64(1); i <= MaxBatchGrantUsers; i++ {
		userIDs = append(userIDs, i)
	}
	// 前两人先持有一份，用来验 skipped 不发。
	store.insertGrant(1, manual.ID, 0, "")
	store.insertGrant(2, manual.ID, 0, "")

	result, err := admin.BatchGrant(context.Background(), manageActor(), BatchGrantInput{
		BadgeID: manual.ID, UserIDs: userIDs, GrantedBy: 9, Reason: "内测名单",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 48 || result.Skipped != 2 || len(result.Failed) != 0 {
		t.Fatalf("batch result = %+v, want granted=48 skipped=2 failed=0", result)
	}
	if len(notifier.calls) != 48 {
		t.Fatalf("notify calls = %d, want 48 (skipped 不发)", len(notifier.calls))
	}
	for _, call := range notifier.calls {
		if call.userID == 1 || call.userID == 2 {
			t.Fatalf("user %d 已持有，不该收到通知", call.userID)
		}
		if len(call.granted) != 1 || call.granted[0].ID != manual.ID {
			t.Fatalf("notify payload = %+v, want one quality-answer", call.granted)
		}
	}
}

// TestRecomputeNotifiesWithinLimit 断言 ≤200 条时逐条发，且 granted 计数不变。
func TestRecomputeNotifiesWithinLimit(t *testing.T) {
	store, automatic, _ := seedNotifyCatalog(t)
	store.activityCounters[7] = ActivityCounters{UserID: 7, DaysVisited: 30}
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	result, err := admin.Recompute(context.Background(), manageActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 1 {
		t.Fatalf("granted = %d, want 1", result.Granted)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(notifier.calls))
	}
	if notifier.calls[0].userID != 7 || notifier.calls[0].granted[0].ID != automatic.ID {
		t.Fatalf("notify call = %+v, want user 7 with %s", notifier.calls[0], automatic.Slug)
	}
	// 响应形状严格三键（这里顺带钉住「通知没往响应里加计数器」）。
	if result.Refreshed < 0 || result.Queued != 0 {
		t.Fatalf("recompute result = %+v, want refreshed>=0 queued=0", result)
	}
}

// TestRecomputeSkipsNotificationsOverLimit 断言 >200 条时整体不发，但计数照旧。
//
// 两侧都要：只验 ≤200 会把「阈值其实没生效」放过；只验 >200 会把「正常路径
// 也不发」放过。
func TestRecomputeSkipsNotificationsOverLimit(t *testing.T) {
	store, _, _ := seedNotifyCatalog(t)
	const total = recomputeNotifyLimit + 1
	for i := int64(1); i <= total; i++ {
		store.activityCounters[i] = ActivityCounters{UserID: i, DaysVisited: 30}
	}
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	result, err := admin.Recompute(context.Background(), manageActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != total {
		t.Fatalf("granted = %d, want %d (阈值只影响通知，不影响授予)", result.Granted, total)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("notify calls = %d, want 0 (超过 %d 条整体不发)", len(notifier.calls), recomputeNotifyLimit)
	}
}

// TestRecomputeCallsNotifierOncePerUser 断言重算按**用户**聚合出口调用次数。
//
// 注意它断言的不是「一条通知里带多枚」：投影侧仍是一枚徽章一条通知行
// （`badgeGrantedNotifier` 逐枚写、去重键 `badge:{slug}`）。这里钉住的是
// 「出口被调用的次数 = 受影响用户数」，把十万级重算的调用次数从「行数」降到
// 「人数」——改成一行一条通知会把收件箱语义换掉，不是同一条断言能描述的改动。
func TestRecomputeCallsNotifierOncePerUser(t *testing.T) {
	store := newFakeStore()
	one, seven := int64(1), int64(7)
	metricDays, metricTopic := MetricDaysVisited, MetricTopicCount
	store.badges = []Badge{
		{ID: 1, Slug: "regular-visitor", Name: "常客", Tier: TierBronze,
			Icon: "i-lucide-calendar", GrantMode: GrantAutomatic,
			Metric: &metricDays, Threshold: &seven, Rule: builtinRule(MetricDaysVisited, 7), Enabled: true, Position: 0},
		{ID: 2, Slug: "first-topic", Name: "初次发帖", Tier: TierBronze,
			Icon: "i-lucide-sprout", GrantMode: GrantAutomatic,
			Metric: &metricTopic, Threshold: &one, Rule: builtinRule(MetricTopicCount, 1), Enabled: true, Position: 0},
	}
	store.activityCounters[7] = ActivityCounters{UserID: 7, DaysVisited: 30, TopicCount: 3}
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	result, err := admin.Recompute(context.Background(), manageActor())
	if err != nil {
		t.Fatal(err)
	}
	if result.Granted != 2 {
		t.Fatalf("granted = %d, want 2", result.Granted)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1 (同一用户的多枚徽章聚合为一条)", len(notifier.calls))
	}
	if len(notifier.calls[0].granted) != 2 {
		t.Fatalf("aggregated badges = %d, want 2", len(notifier.calls[0].granted))
	}
}

// TestGrantSucceedsWithoutNotifier 断言未装配通知出口时授予照常成功。
//
// 这是装配顺序问题的护栏：`SetGrantNotifier` 在 bootstrap 里晚于 store 创建，
// 任何「忘了接」都不该让运营的授予请求变成 500。
func TestGrantSucceedsWithoutNotifier(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	admin := NewAdminService(store)
	if err := admin.Grant(context.Background(), manageActor(), GrantInput{UserID: 7, BadgeID: manual.ID, GrantedBy: 9}); err != nil {
		t.Fatalf("Grant without notifier err = %v, want nil", err)
	}
	if _, ok := store.grants[7][manual.ID]; !ok {
		t.Fatal("授予行必须落库")
	}
}

// TestGrantNotificationFailureDoesNotFailGrant 断言通知失败不回滚授予、不上抛错误。
func TestGrantNotificationFailureDoesNotFailGrant(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	notifier := &recordingGrantNotifier{err: errors.New("smtp down")}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	if err := admin.Grant(context.Background(), manageActor(), GrantInput{UserID: 7, BadgeID: manual.ID, GrantedBy: 9}); err != nil {
		t.Fatalf("Grant err = %v, want nil (通知失败不得回滚授予)", err)
	}
	if _, ok := store.grants[7][manual.ID]; !ok {
		t.Fatal("通知失败时授予行仍必须存在")
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(notifier.calls))
	}
}

// TestGrantNotifierRequiresManagePermission 断言没权限的人连通知都碰不到。
func TestGrantNotifierRequiresManagePermission(t *testing.T) {
	store, _, manual := seedNotifyCatalog(t)
	notifier := &recordingGrantNotifier{}
	admin := NewAdminService(store)
	admin.SetGrantNotifier(notifier)

	err := admin.Grant(context.Background(), identity.Actor{ID: 5, Permissions: map[string]bool{}}, GrantInput{UserID: 7, BadgeID: manual.ID})
	if !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("Grant err = %v, want ErrPermissionDenied", err)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("越权请求不得产生通知，得到 %d 次", len(notifier.calls))
	}
}
