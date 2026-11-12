package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// badges.fact.record：插件向宿主上报一条「用户维度的事实」。
//
// 事实是徽章条件的数据来源，所以这条命令同时是**计数的唯一写口**：插件不能自己
// 记账（没有可信执行环境），只能通过这里上报，由宿主写 `badge_facts` + 累计计数
// 并把 `source_extension_id` 记成运行时身份。拒绝必须可追责——域层拒绝会尽力追加
// 一条审计（见 protocolV2AuditBadgeFactDenied），能力缺失则由共享闸门在事务前拒绝、
// **不落审计**（全仓统一，spec §9 Errata 3 第 4 条）。
const (
	CommandBadgesFactRecordID            = "badges.fact.record"
	CommandBadgesFactRecordVersion       = "1"
	CommandBadgesFactRecordInputSchema   = "sforum.badges.fact.record.input"
	CommandBadgesFactRecordOutputSchema  = "sforum.badges.fact.record.result"
	CommandBadgesFactRecordSchemaVersion = "1"

	// BadgeFactRecordDeniedAuditAction 是域层拒绝的审计 action。
	// 字面量冻结：文档（docs/extensions/badge-plugin-facts.md）、测试与运维查询都按它对齐。
	BadgeFactRecordDeniedAuditAction = "extension.badge_fact_record.denied"
)

// 冻结的拒绝原因（spec §9 Errata 3 第 2 条）。SDK/文档/测试逐字对齐这一组；
// 新增原因必须回到 spec 裁决，不许在实现里就地发明。
const (
	BadgeFactReasonCapabilityDenied  = "capability_denied"
	BadgeFactReasonKeyNotDeclared    = "fact_key_not_declared"
	BadgeFactReasonInvalidUserID     = "invalid_user_id"
	BadgeFactReasonInvalidDelta      = "invalid_delta"
	BadgeFactReasonInvalidOccurredAt = "invalid_occurred_at"
	BadgeFactReasonWriteFailed       = "fact_write_failed"
)

// ProtocolV2BadgeFactRecorder 是 `Models/Badges` 写口的窄接口（*PostgresStore 满足）。
//
// 命令**不自己写 SQL**：事实行与累计计数必须同事务，那份语义只有 `RecordFact`
// 一处维护；另写一份就会得到「窗口条件看得到、累计条件看不到」的分裂。
type ProtocolV2BadgeFactRecorder interface {
	RecordFact(ctx context.Context, input badges.FactInput) (badges.FactResult, error)
}

// ProtocolV2BadgeFactDeclarationReader 返回某扩展**自己声明**的 factKey。
//
// 为 nil 时 fail-closed（所有权无法证明 ⇒ 一律拒绝）：装配漏接不该变成「谁都能写」。
type ProtocolV2BadgeFactDeclarationReader interface {
	BadgeFactKeys(ctx context.Context, extensionID string) ([]string, error)
}

// protocolV2BadgeFactDeniedAudit 是一条拒绝审计。
type protocolV2BadgeFactDeniedAudit struct {
	ExtensionID string
	FactKey     string
	Reason      string
	UserID      int64
}

// protocolV2BadgeFactAuditor 追加拒绝审计。
//
// 抽成接口是为了让「审计写失败不得改变返回码」这条能被测试真的按失败注入，
// 而不是靠读代码相信。
type protocolV2BadgeFactAuditor interface {
	AuditDenied(ctx context.Context, event protocolV2BadgeFactDeniedAudit) error
}

// postgresBadgeFactAuditor 是生产实现：直接落 `audit_events`。
//
// 不用 `protocolV2CommandAudit` 那条路：它要求 tx + resolved scope + 幂等键 + 影响面，
// 而拒绝发生在**没有这些**的域层；用共享形状会把「拒绝」伪装成「已提交命令」。
type postgresBadgeFactAuditor struct {
	pool *pgxpool.Pool
}

func (a postgresBadgeFactAuditor) AuditDenied(ctx context.Context, event protocolV2BadgeFactDeniedAudit) error {
	if a.pool == nil {
		return errors.New("hostapi: badge fact audit pool is not wired")
	}
	metadata := map[string]any{
		"extensionId": event.ExtensionID,
		"factKey":     event.FactKey,
		"reason":      event.Reason,
	}
	if event.UserID > 0 {
		// BIGINT 走字符串：审计是给人看的 JSON，double 会在 2^53 之后悄悄改数字。
		metadata["userId"] = strconv.FormatInt(event.UserID, 10)
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode badge fact audit: %w", err)
	}
	if _, err := a.pool.Exec(ctx, `
		INSERT INTO audit_events (actor_user_id, action, metadata)
		VALUES (NULL, $1, $2::jsonb)
	`, BadgeFactRecordDeniedAuditAction, body); err != nil {
		return fmt.Errorf("insert badge fact audit: %w", err)
	}
	return nil
}

