package hostapi

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/structpb"
	"strings"
	"testing"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 事实上报命令的拒绝/放行面。全部用值级断言：reason 是冻结字面量，审计的
// reason/factKey 也要逐字相等——只断言「有审计行」等于没断言。

type fakeBadgeFactRecorder struct {
	inputs []badges.FactInput
	result badges.FactResult
	err    error
}

func (r *fakeBadgeFactRecorder) RecordFact(_ context.Context, input badges.FactInput) (badges.FactResult, error) {
	r.inputs = append(r.inputs, input)
	if r.err != nil {
		return badges.FactResult{}, r.err
	}
	return r.result, nil
}

type fakeBadgeFactAuditor struct {
	events []protocolV2BadgeFactDeniedAudit
	err    error
}

func (a *fakeBadgeFactAuditor) AuditDenied(_ context.Context, event protocolV2BadgeFactDeniedAudit) error {
	a.events = append(a.events, event)
	return a.err
}

type fakeBadgeFactDeclarations struct {
	keys map[string][]string
	err  error
}

func (d fakeBadgeFactDeclarations) BadgeFactKeys(_ context.Context, extensionID string) ([]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.keys[extensionID], nil
}

func badgeFactRequest(t *testing.T, input map[string]any) *hostv2.CommandRequest {
	t.Helper()
	value, err := structpb.NewStruct(input)
	if err != nil {
		t.Fatal(err)
	}
	return &hostv2.CommandRequest{
		// service 模式：命令不接受 actor（actor 由 Host 签发的委派表达）。
		Context: &protocolv2.RequestContext{
			RequestId: "badge-fact", Locale: "zh-CN",
			Extension: &protocolv2.ExtensionIdentity{ExtensionId: "acme.wiki", ExtensionVersion: "1.0.0", ArtifactDigest: "digest"},
		},
		Input: &protocolv2.TypedDocument{
			SchemaId: CommandBadgesFactRecordInputSchema, SchemaVersion: CommandBadgesFactRecordSchemaVersion, Value: value,
		},
	}
}

func validBadgeFactInput() map[string]any {
	return map[string]any{
		"userId":  "7",
		"factKey": "acme.wiki.pages",
		"variant": "tutorial",
		"delta":   "3",
	}
}

func badgeFactDefinitions() fakeBadgeFactDeclarations {
	return fakeBadgeFactDeclarations{keys: map[string][]string{"acme.wiki": {"acme.wiki.pages"}}}
}

func prepareBadgeFact(t *testing.T, definition protocolV2CommandDefinition, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
	t.Helper()
	return definition.Prepare(context.Background(), nil, request)
}

// TestBadgeFactRecordRejectsWithFrozenReasons 覆盖冻结 reason 的每个触发条件。
//
// 每个用例都断言：协议错误 reason 逐字相等 + 恰好一条审计 + 审计 reason 与错误 reason 一致。
// 审计 reason 与错误 reason 不一致是最隐蔽的一种漂移：运维按审计查不到同一次拒绝。
func TestBadgeFactRecordRejectsWithFrozenReasons(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		reason string
	}{
		{"userId 缺失", func(in map[string]any) { delete(in, "userId") }, BadgeFactReasonInvalidUserID},
		{"userId 非十进制", func(in map[string]any) { in["userId"] = "7.5" }, BadgeFactReasonInvalidUserID},
		{"userId 为 0", func(in map[string]any) { in["userId"] = "0" }, BadgeFactReasonInvalidUserID},
		{"delta 缺失", func(in map[string]any) { delete(in, "delta") }, BadgeFactReasonInvalidDelta},
		{"delta 非十进制", func(in map[string]any) { in["delta"] = "3.0" }, BadgeFactReasonInvalidDelta},
		{"delta 为 0", func(in map[string]any) { in["delta"] = "0" }, BadgeFactReasonInvalidDelta},
		{"occurredAt 非 RFC3339", func(in map[string]any) { in["occurredAt"] = "2026-11-12 20:00" }, BadgeFactReasonInvalidOccurredAt},
		{"factKey 未声明", func(in map[string]any) { in["factKey"] = "acme.wiki.unlisted" }, BadgeFactReasonKeyNotDeclared},
		{"冒用他人 factKey", func(in map[string]any) { in["factKey"] = "other.plugin.pages" }, BadgeFactReasonKeyNotDeclared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auditor := &fakeBadgeFactAuditor{}
			recorder := &fakeBadgeFactRecorder{}
			definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, badgeFactDefinitions(), auditor)
			input := validBadgeFactInput()
			tc.mutate(input)

			_, err := prepareBadgeFact(t, definition, badgeFactRequest(t, input))
			commandErr, ok := err.(*protocolV2CommandError)
			if !ok {
				t.Fatalf("必须是协议错误，得到 %T %v", err, err)
			}
			if got := commandErr.detail.GetReason(); got != tc.reason {
				t.Fatalf("reason = %q，want %q", got, tc.reason)
			}
			if len(auditor.events) != 1 {
				t.Fatalf("域层拒绝必须留痕一次，得到 %+v", auditor.events)
			}
			if auditor.events[0].Reason != tc.reason {
				t.Fatalf("审计 reason = %q，必须与协议 reason %q 逐字相等", auditor.events[0].Reason, tc.reason)
			}
			if len(recorder.inputs) != 0 {
				t.Fatal("被拒绝的请求不得触达写口")
			}
		})
	}
}

