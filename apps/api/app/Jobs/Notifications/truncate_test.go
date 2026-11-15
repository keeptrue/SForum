package notificationjobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

// 按字节切会把多字节字符劈成非法 UTF-8；PG 的 text 列直接拒写，终态写不进去，
// 记录又回到 sending —— 等于把 RV-50 的修复整个抵消。
func TestTruncateUTF8HonoursRuneBoundaries(t *testing.T) {
	// "中"=3 字节、"🚀"=4 字节，7 字节周期；160 mod 7 = 6，正好落在 emoji 中间。
	value := strings.Repeat("中🚀", 40)
	got := truncateUTF8(value, errorSummaryLimit)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated summary is not valid UTF-8: %q", got)
	}
	if len(got) > errorSummaryLimit {
		t.Fatalf("truncated summary length = %d bytes, want <= %d", len(got), errorSummaryLimit)
	}
	if !strings.HasPrefix(value, got) {
		t.Fatalf("truncation must keep a prefix, got %q", got)
	}
}

// 上游带坏字节时也要产出合法 UTF-8。
func TestTruncateUTF8RepairsInvalidInput(t *testing.T) {
	got := truncateUTF8("bad\xffbytes", errorSummaryLimit)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid input was not repaired: %q", got)
	}
}

// 端到端：最后一次调度写终态时，ErrorSummary 必须能落库（合法 UTF-8、长度受控）。
func TestDeliverMailSettlesWithValidUTF8ErrorSummary(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliverySending, Recipient: "u@example.com", AttemptCount: 4}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderReady},
		// 18 字节周期；160 mod 18 = 16，正好切在「败」字中间。
		Sender: fakeSender{err: errors.New(strings.Repeat("邮件发送失败", 40))},
	}
	if err := worker.Work(context.Background(), mailJob(5)); err != nil {
		t.Fatalf("final attempt must settle terminally: %v", err)
	}
	if store.update.Status != notifications.DeliveryFailed {
		t.Fatalf("status = %q, want failed", store.update.Status)
	}
	if !utf8.ValidString(store.update.ErrorSummary) {
		t.Fatalf("error summary is not valid UTF-8 (PG would reject the terminal write): %q", store.update.ErrorSummary)
	}
	if len(store.update.ErrorSummary) > errorSummaryLimit {
		t.Fatalf("error summary length = %d, want <= %d", len(store.update.ErrorSummary), errorSummaryLimit)
	}
}