type protocolV2BadgeFactRecordInput struct {
	// UserID 是「这条事实记给谁」，十进制字符串（BIGINT 不过 double）。
	UserID     string `json:"userId"`
	FactKey    string `json:"factKey"`
	Variant    string `json:"variant"`
	Delta      string `json:"delta"`
	OccurredAt string `json:"occurredAt"`
	DedupeKey  string `json:"dedupeKey"`
}

type protocolV2PreparedBadgeFactRecord struct {
	extensionID string
	input       badges.FactInput
}

func newProtocolV2BadgeFactRecordCommandDefinition(
	pool *pgxpool.Pool,
	recorder ProtocolV2BadgeFactRecorder,
	declarations ProtocolV2BadgeFactDeclarationReader,
) protocolV2CommandDefinition {
	return newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(
		recorder, declarations, postgresBadgeFactAuditor{pool: pool},
	)
}

// newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor 是注入审计的构造点。
//
// 存在理由只有一个：「审计写失败不得改变返回码」这条必须能被**真实注入失败**地
// 测试。生产装配走上面的包装，审计实现固定为落 `audit_events`。
func newProtocolV2BadgeFactRecordCommandDefinitionWithAuditor(
	recorder ProtocolV2BadgeFactRecorder,
	declarations ProtocolV2BadgeFactDeclarationReader,
	auditor protocolV2BadgeFactAuditor,
) protocolV2CommandDefinition {
	definition := protocolV2CommandDefinition{
		ID: CommandBadgesFactRecordID, Version: CommandBadgesFactRecordVersion,
		InputSchemaID: CommandBadgesFactRecordInputSchema, InputSchemaVersion: CommandBadgesFactRecordSchemaVersion,
		OutputSchemaID: CommandBadgesFactRecordOutputSchema, OutputSchemaVersion: CommandBadgesFactRecordSchemaVersion,
		ActorMode:          protocolV2CommandActorService,
		RequiredCapability: capabilities.BadgesFactWrite,
	}
	definition.Preview = func(ctx context.Context, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		prepared, reason := protocolV2PrepareBadgeFactRecord(ctx, request, declarations, true)
		if reason != "" {
			protocolV2AuditBadgeFactDenied(ctx, auditor, request, prepared, reason)
			return nil, protocolV2BadgeFactRecordError(badges.NewBadgeFactWriteDenied(reason))
		}
		return protocolV2BadgeFactRecordPreparation(prepared), nil
	}
	definition.Prepare = func(ctx context.Context, _ pgx.Tx, request *hostv2.CommandRequest) (*protocolV2CommandPreparation, error) {
		prepared, reason := protocolV2PrepareBadgeFactRecord(ctx, request, declarations, false)
		if reason != "" {
			protocolV2AuditBadgeFactDenied(ctx, auditor, request, prepared, reason)
			return nil, protocolV2BadgeFactRecordError(badges.NewBadgeFactWriteDenied(reason))
		}
		return protocolV2BadgeFactRecordPreparation(prepared), nil
	}
	definition.Execute = func(ctx context.Context, _ pgx.Tx, _ *hostv2.CommandRequest, preparation *protocolV2CommandPreparation) (*protocolV2CommandExecution, error) {
		prepared, ok := preparation.private.(*protocolV2PreparedBadgeFactRecord)
		if !ok || prepared == nil {
			return nil, protocolV2BadgeFactRecordError(badges.NewBadgeFactWriteDenied(BadgeFactReasonWriteFailed))
		}
		if recorder == nil {
			// 装配漏接：拒绝而不是「假装成功」。0 行事实 + 成功回执是最坏的一种谎。
			protocolV2AuditBadgeFactDenied(ctx, auditor, nil, prepared, BadgeFactReasonWriteFailed)
			return nil, protocolV2BadgeFactRecordError(badges.NewBadgeFactWriteDenied(BadgeFactReasonWriteFailed))
		}
		result, err := recorder.RecordFact(ctx, prepared.input)
		if err != nil {
			// 唯一索引冲突已由 RecordFact 吞成 inserted=false，走到这里都是真错。
			protocolV2AuditBadgeFactDenied(ctx, auditor, nil, prepared, BadgeFactReasonWriteFailed)
			return nil, protocolV2BadgeFactRecordError(badges.NewBadgeFactWriteDenied(BadgeFactReasonWriteFailed))
		}
		output, err := protocolV2BadgeFactRecordDocument(result)
		if err != nil {
			return nil, err
		}
		return &protocolV2CommandExecution{Output: output}, nil
	}
	return definition
}

