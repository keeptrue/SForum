package pluginv2

import (
	"testing"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 纯函数级断言：只测 Request 构造，不碰真库、不起命令引擎。
//
// 这一层的价值全在**编码约定**上：int64 走十进制字符串、命令 ID 不带 `@N`、
// schema ref 带 `@1`、字段名与 Host 命令输入逐字一致。这些约定任何一条写反都不会
// 在编译期报错，只会在联调时变成「unknown command / 参数非法」的谜题。

func badgeFactHost() *Host {
	// Commands 非空是构造请求的前提（没有命令通道就没有 identity 化的 Host）。
	return &Host{
		Commands: &notificationCommandClientStub{},
		identity: &protocolwire.ExtensionIdentity{ExtensionId: "fixture.badges"},
		instance: "instance",
	}
}

func badgeFactDocumentValues(t *testing.T, input BadgeFactInput) map[string]any {
	t.Helper()
	request, err := badgeFactHost().RecordBadgeFactRequest(nil, input)
	if err != nil {
		t.Fatalf("构造请求失败：%v", err)
	}
	return TypedDocumentValues(request.GetInput())
}

// TestRecordBadgeFactRequestEncodesDecimals 断言 delta/userId 都是十进制字符串。
//
// 变异自检：把 `"delta": strconv.FormatInt(...)` 改成 float64 编码（或 `input.Delta`），
// 这条立刻红——那正是「大数在 2^53 之后被 double 静默改数字」的入口。
func TestRecordBadgeFactRequestEncodesDecimals(t *testing.T) {
	values := badgeFactDocumentValues(t, BadgeFactInput{
		UserID:  9223372036854775807,
		FactKey: "fixture.badges.task_count",
		Delta:   9223372036854775806,
	})
	if got, ok := values["delta"].(string); !ok || got != "9223372036854775806" {
		t.Fatalf("delta 必须是十进制字符串（int64 边界逐字），得到 %#v", values["delta"])
	}
	if got, ok := values["userId"].(string); !ok || got != "9223372036854775807" {
		t.Fatalf("userId 必须是十进制字符串（int64 边界逐字），得到 %#v", values["userId"])
	}

	// 负数：SDK 侧保留 `-`，**不 clamp**（clamp 只发生在 Host 回执与读取侧）。
	negative := badgeFactDocumentValues(t, BadgeFactInput{UserID: 7, FactKey: "fixture.badges.task_count", Delta: -5})
	if got, ok := negative["delta"].(string); !ok || got != "-5" {
		t.Fatalf("负数 delta 必须编码成 \"-5\"，得到 %#v", negative["delta"])
	}
}

// TestRecordBadgeFactRequestFieldNamesMatchHostContract 断言 wire 字段名逐字一致。
func TestRecordBadgeFactRequestFieldNamesMatchHostContract(t *testing.T) {
	values := badgeFactDocumentValues(t, BadgeFactInput{
		UserID: 7, FactKey: "fixture.badges.task_count", Variant: "tutorial",
		Delta: 3, DedupeKey: "event-1",
		OccurredAt: time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC),
	})
	for _, key := range []string{"userId", "factKey", "variant", "delta", "dedupeKey", "occurredAt"} {
		if _, present := values[key]; !present {
			t.Fatalf("缺少 wire 字段 %q，得到 %v", key, values)
		}
	}
	if values["occurredAt"] != "2026-11-12T20:00:00Z" {
		t.Fatalf("occurredAt 必须是 RFC3339（UTC），得到 %#v", values["occurredAt"])
	}
	// 可选项缺省时不写空串：让 Host 的「字段缺失」判定与「空串」判定各自成立。
	minimal := badgeFactDocumentValues(t, BadgeFactInput{UserID: 7, FactKey: "fixture.badges.task_count", Delta: 1})
	for _, key := range []string{"variant", "dedupeKey", "occurredAt"} {
		if _, present := minimal[key]; present {
			t.Fatalf("可选字段 %q 未提供时不该出现，得到 %v", key, minimal)
		}
	}
}

// TestRecordBadgeFactRequestIdentifiersVersions 断言 `@1` 只出现在 schema ref 上。
//
// 这是整个 SDK 最容易写反的一处：命令 ID 带 `@1` → Host 当 unknown command；
// schema ref 不带 `@1` → 文档版本解析失败。两条都只在联调时才炸。
func TestRecordBadgeFactRequestIdentifiersVersions(t *testing.T) {
	request, err := badgeFactHost().RecordBadgeFactRequest(nil, BadgeFactInput{
		UserID: 7, FactKey: "fixture.badges.task_count", Delta: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.GetCommandId() != "badges.fact.record" {
		t.Fatalf("命令 ID = %q，必须不带 @N", request.GetCommandId())
	}
	if request.GetCommandVersion() != "1" {
		t.Fatalf("命令版本 = %q", request.GetCommandVersion())
	}
	if got := request.GetInput().GetSchemaId(); got != "sforum.badges.fact.record.input" {
		t.Fatalf("schema id = %q", got)
	}
	if got := request.GetInput().GetSchemaVersion(); got != "1" {
		t.Fatalf("schema version = %q", got)
	}
	// 常量本身也不许漂移。
	if HostBadgeFactRecordCommandID != "badges.fact.record" || HostBadgeFactRecordInputSchemaRef != "sforum.badges.fact.record.input@1" {
		t.Fatal("SDK 常量与 Host 冻结契约不一致")
	}
}

// TestRecordBadgeFactRequestRejectsInvalidLocalInput 断言作者侧预检。
func TestRecordBadgeFactRequestRejectsInvalidLocalInput(t *testing.T) {
	cases := []struct {
		name  string
		input BadgeFactInput
	}{
		{"userId 非正", BadgeFactInput{UserID: 0, FactKey: "fixture.badges.task_count", Delta: 1}},
		{"factKey 空", BadgeFactInput{UserID: 7, Delta: 1}},
		{"factKey 含空白", BadgeFactInput{UserID: 7, FactKey: "fixture.badges .task_count", Delta: 1}},
		{"delta 为 0", BadgeFactInput{UserID: 7, FactKey: "fixture.badges.task_count"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := badgeFactHost().RecordBadgeFactRequest(nil, tc.input); err == nil {
				t.Fatal("必须拒绝，让必然失败的往返留在本地")
			}
		})
	}
}

// TestBadgeFactRecordResultReadsDecimalTotal 断言回执解析：total 是字符串、inserted 是 bool。
func TestBadgeFactRecordResultReadsDecimalTotal(t *testing.T) {
	document, err := NewTypedDocument(HostBadgeFactRecordResultSchemaRef, map[string]any{"inserted": false, "total": "9223372036854775807"})
	if err != nil {
		t.Fatal(err)
	}
	inserted, total, ok := BadgeFactRecordResult(&hostwire.CommandResult{Output: document})
	if !ok || inserted || total != 9223372036854775807 {
		t.Fatalf("解析结果 = %v/%d/%v", inserted, total, ok)
	}
	// 缺 total 必须报不可读，而不是当成 0：0 是合法累计值，两者不能混。
	broken, err := NewTypedDocument(HostBadgeFactRecordResultSchemaRef, map[string]any{"inserted": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := BadgeFactRecordResult(&hostwire.CommandResult{Output: broken}); ok {
		t.Fatal("缺 total 必须 ok=false")
	}
}
