package notifications

// 庆祝展示层的真实 PostgreSQL 集成测试。
//
// 表结构来自迁移链（`202611130002` 打 celebration 标记并建游标表），
// 相关策略/描述符由既有迁移播种，这里不重抄 DDL。
//
// 覆盖的六件事都是单测证明不了的：
//  1. 首访不回放历史（没有游标行 → initialized=false + 空 items + 种子游标）；
//  2. 游标是硬边界（游标前的行不返回，游标后的行返回）；
//  3. ack 用 GREATEST 语义（旧 cursorId 不会把游标退回去）；
//  4. 目标不可见整条丢弃（不是 targetPath=null 的死卡）；
//  5. wire 键集合与冻结契约逐字一致（含 null 语义与缺省语义）；
//  6. 插件类型走描述符文案、Core 类型走 i18n 键 + 实义兜底文本。

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// celebrationTargets 是可控的目标解析器：只记录被问过什么、返回固定结论。
type celebrationTargets struct {
	available bool
	path      string
	err       error
	asked     []int64
}

func (f *celebrationTargets) ResolveNotificationTarget(_ context.Context, _ int64, _ string, targetID int64) (bool, string, error) {
	f.asked = append(f.asked, targetID)
	if f.err != nil {
		return false, "", f.err
	}
	return f.available, f.path, nil
}