// protocolV2PrepareBadgeFactRecord 解输入并校验。返回的 reason 为空表示通过。
//
// preview 为 true 时不读声明目录：Preview 不该触发额外查询，而且预览要能回答
// 「这个请求形状对不对」，所有权在真正执行时才有意义。
func protocolV2PrepareBadgeFactRecord(
	ctx context.Context,
	request *hostv2.CommandRequest,
	declarations ProtocolV2BadgeFactDeclarationReader,
	preview bool,
) (*protocolV2PreparedBadgeFactRecord, string) {
	prepared := &protocolV2PreparedBadgeFactRecord{}
	if request == nil || request.GetContext() == nil || request.GetContext().GetExtension() == nil {
		return prepared, BadgeFactReasonWriteFailed
	}
	prepared.extensionID = strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId())
	input, err := decodeProtocolV2CommandInput[protocolV2BadgeFactRecordInput](request)
	if err != nil {
		return prepared, BadgeFactReasonInvalidDelta
	}
	prepared.input.Delta = 0
	prepared.input.FactKey = strings.TrimSpace(input.FactKey)
	prepared.input.Variant = strings.TrimSpace(input.Variant)
	prepared.input.DedupeKey = strings.TrimSpace(input.DedupeKey)

	userID, err := strconv.ParseInt(strings.TrimSpace(input.UserID), 10, 64)
	if err != nil || userID <= 0 {
		return prepared, BadgeFactReasonInvalidUserID
	}
	prepared.input.UserID = userID

	delta, err := strconv.ParseInt(strings.TrimSpace(input.Delta), 10, 64)
	if err != nil || delta == 0 {
		// 0 必须在域层变成可读拒绝：DB 的 `delta <> 0` CHECK 会让它变成 500。
		return prepared, BadgeFactReasonInvalidDelta
	}
	prepared.input.Delta = delta

	if raw := strings.TrimSpace(input.OccurredAt); raw != "" {
		occurredAt, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return prepared, BadgeFactReasonInvalidOccurredAt
		}
		prepared.input.OccurredAt = occurredAt
	}

	// sourceExtensionID 只认运行时身份：调用方自报会让「谁写的」变成可伪造字段。
	prepared.input.SourceExtensionID = prepared.extensionID

	if prepared.input.FactKey == "" {
		return prepared, BadgeFactReasonKeyNotDeclared
	}
	if preview {
		return prepared, ""
	}
	declared, err := protocolV2DeclaredBadgeFactKeys(ctx, declarations, prepared.extensionID)
	if err != nil {
		return prepared, BadgeFactReasonWriteFailed
	}
	// 所有权判定用「声明集合」而不是前缀：前缀只能证明命名空间，不能证明这条
	// 事实真被该扩展声明过——未声明的 key 写进去只会永远不满足，属于静默谎言。
	if _, ok := declared[prepared.input.FactKey]; !ok {
		return prepared, BadgeFactReasonKeyNotDeclared
	}
	return prepared, ""
}

func protocolV2DeclaredBadgeFactKeys(ctx context.Context, declarations ProtocolV2BadgeFactDeclarationReader, extensionID string) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	if declarations == nil || extensionID == "" {
		// fail-closed：无法证明所有权就不放行。
		return keys, nil
	}
	declared, err := declarations.BadgeFactKeys(ctx, extensionID)
	if err != nil {
		return nil, err
	}
	for _, key := range declared {
		keys[key] = struct{}{}
	}
	return keys, nil
}

// BadgeFactRecordPolicyID 是这条命令的**策略标识**。
//
// 注意它和命令 ID 的关系：命令 ID `badges.fact.record` **不带 `@N`**（稳定标识，
// 版本走 CommandVersion），而 PolicyId **带 `@N`**（策略是版本化契约，审计与
// 前端按 PolicyId 判定「宿主承诺了什么」）。两者并存不是笔误——把它们「统一」
// 成同一种写法，要么让命令变成 unknown command，要么让审计丢失策略版本。
const BadgeFactRecordPolicyID = "sforum.badges.fact.record@1"