// TestBadgeFactRecordAllowedPath 断言放行路径：写口拿到运行时身份、输出是字符串 total。
func TestBadgeFactRecordAllowedPath(t *testing.T) {
	auditor := &fakeBadgeFactAuditor{}
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 3}}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, badgeFactDefinitions(), auditor)

	prepared, err := prepareBadgeFact(t, definition, badgeFactRequest(t, validBadgeFactInput()))
	if err != nil {
		t.Fatalf("合法请求必须通过：%v", err)
	}
	execution, err := definition.Execute(context.Background(), nil, nil, prepared)
	if err != nil {
		t.Fatalf("执行失败：%v", err)
	}
	if len(auditor.events) != 0 {
		t.Fatalf("放行路径不应产生拒绝审计，得到 %+v", auditor.events)
	}
	if len(recorder.inputs) != 1 {
		t.Fatalf("写口必须被调用一次，得到 %+v", recorder.inputs)
	}
	got := recorder.inputs[0]
	if got.SourceExtensionID != "acme.wiki" {
		t.Fatalf("source 必须是运行时身份，得到 %q", got.SourceExtensionID)
	}
	if got.UserID != 7 || got.Delta != 3 || got.FactKey != "acme.wiki.pages" || got.Variant != "tutorial" {
		t.Fatalf("写口入参不对：%+v", got)
	}
	value := execution.Output.GetValue().AsMap()
	if value["inserted"] != true || value["total"] != "3" {
		t.Fatalf("输出必须是 {inserted:true,total:\"3\"}，得到 %v", value)
	}
}

// TestBadgeFactRecordDedupeIsNotAnError 断言 dedupe 命中是正常结果（spec §9 #3）。
func TestBadgeFactRecordDedupeIsNotAnError(t *testing.T) {
	auditor := &fakeBadgeFactAuditor{}
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: false, Total: 9}}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, badgeFactDefinitions(), auditor)

	input := validBadgeFactInput()
	input["dedupeKey"] = "event-1"
	prepared, err := prepareBadgeFact(t, definition, badgeFactRequest(t, input))
	if err != nil {
		t.Fatalf("dedupeKey 不该被拒绝：%v", err)
	}
	execution, err := definition.Execute(context.Background(), nil, nil, prepared)
	if err != nil {
		t.Fatalf("inserted:false 不是错误：%v", err)
	}
	if len(auditor.events) != 0 {
		t.Fatalf("幂等命中不是拒绝，不该留拒绝审计：%+v", auditor.events)
	}
	value := execution.Output.GetValue().AsMap()
	if value["inserted"] != false || value["total"] != "9" {
		t.Fatalf("输出必须是 {inserted:false,total:\"9\"}，得到 %v", value)
	}
	if recorder.inputs[0].DedupeKey != "event-1" {
		t.Fatalf("dedupeKey 必须透传到写口，得到 %q", recorder.inputs[0].DedupeKey)
	}
}