// insertCelebrationNotification 直接写一行通知（绕过投影，只测读面）。
func insertCelebrationNotification(t *testing.T, ctx context.Context, tx pgx.Tx, userID int64, typ string, payload map[string]any, targetType string, targetID int64) int64 {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO notifications (recipient_user_id, type, category, target_type, target_id, payload, dedupe_key)
		VALUES ($1,$2,'engagement',$3,$4,$5::jsonb,$6) RETURNING id`,
		userID, typ, targetType, targetID, body, fmt.Sprintf("celebration-test:%d:%d", stamp, time.Now().UnixMicro())).Scan(&id); err != nil {
		t.Fatalf("insert celebration notification: %v", err)
	}
	return id
}

// TestListCelebrationsFirstVisitSeedsCursorWithoutItems 断言首访不回放历史。
func TestListCelebrationsFirstVisitSeedsCursorWithoutItems(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_first")
	first := insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted, map[string]any{"badgeName": "元老"}, "badge", 11)
	_ = insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted, map[string]any{"badgeName": "常客"}, "badge", 12)
	store := newPostgresStore(tx)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/x?tab=badges"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Initialized {
		t.Fatal("没有游标行时 initialized 必须是 false（前端据此先 ack 再拉取）")
	}
	if len(feed.Items) != 0 {
		t.Fatalf("首访 items = %d, want 0（不回放历史）", len(feed.Items))
	}
	if feed.Total != 0 {
		t.Fatalf("首访 total = %d, want 0", feed.Total)
	}
	if feed.CursorID <= first {
		t.Fatalf("种子游标 = %d，应指向当前最大通知 id（> %d）", feed.CursorID, first)
	}
	// 首访不得写游标行：写了 initialized 就会变 true，前端那条分支永远走不到。
	if _, initialized, err := store.CelebrationCursor(ctx, userID); err != nil {
		t.Fatal(err)
	} else if initialized {
		t.Fatal("首访不得落游标行")
	}
}

// TestListCelebrationsRespectsCursorBoundary 断言游标是硬边界。
func TestListCelebrationsRespectsCursorBoundary(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_cursor")
	store := newPostgresStore(tx)
	before := insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted, map[string]any{"badgeName": "旧徽章"}, "badge", 21)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, before); err != nil {
		t.Fatal(err)
	}
	after := insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted, map[string]any{"badgeName": "新徽章"}, "badge", 22)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/x?tab=badges"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !feed.Initialized {
		t.Fatal("有游标行时 initialized 必须是 true")
	}
	if len(feed.Items) != 1 {
		t.Fatalf("items = %d, want 1（游标前的行不得返回）", len(feed.Items))
	}
	if feed.Items[0].ID != after {
		t.Fatalf("item id = %d, want %d（游标后的新行）", feed.Items[0].ID, after)
	}
	if feed.CursorID != before {
		t.Fatalf("cursorId = %d, want %d（读到的是已确认游标，不是最新 id）", feed.CursorID, before)
	}
}

// TestListCelebrationsCapsAtLimit 钉住「返回的是 id 升序前缀且条数 ≤ limit」。
//
// 客户端的 ack 正确性直接依赖这两条：它把**已消费的最大 id** 报回来（服务端 GREATEST
// 单调推进）。服务端一旦返回乱序或超过 limit 的集合，ack 就会把没消费的庆祝一并跳过。
// 造 5 条候选、limit=3：只允许 id 最小的 3 条，且 total 仍按独立 COUNT 给出（5）。
func TestListCelebrationsCapsAtLimit(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_limit")
	store := newPostgresStore(tx)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}

	ids := make([]int64, 0, 5)
	for i := 0; i < 5; i++ {
		ids = append(ids, insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted,
			map[string]any{"badgeName": fmt.Sprintf("徽章%d", i)}, "badge", int64(100+i)))
	}

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/x?tab=badges"}, userID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 3 {
		t.Fatalf("items = %d, want 3（不得超过 limit）", len(feed.Items))
	}
	for index, item := range feed.Items {
		if item.ID != ids[index] {
			t.Fatalf("items[%d].id = %d, want %d（必须是 id 升序前缀，ack 依赖这个顺序）", index, item.ID, ids[index])
		}
	}
	if feed.Total != 5 {
		t.Fatalf("total = %d, want 5（COUNT 不受 limit 影响）", feed.Total)
	}
	if feed.CursorID != 0 {
		t.Fatalf("cursorId = %d, want 0（读面不得顺手推进游标）", feed.CursorID)
	}
}

// TestAcknowledgeCelebrationsNeverRewinds 断言 GREATEST 语义：给旧游标不回退。
func TestAcknowledgeCelebrationsNeverRewinds(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_ack")
	store := newPostgresStore(tx)

	stored, err := AcknowledgeCelebrations(ctx, store, userID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 50 {
		t.Fatalf("stored = %d, want 50", stored)
	}
	stored, err = AcknowledgeCelebrations(ctx, store, userID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 50 {
		t.Fatalf("旧游标把游标退回去了：stored = %d, want 50", stored)
	}
	stored, err = AcknowledgeCelebrations(ctx, store, userID, 80)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 80 {
		t.Fatalf("前进失败：stored = %d, want 80", stored)
	}
	// 负游标钳到 0，且不倒退。
	stored, err = AcknowledgeCelebrations(ctx, store, userID, -5)
	if err != nil {
		t.Fatal(err)
	}
	if stored != 80 {
		t.Fatalf("负游标不得倒退：stored = %d, want 80", stored)
	}
}

// TestListCelebrationsDropsUnavailableTargets 断言目标不可见时整条丢弃。
func TestListCelebrationsDropsUnavailableTargets(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_invisible")
	store := newPostgresStore(tx)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}
	insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted, map[string]any{"badgeName": "被收回的徽章"}, "badge", 31)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: false}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("目标不可见时必须整条丢弃，得到 %d 条", len(feed.Items))
	}
	// 反向：目标可见时同一条必须出现（否则「丢弃」可能只是「压根没读到」）。
	feed, err = ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/x?tab=badges"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("目标可见时 items = %d, want 1", len(feed.Items))
	}
}

// TestCelebrationWireShapeMatchesFrozenContract 断言 wire 键集合与冻结形状逐字一致。
//
// 这条**不判结构体字段**，判的是真实序列化结果：字段名拼错、omitempty 漏写、
// null 变成缺省，都会在这里红。
func TestCelebrationWireShapeMatchesFrozenContract(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_wire")
	store := newPostgresStore(tx)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}
	insertCelebrationNotification(t, ctx, tx, userID, TypeBadgeGranted,
		map[string]any{"badgeName": "元老", "badgeSlug": "veteran", "badgeId": 41}, "badge", 41)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/wire?tab=badges"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatal(err)
	}
	assertExactKeys(t, "feed", top, []string{"initialized", "cursorId", "total", "items"})

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(top["items"], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	assertExactKeys(t, "item", items[0], []string{
		"id", "type", "icon", "label", "body", "titleKey", "bodyKey", "params", "targetPath", "createdAt",
	})

	// targetPath 只要出现就必须是可用绝对路径（禁止 null 死卡）。
	var targetPath string
	if err := json.Unmarshal(items[0]["targetPath"], &targetPath); err != nil {
		t.Fatalf("targetPath 必须是字符串：%s", items[0]["targetPath"])
	}
	if !strings.HasPrefix(targetPath, "/") {
		t.Fatalf("targetPath = %q, want 绝对路径", targetPath)
	}

	var titleKey, bodyKey string
	if err := json.Unmarshal(items[0]["titleKey"], &titleKey); err != nil {
		t.Fatalf("core 类型 titleKey 必须是字符串：%s", items[0]["titleKey"])
	}
	if titleKey != "celebrations.badge_granted.title" {
		t.Fatalf("titleKey = %q", titleKey)
	}
	if err := json.Unmarshal(items[0]["bodyKey"], &bodyKey); err != nil {
		t.Fatalf("core 类型 bodyKey 必须是字符串：%s", items[0]["bodyKey"])
	}
	if bodyKey != "celebrations.badge_granted.body" {
		t.Fatalf("bodyKey = %q", bodyKey)
	}

	var params map[string]string
	if err := json.Unmarshal(items[0]["params"], &params); err != nil {
		t.Fatal(err)
	}
	if params["badgeName"] != "元老" {
		t.Fatalf("params.badgeName = %q, want 元老", params["badgeName"])
	}
	// slug 是庆祝卡取「当前」徽章资料的唯一句柄（前端去 /profiles/{me}/badges 查）。
	if params["badgeSlug"] != "veteran" {
		t.Fatalf("params.badgeSlug = %q, want veteran", params["badgeSlug"])
	}
	// 白名单纪律：内部主键不透传。
	if _, exists := params["badgeId"]; exists {
		t.Fatal("params 不得透传 badgeId（对外一律用 slug）")
	}

	// Core 兜底文案必须是实义文本（不是 "Notification"、不是空串）。
	var label, body CelebrationText
	if err := json.Unmarshal(items[0]["label"], &label); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(items[0]["body"], &body); err != nil {
		t.Fatal(err)
	}
	if label.Zh != "徽章" || label.En != "Badge" {
		t.Fatalf("label = %+v, want 徽章/Badge", label)
	}
	if body.Zh == "" || body.En == "" || body.Zh == "Notification" || body.En == "Notification" {
		t.Fatalf("body = %+v, want 实义兜底文案", body)
	}
	if body.En == body.Zh {
		t.Fatalf("body 两种语言不得相同：%+v", body)
	}
}

// TestPluginCelebrationUsesDescriptorText 断言插件类型走描述符文案、titleKey 为 null。
func TestPluginCelebrationUsesDescriptorText(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_plugin")
	store := newPostgresStore(tx)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	typeID := fmt.Sprintf("sforum.test.milestone-%d", stamp)
	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_type_descriptors (
		  type, contract_version, payload_version, owner_extension_id, owner_artifact_digest,
		  category, presentation, target_contract, active
		) VALUES ($1,1,1,'sforum.test',$2,'engagement',$3::jsonb,'{"kind":"none"}'::jsonb,TRUE)`,
		typeID, strings.Repeat("a", 64), `{"icon":"i-lucide-trophy","celebration":true,`+
			`"label":{"zh-CN":"里程碑","en-US":"Milestone"},`+
			`"body":{"zh-CN":"你达成了新的里程碑","en-US":"You reached a new milestone"}}`); err != nil {
		t.Fatalf("insert plugin descriptor: %v", err)
	}
	insertCelebrationNotification(t, ctx, tx, userID, typeID, map[string]any{"ignored": true}, "badge", 51)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/u/x?tab=badges"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 1 {
		t.Fatalf("items = %d, want 1（插件类型也必须能庆祝）", len(feed.Items))
	}
	item := feed.Items[0]
	if item.TitleKey != nil || item.BodyKey != nil {
		t.Fatalf("插件类型 titleKey/bodyKey 必须为 null，得到 %v/%v", item.TitleKey, item.BodyKey)
	}
	if item.Label.Zh != "里程碑" || item.Label.En != "Milestone" {
		t.Fatalf("label = %+v, want 描述符文案", item.Label)
	}
	if item.Body.Zh != "你达成了新的里程碑" || item.Body.En != "You reached a new milestone" {
		t.Fatalf("body = %+v, want 描述符文案", item.Body)
	}
	// 插件的 payload 不透传：庆祝层只认已知参数键。
	if _, exists := item.Params["ignored"]; exists {
		t.Fatal("庆祝层不得透传插件 payload 字段")
	}
}

// TestNonCelebrationTypesAreExcluded 断言没打 celebration 的类型不进候选。
func TestNonCelebrationTypesAreExcluded(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "celebration_excluded")
	store := newPostgresStore(tx)
	if _, err := store.AcknowledgeCelebrationCursor(ctx, userID, 0); err != nil {
		t.Fatal(err)
	}
	insertCelebrationNotification(t, ctx, tx, userID, TypeReaction, map[string]any{}, "topic", 61)

	feed, err := ListCelebrations(ctx, store, &celebrationTargets{available: true, path: "/t/61"}, userID, DefaultCelebrationLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Items) != 0 || feed.Total != 0 {
		t.Fatalf("非庆祝类型不得出现：items=%d total=%d", len(feed.Items), feed.Total)
	}
}

// assertExactKeys 断言键集合完全相等（多一个、少一个都红）。
func assertExactKeys(t *testing.T, label string, value map[string]json.RawMessage, want []string) {
	t.Helper()
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	expected := append([]string(nil), want...)
	sort.Strings(expected)
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("%s keys = %v, want %v", label, got, expected)
	}
}
