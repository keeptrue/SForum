package notifications

// 徽章授予投影的真实 PostgreSQL 集成测试。
//
// 表结构由 scripts/test-api-isolated.sh 的迁移链建立（`202611100002` 播种
// badge_granted 的类型策略：in_app 开、email/web_push 关），这里不重抄 DDL。
//
// 这两条覆盖的是**通知行本身**，不是调用次数：
//   - 目标三件套（target_type='badge' + target_id + payload.badgeId）——收件箱
//     能不能点开徽章页全看它，而单测里 fake notifier 只能证明「有人调用了」；
//   - in_app 偏好关闭后不落行——`CreateProjectionsTx` 的渠道解析在这条类型上
//     也必须生效（徽章通知不是「必有站内行」的特例）。

import (
	"fmt"
	"testing"
	"time"
)

// TestNotifyBadgeGrantedTxWritesBadgeTarget 断言授予通知带着可解析的徽章目标。
func TestNotifyBadgeGrantedTxWritesBadgeTarget(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "badge_target")
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})
	event := BadgeGrantedEvent{
		RecipientUserID: userID,
		BadgeID:         4242,
		BadgeSlug:       fmt.Sprintf("veteran-%d", time.Now().UnixNano()),
		BadgeName:       "元老",
	}
	if err := outbox.NotifyBadgeGrantedTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	// 幂等：dedupe 键仍是 badge:{slug}，重复评估不得写第二行。
	if err := outbox.NotifyBadgeGrantedTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `
		SELECT target_type, target_id, payload->>'badgeId', payload->>'badgeName', type
		FROM notifications WHERE dedupe_key=$1`, "badge:"+event.BadgeSlug)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	var targetType, badgeIDKey, badgeName, typ string
	var targetID int64
	for rows.Next() {
		if err := rows.Scan(&targetType, &targetID, &badgeIDKey, &badgeName, &typ); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("notifications = %d, want 1 (dedupe 键 badge:{slug} 必须幂等)", count)
	}
	if typ != TypeBadgeGranted {
		t.Fatalf("type = %q, want %q", typ, TypeBadgeGranted)
	}
	if targetType != "badge" || targetID != event.BadgeID {
		t.Fatalf("target = %q/%d, want badge/%d", targetType, targetID, event.BadgeID)
	}
	if badgeIDKey != "4242" {
		t.Fatalf("payload.badgeId = %q, want 4242", badgeIDKey)
	}
	if badgeName != "元老" {
		t.Fatalf("payload.badgeName = %q, want 元老", badgeName)
	}
}

// TestNotifyBadgeGrantedTxWithoutBadgeIDSkipsTarget 断言拿不到 id 时不写死链目标。
//
// target_type 是空串、target_id 是 0：收件箱据此渲染成「无链接的纯文本通知」，
// 而不是把 id=0 交给目标解析器。
func TestNotifyBadgeGrantedTxWithoutBadgeIDSkipsTarget(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "badge_no_target")
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})
	event := BadgeGrantedEvent{
		RecipientUserID: userID,
		BadgeSlug:       fmt.Sprintf("legacy-%d", time.Now().UnixNano()),
		BadgeName:       "历史徽章",
	}
	if err := outbox.NotifyBadgeGrantedTx(ctx, tx, event); err != nil {
		t.Fatal(err)
	}
	var targetType string
	var targetID int64
	if err := tx.QueryRow(ctx, `SELECT target_type, target_id FROM notifications WHERE dedupe_key=$1`,
		"badge:"+event.BadgeSlug).Scan(&targetType, &targetID); err != nil {
		t.Fatal(err)
	}
	if targetType != "" || targetID != 0 {
		t.Fatalf("target = %q/%d, want empty/0 (缺 id 时不写目标)", targetType, targetID)
	}
}

// TestNotifyBadgeGrantedTxHonoursDisabledInApp 断言关掉站内渠道后不落通知行。
func TestNotifyBadgeGrantedTxHonoursDisabledInApp(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "badge_inapp_off")
	if _, err := tx.Exec(ctx, `
		UPDATE notification_type_policies
		SET enabled=FALSE, recommended_enabled=FALSE
		WHERE type=$1 AND channel='in_app'`, TypeBadgeGranted); err != nil {
		t.Fatal(err)
	}
	// 兜底：用户级偏好若存在也一并清掉，避免上一轮 fixture 留下 enabled=true。
	if _, err := tx.Exec(ctx, `DELETE FROM notification_preferences WHERE user_id=$1 AND type=$2`,
		userID, TypeBadgeGranted); err != nil {
		t.Fatal(err)
	}
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})
	slug := fmt.Sprintf("muted-%d", time.Now().UnixNano())
	if err := outbox.NotifyBadgeGrantedTx(ctx, tx, BadgeGrantedEvent{
		RecipientUserID: userID, BadgeID: 7, BadgeSlug: slug, BadgeName: "静音徽章",
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE dedupe_key=$1`, "badge:"+slug).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notifications = %d, want 0 (in_app 关闭时不得落行)", count)
	}
}

// TestNotifyBadgeGrantedTxSkipsInactiveRecipient 断言停用用户不产生通知行。
func TestNotifyBadgeGrantedTxSkipsInactiveRecipient(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "badge_inactive")
	if _, err := tx.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	outbox := NewOutbox(nil, newPostgresStore(tx), &channelProjectionJobs{})
	slug := fmt.Sprintf("inactive-%d", time.Now().UnixNano())
	if err := outbox.NotifyBadgeGrantedTx(ctx, tx, BadgeGrantedEvent{
		RecipientUserID: userID, BadgeID: 9, BadgeSlug: slug, BadgeName: "停用用户徽章",
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE dedupe_key=$1`, "badge:"+slug).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("notifications = %d, want 0 (停用用户不投影)", count)
	}
}
