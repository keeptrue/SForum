package pluginv2

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 插件徽章事实上报的身份化助手。
//
// `badges.fact.record` 是**计数事实的唯一写口**：插件不能自己记账（宿主没有可信
// 执行环境），只能上报，由 Host 写 `badge_facts` + 累计计数，并把
// `source_extension_id` 记成运行时身份（调用方自报的扩展 id 一律不采信）。
//
// 与 Host 侧的边界值在这一层重复声明一次（SDK 不 import Host 实现）：命令 ID 的字面量、
// schema ref 与输入字段名两边各有一套常量，由两侧测试各自钉住——一边改了另一边不改，
// 至少有一条测试会红。
const (
	// HostBadgeFactRecordCommandID **不带 `@N`**：命令 id 是稳定标识，版本走
	// CommandVersion。`@1` 只出现在 PolicyId / schema ref 位置（最容易写反的一处）。
	HostBadgeFactRecordCommandID      = "badges.fact.record"
	HostBadgeFactRecordCommandVersion = "1"
	// HostBadgeFactRecordInputSchemaRef / ResultSchemaRef 带 `@1`。
	HostBadgeFactRecordInputSchemaRef  = "sforum.badges.fact.record.input@1"
	HostBadgeFactRecordResultSchemaRef = "sforum.badges.fact.record.result@1"

	// HostBadgeFactMaxFactKeyLen 与 `badge_facts.fact_key` 的 DB CHECK 上限一致
	// （2–121 字符）。SDK 侧只做本地预检，Host 仍然权威判定。
	HostBadgeFactMaxFactKeyLen = 121
	// HostBadgeFactMaxVariantLen 与 manifest `variants[].value` 的上限一致。
	HostBadgeFactMaxVariantLen = 64
	// HostBadgeFactMaxDedupeKeyLen 是幂等键上限（与 Host 命令的解析口径一致）。
	HostBadgeFactMaxDedupeKeyLen = 256
)

// ErrBadgeFactRequestInvalid 表示请求在作者侧就能判定为无效。
var ErrBadgeFactRequestInvalid = errors.New("protocol v2 badge fact record request is invalid")

// BadgeFactInput 是一次事实上报。
//
// **UserID 是必填主体**：事实是按用户的行，而命令的调用方是扩展运行身份
// （`SourceExtensionID`），不是被记账的人。不从 `parent` 上下文推导——插件的事实
// 多在异步 observe handler 里产生，那时没有终端用户请求可推。
//
// Delta 在 SDK 里是 `int64`，编码成**十进制字符串**：protobuf Struct 只有 double，
// 2^53 之后会静默改数字。负数表示扣减（累计到负时由 Host 回执 clamp 成 0）。
type BadgeFactInput struct {
	UserID     int64
	FactKey    string
	Variant    string
	Delta      int64
	OccurredAt time.Time
	DedupeKey  string
}