func protocolV2BadgeFactRecordPreparation(prepared *protocolV2PreparedBadgeFactRecord) *protocolV2CommandPreparation {
	return &protocolV2CommandPreparation{
		// 策略面必须显式给出：宿主对这个命令的承诺是「按冻结契约校验所有权、
		// 主体、十进制字段与幂等键」，不是「默认放行」。缺它会让审计里没有策略
		// 版本，事后无法判断当时的规则是什么。
		Policy: []*hostv2.PolicyDecision{{
			PolicyId:   BadgeFactRecordPolicyID,
			ResourceId: prepared.input.FactKey,
			Allowed:    true,
			Reason:     "The Host validates exact extension ownership of the fact key, the acting user, decimal delta bounds, occurredAt shape, and dedupe idempotency at execution time.",
		}},
		Impact: []*hostv2.ImpactItem{{
			Module:       "badges",
			Action:       "record_fact",
			ResourceType: "badge_fact",
			ResourceId:   prepared.input.FactKey,
			Summary:      fmt.Sprintf("Record %d fact units for user %d.", prepared.input.Delta, prepared.input.UserID),
			Reversible:   false,
		}},
		private: prepared,
	}
}

// protocolV2AuditBadgeFactDenied 尽力追加拒绝审计：**写失败不改变返回码**。
//
// 拒绝仍然是拒绝；把审计失败升级成 500 会把「插件参数写错」变成「宿主坏了」，
// 反过来放行则更糟（该拒的没拒）。两者都不做。
func protocolV2AuditBadgeFactDenied(
	ctx context.Context,
	auditor protocolV2BadgeFactAuditor,
	request *hostv2.CommandRequest,
	prepared *protocolV2PreparedBadgeFactRecord,
	reason string,
) {
	if auditor == nil {
		return
	}
	event := protocolV2BadgeFactDeniedAudit{Reason: reason}
	if prepared != nil {
		event.ExtensionID = prepared.extensionID
		event.FactKey = prepared.input.FactKey
		event.UserID = prepared.input.UserID
	}
	if event.ExtensionID == "" && request != nil && request.GetContext() != nil && request.GetContext().GetExtension() != nil {
		event.ExtensionID = strings.TrimSpace(request.GetContext().GetExtension().GetExtensionId())
	}
	_ = auditor.AuditDenied(ctx, event)
}

func protocolV2BadgeFactRecordDocument(result badges.FactResult) (*protocolv2.TypedDocument, error) {
	// total 回 clamp 后的值（spec §8 Errata 2 第 6 条）：扣减到负数时回执为 0，
	// 与条件读取侧的 `GREATEST(total, 0)` 完全一致，避免两处给出两个数。
	total := result.Total
	if total < 0 {
		total = 0
	}
	return protocolV2Document(CommandBadgesFactRecordOutputSchema, CommandBadgesFactRecordSchemaVersion, map[string]any{
		"inserted": result.Inserted,
		"total":    strconv.FormatInt(total, 10),
	})
}

// protocolV2BadgeFactRecordError 把领域拒绝投影成协议错误。
//
// **拒绝只有一次构造**：全部走 `badges.NewBadgeFactWriteDenied(reason)`，这里从错误链
// 里取 reason。这样「顶层码 forum.badge_fact_write_denied」与「协议 reason」同源——
// 命令侧没有第二份字面量拼装，HTTP 侧的 403 映射也有真实生产者（不再是死代码）。
func protocolV2BadgeFactRecordError(denial error) error {
	reason := badges.BadgeFactWriteDenialReason(denial)
	code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	message := "The badge fact record request is invalid."
	switch reason {
	case BadgeFactReasonKeyNotDeclared:
		code, message = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "The fact key is not declared by this exact extension artifact."
	case BadgeFactReasonInvalidUserID:
		message = "userId must be a positive decimal integer string."
	case BadgeFactReasonInvalidDelta:
		message = "delta must be a non-zero decimal integer string."
	case BadgeFactReasonInvalidOccurredAt:
		message = "occurredAt must be an RFC3339 timestamp."
	case BadgeFactReasonWriteFailed:
		code, message = protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "The badge fact could not be recorded."
	}
	return newProtocolV2CommandError(code, reason, message, false)
}