// TestBadgeFactRecordClampsNegativeTotal 断言累计为负时回执 0（与读取侧 GREATEST 一致）。
func TestBadgeFactRecordClampsNegativeTotal(t *testing.T) {
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: -4}}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, badgeFactDefinitions(), &fakeBadgeFactAuditor{})

	input := validBadgeFactInput()
	input["delta"] = "-5"
	prepared, err := prepareBadgeFact(t, definition, badgeFactRequest(t, input))
	if err != nil {
		t.Fatalf("负数 delta 必须允许（扣减）：%v", err)
	}
	execution, err := definition.Execute(context.Background(), nil, nil, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if got := execution.Output.GetValue().AsMap()["total"]; got != "0" {
		t.Fatalf("负数累计必须 clamp 成 0，得到 %v", got)
	}
}

// TestBadgeFactRecordKeepsInt64Precision 断言大数走字符串不丢精度。
func TestBadgeFactRecordKeepsInt64Precision(t *testing.T) {
	recorder := &fakeBadgeFactRecorder{result: badges.FactResult{Inserted: true, Total: 9223372036854775807}}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(recorder, badgeFactDefinitions(), &fakeBadgeFactAuditor{})

	input := validBadgeFactInput()
	input["delta"] = "9223372036854775807"
	prepared, err := prepareBadgeFact(t, definition, badgeFactRequest(t, input))
	if err != nil {
		t.Fatalf("int64 边界 delta 必须通过：%v", err)
	}
	execution, err := definition.Execute(context.Background(), nil, nil, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if got := execution.Output.GetValue().AsMap()["total"]; got != "9223372036854775807" {
		t.Fatalf("int64 边界必须逐字回显，得到 %v", got)
	}
	if recorder.inputs[0].Delta != 9223372036854775807 {
		t.Fatalf("delta 解析丢精度：%d", recorder.inputs[0].Delta)
	}
}

// TestBadgeFactRecordAuditFailureDoesNotChangeOutcome 断言审计写失败不改返回码。
func TestBadgeFactRecordAuditFailureDoesNotChangeOutcome(t *testing.T) {
	auditor := &fakeBadgeFactAuditor{err: errors.New("audit down")}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(&fakeBadgeFactRecorder{}, badgeFactDefinitions(), auditor)

	input := validBadgeFactInput()
	input["factKey"] = "acme.wiki.unlisted"
	_, err := prepareBadgeFact(t, definition, badgeFactRequest(t, input))
	commandErr, ok := err.(*protocolV2CommandError)
	if !ok {
		t.Fatalf("仍然必须是协议错误，得到 %T", err)
	}
	if got := commandErr.detail.GetReason(); got != BadgeFactReasonKeyNotDeclared {
		t.Fatalf("审计失败不得改变 reason，得到 %q", got)
	}
	if len(auditor.events) != 1 {
		t.Fatal("审计仍应被尝试一次")
	}
}

// TestBadgeFactRecordCapabilityGateOwnsDenial 是「能力缺失不落审计」的反向断言。
//
// 能力闸门在引擎里、位于事务之前（v2_command.go），本命令只声明 RequiredCapability；
// 因此命令自己的任何拒绝路径都**不许**产出 reason=capability_denied 的审计行——
// 否则「拒绝是否留痕」就变成每条命令各一套（spec §9 Errata 3 第 4 条）。
func TestBadgeFactRecordCapabilityGateOwnsDenial(t *testing.T) {
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(
		&fakeBadgeFactRecorder{}, badgeFactDefinitions(), &fakeBadgeFactAuditor{},
	)
	if definition.RequiredCapability != capabilities.BadgesFactWrite {
		t.Fatalf("RequiredCapability = %q，能力闸门必须由共享 gate 承担", definition.RequiredCapability)
	}
	for _, broken := range []map[string]any{
		{"factKey": "acme.wiki.unlisted", "userId": "7", "delta": "1"},
		{"userId": "-1", "factKey": "acme.wiki.pages", "delta": "1"},
	} {
		auditor := &fakeBadgeFactAuditor{}
		cmd := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(&fakeBadgeFactRecorder{}, badgeFactDefinitions(), auditor)
		_, _ = prepareBadgeFact(t, cmd, badgeFactRequest(t, broken))
		for _, event := range auditor.events {
			if event.Reason == BadgeFactReasonCapabilityDenied {
				t.Fatalf("能力拒绝必须由共享闸门产生，命令不得自审 capability_denied：%+v", event)
			}
		}
	}
}

// TestBadgeFactRecordFailsClosedWithoutDeclarations 断言声明读口缺失时不放行。
func TestBadgeFactRecordFailsClosedWithoutDeclarations(t *testing.T) {
	auditor := &fakeBadgeFactAuditor{}
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(&fakeBadgeFactRecorder{}, nil, auditor)
	_, err := prepareBadgeFact(t, definition, badgeFactRequest(t, validBadgeFactInput()))
	if commandErr, ok := err.(*protocolV2CommandError); !ok || commandErr.detail.GetReason() != BadgeFactReasonKeyNotDeclared {
		t.Fatalf("声明读口缺失必须 fail-closed 成 fact_key_not_declared，得到 %v", err)
	}
	if len(auditor.events) != 1 || auditor.events[0].Reason != BadgeFactReasonKeyNotDeclared {
		t.Fatalf("fail-closed 也要留痕，得到 %+v", auditor.events)
	}
}

// TestBadgeFactRecordPolicyIDVersusCommandID 并排钉住两套写法。
//
// 命令 ID 不带 `@N`（稳定标识，版本走 CommandVersion），PolicyId 带 `@1`（版本化
// 策略，审计与前端按它判定宿主承诺）。这俩并存是契约里最容易被「统一」掉的一处：
// 统一成不带 → 审计丢策略版本；统一成带 → 调用方拿 unknown command。
func TestBadgeFactRecordPolicyIDVersusCommandID(t *testing.T) {
	definition := newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(
		&fakeBadgeFactRecorder{}, badgeFactDefinitions(), &fakeBadgeFactAuditor{},
	)
	request := badgeFactRequest(t, validBadgeFactInput())
	// 真实调用方（SDK / 引擎）按定义的 ID 填请求；这里显式照做，避免把「定义 ID」
	// 与「请求上的 ID」混为一谈。
	request.CommandId = definition.ID
	request.CommandVersion = definition.Version
	if definition.ID != "badges.fact.record" || definition.Version != "1" {
		t.Fatalf("定义 ID/Version = %q/%q", definition.ID, definition.Version)
	}
	if request.GetCommandId() != "badges.fact.record" {
		t.Fatalf("命令 ID = %q，必须不带 @N", request.GetCommandId())
	}
	if strings.Contains(request.GetCommandId(), "@") {
		t.Fatalf("命令 ID 不得含 @：%q", request.GetCommandId())
	}

	prepared, err := prepareBadgeFact(t, definition, request)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(prepared.Policy) != 1 || prepared.Policy[0] == nil {
		t.Fatalf("preparation 必须带恰好一条策略，得到 %+v", prepared.Policy)
	}
	policy := prepared.Policy[0]
	if policy.GetPolicyId() != "sforum.badges.fact.record@1" {
		t.Fatalf("PolicyId = %q，必须带 @1", policy.GetPolicyId())
	}
	if !policy.GetAllowed() {
		t.Fatal("策略必须是 allowed（拒绝走 ErrorDetail.reason，不在这里）")
	}
	if policy.GetResourceId() != "acme.wiki.pages" {
		t.Fatalf("策略 ResourceId 必须是 factKey，得到 %q", policy.GetResourceId())
	}
	if policy.GetReason() == "" {
		t.Fatal("策略必须说明宿主承诺什么（审计靠它复盘）")
	}
	// 常量本身也钉住：改字面量等于改契约。
	if BadgeFactRecordPolicyID != "sforum.badges.fact.record@1" || CommandBadgesFactRecordID != "badges.fact.record" {
		t.Fatal("PolicyId/命令 ID 常量被改动")
	}
}