// RecordBadgeFactRequest 构造 actorless 的 `badges.fact.record` v1 请求。
//
// Host 仍是权威校验方：factKey 所有权、variant 是否已声明、delta 是否为 0 都在那里
// 重判；这里只把「必然失败」的请求留在本地。
func (h *Host) RecordBadgeFactRequest(parent *protocolwire.RequestContext, input BadgeFactInput) (*hostwire.CommandRequest, error) {
	if h == nil || h.Commands == nil {
		return nil, ErrBadgeFactRequestInvalid
	}
	input.FactKey = strings.TrimSpace(input.FactKey)
	input.Variant = strings.TrimSpace(input.Variant)
	input.DedupeKey = strings.TrimSpace(input.DedupeKey)
	if input.UserID <= 0 || input.FactKey == "" || len(input.FactKey) > HostBadgeFactMaxFactKeyLen ||
		strings.ContainsAny(input.FactKey, " \t\n") {
		return nil, ErrBadgeFactRequestInvalid
	}
	if len(input.Variant) > HostBadgeFactMaxVariantLen || len(input.DedupeKey) > HostBadgeFactMaxDedupeKeyLen {
		return nil, ErrBadgeFactRequestInvalid
	}
	if input.Delta == 0 {
		return nil, ErrBadgeFactRequestInvalid
	}
	values := map[string]any{
		"userId":  strconv.FormatInt(input.UserID, 10),
		"factKey": input.FactKey,
		// delta 也是十进制字符串：负数保留 `-`，**SDK 侧不 clamp**（clamp 属于
		// Host 回执与读取侧的语义，提前夹住会让「扣减」变成安静的无效操作）。
		"delta": strconv.FormatInt(input.Delta, 10),
	}
	if input.Variant != "" {
		values["variant"] = input.Variant
	}
	if input.DedupeKey != "" {
		values["dedupeKey"] = input.DedupeKey
	}
	if !input.OccurredAt.IsZero() {
		values["occurredAt"] = input.OccurredAt.UTC().Format(time.RFC3339)
	}
	document, err := NewTypedDocument(HostBadgeFactRecordInputSchemaRef, values)
	if err != nil {
		return nil, ErrBadgeFactRequestInvalid
	}
	requestContext := h.RequestContext(parent)
	if requestContext == nil {
		return nil, ErrBadgeFactRequestInvalid
	}
	// 幂等键只在给了 dedupeKey 时透传：没有幂等键的命令重复执行是合法语义
	// （两次真实发生 = 两次累加），套一个随机键反而会让 river 误判去重。
	if input.DedupeKey != "" {
		requestContext.IdempotencyKey = input.DedupeKey
	}
	return &hostwire.CommandRequest{
		Context:        requestContext,
		CommandId:      HostBadgeFactRecordCommandID,
		CommandVersion: HostBadgeFactRecordCommandVersion,
		IdempotencyKey: input.DedupeKey,
		Input:          document,
	}, nil
}

// RecordBadgeFact 执行一次事实写入。
//
// 调用前请确认：清单声明了 `badges.fact.write` 能力、`capabilities[]` 里也写了它、
// 且 factKey 在本扩展的 `badgeConditions[].key` 之内。
//
// 被拒绝的命令与其它命令族一样以正常 CommandResult 返回，原因在 `ErrorDetail.reason`
// （capability_denied / fact_key_not_declared / invalid_user_id / invalid_delta /
// invalid_occurred_at / fact_write_failed）。
//
// **`inserted:false` 不是错误**：同一个 `dedupeKey` 重放会命中唯一索引，回执
// `inserted:false` 且 `total` 不变。那正是「幂等重放」的正常结果。
func (h *Host) RecordBadgeFact(ctx context.Context, parent *protocolwire.RequestContext, input BadgeFactInput) (*hostwire.CommandResult, error) {
	request, err := h.RecordBadgeFactRequest(parent, input)
	if err != nil {
		return nil, err
	}
	return h.Commands.Execute(ctx, request)
}

// BadgeFactRecordValues 读取结果文档（`inserted` bool + `total` 十进制字符串）。
func BadgeFactRecordValues(result *hostwire.CommandResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	return TypedDocumentValues(result.GetOutput())
}

// BadgeFactRecordResult 解析回执。
//
// `ok=false` 表示文档缺字段或不是十进制字符串（协议漂移），调用方据此区分
// 「回执不可读」与「inserted=false」。total 在 Host 侧已 clamp 到 ≥0。
func BadgeFactRecordResult(result *hostwire.CommandResult) (inserted bool, total int64, ok bool) {
	values := BadgeFactRecordValues(result)
	rawInserted, present := values["inserted"].(bool)
	if !present {
		return false, 0, false
	}
	rawTotal, totalOK := decimalStringField(values, "total")
	if !totalOK {
		return false, 0, false
	}
	return rawInserted, int64(rawTotal), true
}
